package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sbrw-evc/VLAN-Analizer/internal/hostnet"
)

// App orchestrates dependencies; HTTP, SQL, network and secrets remain replaceable adapters.
type App struct {
	root                   context.Context
	rootCancel             context.CancelFunc
	mu                     sync.RWMutex
	installMu, importMu    sync.Mutex
	scanMu                 sync.Mutex
	dir, managedPath       string
	config                 *InstallConfig
	repo                   Repository
	vault                  SecretStore
	auth                   *AuthService
	connections            Connections
	scan                   ScanConfig
	inventory              Inventory
	inventoryAt, lastCycle string
	workerError            string
	warnings               map[string][]string
	running                bool
	wake                   chan struct{}
	inventoryReader        InventoryReader
	prober                 Prober
	hostNetwork            HostNetwork
	wg                     sync.WaitGroup
}

func New(dir, managedPath string) (*App, error) {
	if e := os.MkdirAll(dir, 0700); e != nil {
		return nil, e
	}
	root, rootCancel := context.WithCancel(context.Background())
	a := &App{root: root, rootCancel: rootCancel, dir: dir, managedPath: managedPath, connections: DefaultConnections(), scan: DefaultScan(), inventory: Inventory{Interfaces: []Interface{}, Bridge: []Object{}}, warnings: map[string][]string{"gatus": {}, "kuma": {}}, wake: make(chan struct{}, 1), inventoryReader: LinuxInventory{}, prober: NetworkProber{Browser: Chromium{}}, hostNetwork: hostnet.NewClient(hostnet.DefaultSocket)}
	var config InstallConfig
	e := readJSON(filepath.Join(dir, "installation-go.json"), &config)
	if e == nil {
		a.config = &config
	} else if !os.IsNotExist(e) {
		return nil, e
	} else {
		if data, err := os.ReadFile(filepath.Join(dir, "installation.json")); err == nil {
			if e = a.migrate(data); e != nil {
				return nil, e
			}
		}
	}
	return a, nil
}
func (a *App) migrate(data []byte) error {
	var old struct {
		Settings map[string]string `json:"settings"`
	}
	if e := json.Unmarshal(data, &old); e != nil {
		return e
	}
	var creds BaoConfig
	if e := readJSON(filepath.Join(a.dir, "approle.json"), &creds); e != nil {
		return e
	}
	creds.URL = old.Settings["bao_url"]
	creds.Mount = old.Settings["bao_mount"]
	creds.Mode = "approle"
	config := InstallConfig{Version: 2, ID: randomKey(16), PublicURL: old.Settings["public_url"], Bao: creds, ClickHouse: CHConfig{URL: old.Settings["clickhouse_url"], User: old.Settings["clickhouse_user"], Database: old.Settings["clickhouse_database"]}, Scan: DefaultScan()}
	if config.ClickHouse.Database == "" {
		config.ClickHouse.Database = "default"
	}
	if e := atomicJSON(filepath.Join(a.dir, "installation-go.json"), config); e != nil {
		return e
	}
	a.config = &config
	return nil
}
func (a *App) Start(parent context.Context) {
	ctx := a.root
	go func() {
		select {
		case <-parent.Done():
			a.rootCancel()
		case <-ctx.Done():
		}
	}()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		timer := time.NewTicker(15 * time.Second)
		defer timer.Stop()
		for {
			a.mu.RLock()
			configured := a.config != nil
			ready := a.repo != nil
			a.mu.RUnlock()
			if configured && !ready {
				_ = a.activate(ctx)
			}
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
			}
		}
	}()
}
func (a *App) Close() {
	a.rootCancel()
	a.wg.Wait()
}
func (a *App) activate(ctx context.Context) error {
	a.installMu.Lock()
	defer a.installMu.Unlock()
	a.mu.RLock()
	if a.repo != nil || a.config == nil {
		a.mu.RUnlock()
		return nil
	}
	config := *a.config
	a.mu.RUnlock()
	vault := NewVault(config.Bao)
	var secrets Object
	found, e := vault.Read(ctx, "app", &secrets)
	if e != nil || !found {
		return errors.New("OpenBao unavailable")
	}
	config.ClickHouse.Password = text(secrets["clickhouse_password"])
	repo := NewClickHouse(config.ClickHouse)
	if e = repo.Init(ctx); e != nil {
		return e
	}
	connections := DefaultConnections()
	for _, entry := range []struct {
		key string
		out any
	}{{"gatus", &connections.Gatus}, {"kuma", &connections.Kuma}, {"entra", &connections.Entra}} {
		_, err := vault.Read(ctx, "connections/"+entry.key, entry.out)
		if err != nil {
			return err
		}
	}
	scan := config.Scan
	if scan.Interval == 0 {
		scan = DefaultScan()
	}
	if found, e = vault.Read(ctx, "connections/monitoring", &scan); e != nil {
		return e
	}
	if e = scan.Validate(); e != nil {
		return e
	}
	key := text(secrets["session_key"])
	if len(key) < 32 {
		return errors.New("invalid session signing key")
	}
	a.mu.Lock()
	a.vault = vault
	a.repo = repo
	a.auth = NewAuth(vault, key)
	a.connections = connections
	a.scan = scan
	a.mu.Unlock()
	a.wg.Add(1)
	go func() { defer a.wg.Done(); a.loop(a.root) }()
	return nil
}
func (a *App) Status() Object {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, managedErr := os.Stat(a.managedPath)
	return Object{"required": a.config == nil, "ready": a.repo != nil, "error": func() string {
		if a.config != nil && a.repo == nil {
			return "Waiting for configured OpenBao and ClickHouse to reconnect."
		}
		return ""
	}(), "entra_enabled": a.connections.Entra.Enabled, "managed_available": managedErr == nil, "setup_access": "browser", "version": Version}
}
func (a *App) resolveSetup(form SetupForm) (SetupForm, string, error) {
	id := ""
	if form.Bao.Mode == "managed" || form.ClickHouse.Managed {
		var managed ManagedConfig
		if e := readJSON(a.managedPath, &managed); e != nil {
			return form, id, errors.New("managed services are not ready")
		}
		id = managed.ID
		if form.Bao.Mode == "managed" {
			form.Bao = managed.Bao
		}
		if form.ClickHouse.Managed {
			form.ClickHouse = managed.ClickHouse
		}
	}
	if form.Bao.Mount == "" {
		form.Bao.Mount = "secret"
	}
	if form.ClickHouse.Database == "" {
		form.ClickHouse.Database = "default"
	}
	return form, id, nil
}
func (a *App) Complete(ctx context.Context, form SetupForm) error {
	a.installMu.Lock()
	a.mu.RLock()
	configured := a.config != nil
	a.mu.RUnlock()
	if configured {
		a.installMu.Unlock()
		return errors.New("initial setup is already complete")
	}
	defer a.installMu.Unlock()
	form.PublicURL = strings.TrimRight(form.PublicURL, "/")
	if e := originURL(form.PublicURL); e != nil {
		return e
	}
	if !namePattern.MatchString(form.Admin) || len(form.Password) < 12 || len(form.Password) > 1024 {
		return errors.New("invalid administrator name or password")
	}
	if e := form.Entra.Validate(); e != nil {
		return e
	}
	resolved, id, e := a.resolveSetup(form)
	if e != nil {
		return e
	}
	form = resolved
	if e = serverURL(form.Bao.URL); e != nil {
		return e
	}
	if e = serverURL(form.ClickHouse.URL); e != nil {
		return e
	}
	if !namePattern.MatchString(form.Bao.Mount) {
		return errors.New("invalid KV mount")
	}
	if id == "" {
		id = randomKey(16)
	}
	candidate := InstallConfig{Version: 2, ID: id, PublicURL: form.PublicURL, Bao: form.Bao, ClickHouse: form.ClickHouse, Scan: DefaultScan(), Admin: form.Admin}
	pending := filepath.Join(a.dir, "pending-go.json")
	var journal InstallConfig
	if e = readJSON(pending, &journal); e == nil {
		if journal.PublicURL != candidate.PublicURL || journal.Bao.URL != candidate.Bao.URL || journal.ClickHouse.URL != candidate.ClickHouse.URL || journal.Admin != candidate.Admin {
			return errors.New("retry incomplete setup with the original connection settings")
		}
		candidate = journal
	} else if !os.IsNotExist(e) {
		return e
	} else {
		if candidate.Bao.Mode == "token" {
			role, err := ProvisionRole(ctx, candidate.Bao, "vlan-"+id)
			if err != nil {
				return err
			}
			candidate.Bao = role
		}
		candidate.ClickHouse.Password = ""
		candidate.Bao.Token = ""
		if e = atomicJSON(pending, candidate); e != nil {
			return e
		}
	}
	vault := NewVault(candidate.Bao)
	repo := NewClickHouse(form.ClickHouse)
	if e = repo.Init(ctx); e != nil {
		return e
	}
	var secrets Object
	found, e := vault.Read(ctx, "app", &secrets)
	if e != nil {
		return e
	}
	if found && text(secrets["installation_id"]) != candidate.ID {
		return errors.New("OpenBao contains another installation; no secrets were replaced")
	}
	zero := 0
	if !found {
		secrets = Object{"installation_id": candidate.ID, "session_key": randomKey(64), "zabbix_token": randomKey(48), "clickhouse_password": form.ClickHouse.Password}
		if e = vault.Write(ctx, "app", secrets, &zero); e != nil {
			return e
		}
	} else if text(secrets["clickhouse_password"]) != form.ClickHouse.Password {
		return errors.New("retry with the original ClickHouse password")
	}
	var user User
	found, e = vault.Read(ctx, "users/"+form.Admin, &user)
	if e != nil {
		return e
	}
	if found {
		if user.Role != "admin" || user.Disabled || !VerifyPassword(user.PasswordHash, form.Password) {
			return errors.New("administrator cannot be replaced")
		}
	} else {
		user, e = newUser(form.Admin, form.Password, "admin", false)
		if e != nil {
			return e
		}
		if e = vault.Write(ctx, "users/"+form.Admin, user, &zero); e != nil {
			return e
		}
	}
	if e = vault.Write(ctx, "connections/entra", form.Entra, nil); e != nil {
		return e
	}
	if e = atomicJSON(filepath.Join(a.dir, "installation-go.json"), candidate); e != nil {
		return e
	}
	a.mu.Lock()
	a.config = &candidate
	a.mu.Unlock()
	_ = os.Remove(pending)
	a.notify()
	return nil
}
func ProvisionRole(ctx context.Context, c BaoConfig, name string) (BaoConfig, error) {
	client := httpClient(15 * time.Second)
	defer client.CloseIdleConnections()
	h := map[string]string{"X-Vault-Token": c.Token}
	call := func(method, path string, value, out any) error {
		_, e := requestJSON(ctx, client, method, c.URL+"/v1/"+path, h, value, out)
		return e
	}
	var mounts struct {
		Data map[string]Object `json:"data"`
	}
	if e := call("GET", "sys/mounts", nil, &mounts); e != nil {
		return c, e
	}
	if mount, found := mounts.Data[c.Mount+"/"]; found {
		options, _ := mount["options"].(map[string]any)
		if text(mount["type"]) != "kv" || text(options["version"]) != "2" {
			return c, errors.New("KV v2 mount required")
		}
	} else if e := call("POST", "sys/mounts/"+c.Mount, Object{"type": "kv", "options": Object{"version": "2"}}, nil); e != nil {
		return c, e
	}
	var methods struct {
		Data map[string]any `json:"data"`
	}
	if e := call("GET", "sys/auth", nil, &methods); e != nil {
		return c, e
	}
	if _, ok := methods.Data["approle/"]; !ok {
		if e := call("POST", "sys/auth/approle", Object{"type": "approle"}, nil); e != nil {
			return c, e
		}
	}
	policy := fmt.Sprintf(`path "%s/data/vlan-analizer/app" { capabilities=["create","read"] }
path "%s/data/vlan-analizer/users/*" { capabilities=["create","read","update"] }
path "%s/data/vlan-analizer/connections/*" { capabilities=["create","read","update"] }`, c.Mount, c.Mount, c.Mount)
	if e := call("POST", "sys/policies/acl/"+name, Object{"policy": policy}, nil); e != nil {
		return c, e
	}
	if e := call("POST", "auth/approle/role/"+name, Object{"token_policies": []string{name}, "token_ttl": "1h", "token_max_ttl": "4h", "secret_id_ttl": "0", "secret_id_num_uses": 0}, nil); e != nil {
		return c, e
	}
	var role struct {
		Data struct {
			ID string `json:"role_id"`
		} `json:"data"`
	}
	var secret struct {
		Data struct {
			ID string `json:"secret_id"`
		} `json:"data"`
	}
	if e := call("GET", "auth/approle/role/"+name+"/role-id", nil, &role); e != nil {
		return c, e
	}
	if e := call("POST", "auth/approle/role/"+name+"/secret-id", Object{}, &secret); e != nil {
		return c, e
	}
	return BaoConfig{URL: c.URL, Mount: c.Mount, Mode: "approle", RoleID: role.Data.ID, SecretID: secret.Data.ID}, nil
}
func (a *App) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}
