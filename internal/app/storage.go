package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: nil, MaxIdleConns: 64, MaxIdleConnsPerHost: 16}}
}
func decodeJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(out)
}
func requestJSON(ctx context.Context, client *http.Client, method, raw string, headers map[string]string, body, out any) (int, error) {
	var b io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return 0, e
		}
		b = bytes.NewReader(data)
	}
	r, e := http.NewRequestWithContext(ctx, method, raw, b)
	if e != nil {
		return 0, e
	}
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("Content-Type", "application/json")
	resp, e := client.Do(r)
	if e != nil {
		return 0, errors.New("service connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("service returned HTTP %d", resp.StatusCode)
	}
	if out != nil {
		return resp.StatusCode, json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out)
	}
	return resp.StatusCode, nil
}

type ClickHouse struct {
	Config CHConfig
	Client *http.Client
}

func NewClickHouse(c CHConfig) *ClickHouse { return &ClickHouse{c, httpClient(30 * time.Second)} }
func (c *ClickHouse) query(ctx context.Context, sql string, params url.Values) ([]byte, error) {
	u, e := url.Parse(c.Config.URL)
	if e != nil {
		return nil, e
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("database", c.Config.Database)
	params.Set("date_time_input_format", "best_effort")
	params.Set("output_format_json_quote_64bit_integers", "0")
	u.RawQuery = params.Encode()
	r, e := http.NewRequestWithContext(ctx, "POST", u.String(), strings.NewReader(sql))
	if e != nil {
		return nil, e
	}
	r.SetBasicAuth(c.Config.User, c.Config.Password)
	resp, e := c.Client.Do(r)
	if e != nil {
		return nil, errors.New("ClickHouse connection failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("ClickHouse returned HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
}
func (c *ClickHouse) Ping(ctx context.Context) error { _, e := c.query(ctx, "SELECT 1", nil); return e }
func (c *ClickHouse) Init(ctx context.Context) error {
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS sites (id String, name String, url String, source String, enabled UInt8, source_enabled UInt8 DEFAULT 1, version UInt64, deleted UInt8 DEFAULT 0) ENGINE=ReplacingMergeTree(version) ORDER BY id`,
		`ALTER TABLE sites ADD COLUMN IF NOT EXISTS source_enabled UInt8 DEFAULT 1`,
		`CREATE TABLE IF NOT EXISTS inventory (at DateTime64(3,'UTC'), payload String) ENGINE=MergeTree ORDER BY at TTL toDateTime(at)+INTERVAL 30 DAY`,
		`CREATE TABLE IF NOT EXISTS checks (at DateTime64(3,'UTC'), site_id String, interface String, status LowCardinality(String), payload String) ENGINE=MergeTree PARTITION BY toYYYYMM(at) ORDER BY(site_id,interface,at) TTL toDateTime(at)+INTERVAL 90 DAY`} {
		if _, e := c.query(ctx, q, nil); e != nil {
			return e
		}
	}
	return nil
}
func (c *ClickHouse) insert(ctx context.Context, table string, rows any) error {
	if table != "sites" && table != "inventory" && table != "checks" {
		return errors.New("unknown table")
	}
	data, e := json.Marshal(rows)
	if e != nil {
		return e
	}
	var list []json.RawMessage
	if e = json.Unmarshal(data, &list); e != nil {
		return e
	}
	if len(list) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("INSERT INTO " + table + " FORMAT JSONEachRow\n")
	for _, row := range list {
		b.Write(row)
		b.WriteByte('\n')
	}
	_, e = c.query(ctx, b.String(), nil)
	return e
}
func (c *ClickHouse) Sites(ctx context.Context) ([]Site, error) {
	data, e := c.query(ctx, "SELECT * FROM sites FINAL WHERE deleted=0 ORDER BY name FORMAT JSONEachRow", nil)
	if e != nil {
		return nil, e
	}
	rows := []Site{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var s Site
		if e = json.Unmarshal(line, &s); e != nil {
			return nil, e
		}
		rows = append(rows, s)
	}
	return rows, nil
}
func (c *ClickHouse) SaveSite(ctx context.Context, s Site) error {
	s.Version = uint64(time.Now().UnixNano())
	return c.insert(ctx, "sites", []Site{s})
}
func (c *ClickHouse) payloads(ctx context.Context, q string, p url.Values) ([]Object, error) {
	data, e := c.query(ctx, q+" FORMAT JSONEachRow", p)
	if e != nil {
		return nil, e
	}
	out := []Object{}
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var row struct {
			Payload string `json:"payload"`
		}
		if e = json.Unmarshal(line, &row); e != nil {
			return nil, e
		}
		var result Object
		if e = decodeJSON([]byte(row.Payload), &result); e != nil {
			return nil, e
		}
		out = append(out, result)
	}
	return out, nil
}
func (c *ClickHouse) Latest(ctx context.Context) ([]Object, error) {
	return c.payloads(ctx, "SELECT argMax(payload,at) AS payload FROM checks GROUP BY site_id,interface", nil)
}
func (c *ClickHouse) History(ctx context.Context, id, iface string, hours int) ([]Object, error) {
	return c.payloads(ctx, `SELECT payload FROM checks WHERE site_id={site:String} AND interface={iface:String} AND at>=now()-toIntervalHour({hours:UInt32}) ORDER BY at DESC LIMIT 1000`, url.Values{"param_site": {id}, "param_iface": {iface}, "param_hours": {fmt.Sprint(hours)}})
}
func (c *ClickHouse) Inventory(ctx context.Context, i Inventory) error {
	b, e := json.Marshal(i)
	if e != nil {
		return e
	}
	return c.insert(ctx, "inventory", []Object{{"at": timestamp(), "payload": string(b)}})
}
func (c *ClickHouse) Checks(ctx context.Context, rows []Object) error {
	out := []Object{}
	for _, r := range rows {
		b, e := json.Marshal(r)
		if e != nil {
			return e
		}
		out = append(out, Object{"at": r["at"], "site_id": r["site_id"], "interface": r["interface"], "status": r["status"], "payload": string(b)})
	}
	return c.insert(ctx, "checks", out)
}

type Vault struct {
	Config BaoConfig
	Client *http.Client
	mu     sync.Mutex
	token  string
	expiry time.Time
}

func NewVault(c BaoConfig) *Vault {
	if c.Mount == "" {
		c.Mount = "secret"
	}
	return &Vault{Config: c, Client: httpClient(15 * time.Second)}
}
func (v *Vault) headers(ctx context.Context) (map[string]string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Now().After(v.expiry) {
		var result struct {
			Auth struct {
				Token string `json:"client_token"`
				TTL   int    `json:"lease_duration"`
			} `json:"auth"`
		}
		_, e := requestJSON(ctx, v.Client, "POST", v.Config.URL+"/v1/auth/approle/login", nil, Object{"role_id": v.Config.RoleID, "secret_id": v.Config.SecretID}, &result)
		if e != nil {
			return nil, e
		}
		v.token = result.Auth.Token
		v.expiry = time.Now().Add(time.Duration(max(1, result.Auth.TTL-30)) * time.Second)
	}
	return map[string]string{"X-Vault-Token": v.token}, nil
}
func (v *Vault) Read(ctx context.Context, path string, out any) (bool, error) {
	h, e := v.headers(ctx)
	if e != nil {
		return false, e
	}
	var result struct {
		Data struct {
			Data json.RawMessage `json:"data"`
		} `json:"data"`
	}
	code, e := requestJSON(ctx, v.Client, "GET", v.Config.URL+"/v1/"+v.Config.Mount+"/data/vlan-analizer/"+path, h, nil, &result)
	if code == 404 {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	return true, decodeJSON(result.Data.Data, out)
}
func (v *Vault) Write(ctx context.Context, path string, value any, cas *int) error {
	h, e := v.headers(ctx)
	if e != nil {
		return e
	}
	body := Object{"data": value}
	if cas != nil {
		body["options"] = Object{"cas": *cas}
	}
	_, e = requestJSON(ctx, v.Client, "POST", v.Config.URL+"/v1/"+v.Config.Mount+"/data/vlan-analizer/"+path, h, body, nil)
	return e
}
