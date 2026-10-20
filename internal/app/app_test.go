package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

type memoryStore struct {
	mu      sync.Mutex
	sites   map[string]Site
	rows    []Object
	values  map[string]json.RawMessage
	fail    bool
	signals chan struct{}
}

func newMemory() *memoryStore {
	return &memoryStore{sites: map[string]Site{}, rows: []Object{}, values: map[string]json.RawMessage{}, signals: make(chan struct{}, 100)}
}
func (m *memoryStore) Init(context.Context) error { return nil }
func (m *memoryStore) Ping(context.Context) error { return nil }
func (m *memoryStore) Sites(context.Context) ([]Site, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Site{}
	for _, s := range m.sites {
		if s.Deleted == 0 {
			out = append(out, s)
		}
	}
	return out, nil
}
func (m *memoryStore) SaveSite(_ context.Context, s Site) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sites[s.ID] = s
	return nil
}
func (m *memoryStore) Latest(context.Context) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, _ := json.Marshal(m.rows)
	var out []Object
	_ = decodeJSON(data, &out)
	return out, nil
}
func (m *memoryStore) History(ctx context.Context, id, iface string, h int) ([]Object, error) {
	return m.Latest(ctx)
}
func (m *memoryStore) Inventory(context.Context, Inventory) error { return nil }
func (m *memoryStore) Checks(_ context.Context, rows []Object) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, rows...)
	select {
	case m.signals <- struct{}{}:
	default:
	}
	return nil
}
func (m *memoryStore) Read(_ context.Context, key string, out any) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, ok := m.values[key]
	if !ok {
		return false, nil
	}
	return true, decodeJSON(data, out)
}
func (m *memoryStore) Write(_ context.Context, key string, value any, cas *int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return fmt.Errorf("unavailable")
	}
	if _, exists := m.values[key]; exists && cas != nil && *cas == 0 {
		return fmt.Errorf("CAS conflict")
	}
	data, e := json.Marshal(value)
	m.values[key] = data
	return e
}

type fakeInventory struct{}

func (fakeInventory) Discover(context.Context, []string) (Inventory, error) {
	return Inventory{Interfaces: []Interface{{Name: "eth0.20", Probeable: true, Eligible: true, Addresses: []string{"192.0.2.2/24"}}}, Bridge: []Object{}}, nil
}

type countingProber struct{ active, max atomic.Int32 }

func (p *countingProber) Check(ctx context.Context, s Site, i Interface, c ScanConfig) Object {
	n := p.active.Add(1)
	for {
		old := p.max.Load()
		if n <= old || p.max.CompareAndSwap(old, n) {
			break
		}
	}
	defer p.active.Add(-1)
	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Millisecond):
	}
	return Object{"at": timestamp(), "site_id": s.ID, "interface": i.Name, "status": "up"}
}
func TestScanValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*ScanConfig)
		bad    bool
	}{{"defaults", func(*ScanConfig) {}, false}, {"unbounded parallelism", func(c *ScanConfig) { c.Concurrency = 1000 }, true}, {"bad DNS", func(c *ScanConfig) { c.DNS = map[string][]string{"eth0": {"dns.example"}} }, true}, {"invalid interface", func(c *ScanConfig) { c.Interfaces = []string{"eth0;rm"} }, true}, {"valid VLAN", func(c *ScanConfig) {
		c.Interfaces = []string{"eth0.20"}
		c.DNS = map[string][]string{"eth0.20": {"192.0.2.1"}}
	}, false}} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultScan()
			tc.change(&c)
			if (c.Validate() != nil) != tc.bad {
				t.Fatal("unexpected validation result")
			}
		})
	}
}
func TestPasswordCompatibilityAndLimits(t *testing.T) {
	hash, e := HashPassword("long-test-password")
	if e != nil || !strings.HasPrefix(hash, "$argon2id$") || !VerifyPassword(hash, "long-test-password") || VerifyPassword(hash, "wrong") {
		t.Fatal("Argon2 verification")
	}
	if VerifyPassword("$argon2id$v=19$m=4294967295,t=99,p=0$x$x", "x") {
		t.Fatal("unbounded hash accepted")
	}
	if _, e = HashPassword("short"); e == nil {
		t.Fatal("short password")
	}
}
func TestJWTValidation(t *testing.T) {
	service := TokenService{[]byte(strings.Repeat("x", 64))}
	valid, _ := service.Sign(Object{"sub": "admin"}, "vlan-session", time.Minute)
	if _, e := service.Parse(valid, "vlan-session"); e != nil {
		t.Fatal(e)
	}
	expired, _ := service.Sign(Object{}, "vlan-session", -time.Second)
	for _, input := range []string{valid + "x", expired, "bad"} {
		if _, e := service.Parse(input, "vlan-session"); e == nil {
			t.Fatal("invalid token accepted")
		}
	}
	if _, e := service.Parse(valid, "vlan-oidc"); e == nil {
		t.Fatal("wrong audience")
	}
}
func TestSessionRevocation(t *testing.T) {
	ctx := context.Background()
	m := newMemory()
	user, e := newUser("admin", "long-test-password", "admin", false)
	if e != nil {
		t.Fatal(e)
	}
	_ = m.Write(ctx, "users/admin", user, nil)
	auth := NewAuth(m, strings.Repeat("x", 64))
	token, e := auth.Local(ctx, "admin", "long-test-password")
	if e != nil {
		t.Fatal(e)
	}
	req := httptest.NewRequest("GET", "http://localhost/api/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: token})
	if _, e = auth.User(ctx, req, false); e != nil {
		t.Fatal(e)
	}
	user.Version++
	_ = m.Write(ctx, "users/admin", user, nil)
	if _, e = auth.User(ctx, req, false); e == nil {
		t.Fatal("old local session not revoked")
	}
}
func TestRateLimit(t *testing.T) {
	a := NewAuth(nil, "key")
	for i := 0; i < 10; i++ {
		if !a.AllowLogin("ip") {
			t.Fatal("premature limit")
		}
	}
	if a.AllowLogin("ip") {
		t.Fatal("limit not enforced")
	}
}
func TestWorkerBoundedAndConcurrent(t *testing.T) {
	a, e := New(t.TempDir(), "missing")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	m := newMemory()
	for i := 0; i < 20; i++ {
		_ = m.SaveSite(context.Background(), Site{ID: fmt.Sprint(i), Enabled: 1, SourceEnabled: 1})
	}
	probe := &countingProber{}
	a.inventoryReader = fakeInventory{}
	a.prober = probe
	scan := DefaultScan()
	scan.Concurrency = 4
	if e = a.cycle(context.Background(), m, scan); e != nil {
		t.Fatal(e)
	}
	if probe.max.Load() > 4 || probe.max.Load() < 2 || len(m.rows) != 20 {
		t.Fatalf("max=%d results=%d", probe.max.Load(), len(m.rows))
	}
}
func TestAtomicConfigurationPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if e := atomicJSON(path, Object{"value": 1}); e != nil {
		t.Fatal(e)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	var value Object
	if e := readJSON(path, &value); e != nil || number(value["value"]) != 1 {
		t.Fatal("restore failed")
	}
}
func TestPublicConnectionsNeverReturnSecrets(t *testing.T) {
	c := DefaultConnections()
	c.Gatus.Token = "secret-a"
	c.Kuma.Password = "secret-b"
	c.Entra.Secret = "secret-c"
	data, _ := json.Marshal(c.Public("http://localhost"))
	for _, secret := range []string{"secret-a", "secret-b", "secret-c"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("secret leaked")
		}
	}
}
func TestKumaHierarchyAndStableIDs(t *testing.T) {
	parent := int64(1)
	sites, w, e := ParseKuma([]KumaMonitor{{ID: 1, Type: "group", Active: false}, {ID: 2, Type: "http", URL: "https://portal.example", Name: "Portal", Active: true, Parent: &parent}}, "test")
	if e != nil || len(w) != 0 || len(sites) != 1 || sites[0].SourceEnabled != 0 || sites[0].Enabled != 1 {
		t.Fatal("parent pause lost")
	}
	other, _, _ := ParseKuma([]KumaMonitor{{ID: 2, Type: "http", URL: "https://changed.example", Name: "Renamed", Active: true}}, "test")
	if other[0].ID != sites[0].ID {
		t.Fatal("history identity changed")
	}
	cycle := int64(2)
	_, w, _ = ParseKuma([]KumaMonitor{{ID: 2, Type: "http", URL: "https://portal.example", Active: true, Parent: &cycle}}, "test")
	if len(w) != 1 {
		t.Fatal("cycle not detected")
	}
}
func TestKumaLimitAndURLCredentials(t *testing.T) {
	rows := make([]KumaMonitor, 1001)
	if _, _, e := ParseKuma(rows, "test"); e == nil {
		t.Fatal("limit not enforced")
	}
	sites, w, e := ParseKuma([]KumaMonitor{{ID: 1, Type: "http", URL: "https://user:secret@example.com", Active: true}}, "test")
	if e != nil || len(sites) != 0 || len(w) != 1 || strings.Contains(w[0], "secret") {
		t.Fatal("invalid URL or sensitive warning")
	}
}
func TestStaleNeverGreen(t *testing.T) {
	scan := DefaultScan()
	rows := freshResults([]Object{{"at": time.Now().Add(-time.Hour).Format(time.RFC3339), "site_id": "a", "interface": "eth0", "status": "up"}}, scan, []Site{{ID: "a", Enabled: 1, SourceEnabled: 1}}, Inventory{Interfaces: []Interface{{Name: "eth0", Probeable: true}}})
	if len(rows) != 1 || rows[0]["status"] != "unknown" || rows[0]["measured_status"] != "up" {
		t.Fatal(rows)
	}
}
func TestSetupAuthorizationAndNoReopen(t *testing.T) {
	a, _ := New(t.TempDir(), "missing")
	defer a.Close()
	h := a.Handler()
	status := httptest.NewRecorder()
	h.ServeHTTP(status, httptest.NewRequest("GET", "http://localhost/api/setup/status", nil))
	if status.Code != 200 || strings.Contains(status.Body.String(), "secret_id") {
		t.Fatal(status.Body.String())
	}
	for _, origin := range []string{"", "http://evil"} {
		r := httptest.NewRequest("POST", "http://localhost/api/setup/complete", strings.NewReader(`{}`))
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("origin %q accepted", origin)
		}
	}
	a.mu.Lock()
	a.config = &InstallConfig{PublicURL: "http://localhost"}
	a.mu.Unlock()
	r := httptest.NewRequest("POST", "http://localhost/api/setup/complete", strings.NewReader(`{}`))
	r.Header.Set("Origin", "http://localhost")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal("setup reopened for configured outage")
	}
}
func TestMonitoringAuthorizationAndPersistence(t *testing.T) {
	a, _ := New(t.TempDir(), "missing")
	defer a.Close()
	m := newMemory()
	a.mu.Lock()
	a.config = &InstallConfig{PublicURL: "http://localhost"}
	a.repo = m
	a.vault = m
	a.auth = NewAuth(m, strings.Repeat("x", 64))
	a.connections.Entra.Enabled = true
	a.mu.Unlock()
	h := a.Handler()
	for _, role := range []string{"viewer", "admin"} {
		token, _ := a.auth.Tokens.Sign(Object{"sub": "id", "provider": "entra", "role": role}, "vlan-session", time.Hour)
		c := DefaultScan()
		c.Concurrency = 6
		data, _ := json.Marshal(c)
		r := httptest.NewRequest("PUT", "http://localhost/api/settings/monitoring", bytes.NewReader(data))
		r.Header.Set("Origin", "http://localhost")
		r.AddCookie(&http.Cookie{Name: "session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if role == "viewer" && w.Code != 403 {
			t.Fatal("viewer wrote settings")
		}
		if role == "admin" && w.Code != 200 {
			t.Fatal(w.Body.String())
		}
	}
	var c ScanConfig
	found, e := m.Read(context.Background(), "connections/monitoring", &c)
	if e != nil || !found || c.Concurrency != 6 {
		t.Fatal("settings not durable")
	}
}
func TestGatusImport(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			w.WriteHeader(401)
			return
		}
		_, _ = w.Write([]byte(`[{"name":"Portal","url":"https://portal.example"},{"name":"Hidden"}]`))
	}))
	defer s.Close()
	sites, w, e := (GatusImporter{GatusConfig{URL: s.URL, Token: "test"}}).Fetch(context.Background())
	if e != nil || len(sites) != 1 || len(w) != 1 {
		t.Fatal("Gatus import contract")
	}
}
func TestSQLHistoryUsesParameters(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		b.ReadFrom(r.Body)
		if strings.Contains(b.String(), "DROP") {
			t.Error("input interpolated into SQL")
		}
		if r.URL.Query().Get("param_site") != "a';DROP TABLE sites;--" {
			t.Error("missing parameter")
		}
		_, _ = w.Write([]byte(""))
	}))
	defer server.Close()
	repo := NewClickHouse(CHConfig{URL: server.URL, User: "vlan", Database: "default"})
	if _, e := repo.History(context.Background(), "a';DROP TABLE sites;--", "eth0", 24); e != nil {
		t.Fatal(e)
	}
}
func TestConcurrentSettingsAndSnapshots(t *testing.T) {
	a, _ := New(t.TempDir(), "missing")
	defer a.Close()
	m := newMemory()
	a.repo = m
	a.vault = m
	a.config = &InstallConfig{PublicURL: "http://localhost"}
	var wg sync.WaitGroup
	for n := 0; n < 20; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				a.mu.Lock()
				a.scan.Concurrency = 1 + n%8
				a.mu.Unlock()
				_, _ = a.snapshot(context.Background())
				_ = a.Status()
			}
		}(n)
	}
	wg.Wait()
}
