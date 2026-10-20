package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

func (a *App) syncSource(ctx context.Context, source string) (Object, error) {
	a.importMu.Lock()
	defer a.importMu.Unlock()
	a.mu.RLock()
	repo := a.repo
	c := a.connections
	a.mu.RUnlock()
	var importer Importer
	switch source {
	case "gatus":
		if !c.Gatus.Enabled {
			return nil, errors.New("Gatus import is disabled")
		}
		importer = GatusImporter{c.Gatus}
	case "kuma":
		if !c.Kuma.Enabled {
			return nil, errors.New("Kuma import is disabled")
		}
		importer = KumaImporter{c.Kuma}
	default:
		return nil, errors.New("unknown source")
	}
	sites, warnings, e := importer.Fetch(ctx)
	if e != nil {
		a.mu.Lock()
		a.warnings[source] = []string{source + " import failed; verify connection and permissions"}
		a.mu.Unlock()
		return nil, e
	}
	old, e := repo.Sites(ctx)
	if e != nil {
		return nil, e
	}
	existing := map[string]Site{}
	for _, s := range old {
		existing[s.ID] = s
	}
	seen := map[string]bool{}
	for _, s := range sites {
		seen[s.ID] = true
		if prev, ok := existing[s.ID]; ok {
			s.Enabled = prev.Enabled
			if prev.Name == s.Name && prev.URL == s.URL && prev.Enabled == s.Enabled && prev.SourceEnabled == s.SourceEnabled {
				continue
			}
		}
		if e = repo.SaveSite(ctx, s); e != nil {
			return nil, e
		}
	}
	retired := 0
	if len(warnings) == 0 {
		for _, s := range old {
			if s.Source == source && !seen[s.ID] {
				s.Deleted = 1
				if e = repo.SaveSite(ctx, s); e != nil {
					return nil, e
				}
				retired++
			}
		}
	}
	a.mu.Lock()
	a.warnings[source] = warnings
	a.mu.Unlock()
	return Object{"imported": len(sites), "retired": retired, "warnings": warnings}, nil
}
func (a *App) loop(ctx context.Context) {
	lastSync := map[string]time.Time{}
	for {
		a.mu.RLock()
		scan, connections, repo := a.scan, a.connections, a.repo
		a.mu.RUnlock()
		for source, enabled := range map[string]bool{"gatus": connections.Gatus.Enabled, "kuma": connections.Kuma.Enabled} {
			if enabled && time.Since(lastSync[source]) > 5*time.Minute {
				syncCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
				_, _ = a.syncSource(syncCtx, source)
				cancel()
				lastSync[source] = time.Now()
			}
		}
		e := a.cycle(ctx, repo, scan)
		a.mu.Lock()
		a.running = false
		if e != nil {
			a.workerError = "Monitoring cycle failed; check inventory and storage"
		} else {
			a.workerError = ""
			a.lastCycle = timestamp()
		}
		a.mu.Unlock()
		timer := time.NewTimer(time.Duration(scan.Interval) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-a.wake:
			timer.Stop()
			lastSync = map[string]time.Time{}
		case <-timer.C:
		}
	}
}
func (a *App) cycle(ctx context.Context, repo Repository, scan ScanConfig) error {
	a.mu.Lock()
	a.running = true
	a.mu.Unlock()
	inventoryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	inventory, e := a.inventoryReader.Discover(inventoryCtx, nil)
	cancel()
	if e != nil {
		return e
	}
	selected := map[string]bool{}
	for _, name := range scan.Interfaces {
		selected[name] = true
	}
	for n := range inventory.Interfaces {
		iface := &inventory.Interfaces[n]
		if len(selected) > 0 {
			iface.Probeable = iface.Probeable && selected[iface.Name]
		} else if strings.HasPrefix(iface.Name, "docker") || strings.HasPrefix(iface.Name, "br-") || iface.Kind == "veth" {
			iface.Probeable = false
		}
	}
	a.mu.Lock()
	a.inventory = inventory
	a.inventoryAt = timestamp()
	a.mu.Unlock()
	if e = repo.Inventory(ctx, inventory); e != nil {
		return e
	}
	sites, e := repo.Sites(ctx)
	if e != nil {
		return e
	}
	type job struct {
		s Site
		i Interface
	}
	jobs := make(chan job)
	results := make(chan Object, scan.Concurrency)
	var wg sync.WaitGroup
	for n := 0; n < scan.Concurrency; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				probeCtx, cancel := context.WithTimeout(ctx, time.Duration(scan.Timeout*2+20)*time.Second)
				r := a.prober.Check(probeCtx, j.s, j.i, scan)
				cancel()
				select {
				case results <- r:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, s := range sites {
			if !s.Active() {
				continue
			}
			for _, i := range inventory.Interfaces {
				if i.Probeable {
					select {
					case jobs <- job{s, i}:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	go func() { wg.Wait(); close(results) }()
	batch := []Object{}
	var storeErr error
	for r := range results {
		batch = append(batch, r)
		if len(batch) >= 64 {
			if err := repo.Checks(ctx, batch); err != nil {
				storeErr = err
			}
			batch = []Object{}
		}
	}
	if len(batch) > 0 {
		if err := repo.Checks(ctx, batch); err != nil {
			storeErr = err
		}
	}
	return storeErr
}
func freshResults(rows []Object, scan ScanConfig, sites []Site, inventory Inventory) []Object {
	ids, ifaces := map[string]bool{}, map[string]bool{}
	for _, s := range sites {
		if s.Active() {
			ids[s.ID] = true
		}
	}
	for _, i := range inventory.Interfaces {
		if i.Probeable {
			ifaces[i.Name] = true
		}
	}
	out := []Object{}
	for _, r := range rows {
		if !ids[text(r["site_id"])] || !ifaces[text(r["interface"])] {
			continue
		}
		r["measured_status"] = r["status"]
		t, e := time.Parse(time.RFC3339Nano, text(r["at"]))
		age := time.Since(t).Seconds()
		r["age_seconds"] = int(age)
		if e != nil || age > float64(scan.StaleAfter) {
			r["status"] = "unknown"
		}
		out = append(out, r)
	}
	return out
}
func (a *App) snapshot(ctx context.Context) (Object, error) {
	a.mu.RLock()
	repo, inventory, scan := a.repo, a.inventory, a.scan
	inventoryAt, lastCycle, running, workerErr := a.inventoryAt, a.lastCycle, a.running, a.workerError
	warnings := map[string][]string{}
	for k, v := range a.warnings {
		warnings[k] = append([]string{}, v...)
	}
	a.mu.RUnlock()
	sites, e := repo.Sites(ctx)
	if e != nil {
		return nil, e
	}
	rows, e := repo.Latest(ctx)
	if e != nil {
		return nil, e
	}
	return Object{"sites": sites, "interfaces": inventory.Interfaces, "bridge_vlans": inventory.Bridge, "inventory_at": inventoryAt, "results": freshResults(rows, scan, sites, inventory), "worker": Object{"running": running, "last_cycle": lastCycle, "error": workerErr, "gatus_warnings": warnings["gatus"], "kuma_warnings": warnings["kuma"]}}, nil
}
func zabbixItems(snapshot Object) []Object {
	out := []Object{}
	lookup := map[string]Object{}
	for _, r := range snapshot["results"].([]Object) {
		lookup[text(r["site_id"])+"/"+text(r["interface"])] = r
	}
	for _, s := range snapshot["sites"].([]Site) {
		if !s.Active() {
			continue
		}
		for _, i := range snapshot["interfaces"].([]Interface) {
			if !i.Probeable {
				continue
			}
			r := lookup[s.ID+"/"+i.Name]
			state := text(r["status"])
			if state == "" {
				state = "unknown"
			}
			available := -1
			switch state {
			case "up":
				available = 1
			case "down":
				available = 0
			case "degraded":
				available = 2
			}
			item := Object{"site_id": s.ID, "site": s.Name, "interface": i.Name, "vlan_id": i.VLAN, "status": state, "available": available, "at": r["at"]}
			for target, path := range map[string][2]string{"dns_ms": {"dns", "ms"}, "tcp_ms": {"", "tcp_ms"}, "http_code": {"http", "code"}, "http_ms": {"http", "total_ms"}, "page_ms": {"browser", "load_ms"}, "tls_days": {"tls", "expires_days"}} {
				if path[0] == "" {
					item[target] = r[path[1]]
				} else {
					if nested, ok := r[path[0]].(Object); ok {
						item[target] = nested[path[1]]
					} else {
						item[target] = nil
					}
				}
			}
			out = append(out, item)
		}
	}
	return out
}
