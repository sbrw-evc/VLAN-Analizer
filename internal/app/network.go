package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func command(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	data, e := cmd.Output()
	if e != nil {
		return nil, fmt.Errorf("%s failed", name)
	}
	return data, nil
}

type LinuxInventory struct{}

func (LinuxInventory) Discover(ctx context.Context, include []string) (Inventory, error) {
	out := Inventory{Interfaces: []Interface{}, Bridge: []Object{}}
	raw, e := command(ctx, "ip", "-j", "-d", "address", "show")
	if e != nil {
		return out, e
	}
	var links []struct {
		Name   string   `json:"ifname"`
		Index  int      `json:"ifindex"`
		Flags  []string `json:"flags"`
		State  string   `json:"operstate"`
		MTU    int      `json:"mtu"`
		MAC    string   `json:"address"`
		Link   any      `json:"link"`
		Master any      `json:"master"`
		Info   struct {
			Kind string `json:"info_kind"`
			Data struct {
				ID *int `json:"id"`
			} `json:"info_data"`
		} `json:"linkinfo"`
		Addresses []struct {
			Family, Local string
			Prefix        int `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if e = json.Unmarshal(raw, &links); e != nil {
		return out, e
	}
	raw, e = command(ctx, "ip", "-j", "route", "show", "table", "all")
	if e != nil {
		return out, e
	}
	var routes []Object
	if e = json.Unmarshal(raw, &routes); e != nil {
		return out, e
	}
	allowed := map[string]bool{}
	for _, n := range include {
		allowed[n] = true
	}
	for _, l := range links {
		if l.Name == "lo" || (len(allowed) > 0 && !allowed[l.Name]) {
			continue
		}
		i := Interface{Name: l.Name, Index: l.Index, Kind: l.Info.Kind, Parent: l.Link, Master: l.Master, MAC: l.MAC, MTU: l.MTU, State: l.State, Addresses: []string{}, IPv6: []string{}, Routes: []Object{}}
		if i.Kind == "" {
			i.Kind = "physical"
		}
		if i.Kind == "vlan" {
			i.VLAN = l.Info.Data.ID
		}
		for _, a := range l.Addresses {
			if a.Family == "inet" {
				i.Addresses = append(i.Addresses, fmt.Sprintf("%s/%d", a.Local, a.Prefix))
			} else if a.Family == "inet6" {
				i.IPv6 = append(i.IPv6, fmt.Sprintf("%s/%d", a.Local, a.Prefix))
			}
		}
		up := false
		for _, f := range l.Flags {
			up = up || f == "UP"
		}
		i.Probeable = up && len(i.Addresses) > 0 && l.Master == nil
		i.Eligible = i.Probeable
		for _, r := range routes {
			if text(r["dev"]) == i.Name {
				i.Routes = append(i.Routes, r)
			}
		}
		out.Interfaces = append(out.Interfaces, i)
	}
	if raw, e = command(ctx, "bridge", "-j", "vlan", "show"); e == nil {
		_ = json.Unmarshal(raw, &out.Bridge)
	}
	return out, nil
}

type BoundDialer struct {
	Interface, Source string
	Servers           []string
	mu                sync.RWMutex
	resolved          map[string][]string
}

func NewBoundDialer(i Interface, c ScanConfig) *BoundDialer {
	servers := c.DNS[i.Name]
	if len(servers) == 0 {
		data, _ := os.ReadFile("/etc/resolv.conf")
		for _, l := range strings.Split(string(data), "\n") {
			f := strings.Fields(l)
			if len(f) > 1 && f[0] == "nameserver" && net.ParseIP(f[1]) != nil && net.ParseIP(f[1]).To4() != nil {
				servers = append(servers, f[1])
			}
		}
	}
	return &BoundDialer{Interface: i.Name, Source: strings.Split(i.Addresses[0], "/")[0], Servers: servers}
}
func (d *BoundDialer) control(network, address string, c syscall.RawConn) error {
	var socketErr error
	err := c.Control(func(fd uintptr) {
		socketErr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, d.Interface)
	})
	if err != nil {
		return err
	}
	return socketErr
}
func (d *BoundDialer) direct(ctx context.Context, network, address string) (net.Conn, error) {
	dial := net.Dialer{Timeout: 8 * time.Second, Control: d.control}
	if strings.HasPrefix(network, "udp") {
		dial.LocalAddr = &net.UDPAddr{IP: net.ParseIP(d.Source)}
	} else {
		dial.LocalAddr = &net.TCPAddr{IP: net.ParseIP(d.Source)}
	}
	return dial.DialContext(ctx, network, address)
}
func (d *BoundDialer) Resolve(ctx context.Context, host string) ([]string, error) {
	if ip := net.ParseIP(host); ip != nil {
		if ip.To4() == nil {
			return nil, errors.New("IPv6 probes are unsupported")
		}
		return []string{ip.String()}, nil
	}
	// Each dialer belongs to one check. Reuse that check's DNS answer across
	// concurrent HTTP/TCP/browser stages; the next check resolves afresh.
	d.mu.RLock()
	cached := d.resolved[host]
	d.mu.RUnlock()
	if len(cached) > 0 {
		return cached, nil
	}
	if len(d.Servers) == 0 {
		return nil, errors.New("no IPv4 resolver configured")
	}
	var last error
	for _, server := range d.Servers {
		resolver := net.Resolver{PreferGo: true, StrictErrors: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			if strings.HasPrefix(network, "tcp") {
				network = "tcp4"
			} else {
				network = "udp4"
			}
			return d.direct(ctx, network, net.JoinHostPort(server, "53"))
		}}
		lookup, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, e := resolver.LookupIP(lookup, "ip4", strings.TrimSuffix(host, ".")+".")
		cancel()
		if e == nil && len(ips) > 0 {
			out := []string{}
			for _, ip := range ips {
				out = append(out, ip.String())
			}
			d.mu.Lock()
			if d.resolved == nil {
				d.resolved = map[string][]string{}
			}
			d.resolved[host] = out
			d.mu.Unlock()
			return out, nil
		}
		last = e
	}
	return nil, fmt.Errorf("bound DNS resolution failed: %v", last)
}
func (d *BoundDialer) DialContext(ctx context.Context, _, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := d.Resolve(ctx, host)
	if e != nil {
		return nil, e
	}
	for _, ip := range ips {
		var conn net.Conn
		conn, e = d.direct(ctx, "tcp4", net.JoinHostPort(ip, port))
		if e == nil {
			return conn, nil
		}
	}
	return nil, e
}
func webAddress(raw string) (string, string, error) {
	u, e := parseWebURL(raw)
	if e != nil {
		return "", "", e
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return u.Hostname(), port, nil
}
func validPort(p string) bool {
	n, e := strconv.Atoi(p)
	return e == nil && (n == 80 || n == 443 || n == 8080 || n == 8443)
}
