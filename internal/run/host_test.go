package run

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/project"
)

func TestExecuteHostPrepareEnvironmentArgvAndExit(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	marker := filepath.Join(root, "prepare")
	var stdout, stderr bytes.Buffer
	submission := map[string]string{"PATH": "/usr/bin:/bin", "VALUE": "caller=value", "OFFSCREEN_OUT": "forged"}
	contract := project.Contract{Root: root, Project: "sample", Guest: project.Guest{Setup: "exit 91"}, Prepare: project.Prepare{Run: "printf '%s' \"$VALUE\" > prepare"}}
	artifacts := filepath.Join(t.TempDir(), "artifacts")
	result := ExecuteHost(context.Background(), Options{
		ID: "hosttest", Artifacts: artifacts, Contract: contract, Lane: project.LaneHost,
		Argv:    []string{"/bin/sh", "-c", `printf '%s|%s|%s|%s|%s\n' "$1" "$OFFSCREEN_JOB" "$OFFSCREEN_LANE" "$OFFSCREEN_OUT" "$OFFSCREEN_CACHE"; echo error >&2; exit 7`, "script", "a b;c"},
		HostEnv: submission, Stdout: &stdout, Stderr: &stderr,
	})
	require.Equal(t, 7, result.Code)
	require.Error(t, result.Err)
	require.Equal(t, "error\n", stderr.String())
	parts := strings.Split(strings.TrimSpace(stdout.String()), "|")
	require.Len(t, parts, 5)
	require.Equal(t, "a b;c", parts[0])
	require.Equal(t, "hosttest", parts[1])
	require.Equal(t, "host", parts[2])
	require.Equal(t, artifacts, parts[3])
	require.DirExists(t, parts[4])
	prepared, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "caller=value", string(prepared))
	require.Equal(t, "forged", submission["OFFSCREEN_OUT"])
}

func TestHostCallerPATHAndOffscreenEnvironment(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root, bin := t.TempDir(), t.TempDir()
	executable := filepath.Join(bin, "caller-tool")
	require.NoError(t, os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s|%s|%s|%s|%s|%s' \"$1\" \"$OFFSCREEN_CONFIG\" \"$OFFSCREEN_STATE_DIR\" \"$OFFSCREEN_CUSTOM\" \"$OFFSCREEN_JOB\" \"$OFFSCREEN_LANE\"\n"), 0o700)) // #nosec G306 -- private test executable.
	var stdout bytes.Buffer
	result := ExecuteHost(context.Background(), Options{
		ID: "path-test", Contract: project.Contract{Root: root, Project: "sample"}, Lane: project.LaneHost,
		Argv: []string{"caller-tool", "literal ; $(false)"}, HostEnv: map[string]string{
			"PATH": bin + ":/usr/bin:/bin", "OFFSCREEN_CONFIG": "caller-config", "OFFSCREEN_STATE_DIR": "caller-state",
			"OFFSCREEN_CUSTOM": "caller-custom", "OFFSCREEN_JOB": "forged", "OFFSCREEN_LANE": "vm",
		}, Stdout: &stdout,
	})
	require.NoError(t, result.Err)
	require.Equal(t, 0, result.Code)
	require.Equal(t, "literal ; $(false)|caller-config|caller-state|caller-custom|path-test|host", stdout.String())
}

func TestHostCancellationKillsShellDescendants(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan Result, 1)
	go func() {
		completed <- ExecuteHost(ctx, Options{
			ID: "childtest", Artifacts: filepath.Join(root, "out"), Contract: project.Contract{Root: root, Project: "sample"}, Lane: project.LaneHost,
			Argv: []string{"/bin/sh", "-c", "sleep 120 & echo $! > child.pid; wait"}, HostEnv: map[string]string{"PATH": "/usr/bin:/bin"}, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		})
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 3*time.Second, 10*time.Millisecond)
	content, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	require.NoError(t, err)
	cancel()
	select {
	case result := <-completed:
		require.ErrorIs(t, result.Err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("host cancellation did not finish")
	}
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 20*time.Millisecond)
}

func TestVMPrepareCancellationKillsShellDescendants(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	pidFile := filepath.Join(root, "prepare.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan Result, 1)
	cfg := config.Default()
	cfg.CurrentImage = "unused-image"
	go func() {
		completed <- Execute(ctx, Options{
			ID: "preparetest", Artifacts: filepath.Join(root, "out"), Config: cfg, SkipLock: true,
			Contract: project.Contract{Root: root, Project: "sample", Prepare: project.Prepare{Run: "sleep 120 & echo $! > prepare.pid; wait"}},
			Argv:     []string{"/bin/true"}, Lane: project.LaneVM, HostEnv: map[string]string{"PATH": "/usr/bin:/bin"},
			Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		})
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 3*time.Second, 10*time.Millisecond)
	content, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	require.NoError(t, err)
	cancel()
	select {
	case result := <-completed:
		require.ErrorIs(t, result.Err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("VM prepare cancellation did not finish")
	}
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 20*time.Millisecond)
}
