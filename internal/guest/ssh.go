// Package guest provides SSH transport into a disposable Tart macOS guest.
package guest

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

type Credentials struct {
	User, Password, Key string
}

func (c Credentials) dialConfig() (*ssh.ClientConfig, error) {
	methods := []ssh.AuthMethod{}
	if c.Key != "" {
		key := c.Key
		if key == "~" || strings.HasPrefix(key, "~/") {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			key = filepath.Join(home, strings.TrimPrefix(key, "~/"))
		}
		contents, err := os.ReadFile(key)
		if err != nil {
			return nil, fmt.Errorf("read SSH key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(contents)
		if err != nil {
			return nil, fmt.Errorf("parse SSH key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if c.Password != "" {
		methods = append(methods, ssh.Password(c.Password))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("configure image ssh_password or ssh_key")
	}
	// Ephemeral cloned VMs generate host keys; their address can be reused by
	// another clone. Only connect to Tart's private guest IP, never a remote host.
	return &ssh.ClientConfig{User: c.User, Auth: methods, HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second}, nil // #nosec G106 -- ephemeral guest on Tart's private network
}

func (c Credentials) Dial(ctx context.Context, ip string) (*ssh.Client, error) {
	if net.ParseIP(ip) == nil {
		return nil, fmt.Errorf("invalid guest IP %q", ip)
	}
	cfg, err := c.dialConfig()
	if err != nil {
		return nil, err
	}
	return dial(ctx, net.JoinHostPort(ip, "22"), cfg)
}

func dial(ctx context.Context, address string, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, address, cfg)
	if err != nil {
		stopCancel()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	// A cancellation racing the handshake must not return a closed client.
	if !stopCancel() && ctx.Err() != nil {
		_ = clientConn.Close()
		return nil, ctx.Err()
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		_ = clientConn.Close()
		return nil, err
	}
	client := ssh.NewClient(clientConn, chans, reqs)
	conn = nil // The returned client now owns the connection.
	return client, nil
}

// Run executes shell text with optional stdin, preserving guest stdout/stderr
// for diagnostics. Scripts are streamed from the host, not interpreted there.
func Run(client *ssh.Client, script string, input io.Reader) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("open guest session: %w", err)
	}
	defer func() { _ = session.Close() }()
	session.Stdin = input
	out, err := session.CombinedOutput("/bin/bash -e -o pipefail -c " + Quote(script))
	if err != nil {
		return string(out), fmt.Errorf("guest command: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Quote shell-quotes an arbitrary argument without interpolation.
func Quote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
