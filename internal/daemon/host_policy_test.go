package daemon

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func TestGraceExpiryRequeuesOnlyWhileDesktopClaimed(t *testing.T) {
	for _, idle := range []float64{0, 60} {
		t.Run((time.Duration(idle) * time.Second).String(), func(t *testing.T) {
			s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
			s.cfg.IdleThreshold = 30 * time.Second
			release := make(chan struct{})
			var ready atomic.Bool
			ready.Store(true)
			s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for idle desktop" }, func(ctx context.Context, o run.Options) run.Result {
				o.Phase("running")
				select {
				case <-ctx.Done():
					return run.Result{Code: 70, Err: ctx.Err()}
				case <-release:
					return run.Result{}
				}
			})
			job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
			waitState(t, client, job.ID, "running")
			_, err := client.HostAction(context.Background(), "return", job.ID)
			require.NoError(t, err)
			ready.Store(false)
			s.mu.Lock()
			s.hostControl.snapshot = HostSnapshot{IdleSeconds: idle, Unlocked: true, DisplayAwake: true, InputMonitoring: true, EventTapReady: true, CameraIdle: true, MicrophoneIdle: true}
			s.hostControl.seen = time.Now()
			s.hostControl.returnUntil = time.Now().Add(-time.Second)
			s.expireHostReturn(time.Now())
			requeued := s.jobs[job.ID].requeueReason
			s.mu.Unlock()
			if idle < 30 {
				require.Equal(t, "desktop taken back after grace", requeued)
				waitState(t, client, job.ID, "queued")
			} else {
				require.Empty(t, requeued)
				require.Equal(t, "running", waitState(t, client, job.ID, "running").State)
			}
			close(release)
			if idle >= 30 {
				waitState(t, client, job.ID, "done")
			} else {
				_, err = client.Cancel(context.Background(), job.ID)
				require.NoError(t, err)
			}
		})
	}
}

func TestExclusiveBlockPrecedesHostGate(t *testing.T) {
	hold := make(chan struct{})
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		select {
		case <-hold:
			return run.Result{}
		case <-ctx.Done():
			return run.Result{Code: 70, Err: ctx.Err()}
		}
	}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\nexclusive = true\n"), 0o600))
	vm := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, vm.ID, "running")
	var gateCalls atomic.Int32
	s.ConfigureHost(func(context.Context, Job) (bool, string) { gateCalls.Add(1); return false, "countdown started" }, func(context.Context, run.Options) run.Result { return run.Result{} })
	host := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	require.Eventually(t, func() bool {
		j, err := client.Job(context.Background(), host.ID)
		return err == nil && j.Reason == "waiting for exclusive project"
	}, time.Second, 20*time.Millisecond)
	require.EqualValues(t, 0, gateCalls.Load())
	_, err := client.Cancel(context.Background(), host.ID)
	require.NoError(t, err)
	close(hold)
	waitState(t, client, vm.ID, "done")
}
