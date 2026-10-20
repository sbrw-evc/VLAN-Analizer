package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// BoundProxy owns every upstream dial and explicitly closes hijacked CONNECT sockets.
type BoundProxy struct {
	dial        *BoundDialer
	server      *http.Server
	listener    net.Listener
	transport   *http.Transport
	mu          sync.Mutex
	closed      bool
	connections map[net.Conn]bool
	slots       chan struct{}
}

func NewBoundProxy(ctx context.Context, d *BoundDialer) (*BoundProxy, error) {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return nil, e
	}
	p := &BoundProxy{dial: d, listener: l, connections: map[net.Conn]bool{}, slots: make(chan struct{}, 64)}
	p.transport = &http.Transport{DialContext: d.DialContext, Proxy: nil, DisableKeepAlives: true, ResponseHeaderTimeout: 15 * time.Second}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second, MaxHeaderBytes: 64 << 10, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = p.server.Serve(l) }()
	return p, nil
}
func (p *BoundProxy) URL() string { return "http://" + p.listener.Addr().String() }
func (p *BoundProxy) Close() {
	_ = p.server.Close()
	p.transport.CloseIdleConnections()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	for c := range p.connections {
		_ = c.Close()
	}
}
func (p *BoundProxy) track(c net.Conn, add bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if add {
		if p.closed {
			c.Close()
			return
		}
		p.connections[c] = true
	} else {
		delete(p.connections, c)
	}
}
func (p *BoundProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-r.Context().Done():
		return
	}
	if r.Method == "CONNECT" {
		host, port, e := net.SplitHostPort(r.Host)
		if e != nil || host == "" || !validPort(port) {
			http.Error(w, "invalid web destination", 400)
			return
		}
		up, e := p.dial.DialContext(r.Context(), "tcp4", r.Host)
		if e != nil {
			http.Error(w, "bound connection failed", 502)
			return
		}
		h, ok := w.(http.Hijacker)
		if !ok {
			up.Close()
			return
		}
		client, buffer, e := h.Hijack()
		if e != nil {
			up.Close()
			return
		}
		p.track(client, true)
		p.track(up, true)
		defer func() { client.Close(); up.Close(); p.track(client, false); p.track(up, false) }()
		_ = client.SetDeadline(time.Now().Add(2 * time.Minute))
		_ = up.SetDeadline(time.Now().Add(2 * time.Minute))
		_, _ = buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffer.Flush()
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(up, buffer); done <- struct{}{} }()
		go func() { _, _ = io.Copy(client, up); done <- struct{}{} }()
		<-done
		client.Close()
		up.Close()
		<-done
		return
	}
	if r.URL.Scheme != "http" || validateURL(r.URL.String()) != nil {
		http.Error(w, "invalid URL", 400)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	out.Header.Del("Connection")
	resp, e := p.transport.RoundTrip(out)
	if e != nil {
		http.Error(w, "bound request failed", 502)
		return
	}
	defer resp.Body.Close()
	for k, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

var _ http.Handler = (*BoundProxy)(nil)
