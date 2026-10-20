package hostnet

import (
	"bufio"
	"bytes"
	"context"
	"strings"
)

// Distro is the host's /etc/os-release identity.
type Distro struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version"`
	Family  string `json:"family"`
}

// Supported families. Rocky, AlmaLinux, Oracle Linux and Fedora follow the RHEL family.
const (
	FamilyDebian = "debian"
	FamilyUbuntu = "ubuntu"
	FamilyCentOS = "centos"
	FamilyRHEL   = "rhel"
)

// Environment describes what the agent found on the host.
type Environment struct {
	Distro     Distro   `json:"distro"`
	Backend    string   `json:"backend"`
	Persistent bool     `json:"persistent"`
	Supported  bool     `json:"supported"`
	Warnings   []string `json:"warnings"`
	// Error is set when the agent cannot reach the host; no changes are made then.
	Error string `json:"error,omitempty"`
}

// ErrNoHostAccess explains the deployment requirement when the host namespaces cannot be entered.
const ErrNoHostAccess = "the agent cannot enter the host namespaces; run the netagent service with privileged: true and pid: host"

func parseOSRelease(data []byte) map[string]string {
	out := map[string]string{}
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		k, v, ok := strings.Cut(strings.TrimSpace(s.Text()), "=")
		if !ok || strings.HasPrefix(k, "#") {
			continue
		}
		out[k] = strings.Trim(v, `"'`)
	}
	return out
}

// DetectDistro maps os-release to one of the supported families.
func DetectDistro(h Host) Distro {
	data, e := h.ReadFile("/etc/os-release")
	if e != nil {
		data, _ = h.ReadFile("/usr/lib/os-release")
	}
	r := parseOSRelease(data)
	d := Distro{ID: strings.ToLower(r["ID"]), Name: r["PRETTY_NAME"], Version: r["VERSION_ID"]}
	if d.Name == "" {
		d.Name = r["NAME"]
	}
	like := " " + strings.ToLower(r["ID_LIKE"]) + " "
	switch {
	case d.ID == "ubuntu":
		d.Family = FamilyUbuntu
	case d.ID == "debian":
		d.Family = FamilyDebian
	case d.ID == "centos":
		d.Family = FamilyCentOS
	case d.ID == "rhel":
		d.Family = FamilyRHEL
	case strings.Contains(like, " ubuntu "):
		d.Family = FamilyUbuntu
	case strings.Contains(like, " debian "):
		d.Family = FamilyDebian
	case strings.Contains(like, " rhel ") || strings.Contains(like, " centos ") || strings.Contains(like, " fedora ") || d.ID == "fedora":
		d.Family = FamilyRHEL
	}
	return d
}

func networkManagerRunning(ctx context.Context, h Host) bool {
	if !hasCommand(h, "nmcli") {
		return false
	}
	out, e := h.Run(ctx, "nmcli", "-t", "-f", "RUNNING", "general")
	return e == nil && strings.TrimSpace(string(out)) == "running"
}

func netplanConfigured(h Host) bool {
	files, _ := h.Glob("/etc/netplan/*.yaml")
	return hasCommand(h, "netplan") && len(files) > 0
}

// Detect chooses the persistence backend the host's distribution actually uses:
// netplan on Ubuntu (it renders for networkd or NetworkManager), NetworkManager on RHEL/CentOS 7+
// and NetworkManager desktops, ifupdown on Debian servers, legacy network-scripts on CentOS 7 without
// NetworkManager, and runtime-only `ip link` otherwise.
func Detect(ctx context.Context, h Host) Environment {
	env := Environment{Warnings: []string{}}
	if _, e := h.Run(ctx, "true"); e != nil {
		env.Error = ErrNoHostAccess
		return env
	}
	env.Distro = DetectDistro(h)
	env.Supported = env.Distro.Family != ""
	if !env.Supported {
		env.Warnings = append(env.Warnings, "This distribution is not tested. Supported: Debian, Ubuntu, CentOS and RHEL.")
	}
	nm := networkManagerRunning(ctx, h)
	switch {
	case netplanConfigured(h) && (env.Distro.Family == FamilyUbuntu || !nm):
		env.Backend = BackendNetplan
	case nm:
		env.Backend = BackendNetworkManager
	case exists(h, "/etc/network/interfaces") && hasCommand(h, "ifup"):
		env.Backend = BackendIfupdown
	case len(globOrNil(h, "/etc/sysconfig/network-scripts/ifcfg-*")) > 0 && hasCommand(h, "ifup"):
		env.Backend = BackendNetworkScripts
	default:
		env.Backend = BackendRuntime
		env.Warnings = append(env.Warnings, "No supported network configuration service was found. Interfaces are created with ip link and re-created when the agent starts, but are not written to the host configuration.")
	}
	env.Persistent = env.Backend != BackendRuntime
	return env
}

func globOrNil(h Host, pattern string) []string {
	m, _ := h.Glob(pattern)
	return m
}
