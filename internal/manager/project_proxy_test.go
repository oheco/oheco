package manager

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type projectProxyTestTarget struct {
	host string
	port uint16
	kind byte
}

// Every mock listens on loopback and routes only to explicitly supplied local
// endpoints. Fictional target names prove DNS resolution is left to the upstream.
func projectProxyTestListener(t *testing.T, handler func(net.Conn) error) string {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	closed := false
	conns := make(map[net.Conn]bool)
	var wg sync.WaitGroup
	errors := make(chan error, 20)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				mu.Unlock()
				_ = conn.Close()
				return
			}
			conns[conn] = true
			wg.Add(1)
			mu.Unlock()
			go func() {
				defer wg.Done()
				defer func() {
					_ = conn.Close()
					mu.Lock()
					delete(conns, conn)
					mu.Unlock()
				}()
				_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
				if err := handler(conn); err != nil {
					select {
					case errors <- err:
					default:
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		mu.Lock()
		closed = true
		for conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		wg.Wait()
		close(errors)
		for err := range errors {
			t.Error(err)
		}
	})
	return listener.Addr().String()
}

func projectProxyTestSOCKS(t *testing.T, handler func(net.Conn) error) string {
	t.Helper()
	return "socks5://" + projectProxyTestListener(t, handler)
}

func projectProxyTestGreeting(conn net.Conn, method byte) error {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	if header[0] != 5 || header[1] == 0 {
		return fmt.Errorf("invalid greeting: %v", header)
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return err
	}
	if method != 255 && !bytes.Contains(methods, []byte{method}) {
		return fmt.Errorf("method %d was not offered: %v", method, methods)
	}
	_, err := conn.Write([]byte{5, method})
	return err
}

func projectProxyTestAuth(conn net.Conn, status byte) error {
	var header [2]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return err
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, username); err != nil {
		return err
	}
	var size [1]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return err
	}
	password := make([]byte, int(size[0]))
	if _, err := io.ReadFull(conn, password); err != nil {
		return err
	}
	if header[0] != 1 || string(username) != "user" || string(password) != "p@ss:word" {
		return fmt.Errorf("unexpected RFC1929 credentials")
	}
	_, err := conn.Write([]byte{1, status})
	return err
}

func projectProxyTestReadTarget(conn net.Conn) (target projectProxyTestTarget, err error) {
	var header [4]byte
	if _, err = io.ReadFull(conn, header[:]); err != nil {
		return
	}
	if !bytes.Equal(header[:3], []byte{5, 1, 0}) {
		return target, fmt.Errorf("invalid connect header: %v", header)
	}
	target.kind = header[3]
	size := 0
	switch target.kind {
	case 1:
		size = 4
	case 4:
		size = 16
	case 3:
		var length [1]byte
		if _, err = io.ReadFull(conn, length[:]); err != nil {
			return
		}
		size = int(length[0])
	default:
		return target, fmt.Errorf("invalid target type: %d", target.kind)
	}
	host := make([]byte, size)
	if _, err = io.ReadFull(conn, host); err != nil {
		return
	}
	if target.kind == 3 {
		target.host = string(host)
	} else {
		target.host = net.IP(host).String()
	}
	var port [2]byte
	_, err = io.ReadFull(conn, port[:])
	target.port = binary.BigEndian.Uint16(port[:])
	return
}

func projectProxyTestEnvValue(t *testing.T, env []string, key string) string {
	t.Helper()
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if ok && name == key {
			return value
		}
	}
	t.Fatalf("missing environment key %s", key)
	return ""
}

func projectProxyTestClient(t *testing.T, value string) *http.Client {
	t.Helper()
	proxy, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy)}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second}
}

func TestProjectProxyEnvironment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env := []string{
		"HOME=/unchanged/home", "NO_PROXY=localhost,.example.test", "no_proxy=other.test",
		"HTTP_PROXY=socks5://127.0.0.1:10808", "https_proxy=socks5h://127.0.0.1:10808/",
		"HTTPS_PROXY=socks5://user:secret@127.0.0.1:10809", "http_proxy=http://existing:8080",
		"ALL_PROXY=socks5://127.0.0.1:10808", "CUSTOM=a=b", "WITHOUT_EQUALS",
	}
	original := append([]string(nil), env...)
	got, cleanup, err := prepareProjectProxy(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !reflect.DeepEqual(env, original) {
		t.Fatal("input environment was mutated")
	}
	for _, i := range []int{0, 1, 2, 7, 8, 9} {
		if got[i] != env[i] {
			t.Errorf("unrelated environment entry %d changed", i)
		}
	}
	first := projectProxyTestEnvValue(t, got, "HTTP_PROXY")
	if first != projectProxyTestEnvValue(t, got, "https_proxy") {
		t.Fatal("equivalent SOCKS upstreams did not share a listener")
	}
	if first == projectProxyTestEnvValue(t, got, "HTTPS_PROXY") {
		t.Fatal("different SOCKS upstreams shared a listener")
	}
	if first == projectProxyTestEnvValue(t, got, "http_proxy") {
		t.Fatal("different HTTP and SOCKS upstreams shared a listener")
	}
	for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		u, err := url.Parse(projectProxyTestEnvValue(t, got, key))
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "0" || u.Port() == "" || u.User == nil || u.User.String() != "oheco:local" {
			t.Fatalf("invalid local proxy for %s", key)
		}
	}
	cleanup()
	cleanup()
	u, _ := url.Parse(first)
	conn, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("listener survived cleanup")
	}
}

func TestProjectProxyUpstreamNormalization(t *testing.T) {
	for _, upstream := range []struct {
		scheme string
		alias  string
		port   string
	}{
		{"socks5", "socks5h", "1080"},
		{"socks5h", "socks5", "1080"},
		{"http", "http", "80"},
		{"https", "https", "443"},
	} {
		t.Run(upstream.scheme, func(t *testing.T) {
			env := []string{
				"HTTP_PROXY=" + upstream.scheme + "://localhost",
				"HTTPS_PROXY=" + strings.ToUpper(upstream.alias) + "://LOCALHOST:" + upstream.port + "/",
				"http_proxy=" + upstream.scheme + "://localhost:00" + upstream.port,
				"https_proxy=" + upstream.scheme + "://localhost:" + upstream.port,
			}
			got, cleanup, err := prepareProjectProxy(context.Background(), env)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			first := projectProxyTestEnvValue(t, got, "HTTP_PROXY")
			for _, key := range []string{"HTTPS_PROXY", "http_proxy", "https_proxy"} {
				if projectProxyTestEnvValue(t, got, key) != first {
					t.Fatalf("equivalent upstream for %s did not share a listener", key)
				}
			}
		})
	}
	got, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTP_PROXY=http://localhost:443", "HTTPS_PROXY=https://localhost"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if projectProxyTestEnvValue(t, got, "HTTP_PROXY") == projectProxyTestEnvValue(t, got, "HTTPS_PROXY") {
		t.Fatal("HTTP and HTTPS upstreams shared a listener")
	}
}

func TestProjectProxyHTTPUpstreamForwarding(t *testing.T) {
	for _, authenticate := range []bool{false, true} {
		t.Run(fmt.Sprintf("authenticate_%t", authenticate), func(t *testing.T) {
			originSeen := make(chan string, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				originSeen <- r.Host + r.URL.RequestURI() + ":" + string(body) + ":" + r.Header.Get("Proxy-Authorization") + r.Header.Get("X-Hop")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "HTTP upstream origin response")
			}))
			defer origin.Close()
			originURL, _ := url.Parse(origin.URL)
			transport := &http.Transport{}
			defer transport.CloseIdleConnections()
			upstreamSeen := make(chan string, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamSeen <- r.Method + " " + r.RequestURI + ":" + r.Header.Get("Proxy-Authorization") + ":" + r.Header.Get("X-Hop")
				request := r.Clone(r.Context())
				request.RequestURI = ""
				request.URL.Scheme, request.URL.Host = originURL.Scheme, originURL.Host
				request.Header.Del("Proxy-Authorization")
				response, err := transport.RoundTrip(request)
				if err != nil {
					http.Error(w, "mock origin failed", http.StatusBadGateway)
					return
				}
				defer response.Body.Close()
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Body)
			}))
			defer upstream.Close()
			upstreamURL, _ := url.Parse(upstream.URL)
			wantAuth := ""
			if authenticate {
				upstreamURL.User = url.UserPassword("user", "p@ss:word")
				wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("user:p@ss:word"))
			}
			env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTP_PROXY=" + upstreamURL.String()})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			client := projectProxyTestClient(t, projectProxyTestEnvValue(t, env, "HTTP_PROXY"))
			request, _ := http.NewRequest(http.MethodPost, "http://origin.invalid:8123/archive?q=1", strings.NewReader("request body"))
			request.Header.Set("Connection", "X-Hop")
			request.Header.Set("X-Hop", "remove")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if err != nil || response.StatusCode != http.StatusCreated || string(body) != "HTTP upstream origin response" {
				t.Fatalf("HTTP upstream response: %d %q %v", response.StatusCode, body, err)
			}
			if got := <-upstreamSeen; got != "POST http://origin.invalid:8123/archive?q=1:"+wantAuth+":" {
				t.Fatalf("HTTP upstream request or proxy credentials: %q", got)
			}
			if got := <-originSeen; got != "origin.invalid:8123/archive?q=1:request body:" {
				t.Fatalf("origin request leaked proxy credentials or hop headers: %q", got)
			}
		})
	}
}

func TestProjectProxyHTTPUpstreamCONNECT(t *testing.T) {
	for _, authenticate := range []bool{false, true} {
		for _, halfClose := range []string{"client", "upstream"} {
			t.Run(fmt.Sprintf("authenticate_%t/%s_half_close", authenticate, halfClose), func(t *testing.T) {
				wantAuth := ""
				if authenticate {
					wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("user:p@ss:word"))
				}
				payload := []byte{0x16, 0x03, 0x03, 0, 5, 0xff, 0, '\r', '\n', 0x80}
				received := make(chan string, 1)
				address := projectProxyTestListener(t, func(conn net.Conn) error {
					reader := bufio.NewReader(conn)
					request, err := http.ReadRequest(reader)
					if err != nil {
						return err
					}
					defer request.Body.Close()
					if request.Method != http.MethodConnect || request.RequestURI != "tls-origin.invalid:443" || request.Host != "tls-origin.invalid:443" {
						return fmt.Errorf("unexpected HTTP upstream CONNECT: %s %s %s", request.Method, request.RequestURI, request.Host)
					}
					if request.Header.Get("Proxy-Authorization") != wantAuth || request.Header.Get("X-Hop") != "" {
						return fmt.Errorf("HTTP upstream CONNECT received wrong credentials or hop headers")
					}
					if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\nserver greeting"); err != nil {
						return err
					}
					if halfClose == "upstream" {
						if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
							return err
						}
						body, err := io.ReadAll(reader)
						received <- string(body)
						return err
					}
					_, err = io.Copy(conn, reader)
					return err
				})
				upstream := &url.URL{Scheme: "http", Host: address}
				if authenticate {
					upstream.User = url.UserPassword("user", "p@ss:word")
				}
				env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=" + upstream.String()})
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				local, _ := url.Parse(projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
				client, err := net.Dial("tcp", local.Host)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				_ = client.SetDeadline(time.Now().Add(5 * time.Second))
				placeholder := base64.StdEncoding.EncodeToString([]byte("oheco:local"))
				request := "CONNECT tls-origin.invalid:443 HTTP/1.1\r\nHost: tls-origin.invalid:443\r\nProxy-Authorization: Basic " + placeholder + "\r\nConnection: X-Hop\r\nX-Hop: remove\r\n\r\n"
				if _, err := client.Write(append([]byte(request), payload...)); err != nil {
					t.Fatal(err)
				}
				reader := bufio.NewReader(client)
				response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
				if err != nil || response.StatusCode != http.StatusOK {
					t.Fatalf("HTTP upstream CONNECT response: %v %v", response, err)
				}
				greeting := make([]byte, len("server greeting"))
				if _, err := io.ReadFull(reader, greeting); err != nil || string(greeting) != "server greeting" {
					t.Fatalf("buffered upstream bytes: %q %v", greeting, err)
				}
				if halfClose == "upstream" {
					if body, err := io.ReadAll(reader); err != nil || len(body) != 0 {
						t.Fatalf("upstream half-close: %q %v", body, err)
					}
				} else {
					body := make([]byte, len(payload))
					if _, err := io.ReadFull(reader, body); err != nil || !bytes.Equal(body, payload) {
						t.Fatalf("buffered client bytes: %x %v", body, err)
					}
				}
				if _, err := io.WriteString(client, "last bytes"); err != nil {
					t.Fatal(err)
				}
				if err := client.(*net.TCPConn).CloseWrite(); err != nil {
					t.Fatal(err)
				}
				if halfClose == "upstream" {
					select {
					case got := <-received:
						if got != string(payload)+"last bytes" {
							t.Fatalf("data following upstream half-close: %q", got)
						}
					case <-time.After(5 * time.Second):
						t.Fatal("upstream half-close terminated the other copy direction")
					}
				} else if body, err := io.ReadAll(reader); err != nil || string(body) != "last bytes" {
					t.Fatalf("data following client half-close: %q %v", body, err)
				}
			})
		}
	}
}

func TestProjectProxyHTTPUpstreamRejection(t *testing.T) {
	for _, reply := range []string{
		"HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 10\r\n\r\ntop-secret",
		"malformed proxy reply\r\n\r\n",
	} {
		address := projectProxyTestListener(t, func(conn net.Conn) error {
			request, err := http.ReadRequest(bufio.NewReader(conn))
			if err != nil {
				return err
			}
			_ = request.Body.Close()
			_, err = io.WriteString(conn, reply)
			return err
		})
		env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=http://user:top-secret@" + address})
		if err != nil {
			t.Fatal(err)
		}
		client := projectProxyTestClient(t, projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
		response, err := client.Get("https://rejected.invalid/")
		cleanup()
		if err == nil {
			_ = response.Body.Close()
			t.Fatal("rejected CONNECT established a tunnel")
		}
		if !strings.Contains(err.Error(), http.StatusText(http.StatusBadGateway)) || strings.Contains(err.Error(), "top-secret") {
			t.Fatalf("HTTP upstream CONNECT rejection exposed upstream details: %v", err)
		}
	}
}

func TestProjectProxyHTTPUpstreamCancellation(t *testing.T) {
	for _, cancelContext := range []bool{false, true} {
		for _, scheme := range []string{"http", "https"} {
			for _, method := range []string{http.MethodGet, http.MethodConnect} {
				t.Run(fmt.Sprintf("cancel_%t/%s/%s", cancelContext, scheme, method), func(t *testing.T) {
					connected, disconnected := make(chan struct{}), make(chan struct{})
					address := projectProxyTestListener(t, func(conn net.Conn) error {
						// HTTP waits for response headers; HTTPS waits for a TLS server hello.
						if scheme == "http" {
							request, err := http.ReadRequest(bufio.NewReader(conn))
							if err != nil {
								return err
							}
							_ = request.Body.Close()
						} else {
							var first [1]byte
							if _, err := io.ReadFull(conn, first[:]); err != nil {
								return err
							}
						}
						close(connected)
						_, _ = io.Copy(io.Discard, conn)
						close(disconnected)
						return nil
					})
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					env, cleanup, err := prepareProjectProxy(ctx, []string{"HTTP_PROXY=" + scheme + "://" + address})
					if err != nil {
						t.Fatal(err)
					}
					defer cleanup()
					local, _ := url.Parse(projectProxyTestEnvValue(t, env, "HTTP_PROXY"))
					client, err := net.Dial("tcp", local.Host)
					if err != nil {
						t.Fatal(err)
					}
					defer client.Close()
					_ = client.SetDeadline(time.Now().Add(5 * time.Second))
					request := "GET http://cleanup.invalid/ HTTP/1.1\r\nHost: cleanup.invalid\r\n\r\n"
					if method == http.MethodConnect {
						request = "CONNECT cleanup.invalid:443 HTTP/1.1\r\nHost: cleanup.invalid:443\r\n\r\n"
					}
					if _, err := io.WriteString(client, request); err != nil {
						t.Fatal(err)
					}
					select {
					case <-connected:
					case <-time.After(5 * time.Second):
						t.Fatal("upstream handshake did not start")
					}
					finished := make(chan struct{})
					go func() {
						if cancelContext {
							cancel()
						} else {
							cleanup()
						}
						close(finished)
					}()
					select {
					case <-finished:
					case <-time.After(2 * time.Second):
						t.Fatal("HTTP upstream cleanup stalled")
					}
					select {
					case <-disconnected:
					case <-time.After(2 * time.Second):
						t.Fatal("HTTP upstream handshake survived cleanup/cancellation")
					}
					cleanup()
					if _, err := io.ReadAll(client); err != nil {
						if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
							t.Fatal("child connection survived HTTP upstream cleanup")
						}
					}
				})
			}
		}
	}
}

func TestProjectProxyHTTPSUpstreamCertificate(t *testing.T) {
	requests := make(chan struct{}, 2)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- struct{}{}
		_, _ = io.WriteString(w, "untrusted proxy must not receive requests")
	}))
	defer upstream.Close()
	env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=" + upstream.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	for _, scheme := range []string{"http", "https"} {
		client := projectProxyTestClient(t, projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
		response, err := client.Get(scheme + "://untrusted-origin.invalid/")
		if scheme == "http" {
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if response.StatusCode != http.StatusBadGateway {
				t.Fatalf("untrusted HTTPS upstream returned %d", response.StatusCode)
			}
		} else if err == nil || !strings.Contains(err.Error(), http.StatusText(http.StatusBadGateway)) {
			t.Fatalf("untrusted HTTPS upstream CONNECT: %v", err)
		}
	}
	select {
	case <-requests:
		t.Fatal("untrusted HTTPS upstream received an HTTP request")
	default:
	}
}

func TestProjectProxyHTTPForwarding(t *testing.T) {
	for _, authenticate := range []bool{false, true} {
		t.Run(fmt.Sprintf("authenticate_%t", authenticate), func(t *testing.T) {
			seen := make(chan string, 1)
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seen <- r.Host + r.URL.RequestURI() + ":" + string(body) + ":" + r.Header.Get("Proxy-Authorization") + r.Header.Get("X-Hop")
				w.Header().Set("Connection", "X-Response-Hop")
				w.Header().Set("X-Response-Hop", "remove")
				w.Header().Set("X-Origin", "yes")
				w.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(w, "actual origin response")
			}))
			defer origin.Close()
			targets := make(chan projectProxyTestTarget, 1)
			socks := projectProxyTestSOCKS(t, func(conn net.Conn) error {
				method := byte(0)
				if authenticate {
					method = 2
				}
				if err := projectProxyTestGreeting(conn, method); err != nil {
					return err
				}
				if authenticate {
					if err := projectProxyTestAuth(conn, 0); err != nil {
						return err
					}
				}
				target, err := projectProxyTestReadTarget(conn)
				if err != nil {
					return err
				}
				targets <- target
				if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
					return err
				}
				remote, err := net.Dial("tcp", origin.Listener.Addr().String())
				if err != nil {
					return err
				}
				defer remote.Close()
				done := make(chan struct{})
				go func() { _, _ = io.Copy(remote, conn); _ = remote.Close(); close(done) }()
				_, _ = io.Copy(conn, remote)
				_ = conn.Close()
				<-done
				return nil
			})
			if authenticate {
				socks = strings.Replace(socks, "://", "://user:p%40ss%3Aword@", 1)
			}
			env, cleanup, err := prepareProjectProxy(context.Background(), []string{"http_proxy=" + socks})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			client := projectProxyTestClient(t, projectProxyTestEnvValue(t, env, "http_proxy"))
			request, _ := http.NewRequest(http.MethodPost, "http://origin.invalid:8123/archive?q=1", strings.NewReader("request body"))
			request.Header.Set("Proxy-Authorization", "Basic child-secret")
			request.Header.Set("Connection", "X-Hop")
			request.Header.Set("X-Hop", "remove")
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != http.StatusCreated || string(body) != "actual origin response" || response.Header.Get("X-Origin") != "yes" || response.Header.Get("X-Response-Hop") != "" {
				t.Fatalf("forwarded response: status=%d body=%q err=%v", response.StatusCode, body, err)
			}
			if got := <-seen; got != "origin.invalid:8123/archive?q=1:request body:" {
				t.Fatalf("forwarded request: %q", got)
			}
			if got := <-targets; got != (projectProxyTestTarget{"origin.invalid", 8123, 3}) {
				t.Fatalf("SOCKS target: %+v", got)
			}
		})
	}
}

func TestProjectProxyCONNECT(t *testing.T) {
	for _, target := range []struct {
		address string
		want    projectProxyTestTarget
	}{
		{"tls-origin.invalid:443", projectProxyTestTarget{"tls-origin.invalid", 443, 3}},
		{"192.0.2.10:8443", projectProxyTestTarget{"192.0.2.10", 8443, 1}},
		{"[2001:db8::10]:443", projectProxyTestTarget{"2001:db8::10", 443, 4}},
	} {
		t.Run(target.address, func(t *testing.T) {
			targets := make(chan projectProxyTestTarget, 1)
			socks := projectProxyTestSOCKS(t, func(conn net.Conn) error {
				if err := projectProxyTestGreeting(conn, 2); err != nil {
					return err
				}
				if err := projectProxyTestAuth(conn, 0); err != nil {
					return err
				}
				got, err := projectProxyTestReadTarget(conn)
				if err != nil {
					return err
				}
				targets <- got
				// Exercise consumption of a variable-length SOCKS bound address.
				if _, err := conn.Write([]byte{5, 0, 0, 3, 3, 'b', 'n', 'd', 0, 1}); err != nil {
					return err
				}
				_, _ = io.Copy(conn, conn)
				return nil
			})
			socks = strings.Replace(socks, "://", "://user:p%40ss%3Aword@", 1)
			env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=" + socks})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			u, _ := url.Parse(projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
			conn, err := net.Dial("tcp", u.Host)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			payload := []byte{0x16, 0x03, 0x03, 0, 5, 0xff, 0, '\r', '\n', 0x80}
			request := []byte("CONNECT " + target.address + " HTTP/1.1\r\nHost: " + target.address + "\r\n\r\n")
			if _, err := conn.Write(append(request, payload...)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("CONNECT response: %v, %v", response, err)
			}
			got := make([]byte, len(payload))
			if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("opaque bytes changed: %x, %v", got, err)
			}
			// Client half-close must still permit the last upstream response.
			if _, err := conn.Write([]byte("last bytes")); err != nil {
				t.Fatal(err)
			}
			if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
				t.Fatal(err)
			}
			last, err := io.ReadAll(reader)
			if err != nil || string(last) != "last bytes" {
				t.Fatalf("half-close response: %q, %v", last, err)
			}
			if got := <-targets; got != target.want {
				t.Fatalf("SOCKS target: %+v", got)
			}
		})
	}
}

func TestProjectProxyCONNECTUpstreamHalfClose(t *testing.T) {
	received := make(chan string, 1)
	socks := projectProxyTestSOCKS(t, func(conn net.Conn) error {
		if err := projectProxyTestGreeting(conn, 0); err != nil {
			return err
		}
		if _, err := projectProxyTestReadTarget(conn); err != nil {
			return err
		}
		if _, err := conn.Write(append([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}, []byte("server finished sending")...)); err != nil {
			return err
		}
		if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
			return err
		}
		body, err := io.ReadAll(conn)
		if err != nil {
			return err
		}
		received <- string(body)
		return nil
	})
	env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=" + socks})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	u, _ := url.Parse(projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
	client, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(client, "CONNECT halfclose.invalid:443 HTTP/1.1\r\nHost: halfclose.invalid:443\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT response: %v %v", response, err)
	}
	body, err := io.ReadAll(reader)
	if err != nil || string(body) != "server finished sending" {
		t.Fatalf("upstream half-close response: %q %v", body, err)
	}
	if _, err := io.WriteString(client, "client can still send"); err != nil {
		t.Fatal(err)
	}
	if err := client.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != "client can still send" {
			t.Fatalf("data following upstream half-close: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("upstream half-close terminated the other copy direction")
	}
}

func TestProjectProxyRejection(t *testing.T) {
	for _, kind := range []string{"methods", "authentication", "connection", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			socks := projectProxyTestSOCKS(t, func(conn net.Conn) error {
				method := byte(0)
				if kind == "methods" {
					method = 255
				} else if kind == "authentication" {
					method = 2
				}
				if err := projectProxyTestGreeting(conn, method); err != nil {
					return err
				}
				if kind == "methods" {
					return nil
				}
				if kind == "authentication" {
					return projectProxyTestAuth(conn, 1)
				}
				if _, err := projectProxyTestReadTarget(conn); err != nil {
					return err
				}
				reply := []byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0}
				if kind == "malformed" {
					reply = []byte{4, 0, 0, 1}
				}
				_, err := conn.Write(reply)
				return err
			})
			socks = strings.Replace(socks, "://", "://user:p%40ss%3Aword@", 1)
			env, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTPS_PROXY=" + socks})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			proxy := projectProxyTestEnvValue(t, env, "HTTPS_PROXY")
			for _, scheme := range []string{"http", "https"} {
				client := projectProxyTestClient(t, proxy)
				response, err := client.Get(scheme + "://rejected.invalid/path")
				if scheme == "http" {
					if err != nil {
						t.Fatal(err)
					}
					body, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode != http.StatusBadGateway || bytes.Contains(body, []byte("p@ss")) || bytes.Contains(body, []byte("user")) {
						t.Fatalf("unexpected HTTP failure response: %d %q", response.StatusCode, body)
					}
				} else if err == nil || !strings.Contains(err.Error(), http.StatusText(http.StatusBadGateway)) || strings.Contains(err.Error(), "p@ss") {
					t.Fatalf("unexpected CONNECT failure: %v", err)
				}
			}
		})
	}
}

func TestProjectProxyCleanupAndCancellation(t *testing.T) {
	for _, cancelContext := range []bool{false, true} {
		for _, mode := range []string{"http_handshake", "http_idle", "connect_handshake", "tunnel"} {
			t.Run(fmt.Sprintf("cancel_%t/%s", cancelContext, mode), func(t *testing.T) {
				connected := make(chan struct{})
				disconnected := make(chan struct{})
				socks := projectProxyTestSOCKS(t, func(conn net.Conn) error {
					if mode == "tunnel" || mode == "http_idle" {
						if err := projectProxyTestGreeting(conn, 0); err != nil {
							return err
						}
						if _, err := projectProxyTestReadTarget(conn); err != nil {
							return err
						}
						if _, err := conn.Write([]byte{5, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
							return err
						}
					}
					if mode == "http_idle" {
						request, err := http.ReadRequest(bufio.NewReader(conn))
						if err != nil {
							return err
						}
						_ = request.Body.Close()
						if _, err := io.WriteString(conn, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"); err != nil {
							return err
						}
					}
					close(connected)
					_, _ = io.Copy(io.Discard, conn)
					close(disconnected)
					return nil
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				env, cleanup, err := prepareProjectProxy(ctx, []string{"HTTPS_PROXY=" + socks})
				if err != nil {
					t.Fatal(err)
				}
				defer cleanup()
				u, _ := url.Parse(projectProxyTestEnvValue(t, env, "HTTPS_PROXY"))
				client, err := net.Dial("tcp", u.Host)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				_ = client.SetDeadline(time.Now().Add(5 * time.Second))
				request := "CONNECT cleanup.invalid:443 HTTP/1.1\r\nHost: cleanup.invalid:443\r\n\r\n"
				if mode == "http_handshake" || mode == "http_idle" {
					request = "GET http://cleanup.invalid/ HTTP/1.1\r\nHost: cleanup.invalid\r\n\r\n"
				}
				if _, err := io.WriteString(client, request); err != nil {
					t.Fatal(err)
				}
				select {
				case <-connected:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream connection was not established")
				}
				if mode == "tunnel" {
					response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodConnect})
					if err != nil || response.StatusCode != http.StatusOK {
						t.Fatalf("CONNECT response: %v %v", response, err)
					}
				}
				if mode == "http_idle" {
					response, err := http.ReadResponse(bufio.NewReader(client), &http.Request{Method: http.MethodGet})
					if err != nil {
						t.Fatal(err)
					}
					body, err := io.ReadAll(response.Body)
					_ = response.Body.Close()
					if err != nil || string(body) != "ok" {
						t.Fatalf("idle HTTP response: %q %v", body, err)
					}
				}
				finished := make(chan struct{})
				go func() {
					if cancelContext {
						cancel()
					} else {
						cleanup()
					}
					close(finished)
				}()
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Fatal("cleanup did not return promptly")
				}
				select {
				case <-disconnected:
				case <-time.After(2 * time.Second):
					t.Fatal("upstream survived cleanup/cancellation")
				}
				cleanup() // Also waits for the context-triggered cleanup to finish.
				_, readErr := io.ReadAll(client)
				if timeout, ok := readErr.(net.Error); ok && timeout.Timeout() {
					t.Fatal("child connection survived cleanup")
				}
				conn, err := net.DialTimeout("tcp", u.Host, time.Second)
				if err == nil {
					conn.Close()
					t.Fatal("listener survived cleanup/cancellation")
				}
			})
		}
	}
}

func TestProjectProxyInvalidConfiguration(t *testing.T) {
	for _, value := range []string{
		"socks5://user:topsecret@", "socks5://user:topsecret@localhost:bad", "socks5://user:topsecret@localhost:0",
		"socks5://user:topsecret@localhost:1080/path", "socks5://user:%zz@localhost:1080",
		"socks5://" + strings.Repeat("u", 256) + ":topsecret@localhost:1080",
		"http://user:topsecret@", "http://user:topsecret@localhost:bad", "https://user:topsecret@localhost:0",
		"https://user:topsecret@localhost:443/path", "http://user:%zz@localhost:80", "http://localhost:65536",
	} {
		_, cleanup, err := prepareProjectProxy(context.Background(), []string{"HTTP_PROXY=" + value})
		cleanup()
		if err == nil || strings.Contains(err.Error(), "topsecret") || strings.Contains(err.Error(), "user") || strings.Contains(err.Error(), "%zz") {
			t.Fatalf("configuration error must omit credentials: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, cleanup, err := prepareProjectProxy(ctx, []string{"HTTP_PROXY=socks5://127.0.0.1:10808"})
	cleanup()
	if err != context.Canceled {
		t.Fatalf("already canceled context: %v", err)
	}
	env := []string{"HTTP_PROXY=ftp://user:secret@localhost:8080", "HTTPS_PROXY=custom://%zz", "https_proxy=", "HOME=/home/test"}
	got, cleanup, err := prepareProjectProxy(context.Background(), env)
	defer cleanup()
	if err != nil || !reflect.DeepEqual(got, env) {
		t.Fatalf("unsupported proxy environment changed: %v", err)
	}
}
