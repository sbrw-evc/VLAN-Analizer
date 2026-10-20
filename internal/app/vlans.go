package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"

	"github.com/sbrw-evc/VLAN-Analizer/internal/hostnet"
)

// HostNetwork is the privileged host agent that creates and persists VLAN interfaces.
type HostNetwork interface {
	Status(context.Context) (hostnet.Environment, error)
	List(context.Context) (hostnet.Listing, error)
	Create(context.Context, hostnet.Spec) (hostnet.Managed, error)
	Update(context.Context, string, hostnet.Spec) (hostnet.Managed, error)
	Delete(context.Context, string) error
	SetState(context.Context, string, bool) error
}

// SetHostNetwork replaces the agent client (tests and alternative socket paths).
func (a *App) SetHostNetwork(h HostNetwork) {
	a.mu.Lock()
	a.hostNetwork = h
	a.mu.Unlock()
}

func (a *App) agent() HostNetwork {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.hostNetwork
}

func agentFail(w http.ResponseWriter, e error) {
	var ae *hostnet.AgentError
	switch {
	case errors.As(e, &ae):
		fail(w, ae.Status, ae.Message)
	case errors.Is(e, hostnet.ErrUnavailable):
		fail(w, 503, "the host network agent is not running; start it with docker compose up -d")
	default:
		fail(w, 502, "host network agent request failed")
	}
}

func validDNS(servers []string) bool {
	if len(servers) > 4 {
		return false
	}
	for _, s := range servers {
		if ip := net.ParseIP(s); ip == nil || ip.To4() == nil {
			return false
		}
	}
	return true
}

// updateScan applies a change to the monitoring settings and persists them like the settings page does.
func (a *App) updateScan(ctx context.Context, change func(*ScanConfig)) error {
	a.scanMu.Lock()
	defer a.scanMu.Unlock()
	a.mu.RLock()
	scan, vault := a.scan, a.vault
	a.mu.RUnlock()
	next := scan
	next.Interfaces = append([]string{}, scan.Interfaces...)
	next.DNS = map[string][]string{}
	for k, v := range scan.DNS {
		next.DNS[k] = append([]string{}, v...)
	}
	change(&next)
	if e := next.Validate(); e != nil {
		return e
	}
	if e := vault.Write(ctx, "connections/monitoring", next, nil); e != nil {
		return e
	}
	a.mu.Lock()
	a.scan = next
	a.mu.Unlock()
	a.notify()
	return nil
}

func (a *App) networkVLANs(w http.ResponseWriter, r *http.Request, u Session) {
	list, e := a.agent().List(r.Context())
	if errors.Is(e, hostnet.ErrUnavailable) {
		respond(w, 200, Object{"available": false, "detail": "The host network agent is not running. Start it with docker compose up -d."})
		return
	}
	if e != nil {
		agentFail(w, e)
		return
	}
	a.mu.RLock()
	scan := a.scan
	a.mu.RUnlock()
	respond(w, 200, Object{"available": true, "environment": list.Environment, "interfaces": list.Interfaces, "trunks": list.Trunks, "monitoring": Object{"interfaces": scan.Interfaces, "dns_servers": scan.DNS}})
}

type vlanForm struct {
	Parent  string   `json:"parent"`
	VLANs   string   `json:"vlans"`
	Naming  string   `json:"naming"`
	Mode    string   `json:"mode"`
	Address string   `json:"address"`
	Gateway string   `json:"gateway"`
	MTU     int      `json:"mtu"`
	Metric  int      `json:"metric"`
	DNS     []string `json:"dns_servers"`
	Monitor *bool    `json:"monitor"`
}

func (f vlanForm) spec(vlan int) hostnet.Spec {
	return hostnet.Spec{Name: hostnet.NameFor(f.Naming, f.Parent, vlan), Parent: f.Parent, VLAN: vlan, Mode: f.Mode, Address: f.Address, Gateway: f.Gateway, MTU: f.MTU, Metric: f.Metric}
}

func (a *App) createVLANs(w http.ResponseWriter, r *http.Request, u Session) {
	var form vlanForm
	if body(r, w, &form) != nil {
		fail(w, 422, "invalid VLAN interface fields")
		return
	}
	ids, e := hostnet.ParseVLANs(form.VLANs)
	if e != nil {
		fail(w, 422, e.Error())
		return
	}
	if len(ids) > 1 && form.Mode == hostnet.ModeStatic {
		fail(w, 422, "static addressing applies to one VLAN at a time")
		return
	}
	if len(ids) > 1 && form.Metric != 0 {
		fail(w, 422, "a custom route metric applies to one VLAN at a time")
		return
	}
	if !validDNS(form.DNS) {
		fail(w, 422, "DNS servers must be up to four IPv4 addresses")
		return
	}
	specs := make([]hostnet.Spec, 0, len(ids))
	for _, id := range ids {
		s, err := form.spec(id).Normalize()
		if err != nil {
			fail(w, 422, err.Error())
			return
		}
		specs = append(specs, s)
	}
	created, failed := []hostnet.Managed{}, []Object{}
	var last error
	for _, s := range specs {
		item, err := a.agent().Create(r.Context(), s)
		if err != nil {
			if errors.Is(err, hostnet.ErrUnavailable) {
				agentFail(w, err)
				return
			}
			last = err
			failed = append(failed, Object{"vlan": s.VLAN, "name": s.Name, "detail": err.Error()})
			continue
		}
		created = append(created, item)
	}
	if len(created) == 0 {
		agentFail(w, last)
		return
	}
	monitor := form.Monitor == nil || *form.Monitor
	if e = a.updateScan(r.Context(), func(scan *ScanConfig) {
		names := []string{}
		for _, c := range created {
			names = append(names, c.Name)
			if len(form.DNS) > 0 {
				scan.DNS[c.Name] = form.DNS
			}
		}
		switch {
		case monitor && len(scan.Interfaces) > 0:
			scan.Interfaces = append(scan.Interfaces, names...)
		case !monitor && len(scan.Interfaces) == 0:
			// An empty selection monitors every eligible interface; pin the current ones to leave the new VLANs out.
			a.mu.RLock()
			for _, i := range a.inventory.Interfaces {
				if i.Probeable && !slices.Contains(names, i.Name) {
					scan.Interfaces = append(scan.Interfaces, i.Name)
				}
			}
			a.mu.RUnlock()
		}
	}); e != nil {
		failed = append(failed, Object{"detail": "interfaces were created, but monitoring settings were not updated"})
	}
	respond(w, 201, Object{"created": created, "failed": failed})
}

func (a *App) updateVLAN(w http.ResponseWriter, r *http.Request, u Session) {
	var form struct {
		Mode    string    `json:"mode"`
		Address string    `json:"address"`
		Gateway string    `json:"gateway"`
		MTU     int       `json:"mtu"`
		Metric  int       `json:"metric"`
		DNS     *[]string `json:"dns_servers"`
	}
	if body(r, w, &form) != nil {
		fail(w, 422, "invalid VLAN interface fields")
		return
	}
	if form.DNS != nil && !validDNS(*form.DNS) {
		fail(w, 422, "DNS servers must be up to four IPv4 addresses")
		return
	}
	name := r.PathValue("name")
	item, e := a.agent().Update(r.Context(), name, hostnet.Spec{Mode: form.Mode, Address: form.Address, Gateway: form.Gateway, MTU: form.MTU, Metric: form.Metric})
	if e != nil {
		agentFail(w, e)
		return
	}
	if form.DNS != nil {
		if e = a.updateScan(r.Context(), func(scan *ScanConfig) {
			if len(*form.DNS) == 0 {
				delete(scan.DNS, name)
			} else {
				scan.DNS[name] = *form.DNS
			}
		}); e != nil {
			fail(w, 503, "interface updated, but DNS servers were not saved")
			return
		}
	}
	respond(w, 200, item)
}

func (a *App) deleteVLAN(w http.ResponseWriter, r *http.Request, u Session) {
	name := r.PathValue("name")
	if e := a.agent().Delete(r.Context(), name); e != nil {
		agentFail(w, e)
		return
	}
	_ = a.updateScan(r.Context(), func(scan *ScanConfig) {
		delete(scan.DNS, name)
		scan.Interfaces = slices.DeleteFunc(scan.Interfaces, func(n string) bool { return n == name })
	})
	respond(w, 200, Object{"ok": true})
}

func (a *App) vlanState(w http.ResponseWriter, r *http.Request, u Session) {
	state := r.PathValue("state")
	if state != "up" && state != "down" {
		fail(w, 404, "unknown action")
		return
	}
	if e := a.agent().SetState(r.Context(), r.PathValue("name"), state == "up"); e != nil {
		agentFail(w, e)
		return
	}
	a.notify()
	respond(w, 200, Object{"ok": true})
}
