package hostnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeHost struct {
	mu    sync.Mutex
	files map[string][]byte
	cmds  []string
	links string
	fail  map[string]string
	nm    bool
}

func newFake(release string, files ...string) *fakeHost {
	h := &fakeHost{files: map[string][]byte{"/etc/os-release": []byte(release)}, fail: map[string]string{}}
	for _, f := range files {
		h.files[f] = []byte("#\n")
	}
	h.links = `[{"ifname":"lo","flags":["LOOPBACK","UP"],"operstate":"UNKNOWN","mtu":65536,"link_type":"loopback","addr_info":[]},
{"ifname":"eth0","flags":["UP"],"operstate":"UP","mtu":1500,"address":"52:54:00:00:00:01","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24}]},
{"ifname":"eth1","flags":["UP"],"operstate":"UP","mtu":1500,"address":"52:54:00:00:00:02","addr_info":[]},
{"ifname":"eth1.7","flags":["UP"],"operstate":"UP","mtu":1500,"link":"eth1","linkinfo":{"info_kind":"vlan","info_data":{"id":7}},"addr_info":[]},
{"ifname":"docker0","flags":["UP"],"operstate":"DOWN","mtu":1500,"linkinfo":{"info_kind":"bridge"},"addr_info":[]}]`
	return h
}

func (h *fakeHost) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := strings.Join(append([]string{name}, args...), " ")
	h.cmds = append(h.cmds, line)
	for prefix, out := range h.fail {
		if strings.HasPrefix(line, prefix) {
			return nil, &CommandError{Command: name, Output: out}
		}
	}
	switch {
	case line == "ip -j -d address show":
		return []byte(h.links), nil
	case line == "nmcli -t -f RUNNING general":
		if h.nm {
			return []byte("running\n"), nil
		}
		return []byte("stopped\n"), nil
	}
	return nil, nil
}

func (h *fakeHost) ReadFile(p string) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if d, ok := h.files[p]; ok {
		return d, nil
	}
	return nil, os.ErrNotExist
}

func (h *fakeHost) WriteFile(p string, data []byte, _ os.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.files[p] = append([]byte{}, data...)
	return nil
}

func (h *fakeHost) Remove(p string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.files, p)
	return nil
}

func (h *fakeHost) Glob(pattern string) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for f := range h.files {
		if ok, _ := path.Match(pattern, f); ok {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (h *fakeHost) commands() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string{}, h.cmds...)
}

const (
	ubuntu   = "ID=ubuntu\nID_LIKE=debian\nPRETTY_NAME=\"Ubuntu 24.04 LTS\"\nVERSION_ID=\"24.04\"\n"
	debian   = "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"\n"
	rhel     = "ID=\"rhel\"\nID_LIKE=\"fedora\"\nPRETTY_NAME=\"Red Hat Enterprise Linux 9.4 (Plow)\"\nVERSION_ID=\"9.4\"\n"
	centos7  = "ID=\"centos\"\nID_LIKE=\"rhel fedora\"\nPRETTY_NAME=\"CentOS Linux 7 (Core)\"\nVERSION_ID=\"7\"\n"
	rocky    = "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\nPRETTY_NAME=\"Rocky Linux 9.4\"\nVERSION_ID=\"9.4\"\n"
	alpine   = "ID=alpine\nPRETTY_NAME=\"Alpine Linux v3.20\"\n"
	netplanF = "/etc/netplan/50-cloud-init.yaml"
)

func TestDetectBackends(t *testing.T) {
	cases := []struct {
		name, family, backend string
		host                  *fakeHost
	}{
		{"ubuntu netplan", FamilyUbuntu, BackendNetplan, newFake(ubuntu, netplanF, "/usr/sbin/netplan")},
		{"debian ifupdown", FamilyDebian, BackendIfupdown, newFake(debian, "/etc/network/interfaces", "/usr/sbin/ifup")},
		{"rhel networkmanager", FamilyRHEL, BackendNetworkManager, func() *fakeHost {
			h := newFake(rhel, "/usr/bin/nmcli")
			h.nm = true
			return h
		}()},
		{"centos 7 network-scripts", FamilyCentOS, BackendNetworkScripts, newFake(centos7, "/etc/sysconfig/network-scripts/ifcfg-eth0", "/usr/sbin/ifup")},
		{"rocky follows rhel", FamilyRHEL, BackendNetworkManager, func() *fakeHost {
			h := newFake(rocky, "/usr/bin/nmcli")
			h.nm = true
			return h
		}()},
		{"debian with networkmanager", FamilyDebian, BackendNetworkManager, func() *fakeHost {
			h := newFake(debian, "/usr/bin/nmcli", "/etc/network/interfaces", "/usr/sbin/ifup")
			h.nm = true
			return h
		}()},
		{"unsupported runtime", "", BackendRuntime, newFake(alpine)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := Detect(context.Background(), c.host)
			if env.Distro.Family != c.family || env.Backend != c.backend {
				t.Fatalf("got %s/%s, want %s/%s", env.Distro.Family, env.Backend, c.family, c.backend)
			}
			if env.Persistent != (c.backend != BackendRuntime) || env.Supported != (c.family != "") {
				t.Fatalf("flags %+v", env)
			}
		})
	}
}

func TestSpecValidation(t *testing.T) {
	good := []Spec{
		{Parent: "eth1", VLAN: 20},
		{Parent: "eth1", VLAN: 4094, Name: "vlan4094", Mode: ModeNone},
		{Parent: "bond0", VLAN: 30, Mode: ModeStatic, Address: "10.30.0.5/24", Gateway: "10.30.0.1", MTU: 9000},
	}
	for _, s := range good {
		if _, e := s.Normalize(); e != nil {
			t.Fatalf("%+v: %v", s, e)
		}
	}
	bad := []Spec{
		{Parent: "eth1", VLAN: 0},
		{Parent: "eth1", VLAN: 4095},
		{Parent: "eth1;reboot", VLAN: 5},
		{Parent: "-force", VLAN: 5},
		{Parent: "eth1\nauto", VLAN: 5},
		{Parent: "eth1", VLAN: 5, Name: "evil"},
		{Parent: "eth1", VLAN: 5, Mode: ModeStatic, Address: "10.0.0.5"},
		{Parent: "eth1", VLAN: 5, Mode: ModeStatic, Address: "10.0.0.5/24", Gateway: "10.0.1.1"},
		{Parent: "eth1", VLAN: 5, Mode: ModeStatic, Address: "fd00::5/64"},
		{Parent: "eth1", VLAN: 5, Mode: ModeDHCP, Address: "10.0.0.5/24"},
		{Parent: "eth1", VLAN: 5, MTU: 100},
		{Parent: "eth1", VLAN: 5, Mode: "bootp"},
	}
	for _, s := range bad {
		if _, e := s.Normalize(); e == nil {
			t.Fatalf("accepted %+v", s)
		}
	}
	if DefaultName("enp0s31f6", 4000) != "enp0s31f6.4000" || DefaultName("enp129s0f1np1", 20) != "vlan20" {
		t.Fatal("default names")
	}
}

func TestParseVLANs(t *testing.T) {
	got, e := ParseVLANs("30-32, 10;10 4094")
	if e != nil || !reflect.DeepEqual(got, []int{10, 30, 31, 32, 4094}) {
		t.Fatalf("%v %v", got, e)
	}
	for _, raw := range []string{"", "0", "5-3", "4095", "a", "1-100"} {
		if _, e := ParseVLANs(raw); e == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestRenderers(t *testing.T) {
	static := Spec{Name: "eth1.30", Parent: "eth1", VLAN: 30, Mode: ModeStatic, Address: "10.30.0.5/24", Gateway: "10.30.0.1", MTU: 9000, Metric: 2030}
	args := strings.Join(NMArgs(static), " ")
	for _, want := range []string{"type vlan con-name vlan-analizer-eth1.30 ifname eth1.30 dev eth1 id 30 mtu 9000", "ipv4.method manual ipv4.addresses 10.30.0.5/24 ipv4.gateway 10.30.0.1", "ipv4.route-metric 2030"} {
		if !strings.Contains(args, want) {
			t.Fatalf("nmcli args %q missing %q", args, want)
		}
	}
	dhcp := Spec{Name: "vlan40", Parent: "bond0", VLAN: 40, Mode: ModeDHCP, Metric: 2040}
	plan, e := RenderNetplan([]Spec{static, dhcp}, map[string]string{"eth1": "eth1"})
	if e != nil {
		t.Fatal(e)
	}
	text := string(plan)
	for _, want := range []string{"link: eth1", "link: bond0", "route-metric: 2040", "via: 10.30.0.1", "metric: 2030", "bond0:\n            dhcp4: false", "mtu: 9000"} {
		if !strings.Contains(text, want) {
			t.Fatalf("netplan missing %q:\n%s", want, text)
		}
	}
	stanza := string(RenderIfupdown(static))
	for _, want := range []string{"auto eth1.30\niface eth1.30 inet static", "vlan-raw-device eth1", "address 10.30.0.5/24", "gateway 10.30.0.1", "metric 2030", "mtu 9000"} {
		if !strings.Contains(stanza, want) {
			t.Fatalf("ifupdown missing %q:\n%s", want, stanza)
		}
	}
	if IfupdownFile(static) != "/etc/network/interfaces.d/vlan-analizer-eth1_30" {
		t.Fatal(IfupdownFile(static))
	}
	ifcfg := string(RenderIfcfg(static))
	for _, want := range []string{"DEVICE=eth1.30", "PHYSDEV=eth1", "VLAN=yes", "VLAN_ID=30", "IPADDR=10.30.0.5", "PREFIX=24", "GATEWAY=10.30.0.1", "METRIC=2030", "NM_CONTROLLED=no"} {
		if !strings.Contains(ifcfg, want) {
			t.Fatalf("ifcfg missing %q:\n%s", want, ifcfg)
		}
	}
	none := RenderIfcfg(Spec{Name: "eth1.31", Parent: "eth1", VLAN: 31, Mode: ModeNone, Metric: 2031})
	if !strings.Contains(string(none), "DEFROUTE=no") || strings.Contains(string(none), "METRIC") {
		t.Fatalf("ifcfg none:\n%s", none)
	}
}

func TestNetplanParentIDs(t *testing.T) {
	h := newFake(ubuntu, "/usr/sbin/netplan")
	h.files[netplanF] = []byte("network:\n  version: 2\n  ethernets:\n    uplink:\n      match:\n        name: eth1\n      set-name: eth1\n    eth0:\n      dhcp4: true\n")
	ids := netplanIDs(h)
	if ids["eth1"] != "uplink" || ids["eth0"] != "eth0" {
		t.Fatalf("%v", ids)
	}
}

func TestManagerLifecycleNetworkManager(t *testing.T) {
	h := newFake(rhel, "/usr/bin/nmcli")
	h.nm = true
	m := NewManager(h)
	m.now = func() time.Time { return time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	item, e := m.Create(ctx, Spec{Parent: "eth1", VLAN: 20, Mode: ModeStatic, Address: "10.20.0.5/24", Gateway: "10.20.0.1"})
	if e != nil {
		t.Fatal(e)
	}
	if item.Name != "eth1.20" || item.Backend != BackendNetworkManager || item.Metric != 2020 {
		t.Fatalf("%+v", item)
	}
	cmds := strings.Join(h.commands(), "\n")
	for _, want := range []string{"nmcli connection add type vlan con-name vlan-analizer-eth1.20", "nmcli connection up id vlan-analizer-eth1.20", "sysctl -q -w net/ipv4/conf/eth1.20/rp_filter=2"} {
		if !strings.Contains(cmds, want) {
			t.Fatalf("missing %q in\n%s", want, cmds)
		}
	}
	if !strings.Contains(string(h.files[SysctlFile]), "net/ipv4/conf/eth1.20/rp_filter = 2") {
		t.Fatal(string(h.files[SysctlFile]))
	}
	var saved state
	if e = json.Unmarshal(h.files[StateFile], &saved); e != nil || len(saved.Interfaces) != 1 {
		t.Fatalf("state %s %v", h.files[StateFile], e)
	}
	if _, e = m.Create(ctx, Spec{Parent: "eth1", VLAN: 20}); e == nil {
		t.Fatal("duplicate accepted")
	}
	if _, e = m.Create(ctx, Spec{Parent: "eth1", VLAN: 7, Name: "vlan7"}); e == nil || !strings.Contains(e.Error(), "already exists on eth1") {
		t.Fatalf("existing kernel VLAN accepted: %v", e)
	}
	if _, e = m.Create(ctx, Spec{Parent: "docker0", VLAN: 9}); e == nil {
		t.Fatal("container bridge accepted as trunk")
	}
	if _, e = m.Create(ctx, Spec{Parent: "eth9", VLAN: 9}); e == nil {
		t.Fatal("missing parent accepted")
	}
	if _, e = m.Update(ctx, "eth1.20", Spec{Mode: ModeDHCP, MTU: 1400}); e != nil {
		t.Fatal(e)
	}
	list, e := m.List(ctx)
	if e != nil || len(list.Interfaces) != 1 || list.Interfaces[0].Mode != ModeDHCP || list.Interfaces[0].Parent != "eth1" {
		t.Fatalf("%+v %v", list, e)
	}
	trunks := []string{}
	for _, tr := range list.Trunks {
		trunks = append(trunks, tr.Name)
	}
	if !reflect.DeepEqual(trunks, []string{"eth0", "eth1"}) {
		t.Fatalf("trunks %v", trunks)
	}
	if e = m.SetState(ctx, "eth1.20", false); e != nil {
		t.Fatal(e)
	}
	if e = m.Delete(ctx, "eth1.20"); e != nil {
		t.Fatal(e)
	}
	if _, ok := h.files[SysctlFile]; ok {
		t.Fatal("sysctl file kept after last delete")
	}
	if e = m.Delete(ctx, "eth1.20"); e != ErrNotFound {
		t.Fatalf("second delete: %v", e)
	}
}

func TestManagerRollsBackFailedActivation(t *testing.T) {
	h := newFake(debian, "/etc/network/interfaces", "/usr/sbin/ifup")
	h.files["/etc/network/interfaces"] = []byte("auto lo\niface lo inet loopback\n")
	h.fail["ifup eth1.20"] = "RTNETLINK answers: Operation not supported"
	m := NewManager(h)
	_, e := m.Create(context.Background(), Spec{Parent: "eth1", VLAN: 20, Mode: ModeNone})
	if e == nil || !strings.Contains(e.Error(), "Operation not supported") {
		t.Fatalf("error %v", e)
	}
	if _, ok := h.files["/etc/network/interfaces.d/vlan-analizer-eth1_20"]; ok {
		t.Fatal("stanza left after failed activation")
	}
	if _, ok := h.files[StateFile]; ok {
		t.Fatal("state saved after failure")
	}
	if !strings.Contains(string(h.files["/etc/network/interfaces"]), "source /etc/network/interfaces.d/vlan-analizer-*") {
		t.Fatal("interfaces.d is not sourced")
	}
}

func TestNetplanBackendSingleFile(t *testing.T) {
	h := newFake(ubuntu, netplanF, "/usr/sbin/netplan")
	m := NewManager(h)
	ctx := context.Background()
	for _, v := range []int{10, 11} {
		if _, e := m.Create(ctx, Spec{Parent: "eth1", VLAN: v, Mode: ModeDHCP}); e != nil {
			t.Fatal(e)
		}
	}
	plan := string(h.files[NetplanFile])
	if !strings.Contains(plan, "eth1.10:") || !strings.Contains(plan, "eth1.11:") {
		t.Fatal(plan)
	}
	if e := m.Delete(ctx, "eth1.10"); e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(h.files[NetplanFile]), "eth1.10:") {
		t.Fatal("deleted VLAN still rendered")
	}
	if e := m.Delete(ctx, "eth1.11"); e != nil {
		t.Fatal(e)
	}
	if _, ok := h.files[NetplanFile]; ok {
		t.Fatal("empty netplan file kept")
	}
	if !strings.Contains(strings.Join(h.commands(), "\n"), "ip link delete dev eth1.11") {
		t.Fatal("link not deleted")
	}
}

func TestKernelWithoutVLANSupport(t *testing.T) {
	h := newFake(alpine)
	h.fail["ip link add"] = "Error: Unknown device type."
	_, e := NewManager(h).Create(context.Background(), Spec{Parent: "eth1", VLAN: 5, Mode: ModeNone})
	if e == nil || e.Error() != ErrNoVLANSupport {
		t.Fatalf("%v", e)
	}
	if _, ok := h.files[StateFile]; ok {
		t.Fatal("state saved")
	}
}

func TestRuntimeRestoreAndDHCPRefusal(t *testing.T) {
	h := newFake(alpine)
	m := NewManager(h)
	ctx := context.Background()
	if _, e := m.Create(ctx, Spec{Parent: "eth1", VLAN: 50, Mode: ModeDHCP}); e == nil {
		t.Fatal("runtime DHCP accepted")
	}
	if _, e := m.Create(ctx, Spec{Parent: "eth1", VLAN: 50, Mode: ModeStatic, Address: "10.50.0.2/24", Gateway: "10.50.0.1"}); e != nil {
		t.Fatal(e)
	}
	h.cmds = nil
	if errs := m.Restore(ctx); len(errs) != 0 {
		t.Fatal(errs)
	}
	cmds := strings.Join(h.commands(), "\n")
	if !strings.Contains(cmds, "ip link add link eth1 name eth1.50 type vlan id 50") || !strings.Contains(cmds, "ip route replace default via 10.50.0.1 dev eth1.50 metric 2050") {
		t.Fatal(cmds)
	}
}

func TestAgentSocketRoundTrip(t *testing.T) {
	h := newFake(rhel, "/usr/bin/nmcli")
	h.nm = true
	dir, e := os.MkdirTemp("", "va")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(dir)
	socket := filepath.Join(dir, "a.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, socket, NewManager(h)) }()
	c := NewClient(socket)
	var env Environment
	for i := 0; i < 50; i++ {
		if env, e = c.Status(ctx); e == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e != nil || env.Backend != BackendNetworkManager {
		t.Fatalf("%+v %v", env, e)
	}
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket mode %v %v", info, err)
	}
	if _, e = c.Create(ctx, Spec{Parent: "eth1", VLAN: 99, Mode: ModeStatic, Address: "bad"}); e == nil {
		t.Fatal("bad spec accepted")
	} else if ae, ok := e.(*AgentError); !ok || ae.Status != 422 {
		t.Fatalf("%#v", e)
	}
	if _, e = c.Create(ctx, Spec{Parent: "eth1", VLAN: 99}); e != nil {
		t.Fatal(e)
	}
	if e = c.SetState(ctx, "eth1.99", true); e != nil {
		t.Fatal(e)
	}
	if e = c.Delete(ctx, "missing"); e == nil || e.(*AgentError).Status != 404 {
		t.Fatalf("%v", e)
	}
	cancel()
	if e = <-done; e != nil {
		t.Fatal(e)
	}
	if _, e = NewClient(socket).Status(context.Background()); e != ErrUnavailable {
		t.Fatalf("stopped agent: %v", e)
	}
}

func TestHandlerRejectsUnknownFields(t *testing.T) {
	h := newFake(rhel, "/usr/bin/nmcli")
	srv := httptest.NewServer(Handler(NewManager(h)))
	defer srv.Close()
	resp, e := http.Post(srv.URL+"/v1/vlans", "application/json", strings.NewReader(`{"parent":"eth1","vlan":5,"script":"x"}`))
	if e != nil || resp.StatusCode != 422 {
		t.Fatalf("%v %v", resp, e)
	}
}

func TestNoHostAccessRefusesChanges(t *testing.T) {
	h := newFake(ubuntu, netplanF, "/usr/sbin/netplan")
	h.fail["true"] = "nsenter: cannot open /proc/1/ns/net: Permission denied"
	m := NewManager(h)
	list, e := m.List(context.Background())
	if e != nil || list.Environment.Error != ErrNoHostAccess || len(list.Trunks) != 0 {
		t.Fatalf("%+v %v", list, e)
	}
	if _, e = m.Create(context.Background(), Spec{Parent: "eth1", VLAN: 5}); e == nil || !strings.Contains(e.Error(), "privileged") {
		t.Fatalf("created without host access: %v", e)
	}
	for _, c := range h.commands() {
		if strings.HasPrefix(c, "ip link add") || strings.HasPrefix(c, "netplan") {
			t.Fatalf("changed the host: %s", c)
		}
	}
}
