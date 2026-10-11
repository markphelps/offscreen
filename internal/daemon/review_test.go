package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

func TestRestartMissingQueuedContractFailsOnlyThatJob(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	missing := submitFixture(t, client, root, Request{Detach: true, Lane: "host"})
	waitState(t, client, missing.ID, "queued")
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, ".offscreen.toml"), []byte("project = 'hello'\n"), 0o600))
	resumable, err := client.Submit(context.Background(), Request{Root: other, Project: "hello", Lane: "host", Argv: []string{"true"}, Detach: true})
	require.NoError(t, err)
	require.NoError(t, s.Close())
	socket, err := Socket()
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return os.IsNotExist(err) }, time.Second, 10*time.Millisecond)
	require.NoError(t, os.Remove(filepath.Join(root, ".offscreen.toml")))
	fake := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o700)) // #nosec G306 -- test executable
	reopened, err := open(config.Default(), func(context.Context, run.Options) run.Result { return run.Result{} }, tart.Client{Binary: fake}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	failed := reopened.jobs[missing.ID].job
	require.Equal(t, "failed", failed.State)
	require.Equal(t, exitcode.RunnerFailure, failed.Code)
	require.Contains(t, failed.Reason, "queued project contract unavailable")
	require.Equal(t, "queued", reopened.jobs[resumable.ID].job.State)
	var state, reason string
	require.NoError(t, reopened.db.QueryRow(`SELECT state,reason FROM jobs WHERE id=?`, missing.ID).Scan(&state, &reason))
	require.Equal(t, failed.State, state)
	require.Equal(t, failed.Reason, reason)
	ctx, stop := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() { serveDone <- reopened.Serve(ctx) }()
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	restartedClient, err := Dial()
	require.NoError(t, err)
	served, err := restartedClient.Job(ctx, missing.ID)
	require.NoError(t, err)
	require.Equal(t, failed.Reason, served.Reason)
	stop()
	require.NoError(t, <-serveDone)
}

func TestPersistenceFailureStopsServiceBeforeExecutionOrFalseCompletion(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "osd-failure-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(state) }()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\n"), 0o600))
	fake := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o700)) // #nosec G306 -- test executable
	var executed atomic.Int32
	s, err := open(config.Default(), func(context.Context, run.Options) run.Result {
		executed.Add(1)
		return run.Result{}
	}, tart.Client{Binary: fake}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { require.NoError(t, s.Close()) }()
	ctx := t.Context()
	serveErr := make(chan error, 1)
	go func() { serveErr <- s.Serve(ctx) }()
	socket, err := Socket()
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	client, err := Dial()
	require.NoError(t, err)
	// A host-lane submission stays queued until a host gate is configured.
	job, err := client.Submit(ctx, Request{Root: root, Project: "hello", Lane: "host", Argv: []string{"true"}, Detach: true})
	require.NoError(t, err)
	_, err = s.db.Exec(`CREATE TRIGGER fail_transition BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'store unavailable'); END`)
	require.NoError(t, err)
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, func(context.Context, run.Options) run.Result {
		executed.Add(1)
		return run.Result{}
	})
	select {
	case err := <-serveErr:
		require.ErrorContains(t, err, "store unavailable")
	case <-time.After(3 * time.Second):
		t.Fatal("Serve continued after store failure")
	}
	require.EqualValues(t, 0, executed.Load())
	require.Equal(t, "queued", s.jobs[job.ID].job.State)
	var persisted string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&persisted))
	require.Equal(t, "queued", persisted)
}

func TestTerminalPersistenceFailureDoesNotPublishCompletion(t *testing.T) {
	release := make(chan struct{})
	s, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-release
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, job.ID, "running")
	_, err := s.db.Exec(`CREATE TRIGGER fail_terminal BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'write denied'); END`)
	require.NoError(t, err)
	close(release)
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.fatalErr != nil
	}, 3*time.Second, 10*time.Millisecond)
	s.mu.Lock()
	require.Equal(t, "running", s.jobs[job.ID].job.State)
	s.mu.Unlock()
	var persisted string
	require.NoError(t, s.db.QueryRow(`SELECT state FROM jobs WHERE id=?`, job.ID).Scan(&persisted))
	require.Equal(t, "running", persisted)
}

func TestExclusiveAcrossRootsAndLanesDuringCleanup(t *testing.T) {
	for _, incomingExclusive := range []bool{false, true} {
		for _, activeHost := range []bool{false, true} {
			t.Run(fmt.Sprintf("incoming=%t/activeHost=%t", incomingExclusive, activeHost), func(t *testing.T) {
				cleaning := make(chan struct{})
				started := make(chan string, 2)
				exec := func(ctx context.Context, o run.Options) run.Result {
					o.Phase("running")
					started <- o.ID
					<-ctx.Done()
					<-cleaning // model VM destruction or host teardown
					return run.Result{Code: 70, Err: ctx.Err()}
				}
				s, client, root := fixture(t, exec, func() (uint64, error) { return 20 << 30, nil })
				s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, exec)
				firstExclusive := !incomingExclusive
				if firstExclusive {
					require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\nexclusive = true\n"), 0o600))
				}
				lane := "vm"
				if activeHost {
					lane = "host"
				}
				first := submitFixture(t, client, root, Request{Detach: true, Lane: project.Lane(lane)})
				waitState(t, client, first.ID, "running")
				otherRoot := t.TempDir()
				contract := "project = 'hello'\n"
				if incomingExclusive {
					contract += "exclusive = true\n"
				}
				require.NoError(t, os.WriteFile(filepath.Join(otherRoot, ".offscreen.toml"), []byte(contract), 0o600))
				incomingLane := "host"
				if activeHost {
					incomingLane = "vm"
				}
				second, err := client.Submit(context.Background(), Request{Root: otherRoot, Project: "hello", Lane: project.Lane(incomingLane), Argv: []string{"true"}, Detach: true})
				require.NoError(t, err)
				require.Eventually(t, func() bool {
					j, e := client.Job(context.Background(), second.ID)
					return e == nil && j.State == "queued" && strings.Contains(j.Reason, "exclusive")
				}, time.Second, 20*time.Millisecond)
				// Cancellation is visible before the old worker finishes cleanup.
				cancelDone := make(chan error, 1)
				go func() { _, err := client.Cancel(context.Background(), first.ID); cancelDone <- err }()
				waitState(t, client, first.ID, "cancelled")
				select {
				case <-started:
				default:
				}
				time.Sleep(250 * time.Millisecond)
				require.Equal(t, "queued", waitState(t, client, second.ID, "queued").State)
				close(cleaning)
				require.NoError(t, <-cancelDone)
				waitState(t, client, second.ID, "running")
				_, err = client.Cancel(context.Background(), second.ID)
				require.NoError(t, err)
			})
		}
	}
}

func TestCancelledStreamWaitsForCleanupAndDeliversTail(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	_, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		close(started)
		<-ctx.Done()
		<-gate
		_, _ = o.Stdout.Write([]byte("tail after cancel\n"))
		return run.Result{Code: 70, Err: ctx.Err()}
	}, func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	<-started
	var out bytes.Buffer
	streamDone := make(chan error, 1)
	go func() {
		_, err := client.Stream(context.Background(), job.ID, &out, &bytes.Buffer{})
		streamDone <- err
	}()
	cancelDone := make(chan error, 1)
	go func() { _, err := client.Cancel(context.Background(), job.ID); cancelDone <- err }()
	waitState(t, client, job.ID, "cancelled")
	select {
	case err := <-streamDone:
		t.Fatalf("stream finished before cleanup: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	close(gate)
	require.NoError(t, <-cancelDone)
	require.NoError(t, <-streamDone)
	require.Equal(t, "tail after cancel\n", out.String())
}

func TestExplicitCancelWinsOverHostRequeueDuringCleanup(t *testing.T) {
	cleanup := make(chan struct{})
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-ctx.Done()
		<-cleanup
		return run.Result{Code: exitcode.RunnerFailure, Err: ctx.Err()}
	})
	job := submitFixture(t, client, root, Request{Lane: project.LaneHost, Detach: true})
	waitState(t, client, job.ID, "running")
	require.NoError(t, s.RequeueHost(job.ID, "desktop taken back"))
	cancelDone := make(chan error, 1)
	go func() {
		_, err := client.Cancel(context.Background(), job.ID)
		cancelDone <- err
	}()
	waitState(t, client, job.ID, "cancelled")
	close(cleanup)
	require.NoError(t, <-cancelDone)
	final, err := client.Job(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, "cancelled", final.State)
	require.Never(t, func() bool {
		current, err := client.Job(context.Background(), job.ID)
		return err != nil || current.State != "cancelled"
	}, 500*time.Millisecond, 20*time.Millisecond)
}

func TestTruncatedStreamAndMalformedSubmissionAreTransportErrors(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/submit":
			_, _ = w.Write([]byte(`{"id":`))
		case "/jobs/job/stream":
			w.Header().Set("Content-Length", "20")
			_, _ = w.Write([]byte{'o', 0, 0})
		case "/jobs/job":
			_ = json.NewEncoder(w).Encode(Job{ID: "job", State: "done", Code: 3})
		}
	}))
	defer fake.Close()
	client := Client{http: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", fake.Listener.Addr().String())
	}}}}
	_, err := client.Submit(context.Background(), Request{})
	require.ErrorContains(t, err, "decode submission response")
	_, err = client.Stream(context.Background(), "job", &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "read job stream")
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
}
