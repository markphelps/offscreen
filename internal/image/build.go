// Package image builds a reproducible golden image from a Tart base.
package image

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/tart"
)

//go:embed scripts/*.sh
var scripts embed.FS

var (
	displayPattern = regexp.MustCompile(`^[1-9][0-9]*x[1-9][0-9]*(pt|px)?$`)
	versionPattern = regexp.MustCompile(`(?m)^Xcode ([0-9]+(?:\.[0-9]+)*)$`)
)

// Build creates a new image and records it only after all steps succeed.
// Refresh unconditionally pulls the base; build pulls only when it is absent.
func Build(ctx context.Context, path string, refresh bool) (string, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return "", err
	}
	if !displayPattern.MatchString(cfg.Image.Display) {
		return "", fmt.Errorf("invalid image display %q: expected WIDTHxHEIGHT[pt|px]", cfg.Image.Display)
	}
	if cfg.Image.Base == "" {
		return "", fmt.Errorf("image base must not be empty")
	}
	if cfg.Image.SSHUser == "" {
		return "", fmt.Errorf("image ssh_user must not be empty")
	}
	creds := guest.Credentials{User: cfg.Image.SSHUser, Password: cfg.Image.SSHPassword, Key: cfg.Image.SSHKey}
	client := tart.Client{}
	if refresh {
		if err := client.Pull(ctx, cfg.Image.Base); err != nil {
			return "", err
		}
	} else {
		vms, err := client.List(ctx)
		if err != nil {
			return "", err
		}
		found := false
		for _, vm := range vms {
			if vm.Name == cfg.Image.Base {
				found = true
				break
			}
		}
		if !found {
			if err := client.Pull(ctx, cfg.Image.Base); err != nil {
				return "", err
			}
		}
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("generate image ID: %w", err)
	}
	suffix := hex.EncodeToString(id)
	working := "offscreen-build-" + suffix
	if err := client.Clone(ctx, cfg.Image.Base, working); err != nil {
		return "", err
	}
	owned := working
	defer func() {
		if owned != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			_ = client.Stop(cleanup, owned)
			_ = client.Delete(cleanup, owned)
		}
	}()
	if err := client.SetDisplay(ctx, working, cfg.Image.Display); err != nil {
		return "", err
	}
	bootCtx, cancelBoot := context.WithCancel(ctx)
	vm, err := client.Start(bootCtx, working)
	if err != nil {
		cancelBoot()
		return "", err
	}
	// Reap the tart run process before the owned VM is deleted on every exit.
	// The same wait is used after a successful stop, so Wait is never called twice.
	waitVM := sync.OnceValue(vm.Wait)
	defer func() {
		cancelBoot()
		_ = waitVM()
	}()
	sshClient, err := waitForGuest(ctx, client, creds, working)
	if err != nil {
		return "", err
	}
	defer func() { _ = sshClient.Close() }()
	check, err := scripts.ReadFile("scripts/check.sh")
	if err != nil {
		return "", err
	}
	if _, err := guest.Run(sshClient, "/bin/bash -e -s -- "+guest.Quote(cfg.Image.SSHUser), strings.NewReader(string(check))); err != nil {
		return "", fmt.Errorf("confirm auto-login and GUI: %w", err)
	}
	automation, err := scripts.ReadFile("scripts/automation.sh")
	if err != nil {
		return "", err
	}
	checkAutomation := func() error {
		_, err := guest.Run(sshClient, "/bin/bash -e -s", strings.NewReader(string(automation)))
		return err
	}
	if err := checkAutomation(); err != nil {
		command := "sudo -n /usr/bin/automationmodetool enable-automationmode-without-authentication"
		var passwordInput io.Reader
		if cfg.Image.SSHPassword != "" {
			command = "sudo -S -p '' /usr/bin/automationmodetool enable-automationmode-without-authentication"
			passwordInput = strings.NewReader(cfg.Image.SSHPassword + "\n")
		}
		if _, err := guest.Run(sshClient, command, passwordInput); err != nil {
			return "", fmt.Errorf("enable Xcode automation without authentication: %w", err)
		}
		if err := checkAutomation(); err != nil {
			return "", fmt.Errorf("verify Xcode automation without authentication: %w", err)
		}
	}
	setupWrapper, err := scripts.ReadFile("scripts/setup.sh")
	if err != nil {
		return "", err
	}
	for _, path := range cfg.Image.Setup {
		expanded, err := expandHome(path)
		if err != nil {
			return "", err
		}
		file, err := os.Open(expanded)
		if err != nil {
			return "", fmt.Errorf("open image setup script %q: %w", path, err)
		}
		_, runErr := guest.Run(sshClient, "/bin/bash -e -o pipefail -c "+guest.Quote(string(setupWrapper)), file)
		_ = file.Close()
		if runErr != nil {
			return "", fmt.Errorf("image setup script %q: %w", path, runErr)
		}
	}
	versionOutput, err := guest.Run(sshClient, "xcodebuild -version", nil)
	if err != nil {
		return "", fmt.Errorf("read guest Xcode version: %w", err)
	}
	match := versionPattern.FindStringSubmatch(versionOutput)
	if match == nil {
		return "", fmt.Errorf("unrecognized guest Xcode version: %q", versionOutput)
	}
	name := fmt.Sprintf("offscreen-%s-xcode%s-%s", time.Now().Format("20060102"), match[1], suffix)
	_ = sshClient.Close()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelStop()
	if err := client.Stop(stopCtx, working); err != nil {
		return "", err
	}
	if err := waitVM(); err != nil {
		return "", fmt.Errorf("VM exited: %w", err)
	}
	if err := client.Rename(ctx, working, name); err != nil {
		return "", err
	}
	owned = name
	if err := config.SetCurrentImageFile(path, name); err != nil {
		return "", err
	}
	owned = ""
	return name, nil
}

func waitForGuest(ctx context.Context, client tart.Client, creds guest.Credentials, name string) (*ssh.Client, error) {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var last error
	for deadline.Err() == nil {
		ip, err := client.IP(deadline, name)
		if err == nil && ip != "" {
			connection, dialErr := creds.Dial(deadline, ip)
			if dialErr == nil {
				return connection, nil
			}
			last = dialErr
		} else {
			last = err
		}
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf("guest SSH not ready: %w (last attempt: %w)", deadline.Err(), last)
		case <-time.After(3 * time.Second):
		}
	}
	return nil, fmt.Errorf("guest SSH not ready: %w (last attempt: %w)", deadline.Err(), last)
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
