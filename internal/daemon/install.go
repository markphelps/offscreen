package daemon

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	Label      = "dev.offscreen.offscreend"
	bundleName = "OffscreenDaemon.app"
	lsregister = "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister"
)

type commandRunner func(string, ...string) ([]byte, error)

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func xmlString(v string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

func infoPlist() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + Label + `</string>
<key>CFBundleExecutable</key><string>offscreend</string>
<key>CFBundleName</key><string>Offscreen Daemon</string>
<key>CFBundleDisplayName</key><string>Offscreen Daemon</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>CFBundleVersion</key><string>1</string>
<key>CFBundleShortVersionString</key><string>1.0</string>
<key>LSUIElement</key><true/>
<key>NSLocalNetworkUsageDescription</key><string>Offscreen Daemon connects to local Tart virtual machines to run GUI jobs and collect their artifacts.</string>
</dict></plist>`)
}

func agentPlist(dir, bundle string, env map[string]string) []byte {
	var variables strings.Builder
	for _, key := range []string{"PATH", "OFFSCREEN_CONFIG", "OFFSCREEN_STATE_DIR"} {
		if value := env[key]; value != "" {
			variables.WriteString("<key>" + key + "</key><string>" + xmlString(value) + "</string>")
		}
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + Label + `</string><key>ProgramArguments</key><array><string>` + xmlString(filepath.Join(bundle, "Contents", "MacOS", "offscreend")) + `</string></array><key>AssociatedBundleIdentifiers</key><array><string>` + Label + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>EnvironmentVariables</key><dict>` + variables.String() + `</dict><key>StandardErrorPath</key><string>` + xmlString(filepath.Join(dir, "daemon.log")) + `</string></dict></plist>`)
}

// plistValue reads only top-level string values in a plist dict, not nested values.
func plistValue(data []byte, key string) (string, error) {
	decoder := xml.NewDecoder(strings.NewReader(string(data)))
	depth := 0
	wanted := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			return "", fmt.Errorf("missing %s", key)
		}
		if err != nil {
			return "", err
		}
		switch element := token.(type) {
		case xml.StartElement:
			if depth == 2 && element.Name.Local == "key" {
				var name string
				if err := decoder.DecodeElement(&name, &element); err != nil {
					return "", err
				}
				wanted = name == key
				continue
			}
			if depth == 2 && wanted {
				if element.Name.Local != "string" {
					return "", fmt.Errorf("%s is not a string", key)
				}
				var value string
				if err := decoder.DecodeElement(&value, &element); err != nil {
					return "", err
				}
				return value, nil
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

func ownedPlist(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	label, err := plistValue(data, "Label")
	if err != nil || label != Label {
		return nil, fmt.Errorf("refusing to replace LaunchAgent not owned by Offscreen: %s", path)
	}
	return data, nil
}

func ownedBundle(path string) (bool, error) {
	_, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(filepath.Join(path, "Contents", "Info.plist"))
	if err != nil {
		return false, fmt.Errorf("refusing to replace unrecognized bundle %s: %w", path, err)
	}
	identifier, err := plistValue(data, "CFBundleIdentifier")
	if err != nil || identifier != Label {
		return false, fmt.Errorf("refusing to replace bundle not owned by Offscreen: %s", path)
	}
	return true, nil
}

func runCommand(run commandRunner, name string, args ...string) error {
	output, err := run(name, args...)
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// A missing *service* is different from an inaccessible or missing domain.
// Inspect the named label, never infer its state from bootout's exit code:
// bootout by plist can return EIO even for a valid, stopped installation.
func serviceLoaded(run commandRunner, domain string) (bool, error) {
	target := domain + "/" + Label
	output, err := run("launchctl", "print", target)
	if err == nil {
		return true, nil
	}
	message := strings.TrimSpace(string(output))
	missing := strings.TrimSpace(strings.TrimPrefix(message, "Bad request."))
	if strings.HasPrefix(missing, `Could not find service "`+Label+`" in domain `) {
		return false, nil
	}
	return false, fmt.Errorf("launchctl print %s: %w: %s", target, err, message)
}

const installWait = 15 * time.Second

func waitServiceGone(ctx context.Context, run commandRunner, domain string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		loaded, err := serviceLoaded(run, domain)
		if err != nil {
			return fmt.Errorf("inspect stopped daemon: %w", err)
		}
		if !loaded {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon label still loaded: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// The old label can disappear before its process closes the Unix listener.
// Do not mistake its still-serving API for the newly bootstrapped daemon.
func waitOldSocketGone(ctx context.Context, dir string) error {
	socket := filepath.Join(dir, "offscreend.sock")
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(socket); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return fmt.Errorf("inspect previous daemon socket: %w", err)
		}
		conn, err := (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext(ctx, "unix", socket)
		switch {
		case err == nil:
			_ = conn.Close()
		case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ECONNREFUSED):
			return nil
		case ctx.Err() != nil:
			return fmt.Errorf("previous daemon socket still accepting connections: %w", ctx.Err())
		default:
			return fmt.Errorf("inspect previous daemon socket: %w", err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("previous daemon socket still accepting connections: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// A successful HTTP response proves the daemon's API is accepting requests;
// launchctl bootstrap and a socket pathname alone do not establish readiness.
func waitDaemonReady(ctx context.Context, ready func(context.Context) error) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if err := ready(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon did not become ready: %w (last attempt: %w)", ctx.Err(), last)
		case <-ticker.C:
		}
	}
}

func stageBundle(stage, source string, run commandRunner) error {
	macos := filepath.Join(stage, "Contents", "MacOS")
	if err := os.MkdirAll(macos, 0o700); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(filepath.Join(macos, "offscreend"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700) // #nosec G302 -- launchd must execute the owner-only daemon binary
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "Contents", "Info.plist"), infoPlist(), 0o600); err != nil {
		return err
	}
	if err := runCommand(run, "/usr/bin/codesign", "--force", "--sign", "-", "--identifier", Label, "-r", `=designated => identifier "`+Label+`"`, stage); err != nil {
		return fmt.Errorf("sign daemon bundle: %w", err)
	}
	if err := runCommand(run, "/usr/bin/codesign", "--verify", "--strict", stage); err != nil {
		return fmt.Errorf("verify daemon bundle: %w", err)
	}
	if err := runCommand(run, lsregister, "-f", stage); err != nil {
		return fmt.Errorf("register daemon bundle: %w", err)
	}
	return nil
}

func writePlist(path string, contents []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".offscreend-plist-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(contents); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(temp.Name(), path)
}

// Install stages and verifies a signed app before stopping the existing user
// service. Rollback restores its app and plist if publication or bootstrap fails.
func Install() error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	path, err := plistPath()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	source := filepath.Join(filepath.Dir(exe), "offscreend")
	if _, err := os.Stat(source); err != nil {
		source, err = exec.LookPath("offscreend")
		if err != nil {
			return fmt.Errorf("build offscreend beside offscreen or add it to PATH: %w", err)
		}
	}
	env := map[string]string{}
	for _, key := range []string{"PATH", "OFFSCREEN_CONFIG", "OFFSCREEN_STATE_DIR"} {
		env[key] = os.Getenv(key)
	}
	return install(dir, path, source, fmt.Sprintf("gui/%d", os.Getuid()), env, func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- command names are fixed installer tools, arguments are file paths and fixed flags
	}, func(ctx context.Context) error {
		client, err := Dial()
		if err != nil {
			return err
		}
		_, err = client.Jobs(ctx)
		return err
	})
}

func install(dir, path, source, domain string, env map[string]string, run commandRunner, ready func(context.Context) error) error {
	return installWithWait(dir, path, source, domain, env, run, ready, installWait)
}

func installWithWait(dir, path, source, domain string, env map[string]string, run commandRunner, ready func(context.Context) error, wait time.Duration) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	oldPlist, err := ownedPlist(path)
	if err != nil {
		return err
	}
	bundle := filepath.Join(dir, bundleName)
	hadBundle, err := ownedBundle(bundle)
	if err != nil {
		return err
	}
	loadedOld := false
	if oldPlist != nil {
		loadedOld, err = serviceLoaded(run, domain)
		if err != nil {
			return fmt.Errorf("inspect existing daemon: %w", err)
		}
	}
	if !loadedOld {
		// A foreground daemon has no launchd label but can still answer /jobs.
		// Refuse before staging or publishing rather than stopping that process
		// or accepting its response as readiness from the new service.
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		err := waitOldSocketGone(ctx, dir)
		cancel()
		if err != nil {
			return fmt.Errorf("daemon socket busy without a loaded LaunchAgent; stop the foreground daemon before installing: %w", err)
		}
	}
	staging, err := os.MkdirTemp(dir, ".offscreend-stage-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	candidate := filepath.Join(staging, bundleName)
	restoreRegistration := func(cause error) error {
		if hadBundle {
			if err := runCommand(run, lsregister, "-f", bundle); err != nil {
				return errors.Join(cause, fmt.Errorf("restore previous bundle registration: %w", err))
			}
		}
		return cause
	}
	if err := stageBundle(candidate, source, run); err != nil {
		return restoreRegistration(err)
	}
	backup, err := os.MkdirTemp(dir, ".offscreend-backup-*")
	if err != nil {
		return restoreRegistration(err)
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(backup)
		}
	}()
	oldBundle := filepath.Join(backup, bundleName)
	stoppedOld := false
	if loadedOld {
		if err := runCommand(run, "launchctl", "bootout", domain+"/"+Label); err != nil {
			return restoreRegistration(fmt.Errorf("stop existing daemon: %w", err))
		}
		stoppedOld = true
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		err := waitServiceGone(ctx, run, domain)
		if err == nil {
			err = waitOldSocketGone(ctx, dir)
		}
		cancel()
		if err != nil {
			cause := fmt.Errorf("wait for previous daemon to stop: %w", err)
			if restartErr := runCommand(run, "launchctl", "bootstrap", domain, path); restartErr != nil {
				cause = errors.Join(cause, fmt.Errorf("restart previous daemon: %w", restartErr))
			}
			return restoreRegistration(cause)
		}
	}
	published := false
	movedOld := false
	rollback := func(cause error) error {
		var failures []error
		// A failed bootstrap can still have loaded a service. Inspect this
		// label before stopping it; a missing service is benign, other errors
		// must remain visible to the caller.
		loaded, err := serviceLoaded(run, domain)
		if err != nil {
			failures = append(failures, fmt.Errorf("inspect failed replacement: %w", err))
		} else {
			if loaded {
				if err := runCommand(run, "launchctl", "bootout", domain+"/"+Label); err != nil {
					failures = append(failures, fmt.Errorf("stop failed replacement: %w", err))
				}
			}
			if len(failures) == 0 {
				ctx, cancel := context.WithTimeout(context.Background(), wait)
				err := waitServiceGone(ctx, run, domain)
				if err == nil {
					err = waitOldSocketGone(ctx, dir)
				}
				cancel()
				if err != nil {
					failures = append(failures, fmt.Errorf("wait for failed replacement to stop: %w", err))
				}
			}
		}
		if len(failures) != 0 {
			// A service that might still be executing the new bundle cannot
			// safely have its executable removed. Retain the previous bundle.
			keepBackup = movedOld
			return errors.Join(append([]error{cause, fmt.Errorf("replacement may still be running; previous bundle retained at %s", oldBundle)}, failures...)...)
		}
		if published {
			if err := os.RemoveAll(bundle); err != nil {
				failures = append(failures, err)
			}
		}
		if movedOld {
			if err := os.Rename(oldBundle, bundle); err != nil {
				keepBackup = true
				failures = append(failures, fmt.Errorf("restore previous bundle (retained at %s): %w", oldBundle, err))
			} else if err := runCommand(run, lsregister, "-f", bundle); err != nil {
				failures = append(failures, fmt.Errorf("register previous bundle: %w", err))
			}
		}
		if oldPlist != nil {
			if err := writePlist(path, oldPlist); err != nil {
				failures = append(failures, fmt.Errorf("restore previous plist: %w", err))
			} else if stoppedOld {
				if err := runCommand(run, "launchctl", "bootstrap", domain, path); err != nil {
					failures = append(failures, fmt.Errorf("restart previous daemon: %w", err))
				}
			}
		} else if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failures = append(failures, err)
		}
		return errors.Join(append([]error{cause}, failures...)...)
	}
	if hadBundle {
		if err := os.Rename(bundle, oldBundle); err != nil {
			return rollback(fmt.Errorf("back up previous daemon bundle: %w", err))
		}
		movedOld = true
	}
	if err := os.Rename(candidate, bundle); err != nil {
		return rollback(fmt.Errorf("publish daemon bundle: %w", err))
	}
	published = true
	if err := runCommand(run, lsregister, "-f", bundle); err != nil {
		return rollback(fmt.Errorf("register installed daemon bundle: %w", err))
	}
	if err := writePlist(path, agentPlist(dir, bundle, env)); err != nil {
		return rollback(fmt.Errorf("write daemon LaunchAgent: %w", err))
	}
	if err := runCommand(run, "launchctl", "bootstrap", domain, path); err != nil {
		return rollback(fmt.Errorf("start daemon LaunchAgent: %w", err))
	}
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	err = waitDaemonReady(ctx, ready)
	cancel()
	if err != nil {
		return rollback(fmt.Errorf("start daemon LaunchAgent: %w", err))
	}
	return nil
}

// Uninstall stops only this LaunchAgent. Keep the signed app (as the old
// installer kept its copied binary); a later install may reuse the directory.
func Uninstall() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	return uninstall(path, fmt.Sprintf("gui/%d", os.Getuid()), func(name string, args ...string) ([]byte, error) {
		return exec.Command(name, args...).CombinedOutput() // #nosec G204 -- fixed command and owned plist path, no shell
	})
}

func uninstall(path, domain string, run commandRunner) error {
	old, err := ownedPlist(path)
	if err != nil || old == nil {
		return err
	}
	loaded, err := serviceLoaded(run, domain)
	if err != nil {
		return fmt.Errorf("inspect daemon LaunchAgent: %w", err)
	}
	if loaded {
		if err := runCommand(run, "launchctl", "bootout", domain+"/"+Label); err != nil {
			return fmt.Errorf("stop daemon LaunchAgent: %w", err)
		}
	}
	return os.Remove(path)
}
