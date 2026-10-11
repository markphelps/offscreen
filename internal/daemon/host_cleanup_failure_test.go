package daemon

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func TestUnconfirmedCleanupStopsAdmissionInsteadOfRequeue(t *testing.T) {
	for _, requeue := range []bool{false, true} {
		t.Run(fmt.Sprintf("requeue=%t", requeue), func(t *testing.T) {
			s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
			release := make(chan struct{})
			var starts atomic.Int32
			s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, func(_ context.Context, options run.Options) run.Result {
				starts.Add(1)
				options.Phase("running")
				<-release
				return run.Result{Code: 70, Err: fmt.Errorf("lost guardian status: %w", run.ErrCleanupUnconfirmed)}
			})
			first := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
			waitState(t, client, first.ID, "running")
			second := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
			if requeue {
				require.NoError(t, s.RequeueHost(first.ID, "desktop taken back"))
			}
			close(release)
			require.Eventually(t, func() bool { return s.ctx.Err() != nil }, time.Second, 10*time.Millisecond,
				"unconfirmed cleanup must stop scheduling before the host slot is reused")
			s.mu.Lock()
			defer s.mu.Unlock()
			require.ErrorIs(t, s.fatalErr, run.ErrCleanupUnconfirmed)
			require.Equal(t, "failed", s.jobs[first.ID].job.State)
			require.Equal(t, exitcode.RunnerFailure, s.jobs[first.ID].job.Code)
			require.Equal(t, "queued", s.jobs[second.ID].job.State)
			require.EqualValues(t, 1, starts.Load())
		})
	}
}
