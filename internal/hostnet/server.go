package hostnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// DefaultSocket is shared with the application container through a volume; nothing else can reach it.
const DefaultSocket = "/run/vlan-agent/agent.sock"

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func replyError(w http.ResponseWriter, e error) {
	var validation ValidationError
	var conflict ConflictError
	var command *CommandError
	switch {
	case errors.As(e, &validation):
		reply(w, 422, map[string]string{"detail": validation.Message})
	case errors.As(e, &conflict):
		reply(w, 409, map[string]string{"detail": conflict.Message})
	case errors.Is(e, ErrNotFound):
		reply(w, 404, map[string]string{"detail": e.Error()})
	case errors.As(e, &command):
		reply(w, 502, map[string]string{"detail": "host network configuration failed: " + command.Error()})
	default:
		reply(w, 502, map[string]string{"detail": "host network configuration failed: " + e.Error()})
	}
}

func decode(r *http.Request, w http.ResponseWriter, out any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return ValidationError{"invalid VLAN interface fields"}
	}
	return nil
}

// Handler exposes the manager on the agent socket.
func Handler(m *Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, m.Status(r.Context())) })
	mux.HandleFunc("GET /v1/vlans", func(w http.ResponseWriter, r *http.Request) {
		list, e := m.List(r.Context())
		if e != nil {
			replyError(w, e)
			return
		}
		reply(w, 200, list)
	})
	mux.HandleFunc("POST /v1/vlans", func(w http.ResponseWriter, r *http.Request) {
		var s Spec
		if e := decode(r, w, &s); e != nil {
			replyError(w, e)
			return
		}
		item, e := m.Create(r.Context(), s)
		if e != nil {
			replyError(w, e)
			return
		}
		reply(w, 201, item)
	})
	mux.HandleFunc("PUT /v1/vlans/{name}", func(w http.ResponseWriter, r *http.Request) {
		var s Spec
		if e := decode(r, w, &s); e != nil {
			replyError(w, e)
			return
		}
		item, e := m.Update(r.Context(), r.PathValue("name"), s)
		if e != nil {
			replyError(w, e)
			return
		}
		reply(w, 200, item)
	})
	mux.HandleFunc("DELETE /v1/vlans/{name}", func(w http.ResponseWriter, r *http.Request) {
		if e := m.Delete(r.Context(), r.PathValue("name")); e != nil {
			replyError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("POST /v1/vlans/{name}/{state}", func(w http.ResponseWriter, r *http.Request) {
		state := r.PathValue("state")
		if state != "up" && state != "down" {
			reply(w, 404, map[string]string{"detail": "unknown action"})
			return
		}
		if e := m.SetState(r.Context(), r.PathValue("name"), state == "up"); e != nil {
			replyError(w, e)
			return
		}
		reply(w, 200, map[string]bool{"ok": true})
	})
	return mux
}

// Serve runs the agent on a Unix socket until ctx ends.
func Serve(ctx context.Context, socket string, m *Manager) error {
	if m.Status(ctx).Error == "" {
		for _, e := range m.Restore(ctx) {
			log.Printf("restore: %v", e)
		}
	}
	if e := os.MkdirAll(filepath.Dir(socket), 0700); e != nil {
		return e
	}
	_ = os.Remove(socket)
	l, e := net.Listen("unix", socket)
	if e != nil {
		return e
	}
	if e = os.Chmod(socket, 0600); e != nil {
		l.Close()
		return e
	}
	server := &http.Server{Handler: Handler(m), ReadHeaderTimeout: 10 * time.Second, WriteTimeout: 5 * time.Minute}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if env := m.Status(ctx); env.Error != "" {
		log.Printf("host network agent on %s: %s", socket, env.Error)
	} else {
		log.Printf("host network agent on %s: %s, backend %s", socket, env.Distro.Name, env.Backend)
	}
	if e = server.Serve(l); e != nil && !errors.Is(e, http.ErrServerClosed) {
		return e
	}
	return nil
}

// Client calls the agent over its Unix socket.
type Client struct {
	Socket string
	http   *http.Client
}

func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}
	return &Client{Socket: socket, http: &http.Client{Transport: transport, Timeout: 5 * time.Minute}}
}

// AgentError carries the agent's status and message.
type AgentError struct {
	Status  int
	Message string
}

func (e *AgentError) Error() string { return e.Message }

// ErrUnavailable means the agent container is not running.
var ErrUnavailable = errors.New("the host network agent is not running")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		data, e := json.Marshal(body)
		if e != nil {
			return e
		}
		reader = bytes.NewReader(data)
	}
	req, e := http.NewRequestWithContext(ctx, method, "http://agent"+path, reader)
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Detail == "" {
			problem.Detail = fmt.Sprintf("host network agent answered %d", resp.StatusCode)
		}
		return &AgentError{Status: resp.StatusCode, Message: problem.Detail}
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Status(ctx context.Context) (Environment, error) {
	var env Environment
	return env, c.do(ctx, "GET", "/v1/status", nil, &env)
}

func (c *Client) List(ctx context.Context) (Listing, error) {
	var l Listing
	return l, c.do(ctx, "GET", "/v1/vlans", nil, &l)
}

func (c *Client) Create(ctx context.Context, s Spec) (Managed, error) {
	var m Managed
	return m, c.do(ctx, "POST", "/v1/vlans", s, &m)
}

func (c *Client) Update(ctx context.Context, name string, s Spec) (Managed, error) {
	var m Managed
	return m, c.do(ctx, "PUT", "/v1/vlans/"+url.PathEscape(name), s, &m)
}

func (c *Client) Delete(ctx context.Context, name string) error {
	return c.do(ctx, "DELETE", "/v1/vlans/"+url.PathEscape(name), nil, nil)
}

func (c *Client) SetState(ctx context.Context, name string, up bool) error {
	state := "down"
	if up {
		state = "up"
	}
	return c.do(ctx, "POST", "/v1/vlans/"+url.PathEscape(name)+"/"+state, nil, nil)
}
