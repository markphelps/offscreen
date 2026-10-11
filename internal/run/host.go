package run

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
)

// ExecuteHost runs one already-admitted host job in the daemon's login session.
// The daemon owns serialization; guest setup and VM shares are not used here.
func ExecuteHost(ctx context.Context, o Options) (result Result) {
	result.ID, result.Artifacts = o.ID, o.Artifacts
	if result.ID == "" {
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			result.Code, result.Err = exitcode.RunnerFailure, err
			return
		}
		result.ID = hex.EncodeToString(id)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		result.Code, result.Err = exitcode.RunnerFailure, err
		return
	}
	base := filepath.Join(home, "Library", "Caches", "offscreen")
	if state := os.Getenv("OFFSCREEN_STATE_DIR"); state != "" {
		base = state
	}
	if result.Artifacts == "" {
		result.Artifacts = filepath.Join(base, "jobs", result.ID)
	}
	fail := func(err error) { result.Code, result.Err = exitcode.RunnerFailure, err }
	if err := os.MkdirAll(result.Artifacts, 0o700); err != nil { // #nosec G703 -- daemon assigns this owner-only job directory; standalone caller owns its explicit output path.
		fail(err)
		return
	}
	cache := filepath.Join(base, "projects", projectKey(o.Contract.Root))
	if err := os.MkdirAll(cache, 0o700); err != nil { // #nosec G703 -- cache path uses owner-selected state dir and a hex-encoded hash, not raw project input.
		fail(err)
		return
	}
	hostEnv := o.HostEnv
	if hostEnv == nil {
		hostEnv = environ()
	}
	spec, err := project.Resolve(o.Contract, project.RunRequest{
		Task: o.Task, Argv: o.Argv, Lane: o.Lane, JobID: result.ID,
		OutDir: result.Artifacts, CacheDir: cache, HostEnv: hostEnv,
	})
	if err != nil {
		fail(err)
		return
	}
	if spec.Lane != project.LaneHost {
		fail(fmt.Errorf("expected host lane, got %s", spec.Lane))
		return
	}
	phase := func(name string) {
		if o.Phase != nil {
			o.Phase(name)
		}
	}
	phase("preparing")
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	if spec.Prepare != "" {
		cmd := exec.Command("/bin/bash", "-o", "pipefail", "-c", spec.Prepare) // #nosec G204 -- repository-owned prepare command
		cmd.Dir, cmd.Stdout, cmd.Stderr = spec.Root, o.Stderr, o.Stderr
		cmd.Env = hostCommandEnv(hostEnv)
		if err := runGroup(ctx, cmd); err != nil {
			fail(fmt.Errorf("host prepare: %w", err))
			return
		}
	}
	if err := ctx.Err(); err != nil {
		fail(err)
		return
	}
	phase("running")
	var cmd *exec.Cmd
	if spec.Command.Shell != "" {
		cmd = exec.Command("/bin/bash", "-o", "pipefail", "-c", spec.Command.Shell) // #nosec G204 -- repository-owned task shell
	} else {
		// Resolve PATH inside the guardian with the submitting caller's env.
		cmd = &exec.Cmd{Path: spec.Command.Argv[0], Args: spec.Command.Argv}
	}
	cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.Root, o.Stdin, o.Stdout, o.Stderr
	taskEnv := make(map[string]string, len(hostEnv)+len(spec.Env))
	maps.Copy(taskEnv, hostEnv)
	maps.Copy(taskEnv, spec.Env)
	// Preserve caller host settings; only the four task-owned values are
	// overwritten by Resolve. Guest environment is constructed separately.
	cmd.Env = hostCommandEnv(taskEnv)
	result.Code, result.Err = localExit(runGroup(ctx, cmd))
	phase("collecting")
	return
}
