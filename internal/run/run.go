// Package run executes synchronous VM jobs independently of the CLI. A daemon
// worker can call Execute with the same resolved JobSpec and streams later.
package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/tart"
)

const guestShares = "/Volumes/My Shared Files"

// Result identifies the job and its output even if execution failed.
type Result struct {
	ID, Artifacts, VM string
	Code              int
	Err               error
	Kept              bool
}

// Options hold the host and guest streams for an execution.
type Options struct {
	ID        string // Daemon-assigned ID; empty for standalone callers.
	Artifacts string // Daemon-assigned directory; empty uses the default cache.
	SkipLock  bool   // Daemon scheduler owns admission and project exclusion.
	// Phase reports each job state: on the VM lane preparing (host
	// prepare), cloning, booting, preparing (copy and guest setup), running
	// and collecting; the host lane skips cloning and booting.
	Phase          func(string)
	Config         config.Config
	Contract       project.Contract
	Task           string
	Argv           []string
	HostEnv        map[string]string // nil uses this process's environment for standalone/legacy runs.
	Lane           project.Lane
	Timeout        time.Duration
	Keep           bool
	Show           bool // Open the VM's window on the host instead of running headless.
	Stdin          io.Reader
	Stdout, Stderr io.Writer
	TTY            bool
}

// Execute always creates an artifact directory and returns a Result. The caller
// prints its final location on stderr, leaving stdout exclusively for the task.
func Execute(ctx context.Context, o Options) (result Result) {
	result.ID = o.ID
	if result.ID == "" {
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			result.Code, result.Err = exitcode.RunnerFailure, err
			return
		}
		result.ID = hex.EncodeToString(id)
	}
	result.VM = "job-" + result.ID
	home, err := os.UserHomeDir()
	if err != nil {
		result.Code, result.Err = exitcode.RunnerFailure, err
		return
	}
	base := filepath.Join(home, "Library", "Caches", "offscreen")
	if state := os.Getenv("OFFSCREEN_STATE_DIR"); state != "" {
		base = state
	}
	result.Artifacts = o.Artifacts
	if result.Artifacts == "" {
		result.Artifacts = filepath.Join(base, "jobs", result.ID)
	}
	if err := os.MkdirAll(result.Artifacts, 0o700); err != nil { // #nosec G703 -- daemon-owned job output or explicit standalone caller output directory.
		result.Code, result.Err = exitcode.RunnerFailure, err
		return
	}
	fail := func(err error) { result.Code, result.Err = exitcode.RunnerFailure, err }
	if o.Config.CurrentImage == "" {
		fail(errors.New("no current image; run offscreen image build first"))
		return
	}
	cache := filepath.Join(base, "projects", projectKey(o.Contract.Root))
	if err := os.MkdirAll(cache, 0o700); err != nil { // #nosec G703 -- owner state path plus a SHA-256 project key, never raw project path.
		fail(err)
		return
	}
	work := "offscreen/jobs/" + result.ID + "/worktree"
	out := "offscreen/jobs/" + result.ID + "/out"
	guestHome := "/Users/" + o.Config.Image.SSHUser
	guestWork := guestHome + "/" + work
	guestOut := guestHome + "/" + out
	guestCache := guestShares + "/cache"
	hostEnv := o.HostEnv
	if hostEnv == nil {
		hostEnv = environ()
	}
	spec, err := project.Resolve(o.Contract, project.RunRequest{
		Task: o.Task, Argv: o.Argv, Lane: o.Lane, JobID: result.ID,
		OutDir: guestOut, CacheDir: guestCache, HostEnv: hostEnv,
	})
	if err != nil {
		fail(err)
		return
	}
	if spec.Lane != project.LaneVM {
		fail(fmt.Errorf("%s lane is not supported yet; use --lane vm", spec.Lane))
		return
	}
	if o.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.Timeout)
		defer cancel()
	}
	if !o.SkipLock {
		unlock, err := acquire(ctx, filepath.Join(base, "locks"), spec.Root, spec.Exclusive, o.Config.VMSlots, o.Config.LowMemoryThresholdGB)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				result.Code, result.Err = exitcode.QueueTimeout, err
			} else {
				fail(err)
			}
			return
		}
		defer unlock()
	}
	phase := func(s string) {
		if o.Phase != nil {
			o.Phase(s)
		}
	}
	phase("preparing")
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	if spec.Prepare != "" {
		cmd := exec.Command("/bin/bash", "-o", "pipefail", "-c", spec.Prepare) // #nosec G204 -- project-owned contract explicitly supplies a host shell command
		cmd.Dir, cmd.Stdout, cmd.Stderr = spec.Root, o.Stderr, o.Stderr
		cmd.Env = hostCommandEnv(hostEnv)
		if err := runGroup(ctx, cmd); err != nil {
			fail(fmt.Errorf("host prepare: %w", err))
			return
		}
	}
	client := tart.Client{}
	phase("cloning")
	if err := client.Clone(ctx, o.Config.CurrentImage, result.VM); err != nil {
		fail(err)
		return
	}
	bootCtx, cancelBoot := context.WithCancel(context.Background())
	var vm *exec.Cmd
	var waitVM func() error
	defer func() {
		if result.Code != 0 && o.Keep && vm != nil && !errors.Is(ctx.Err(), context.Canceled) {
			result.Kept = true
			// Keep a started clone after task failure or deadline expiry, but
			// explicit cancellation (Ctrl-C) must still destroy it. Tart's
			// process cannot be tied to the execution context on this path.
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if vm != nil {
			_ = client.Stop(cleanup, result.VM)
		}
		cancelBoot()
		if waitVM != nil {
			_ = waitVM()
		}
		if err := client.Delete(cleanup, result.VM); err != nil {
			if result.Err == nil {
				fail(fmt.Errorf("delete clone: %w", err))
			} else {
				result.Err = fmt.Errorf("%w; delete clone: %w", result.Err, err)
			}
		}
	}()
	shares := []string{"worktree:" + spec.Root + ":ro", "artifacts:" + result.Artifacts, "cache:" + cache}
	phase("booting")
	vm, err = client.StartShared(bootCtx, result.VM, shares, o.Show)
	if err != nil {
		fail(err)
		return
	}
	waitVM = sync.OnceValue(vm.Wait)
	creds := guest.Credentials{User: o.Config.Image.SSHUser, Password: o.Config.Image.SSHPassword, Key: o.Config.Image.SSHKey}
	sshClient, err := waitGuest(ctx, client, creds, result.VM)
	if err != nil {
		fail(err)
		return
	}
	defer func() { _ = sshClient.Close() }()
	if err := waitGUI(ctx, sshClient); err != nil {
		fail(fmt.Errorf("guest GUI not ready: %w", err))
		return
	}
	// Copy the worktree and shares, then run guest setup.
	phase("preparing")
	if err := guest.Stream(ctx, sshClient, "mkdir -p "+guest.Quote(guestWork)+" "+guest.Quote(guestOut), nil, io.Discard, o.Stderr, false); err != nil {
		fail(fmt.Errorf("create guest directories: %w", err))
		return
	}
	if err := sendWorktree(ctx, sshClient, spec.Root, guestWork); err != nil {
		fail(fmt.Errorf("copy worktree: %w", err))
		return
	}
	for _, share := range spec.Shares {
		// Explicit products are copied separately, including those under
		// .build (excluded from the main worktree transfer).
		destination := guestWork
		if relative, err := filepath.Rel(spec.Root, share); err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			destination = filepath.Join(guestWork, filepath.Dir(relative))
		}
		if err := sendTar(ctx, sshClient, filepath.Dir(share), filepath.Base(share), destination); err != nil {
			fail(fmt.Errorf("copy prepared share %q: %w", share, err))
			return
		}
	}
	var env strings.Builder
	for _, key := range []string{"OFFSCREEN_OUT", "OFFSCREEN_JOB", "OFFSCREEN_LANE", "OFFSCREEN_CACHE"} {
		env.WriteString(key)
		env.WriteByte('=')
		env.WriteString(guest.Quote(spec.Env[key]))
		env.WriteByte(' ')
	}
	prefix := "cd " + guest.Quote(guestWork) + " && " + env.String()
	if spec.GuestSetup != "" {
		err = guest.Stream(ctx, sshClient, prefix+guest.Command(spec.GuestSetup, nil), nil, o.Stdout, o.Stderr, false)
	}
	if err == nil {
		phase("running")
		err = guest.Stream(ctx, sshClient, prefix+guest.Command(spec.Command.Shell, spec.Command.Argv), o.Stdin, o.Stdout, o.Stderr, o.TTY)
	}
	result.Code, result.Err = commandExit(err)
	phase("collecting")
	// Collect while the clone is still running, even after the active context
	// expires. Stream closes its SSH client on cancellation, so recovery must
	// dial again under a separate, short-lived context.
	collectErr := collectArtifacts(ctx,
		func(active context.Context) error {
			return receiveTar(active, sshClient, guestOut, result.Artifacts)
		},
		func(cleanup context.Context) error {
			recoveryClient, err := waitGuest(cleanup, client, creds, result.VM)
			if err != nil {
				return fmt.Errorf("reconnect guest for artifacts: %w", err)
			}
			defer func() { _ = recoveryClient.Close() }()
			return receiveTar(cleanup, recoveryClient, guestOut, result.Artifacts)
		},
	)
	if collectErr != nil {
		if result.Err == nil {
			fail(fmt.Errorf("collect artifacts: %w", collectErr))
		} else {
			result.Err = fmt.Errorf("%w; collect artifacts: %w", result.Err, collectErr)
		}
	}
	return
}

// collectArtifacts uses the original connection while it is live. If the
// active deadline/cancellation has closed it (including mid-transfer), retry
// once with a fresh connection. The single recovery budget covers both dial
// and transfer; it never extends the active job's execution time or exit code.
func collectArtifacts(ctx context.Context, collect, recoverArtifacts func(context.Context) error) error {
	if ctx.Err() == nil {
		err := collect(ctx)
		if err == nil || ctx.Err() == nil {
			return err
		}
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return recoverArtifacts(cleanup)
}

func commandExit(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if status, ok := errors.AsType[*ssh.ExitError](err); ok {
		return status.ExitStatus(), err
	}
	return exitcode.RunnerFailure, err
}

// hostCommandEnv builds a stable, per-job environment without changing the
// daemon's own PATH (used to find Tart) or forwarding it to the guest.
func hostCommandEnv(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+env[key])
	}
	return result
}

func environ() map[string]string {
	result := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			result[key] = value
		}
	}
	return result
}
