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
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/tart"
)

//go:embed scripts/*.sh scripts/*.swift
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
	display, err := parseDisplay(cfg.Image.Display)
	if err != nil {
		return "", err
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
	// Apply the mode before driving System Events: an unsupported Tart hint
	// can leave WindowServer unable to answer Apple Events at all.
	if err := installDisplay(sshClient, display); err != nil {
		return "", fmt.Errorf("configure guest display %q: %w (guest LaunchAgent logs: ~/Library/Logs/offscreen/display.err and display.log)", cfg.Image.Display, err)
	}
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
	// The golden disk is cloned immediately after stopping Tart. Flush guest
	// filesystem writes so setup scripts and login services survive that clone.
	if _, err := guest.Run(sshClient, "/usr/bin/sync", nil); err != nil {
		return "", fmt.Errorf("flush guest filesystem before sealing image: %w", err)
	}
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

type displaySpec struct {
	width, height int
	unit          string
}

func parseDisplay(value string) (displaySpec, error) {
	if !displayPattern.MatchString(value) {
		return displaySpec{}, fmt.Errorf("invalid image display %q: expected WIDTHxHEIGHT[pt|px]", value)
	}
	unit := "pt"
	dimensions := value
	if strings.HasSuffix(value, "px") || strings.HasSuffix(value, "pt") {
		unit = value[len(value)-2:]
		dimensions = value[:len(value)-2]
	}
	parts := strings.Split(dimensions, "x")
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width > int(^uint(0)>>1)/2 || height > int(^uint(0)>>1)/2 {
		return displaySpec{}, fmt.Errorf("invalid image display %q: dimensions exceed supported range", value)
	}
	return displaySpec{width: width, height: height, unit: unit}, nil
}

func installDisplay(client *ssh.Client, display displaySpec) error {
	source, err := scripts.ReadFile("scripts/display.swift")
	if err != nil {
		return err
	}
	command := `/bin/mkdir -p "$HOME/Library/Application Support/offscreen" && /bin/cat > "$HOME/Library/Application Support/offscreen/display.swift"`
	if _, err := guest.Run(client, command, strings.NewReader(string(source))); err != nil {
		return fmt.Errorf("copy display selector: %w", err)
	}
	installer, err := scripts.ReadFile("scripts/display.sh")
	if err != nil {
		return err
	}
	args := fmt.Sprintf("/bin/bash -e -s -- %d %d %s", display.width, display.height, display.unit)
	if _, err := guest.Run(client, args, strings.NewReader(string(installer))); err != nil {
		return fmt.Errorf("install, apply and verify display: %w", err)
	}
	return nil
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
