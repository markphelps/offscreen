package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const cleanupLockName = "host-cleanup.lock"

const recoveryMarkerPrefix = "host-group-"

type recoveryMarker struct {
	GuardianPID int       `json:"guardian_pid"`
	TaskArgv    []string  `json:"task_argv"`
	Dir         string    `json:"dir"`
	StartedAt   time.Time `json:"started_at"`
}

// createRecoveryMarker persists a no-env ownership record before any task can
// start. A guardian killed without cleanup leaves it for human inspection.
func createRecoveryMarker(spec guardianSpec) (string, error) {
	if spec.RecoveryDir == "" {
		return "", errors.New("missing recovery directory")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	path := filepath.Join(spec.RecoveryDir, recoveryMarkerPrefix+hex.EncodeToString(id[:])+".json")
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600) // #nosec G703 -- owner-only directory from the inherited cleanup lease.
	if err != nil {
		return "", err
	}
	fail := func(cause error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path) // No child has started yet.
		return "", cause
	}
	if err := json.NewEncoder(file).Encode(recoveryMarker{GuardianPID: os.Getpid(), TaskArgv: spec.Args, Dir: spec.Dir, StartedAt: time.Now().UTC()}); err != nil {
		return fail(err)
	}
	if err := file.Sync(); err != nil {
		return fail(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path) // No child has started yet.
		return "", err
	}
	// Sync the directory entry as well as the file: a crash must not make a
	// started task invisible to the next daemon's admission barrier.
	dir, err := os.Open(spec.RecoveryDir) // #nosec G703 -- owner state directory from inherited lease.
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	err = dir.Sync()
	closeErr := dir.Close()
	if err != nil || closeErr != nil {
		_ = os.Remove(path) // Still before task Start.
		return "", errors.Join(err, closeErr)
	}
	return path, nil
}

func removeRecoveryMarker(path string) error {
	if err := os.Remove(path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func cleanupLockPath() (string, error) {
	state := os.Getenv("OFFSCREEN_STATE_DIR")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		state = filepath.Join(home, "Library", "Application Support", "offscreen")
	}
	if err := os.MkdirAll(state, 0o700); err != nil { // #nosec G703 -- owner's configured state directory
		return "", err
	}
	info, err := os.Lstat(state) // #nosec G703 -- caller-selected state path is validated for owner and permissions below.
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || stat.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("host cleanup state directory must be owner-only: %s", state)
	}
	return filepath.Join(state, cleanupLockName), nil
}

func hostCleanupLock(ctx context.Context, exclusive bool) (*os.File, error) {
	path, err := cleanupLockPath()
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600) // #nosec G703 -- fixed filename under owner-only directory
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		return nil, fmt.Errorf("host cleanup lock must be owner-only regular file: %w", err)
	}
	mode := syscall.LOCK_SH | syscall.LOCK_NB
	if exclusive {
		mode = syscall.LOCK_EX | syscall.LOCK_NB
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := syscall.Flock(int(file.Fd()), mode); err == nil {
			return file, nil
		} else if !errors.Is(err, syscall.EWOULDBLOCK) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

// WaitForHostCleanup is the restart admission barrier. Call after taking the
// daemon's singleton lock and BEFORE orphan sweep or scheduling any new jobs.
// An old guardian holds a shared flock through TERM/KILL and reap; an
// exclusive flock proves all living guardians finished. A marker left by a
// crashed guardian still blocks admission after the flock becomes free.
func WaitForHostCleanup(ctx context.Context) error {
	file, err := hostCleanupLock(ctx, true)
	if err != nil {
		return fmt.Errorf("wait for host cleanup: %w", err)
	}
	defer file.Close()
	entries, err := os.ReadDir(filepath.Dir(file.Name()))
	if err != nil {
		return fmt.Errorf("inspect host cleanup markers: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, recoveryMarkerPrefix) && strings.HasSuffix(name, ".json") {
			return fmt.Errorf("host cleanup unconfirmed: inspect %s and resolve leftover processes before explicitly removing the marker", filepath.Join(filepath.Dir(file.Name()), name))
		}
	}
	return nil
}

func guardianMain() int {
	control := os.NewFile(3, "control")
	specFile := os.NewFile(4, "spec")
	statusFile := os.NewFile(5, "status")
	lease := os.NewFile(6, "lease")
	// These are deliberately inherited by the guardian, but must not be
	// inherited again by its task. In particular, a leaked control pipe can
	// hide daemon death and a leaked lease can defeat the restart barrier.
	for fd := 3; fd <= 6; fd++ {
		syscall.CloseOnExec(fd)
	}
	defer control.Close()
	defer specFile.Close()
	defer statusFile.Close()
	defer lease.Close()
	status := guardianStatus{Version: 1, Code: 70, Failure: true}
	defer func() { _ = json.NewEncoder(statusFile).Encode(status) }()
	var spec guardianSpec
	if err := json.NewDecoder(io.LimitReader(specFile, 1<<20)).Decode(&spec); err != nil {
		status.Error = fmt.Sprintf("decode command: %v", err)
		return 0
	}
	if len(spec.Args) == 0 || spec.Path == "" {
		status.Error = "missing command"
		return 0
	}
	// Relative PATH lookup must happen under the submission environment and
	// task root, not in the daemon before cmd.Env is assigned.
	if err := os.Chdir(spec.Dir); err != nil {
		status.Error = fmt.Sprintf("working directory: %v", err)
		return 0
	}
	os.Clearenv()
	for _, entry := range spec.Env {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			_ = os.Setenv(key, value)
		}
	}
	path := spec.Path
	if !filepath.IsAbs(path) && !strings.ContainsRune(path, '/') {
		resolved, err := exec.LookPath(path)
		if err != nil {
			status.Error = fmt.Sprintf("find %s: %v", path, err)
			return 0
		}
		path = resolved
	}
	cmd := &exec.Cmd{
		Path: path, Args: spec.Args, Dir: spec.Dir, Env: spec.Env, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
	marker, err := createRecoveryMarker(spec)
	if err != nil {
		status.Error = fmt.Sprintf("record host group before start: %v", err)
		return 0
	}
	if err := cmd.Start(); err != nil {
		if removeErr := removeRecoveryMarker(marker); removeErr != nil {
			status.Error = fmt.Sprintf("start command: %v; remove recovery marker %s: %v", err, marker, removeErr)
			status.CleanupUnconfirmed = true
		} else {
			status.Error = fmt.Sprintf("start command: %v", err)
		}
		return 0
	}
	// The child is deliberately not Wait'ed until all group signals are sent:
	// an exited leader pins its PID against reuse while descendants are killed.
	kq, err := unix.Kqueue()
	if err == nil {
		defer unix.Close(kq)
		changes := []unix.Kevent_t{{Ident: uint64(cmd.Process.Pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
		_, err = unix.Kevent(kq, changes, nil, nil)
	}
	leaderExited := make(chan struct{}, 1)
	switch {
	case err == nil:
		go func() {
			var events [1]unix.Kevent_t
			for {
				_, watchErr := unix.Kevent(kq, nil, events[:], nil)
				if errors.Is(watchErr, syscall.EINTR) {
					continue
				}
				leaderExited <- struct{}{}
				return
			}
		}()
	case errors.Is(err, syscall.ESRCH):
		// It exited between Start and registration. As our unreaped child,
		// its PID is still pinned; cleanup below is safe.
		leaderExited <- struct{}{}
	default:
		status.Error = fmt.Sprintf("watch command: %v", err)
	}
	parentGone := make(chan struct{}, 1)
	go func() {
		var buf [1]byte
		_, _ = control.Read(buf[:])
		parentGone <- struct{}{}
	}()
	grace := 2 * time.Second
	if err == nil || errors.Is(err, syscall.ESRCH) {
		select {
		case <-leaderExited:
			// Normal completion should not cost two seconds per command.
			grace = 100 * time.Millisecond
		case <-parentGone:
		}
	}
	// A shell that exits successfully may have left background children. Even
	// if TERM killed the leader, still KILL the group before reaping it.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	time.Sleep(grace)
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	waitErr := cmd.Wait()
	// The exclusive startup barrier cannot pass until descendants are gone.
	// No signals are sent after Wait: the leader PID is no longer pinned.
	for {
		err := syscall.Kill(-cmd.Process.Pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			break
		}
		if errors.Is(err, syscall.EPERM) {
			// Darwin may report EPERM for an emptied group. Unlike ESRCH it
			// isn't proof: enumerate the group before clearing its marker.
			members, inspectErr := unix.SysctlKinfoProcSlice("kern.proc.pgrp", cmd.Process.Pid)
			if inspectErr == nil && len(members) == 0 {
				break
			}
			if inspectErr != nil {
				err = inspectErr
			} else {
				err = nil // Still has members; keep waiting.
			}
		}
		if err != nil {
			status.Error = fmt.Sprintf("confirm group cleanup for %s: %v", marker, err)
			status.CleanupUnconfirmed = true
			return 0
		}
		time.Sleep(20 * time.Millisecond)
	}
	if waitErr != nil {
		if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
			status.Error = fmt.Sprintf("confirm task reap for %s: %v", marker, waitErr)
			status.CleanupUnconfirmed = true
			return 0
		}
	}
	if err := removeRecoveryMarker(marker); err != nil {
		status.Error = fmt.Sprintf("remove recovery marker %s: %v", marker, err)
		status.CleanupUnconfirmed = true
		return 0
	}
	if status.Error != "" {
		return 0
	}
	status.Failure = false
	status.Code = 0
	if waitErr != nil {
		if exit, ok := errors.AsType[*exec.ExitError](waitErr); ok {
			status.Code = exit.ExitCode()
			if status.Code < 0 {
				if state, ok := exit.Sys().(syscall.WaitStatus); ok && state.Signaled() {
					status.Code = 128 + int(state.Signal())
				}
			}
			status.Error = waitErr.Error()
		} else {
			status.Failure, status.Code, status.Error = true, 70, waitErr.Error()
		}
	}
	return 0
}
