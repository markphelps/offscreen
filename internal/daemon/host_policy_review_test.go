package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

func TestExclusiveVMInvalidatesHostCountdownBeforeGate(t *testing.T) {
	release := make(chan struct{})
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		select {
		case <-release:
			return run.Result{}
		case <-ctx.Done():
			return run.Result{Code: 70, Err: ctx.Err()}
		}
	}, func() (uint64, error) { return 20 << 30, nil })
	s.cfg.IdleThreshold = 30 * time.Second
	s.ConfigureHostController()
	s.mu.Lock()
	s.hostControl.seen = time.Now()
	s.hostControl.snapshot = HostSnapshot{IdleSeconds: 60, Unlocked: true, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}
	s.mu.Unlock()
	host := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	require.Eventually(t, func() bool { return s.hostStatus().CountdownJob == host.ID }, time.Second, 10*time.Millisecond)
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\nexclusive = true\n"), 0o600))
	vm := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, vm.ID, "running")
	s.mu.Lock()
	s.hostControl.countdownAt = time.Now().Add(-11 * time.Second)
	s.mu.Unlock()
	require.Eventually(t, func() bool {
		j, err := client.Job(context.Background(), host.ID)
		return err == nil && j.Reason == "waiting for exclusive project" && s.hostStatus().CountdownJob == ""
	}, time.Second, 10*time.Millisecond)
	_, err := client.HostAction(context.Background(), "not-now", host.ID)
	require.ErrorContains(t, err, "409", "blocked countdown must not retain an old Not now action")
	// This is the equivalent of the exclusive VM running longer than the
	// entire countdown; the next gate must begin at ten seconds again.
	close(release)
	waitState(t, client, vm.ID, "done")
	require.Eventually(t, func() bool { return s.hostStatus().CountdownJob == host.ID }, time.Second, 10*time.Millisecond)
	status := s.hostStatus()
	require.GreaterOrEqual(t, status.CountdownSeconds, 9)
	j, err := client.Job(context.Background(), host.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", j.State)
	_, err = client.Cancel(context.Background(), host.ID)
	require.NoError(t, err)
}

func TestHostPausePolicySurvivesReopenAndExpiredHold(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	binary := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- private fixture executable
	openTest := func() *Server {
		t.Helper()
		s, err := open(config.Default(), func(context.Context, run.Options) run.Result { return run.Result{} }, tart.Client{Binary: binary}, func() (uint64, error) { return 20 << 30, nil })
		require.NoError(t, err)
		return s
	}
	s := openTest()
	require.NoError(t, s.PauseHost())
	s.mu.Lock()
	until := time.Now().Add(30 * time.Minute)
	require.NoError(t, s.persistHostPolicy(true, until))
	s.hostControl.pausedUntil = until
	s.mu.Unlock()
	require.NoError(t, s.Close())
	s = openTest()
	require.True(t, s.hostStatus().Paused)
	require.True(t, s.hostPaused)
	require.WithinDuration(t, until, s.hostControl.pausedUntil, time.Millisecond)
	require.NoError(t, s.ResumeHost())
	require.NoError(t, s.Close())
	s = openTest()
	require.False(t, s.hostStatus().Paused)
	require.True(t, s.hostControl.pausedUntil.IsZero())
	s.mu.Lock()
	require.NoError(t, s.persistHostPolicy(false, until))
	s.hostControl.pausedUntil = until
	s.mu.Unlock()
	require.NoError(t, s.Close())
	s = openTest()
	require.False(t, s.hostPaused)
	require.WithinDuration(t, until, s.hostControl.pausedUntil, time.Millisecond)
	require.True(t, s.hostStatus().Paused, "Not now hold survives restart without manual pause")
	s.mu.Lock()
	require.NoError(t, s.persistHostPolicy(false, time.Now().Add(-time.Second)))
	s.mu.Unlock()
	require.NoError(t, s.Close())
	s = openTest()
	require.False(t, s.hostStatus().Paused, "expired timed holds must not pause admission")
	require.NoError(t, s.Close())
}

func TestUnpersistedPauseFailsWithoutAcknowledgement(t *testing.T) {
	s, client, _ := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	_, err := s.db.Exec(`DROP TABLE host_policy`)
	require.NoError(t, err)
	_, err = client.HostAction(context.Background(), "pause", "")
	require.ErrorContains(t, err, "500")
	s.mu.Lock()
	defer s.mu.Unlock()
	require.False(t, s.hostPaused, "candidate must not be published after failed write")
	require.ErrorContains(t, s.fatalErr, "persist host pause policy")
}

func TestReturnDuringActualHostPrepareNeverRunsTask(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	// This prepare owns no GUI; the task would create a marker if launched.
	contract := "project = 'hello'\n[prepare]\nrun = 'touch prepare-started; sleep 60'\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte(contract), 0o600))
	var ready atomic.Bool
	ready.Store(true)
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for idle desktop" }, run.ExecuteHost)
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Argv: []string{"/usr/bin/touch", "task-ran"}, Detach: true})
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(root, "prepare-started")); return err == nil }, 3*time.Second, 10*time.Millisecond)
	ready.Store(false)
	_, err := client.HostAction(context.Background(), "return", job.ID)
	require.NoError(t, err)
	waitState(t, client, job.ID, "queued")
	_, err = os.Stat(filepath.Join(root, "task-ran"))
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = client.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
}

func TestReturnWhilePreparingAllowsTakeBackBeforeTask(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	prepare := make(chan struct{})
	var taskStarted atomic.Bool
	var ready atomic.Bool
	ready.Store(true)
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for idle desktop" }, func(ctx context.Context, o run.Options) run.Result {
		<-prepare // simulate a long preparation finishing only after return
		if ctx.Err() == nil {
			taskStarted.Store(true)
		}
		return run.Result{Code: 70, Err: ctx.Err()}
	})
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	waitState(t, client, job.ID, "preparing")
	status, err := client.HostAction(context.Background(), "return", job.ID)
	require.NoError(t, err)
	require.Equal(t, job.ID, status.ReturnJob)
	ready.Store(false)
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.jobs[job.ID].requeueReason == "desktop returned during prepare"
	}, time.Second, 10*time.Millisecond)
	close(prepare)
	waitState(t, client, job.ID, "queued")
	require.False(t, taskStarted.Load(), "return before prepare completion must not launch task")
	_, err = client.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
}
