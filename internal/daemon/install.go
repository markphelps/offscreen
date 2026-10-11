package daemon

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const Label = "dev.offscreen.offscreend"

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

// Install copies the daemon to an owned, stable path so launchd never refers to
// a temporary build/worktree. Reinstall to update it after rebuilding.
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
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o700); err != nil {
		return err
	}
	dest := filepath.Join(dir, "bin", "offscreend")
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	temp, err := os.CreateTemp(filepath.Dir(dest), "offscreend-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(temp.Name()) }()
	if _, err = io.Copy(temp, in); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Chmod(0o700); err != nil {
		_ = temp.Close()
		return err
	}
	if err = temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), dest); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var env strings.Builder
	env.WriteString("<key>PATH</key><string>" + xmlString(os.Getenv("PATH")) + "</string>")
	for _, key := range []string{"OFFSCREEN_CONFIG", "OFFSCREEN_STATE_DIR"} {
		if v := os.Getenv(key); v != "" {
			env.WriteString("<key>" + key + "</key><string>" + xmlString(v) + "</string>")
		}
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>Label</key><string>` + Label + `</string><key>ProgramArguments</key><array><string>` + xmlString(dest) + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><true/><key>EnvironmentVariables</key><dict>` + env.String() + `</dict><key>StandardErrorPath</key><string>` + xmlString(filepath.Join(dir, "daemon.log")) + `</string></dict></plist>`
	if err := os.WriteFile(path, []byte(plist), 0o600); err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", domain, path).Run()
	if output, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, output)
	}
	return nil
}

func Uninstall() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if output, err := exec.Command("launchctl", "bootout", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootout: %w: %s", err, output)
	}
	return os.Remove(path)
}
