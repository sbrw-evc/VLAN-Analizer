package hostnet

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Backend names.
const (
	BackendNetworkManager = "networkmanager"
	BackendNetplan        = "netplan"
	BackendIfupdown       = "ifupdown"
	BackendNetworkScripts = "network-scripts"
	BackendRuntime        = "runtime"
)

const managedHeader = "# Managed by VLAN Analizer. Changes made here are replaced by the application.\n"

// Backend writes one persistence format and activates the interface with the host's own tools.
// Apply creates or replaces s; all lists every managed spec including s. Remove deletes s; all no longer contains it.
type Backend interface {
	Name() string
	Apply(ctx context.Context, all []Spec, s Spec) error
	Remove(ctx context.Context, all []Spec, s Spec) error
	SetState(ctx context.Context, s Spec, up bool) error
}

// NewBackend returns the implementation for a backend name.
func NewBackend(name string, h Host) (Backend, error) {
	switch name {
	case BackendNetworkManager:
		return NetworkManager{h}, nil
	case BackendNetplan:
		return Netplan{h}, nil
	case BackendIfupdown:
		return Ifupdown{h}, nil
	case BackendNetworkScripts:
		return NetworkScripts{h}, nil
	case BackendRuntime:
		return Runtime{h}, nil
	}
	return nil, fmt.Errorf("unknown network backend %q", name)
}

func notFound(e error) bool {
	var c *CommandError
	if !errors.As(e, &c) {
		return false
	}
	out := strings.ToLower(c.Output)
	return strings.Contains(out, "cannot find device") || strings.Contains(out, "does not exist") || strings.Contains(out, "unknown connection") || strings.Contains(out, "not found")
}

func linkDelete(ctx context.Context, h Host, name string) error {
	if e := run(ctx, h, "ip", "link", "delete", "dev", name); e != nil && !notFound(e) {
		return e
	}
	return nil
}

func linkState(ctx context.Context, h Host, name string, up bool) error {
	state := "down"
	if up {
		state = "up"
	}
	return run(ctx, h, "ip", "link", "set", "dev", name, state)
}

// ---------- NetworkManager (RHEL, CentOS, Rocky/Alma, NetworkManager desktops) ----------

type NetworkManager struct{ h Host }

func (NetworkManager) Name() string { return BackendNetworkManager }

// ConnectionName is the NetworkManager profile owned by the application.
func ConnectionName(s Spec) string { return "vlan-analizer-" + s.Name }

// NMArgs builds `nmcli connection add` for a spec.
func NMArgs(s Spec) []string {
	args := []string{"connection", "add", "type", "vlan", "con-name", ConnectionName(s), "ifname", s.Name, "dev", s.Parent, "id", strconv.Itoa(s.VLAN)}
	if s.MTU > 0 {
		args = append(args, "mtu", strconv.Itoa(s.MTU))
	}
	args = append(args, "connection.autoconnect", "yes", "ipv6.method", "ignore", "ipv4.route-metric", strconv.Itoa(s.Metric))
	switch s.Mode {
	case ModeDHCP:
		args = append(args, "ipv4.method", "auto")
	case ModeStatic:
		args = append(args, "ipv4.method", "manual", "ipv4.addresses", s.Address)
		if s.Gateway != "" {
			args = append(args, "ipv4.gateway", s.Gateway)
		} else {
			args = append(args, "ipv4.never-default", "yes")
		}
	default:
		args = append(args, "ipv4.method", "disabled")
	}
	return args
}

func (n NetworkManager) Apply(ctx context.Context, _ []Spec, s Spec) error {
	if e := run(ctx, n.h, "nmcli", "connection", "delete", "id", ConnectionName(s)); e != nil && !notFound(e) {
		return e
	}
	if e := run(ctx, n.h, "nmcli", NMArgs(s)...); e != nil {
		return e
	}
	return run(ctx, n.h, "nmcli", "connection", "up", "id", ConnectionName(s))
}

func (n NetworkManager) Remove(ctx context.Context, _ []Spec, s Spec) error {
	if e := run(ctx, n.h, "nmcli", "connection", "delete", "id", ConnectionName(s)); e != nil && !notFound(e) {
		return e
	}
	return linkDelete(ctx, n.h, s.Name)
}

func (n NetworkManager) SetState(ctx context.Context, s Spec, up bool) error {
	action := "down"
	if up {
		action = "up"
	}
	return run(ctx, n.h, "nmcli", "connection", action, "id", ConnectionName(s))
}

// ---------- netplan (Ubuntu; renders for systemd-networkd or NetworkManager) ----------

type Netplan struct{ h Host }

// NetplanFile holds every managed VLAN; netplan merges it with the host's own files.
const NetplanFile = "/etc/netplan/90-vlan-analizer.yaml"

func (Netplan) Name() string { return BackendNetplan }

// netplanIDs maps device names to the IDs the host's netplan files define them under.
func netplanIDs(h Host) map[string]string {
	ids := map[string]string{}
	files, _ := h.Glob("/etc/netplan/*.yaml")
	sort.Strings(files)
	for _, f := range files {
		if f == NetplanFile {
			continue
		}
		data, e := h.ReadFile(f)
		if e != nil {
			continue
		}
		var doc struct {
			Network map[string]any `yaml:"network"`
		}
		if yaml.Unmarshal(data, &doc) != nil {
			continue
		}
		for _, section := range []string{"ethernets", "bonds", "bridges", "vlans"} {
			defs, _ := doc.Network[section].(map[string]any)
			for id, raw := range defs {
				ids[id] = id
				def, _ := raw.(map[string]any)
				if name, ok := def["set-name"].(string); ok {
					ids[name] = id
				}
				if match, ok := def["match"].(map[string]any); ok {
					if name, ok := match["name"].(string); ok && !strings.ContainsAny(name, "*?[") {
						ids[name] = id
					}
				}
			}
		}
	}
	return ids
}

// RenderNetplan writes the managed VLANs. A parent the host's netplan does not define is declared as a
// trunk port without addresses so that netplan can bring it up.
func RenderNetplan(all []Spec, ids map[string]string) ([]byte, error) {
	vlans := map[string]any{}
	ethernets := map[string]any{}
	for _, s := range all {
		link, ok := ids[s.Parent]
		if !ok {
			link = s.Parent
			ethernets[s.Parent] = map[string]any{"dhcp4": false, "dhcp6": false, "link-local": []string{}, "optional": true}
		}
		v := map[string]any{"id": s.VLAN, "link": link, "dhcp6": false, "link-local": []string{}, "optional": true}
		if s.MTU > 0 {
			v["mtu"] = s.MTU
		}
		switch s.Mode {
		case ModeDHCP:
			v["dhcp4"] = true
			v["dhcp4-overrides"] = map[string]any{"route-metric": s.Metric}
		case ModeStatic:
			v["dhcp4"] = false
			v["addresses"] = []string{s.Address}
			if s.Gateway != "" {
				v["routes"] = []map[string]any{{"to": "0.0.0.0/0", "via": s.Gateway, "metric": s.Metric}}
			}
		default:
			v["dhcp4"] = false
		}
		vlans[s.Name] = v
	}
	network := map[string]any{"version": 2, "vlans": vlans}
	if len(ethernets) > 0 {
		network["ethernets"] = ethernets
	}
	body, e := yaml.Marshal(map[string]any{"network": network})
	if e != nil {
		return nil, e
	}
	return append([]byte(managedHeader), body...), nil
}

func (n Netplan) write(ctx context.Context, all []Spec) error {
	if len(all) == 0 {
		if e := n.h.Remove(NetplanFile); e != nil {
			return e
		}
	} else {
		data, e := RenderNetplan(all, netplanIDs(n.h))
		if e != nil {
			return e
		}
		if e = n.h.WriteFile(NetplanFile, data, 0600); e != nil {
			return e
		}
	}
	if e := run(ctx, n.h, "netplan", "generate"); e != nil {
		return e
	}
	return run(ctx, n.h, "netplan", "apply")
}

func (n Netplan) Apply(ctx context.Context, all []Spec, _ Spec) error { return n.write(ctx, all) }

func (n Netplan) Remove(ctx context.Context, all []Spec, s Spec) error {
	if e := n.write(ctx, all); e != nil {
		return e
	}
	return linkDelete(ctx, n.h, s.Name)
}

func (n Netplan) SetState(ctx context.Context, s Spec, up bool) error {
	return linkState(ctx, n.h, s.Name, up)
}

// ---------- ifupdown (Debian servers) ----------

type Ifupdown struct{ h Host }

const interfacesFile = "/etc/network/interfaces"

func (Ifupdown) Name() string { return BackendIfupdown }

// IfupdownFile avoids dots so that `source-directory` (run-parts naming) also reads it.
func IfupdownFile(s Spec) string {
	return "/etc/network/interfaces.d/vlan-analizer-" + strings.ReplaceAll(s.Name, ".", "_")
}

var sourcesInterfacesD = regexp.MustCompile(`(?m)^\s*source(-directory)?\s+\S*interfaces\.d`)

// RenderIfupdown writes one ifupdown stanza; `metric` keeps DHCP and gateway routes behind the primary route.
func RenderIfupdown(s Spec) []byte {
	method := map[string]string{ModeDHCP: "dhcp", ModeStatic: "static", ModeNone: "manual"}[s.Mode]
	var b strings.Builder
	b.WriteString(managedHeader)
	fmt.Fprintf(&b, "auto %s\niface %s inet %s\n", s.Name, s.Name, method)
	fmt.Fprintf(&b, "    vlan-raw-device %s\n", s.Parent)
	fmt.Fprintf(&b, "    pre-up ip link set dev %s up\n", s.Parent)
	if s.Mode == ModeStatic {
		fmt.Fprintf(&b, "    address %s\n", s.Address)
		if s.Gateway != "" {
			fmt.Fprintf(&b, "    gateway %s\n", s.Gateway)
		}
	}
	if s.Mode == ModeDHCP || s.Gateway != "" {
		fmt.Fprintf(&b, "    metric %d\n", s.Metric)
	}
	if s.MTU > 0 {
		fmt.Fprintf(&b, "    post-up ip link set dev %s mtu %d\n", s.Name, s.MTU)
	}
	return []byte(b.String())
}

func (i Ifupdown) ensureSource() error {
	data, e := i.h.ReadFile(interfacesFile)
	if e != nil {
		return errors.New("/etc/network/interfaces is missing")
	}
	if sourcesInterfacesD.Match(data) {
		return nil
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		data = append(data, '\n')
	}
	data = append(data, []byte("\n# VLAN interfaces managed by VLAN Analizer\nsource /etc/network/interfaces.d/vlan-analizer-*\n")...)
	return i.h.WriteFile(interfacesFile, data, 0644)
}

func (i Ifupdown) Apply(ctx context.Context, _ []Spec, s Spec) error {
	if e := i.ensureSource(); e != nil {
		return e
	}
	if e := i.h.WriteFile(IfupdownFile(s), RenderIfupdown(s), 0644); e != nil {
		return e
	}
	return run(ctx, i.h, "ifup", s.Name)
}

func (i Ifupdown) Remove(ctx context.Context, _ []Spec, s Spec) error {
	_ = run(ctx, i.h, "ifdown", "--force", s.Name)
	if e := i.h.Remove(IfupdownFile(s)); e != nil {
		return e
	}
	return linkDelete(ctx, i.h, s.Name)
}

func (i Ifupdown) SetState(ctx context.Context, s Spec, up bool) error {
	if up {
		return run(ctx, i.h, "ifup", s.Name)
	}
	return run(ctx, i.h, "ifdown", s.Name)
}

// ---------- network-scripts (CentOS/RHEL 7 without NetworkManager) ----------

type NetworkScripts struct{ h Host }

func (NetworkScripts) Name() string { return BackendNetworkScripts }

func NetworkScriptsFile(s Spec) string {
	return path.Join("/etc/sysconfig/network-scripts", "ifcfg-"+s.Name)
}

// RenderIfcfg writes an initscripts VLAN profile that NetworkManager leaves alone.
func RenderIfcfg(s Spec) []byte {
	var b strings.Builder
	b.WriteString(managedHeader)
	lines := []string{"DEVICE=" + s.Name, "PHYSDEV=" + s.Parent, "VLAN=yes", "VLAN_ID=" + strconv.Itoa(s.VLAN), "TYPE=Vlan", "ONBOOT=yes", "NM_CONTROLLED=no", "IPV6INIT=no"}
	switch s.Mode {
	case ModeDHCP:
		lines = append(lines, "BOOTPROTO=dhcp", "DEFROUTE=yes", "METRIC="+strconv.Itoa(s.Metric))
	case ModeStatic:
		ip, prefix := s.Prefix()
		lines = append(lines, "BOOTPROTO=none", "IPADDR="+ip, "PREFIX="+strconv.Itoa(prefix))
		if s.Gateway != "" {
			lines = append(lines, "GATEWAY="+s.Gateway, "DEFROUTE=yes", "METRIC="+strconv.Itoa(s.Metric))
		} else {
			lines = append(lines, "DEFROUTE=no")
		}
	default:
		lines = append(lines, "BOOTPROTO=none", "DEFROUTE=no")
	}
	if s.MTU > 0 {
		lines = append(lines, "MTU="+strconv.Itoa(s.MTU))
	}
	b.WriteString(strings.Join(lines, "\n") + "\n")
	return []byte(b.String())
}

func (n NetworkScripts) Apply(ctx context.Context, _ []Spec, s Spec) error {
	if e := n.h.WriteFile(NetworkScriptsFile(s), RenderIfcfg(s), 0644); e != nil {
		return e
	}
	return run(ctx, n.h, "ifup", s.Name)
}

func (n NetworkScripts) Remove(ctx context.Context, _ []Spec, s Spec) error {
	_ = run(ctx, n.h, "ifdown", s.Name)
	if e := n.h.Remove(NetworkScriptsFile(s)); e != nil {
		return e
	}
	return linkDelete(ctx, n.h, s.Name)
}

func (n NetworkScripts) SetState(ctx context.Context, s Spec, up bool) error {
	if up {
		return run(ctx, n.h, "ifup", s.Name)
	}
	return run(ctx, n.h, "ifdown", s.Name)
}

// ---------- runtime only (ip link; re-created when the agent starts) ----------

type Runtime struct{ h Host }

func (Runtime) Name() string { return BackendRuntime }

// RuntimeCommands lists the ip(8) invocations that create s.
func RuntimeCommands(s Spec) [][]string {
	cmds := [][]string{
		{"ip", "link", "set", "dev", s.Parent, "up"},
		{"ip", "link", "add", "link", s.Parent, "name", s.Name, "type", "vlan", "id", strconv.Itoa(s.VLAN)},
	}
	if s.MTU > 0 {
		cmds = append(cmds, []string{"ip", "link", "set", "dev", s.Name, "mtu", strconv.Itoa(s.MTU)})
	}
	cmds = append(cmds, []string{"ip", "link", "set", "dev", s.Name, "up"})
	if s.Mode == ModeStatic {
		cmds = append(cmds, []string{"ip", "address", "replace", s.Address, "dev", s.Name})
		if s.Gateway != "" {
			cmds = append(cmds, []string{"ip", "route", "replace", "default", "via", s.Gateway, "dev", s.Name, "metric", strconv.Itoa(s.Metric)})
		}
	}
	return cmds
}

func (r Runtime) Apply(ctx context.Context, _ []Spec, s Spec) error {
	if s.Mode == ModeDHCP {
		return errors.New("DHCP needs a persistent network service on the host; use static addressing")
	}
	if e := linkDelete(ctx, r.h, s.Name); e != nil {
		return e
	}
	for _, c := range RuntimeCommands(s) {
		if e := run(ctx, r.h, c[0], c[1:]...); e != nil {
			return e
		}
	}
	return nil
}

func (r Runtime) Remove(ctx context.Context, _ []Spec, s Spec) error {
	return linkDelete(ctx, r.h, s.Name)
}

func (r Runtime) SetState(ctx context.Context, s Spec, up bool) error {
	return linkState(ctx, r.h, s.Name, up)
}
