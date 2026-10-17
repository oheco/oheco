package manager

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const projectProxyHandshakeTimeout = 10 * time.Second

// prepareProjectProxy adapts supported proxy variables to local HTTP proxies for
// tools which require HTTP proxy credentials. The environment and listeners belong
// to this operation; cleanup must wait until all child networking commands exit.
func prepareProjectProxy(ctx context.Context, env []string) (newEnv []string, cleanup func(), err error) {
	proxyCtx, cancel := context.WithCancel(ctx)
	proxies := make(map[string]*projectProxy)
	done := make(chan struct{})
	var once sync.Once
	cleanup = func() {
		once.Do(func() {
			cancel()
			for _, proxy := range proxies {
				proxy.close()
			}
			close(done)
		})
	}
	fail := func(err error) ([]string, func(), error) {
		cleanup()
		return nil, func() {}, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	newEnv = append([]string(nil), env...)
	for i, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || (key != "HTTP_PROXY" && key != "HTTPS_PROXY" && key != "http_proxy" && key != "https_proxy") {
			continue
		}
		scheme, _, _ := strings.Cut(value, ":")
		switch strings.ToLower(scheme) {
		case "socks5", "socks5h", "http", "https":
		default:
			continue
		}
		upstream, err := projectProxyURL(value)
		if err != nil {
			return fail(err)
		}
		proxy := proxies[upstream.String()]
		if proxy == nil {
			proxy, err = newProjectProxy(proxyCtx, upstream)
			if err != nil {
				return fail(err)
			}
			proxies[upstream.String()] = proxy
		}
		// CLI 1.3.5 always supplies an auth object; recent Axios rejects an
		// empty object. This local-only placeholder is stripped by our proxy.
		newEnv[i] = key + "=http://oheco:local@" + proxy.listener.Addr().String()
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	go func() {
		select {
		case <-ctx.Done():
			cleanup()
		case <-done:
		}
	}()
	return newEnv, cleanup, nil
}

// Never include a supplied URL in an error: it may contain proxy credentials.
func projectProxyURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid project proxy configuration")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	defaultPort := "1080"
	switch u.Scheme {
	case "socks5", "socks5h":
		// Both schemes delegate target DNS to SOCKS.
		u.Scheme = "socks5"
	case "http":
		defaultPort = "80"
	case "https":
		defaultPort = "443"
	default:
		return nil, errors.New("unsupported project proxy scheme")
	}
	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("invalid project proxy port")
	}
	if u.Scheme == "socks5" && u.User != nil {
		password, _ := u.User.Password()
		if len(u.User.Username()) == 0 || len(u.User.Username()) > 255 || len(password) > 255 {
			return nil, errors.New("invalid project SOCKS proxy credentials")
		}
	}
	// Aliases of the same upstream share a listener, including default ports.
	u.Host = net.JoinHostPort(strings.ToLower(u.Hostname()), strconv.Itoa(n))
	u.Path, u.RawPath = "", ""
	return u, nil
}

type projectProxy struct {
	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	upstream  *url.URL
	ctx       context.Context
	mu        sync.Mutex
	closed    bool
	conns     map[net.Conn]struct{}
	handlers  sync.WaitGroup
}

func newProjectProxy(ctx context.Context, upstream *url.URL) (*projectProxy, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errors.New("cannot start project HTTP proxy listener")
	}
	p := &projectProxy{
		listener: listener,
		upstream: upstream,
		ctx:      ctx,
		conns:    make(map[net.Conn]struct{}),
		transport: &http.Transport{
			Proxy:                 http.ProxyURL(upstream),
			TLSHandshakeTimeout:   projectProxyHandshakeTimeout,
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
	}
	p.transport.DialContext = p.dialHTTP
	p.server = &http.Server{
		Handler:           p,
		ReadHeaderTimeout: projectProxyHandshakeTimeout,
		IdleTimeout:       30 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	go func() { _ = p.server.Serve(listener) }()
	return p, nil
}

func (p *projectProxy) close() {
	p.mu.Lock()
	p.closed = true
	conns := make([]net.Conn, 0, len(p.conns))
	for conn := range p.conns {
		conns = append(conns, conn)
	}
	p.mu.Unlock()
	// Serve may not yet have registered the listener when cleanup wins the
	// startup race, so close the listener explicitly as well as the server.
	_ = p.listener.Close()
	_ = p.server.Close()
	for _, conn := range conns {
		_ = conn.Close()
	}
	p.transport.CloseIdleConnections()
	p.handlers.Wait()
	p.transport.CloseIdleConnections()
}

func (p *projectProxy) track(conn net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = conn.Close()
		return false
	}
	p.conns[conn] = struct{}{}
	return true
}

func (p *projectProxy) untrack(conn net.Conn) {
	_ = conn.Close()
	p.mu.Lock()
	delete(p.conns, conn)
	p.mu.Unlock()
}

func (p *projectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		http.Error(w, "project proxy is closed", http.StatusServiceUnavailable)
		return
	}
	p.handlers.Add(1)
	p.mu.Unlock()
	defer p.handlers.Done()
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Host == "" || (r.URL.Scheme != "http" && r.URL.Scheme != "https") {
		http.Error(w, "invalid proxy request", http.StatusBadRequest)
		return
	}
	// Transport performs proxy/TLS handshakes after DialContext returns.
	// Bound those handshakes with a socket deadline, then remove the deadline
	// once Transport has a usable connection, allowing long response bodies.
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		_ = info.Conn.SetDeadline(time.Time{})
	}}
	request := r.Clone(httptrace.WithClientTrace(r.Context(), trace))
	request.RequestURI = ""
	projectProxyRemoveHopHeaders(request.Header)
	response, err := p.transport.RoundTrip(request)
	if err != nil {
		http.Error(w, "project proxy upstream request failed", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	projectProxyRemoveHopHeaders(response.Header)
	for name, values := range response.Header {
		w.Header()[name] = values
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

// Track Transport's sockets as well as hijacked tunnels. An upstream that stalls
// during its handshake must be interrupted immediately on cleanup.
func (p *projectProxy) dialHTTP(ctx context.Context, network, address string) (net.Conn, error) {
	dialCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(p.ctx, cancel)
	defer stop()
	defer cancel()
	conn, err := (&net.Dialer{Timeout: projectProxyHandshakeTimeout, KeepAlive: 30 * time.Second}).DialContext(dialCtx, network, address)
	if err != nil {
		return nil, errors.New("cannot connect to project proxy upstream")
	}
	if !p.track(conn) {
		return nil, errors.New("project proxy is closed")
	}
	if err := conn.SetDeadline(time.Now().Add(projectProxyHandshakeTimeout)); err != nil {
		p.untrack(conn)
		return nil, errors.New("cannot set proxy handshake deadline")
	}
	return &projectProxyTransportConn{Conn: conn, proxy: p}, nil
}

type projectProxyTransportConn struct {
	net.Conn
	proxy *projectProxy
}

func (c *projectProxyTransportConn) Close() error {
	c.proxy.untrack(c.Conn)
	return nil
}

func (c *projectProxyTransportConn) CloseWrite() error {
	if conn, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return conn.CloseWrite()
	}
	return errors.New("tunnel cannot half-close")
}

func projectProxyRemoveHopHeaders(header http.Header) {
	for _, value := range header.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			header.Del(strings.TrimSpace(name))
		}
	}
	for _, name := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		header.Del(name)
	}
}

func (p *projectProxy) connect(w http.ResponseWriter, r *http.Request) {
	var upstream net.Conn
	var reader io.Reader
	var err error
	if p.upstream.Scheme == "socks5" {
		upstream, err = p.dialSOCKS(r.Context(), r.Host)
		reader = upstream
	} else {
		upstream, reader, err = p.dialHTTPConnect(r.Context(), r.Host)
	}
	if err != nil {
		http.Error(w, "project proxy upstream connection failed", http.StatusBadGateway)
		return
	}
	if p.upstream.Scheme == "socks5" {
		upstream = &projectProxyTransportConn{Conn: upstream, proxy: p}
	}
	defer upstream.Close()
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "project proxy cannot create tunnel", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	if !p.track(client) {
		return
	}
	defer p.untrack(client)
	_ = client.SetDeadline(time.Time{})
	if _, err = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = buffered.Flush(); err != nil {
		return
	}
	copyHalf := func(destination net.Conn, source io.Reader) {
		_, err := io.Copy(destination, source)
		if err == nil {
			if tcp, ok := destination.(interface{ CloseWrite() error }); ok {
				err = tcp.CloseWrite()
			} else {
				err = errors.New("tunnel cannot half-close")
			}
		}
		if err != nil {
			_ = client.Close()
			_ = upstream.Close()
		}
	}
	copied := make(chan struct{})
	go func() {
		// Hijack may already have read bytes following the CONNECT headers.
		copyHalf(upstream, buffered.Reader)
		close(copied)
	}()
	copyHalf(client, reader)
	<-copied
}

func (p *projectProxy) dialHTTPConnect(ctx context.Context, target string) (conn net.Conn, reader io.Reader, err error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" {
		return nil, nil, errors.New("invalid HTTP proxy target")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, nil, errors.New("invalid HTTP proxy target port")
	}
	socket, err := p.dialHTTP(ctx, "tcp", p.upstream.Host)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = socket.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = socket.Close() })
	defer stop()
	conn = socket
	if p.upstream.Scheme == "https" {
		secure := tls.Client(socket, &tls.Config{ServerName: p.upstream.Hostname()})
		if err = secure.HandshakeContext(ctx); err != nil {
			return nil, nil, errors.New("HTTPS proxy TLS handshake failed")
		}
		conn = secure
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: target},
		Host:   target,
		Header: make(http.Header),
	}
	if p.upstream.User != nil {
		password, _ := p.upstream.User.Password()
		credentials := p.upstream.User.Username() + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err = request.Write(conn); err != nil {
		return nil, nil, errors.New("HTTP proxy CONNECT request failed")
	}
	buffered := bufio.NewReader(conn)
	response, err := http.ReadResponse(buffered, request)
	if err != nil {
		return nil, nil, errors.New("invalid HTTP proxy CONNECT response")
	}
	if response.StatusCode != http.StatusOK {
		return nil, nil, errors.New("HTTP proxy rejected CONNECT")
	}
	if err = ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return nil, nil, errors.New("cannot clear proxy handshake deadline")
	}
	// The proxy may send tunnel bytes together with its CONNECT response.
	return conn, buffered, nil
}

func (p *projectProxy) dialSOCKS(ctx context.Context, target string) (conn net.Conn, err error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil || host == "" || len(host) > 255 {
		return nil, errors.New("invalid SOCKS target")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, errors.New("invalid SOCKS target port")
	}
	conn, err = (&net.Dialer{Timeout: projectProxyHandshakeTimeout}).DialContext(ctx, "tcp", p.upstream.Host)
	if err != nil {
		return nil, errors.New("cannot connect to SOCKS proxy")
	}
	if !p.track(conn) {
		return nil, errors.New("project proxy is closed")
	}
	defer func() {
		if err != nil {
			p.untrack(conn)
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline := time.Now().Add(projectProxyHandshakeTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return conn, errors.New("cannot set SOCKS handshake deadline")
	}
	if err = p.socksHandshake(conn, host, uint16(portNumber)); err != nil {
		return conn, err
	}
	if err = ctx.Err(); err != nil {
		return conn, err
	}
	if err = conn.SetDeadline(time.Time{}); err != nil {
		return conn, errors.New("cannot clear SOCKS handshake deadline")
	}
	return conn, nil
}

func (p *projectProxy) socksHandshake(conn net.Conn, host string, port uint16) error {
	greeting := []byte{5, 1, 0}
	if p.upstream.User != nil {
		greeting = []byte{5, 2, 0, 2}
	}
	if _, err := conn.Write(greeting); err != nil {
		return errors.New("SOCKS greeting failed")
	}
	var reply [2]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 5 {
		return errors.New("invalid SOCKS greeting response")
	}
	switch reply[1] {
	case 0:
	case 2:
		if p.upstream.User == nil {
			return errors.New("SOCKS proxy requires credentials")
		}
		username := p.upstream.User.Username()
		password, _ := p.upstream.User.Password()
		auth := append([]byte{1, byte(len(username))}, username...)
		auth = append(auth, byte(len(password)))
		auth = append(auth, password...)
		if _, err := conn.Write(auth); err != nil {
			return errors.New("SOCKS authentication failed")
		}
		if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 1 || reply[1] != 0 {
			return errors.New("SOCKS authentication rejected")
		}
	default:
		return errors.New("SOCKS proxy rejected authentication methods")
	}
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, port)
	if _, err := conn.Write(request); err != nil {
		return errors.New("SOCKS connection request failed")
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 5 || header[2] != 0 {
		return errors.New("invalid SOCKS connection response")
	}
	if header[1] != 0 {
		return errors.New("SOCKS proxy rejected connection")
	}
	var addressLength int
	switch header[3] {
	case 1:
		addressLength = net.IPv4len
	case 4:
		addressLength = net.IPv6len
	case 3:
		var size [1]byte
		if _, err := io.ReadFull(conn, size[:]); err != nil || size[0] == 0 {
			return errors.New("invalid SOCKS bound address")
		}
		addressLength = int(size[0])
	default:
		return errors.New("invalid SOCKS bound address type")
	}
	if _, err := io.CopyN(io.Discard, conn, int64(addressLength+2)); err != nil {
		return errors.New("incomplete SOCKS connection response")
	}
	return nil
}
