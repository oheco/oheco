package manager

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestHTTPClientProxyEnvironment(t *testing.T) {
	cases := []struct {
		name, scheme, proxyVariable, bypassVariable string
	}{
		{"https_uppercase", "https", "HTTPS_PROXY", ""},
		{"https_lowercase", "https", "https_proxy", ""},
		{"http_uppercase", "http", "HTTP_PROXY", ""},
		{"http_lowercase", "http", "http_proxy", ""},
		{"no_proxy_uppercase", "https", "HTTPS_PROXY", "NO_PROXY"},
		{"no_proxy_lowercase", "https", "https_proxy", "no_proxy"},
	}
	const childVariable = "OHECO_PROXY_TEST_CASE"
	child := os.Getenv(childVariable)
	for _, tc := range cases {
		if child != "" && child != tc.name {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			if child == "" {
				// net/http caches proxy environment variables process-wide.
				// Each case therefore needs a fresh test process.
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestHTTPClientProxyEnvironment$")
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					switch strings.ToUpper(key) {
					case "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "REQUEST_METHOD", childVariable:
						continue
					}
					cmd.Env = append(cmd.Env, entry)
				}
				cmd.Env = append(cmd.Env, childVariable+"="+tc.name)
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("proxy subprocess: %v\n%s", err, output)
				}
				return
			}

			type proxyRequest struct{ method, host, url string }
			requests := make(chan proxyRequest, 1)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests <- proxyRequest{r.Method, r.Host, r.URL.String()}
				if r.Method == http.MethodConnect {
					// Receiving CONNECT proves HTTPS used the proxy; no tunnel
					// or connection to the fictional target is needed.
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer proxy.Close()
			t.Setenv(tc.proxyVariable, proxy.URL)
			if tc.bypassVariable != "" {
				t.Setenv(tc.bypassVariable, "packages.example.test")
			}

			client := HTTPClient()
			transport := client.Transport
			if transport == nil {
				transport = http.DefaultTransport
			}
			httpTransport, ok := transport.(*http.Transport)
			if !ok || httpTransport.Proxy == nil {
				t.Fatal("HTTPClient must use an HTTP transport with environment proxy support")
			}
			target := tc.scheme + "://packages.example.test/archive.tar.gz"
			req, err := http.NewRequest(http.MethodGet, target, nil)
			if err != nil {
				t.Fatal(err)
			}
			selectedProxy, err := httpTransport.Proxy(req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.bypassVariable != "" {
				if selectedProxy != nil {
					t.Fatalf("%s ignored: selected proxy %s", tc.bypassVariable, selectedProxy)
				}
				return
			}
			if selectedProxy == nil || selectedProxy.String() != proxy.URL {
				t.Fatalf("%s ignored: selected proxy %v, want %s", tc.proxyVariable, selectedProxy, proxy.URL)
			}

			// Keep the client's proxy behavior while preventing any accidental
			// external network access if its routing regresses.
			localTransport := httpTransport.Clone()
			localTransport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address != proxy.Listener.Addr().String() {
					return nil, fmt.Errorf("unexpected non-proxy dial: %s", address)
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			defer localTransport.CloseIdleConnections()
			client.Transport = localTransport
			client.Timeout = 5 * time.Second
			resp, err := client.Do(req)
			if resp != nil {
				resp.Body.Close()
			}
			if tc.scheme == "http" && err != nil {
				t.Fatal(err)
			}
			if tc.scheme == "https" && err == nil {
				t.Fatal("expected the local proxy's CONNECT rejection")
			}
			select {
			case got := <-requests:
				if tc.scheme == "https" {
					if got.method != http.MethodConnect || got.host != "packages.example.test:443" {
						t.Fatalf("unexpected HTTPS proxy request: %+v", got)
					}
				} else if got.method != http.MethodGet || got.url != target {
					t.Fatalf("unexpected HTTP proxy request: %+v", got)
				}
			default:
				t.Fatalf("request did not reach the local proxy: %v", err)
			}
		})
	}
}
