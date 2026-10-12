package daemon

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

func fixture(t *testing.T, executor func(context.Context, run.Options) run.Result, memory func() (uint64, error)) (*Server, Client, string) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "osd-") // Unix socket paths are limited on macOS.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\n"), 0o600))
	binary := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- test fixture executable in a private directory
	cfg := config.Default()
	cfg.LowMemoryThresholdGB = 8
	s, err := open(cfg, executor, tart.Client{Binary: binary}, memory)
	require.NoError(t, err)
	client := serveFixture(t, s)
	return s, client, root
}

// serveFixture serves an opened server on its isolated socket until cleanup.
func serveFixture(t *testing.T, s *Server) Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; require.NoError(t, s.Close()) })
	socket, err := Socket()
	require.NoError(t, err)
	require.Eventually(t, func() bool { _, err := os.Stat(socket); return err == nil }, time.Second, 10*time.Millisecond)
	client, err := Dial()
	require.NoError(t, err)
	return client
}

func submitFixture(t *testing.T, c Client, root string, req Request) Job {
	t.Helper()
	req.Root = root
	req.Project = "hello"
	if len(req.Argv) == 0 && req.Task == "" {
		req.Argv = []string{"true"}
	}
	j, err := c.Submit(context.Background(), req)
	require.NoError(t, err)
	return j
}

func waitState(t *testing.T, c Client, id, state string) Job {
	t.Helper()
	var result Job
	require.Eventually(t, func() bool {
		var err error
		result, err = c.Job(context.Background(), id)
		return err == nil && result.State == state
	}, 5*time.Second, 20*time.Millisecond)
	return result
}

func TestStartupSweepsOnlyOwnedUnrecordedClone(t *testing.T) {
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	id := "abcdef0123456789"
	dir, err := JobDir(id)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	state, err := StateDir()
	require.NoError(t, err)
	fake := filepath.Join(t.TempDir(), "tart")
	body := "#!/bin/sh\nif [ \"$1\" = list ]; then printf '[{\"Name\":\"job-" + id + "\",\"Running\":true},{\"Name\":\"job-unrelated\",\"Running\":true}]\\n'; else printf '%s %s\\n' \"$1\" \"$2\" >> '" + filepath.Join(state, "sweep.log") + "'; fi\n"
	require.NoError(t, os.WriteFile(fake, []byte(body), 0o600))
	require.NoError(t, os.Chmod(fake, 0o700)) // #nosec G302 -- private test executable
	s, err := open(config.Default(), run.Execute, tart.Client{Binary: fake}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { _ = s.Close() }()
	swept, err := os.ReadFile(filepath.Join(state, "sweep.log"))
	require.NoError(t, err)
	require.Contains(t, string(swept), "stop job-"+id)
	require.Contains(t, string(swept), "delete job-"+id)
	require.NotContains(t, string(swept), "unrelated")
}

func TestQueuedJobsResumeAfterDaemonRestart(t *testing.T) {
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-ctx.Done()
		return run.Result{Code: 70, Err: ctx.Err()}
	}, func() (uint64, error) { return 1 << 30, nil })
	a := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, a.ID, "running")
	b := submitFixture(t, client, root, Request{Detach: true})
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), b.ID)
		return e == nil && j.State == "queued" && j.Reason != ""
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, s.Close())
	binary := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- private test executable
	restored, err := open(config.Default(), func(ctx context.Context, o run.Options) run.Result {
		require.Equal(t, b.ID, o.ID)
		o.Phase("running")
		return run.Result{}
	}, tart.Client{Binary: binary}, func() (uint64, error) { return 1 << 30, nil })
	require.NoError(t, err)
	defer func() { _ = restored.Close() }()
	require.Eventually(t, func() bool {
		restored.mu.Lock()
		defer restored.mu.Unlock()
		return restored.jobs[b.ID].job.State == "done"
	}, 3*time.Second, 20*time.Millisecond)
	require.Equal(t, "failed", restored.jobs[a.ID].job.State)
}

func TestSecondDaemonCannotSweepLiveJobs(t *testing.T) {
	_, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-ctx.Done()
		return run.Result{Code: 70, Err: ctx.Err()}
	}, func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, job.ID, "running")
	_, err := open(config.Default(), run.Execute, tart.Client{Binary: "/nonexistent/tart"}, func() (uint64, error) { return 20 << 30, nil })
	require.ErrorContains(t, err, "daemon already running")
	require.Equal(t, "running", waitState(t, client, job.ID, "running").State)
	_, err = client.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
}

func TestFIFOStreamStdinAndQueueDeadline(t *testing.T) {
	release := make(chan struct{})
	var active, maxActive atomic.Int32
	_, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		n := active.Add(1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		o.Phase("running")
		select {
		case <-release:
		case <-ctx.Done():
			return run.Result{Code: 70, Err: ctx.Err()}
		}
		b, _ := io.ReadAll(o.Stdin)
		_, _ = o.Stdout.Write(append([]byte{'a', 0}, b...))
		_, _ = o.Stderr.Write([]byte{'b', 0, 'c'})
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	a := submitFixture(t, client, root, Request{Detach: true})
	b := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, a.ID, "running")
	waitState(t, client, b.ID, "running")
	c := submitFixture(t, client, root, Request{Detach: true})
	d := submitFixture(t, client, root, Request{Detach: true, Timeout: 300 * time.Millisecond})
	var j Job
	require.Eventually(t, func() bool {
		var err error
		j, err = client.Job(context.Background(), c.ID)
		return err == nil && j.State == "queued" && strings.Contains(j.Reason, "VM slot")
	}, time.Second, 20*time.Millisecond)
	failed := waitState(t, client, d.ID, "failed")
	require.Equal(t, exitcode.QueueTimeout, failed.Code)
	close(release)
	waitState(t, client, a.ID, "done")
	waitState(t, client, b.ID, "done")
	waitState(t, client, c.ID, "done")
	require.EqualValues(t, 2, maxActive.Load())
	var out, stderr bytes.Buffer
	require.NoError(t, client.Logs(context.Background(), a.ID, false, &out, &stderr))
	require.Equal(t, []byte{'a', 0}, out.Bytes())
	require.Equal(t, []byte{'b', 0, 'c'}, stderr.Bytes())
}

func TestAttachedInputCancellationAndCrashRecovery(t *testing.T) {
	started := make(chan string, 2)
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		started <- o.ID
		if o.ID == "" {
			panic("missing daemon id")
		}
		b, _ := io.ReadAll(o.Stdin)
		_, _ = o.Stdout.Write(b)
		<-ctx.Done()
		return run.Result{Code: 70, Err: ctx.Err()}
	}, func() (uint64, error) { return 20 << 30, nil })
	a := submitFixture(t, client, root, Request{Detach: false})
	<-started
	go client.Input(context.Background(), a.ID, strings.NewReader("echo piped\n"))
	waitState(t, client, a.ID, "running")
	var out, stderr bytes.Buffer
	streamDone := make(chan error, 1)
	go func() { _, err := client.Stream(context.Background(), a.ID, &out, &stderr); streamDone <- err }()
	require.Eventually(t, func() bool {
		info, err := os.Stat(filepath.Join(a.Artifacts, "stream.bin"))
		return err == nil && info.Size() > 0
	}, time.Second, 10*time.Millisecond)
	_, err := client.Cancel(context.Background(), a.ID)
	require.NoError(t, err)
	require.NoError(t, <-streamDone)
	require.Equal(t, "echo piped\n", out.String())
	require.Equal(t, "cancelled", waitState(t, client, a.ID, "cancelled").State)
	// Persisted active records never re-run on startup, even if they were in collecting.
	b := submitFixture(t, client, root, Request{Detach: true})
	<-started
	// Simulate an abrupt process loss with its last durable state still active.
	require.NoError(t, s.Close())
	dir, err := StateDir()
	require.NoError(t, err)
	db, err := sql.Open("sqlite", filepath.Join(dir, "jobs.sqlite"))
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE jobs SET state='collecting',reason='',code=0 WHERE id=?`, b.ID)
	require.NoError(t, err)
	require.NoError(t, db.Close())
	fake := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[{\"Name\":\"job-"+b.ID+"\",\"Running\":true}]\\n'; else printf '%s %s\\n' \"$1\" \"$2\" >> '"+filepath.Join(dir, "sweep.log")+"'; fi\n"), 0o600))
	require.NoError(t, os.Chmod(fake, 0o700)) // #nosec G302 -- test fixture executable in a private directory
	reopened, e := open(config.Default(), func(context.Context, run.Options) run.Result { panic("orphan retried") }, tart.Client{Binary: fake}, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, e)
	defer func() { _ = reopened.Close() }()
	orphan := reopened.jobs[b.ID].job
	require.Equal(t, "failed", orphan.State)
	require.Contains(t, orphan.Reason, "orphaned")
	swept, e := os.ReadFile(filepath.Join(dir, "sweep.log"))
	require.NoError(t, e)
	require.Contains(t, string(swept), "stop job-"+b.ID)
	require.Contains(t, string(swept), "delete job-"+b.ID)
}

func TestHostGatePauseAndTakeBackRequeue(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { panic("host used VM executor") }, func() (uint64, error) { return 20 << 30, nil })
	host := submitFixture(t, client, root, Request{Detach: true, Lane: "host"})
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), host.ID)
		return e == nil && strings.Contains(j.Reason, "host lane unavailable")
	}, time.Second, 20*time.Millisecond)
	var ready atomic.Bool
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for countdown" }, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-ctx.Done()
		return run.Result{Code: 70, Err: ctx.Err()}
	})
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), host.ID)
		return e == nil && j.Reason == "waiting for countdown"
	}, time.Second, 20*time.Millisecond)
	require.NoError(t, s.PauseHost())
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), host.ID)
		return e == nil && j.Reason == "host lane paused"
	}, time.Second, 20*time.Millisecond)
	require.NoError(t, s.ResumeHost())
	ready.Store(true)
	waitState(t, client, host.ID, "running")
	ready.Store(false)
	require.NoError(t, s.RequeueHost(host.ID, "user took back desktop"))
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), host.ID)
		return e == nil && j.State == "queued" && j.Reason == "waiting for countdown"
	}, time.Second, 20*time.Millisecond)
	_, err := client.Cancel(context.Background(), host.ID)
	require.NoError(t, err)
}

func TestLowMemoryAndExclusive(t *testing.T) {
	hold := make(chan struct{})
	var active, maxActive atomic.Int32
	_, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		n := active.Add(1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		defer active.Add(-1)
		select {
		case <-hold:
			return run.Result{}
		case <-ctx.Done():
			return run.Result{Code: 70, Err: ctx.Err()}
		}
	}, func() (uint64, error) { return 1 << 30, errors.New("probe failed") })
	require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\nexclusive = true\n"), 0o600))
	a := submitFixture(t, client, root, Request{Detach: true})
	waitState(t, client, a.ID, "running")
	b := submitFixture(t, client, root, Request{Detach: true})
	require.Eventually(t, func() bool {
		j, e := client.Job(context.Background(), b.ID)
		return e == nil && j.State == "queued" && strings.Contains(j.Reason, "VM slot")
	}, time.Second, 20*time.Millisecond)
	close(hold)
	waitState(t, client, b.ID, "done")
	require.EqualValues(t, 1, maxActive.Load())
}
