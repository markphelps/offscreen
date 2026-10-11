package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

func TestHostNormalExitUnblocksLargeAttachedInput(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, run.ExecuteHost)
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Argv: []string{"/usr/bin/true"}})
	// Release a blocked request even when the pre-fix assertion fails.
	defer func() { _ = s.jobs[job.ID].inputR.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inputDone := make(chan struct{})
	go func() {
		client.Input(ctx, job.ID, strings.NewReader(strings.Repeat("x", 8<<20)))
		close(inputDone)
	}()
	waitState(t, client, job.ID, "done")
	select {
	case <-inputDone:
	case <-time.After(time.Second):
		t.Fatal("normal host completion stranded its attached input request")
	}
}

func TestQueuedDeadlineUnblocksAttachedInput(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return false, "waiting for idle desktop" }, run.ExecuteHost)
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Timeout: 100 * time.Millisecond})
	defer func() { _ = s.jobs[job.ID].inputR.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	inputDone := make(chan struct{})
	go func() {
		client.Input(ctx, job.ID, strings.NewReader(strings.Repeat("x", 8<<20)))
		close(inputDone)
	}()
	waitState(t, client, job.ID, "failed")
	select {
	case <-inputDone:
	case <-time.After(time.Second):
		t.Fatal("queue timeout stranded its attached input request")
	}
}
