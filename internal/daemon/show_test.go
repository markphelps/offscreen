package daemon

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func TestShowReachesVMExecutor(t *testing.T) {
	shown := make(chan bool, 1)
	_, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		shown <- o.Show
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	submitFixture(t, client, root, Request{Detach: true, Show: true})
	require.True(t, <-shown)
}

func TestShowIsRejectedForHostJobs(t *testing.T) {
	_, client, root := fixture(t, func(context.Context, run.Options) run.Result {
		t.Error("a rejected job must not run")
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	_, err := client.Submit(context.Background(), Request{Root: root, Project: "hello", Argv: []string{"true"}, Lane: project.LaneHost, Show: true, Detach: true})
	require.ErrorContains(t, err, "--show applies only to VM jobs")
}
