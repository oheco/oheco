package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func response(code int, body []byte, headers http.Header) *http.Response {
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Header: headers, Body: io.NopCloser(bytes.NewReader(body))}
}

func expireIndexCheck(t *testing.T, m *Manager) {
	ageIndexCheck(t, m, 31*time.Minute)
}

func ageIndexCheck(t *testing.T, m *Manager, age time.Duration) {
	t.Helper()
	filename := filepath.Join(m.Root, "index", "http.json")
	var cache indexCache
	if err := readJSON(filename, &cache); err != nil {
		t.Fatal(err)
	}
	cache.AttemptedAt = time.Now().Add(-age)
	if err := writeJSON(filename, cache); err != nil {
		t.Fatal(err)
	}
}

func backgroundCheck(t *testing.T, m *Manager) error {
	t.Helper()
	lock, err := m.TryUpdateLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	return m.BackgroundUpdate(context.Background(), lock)
}

func TestConditionalIndexUpdates(t *testing.T) {
	for _, header := range []string{"ETag", "Last-Modified", "none"} {
		t.Run(header, func(t *testing.T) {
			f := setup(t)
			data, _ := json.Marshal(f.idx)
			calls := 0
			f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.Header.Get("Cache-Control") != "no-cache" {
					t.Errorf("incorrect index request: %s %v", r.Method, r.Header)
				}
				headers := http.Header{}
				if header != "none" {
					headers.Set(header, "token")
				}
				if calls > 1 && header != "none" {
					condition := "If-None-Match"
					if header == "Last-Modified" {
						condition = "If-Modified-Since"
					}
					if r.Header.Get(condition) != "token" {
						t.Errorf("missing validator: %v", r.Header)
					}
					return response(http.StatusNotModified, nil, headers), nil
				}
				return response(http.StatusOK, data, headers), nil
			})}
			if err := f.m.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(f.m.Root, "index", "index.json")
			before, _ := os.Stat(filename)
			if err := backgroundCheck(t, f.m); err != nil || calls != 1 {
				t.Fatalf("recent check repeated network work: calls=%d err=%v", calls, err)
			}
			ageIndexCheck(t, f.m, 29*time.Minute)
			if err := backgroundCheck(t, f.m); err != nil || calls != 1 {
				t.Fatalf("check repeated within 30 minutes: calls=%d err=%v", calls, err)
			}
			expireIndexCheck(t, f.m)
			if err := backgroundCheck(t, f.m); err != nil || calls != 2 {
				t.Fatalf("conditional background check: calls=%d err=%v", calls, err)
			}
			after, _ := os.Stat(filename)
			if !os.SameFile(before, after) {
				t.Fatal("unchanged index was replaced")
			}
			// A manual update bypasses the recent-check interval.
			if err := f.m.Update(context.Background()); err != nil || calls != 3 {
				t.Fatalf("manual update did not check: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestIndexValidatorInvalidationAndFailureRecovery(t *testing.T) {
	for _, mode := range []string{"deleted", "edited", "corrupt", "source", "invalid-response", "offline", "stale"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			original, _ := json.Marshal(f.idx)
			calls := 0
			f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return response(http.StatusOK, original, http.Header{"Etag": {"old"}}), nil
				}
				if mode == "deleted" || mode == "edited" || mode == "corrupt" || mode == "source" {
					if r.Header.Get("If-None-Match") != "" {
						t.Error("reused validator for mismatched local index or source")
					}
				}
				if mode == "offline" {
					return nil, errors.New("offline")
				}
				if mode == "invalid-response" {
					return response(http.StatusOK, []byte(`{"schema_version":999}`), http.Header{"Etag": {"invalid"}}), nil
				}
				idx := f.idx
				idx.Packages = append(idx.Packages[:0:0], idx.Packages...)
				idx.Packages[0].Description = "new description"
				if mode == "stale" {
					stamp, _ := time.Parse(time.RFC3339, idx.GeneratedAt)
					idx.GeneratedAt = stamp.Add(-time.Second).Format(time.RFC3339)
				}
				data, _ := json.Marshal(idx)
				return response(http.StatusOK, data, http.Header{"Etag": {"new"}}), nil
			})}
			if err := f.m.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			filename := filepath.Join(f.m.Root, "index", "index.json")
			switch mode {
			case "deleted":
				if err := os.Remove(filename); err != nil {
					t.Fatal(err)
				}
			case "edited":
				if err := os.WriteFile(filename, append(original, '\n'), 0644); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(filename, []byte("broken"), 0644); err != nil {
					t.Fatal(err)
				}
			case "source":
				f.m.IndexURL += "?other-source"
			default:
				expireIndexCheck(t, f.m)
			}
			err := backgroundCheck(t, f.m)
			failed := mode == "offline" || mode == "invalid-response"
			if (err != nil) != failed || calls != 2 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			got, _ := os.ReadFile(filename)
			if failed || mode == "stale" {
				if !bytes.Equal(got, original) {
					t.Fatal("lost usable local index")
				}
			} else if !bytes.Contains(got, []byte("new description")) {
				t.Fatal("new index was not applied")
			}
			var cache indexCache
			if err := readJSON(filepath.Join(f.m.Root, "index", "http.json"), &cache); err != nil {
				t.Fatal(err)
			}
			if failed && (cache.ETag != "old" || cache.LastError == "") {
				t.Fatalf("lost retry state: %+v", cache)
			}
			if mode == "stale" && cache.ETag != "" {
				t.Fatal("cached validator for rejected old index")
			}
			if err := backgroundCheck(t, f.m); err != nil || calls != 2 {
				t.Fatalf("no cooldown: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestIndexUpdateLockDoesNotBlockPackageOperations(t *testing.T) {
	f := setup(t)
	lock, err := f.m.TryUpdateLock()
	if err != nil {
		t.Fatal(err)
	}
	if second, err := f.m.TryUpdateLock(); !errors.Is(err, ErrUpdateBusy) {
		if second != nil {
			second.Close()
		}
		t.Fatalf("second update lock: %v", err)
	}
	if err := f.m.Recover(); err != nil {
		t.Fatalf("index check blocked package operation: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := f.m.Update(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("update lock wait ignored cancellation: %v", err)
	}
	lock.Close()
	if err := f.m.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestChangedSourceRetryCanUseAnOlderSnapshot(t *testing.T) {
	f := setup(t)
	previousSource := f.m.IndexURL
	f.m.IndexURL += "?new-source"
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("temporarily offline")
	})}
	if err := backgroundCheck(t, f.m); err == nil {
		t.Fatal("expected first request to fail")
	}
	var cache indexCache
	if err := readJSON(filepath.Join(f.m.Root, "index", "http.json"), &cache); err != nil {
		t.Fatal(err)
	}
	if cache.IndexSource != previousSource {
		t.Fatalf("failed request changed local index provenance: %+v", cache)
	}
	expireIndexCheck(t, f.m)
	f.idx.GeneratedAt = "2020-01-01T00:00:00Z"
	f.idx.Packages[0].Description = "different source"
	data, _ := json.Marshal(f.idx)
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, data, nil), nil
	})}
	if err := backgroundCheck(t, f.m); err != nil {
		t.Fatal(err)
	}
	current, err := f.m.LoadIndex()
	if err != nil || current.Packages[0].Description != "different source" {
		t.Fatalf("rejected newly selected source as stale: %v %+v", err, current)
	}
}

func TestSearchNeverUsesNetwork(t *testing.T) {
	f := setup(t)
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("local search attempted a network request")
		return nil, errors.New("network forbidden")
	})}
	var out bytes.Buffer
	f.m.Out = &out
	if err := f.m.Search(context.Background(), "demo"); err != nil || !strings.Contains(out.String(), "demo") {
		t.Fatalf("local search failed: %v %q", err, out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.m.Search(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("search ignored cancellation: %v", err)
	}
}
