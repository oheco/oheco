package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oheco/oheco/internal/catalog"
	"github.com/oheco/oheco/internal/manager"
)

type forbiddenInput struct{}

func (forbiddenInput) Read([]byte) (int, error) { panic("non-interactive search read stdin") }

type promptInput func([]byte) (int, error)

func (r promptInput) Read(p []byte) (int, error) { return r(p) }

func TestSearchUpdatePrompt(t *testing.T) {
	for _, tc := range []struct {
		name, answer        string
		interactive, update bool
	}{
		{"accept", "y\n", true, true},
		{"yes", "YES\n", true, true},
		{"decline", "n\n", true, false},
		{"default_no", "\n", true, false},
		{"eof", "", true, false},
		{"non_interactive", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var served atomic.Value
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Write(served.Load().([]byte))
			}))
			defer server.Close()
			var out bytes.Buffer
			m, err := manager.New(t.TempDir(), server.URL+"/index.json", &out)
			if err != nil {
				t.Fatal(err)
			}
			a := catalog.Artifact{URL: "https://example.com/demo.tar.gz", SHA256: strings.Repeat("0", 64), Size: 1024, Format: "tar.gz", Binaries: map[string]string{"demo": "bin/demo"}}
			idx := catalog.Index{SchemaVersion: 2, GeneratedAt: "2026-09-12T00:00:00Z", Packages: []catalog.Package{{SchemaVersion: 1, Name: "demo", Description: "old description", Upstream: "https://example.com", Repository: "https://github.com/oheco/demo", Maintainers: []catalog.Maintainer{{GitHub: "alice"}}, License: "MIT", Latest: map[string]string{m.Platform: "1"}, Versions: []catalog.Version{{Version: "1", Artifacts: map[string]catalog.Artifact{m.Platform: a}}}}}}
			oldData, _ := json.Marshal(idx)
			served.Store(oldData)
			if err := m.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			idx.GeneratedAt = "2026-09-12T00:00:01Z"
			idx.Packages[0].Description = "new description"
			newData, _ := json.Marshal(idx)
			served.Store(newData)
			out.Reset()
			var in io.Reader = strings.NewReader(tc.answer)
			if !tc.interactive {
				in = forbiddenInput{}
			}
			if err := search(context.Background(), m, "demo", in, tc.interactive); err != nil {
				t.Fatal(err)
			}
			output := out.String()
			localAt, noticeAt := strings.Index(output, "old description"), strings.Index(output, "\n\nA newer package index")
			if localAt < 0 || noticeAt < localAt || strings.Contains(output, "new description") {
				t.Fatalf("local results must precede the notice: %q", output)
			}
			if strings.Contains(output, "Update now? [y/N]") != tc.interactive {
				t.Fatalf("unexpected prompt: %q", output)
			}
			if strings.Contains(output, "Index updated.") != tc.update {
				t.Fatalf("unexpected update status: %q", output)
			}
			if tc.update && !strings.Contains(output, "Run oo search again") {
				t.Fatalf("missing search-again hint: %q", output)
			}
			if !tc.interactive && !strings.Contains(output, "Run oo update") {
				t.Fatalf("missing non-interactive update hint: %q", output)
			}
			data, err := os.ReadFile(filepath.Join(m.Root, "index", "index.json"))
			want := oldData
			if tc.update {
				want = newData
			}
			if err != nil || !bytes.Equal(data, want) || requests.Load() != 2 {
				t.Fatalf("unexpected saved index or repeat request: err=%v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestUpdatePromptCanBeCanceled(t *testing.T) {
	in, writer := io.Pipe()
	defer in.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	reading := make(chan struct{})
	go func() {
		_, err := confirmUpdate(ctx, promptInput(func(p []byte) (int, error) {
			close(reading)
			return in.Read(p)
		}), io.Discard)
		done <- err
	}()
	<-reading
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("prompt error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked on terminal input")
	}
}
