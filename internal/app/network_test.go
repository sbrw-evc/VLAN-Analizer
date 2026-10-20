package app

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func localWebsite(t *testing.T, tls bool) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, "ok") }))
	_ = server.Listener.Close()
	listener, e := net.Listen("tcp4", "127.0.0.1:8080")
	if e != nil {
		t.Skip("loopback port 8080 unavailable")
	}
	server.Listener = listener
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(server.Close)
	return server
}
func TestBoundProxyHTTPAndConnect(t *testing.T) {
	_ = localWebsite(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	proxy, e := NewBoundProxy(ctx, &BoundDialer{Interface: "lo", Source: "127.0.0.1"})
	if e != nil {
		t.Fatal(e)
	}
	defer proxy.Close()
	u, _ := url.Parse(proxy.URL())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 3 * time.Second}
	resp, e := client.Get("http://127.0.0.1:8080/")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	conn, e := net.Dial("tcp4", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprint(conn, "CONNECT 127.0.0.1:8080 HTTP/1.1\r\nHost: 127.0.0.1:8080\r\n\r\n")
	reader := bufio.NewReader(conn)
	response, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if e != nil || response.StatusCode != 200 {
		t.Fatal("CONNECT failed", e)
	}
	fmt.Fprint(conn, "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")
	response, e = http.ReadResponse(reader, &http.Request{Method: "GET"})
	if e != nil || response.StatusCode != 200 {
		t.Fatal("tunnel request failed", e)
	}
	response.Body.Close()
}
func TestBoundDialNeverFallsBack(t *testing.T) {
	_ = localWebsite(t, false)
	d := BoundDialer{Interface: "not-a-device", Source: "127.0.0.1"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if c, e := d.DialContext(ctx, "tcp4", "127.0.0.1:8080"); e == nil {
		c.Close()
		t.Fatal("invalid device silently used default route")
	}
}
func TestProbeHTTPAndInvalidTLS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprint(secure), func(t *testing.T) {
			server := localWebsite(t, secure)
			scan := DefaultScan()
			scan.Browser = false
			scan.Timeout = 5
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			result := (NetworkProber{}).Check(ctx, Site{ID: "probe", URL: server.URL}, Interface{Name: "lo", Addresses: []string{"127.0.0.1/8"}}, scan)
			if secure {
				info, ok := result["tls"].(Object)
				if !ok || info["valid"] != false || info["sha256"] == nil || result["status"] != "down" {
					t.Fatal(result)
				}
			} else if result["status"] != "up" {
				t.Fatal(result)
			}
		})
	}
}
func TestProxyCloseCancelsTunnels(t *testing.T) {
	_ = localWebsite(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	proxy, e := NewBoundProxy(ctx, &BoundDialer{Interface: "lo", Source: "127.0.0.1"})
	if e != nil {
		t.Fatal(e)
	}
	u, _ := url.Parse(proxy.URL())
	conn, e := net.Dial("tcp4", u.Host)
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(time.Second))
	fmt.Fprint(conn, "CONNECT 127.0.0.1:8080 HTTP/1.1\r\nHost: 127.0.0.1:8080\r\n\r\n")
	if _, e = http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"}); e != nil {
		t.Fatal(e)
	}
	proxy.Close()
	if _, e = conn.Read(make([]byte, 1)); e == nil {
		t.Fatal("hijacked connection remained open")
	}
}

func TestManagedHTTPUsesProtectedAddressMap(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "managed-service") }))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	file := t.TempDir() + "/addresses.json"
	t.Setenv("MANAGED_ADDRESS_FILE", file)
	if e := atomicJSON(file, map[string]string{"managed-openbao:8200": target.Host}); e != nil {
		t.Fatal(e)
	}
	client := httpClient(time.Second)
	response, e := client.Get("http://managed-openbao:8200/v1/sys/seal-status")
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	// Replacing the map changes subsequent connections and never attempts DNS on
	// a reserved alias when its mapping is missing.
	if e := atomicJSON(file, map[string]string{}); e != nil {
		t.Fatal(e)
	}
	client.CloseIdleConnections()
	if response, e = client.Get("http://managed-openbao:8200/"); e == nil {
		response.Body.Close()
		t.Fatal("missing mapping did not fail closed")
	}
}
