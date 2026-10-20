package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/go-sql-driver/mysql"
	"gopkg.in/yaml.v3"
)

type GatusImporter struct{ Config GatusConfig }

func (g GatusImporter) Fetch(ctx context.Context) ([]Site, []string, error) {
	var data []byte
	var e error
	if g.Config.Path != "" {
		data, e = os.ReadFile(g.Config.Path)
	} else {
		r, err := http.NewRequestWithContext(ctx, "GET", g.Config.URL+"/api/v1/endpoints/statuses", nil)
		if err != nil {
			return nil, nil, err
		}
		if g.Config.Token != "" {
			r.Header.Set("Authorization", "Bearer "+g.Config.Token)
		}
		resp, err := httpClient(20 * time.Second).Do(r)
		if err != nil {
			return nil, nil, errors.New("Gatus connection failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, nil, errors.New("Gatus returned an error")
		}
		data, e = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	}
	if e != nil {
		return nil, nil, e
	}
	var rows []Object
	if g.Config.Path != "" {
		var doc struct {
			Endpoints []Object `yaml:"endpoints"`
		}
		if e = yaml.Unmarshal(data, &doc); e != nil {
			return nil, nil, e
		}
		rows = doc.Endpoints
	} else {
		if e = json.Unmarshal(data, &rows); e != nil {
			return nil, nil, e
		}
	}
	if len(rows) > 1000 {
		return nil, nil, errors.New("source exceeds 1000 endpoints")
	}
	sites, warnings := []Site{}, []string{}
	seen := map[string]bool{}
	for i, row := range rows {
		u := text(row["url"])
		if e = validateURL(u); e != nil {
			warnings = append(warnings, fmt.Sprintf("Gatus endpoint %d: URL unavailable or unsupported", i+1))
			continue
		}
		id := "gatus-" + digest(u)[:24]
		if seen[id] {
			continue
		}
		seen[id] = true
		name := text(row["name"])
		if name == "" {
			name = u
		}
		sites = append(sites, Site{ID: id, Name: name, URL: u, Source: "gatus", Enabled: 1, SourceEnabled: 1})
	}
	return sites, warnings, nil
}

type KumaMonitor struct {
	ID     int64
	Name   string
	URL    string
	Type   string
	Active bool
	Parent *int64
}

func ParseKuma(rows []KumaMonitor, instance string) ([]Site, []string, error) {
	if len(rows) > 1000 {
		return nil, nil, errors.New("Kuma exceeds 1000 monitors")
	}
	byID := map[int64]KumaMonitor{}
	for _, r := range rows {
		if _, found := byID[r.ID]; found {
			return nil, nil, errors.New("duplicate monitor IDs")
		}
		byID[r.ID] = r
	}
	sites, warnings := []Site{}, []string{}
	for _, r := range rows {
		if r.Type != "http" && r.Type != "keyword" && r.Type != "json-query" && r.Type != "real-browser" {
			continue
		}
		if validateURL(r.URL) != nil {
			warnings = append(warnings, fmt.Sprintf("Kuma monitor %d: invalid URL", r.ID))
			continue
		}
		enabled := r.Active
		current := r
		visited := map[int64]bool{}
		valid := true
		for {
			if visited[current.ID] {
				valid = false
				break
			}
			visited[current.ID] = true
			enabled = enabled && current.Active
			if current.Parent == nil || *current.Parent == 0 {
				break
			}
			p, found := byID[*current.Parent]
			if !found {
				valid = false
				break
			}
			current = p
		}
		if !valid {
			warnings = append(warnings, fmt.Sprintf("Kuma monitor %d: invalid parent hierarchy", r.ID))
			continue
		}
		active := 0
		if enabled {
			active = 1
		}
		sites = append(sites, Site{ID: "kuma-" + digest(instance)[:16] + "-" + strconv.FormatInt(r.ID, 10), Name: r.Name, URL: r.URL, Source: "kuma", Enabled: 1, SourceEnabled: active})
	}
	return sites, warnings, nil
}

type KumaImporter struct{ Config KumaConfig }

func (k KumaImporter) Fetch(ctx context.Context) ([]Site, []string, error) {
	c := k.Config
	cfg := mysql.NewConfig()
	cfg.User = c.User
	cfg.Passwd = c.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.DBName = c.Database
	cfg.Timeout = 10 * time.Second
	cfg.ReadTimeout = 10 * time.Second
	cfg.WriteTimeout = 10 * time.Second
	cfg.AllowNativePasswords = true
	cfg.AllowCleartextPasswords = false
	if e := cfg.Apply(mysql.Charset("utf8mb4", "")); e != nil {
		return nil, nil, e
	}
	if c.TLS {
		roots, e := x509.SystemCertPool()
		if e != nil {
			roots = x509.NewCertPool()
		}
		if c.CA != "" {
			data, e := os.ReadFile(c.CA)
			if e != nil {
				return nil, nil, e
			}
			if !roots.AppendCertsFromPEM(data) {
				return nil, nil, errors.New("invalid CA file")
			}
		}
		cfg.TLS = &tls.Config{ServerName: c.Host, RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	connector, e := mysql.NewConnector(cfg)
	if e != nil {
		return nil, nil, e
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, e := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if e != nil {
		return nil, nil, errors.New("Kuma read-only connection failed")
	}
	defer tx.Rollback()
	rows, e := tx.QueryContext(ctx, "SELECT id, name, url, type, active, parent FROM monitor ORDER BY id LIMIT ?", 1001)
	if e != nil {
		return nil, nil, errors.New("Kuma schema or SELECT permissions are incompatible")
	}
	defer rows.Close()
	out := []KumaMonitor{}
	for rows.Next() {
		var r KumaMonitor
		var name, u sql.NullString
		var active int
		var parent sql.NullInt64
		if e = rows.Scan(&r.ID, &name, &u, &r.Type, &active, &parent); e != nil {
			return nil, nil, e
		}
		r.Name = name.String
		r.URL = u.String
		r.Active = active == 1
		if parent.Valid {
			p := parent.Int64
			r.Parent = &p
		}
		out = append(out, r)
	}
	if e = rows.Err(); e != nil {
		return nil, nil, e
	}
	return ParseKuma(out, c.Instance)
}
