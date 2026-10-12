package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// The test binary enters guardian mode only here, never during package init.
func TestMain(m *testing.M) {
	if os.Getenv("OFFSCREEN_TEST_GUARDIAN_CRASH") == "1" && len(os.Args) == 2 && os.Args[1] == guardianArg {
		os.Exit(2)
	}
	if MaybeGuardian(os.Args[1:]) {
		return
	}
	if os.Getenv("OFFSCREEN_TEST_PARENT") == "1" {
		cmd := exec.Command("/bin/sh", "-c", `sh -c 'trap "" TERM; echo $$ > child.pid; while :; do sleep 1; done' & wait`)
		cmd.Dir = os.Getenv("OFFSCREEN_TEST_ROOT")
		_ = runGroup(context.Background(), cmd)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func testGroup(t *testing.T, ctx context.Context, script string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", script)
	cmd.Dir = t.TempDir()
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runGroup(ctx, cmd)
	return stdout.String(), stderr.String(), err
}

func TestGuardianOutputExitAndFastLeader(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	out, stderr, err := testGroup(t, context.Background(), `printf 'out'; printf 'err' >&2; exit 17`)
	code, _ := localExit(err)
	require.Equal(t, 17, code)
	require.Equal(t, "out", out)
	require.Equal(t, "err", stderr)
	for range 5 {
		_, _, err = testGroup(t, context.Background(), `exit 0`)
		require.NoError(t, err)
	}
	requireNoRecoveryMarkers(t, os.Getenv("OFFSCREEN_STATE_DIR"))
	require.NoError(t, WaitForHostCleanup(context.Background()))
}

// Under go test -cover the guardian keeps its own GOCOVERDIR for its exit,
// but the task still sees only its submitted environment.
func TestGuardianCoverageSettingStaysOutOfTask(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	var stdout, stderr bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", `printf '%s' "${GOCOVERDIR-unset}"; printf 'err' >&2; exit 3`)
	cmd.Dir, cmd.Env = t.TempDir(), []string{"PATH=/usr/bin:/bin"}
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code, _ := localExit(runGroup(context.Background(), cmd))
	require.Equal(t, 3, code)
	require.Equal(t, "unset", stdout.String())
	require.Equal(t, "err", stderr.String())
}

func TestGuardianStartFailureRemovesMarker(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	cmd := &exec.Cmd{Path: "/no-such-offscreen-command", Args: []string{"/no-such-offscreen-command"}, Dir: t.TempDir()}
	err := runGroup(context.Background(), cmd)
	require.ErrorContains(t, err, "start command")
	require.NotErrorIs(t, err, ErrCleanupUnconfirmed)
	requireNoRecoveryMarkers(t, state)
	require.NoError(t, WaitForHostCleanup(context.Background()))
}

func TestGuardianRecordsOwnerStateBeforeStartingTask(t *testing.T) {
	state := filepath.Join(t.TempDir(), "owner-state")
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	// The submitted environment can name a different state directory. Only
	// the already-acquired cleanup lease identifies the owner's state.
	cmd := exec.Command("/bin/sh", "-c", `set -- "$1"/host-group-*.json; test -f "$1" || exit 25; echo $$ > task.pid; sleep 1`, "sh", state)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "OFFSCREEN_STATE_DIR=" + filepath.Join(root, "wrong-state")}
	done := make(chan error, 1)
	go func() { done <- runGroup(context.Background(), cmd) }()
	pidFile := filepath.Join(root, "task.pid")
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 3*time.Second, 10*time.Millisecond)
	entries, err := os.ReadDir(state)
	require.NoError(t, err)
	var markers int
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), recoveryMarkerPrefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		markers++
		info, err := entry.Info()
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		data, err := os.ReadFile(filepath.Join(state, entry.Name()))
		require.NoError(t, err)
		var marker recoveryMarker
		require.NoError(t, json.Unmarshal(data, &marker))
		require.NotZero(t, marker.GuardianPID)
		require.Equal(t, cmd.Args, marker.TaskArgv)
		require.Equal(t, root, marker.Dir)
		require.False(t, marker.StartedAt.IsZero())
		require.NotContains(t, string(data), "OFFSCREEN_STATE_DIR")
	}
	require.Equal(t, 1, markers)
	require.NoError(t, <-done)
	requireNoRecoveryMarkers(t, state)
	require.NoError(t, WaitForHostCleanup(context.Background()))
}

func TestGuardianSignalExitAndStatusFailures(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	_, _, err := testGroup(t, context.Background(), `kill -TERM $$`)
	code, _ := localExit(err)
	require.Equal(t, 128+int(syscall.SIGTERM), code)
	_, _, err = testGroup(t, context.Background(), `exit 70`)
	code, _ = localExit(err)
	require.Equal(t, 70, code, "a task's own exit 70 is not a transport failure")
	for _, raw := range []string{"", `{\"version\":1`, `{\"version\":2,\"code\":0}`, `{\"version\":1,\"code\":0}garbage`} {
		_, statusErr := readGuardianStatus(strings.NewReader(raw))
		require.Error(t, statusErr)
		code, _ := localExit(guardianResult(strings.NewReader(raw), nil))
		require.Equal(t, 70, code)
	}
	_, statusErr := readGuardianStatus(strings.NewReader(`{"version":1,"code":0}`))
	require.NoError(t, statusErr)
	waitResult := guardianResult(strings.NewReader(`{"version":1,"code":0}`), io.ErrUnexpectedEOF)
	code, _ = localExit(waitResult)
	require.Equal(t, 70, code)
	require.ErrorIs(t, waitResult, ErrCleanupUnconfirmed)
	require.ErrorIs(t, waitResult, io.ErrUnexpectedEOF)
}

func TestGuardianStatusConfirmation(t *testing.T) {
	for _, raw := range []string{
		"", `{"version":1`, `{"version":1}`, `{"code":0}`,
		`{"version":1,"code":null}`, `{"version":1,"code":256}`,
		`{"version":1,"code":70,"failure":true}`,
		`{"version":1,"code":0}{"version":1,"code":0}`,
	} {
		t.Run(raw, func(t *testing.T) {
			result := guardianResult(strings.NewReader(raw), nil)
			require.ErrorIs(t, result, ErrCleanupUnconfirmed)
			code, _ := localExit(result)
			require.Equal(t, 70, code)
			canceled := guardianCancellation(strings.NewReader(raw), nil, context.Canceled)
			require.ErrorIs(t, canceled, ErrCleanupUnconfirmed)
			require.ErrorIs(t, canceled, context.Canceled)
		})
	}
	for _, raw := range []string{`{"version":1,"code":0}`, `{"version":1,"code":7}`, `{"version":1,"code":70,"failure":true,"error":"start failed"}`} {
		require.NotErrorIs(t, guardianResult(strings.NewReader(raw), nil), ErrCleanupUnconfirmed)
		canceled := guardianCancellation(strings.NewReader(raw), nil, context.Canceled)
		require.ErrorIs(t, canceled, context.Canceled)
		require.NotErrorIs(t, canceled, ErrCleanupUnconfirmed)
	}
	waitFailure := guardianCancellation(strings.NewReader(`{"version":1,"code":0}`), errors.New("wait failed"), context.DeadlineExceeded)
	require.ErrorIs(t, waitFailure, ErrCleanupUnconfirmed)
	require.ErrorIs(t, waitFailure, context.DeadlineExceeded)
}

func TestGuardianCrashFailsClosed(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	t.Setenv("OFFSCREEN_TEST_GUARDIAN_CRASH", "1")
	_, _, err := testGroup(t, context.Background(), `exit 0`)
	require.ErrorIs(t, err, ErrCleanupUnconfirmed)
	code, _ := localExit(err)
	require.Equal(t, 70, code)
}

// A guardian can die after Start, dropping its flock while its task lives.
// The durable marker must keep a new daemon from admitting overlapping work.
func TestGuardianCrashAfterTaskStartBlocksRestart(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	pidFile := filepath.Join(root, "task.pid")
	cmd := exec.Command("/bin/sh", "-c", `echo $$ > task.pid; kill -9 "$PPID"; exec /bin/sleep 60`)
	cmd.Dir = root
	defer func() {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return // No task started; no owned group to clean up.
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || syscall.Kill(pid, 0) != nil {
			return
		}
		// Never signal a stored marker PID: this is the known, still-live task
		// started by this test. Check its process-group identity before signaling.
		pgid, err := syscall.Getpgid(pid)
		if err != nil || pgid != pid {
			return
		}
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond)
	}()
	err := runGroup(context.Background(), cmd)
	require.ErrorIs(t, err, ErrCleanupUnconfirmed)
	pid := readTestPID(t, pidFile)
	require.NoError(t, syscall.Kill(pid, 0), "orphaned task must really still be alive")
	lease, err := hostCleanupLock(context.Background(), true)
	require.NoError(t, err, "the guardian's shared flock must have been released")
	require.NoError(t, lease.Close())
	entries, err := os.ReadDir(state)
	require.NoError(t, err)
	var marker string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "host-group-") && strings.HasSuffix(entry.Name(), ".json") {
			marker = filepath.Join(state, entry.Name())
		}
	}
	require.NotEmpty(t, marker)
	barrier, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err = WaitForHostCleanup(barrier)
	require.ErrorContains(t, err, marker)
	require.ErrorContains(t, err, "inspect")
	// Simulate explicit manual recovery only for this test-owned, live group.
	pgid, err := syscall.Getpgid(pid)
	require.NoError(t, err)
	require.Equal(t, pid, pgid)
	require.NoError(t, syscall.Kill(-pid, syscall.SIGKILL))
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond)
	require.NoError(t, os.Remove(marker))
	require.NoError(t, WaitForHostCleanup(context.Background()))
}

func TestGuardianControlDescriptorsNotInheritedByTask(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	// /dev/fd/N reports an open descriptor in the actual exec'd task. None
	// of the guardian's control, spec, status or cleanup lease may survive.
	out, _, err := testGroup(t, context.Background(), `for fd in 3 4 5 6; do if [ -e /dev/fd/$fd ]; then printf '%s ' "$fd"; fi; done`)
	require.NoError(t, err)
	require.Empty(t, out, "task inherited guardian-only descriptors: %s", out)
}

func TestGuardianCleansNormalBackgroundAndUnrelatedProcess(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	sentinel := exec.Command("/bin/sleep", "30")
	require.NoError(t, sentinel.Start())
	t.Cleanup(func() { _ = sentinel.Process.Kill(); _ = sentinel.Wait() })
	cmd := exec.Command("/bin/sh", "-c", `sleep 30 >/dev/null 2>&1 & echo $! > child.pid; exit 0`)
	cmd.Dir = root
	require.NoError(t, runGroup(context.Background(), cmd))
	pid := readTestPID(t, pidFile)
	require.Equal(t, syscall.ESRCH, syscall.Kill(pid, 0))
	require.NoError(t, syscall.Kill(sentinel.Process.Pid, 0))
}

func TestGuardianCancelKillsTermIgnoringChild(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	pidFile := filepath.Join(root, "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		cmd := exec.Command("/bin/sh", "-c", `sh -c 'trap "" TERM; echo $$ > child.pid; while :; do sleep 1; done' & wait`)
		cmd.Dir = root
		done <- runGroup(ctx, cmd)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 3*time.Second, 10*time.Millisecond)
	pid := readTestPID(t, pidFile)
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(7 * time.Second):
		t.Fatal("guardian failed to cancel")
	}
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 10*time.Millisecond)
}

func TestGuardianBlockedInputAndInheritedOutputBounded(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	defer reader.Close()
	defer writer.Close()
	var out bytes.Buffer
	cmd := exec.Command("/bin/sh", "-c", `sleep 30 & exit 0`)
	cmd.Dir, cmd.Stdin, cmd.Stdout = t.TempDir(), reader, &out
	start := time.Now()
	require.NoError(t, runGroup(context.Background(), cmd))
	require.Less(t, time.Since(start), 6*time.Second)
}

func TestHostCleanupBarrierWaitsForGuardian(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		cmd := exec.Command("/bin/sh", "-c", `sh -c 'trap "" TERM; echo $$ > child.pid; while :; do sleep 1; done' & wait`)
		cmd.Dir = root
		done <- runGroup(ctx, cmd)
	}()
	pidFile := filepath.Join(root, "child.pid")
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 3*time.Second, 10*time.Millisecond)
	pid := readTestPID(t, pidFile)
	cancel()
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	require.ErrorIs(t, WaitForHostCleanup(short), context.DeadlineExceeded)
	require.ErrorIs(t, <-done, context.Canceled)
	full, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	require.NoError(t, WaitForHostCleanup(full))
	requireNoRecoveryMarkers(t, os.Getenv("OFFSCREEN_STATE_DIR"))
	require.Equal(t, syscall.ESRCH, syscall.Kill(pid, 0))
}

func TestGuardianSurvivesKilledDaemon(t *testing.T) {
	state, root := filepath.Join(t.TempDir(), "state"), t.TempDir()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	parent := exec.Command(os.Args[0]) // #nosec G204,G702 -- this test binary runs a controlled test harness.
	parent.Env = append(os.Environ(), "OFFSCREEN_TEST_PARENT=1", "OFFSCREEN_TEST_ROOT="+root)
	require.NoError(t, parent.Start())
	t.Cleanup(func() { _ = parent.Process.Kill(); _ = parent.Wait() })
	pidFile := filepath.Join(root, "child.pid")
	require.Eventually(t, func() bool { _, err := os.Stat(pidFile); return err == nil }, 4*time.Second, 10*time.Millisecond)
	pid := readTestPID(t, pidFile)
	require.NoError(t, parent.Process.Kill())
	require.Error(t, parent.Wait())
	short, stopShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	require.ErrorIs(t, WaitForHostCleanup(short), context.DeadlineExceeded)
	stopShort()
	barrier, stop := context.WithTimeout(context.Background(), 8*time.Second)
	defer stop()
	require.NoError(t, WaitForHostCleanup(barrier))
	requireNoRecoveryMarkers(t, state)
	require.Eventually(t, func() bool { return syscall.Kill(pid, 0) == syscall.ESRCH }, 3*time.Second, 20*time.Millisecond)
}

func requireNoRecoveryMarkers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), recoveryMarkerPrefix) && strings.HasSuffix(entry.Name(), ".json"), "unexpected recovery marker %s", entry.Name())
	}
}

func readTestPID(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	require.NoError(t, err)
	return pid
}
