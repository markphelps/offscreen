package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

const daemonInstallHint = "run `offscreen daemon install` from a shell where tart is on PATH"

func TestStartupWithoutTartNamesInstallAndDaemonInstall(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "osd-") // Unix socket paths are limited on macOS.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	t.Setenv("OFFSCREEN_CONFIG", filepath.Join(state, "config.toml"))
	missing := tart.Client{Binary: filepath.Join(t.TempDir(), "tart")}
	_, err = open(config.Default(), func(context.Context, run.Options) run.Result { return run.Result{} }, missing, func() (uint64, error) { return 20 << 30, nil })
	require.ErrorIs(t, err, tart.ErrNotInstalled)
	require.ErrorContains(t, err, "startup Tart sweep: tart list --format json: tart isn't installed or isn't on PATH")
	require.ErrorContains(t, err, tart.InstallHint)
	require.ErrorContains(t, err, daemonInstallHint)
}

func TestVMJobWithoutTartFailsWithInstallHints(t *testing.T) {
	missing := tart.Client{Binary: filepath.Join(t.TempDir(), "tart")}
	executor := func(ctx context.Context, _ run.Options) run.Result {
		return run.Result{Code: exitcode.RunnerFailure, Err: missing.Clone(ctx, "base", "job-1")}
	}
	_, client, root := fixture(t, executor, func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	failed := waitState(t, client, job.ID, "failed")
	require.Equal(t, exitcode.RunnerFailure, failed.Code)
	require.Contains(t, failed.Reason, "tart isn't installed or isn't on PATH")
	require.Contains(t, failed.Reason, tart.InstallHint)
	require.Contains(t, failed.Reason, daemonInstallHint)
}

func TestTartHintLeavesOtherErrorsUnchanged(t *testing.T) {
	err := os.ErrPermission
	require.Same(t, err, tartHint(err))
}
