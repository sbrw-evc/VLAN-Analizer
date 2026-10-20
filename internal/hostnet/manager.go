package hostnet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// StateFile lists the interfaces the application owns; it lives on the host next to the configuration it describes.
const StateFile = "/etc/vlan-analizer/vlans.json"

// SysctlFile gives managed VLANs loose reverse-path filtering: replies to probes bound to a VLAN arrive on that
// VLAN while the host's best route back may use the primary interface (strict rp_filter is the RHEL default).
const SysctlFile = "/etc/sysctl.d/90-vlan-analizer.conf"

// Managed is a stored spec with the backend that persisted it.
type Managed struct {
	Spec
	Backend string `json:"backend"`
	Created string `json:"created"`
}

// Link is the live kernel view of an interface.
type Link struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Parent    string   `json:"parent,omitempty"`
	VLAN      int      `json:"vlan,omitempty"`
	State     string   `json:"state"`
	Up        bool     `json:"up"`
	MTU       int      `json:"mtu"`
	MAC       string   `json:"mac"`
	Addresses []string `json:"addresses"`
}

// Interface is a managed VLAN with its live state.
type Interface struct {
	Managed
	Present bool `json:"present"`
	Live    Link `json:"live"`
}

// Trunk is an interface that can carry tagged VLANs, with the VLANs it carries now.
type Trunk struct {
	Link
	VLANs []Link `json:"vlans"`
}

// Listing is the agent's answer to GET /v1/vlans.
type Listing struct {
	Environment Environment `json:"environment"`
	Interfaces  []Interface `json:"interfaces"`
	Trunks      []Trunk     `json:"trunks"`
}

// ErrNoVLANSupport replaces the kernel's "Unknown device type" when the 8021q module is missing.
const ErrNoVLANSupport = "the host kernel has no 802.1Q VLAN support; load the 8021q module (modprobe 8021q)"

func explain(e error) error {
	var c *CommandError
	if errors.As(e, &c) && strings.Contains(c.Output, "Unknown device type") {
		return errors.New(ErrNoVLANSupport)
	}
	return e
}

// ErrNotFound is returned for names the application does not manage.
var ErrNotFound = errors.New("managed VLAN interface not found")

// ValidationError marks a bad request.
type ValidationError struct{ Message string }

func (e ValidationError) Error() string { return e.Message }

// ConflictError marks a name or VLAN that already exists.
type ConflictError struct{ Message string }

func (e ConflictError) Error() string { return e.Message }

// Manager serializes all changes to the host network configuration.
type Manager struct {
	host Host
	mu   sync.Mutex
	now  func() time.Time
}

func NewManager(h Host) *Manager { return &Manager{host: h, now: time.Now} }

type state struct {
	Version    int       `json:"version"`
	Interfaces []Managed `json:"interfaces"`
}

func (m *Manager) load() ([]Managed, error) {
	data, e := m.host.ReadFile(StateFile)
	if e != nil {
		return []Managed{}, nil
	}
	var s state
	if e = json.Unmarshal(data, &s); e != nil {
		return nil, fmt.Errorf("%s is damaged: %w", StateFile, e)
	}
	if s.Interfaces == nil {
		s.Interfaces = []Managed{}
	}
	return s.Interfaces, nil
}

func (m *Manager) save(items []Managed) error {
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	data, _ := json.MarshalIndent(state{Version: 1, Interfaces: items}, "", "  ")
	return m.host.WriteFile(StateFile, append(data, '\n'), 0600)
}

// Links reads the kernel's interfaces from the host network namespace.
func Links(ctx context.Context, h Host) ([]Link, error) {
	raw, e := h.Run(ctx, "ip", "-j", "-d", "address", "show")
	if e != nil {
		return nil, e
	}
	return ParseLinks(raw)
}

// ParseLinks decodes `ip -j -d address show`.
func ParseLinks(raw []byte) ([]Link, error) {
	var links []struct {
		Name     string   `json:"ifname"`
		Flags    []string `json:"flags"`
		State    string   `json:"operstate"`
		MTU      int      `json:"mtu"`
		MAC      string   `json:"address"`
		Link     string   `json:"link"`
		LinkType string   `json:"link_type"`
		Info     struct {
			Kind string `json:"info_kind"`
			Data struct {
				ID int `json:"id"`
			} `json:"info_data"`
		} `json:"linkinfo"`
		Addresses []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Prefix int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if e := json.Unmarshal(raw, &links); e != nil {
		return nil, e
	}
	out := make([]Link, 0, len(links))
	for _, l := range links {
		k := Link{Name: l.Name, Kind: l.Info.Kind, State: l.State, MTU: l.MTU, MAC: l.MAC, Addresses: []string{}}
		if k.Kind == "" {
			k.Kind = "physical"
			if l.LinkType == "loopback" {
				k.Kind = "loopback"
			}
		}
		if k.Kind == "vlan" {
			k.Parent = l.Link
			k.VLAN = l.Info.Data.ID
		}
		for _, f := range l.Flags {
			k.Up = k.Up || f == "UP"
		}
		for _, a := range l.Addresses {
			if a.Family == "inet" {
				k.Addresses = append(k.Addresses, fmt.Sprintf("%s/%d", a.Local, a.Prefix))
			}
		}
		out = append(out, k)
	}
	return out, nil
}

// TrunkCapable accepts physical NICs, bonds, teams and non-container bridges as VLAN parents.
func TrunkCapable(l Link) bool {
	switch l.Kind {
	case "physical", "bond", "team", "bridge":
	default:
		return false
	}
	for _, prefix := range []string{"docker", "br-", "veth", "virbr", "cni", "flannel", "cali", "lxc", "vnet", "tap", "tun"} {
		if strings.HasPrefix(l.Name, prefix) {
			return false
		}
	}
	return true
}

func (m *Manager) backend(name string) (Backend, error) { return NewBackend(name, m.host) }

func specs(items []Managed) []Spec {
	out := make([]Spec, 0, len(items))
	for _, i := range items {
		out = append(out, i.Spec)
	}
	return out
}

// sameBackend lists the specs persisted by one backend (netplan writes them all into one file).
func sameBackend(items []Managed, backend string) []Spec {
	out := []Spec{}
	for _, i := range items {
		if i.Backend == backend {
			out = append(out, i.Spec)
		}
	}
	return out
}

func (m *Manager) writeSysctl(ctx context.Context, items []Managed, applied *Spec) {
	if len(items) == 0 {
		_ = m.host.Remove(SysctlFile)
	} else {
		var b strings.Builder
		b.WriteString("# Managed by VLAN Analizer: accept replies on probe VLANs whose best return route uses another interface.\n")
		for _, s := range specs(items) {
			fmt.Fprintf(&b, "net/ipv4/conf/%s/rp_filter = 2\n", s.Name)
		}
		_ = m.host.WriteFile(SysctlFile, []byte(b.String()), 0644)
	}
	if applied != nil {
		_ = run(ctx, m.host, "sysctl", "-q", "-w", fmt.Sprintf("net/ipv4/conf/%s/rp_filter=2", applied.Name))
	}
}

// Status detects the host environment.
func (m *Manager) Status(ctx context.Context) Environment { return Detect(ctx, m.host) }

// List returns managed VLANs, their live state and the trunk-capable parents.
func (m *Manager) List(ctx context.Context) (Listing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	env := Detect(ctx, m.host)
	if env.Error != "" {
		return Listing{Environment: env, Interfaces: []Interface{}, Trunks: []Trunk{}}, nil
	}
	items, e := m.load()
	if e != nil {
		return Listing{}, e
	}
	links, e := Links(ctx, m.host)
	if e != nil {
		return Listing{}, e
	}
	byName := map[string]Link{}
	for _, l := range links {
		byName[l.Name] = l
	}
	out := Listing{Environment: env, Interfaces: []Interface{}, Trunks: []Trunk{}}
	for _, i := range items {
		live, ok := byName[i.Name]
		out.Interfaces = append(out.Interfaces, Interface{Managed: i, Present: ok, Live: live})
	}
	for _, l := range links {
		if !TrunkCapable(l) {
			continue
		}
		t := Trunk{Link: l, VLANs: []Link{}}
		for _, child := range links {
			if child.Kind == "vlan" && child.Parent == l.Name {
				t.VLANs = append(t.VLANs, child)
			}
		}
		sort.Slice(t.VLANs, func(a, b int) bool { return t.VLANs[a].VLAN < t.VLANs[b].VLAN })
		out.Trunks = append(out.Trunks, t)
	}
	return out, nil
}

func (m *Manager) checkNew(links []Link, items []Managed, s Spec) error {
	var parent *Link
	for i := range links {
		l := links[i]
		if l.Name == s.Parent {
			parent = &links[i]
		}
		if l.Name == s.Name {
			return ConflictError{fmt.Sprintf("interface %s already exists", s.Name)}
		}
		if l.Kind == "vlan" && l.Parent == s.Parent && l.VLAN == s.VLAN {
			return ConflictError{fmt.Sprintf("VLAN %d already exists on %s as %s", s.VLAN, s.Parent, l.Name)}
		}
	}
	if parent == nil {
		return ValidationError{fmt.Sprintf("parent interface %s does not exist", s.Parent)}
	}
	if !TrunkCapable(*parent) {
		return ValidationError{fmt.Sprintf("%s cannot carry tagged VLANs; choose a physical interface, bond or bridge", s.Parent)}
	}
	for _, i := range items {
		if i.Name == s.Name {
			return ConflictError{fmt.Sprintf("interface %s is already managed", s.Name)}
		}
	}
	return nil
}

// Create persists and activates a new VLAN interface. A failed activation is rolled back.
func (m *Manager) Create(ctx context.Context, s Spec) (Managed, error) {
	s, e := s.Normalize()
	if e != nil {
		return Managed{}, ValidationError{e.Error()}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	items, e := m.load()
	if e != nil {
		return Managed{}, e
	}
	links, e := Links(ctx, m.host)
	if e != nil {
		return Managed{}, e
	}
	if e = m.checkNew(links, items, s); e != nil {
		return Managed{}, e
	}
	env := Detect(ctx, m.host)
	if env.Error != "" {
		return Managed{}, errors.New(env.Error)
	}
	b, e := m.backend(env.Backend)
	if e != nil {
		return Managed{}, e
	}
	item := Managed{Spec: s, Backend: env.Backend, Created: m.now().UTC().Format(time.RFC3339)}
	next := append(append([]Managed{}, items...), item)
	if e = b.Apply(ctx, sameBackend(next, env.Backend), s); e != nil {
		_ = b.Remove(context.WithoutCancel(ctx), sameBackend(items, env.Backend), s)
		return Managed{}, explain(e)
	}
	if e = m.save(next); e != nil {
		return Managed{}, e
	}
	m.writeSysctl(ctx, next, &s)
	return item, nil
}

func find(items []Managed, name string) int {
	for i, item := range items {
		if item.Name == name {
			return i
		}
	}
	return -1
}

// Update changes the addressing, MTU or metric of a managed interface. Parent and VLAN ID are fixed.
func (m *Manager) Update(ctx context.Context, name string, s Spec) (Managed, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, e := m.load()
	if e != nil {
		return Managed{}, e
	}
	at := find(items, name)
	if at < 0 {
		return Managed{}, ErrNotFound
	}
	old := items[at]
	s.Name, s.Parent, s.VLAN = old.Name, old.Parent, old.VLAN
	if s, e = s.Normalize(); e != nil {
		return Managed{}, ValidationError{e.Error()}
	}
	b, e := m.backend(old.Backend)
	if e != nil {
		return Managed{}, e
	}
	without := append(append([]Managed{}, items[:at]...), items[at+1:]...)
	if e = b.Remove(ctx, sameBackend(without, old.Backend), old.Spec); e != nil {
		return Managed{}, e
	}
	updated := Managed{Spec: s, Backend: old.Backend, Created: old.Created}
	next := append(append([]Managed{}, without...), updated)
	if e = b.Apply(ctx, sameBackend(next, old.Backend), s); e != nil {
		restore := append(append([]Managed{}, without...), old)
		if b.Apply(context.WithoutCancel(ctx), sameBackend(restore, old.Backend), old.Spec) == nil {
			m.writeSysctl(ctx, restore, &old.Spec)
		}
		return Managed{}, e
	}
	if e = m.save(next); e != nil {
		return Managed{}, e
	}
	m.writeSysctl(ctx, next, &s)
	return updated, nil
}

// Delete removes the interface and its persistent configuration.
func (m *Manager) Delete(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, e := m.load()
	if e != nil {
		return e
	}
	at := find(items, name)
	if at < 0 {
		return ErrNotFound
	}
	old := items[at]
	b, e := m.backend(old.Backend)
	if e != nil {
		return e
	}
	without := append(append([]Managed{}, items[:at]...), items[at+1:]...)
	if e = b.Remove(ctx, sameBackend(without, old.Backend), old.Spec); e != nil {
		return e
	}
	if e = m.save(without); e != nil {
		return e
	}
	m.writeSysctl(ctx, without, nil)
	return nil
}

// SetState brings a managed interface up or down.
func (m *Manager) SetState(ctx context.Context, name string, up bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, e := m.load()
	if e != nil {
		return e
	}
	at := find(items, name)
	if at < 0 {
		return ErrNotFound
	}
	b, e := m.backend(items[at].Backend)
	if e != nil {
		return e
	}
	return b.SetState(ctx, items[at].Spec, up)
}

// Restore re-creates runtime-only interfaces after a host reboot; persistent backends restore themselves.
func (m *Manager) Restore(ctx context.Context) []error {
	m.mu.Lock()
	defer m.mu.Unlock()
	items, e := m.load()
	if e != nil {
		return []error{e}
	}
	links, e := Links(ctx, m.host)
	if e != nil {
		return []error{e}
	}
	present := map[string]bool{}
	for _, l := range links {
		present[l.Name] = true
	}
	var errs []error
	for _, i := range items {
		if i.Backend != BackendRuntime || present[i.Name] {
			continue
		}
		if e := (Runtime{m.host}).Apply(ctx, nil, i.Spec); e != nil {
			errs = append(errs, fmt.Errorf("%s: %w", i.Name, e))
			continue
		}
		m.writeSysctl(ctx, items, &i.Spec)
	}
	return errs
}
