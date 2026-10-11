package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/tart"
)

func projectKey(root string) string {
	hash := sha256.Sum256([]byte(root))
	return hex.EncodeToString(hash[:])
}

func waitGuest(ctx context.Context, vm tart.Client, creds guest.Credentials, name string) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var last error
	for ctx.Err() == nil {
		ip, err := vm.IP(ctx, name)
		if err == nil && ip != "" {
			client, dialErr := creds.Dial(ctx, ip)
			if dialErr == nil {
				return client, nil
			}
			last = dialErr
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("guest SSH not ready: %w (last attempt: %w)", ctx.Err(), last)
}

func waitGUI(ctx context.Context, client *ssh.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var last error
	for ctx.Err() == nil {
		// System Events answering from the SSH user proves that auto-login,
		// WindowServer and the in-image display helper have initialized.
		last = guest.Stream(ctx, client, `/usr/bin/osascript -e 'tell application "System Events" to get name of first process whose frontmost is true'`, nil, io.Discard, io.Discard, false)
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	return fmt.Errorf("%w (last attempt: %w)", ctx.Err(), last)
}

// Tar streams directly from the host filesystem, never from Tart's shared
// directory: VirtioFS shares can omit framework symlinks.
func sendWorktree(ctx context.Context, client *ssh.Client, root, destination string) error {
	return sendTarArgs(ctx, client, root, destination, "--exclude=.build", "--exclude=.git", ".")
}

func sendTar(ctx context.Context, client *ssh.Client, parent, name, destination string) error {
	if _, err := os.Lstat(filepath.Join(parent, name)); err != nil {
		return err
	}
	return sendTarArgs(ctx, client, parent, destination, name)
}

func sendTarArgs(ctx context.Context, client *ssh.Client, parent, destination string, args ...string) error {
	cmd := exec.CommandContext(ctx, "tar", append([]string{"-C", parent, "-cf", "-"}, args...)...) // #nosec G204 -- fixed tar binary and runner-generated arguments, no shell
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	if err := cmd.Start(); err != nil {
		return err
	}
	remoteErr := guest.Stream(ctx, client, "mkdir -p "+guest.Quote(destination)+" && tar -xf - -C "+guest.Quote(destination), pipe, io.Discard, io.Discard, false)
	if remoteErr != nil {
		_ = cmd.Process.Kill()
	}
	localErr := cmd.Wait()
	if remoteErr != nil {
		return remoteErr
	}
	if localErr != nil {
		return fmt.Errorf("host tar: %w: %s", localErr, diagnostics.String())
	}
	return nil
}

func receiveTar(ctx context.Context, client *ssh.Client, source, destination string) error {
	reader, writer := io.Pipe()
	remote := make(chan error, 1)
	go func() {
		err := guest.Stream(ctx, client, "tar -cf - -C "+guest.Quote(source)+" .", nil, writer, io.Discard, false)
		_ = writer.CloseWithError(err)
		remote <- err
	}()
	cmd := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", destination)
	cmd.Stdin = reader
	var diagnostics strings.Builder
	cmd.Stderr = &diagnostics
	localErr := cmd.Run()
	_ = reader.Close()
	remoteErr := <-remote
	if remoteErr != nil {
		return remoteErr
	}
	if localErr != nil {
		return fmt.Errorf("extract artifacts: %w: %s", localErr, diagnostics.String())
	}
	return nil
}
