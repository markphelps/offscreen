package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func snapshotTest(t *testing.T, client Client, s HostSnapshot) {
	t.Helper()
	body, err := json.Marshal(s)
	require.NoError(t, err)
	resp, err := client.call(context.Background(), http.MethodPost, "/host/snapshot", bytes.NewReader(body))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var status HostStatus
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	require.GreaterOrEqual(t, status.Queued, 0)
}

func TestHostGatePredicatesCountdownInputNotNowPauseResume(t *testing.T) {
	var vmProbes atomic.Int32
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { vmProbes.Add(1); return 20 << 30, nil })
	s.cfg.IdleThreshold = 30 * time.Second
	s.ConfigureHostController()
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Argv: []string{"/bin/echo", "hello"}, Detach: true})
	require.Eventually(t, func() bool { return s.hostStatus().Queued == 1 }, time.Second, 10*time.Millisecond)
	require.Equal(t, "waiting for menu bar helper (signal unavailable)", gateReason(s, job))
	good := HostSnapshot{IdleSeconds: 35, Unlocked: true, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}
	cases := []struct {
		name     string
		snapshot HostSnapshot
		reason   string
	}{
		{"permission", HostSnapshot{IdleSeconds: 35}, "waiting for Input Monitoring permission"},
		{"tap", HostSnapshot{IdleSeconds: 35, InputMonitoring: true}, "waiting for event tap"},
		{"locked", HostSnapshot{IdleSeconds: 35, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}, "waiting for unlocked session"},
		{"display", HostSnapshot{IdleSeconds: 35, Unlocked: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}, "waiting for awake display"},
		{"camera", HostSnapshot{IdleSeconds: 35, Unlocked: true, DisplayAwake: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}, "waiting for camera/microphone to be idle"},
		{"microphone", HostSnapshot{IdleSeconds: 35, Unlocked: true, DisplayAwake: true, CameraIdle: true, InputMonitoring: true, EventTapReady: true}, "waiting for camera/microphone to be idle"},
		{"idle", HostSnapshot{IdleSeconds: 2, Unlocked: true, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}, "waiting for idle desktop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshotTest(t, client, tc.snapshot)
			require.Equal(t, tc.reason, gateReason(s, job))
		})
	}
	snapshotTest(t, client, good)
	require.Contains(t, gateReason(s, job), "10 seconds")
	s.mu.Lock()
	require.Equal(t, job.ID, s.hostControl.countdownID)
	s.hostControl.countdownAt = time.Now().Add(-9 * time.Second)
	s.mu.Unlock()
	// Physical click resets HIDIdleTime before Not now reaches the HTTP handler.
	good.IdleSeconds = 0
	snapshotTest(t, client, good)
	require.Equal(t, "waiting for idle desktop", gateReason(s, job))
	require.Equal(t, job.ID, s.hostStatus().NotNowJob)
	s.mu.Lock()
	s.hostControl.countdownAt = time.Time{}
	s.mu.Unlock()
	// No three-second race: the job-bound action remains available after
	// HID idle resets while the pointer moves to the control.
	status, err := client.HostAction(context.Background(), "not-now", job.ID)
	require.NoError(t, err)
	require.True(t, status.Paused)
	var persistedUntil string
	require.NoError(t, s.db.QueryRow(`SELECT paused_until FROM host_policy WHERE id = 1`).Scan(&persistedUntil))
	require.NotEqual(t, time.Time{}.UTC().Format(time.RFC3339Nano), persistedUntil)
	require.Eventually(t, func() bool {
		current, err := client.Job(context.Background(), job.ID)
		return err == nil && current.Reason == "host lane paused"
	}, time.Second, 20*time.Millisecond)
	_, err = client.HostAction(context.Background(), "resume", "")
	require.NoError(t, err)
	snapshotTest(t, client, HostSnapshot{IdleSeconds: 35, Unlocked: true, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true})
	require.Contains(t, gateReason(s, job), "10 seconds")
	require.EqualValues(t, 0, vmProbes.Load(), "host-only waiting never probes VM memory")
	_, err = client.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
}

func gateReason(s *Server, job Job) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, reason := s.hostGateSnapshot(context.Background(), job)
	return reason
}

func TestHostReturnGraceAndJobBoundTakeBackCleanup(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.cfg.IdleThreshold = 30 * time.Second
	s.cfg.Grace = 60 * time.Second
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var ready atomic.Bool
	ready.Store(true)
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for idle desktop" }, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		entered <- struct{}{}
		<-ctx.Done()
		<-release
		return run.Result{Code: 70, Err: ctx.Err()}
	})
	a := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	<-entered
	b := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	_, err := client.HostAction(context.Background(), "return", b.ID)
	require.ErrorContains(t, err, "409")
	status, err := client.HostAction(context.Background(), "return", a.ID)
	require.NoError(t, err)
	require.Equal(t, a.ID, status.ReturnJob)
	require.Greater(t, status.GraceSeconds, 50)
	// Return detection alone does not cancel before the full grace period.
	require.Equal(t, "running", waitState(t, client, a.ID, "running").State)
	_, err = client.HostAction(context.Background(), "take-back", b.ID)
	require.ErrorContains(t, err, "409")
	ready.Store(false)
	completed := make(chan error, 1)
	go func() { _, err := client.HostAction(context.Background(), "take-back", a.ID); completed <- err }()
	require.Eventually(t, func() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.jobs[a.ID].requeueReason != "" }, time.Second, 10*time.Millisecond)
	select {
	case err := <-completed:
		t.Fatalf("returned before cleanup: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-completed)
	require.Eventually(t, func() bool { j, e := client.Job(context.Background(), a.ID); return e == nil && j.State == "queued" }, time.Second, 10*time.Millisecond)
	s.mu.Lock()
	require.Equal(t, a.ID, s.order[0])
	s.mu.Unlock()
	// Explicit cancellation wins over a pending requeue.
	_, err = client.Cancel(context.Background(), a.ID)
	require.NoError(t, err)
	_, err = client.Cancel(context.Background(), b.ID)
	require.NoError(t, err)
}

func TestIdleSchedulerDoesNotProbeVMMemory(t *testing.T) {
	var calls atomic.Int32
	s, _, _ := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { calls.Add(1); return 20 << 30, nil })
	time.Sleep(450 * time.Millisecond)
	require.Empty(t, s.order)
	require.EqualValues(t, 0, calls.Load())
}
