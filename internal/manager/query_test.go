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
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
)

func TestInstalledSearchAndListTables(t *testing.T) {
	f := setup(t)
	f.idx.Packages[0].Maintainers = []catalog.Maintainer{{GitHub: "alice", Name: "Alice Example"}, {GitHub: "bob"}}
	if err := writeJSON(filepath.Join(f.m.Root, "index", "index.json"), f.idx); err != nil {
		t.Fatal(err)
	}
	p := InstalledPackage{Active: "1.0.0", Versions: map[string]Receipt{}}
	a := f.idx.Packages[0].Versions[0].Artifacts[f.m.Platform]
	for _, version := range []string{"1.0.0", "1.9.0", "1.10.0"} {
		p.Versions[version] = Receipt{Platform: f.m.Platform, Artifact: a}
	}
	s := emptyState()
	s.Packages["demo"] = p
	if err := writeJSON(filepath.Join(f.m.Root, "state", "installed.json"), s); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	f.m.Out = &out
	if _, err := f.m.Search(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	start, end := strings.Index(lines[0], "INSTALLED"), strings.Index(lines[0], "SIZE")
	if got := strings.TrimSpace(lines[1][start:end]); got != "1.10.0 (3)" {
		t.Fatalf("installed summary = %q, want newest installed version and count", got)
	}
	start, end = strings.Index(lines[0], "MAINTAINERS"), strings.Index(lines[0], "DESCRIPTION")
	searchMaintainers := strings.TrimSpace(lines[1][start:end])
	out.Reset()
	if err := f.m.List(); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); strings.Count(got, "\n") != 2 || !strings.Contains(got, "1.0.0*, 1.9.0, 1.10.0") || !strings.Contains(got, "Alice Example, @bob") {
		t.Fatalf("incorrect grouped list: %q", got)
	}
	lines = strings.Split(strings.TrimSpace(out.String()), "\n")
	listMaintainers := strings.TrimSpace(lines[1][strings.Index(lines[0], "MAINTAINERS"):])
	if searchMaintainers != listMaintainers {
		t.Fatalf("maintainer display differs: search=%q, list=%q", searchMaintainers, listMaintainers)
	}
	if err := os.Remove(filepath.Join(f.m.Root, "index", "index.json")); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := f.m.List(); err != nil || !strings.Contains(out.String(), "1.0.0*, 1.9.0, 1.10.0") || !strings.HasSuffix(out.String(), "-\n") {
		t.Fatalf("list must work without an index: %q, %v", out.String(), err)
	}
}

func TestInstalledVersionOrdering(t *testing.T) {
	p := InstalledPackage{Versions: map[string]Receipt{}}
	for _, v := range []string{"1.10.0", "1.9.0-ohos.10", "1.9.0", "1.9.0-rc.10", "1.9.0-ohos.2", "1.9.0-rc.2"} {
		p.Versions[v] = Receipt{Platform: "ohos-arm64"}
	}
	p.Versions["99"] = Receipt{Platform: "other-arm64"}
	want := []string{"1.9.0-rc.2", "1.9.0-rc.10", "1.9.0", "1.9.0-ohos.2", "1.9.0-ohos.10", "1.10.0"}
	if got := installedVersions(p, "ohos-arm64"); !reflect.DeepEqual(got, want) {
		t.Fatalf("installed versions: got %v, want %v", got, want)
	}
}

type queryWriter func([]byte) (int, error)

func (w queryWriter) Write(p []byte) (int, error) { return w(p) }

func TestSearchStopsWaitingAndCancelsRemoteRequest(t *testing.T) {
	f := setup(t)
	started, canceled := make(chan struct{}), make(chan struct{})
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		close(canceled)
		return nil, r.Context().Err()
	})}
	var out bytes.Buffer
	var printed time.Time
	f.m.Out = queryWriter(func(data []byte) (int, error) {
		<-started // Ensure the background request is live while local output proceeds.
		printed = time.Now()
		return out.Write(data)
	})
	update, err := f.m.Search(context.Background(), "demo")
	elapsed := time.Since(printed)
	if err != nil || update != nil || !strings.Contains(out.String(), "demo") {
		t.Fatalf("local search failed: update=%v err=%v out=%q", update, err, out.String())
	}
	if elapsed < 450*time.Millisecond || elapsed > time.Second {
		t.Fatalf("wait after local output was %v, expected about 500ms", elapsed)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("background request was not canceled")
	}
}

func TestSearchWaitBudgetStartsAfterLocalOutput(t *testing.T) {
	f := setup(t)
	data, err := os.ReadFile(filepath.Join(f.m.Root, "index", "index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var remote catalog.Index
	if err := json.Unmarshal(data, &remote); err != nil {
		t.Fatal(err)
	}
	remote.Packages[0].Description = "updated description"
	data, _ = json.Marshal(remote)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		select {
		case <-release:
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	})}
	var slow sync.Once
	f.m.Out = queryWriter(func(data []byte) (int, error) {
		slow.Do(func() {
			time.Sleep(550 * time.Millisecond)
			time.AfterFunc(50*time.Millisecond, unblock)
		})
		return len(data), nil
	})
	update, err := f.m.Search(context.Background(), "demo")
	if err != nil || update == nil {
		t.Fatalf("response in the post-output budget was lost: %v, %v", update, err)
	}
}

func TestSearchIndexChecksAndConfirmedUpdate(t *testing.T) {
	for _, mode := range []string{"changed", "timestamp-only", "older", "invalid", "offline"} {
		t.Run(mode, func(t *testing.T) {
			f := setup(t)
			filename := filepath.Join(f.m.Root, "index", "index.json")
			before, err := os.ReadFile(filename)
			if err != nil {
				t.Fatal(err)
			}
			var remote catalog.Index
			if err := json.Unmarshal(before, &remote); err != nil {
				t.Fatal(err)
			}
			stamp, _ := time.Parse(time.RFC3339, remote.GeneratedAt)
			remote.GeneratedAt = stamp.Add(time.Second).Format(time.RFC3339)
			if mode != "timestamp-only" {
				remote.Packages[0].Description = "new remote description"
			}
			if mode == "older" {
				remote.GeneratedAt = stamp.Add(-time.Second).Format(time.RFC3339)
			}
			data, _ := json.Marshal(remote)
			if mode == "invalid" {
				data = []byte(`{"schema_version":999}`)
			}
			requests := 0
			f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.Header.Get("Cache-Control") != "no-cache" {
					return nil, errors.New("missing index cache revalidation")
				}
				if mode == "offline" {
					return nil, errors.New("offline")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data))}, nil
			})}
			var out bytes.Buffer
			f.m.Out = &out
			update, err := f.m.Search(context.Background(), "demo")
			if err != nil || (update != nil) != (mode == "changed") {
				t.Fatalf("unexpected update=%v err=%v", update, err)
			}
			after, _ := os.ReadFile(filename)
			if !bytes.Equal(before, after) || strings.Contains(out.String(), "new remote description") {
				t.Fatal("remote data replaced local results before confirmation")
			}
			if update != nil {
				changed, err := f.m.ApplyIndexUpdate(context.Background(), update)
				if err != nil || !changed || requests != 1 {
					t.Fatalf("confirmed update must reuse validated response: changed=%v requests=%d err=%v", changed, requests, err)
				}
				after, _ = os.ReadFile(filename)
				if !bytes.Equal(data, after) {
					t.Fatal("confirmed index was not saved")
				}
				changed, err = f.m.ApplyIndexUpdate(context.Background(), update)
				if err != nil || changed {
					t.Fatalf("already applied update: changed=%v err=%v", changed, err)
				}
				remote.GeneratedAt = stamp.Add(2 * time.Second).Format(time.RFC3339)
				remote.Packages[0].Description = "newer concurrent update"
				if err := writeJSON(filename, remote); err != nil {
					t.Fatal(err)
				}
				changed, err = f.m.ApplyIndexUpdate(context.Background(), update)
				current, readErr := f.m.LoadIndex()
				if err != nil || readErr != nil || changed || current.Packages[0].Description != "newer concurrent update" {
					t.Fatalf("confirmation replaced a newer local index: changed=%v err=%v readErr=%v", changed, err, readErr)
				}
			}
		})
	}
}

func TestSearchCancellationDoesNotWaitForDeadline(t *testing.T) {
	f := setup(t)
	started := make(chan struct{})
	f.m.Client = &http.Client{Transport: downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		close(started)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.m.Out = queryWriter(func(data []byte) (int, error) {
		<-started
		cancel()
		return len(data), nil
	})
	begin := time.Now()
	if _, err := f.m.Search(ctx, "demo"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected canceled search, got %v", err)
	}
	if time.Since(begin) >= 500*time.Millisecond {
		t.Fatal("canceled search waited for the full remote budget")
	}
}
