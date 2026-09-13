package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/oheco/oheco/internal/catalog"
)

// The legacy URL is intentionally retained here to test old-client redirects.
const legacyDomainIndex = "https://oheco.github.io/oheco-packages/index/v5/index.json"

func TestOfficialDomainDefaultAndExplicitOverride(t *testing.T) {
	m, err := New(t.TempDir(), "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if m.IndexURL != "https://oheco.org/index/v5/index.json" {
		t.Fatalf("wrong canonical source: %s", m.IndexURL)
	}
	custom := "https://mirror.example.test/custom-index.json"
	m, err = New(t.TempDir(), custom, io.Discard)
	if err != nil || m.IndexURL != custom {
		t.Fatalf("overrode a user's explicit index: %+v %v", m, err)
	}
	if _, err := New(t.TempDir(), "http://oheco.org/index/v5/index.json", io.Discard); err == nil {
		t.Fatal("accepted insecure official index")
	}
}

func TestOfficialDomainRedirectRequiresHTTPS(t *testing.T) {
	for _, secure := range []bool{true, false} {
		name, destination := "https", catalog.DefaultURL
		if !secure {
			name, destination = "downgrade", "http://oheco.org/index/v5/index.json"
		}
		t.Run(name, func(t *testing.T) {
			client := HTTPClient()
			var requests []string
			client.Transport = downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
				requests = append(requests, r.URL.String())
				if r.URL.String() == legacyDomainIndex {
					return response(http.StatusMovedPermanently, nil, http.Header{"Location": {destination}}), nil
				}
				if !secure {
					t.Fatal("sent an HTTP request before rejecting the downgrade")
				}
				if r.URL.String() != catalog.DefaultURL {
					t.Fatalf("unexpected redirect target %s", r.URL)
				}
				return response(http.StatusOK, []byte("ok"), nil), nil
			})
			resp, err := client.Get(legacyDomainIndex)
			if resp != nil {
				resp.Body.Close()
			}
			if secure {
				if err != nil || len(requests) != 2 {
					t.Fatalf("HTTPS redirect failed: %v %v", requests, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "URL must use HTTPS") || len(requests) != 1 {
				t.Fatalf("downgrade not blocked: %v %v", requests, err)
			}
		})
	}
}

func TestOfficialDomainMigrationResetsOnlyIndexValidators(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	if err := f.m.Install(ctx, "demo@1.0.0", false); err != nil {
		t.Fatal(err)
	}
	stateFile := filepath.Join(f.m.Root, "state", "installed.json")
	before, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatal(err)
	}
	index, err := json.Marshal(f.idx)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := HTTPClient()
	client.Transport = downloadRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			if r.URL.String() != legacyDomainIndex {
				t.Fatalf("unexpected legacy source: %s", r.URL)
			}
			return response(http.StatusOK, index, http.Header{"Etag": {"old-domain"}}), nil
		}
		if r.URL.String() != catalog.DefaultURL {
			t.Fatalf("new client contacted old domain: %s", r.URL)
		}
		if calls == 2 {
			if r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "" {
				t.Fatal("reused another source's cache validator")
			}
			return response(http.StatusOK, index, http.Header{"Etag": {"canonical-domain"}}), nil
		}
		if r.Header.Get("If-None-Match") != "canonical-domain" {
			t.Fatal("lost new source validator")
		}
		return response(http.StatusNotModified, nil, nil), nil
	})
	f.m.IndexURL, f.m.Client = legacyDomainIndex, client
	if err := f.m.Update(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := New(f.m.Root, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	m.Client = client
	// A changed default is not delayed by the old source's 30-minute cooldown.
	if err := backgroundCheck(t, m); err != nil || calls != 2 {
		t.Fatalf("domain migration did not refresh: %d %v", calls, err)
	}
	if err := m.Update(ctx); err != nil || calls != 3 {
		t.Fatalf("new source conditional update: %d %v", calls, err)
	}
	after, err := os.ReadFile(stateFile)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("domain migration changed installed packages")
	}
	linkIs(t, m.Root, "demo", "demo@1.0.0")
	var cache indexCache
	if err := readJSON(filepath.Join(m.Root, "index", "http.json"), &cache); err != nil {
		t.Fatal(err)
	}
	if cache.URL != catalog.DefaultURL || cache.IndexSource != catalog.DefaultURL {
		t.Fatalf("wrong cache provenance: %+v", cache)
	}
}
