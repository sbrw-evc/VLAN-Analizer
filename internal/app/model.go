// Package app implements the application through small, consumer-owned interfaces.
package app

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var Version = "dev"

type Object = map[string]any

func text(v any) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}
func number(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
func truth(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return number(v) != 0
}
func randomKey(n int) string {
	b := make([]byte, n)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func timestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,64}$`)
var interfacePattern = regexp.MustCompile(`^[a-zA-Z0-9_.:@-]+$`)
var guidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}(?:-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)

func validateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("use an HTTP(S) URL without credentials")
	}
	if strings.Contains(u.Hostname(), ":") {
		return errors.New("IPv6 probes are not yet supported")
	}
	if u.Port() == "" || validPort(u.Port()) {
		return nil
	}
	return errors.New("allowed web ports: 80, 443, 8080, 8443")
}
func serverURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("invalid server URL")
	}
	return nil
}
func originURL(raw string) error {
	if e := serverURL(raw); e != nil {
		return e
	}
	u, _ := url.Parse(raw)
	if u.Path != "" {
		return errors.New("application URL must be an origin without a path")
	}
	return nil
}

type Site struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	Source        string `json:"source"`
	Enabled       int    `json:"enabled"`
	SourceEnabled int    `json:"source_enabled"`
	Version       uint64 `json:"version,omitempty"`
	Deleted       int    `json:"deleted"`
}

func (s Site) Active() bool { return s.Enabled == 1 && s.SourceEnabled == 1 && s.Deleted == 0 }

type Interface struct {
	Name      string   `json:"interface"`
	Index     int      `json:"ifindex"`
	Kind      string   `json:"kind"`
	VLAN      *int     `json:"vlan_id"`
	Parent    any      `json:"parent"`
	Master    any      `json:"master"`
	MAC       string   `json:"mac"`
	MTU       int      `json:"mtu"`
	State     string   `json:"state"`
	Addresses []string `json:"addresses"`
	IPv6      []string `json:"ipv6"`
	Routes    []Object `json:"routes"`
	Eligible  bool     `json:"eligible"`
	Probeable bool     `json:"probeable"`
}
type Inventory struct {
	Interfaces []Interface `json:"interfaces"`
	Bridge     []Object    `json:"bridge_vlans"`
}
type ScanConfig struct {
	Interfaces  []string            `json:"interfaces"`
	DNS         map[string][]string `json:"dns_servers"`
	Interval    int                 `json:"interval"`
	Concurrency int                 `json:"concurrency"`
	Timeout     int                 `json:"timeout"`
	StaleAfter  int                 `json:"stale_after"`
	Browser     bool                `json:"browser"`
}

func DefaultScan() ScanConfig {
	return ScanConfig{Interfaces: []string{}, DNS: map[string][]string{}, Interval: 120, Concurrency: 3, Timeout: 20, StaleAfter: 600, Browser: true}
}
func (c ScanConfig) Validate() error {
	if c.Interval < 30 || c.Interval > 86400 || c.Concurrency < 1 || c.Concurrency > 32 || c.Timeout < 5 || c.Timeout > 120 || c.StaleAfter < 60 || c.StaleAfter > 604800 {
		return errors.New("invalid scan limits")
	}
	for _, n := range c.Interfaces {
		if len(n) > 15 || !interfacePattern.MatchString(n) {
			return errors.New("invalid interface name")
		}
	}
	for _, ss := range c.DNS {
		if len(ss) == 0 || len(ss) > 4 {
			return errors.New("configure one to four DNS servers")
		}
		for _, s := range ss {
			if net.ParseIP(s) == nil || net.ParseIP(s).To4() == nil {
				return errors.New("DNS servers must be IPv4 addresses")
			}
		}
	}
	return nil
}

type BaoConfig struct {
	URL      string `json:"url"`
	Mount    string `json:"mount"`
	Mode     string `json:"mode"`
	Token    string `json:"token,omitempty"`
	RoleID   string `json:"role_id,omitempty"`
	SecretID string `json:"secret_id,omitempty"`
}
type CHConfig struct {
	URL      string `json:"url"`
	User     string `json:"user"`
	Database string `json:"database"`
	Password string `json:"password,omitempty"`
	Managed  bool   `json:"managed,omitempty"`
}
type GatusConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Path    string `json:"config_path"`
	Token   string `json:"token"`
}
type KumaConfig struct {
	Enabled  bool   `json:"enabled"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	User     string `json:"user"`
	TLS      bool   `json:"tls"`
	CA       string `json:"ca"`
	Instance string `json:"instance"`
	Password string `json:"password"`
}
type EntraConfig struct {
	Enabled  bool   `json:"enabled"`
	Tenant   string `json:"tenant"`
	ClientID string `json:"client_id"`
	Secret   string `json:"client_secret"`
}
type Connections struct {
	Gatus GatusConfig `json:"gatus"`
	Kuma  KumaConfig  `json:"kuma"`
	Entra EntraConfig `json:"entra"`
}

func DefaultConnections() Connections {
	return Connections{Kuma: KumaConfig{Port: 3306, Database: "kuma", User: "vlan_reader", TLS: true, Instance: "default"}}
}
func (e EntraConfig) Validate() error {
	if e.Enabled && (!guidPattern.MatchString(e.Tenant) || !guidPattern.MatchString(e.ClientID) || e.Secret == "") {
		return errors.New("Entra requires tenant/client UUIDs and a secret")
	}
	return nil
}
func (c Connections) Public(origin string) Object {
	return Object{"gatus": Object{"enabled": c.Gatus.Enabled, "url": c.Gatus.URL, "config_path": c.Gatus.Path, "token_set": c.Gatus.Token != ""}, "kuma": Object{"enabled": c.Kuma.Enabled, "host": c.Kuma.Host, "port": c.Kuma.Port, "database": c.Kuma.Database, "user": c.Kuma.User, "tls": c.Kuma.TLS, "ca": c.Kuma.CA, "instance": c.Kuma.Instance, "password_set": c.Kuma.Password != ""}, "entra": Object{"enabled": c.Entra.Enabled, "tenant": c.Entra.Tenant, "client_id": c.Entra.ClientID, "client_secret_set": c.Entra.Secret != "", "redirect_uri": origin + "/auth/entra/callback"}}
}

type User struct {
	PasswordHash string `json:"password_hash"`
	Role         string `json:"role"`
	Disabled     bool   `json:"disabled"`
	Version      int64  `json:"version"`
}
type Session struct {
	Sub      string `json:"sub"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Role     string `json:"role"`
	Version  int64  `json:"version"`
}
type SetupForm struct {
	PublicURL  string      `json:"public_url"`
	Bao        BaoConfig   `json:"bao"`
	ClickHouse CHConfig    `json:"clickhouse"`
	Admin      string      `json:"admin_username"`
	Password   string      `json:"admin_password"`
	Entra      EntraConfig `json:"entra"`
}
type InstallConfig struct {
	Version    int        `json:"version"`
	ID         string     `json:"id"`
	PublicURL  string     `json:"public_url"`
	Bao        BaoConfig  `json:"bao"`
	ClickHouse CHConfig   `json:"clickhouse"`
	Scan       ScanConfig `json:"scan"`
	Admin      string     `json:"admin,omitempty"`
}
type ManagedConfig struct {
	ID         string    `json:"id"`
	Bao        BaoConfig `json:"bao"`
	ClickHouse CHConfig  `json:"clickhouse"`
}

// Interfaces separate orchestration from storage, network and browser implementations.
type Repository interface {
	Init(context.Context) error
	Ping(context.Context) error
	Sites(context.Context) ([]Site, error)
	SaveSite(context.Context, Site) error
	Latest(context.Context) ([]Object, error)
	History(context.Context, string, string, int) ([]Object, error)
	Inventory(context.Context, Inventory) error
	Checks(context.Context, []Object) error
}
type SecretStore interface {
	Read(context.Context, string, any) (bool, error)
	Write(context.Context, string, any, *int) error
}
type InventoryReader interface {
	Discover(context.Context, []string) (Inventory, error)
}
type Prober interface {
	Check(context.Context, Site, Interface, ScanConfig) Object
}
type Browser interface {
	Check(context.Context, string, string) Object
}
type Importer interface {
	Fetch(context.Context) ([]Site, []string, error)
}
