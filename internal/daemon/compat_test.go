package daemon

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

func TestSubmissionUsesEachClientsHostEnvironment(t *testing.T) {
	var mu sync.Mutex
	received := make(map[string]map[string]string)
	s, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		mu.Lock()
		received[o.ID] = o.HostEnv
		mu.Unlock()
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	const key = "OFFSCREEN_CLIENT_ONLY_PRODUCTS_DIR"
	t.Setenv(key, "daemon-value")
	require.NoError(t, os.Unsetenv(key)) // A caller-only variable cannot be resolved from the daemon.
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\n[prepare]\nshare = ['$"+key+"/App.app']\n"), 0o600))
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first := submitFixture(t, client, root, Request{Detach: true, HostEnv: map[string]string{key: firstDir, "FOO": "first=value"}})
	second := submitFixture(t, client, root, Request{Detach: true, HostEnv: map[string]string{key: secondDir, "FOO": "second"}})
	waitState(t, client, first.ID, "done")
	waitState(t, client, second.ID, "done")
	mu.Lock()
	require.Equal(t, firstDir, received[first.ID][key])
	require.Equal(t, "first=value", received[first.ID]["FOO"])
	require.Equal(t, secondDir, received[second.ID][key])
	require.Equal(t, "second", received[second.ID]["FOO"])
	mu.Unlock()
	for _, tc := range []struct {
		job Job
		dir string
	}{{first, firstDir}, {second, secondDir}} {
		var stored string
		require.NoError(t, s.db.QueryRow(`SELECT request FROM jobs WHERE id=?`, tc.job.ID).Scan(&stored))
		var request Request
		require.NoError(t, json.Unmarshal([]byte(stored), &request))
		contract, err := project.LoadFile(filepath.Join(root, project.FileName))
		require.NoError(t, err)
		spec, err := project.Resolve(contract, project.RunRequest{Argv: []string{"true"}, JobID: tc.job.ID, OutDir: "/tmp/out", CacheDir: "/tmp/cache", HostEnv: received[tc.job.ID]})
		require.NoError(t, err)
		require.Equal(t, []string{filepath.Join(tc.dir, "App.app")}, spec.Shares)
		// Completed records drop the caller snapshot once execution has used it.
		require.Nil(t, request.HostEnv)
		require.NotContains(t, stored, tc.dir)
		require.Equal(t, []string{"true"}, request.Argv)
	}
}

func TestLegacySubmissionFallsBackToDaemonEnvironment(t *testing.T) {
	observed := make(chan map[string]string, 1)
	_, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		observed <- o.HostEnv
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	t.Setenv("OFFSCREEN_LEGACY_PRODUCTS_DIR", t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\n[prepare]\nshare = ['$OFFSCREEN_LEGACY_PRODUCTS_DIR/App.app']\n"), 0o600))
	job := submitFixture(t, client, root, Request{Detach: true})
	require.Equal(t, "done", waitState(t, client, job.ID, "done").State)
	require.Nil(t, <-observed) // Legacy workers retain the standalone fallback.
}

func TestRestartFailsQueuedAttachedButLeavesDetachedQueued(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result {
		t.Error("queued job executed without a host gate")
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	attached := submitFixture(t, client, root, Request{Lane: project.LaneHost})
	detached := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	require.NoError(t, s.Close())
	fake := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o700)) // #nosec G306 -- test executable
	reopened, err := open(config.Default(), func(context.Context, run.Options) run.Result {
		t.Error("recovered queued job executed without a host gate")
		return run.Result{}
	}, tart.Client{Binary: fake}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	failure := reopened.jobs[attached.ID].job
	require.Equal(t, "failed", failure.State)
	require.Equal(t, exitcode.RunnerFailure, failure.Code)
	require.Contains(t, failure.Reason, "stdin attachment")
	require.Equal(t, "queued", reopened.jobs[detached.ID].job.State)
	var state, reason string
	require.NoError(t, reopened.db.QueryRow(`SELECT state,reason FROM jobs WHERE id=?`, attached.ID).Scan(&state, &reason))
	require.Equal(t, failure.State, state)
	require.Equal(t, failure.Reason, reason)
	require.Eventually(t, func() bool {
		reopened.mu.Lock()
		defer reopened.mu.Unlock()
		return reopened.jobs[detached.ID].job.State == "queued"
	}, time.Second, 20*time.Millisecond)
}
