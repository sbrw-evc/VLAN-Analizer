package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sbrw-evc/VLAN-Analizer/internal/hostnet"
)

type fakeAgent struct {
	mu      sync.Mutex
	created []hostnet.Spec
	deleted []string
	down    bool
	refuse  int
}

func (f *fakeAgent) Status(context.Context) (hostnet.Environment, error) {
	return hostnet.Environment{Backend: hostnet.BackendNetplan}, nil
}
func (f *fakeAgent) List(context.Context) (hostnet.Listing, error) {
	if f.down {
		return hostnet.Listing{}, hostnet.ErrUnavailable
	}
	return hostnet.Listing{Environment: hostnet.Environment{Backend: hostnet.BackendNetplan}, Interfaces: []hostnet.Interface{}, Trunks: []hostnet.Trunk{}}, nil
}
func (f *fakeAgent) Create(_ context.Context, s hostnet.Spec) (hostnet.Managed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.VLAN == f.refuse {
		return hostnet.Managed{}, &hostnet.AgentError{Status: 409, Message: "VLAN already exists"}
	}
	f.created = append(f.created, s)
	return hostnet.Managed{Spec: s, Backend: hostnet.BackendNetplan}, nil
}
func (f *fakeAgent) Update(_ context.Context, name string, s hostnet.Spec) (hostnet.Managed, error) {
	s.Name = name
	return hostnet.Managed{Spec: s}, nil
}
func (f *fakeAgent) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *fakeAgent) SetState(context.Context, string, bool) error { return nil }

func vlanApp(t *testing.T) (*App, *memoryStore, *fakeAgent, http.Handler) {
	a, _ := New(t.TempDir(), "missing")
	t.Cleanup(a.Close)
	m := newMemory()
	agent := &fakeAgent{}
	a.mu.Lock()
	a.config = &InstallConfig{PublicURL: "http://localhost"}
	a.repo = m
	a.vault = m
	a.auth = NewAuth(m, strings.Repeat("x", 64))
	a.connections.Entra.Enabled = true
	a.inventory = Inventory{Interfaces: []Interface{{Name: "eth0", Probeable: true}, {Name: "eth1"}}}
	a.mu.Unlock()
	a.SetHostNetwork(agent)
	return a, m, agent, a.Handler()
}

func call(t *testing.T, a *App, h http.Handler, role, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	token, _ := a.auth.Tokens.Sign(Object{"sub": "id", "provider": "entra", "role": role}, "vlan-session", time.Hour)
	r := httptest.NewRequest(method, "http://localhost"+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://localhost")
	r.AddCookie(&http.Cookie{Name: "session", Value: token})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestVLANRoutesRequireAdministrator(t *testing.T) {
	a, _, agent, h := vlanApp(t)
	for _, c := range [][2]string{{"GET", "/api/network/vlans"}, {"POST", "/api/network/vlans"}, {"DELETE", "/api/network/vlans/eth1.10"}, {"POST", "/api/network/vlans/eth1.10/down"}} {
		if w := call(t, a, h, "viewer", c[0], c[1], `{}`); w.Code != 403 {
			t.Fatalf("%v answered %d to a viewer", c, w.Code)
		}
	}
	if len(agent.created)+len(agent.deleted) != 0 {
		t.Fatal("viewer reached the agent")
	}
}

func TestCreateVLANBatchUpdatesMonitoring(t *testing.T) {
	a, m, agent, h := vlanApp(t)
	w := call(t, a, h, "admin", "POST", "/api/network/vlans", `{"parent":"eth1","vlans":"10-11, 12","mode":"dhcp","dns_servers":["10.0.0.53"],"monitor":true}`)
	if w.Code != 201 {
		t.Fatal(w.Body.String())
	}
	var out struct {
		Created []hostnet.Managed `json:"created"`
		Failed  []Object          `json:"failed"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if len(out.Created) != 3 || len(out.Failed) != 0 || agent.created[0].Name != "eth1.10" || agent.created[2].Metric != 2012 {
		t.Fatalf("%+v %+v", out, agent.created)
	}
	var scan ScanConfig
	if found, _ := m.Read(context.Background(), "connections/monitoring", &scan); !found || len(scan.DNS["eth1.11"]) != 1 || len(scan.Interfaces) != 0 {
		t.Fatalf("%+v", scan)
	}
	if w = call(t, a, h, "admin", "DELETE", "/api/network/vlans/eth1.11", ``); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var after ScanConfig
	_, _ = m.Read(context.Background(), "connections/monitoring", &after)
	if _, ok := after.DNS["eth1.11"]; ok || len(after.DNS["eth1.10"]) != 1 {
		t.Fatal("DNS override kept for a deleted interface")
	}
}

func TestCreateVLANUnmonitoredPinsSelection(t *testing.T) {
	a, _, agent, h := vlanApp(t)
	agent.refuse = 21
	w := call(t, a, h, "admin", "POST", "/api/network/vlans", `{"parent":"eth1","vlans":"20,21","naming":"vlan","mode":"none","monitor":false}`)
	if w.Code != 201 || !strings.Contains(w.Body.String(), "VLAN already exists") {
		t.Fatal(w.Body.String())
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if strings.Join(a.scan.Interfaces, ",") != "eth0" || agent.created[0].Name != "vlan20" {
		t.Fatalf("%v %+v", a.scan.Interfaces, agent.created)
	}
}

func TestCreateVLANValidation(t *testing.T) {
	a, _, agent, h := vlanApp(t)
	for _, body := range []string{
		`{"parent":"eth1","vlans":"10,11","mode":"static","address":"10.0.0.2/24"}`,
		`{"parent":"eth1","vlans":"0"}`,
		`{"parent":"eth1","vlans":"10","dns_servers":["fd00::1"]}`,
		`{"parent":"eth1;id","vlans":"10"}`,
		`{"parent":"eth1","vlans":"10","shell":"x"}`,
	} {
		if w := call(t, a, h, "admin", "POST", "/api/network/vlans", body); w.Code != 422 {
			t.Fatalf("%s: %d %s", body, w.Code, w.Body.String())
		}
	}
	agent.refuse = 30
	if w := call(t, a, h, "admin", "POST", "/api/network/vlans", `{"parent":"eth1","vlans":"30"}`); w.Code != 409 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	if len(agent.created) != 0 {
		t.Fatal("invalid request reached the agent")
	}
}

func TestVLANAgentUnavailable(t *testing.T) {
	a, _, agent, h := vlanApp(t)
	agent.down = true
	w := call(t, a, h, "admin", "GET", "/api/network/vlans", ``)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"available":false`) {
		t.Fatal(w.Body.String())
	}
	a.SetHostNetwork(hostnet.NewClient("/nonexistent/agent.sock"))
	if w = call(t, a, h, "admin", "POST", "/api/network/vlans", `{"parent":"eth1","vlans":"5"}`); w.Code != 503 {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}

func TestWebAppFallback(t *testing.T) {
	_, _, _, h := vlanApp(t)
	r := httptest.NewRequest("GET", "http://localhost/api/unknown", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Header().Get("Content-Type"), "json") {
		t.Fatalf("%d %s", w.Code, w.Header())
	}
	r = httptest.NewRequest("GET", "http://localhost/vlans", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 && w.Code != 503 {
		t.Fatalf("client route answered %d", w.Code)
	}
	if !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Fatal("CSP missing")
	}
}
