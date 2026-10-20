package app

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

func TestChromiumIntegration(t *testing.T) {
	if os.Getenv("RUN_BROWSER_TESTS") != "1" {
		t.Skip("requires Chromium runtime")
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing.js" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if r.URL.Path == "/broken" {
			fmt.Fprint(w, `<html><script src="/missing.js"></script><body>Broken resource</body></html>`)
			return
		}
		fmt.Fprint(w, `<html><head><link rel="icon" href="data:,"></head><body>Healthy page</body></html>`)
	}))
	server.Listener.Close()
	listener, e := net.Listen("tcp4", "127.0.0.1:8080")
	if e != nil {
		t.Fatal(e)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()
	proxy, e := NewBoundProxy(context.Background(), &BoundDialer{Interface: "lo", Source: "127.0.0.1"})
	if e != nil {
		t.Fatal(e)
	}
	defer proxy.Close()
	for _, path := range []string{"/", "/broken"} {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		result := (Chromium{}).Check(ctx, proxy.URL(), server.URL+path)
		cancel()
		if path == "/" && result["ok"] != true {
			t.Fatal(result)
		}
		if path == "/broken" && result["ok"] != false {
			t.Fatal("broken resource appeared healthy", result)
		}
	}
}

func TestMariaDBIntegration(t *testing.T) {
	if os.Getenv("RUN_KUMA_TESTS") != "1" {
		t.Skip("requires disposable MariaDB")
	}
	db, e := sql.Open("mysql", "root:integration-root-password@tcp(127.0.0.1:3307)/kuma")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE monitor (id INT PRIMARY KEY, name VARCHAR(100), url TEXT, type VARCHAR(50), active INT, parent INT NULL)`,
		`INSERT INTO monitor VALUES (1,'Healthy','https://example.com','http',1,NULL),(2,'Paused','https://paused.example.com','http',0,NULL),(3,'Ping',NULL,'ping',1,NULL)`,
		`CREATE USER 'vlan_reader'@'%' IDENTIFIED BY 'integration-kuma-password'`,
		`GRANT SELECT (id,name,url,type,active,parent) ON kuma.monitor TO 'vlan_reader'@'%'`,
	} {
		if _, e = db.Exec(q); e != nil {
			t.Fatal(e)
		}
	}
	c := KumaConfig{Host: "127.0.0.1", Port: 3307, User: "vlan_reader", Password: "integration-kuma-password", Database: "kuma", Instance: "ci"}
	sites, _, e := (KumaImporter{c}).Fetch(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(sites) != 2 || !sites[0].Active() || sites[1].Active() {
		t.Fatal(sites)
	}
	reader, e := sql.Open("mysql", "vlan_reader:integration-kuma-password@tcp(127.0.0.1:3307)/kuma")
	if e != nil {
		t.Fatal(e)
	}
	defer reader.Close()
	if _, e = reader.Exec("DELETE FROM monitor"); e == nil {
		t.Fatal("import user can mutate source")
	}
}

func TestManagedStorageIntegration(t *testing.T) {
	path := os.Getenv("MANAGED_TEST_CONFIG")
	if path == "" {
		t.Skip("requires bundled Compose services")
	}
	var managed ManagedConfig
	if e := readJSON(path, &managed); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	vault := NewVault(managed.Bao)
	var secrets Object
	found, e := vault.Read(ctx, "app", &secrets)
	if e != nil || !found {
		t.Fatal("application secrets unavailable", e)
	}
	// Domain revisions survive the KV service without float64 rounding.
	version := Revision(time.Now().UnixNano())
	if e = vault.Write(ctx, "connections/integration-test", Object{"version": version}, nil); e != nil {
		t.Fatal(e)
	}
	var restored struct {
		Version Revision `json:"version"`
	}
	if _, e = vault.Read(ctx, "connections/integration-test", &restored); e != nil || restored.Version != version {
		t.Fatal("KV round trip", e)
	}
	repo := NewClickHouse(managed.ClickHouse)
	if e = repo.Init(ctx); e != nil {
		t.Fatal(e)
	}
	s := Site{ID: "integration-history", Name: "Storage integration", URL: "http://127.0.0.1:8080/", Source: "manual", Enabled: 0, SourceEnabled: 1}
	if e = repo.SaveSite(ctx, s); e != nil {
		t.Fatal(e)
	}
	sites, e := repo.Sites(ctx)
	if e != nil || len(sites) == 0 {
		t.Fatal("sites round trip", e)
	}
	row := Object{"at": timestamp(), "site_id": s.ID, "interface": "integration.999", "status": "down", "http": Object{"code": 503}}
	if e = repo.Checks(ctx, []Object{row}); e != nil {
		t.Fatal(e)
	}
	history, e := repo.History(ctx, s.ID, "integration.999", 24)
	if e != nil || len(history) != 1 {
		t.Fatal("history round trip", e)
	}
	if text(history[0]["status"]) != "down" {
		t.Fatal(history)
	}
	s.Deleted = 1
	if e = repo.SaveSite(ctx, s); e != nil {
		t.Fatal(e)
	}
}
