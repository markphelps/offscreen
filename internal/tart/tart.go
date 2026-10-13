// Package tart wraps the Tart CLI. Names passed to destructive operations must be
// owned by the caller; this package never sweeps VMs by a name prefix.
package tart

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// errorLines is how many trailing output lines a failed command's error keeps.
const errorLines = 20

type VM struct {
	Name    string `json:"Name"`
	Running bool   `json:"Running"`
	// Size is the disk space the VM uses on the host, in gigabytes.
	Size int `json:"Size"`
}

type Client struct {
	Binary string
}

func (c Client) command(ctx context.Context, args ...string) ([]byte, error) {
	binary := c.Binary
	if binary == "" {
		binary = "tart"
	}
	out, err := exec.CommandContext(ctx, binary, args...).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("tart %s: %w: %s", strings.Join(args, " "), err, lastLines(string(out), errorLines))
	}
	return out, nil
}

// lastLines returns at most n trailing lines of s, trimmed of surrounding
// space, so an error never carries a command's full output.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (c Client) List(ctx context.Context) ([]VM, error) {
	out, err := c.command(ctx, "list", "--format", "json")
	if err != nil {
		return nil, err
	}
	var vms []VM
	if err := json.Unmarshal(out, &vms); err != nil {
		return nil, fmt.Errorf("decode tart list: %w", err)
	}
	return vms, nil
}

// Pull downloads name, copying Tart's stdout and stderr to out (which may be
// nil) as they arrive. Tart has no supported progress interface, so callers
// must not parse or display this output; it is for logs and error tails.
// CI=1 makes Tart print one line per update instead of redrawing with
// escape codes. The error does not include the output.
func (c Client) Pull(ctx context.Context, name string, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	binary := c.Binary
	if binary == "" {
		binary = "tart"
	}
	cmd := exec.CommandContext(ctx, binary, "pull", name)
	cmd.Env = append(os.Environ(), "CI=1")
	// One writer for both streams, so exec serializes the writes.
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tart pull %s: %w", name, err)
	}
	return nil
}

func (c Client) Clone(ctx context.Context, from, to string) error {
	_, err := c.command(ctx, "clone", from, to)
	return err
}

func (c Client) SetDisplay(ctx context.Context, name, display string) error {
	_, err := c.command(ctx, "set", name, "--display", display)
	return err
}

func (c Client) IP(ctx context.Context, name string) (string, error) {
	out, err := c.command(ctx, "ip", name, "--wait", "10")
	return strings.TrimSpace(string(out)), err
}

func (c Client) Rename(ctx context.Context, from, to string) error {
	_, err := c.command(ctx, "rename", from, to)
	return err
}

func (c Client) Stop(ctx context.Context, name string) error {
	_, err := c.command(ctx, "stop", name)
	return err
}

func (c Client) Delete(ctx context.Context, name string) error {
	_, err := c.command(ctx, "delete", name)
	return err
}

// Start returns immediately while Tart runs; Wait must be called before deleting.
func (c Client) Start(ctx context.Context, name string) (*exec.Cmd, error) {
	return c.StartShared(ctx, name, nil, false)
}

// StartShared boots a VM with named Tart directory shares. Each entry is a
// complete --dir=<name>:<path>[:ro] argument owned by the caller. The VM is
// headless unless show is set, which opens Tart's window on the host desktop.
func (c Client) StartShared(ctx context.Context, name string, shares []string, show bool) (*exec.Cmd, error) {
	binary := c.Binary
	if binary == "" {
		binary = "tart"
	}
	args := []string{"run"}
	if !show {
		args = append(args, "--no-graphics")
	}
	args = append(args, name)
	for _, share := range shares {
		args = append(args, "--dir="+share)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tart VM %q: %w", name, err)
	}
	return cmd, nil
}
