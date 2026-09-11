package manager

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type downloadOutput struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	updates chan string
}

func (o *downloadOutput) Write(data []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(data)
	select {
	case o.updates <- string(data):
	default:
	}
	return n, err
}

func (o *downloadOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func TestDownloadReportsProgressBeforeCompletionAndUsesCache(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 2048)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Write(data[:1024])
		w.(http.Flusher).Flush()
		<-release
		w.Write(data[1024:])
	}))
	t.Cleanup(server.Close)
	t.Cleanup(unblock)
	out := &downloadOutput{updates: make(chan string, 16)}
	m, err := New(t.TempDir(), server.URL+"/index.json", out)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.prepare(); err != nil {
		t.Fatal(err)
	}
	a := artifact(data, server.URL+"/archive", nil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	var filename string
	go func() {
		var err error
		filename, err = m.download(ctx, a)
		done <- err
	}()
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for {
		select {
		case update := <-out.updates:
			if !strings.Contains(update, "50%") {
				continue
			}
			if !strings.Contains(update, "1.0 KiB/2.0 KiB") || !strings.Contains(update, " B/s") || strings.Contains(update, "done") {
				t.Fatalf("invalid progress while response is still blocked: %q", update)
			}
		case err := <-done:
			t.Fatalf("download completed before the server released its response: %v", err)
		case <-timer.C:
			t.Fatalf("no intermediate download progress: %q", out.String())
		}
		break
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filename)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("downloaded content: %q, %v", got, err)
	}
	output := out.String()
	if !strings.Contains(output, "100%") || !strings.HasSuffix(output, " done\n") || strings.ContainsAny(output, "\r\x1b") {
		t.Fatalf("invalid final plain progress: %q", output)
	}
	cached, err := m.download(context.Background(), a)
	if err != nil || cached != filename || requests.Load() != 1 || out.String() != output {
		t.Fatalf("cache should avoid requests and download output: path=%q, requests=%d, err=%v, output=%q", cached, requests.Load(), err, out.String())
	}
}

type downloadRoundTripper func(*http.Request) (*http.Response, error)

func (f downloadRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type downloadReadFunc func([]byte) (int, error)

func (f downloadReadFunc) Read(data []byte) (int, error) { return f(data) }

func TestDownloadFailureDoesNotCompleteOrKeepPartialFile(t *testing.T) {
	readErr := errors.New("download read failed")
	for _, name := range []string{"short", "long", "hash", "read", "cancel"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			m, err := New(t.TempDir(), "https://example.com/index.json", &out)
			if err != nil {
				t.Fatal(err)
			}
			if err := m.prepare(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var body io.Reader
			var wantErr error
			wantMessage := "archive size mismatch"
			switch name {
			case "short":
				body = strings.NewReader("abc")
			case "long":
				body = strings.NewReader("abcde")
			case "hash":
				body, wantMessage = strings.NewReader("xxxx"), "SHA-256 mismatch"
			case "read":
				body = io.MultiReader(strings.NewReader("ab"), downloadReadFunc(func([]byte) (int, error) { return 0, readErr }))
				wantErr, wantMessage = readErr, readErr.Error()
			case "cancel":
				body = io.MultiReader(strings.NewReader("ab"), downloadReadFunc(func([]byte) (int, error) {
					cancel()
					return 0, ctx.Err()
				}))
				wantErr, wantMessage = context.Canceled, "context canceled"
			}
			m.Client = &http.Client{Transport: downloadRoundTripper(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}, nil
			})}
			a := artifact([]byte("abcd"), "https://example.com/archive", nil)
			filename, err := m.download(ctx, a)
			if err == nil || !strings.Contains(err.Error(), wantMessage) || (wantErr != nil && !errors.Is(err, wantErr)) || filename != "" {
				t.Fatalf("download result: %q, %v; want %q", filename, err, wantMessage)
			}
			if got := out.String(); !strings.HasSuffix(got, " failed\n") || strings.Contains(got, " done") {
				t.Errorf("failed download should not report completion: %q", got)
			}
			files, err := os.ReadDir(filepath.Join(m.Root, "cache", "downloads"))
			if err != nil || len(files) != 0 {
				t.Errorf("failed download left files in cache: %v, %v", files, err)
			}
		})
	}
}
