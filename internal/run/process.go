package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/markphelps/offscreen/internal/exitcode"
)

const guardianArg = "--offscreen-internal-guardian"

// ErrCleanupUnconfirmed means the guardian did not prove that its process
// group has been cleaned up. Callers must stop host admission, not free a
// slot and start another task while an old one may still be running.
var ErrCleanupUnconfirmed = errors.New("guardian cleanup unconfirmed")

type guardianSpec struct {
	Path        string   `json:"path"`
	Args        []string `json:"args"`
	Dir         string   `json:"dir"`
	Env         []string `json:"env"`
	RecoveryDir string   `json:"recovery_dir"`
}

type guardianStatus struct {
	Version            int    `json:"version"`
	Code               int    `json:"code"`
	Error              string `json:"error,omitempty"`
	Failure            bool   `json:"failure,omitempty"`
	CleanupUnconfirmed bool   `json:"cleanup_unconfirmed,omitempty"`
}

// runGroup delegates group ownership to an independent process. Its liveness
// pipe closes even on SIGKILL of the daemon; task processes never inherit it.
func runGroup(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lease, err := hostCleanupLock(ctx, false)
	if err != nil {
		return err
	}
	defer lease.Close()
	controlR, controlW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer controlR.Close()
	defer controlW.Close()
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer statusR.Close()
	defer statusW.Close()
	specFile, err := os.CreateTemp("", "offscreen-guardian-*")
	if err != nil {
		return err
	}
	defer specFile.Close()
	defer os.Remove(specFile.Name())
	if err := json.NewEncoder(specFile).Encode(guardianSpec{Path: cmd.Path, Args: cmd.Args, Dir: cmd.Dir, Env: cmd.Env, RecoveryDir: filepath.Dir(lease.Name())}); err != nil {
		return err
	}
	if _, err := specFile.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Keep the spec only as a private open descriptor. A SIGKILL of the
	// launching process must not leave caller environment on disk.
	if err := os.Remove(specFile.Name()); err != nil {
		return err
	}
	// ExtraFiles become fd 3 (control), 4 (spec), 5 (status), 6 (lease).
	// The subprocess receives ONLY standard streams, not these descriptors.
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	guardian := exec.Command(exe, guardianArg) // #nosec G204 -- this is our own executable, not the task argv.
	// A terminal Ctrl-C targeting the daemon's foreground process group must
	// not terminate the guardian along with it.
	guardian.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	guardian.ExtraFiles = []*os.File{controlR, specFile, statusW, lease}
	guardian.Stdout, guardian.Stderr = cmd.Stdout, cmd.Stderr
	guardian.WaitDelay = 3 * time.Second
	var inputW *os.File
	if cmd.Stdin != nil {
		inputR, writer, err := os.Pipe()
		if err != nil {
			return err
		}
		inputW = writer
		guardian.Stdin = inputR
		defer inputR.Close()
		defer inputW.Close()
	}
	if err := guardian.Start(); err != nil {
		return err
	}
	_ = controlR.Close()
	_ = statusW.Close()
	if inputW != nil {
		// Never wait for a caller's arbitrary, potentially blocked Reader. Only
		// close our end of the pipe, never a Reader we don't own.
		go func() {
			_, _ = io.Copy(inputW, cmd.Stdin)
			_ = inputW.Close()
		}()
	}
	// The guardian now holds the same shared flock. The parent is not its
	// owner after launch, allowing recovery to wait for guardian cleanup.
	_ = lease.Close()
	done := make(chan error, 1)
	go func() { done <- guardian.Wait() }()
	select {
	case err := <-done:
		result := guardianResult(statusR, err)
		if ctxErr := ctx.Err(); ctxErr != nil {
			if _, taskExited := errors.AsType[taskExit](result); result == nil || taskExited {
				return ctxErr
			}
			return errors.Join(ctxErr, result)
		}
		return result
	case <-ctx.Done():
		_ = controlW.Close()
		select {
		case err := <-done:
			return guardianCancellation(statusR, err, ctx.Err())
		case <-time.After(10 * time.Second):
			return fmt.Errorf("%w: %w", ErrCleanupUnconfirmed, ctx.Err())
		}
	}
}

func guardianCancellation(r io.Reader, waitErr, ctxErr error) error {
	status, statusErr := readGuardianStatus(r)
	if statusErr != nil || waitErr != nil || status.CleanupUnconfirmed {
		if status.CleanupUnconfirmed {
			statusErr = errors.Join(statusErr, errors.New(status.Error))
		}
		return fmt.Errorf("%w: %w", ErrCleanupUnconfirmed, errors.Join(ctxErr, statusErr, waitErr))
	}
	return ctxErr
}

func guardianResult(r io.Reader, waitErr error) error {
	status, err := readGuardianStatus(r)
	if waitErr != nil || err != nil {
		return fmt.Errorf("%w: guardian status: %w", ErrCleanupUnconfirmed, errors.Join(waitErr, err))
	}
	if status.CleanupUnconfirmed {
		return fmt.Errorf("%w: guardian: %s", ErrCleanupUnconfirmed, status.Error)
	}
	if status.Failure {
		return fmt.Errorf("guardian: %s", status.Error)
	}
	if status.Code != 0 {
		return taskExit{code: status.Code, message: status.Error}
	}
	return nil
}

func readGuardianStatus(r io.Reader) (guardianStatus, error) {
	// Requiring both fields prevents a truncated but valid JSON object from
	// looking like a successful exit (the zero values would be version 1, code 0).
	var wire struct {
		Version            *int   `json:"version"`
		Code               *int   `json:"code"`
		Error              string `json:"error"`
		Failure            bool   `json:"failure"`
		CleanupUnconfirmed bool   `json:"cleanup_unconfirmed"`
	}
	dec := json.NewDecoder(io.LimitReader(r, 4096))
	if err := dec.Decode(&wire); err != nil {
		return guardianStatus{}, err
	}
	if wire.Version == nil || *wire.Version != 1 || wire.Code == nil || *wire.Code < 0 || *wire.Code > 255 || wire.Failure && wire.Error == "" {
		return guardianStatus{}, fmt.Errorf("invalid guardian status")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return guardianStatus{}, fmt.Errorf("trailing guardian status: %w", err)
	}
	return guardianStatus{Version: *wire.Version, Code: *wire.Code, Error: wire.Error, Failure: wire.Failure, CleanupUnconfirmed: wire.CleanupUnconfirmed}, nil
}

type taskExit struct {
	code    int
	message string
}

func (e taskExit) Error() string { return e.message }

func localExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exit, ok := errors.AsType[taskExit](err); ok {
		return exit.code, err
	}
	return exitcode.RunnerFailure, err
}

// MaybeGuardian is called by both binary entrypoints before parsing any flags.
// Tests invoke it explicitly in TestMain (never as a package init side effect).
func MaybeGuardian(args []string) bool {
	if len(args) != 1 || args[0] != guardianArg {
		return false
	}
	// guardianMain replaces this process's environment with the task's. A
	// coverage-instrumented build reads GOCOVERDIR as it exits, so restore the
	// guardian's own value after the task is gone. Otherwise the coverage
	// runtime warns on the task's stderr instead of writing its counters.
	coverDir, hasCoverDir := os.LookupEnv("GOCOVERDIR")
	code := guardianMain()
	if hasCoverDir {
		_ = os.Setenv("GOCOVERDIR", coverDir)
	} else {
		_ = os.Unsetenv("GOCOVERDIR")
	}
	os.Exit(code)
	return true
}
