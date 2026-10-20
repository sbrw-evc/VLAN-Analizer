package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type BundleSeed struct {
	ID       string         `json:"id"`
	Password string         `json:"password"`
	Unseal   string         `json:"unseal,omitempty"`
	Root     string         `json:"root,omitempty"`
	Managed  *ManagedConfig `json:"managed,omitempty"`
}

// PrepareBundle is an idempotent Compose init job. No host-side files are required.
func PrepareBundle(dir, users string) error {
	var seed BundleSeed
	path := filepath.Join(dir, "seed.json")
	if e := readJSON(path, &seed); e != nil {
		if !os.IsNotExist(e) {
			return e
		}
		seed = BundleSeed{ID: randomKey(16), Password: randomKey(48)}
		if e = atomicJSON(path, seed); e != nil {
			return e
		}
	}
	if e := os.MkdirAll(users, 0755); e != nil {
		return e
	}
	xml := fmt.Sprintf(`<clickhouse><users><default><networks replace="replace"><ip>127.0.0.1</ip><ip>::1</ip></networks></default><vlan><password_sha256_hex>%s</password_sha256_hex><networks><ip>::/0</ip></networks><profile>default</profile><quota>default</quota><access_management>0</access_management></vlan></users></clickhouse>`, digest(seed.Password))
	return os.WriteFile(filepath.Join(users, "vlan.xml"), []byte(xml), 0644)
}

// The helper resolves Docker service names from inside the isolated bridge.
// Host-network application clients read this atomically replaced routing map.
func managedAddresses(ctx context.Context, dir, baoURL, chURL string) error {
	addresses := map[string]string{}
	for alias, raw := range map[string]string{"managed-openbao:8200": baoURL, "managed-clickhouse:8123": chURL} {
		u, e := url.Parse(raw)
		if e != nil {
			return e
		}
		ips, e := net.DefaultResolver.LookupNetIP(ctx, "ip4", u.Hostname())
		if e != nil || len(ips) == 0 {
			return errors.New("managed service discovery failed")
		}
		port := u.Port()
		if port == "" {
			port = "80"
		}
		addresses[alias] = net.JoinHostPort(ips[0].String(), port)
	}
	return atomicJSON(filepath.Join(dir, "addresses.json"), addresses)
}
func ensureBundle(ctx context.Context, dir, baoURL, chURL string) error {
	var seed BundleSeed
	path := filepath.Join(dir, "seed.json")
	if e := readJSON(path, &seed); e != nil {
		return e
	}
	client := httpClient(10 * time.Second)
	defer client.CloseIdleConnections()
	call := func(method, path, token string, body, out any) (int, error) {
		h := map[string]string{}
		if token != "" {
			h["X-Vault-Token"] = token
		}
		return requestJSON(ctx, client, method, baoURL+"/v1/"+path, h, body, out)
	}
	var status struct {
		Initialized bool `json:"initialized"`
		Sealed      bool `json:"sealed"`
	}
	if _, e := call("GET", "sys/seal-status", "", nil, &status); e != nil {
		return e
	}
	if !status.Initialized {
		var init struct {
			Keys []string `json:"keys_base64"`
			Root string   `json:"root_token"`
		}
		if _, e := call("PUT", "sys/init", "", Object{"secret_shares": 1, "secret_threshold": 1}, &init); e != nil {
			return e
		}
		if len(init.Keys) != 1 {
			return errors.New("invalid managed OpenBao initialization")
		}
		seed.Unseal = init.Keys[0]
		seed.Root = init.Root
		if e := atomicJSON(path, seed); e != nil {
			return e
		}
		status.Sealed = true
	}
	if status.Sealed {
		if seed.Unseal == "" {
			return errors.New("managed recovery material unavailable")
		}
		if _, e := call("PUT", "sys/unseal", "", Object{"key": seed.Unseal}, nil); e != nil {
			return e
		}
	}
	if seed.Managed == nil {
		if seed.Root == "" {
			return errors.New("managed provisioning token unavailable")
		}
		role, e := ProvisionRole(ctx, BaoConfig{URL: baoURL, Mount: "secret", Mode: "token", Token: seed.Root}, "vlan-"+seed.ID)
		if e != nil {
			return e
		}
		role.URL = "http://managed-openbao:8200"
		seed.Managed = &ManagedConfig{ID: seed.ID, Bao: role, ClickHouse: CHConfig{URL: "http://managed-clickhouse:8123", User: "vlan", Database: "default", Password: seed.Password, Managed: true}}
		if e = atomicJSON(path, seed); e != nil {
			return e
		}
	}
	if seed.Root != "" {
		code, e := call("POST", "auth/token/revoke-self", seed.Root, nil, nil)
		if e != nil && code != http.StatusForbidden {
			return e
		}
		seed.Root = ""
		if e := atomicJSON(path, seed); e != nil {
			return e
		}
	}
	if e := managedAddresses(ctx, dir, baoURL, chURL); e != nil {
		return e
	}
	return atomicJSON(filepath.Join(dir, "managed.json"), seed.Managed)
}
func RunBundle(ctx context.Context, dir, baoURL, chURL string) error {
	lastError := ""
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		iteration, cancel := context.WithTimeout(ctx, 30*time.Second)
		e := ensureBundle(iteration, dir, baoURL, chURL)
		cancel()
		if e == nil {
			lastError = ""
			_ = os.WriteFile(filepath.Join(dir, "ready"), []byte(time.Now().UTC().Format(time.RFC3339)), 0600)
		} else {
			if e.Error() != lastError {
				log.Printf("managed services are not ready: %s", e)
				lastError = e.Error()
			}
			_ = os.Remove(filepath.Join(dir, "ready"))
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func BundleReady(dir string) bool {
	info, e := os.Stat(filepath.Join(dir, "ready"))
	return e == nil && time.Since(info.ModTime()) < 20*time.Second
}
func Health(raw string) error {
	resp, e := httpClient(5 * time.Second).Get(raw)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return errors.New("not ready")
	}
	return nil
}
