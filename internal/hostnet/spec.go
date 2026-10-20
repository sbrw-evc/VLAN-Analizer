// Package hostnet creates and persists 802.1Q VLAN interfaces on the Docker host through the host's own
// network stack (NetworkManager, netplan, ifupdown or network-scripts), so trunk subinterfaces survive reboots.
package hostnet

import (
	"errors"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Addressing modes of a VLAN interface.
const (
	ModeDHCP   = "dhcp"
	ModeStatic = "static"
	ModeNone   = "none"
)

// MaxBatch bounds the number of VLANs created by one request.
const MaxBatch = 64

// Spec is the desired configuration of one managed VLAN interface.
type Spec struct {
	Name    string `json:"name"`
	Parent  string `json:"parent"`
	VLAN    int    `json:"vlan"`
	Mode    string `json:"mode"`
	Address string `json:"address,omitempty"`
	Gateway string `json:"gateway,omitempty"`
	MTU     int    `json:"mtu,omitempty"`
	Metric  int    `json:"metric,omitempty"`
}

var deviceName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.-]{0,14}$`)

// ValidDevice reports whether name is a safe Linux interface name.
func ValidDevice(name string) bool {
	return deviceName.MatchString(name) && name != "." && name != ".." && !strings.Contains(name, "..")
}

// DefaultName is parent.vid, or vlan<vid> when parent.vid exceeds the 15-character kernel limit.
func DefaultName(parent string, vlan int) string {
	if name := fmt.Sprintf("%s.%d", parent, vlan); len(name) <= 15 {
		return name
	}
	return fmt.Sprintf("vlan%d", vlan)
}

// NameFor applies a naming style: "vlan" gives vlan<vid>, anything else the default dotted name.
func NameFor(style, parent string, vlan int) string {
	if style == "vlan" {
		return fmt.Sprintf("vlan%d", vlan)
	}
	return DefaultName(parent, vlan)
}

// DefaultMetric keeps probe default routes behind the host's primary route and distinct per VLAN.
func DefaultMetric(vlan int) int { return 2000 + vlan }

// Normalize fills defaults and returns a validated copy.
func (s Spec) Normalize() (Spec, error) {
	s.Parent = strings.TrimSpace(s.Parent)
	s.Name = strings.TrimSpace(s.Name)
	s.Address = strings.TrimSpace(s.Address)
	s.Gateway = strings.TrimSpace(s.Gateway)
	if s.Mode == "" {
		s.Mode = ModeDHCP
	}
	if s.Name == "" {
		s.Name = DefaultName(s.Parent, s.VLAN)
	}
	if s.Metric == 0 {
		s.Metric = DefaultMetric(s.VLAN)
	}
	return s, s.Validate()
}

// Validate checks every field; names are limited to the forms ifupdown and network-scripts understand.
func (s Spec) Validate() error {
	if !ValidDevice(s.Parent) {
		return errors.New("invalid parent interface name")
	}
	if s.VLAN < 1 || s.VLAN > 4094 {
		return errors.New("VLAN ID must be between 1 and 4094")
	}
	if s.Name != fmt.Sprintf("%s.%d", s.Parent, s.VLAN) && s.Name != fmt.Sprintf("vlan%d", s.VLAN) {
		return errors.New("interface name must be parent.vid or vlan<vid>")
	}
	if !ValidDevice(s.Name) {
		return errors.New("interface name must be at most 15 characters")
	}
	if s.MTU != 0 && (s.MTU < 576 || s.MTU > 9216) {
		return errors.New("MTU must be between 576 and 9216")
	}
	if s.Metric < 1 || s.Metric > 65535 {
		return errors.New("route metric must be between 1 and 65535")
	}
	switch s.Mode {
	case ModeDHCP, ModeNone:
		if s.Address != "" || s.Gateway != "" {
			return errors.New("addresses are only used with static addressing")
		}
	case ModeStatic:
		ip, network, e := net.ParseCIDR(s.Address)
		if e != nil || ip.To4() == nil {
			return errors.New("static addressing needs an IPv4 address in CIDR form, for example 10.20.0.5/24")
		}
		if ones, _ := network.Mask.Size(); ones < 8 || ones > 32 {
			return errors.New("IPv4 prefix length must be between 8 and 32")
		}
		if s.Gateway != "" {
			gw := net.ParseIP(s.Gateway)
			if gw == nil || gw.To4() == nil || !network.Contains(gw) || gw.Equal(ip) {
				return errors.New("gateway must be another IPv4 address in the interface subnet")
			}
		}
	default:
		return errors.New("addressing must be dhcp, static or none")
	}
	return nil
}

// Prefix returns the address and prefix length of a static spec.
func (s Spec) Prefix() (string, int) {
	ip, network, e := net.ParseCIDR(s.Address)
	if e != nil {
		return "", 0
	}
	ones, _ := network.Mask.Size()
	return ip.String(), ones
}

// ParseVLANs parses "10, 20, 30-35" into sorted unique IDs.
func ParseVLANs(raw string) ([]int, error) {
	seen := map[int]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		from, to, ranged := strings.Cut(part, "-")
		a, e := strconv.Atoi(strings.TrimSpace(from))
		b := a
		if e == nil && ranged {
			b, e = strconv.Atoi(strings.TrimSpace(to))
		}
		if e != nil || a < 1 || b > 4094 || a > b {
			return nil, fmt.Errorf("invalid VLAN list item %q", part)
		}
		for v := a; v <= b; v++ {
			seen[v] = true
			if len(seen) > MaxBatch {
				return nil, fmt.Errorf("at most %d VLANs per request", MaxBatch)
			}
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("enter at least one VLAN ID")
	}
	out := make([]int, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Ints(out)
	return out, nil
}
