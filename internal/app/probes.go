package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

func parseWebURL(raw string) (*url.URL, error) {
	if e := validateURL(raw); e != nil {
		return nil, e
	}
	return url.Parse(raw)
}
func milliseconds(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }
func certificate(c *x509.Certificate, state tls.ConnectionState) Object {
	hash := sha256.Sum256(c.Raw)
	return Object{"subject": c.Subject.String(), "issuer": c.Issuer.String(), "san": c.DNSNames, "not_before": c.NotBefore.UTC().Format(time.RFC3339), "not_after": c.NotAfter.UTC().Format(time.RFC3339), "expires_days": int(time.Until(c.NotAfter).Hours() / 24), "sha256": hex.EncodeToString(hash[:]), "version": tls.VersionName(state.Version), "cipher": tls.CipherSuiteName(state.CipherSuite)}
}

type Chromium struct{}

func (Chromium) Check(ctx context.Context, proxy, raw string) Object {
	options := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	options = append(options, chromedp.NoSandbox, chromedp.ProxyServer(proxy), chromedp.Flag("proxy-bypass-list", "<-loopback>"), chromedp.Flag("disable-quic", true), chromedp.Flag("host-resolver-rules", "MAP * ~NOTFOUND, EXCLUDE 127.0.0.1"), chromedp.Flag("force-webrtc-ip-handling-policy", "disable_non_proxied_udp"))
	alloc, stop := chromedp.NewExecAllocator(ctx, options...)
	defer stop()
	browser, cancel := chromedp.NewContext(alloc)
	defer cancel()
	var mu sync.Mutex
	bad, failed := []Object{}, []Object{}
	chromedp.ListenTarget(browser, func(event any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := event.(type) {
		case *network.EventResponseReceived:
			if e.Response.Status >= 400 && len(bad) < 50 {
				bad = append(bad, Object{"url": e.Response.URL, "status": e.Response.Status})
			}
		case *network.EventLoadingFailed:
			if len(failed) < 50 {
				failed = append(failed, Object{"error": e.ErrorText})
			}
		}
	})
	start := time.Now()
	var timing Object
	err := chromedp.Run(browser, network.Enable(), chromedp.Navigate(raw), chromedp.Evaluate(`(() => {const n=performance.getEntriesByType('navigation')[0];return {status:n.responseStatus || 200,load_ms:n.loadEventEnd,dom_ms:n.domContentLoadedEventEnd,ttfb_ms:n.responseStart,resources:performance.getEntriesByType('resource').length,final_url:location.href};})()`, &timing))
	mu.Lock()
	defer mu.Unlock()
	if timing == nil {
		timing = Object{}
	}
	timing["elapsed_ms"] = milliseconds(start)
	timing["failed_requests"] = failed
	timing["bad_responses"] = bad
	timing["ok"] = err == nil && len(failed) == 0 && len(bad) == 0 && number(timing["status"]) < 400
	if err != nil {
		timing["error"] = "browser navigation failed or timed out"
	}
	return timing
}

type NetworkProber struct{ Browser Browser }

func (p NetworkProber) Check(ctx context.Context, site Site, iface Interface, scan ScanConfig) Object {
	result := Object{"at": timestamp(), "site_id": site.ID, "interface": iface.Name, "vlan_id": iface.VLAN, "status": "down", "errors": []Object{}}
	d := NewBoundDialer(iface, scan)
	host, port, e := webAddress(site.URL)
	if e != nil {
		return result
	}
	start := time.Now()
	ips, e := d.Resolve(ctx, host)
	if e != nil {
		result["errors"] = []Object{{"stage": "dns", "message": e.Error()}}
		return result
	}
	result["dns"] = Object{"addresses": ips, "servers": d.Servers, "ms": milliseconds(start)}
	tasks := make(chan Object, 4)
	var wg sync.WaitGroup
	stage := func(fn func() Object) { wg.Add(1); go func() { defer wg.Done(); tasks <- fn() }() }
	stage(func() Object {
		out := Object{}
		t := time.Now()
		conn, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(host, port))
		if err != nil {
			return Object{"tcp_error": "bound TCP connection failed"}
		}
		defer conn.Close()
		out["tcp_ms"] = milliseconds(t)
		out["remote_ip"] = conn.RemoteAddr().String()
		if strings.HasPrefix(site.URL, "https:") {
			t = time.Now()
			secure := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
			if err = secure.HandshakeContext(ctx); err == nil {
				state := secure.ConnectionState()
				info := certificate(state.PeerCertificates[0], state)
				info["valid"] = true
				info["handshake_ms"] = milliseconds(t)
				out["tls"] = info
			} else {
				info := Object{"valid": false, "error": "TLS or certificate verification failed"}
				if diagnostic, err := d.DialContext(ctx, "tcp4", net.JoinHostPort(host, port)); err == nil {
					defer diagnostic.Close()
					untrusted := tls.Client(diagnostic, &tls.Config{ServerName: host, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12})
					if untrusted.HandshakeContext(ctx) == nil {
						state := untrusted.ConnectionState()
						info = certificate(state.PeerCertificates[0], state)
						info["valid"] = false
						info["error"] = "certificate verification failed"
					}
				}
				out["tls"] = info
			}
		}
		return out
	})
	stage(func() Object {
		t := time.Now()
		var ttfb float64
		transport := &http.Transport{Proxy: nil, DialContext: d.DialContext, ForceAttemptHTTP2: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: time.Duration(scan.Timeout) * time.Second}
		defer transport.CloseIdleConnections()
		redirects := 0
		client := &http.Client{Transport: transport, Timeout: time.Duration(scan.Timeout) * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			redirects = len(via)
			if len(via) > 5 {
				return fmt.Errorf("redirect limit")
			}
			return validateURL(req.URL.String())
		}}
		trace := &httptrace.ClientTrace{GotFirstResponseByte: func() { ttfb = milliseconds(t) }}
		req, _ := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace), "GET", site.URL, nil)
		resp, err := client.Do(req)
		if err != nil {
			return Object{"http_error": "bound HTTP request failed"}
		}
		defer resp.Body.Close()
		size, err := io.Copy(io.Discard, io.LimitReader(resp.Body, (10<<20)+1))
		out := Object{"http": Object{"code": resp.StatusCode, "url": resp.Request.URL.String(), "ttfb_ms": ttfb, "total_ms": milliseconds(t), "bytes": size, "redirects": redirects}}
		if err != nil || size > 10<<20 {
			out["http_error"] = "HTTP body incomplete or exceeds 10 MiB"
		}
		return out
	})
	if scan.Browser && p.Browser != nil {
		stage(func() Object {
			proxy, err := NewBoundProxy(ctx, d)
			if err != nil {
				return Object{"browser": Object{"ok": false, "error": "proxy initialization failed"}}
			}
			defer proxy.Close()
			browserCtx, cancel := context.WithTimeout(ctx, time.Duration(scan.Timeout)*time.Second)
			defer cancel()
			return Object{"browser": p.Browser.Check(browserCtx, proxy.URL(), site.URL)}
		})
	}
	stage(func() Object {
		traceCtx, cancel := context.WithTimeout(ctx, 16*time.Second)
		defer cancel()
		data, err := command(traceCtx, "traceroute", "-n", "-i", iface.Name, "-s", d.Source, "-m", "12", "-q", "1", "-w", "1", ips[0])
		if err != nil {
			return Object{"trace": Object{"error": "traceroute unavailable or timed out"}}
		}
		return Object{"trace": Object{"output": string(data), "method": "udp"}}
	})
	go func() { wg.Wait(); close(tasks) }()
	for data := range tasks {
		for k, v := range data {
			result[k] = v
		}
	}
	httpInfo, _ := result["http"].(Object)
	if result["tcp_ms"] != nil && httpInfo != nil && number(httpInfo["code"]) >= 200 && number(httpInfo["code"]) < 400 && result["http_error"] == nil {
		result["status"] = "up"
		if browser, ok := result["browser"].(Object); ok && !truth(browser["ok"]) {
			result["status"] = "degraded"
		}
	}
	if tlsInfo, ok := result["tls"].(Object); ok && !truth(tlsInfo["valid"]) {
		result["status"] = "down"
	}
	return result
}
