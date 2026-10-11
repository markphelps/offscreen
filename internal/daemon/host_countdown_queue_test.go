package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func TestLaterHostJobDoesNotResetEarlierCountdown(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.cfg.IdleThreshold = 30 * time.Second
	s.ConfigureHost(s.hostGateSnapshot, func(ctx context.Context, options run.Options) run.Result {
		options.Phase("running")
		<-ctx.Done()
		return run.Result{Code: 70, Err: ctx.Err()}
	})
	first := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	second := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	s.mu.Lock()
	s.hostControl.snapshot = HostSnapshot{IdleSeconds: 60, Unlocked: true, DisplayAwake: true, CameraIdle: true, MicrophoneIdle: true, InputMonitoring: true, EventTapReady: true}
	s.hostControl.seen = time.Now()
	s.mu.Unlock()
	s.admit()
	require.Equal(t, first.ID, s.hostStatus().CountdownJob,
		"a later queued job must not erase the first job's countdown")
	s.mu.Lock()
	s.hostControl.countdownAt = time.Now().Add(-11 * time.Second)
	s.mu.Unlock()
	s.admit()
	waitState(t, client, first.ID, "running")
	queued, err := client.Job(context.Background(), second.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", queued.State)
	_, err = client.Cancel(context.Background(), second.ID)
	require.NoError(t, err)
	_, err = client.Cancel(context.Background(), first.ID)
	require.NoError(t, err)
}
