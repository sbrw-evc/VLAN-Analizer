package hostnet

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Host runs the host's own tools and edits the host's files. The agent container shares the host PID
// namespace, so commands enter PID 1's namespaces and files are reached through /proc/1/root.
type Host interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte, perm os.FileMode) error
	Remove(path string) error
	Glob(pattern string) ([]string, error)
}

// CommandError keeps the host tool's diagnostic output for the operator.
type CommandError struct {
	Command string
	Output  string
}

func (e *CommandError) Error() string {
	if e.Output == "" {
		return e.Command + " failed"
	}
	return e.Command + " failed: " + e.Output
}

// NamespaceHost implements Host for a privileged container with `pid: host`. PID is the host process whose
// mount and network namespaces the agent uses: 1 (the host's init) unless overridden.
type NamespaceHost struct {
	Root string
	PID  int
}

func NewNamespaceHost(pid int) NamespaceHost {
	if pid < 1 {
		pid = 1
	}
	return NamespaceHost{Root: fmt.Sprintf("/proc/%d/root", pid), PID: pid}
}

func (h NamespaceHost) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	full := append([]string{"--target", strconv.Itoa(h.PID), "--mount", "--net", "--", name}, args...)
	cmd := exec.CommandContext(ctx, "nsenter", full...)
	cmd.Env = []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C", "LC_ALL=C"}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, e := cmd.Output()
	if e != nil {
		return out, &CommandError{Command: name, Output: clip(strings.TrimSpace(stderr.String() + " " + string(out)))}
	}
	return out, nil
}

func (h NamespaceHost) path(p string) (string, error) {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return "", errors.New("host paths must be absolute and clean")
	}
	return filepath.Join(h.Root, p), nil
}

func (h NamespaceHost) ReadFile(p string) ([]byte, error) {
	full, e := h.path(p)
	if e != nil {
		return nil, e
	}
	return os.ReadFile(full)
}

// WriteFile replaces the file atomically and restores its SELinux label where the host uses SELinux.
func (h NamespaceHost) WriteFile(p string, data []byte, perm os.FileMode) error {
	full, e := h.path(p)
	if e != nil {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(full), 0755); e != nil {
		return e
	}
	tmp, e := os.CreateTemp(filepath.Dir(full), ".vlan-analizer-*")
	if e != nil {
		return e
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(data); e == nil {
		e = tmp.Chmod(perm)
	}
	if e == nil {
		e = tmp.Sync()
	}
	if closeErr := tmp.Close(); e == nil {
		e = closeErr
	}
	if e == nil {
		e = os.Rename(tmp.Name(), full)
	}
	if e == nil {
		if _, err := os.Stat(filepath.Join(h.Root, "/usr/sbin/restorecon")); err == nil {
			_, _ = h.Run(context.Background(), "restorecon", p)
		}
	}
	return e
}

func (h NamespaceHost) Remove(p string) error {
	full, e := h.path(p)
	if e != nil {
		return e
	}
	if e = os.Remove(full); e != nil && !os.IsNotExist(e) {
		return e
	}
	return nil
}

func (h NamespaceHost) Glob(pattern string) ([]string, error) {
	full, e := h.path(pattern)
	if e != nil {
		return nil, e
	}
	matches, e := filepath.Glob(full)
	for i, m := range matches {
		matches[i] = strings.TrimPrefix(m, h.Root)
	}
	return matches, e
}

func clip(s string) string {
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

func exists(h Host, p string) bool {
	_, e := h.ReadFile(p)
	return e == nil
}

// hasCommand checks the host for an executable without depending on `which` being installed.
func hasCommand(h Host, name string) bool {
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin", "/usr/local/sbin", "/usr/local/bin"} {
		if matches, _ := h.Glob(dir + "/" + name); len(matches) > 0 {
			return true
		}
	}
	return false
}

func run(ctx context.Context, h Host, name string, args ...string) error {
	_, e := h.Run(ctx, name, args...)
	return e
}
