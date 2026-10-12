package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
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

const secretKey = "OFFSCREEN_CALLER_SECRET"

// envRecorder captures the caller snapshot each executor call received.
type envRecorder struct {
	mu   sync.Mutex
	seen map[string][]map[string]string
}

func (r *envRecorder) record(o run.Options) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seen == nil {
		r.seen = make(map[string][]map[string]string)
	}
	r.seen[o.ID] = append(r.seen[o.ID], o.HostEnv)
}

func (r *envRecorder) calls(id string) []map[string]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.seen[id]
}

func storedRequest(t *testing.T, s *Server, id string) string {
	t.Helper()
	var stored string
	require.NoError(t, s.db.QueryRow(`SELECT request FROM jobs WHERE id=?`, id).Scan(&stored))
	return stored
}

func requireScrubbed(t *testing.T, s *Server, id, sentinel string) {
	t.Helper()
	stored := storedRequest(t, s, id)
	require.NotContains(t, stored, sentinel)
	require.NotContains(t, stored, "host_env")
	var request Request
	require.NoError(t, json.Unmarshal([]byte(stored), &request))
	require.Nil(t, request.HostEnv)
	s.mu.Lock()
	defer s.mu.Unlock()
	require.True(t, Terminal(s.jobs[id].job.State))
	require.Nil(t, s.jobs[id].req.HostEnv)
}

func requireSnapshot(t *testing.T, s *Server, id, sentinel string) {
	t.Helper()
	var request Request
	require.NoError(t, json.Unmarshal([]byte(storedRequest(t, s, id)), &request))
	require.Equal(t, sentinel, request.HostEnv[secretKey])
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Equal(t, sentinel, s.jobs[id].req.HostEnv[secretKey])
}

func emptyTart(t *testing.T) tart.Client {
	t.Helper()
	fake := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(fake, []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o700)) // #nosec G306 -- test executable
	return tart.Client{Binary: fake}
}

func insertRecord(t *testing.T, s *Server, id, root, lane, state string, request Request) {
	t.Helper()
	b, err := json.Marshal(request)
	require.NoError(t, err)
	now := time.Now().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO jobs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "hello", root, "", lane, state, 0, t.TempDir(), "", now, now, time.Time{}.Format(time.RFC3339Nano), string(b), 0)
	require.NoError(t, err)
}

func TestTerminalJobsScrubCallerEnvironment(t *testing.T) {
	var rec envRecorder
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		rec.record(o)
		switch o.Argv[0] {
		case "fail":
			return run.Result{Code: 9, Err: errors.New("task failed")}
		case "block":
			o.Phase("running")
			<-ctx.Done()
			return run.Result{Code: exitcode.RunnerFailure, Err: ctx.Err()}
		}
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	env := func(sentinel string) map[string]string { return map[string]string{secretKey: sentinel} }

	done := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}, HostEnv: env("sentinel-done")})
	waitState(t, client, done.ID, "done")
	requireScrubbed(t, s, done.ID, "sentinel-done")

	failed := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"fail"}, HostEnv: env("sentinel-failed")})
	require.Equal(t, 9, waitState(t, client, failed.ID, "failed").Code)
	requireScrubbed(t, s, failed.ID, "sentinel-failed")

	cancelled := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"block"}, HostEnv: env("sentinel-cancelled")})
	waitState(t, client, cancelled.ID, "running")
	requireSnapshot(t, s, cancelled.ID, "sentinel-cancelled") // active jobs keep it
	_, err := client.Cancel(context.Background(), cancelled.ID)
	require.NoError(t, err)
	requireScrubbed(t, s, cancelled.ID, "sentinel-cancelled")

	// A host job with no gate stays queued until its deadline expires.
	timedOut := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost, Timeout: 300 * time.Millisecond, HostEnv: env("sentinel-timeout")})
	requireSnapshot(t, s, timedOut.ID, "sentinel-timeout")
	require.Equal(t, exitcode.QueueTimeout, waitState(t, client, timedOut.ID, "failed").Code)
	requireScrubbed(t, s, timedOut.ID, "sentinel-timeout")

	// Executors used their own snapshot; scrubbing never mutated those maps.
	for id, sentinel := range map[string]string{done.ID: "sentinel-done", failed.ID: "sentinel-failed", cancelled.ID: "sentinel-cancelled"} {
		calls := rec.calls(id)
		require.Len(t, calls, 1)
		require.Equal(t, sentinel, calls[0][secretKey])
	}
	require.Empty(t, rec.calls(timedOut.ID))
}

func TestHostExecutionUsesCallerSnapshotThenScrubs(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	tools, share := t.TempDir(), t.TempDir()
	// The probe exists only on the caller's PATH, not the daemon's.
	probe := "#!/bin/sh\nprintf '%s' \"$" + secretKey + "\" > task-env\n"
	require.NoError(t, os.WriteFile(filepath.Join(tools, "offscreen-env-probe"), []byte(probe), 0o700)) // #nosec G306 -- private test executable
	contract := "project = 'hello'\n[prepare]\nrun = 'printf \"%s\" \"$" + secretKey + "\" > prepare-env'\nshare = ['$OFFSCREEN_CALLER_SHARE/App.app']\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte(contract), 0o600))
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, run.ExecuteHost)
	const sentinel = "sentinel-host-execution"
	job := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost, Argv: []string{"offscreen-env-probe"}, HostEnv: map[string]string{
		"PATH":                   tools + ":/usr/bin:/bin",
		secretKey:                sentinel,
		"OFFSCREEN_CALLER_SHARE": share,
	}})
	require.Equal(t, 0, waitState(t, client, job.ID, "done").Code)
	for _, name := range []string{"prepare-env", "task-env"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		require.NoError(t, err)
		require.Equal(t, sentinel, string(got), name)
	}
	requireScrubbed(t, s, job.ID, sentinel)
	require.NotContains(t, storedRequest(t, s, job.ID), share)
}

func TestHostRequeueKeepsCallerSnapshot(t *testing.T) {
	var rec envRecorder
	var ready atomic.Bool
	ready.Store(true)
	entered := make(chan struct{}, 1)
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return ready.Load(), "waiting for idle desktop" }, func(ctx context.Context, o run.Options) run.Result {
		rec.record(o)
		if len(rec.calls(o.ID)) == 1 {
			o.Phase("running")
			entered <- struct{}{}
			<-ctx.Done()
			return run.Result{Code: exitcode.RunnerFailure, Err: ctx.Err()}
		}
		return run.Result{}
	})
	const sentinel = "sentinel-requeue"
	job := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost, HostEnv: map[string]string{secretKey: sentinel}})
	<-entered
	ready.Store(false)
	require.NoError(t, s.RequeueHost(job.ID, "desktop returned"))
	waitState(t, client, job.ID, "queued")
	requireSnapshot(t, s, job.ID, sentinel)
	ready.Store(true)
	waitState(t, client, job.ID, "done")
	calls := rec.calls(job.ID)
	require.Len(t, calls, 2)
	for _, env := range calls {
		require.Equal(t, sentinel, env[secretKey])
	}
	requireScrubbed(t, s, job.ID, sentinel)
}

func TestRestartKeepsQueuedSnapshotsAndScrubsTerminalRecords(t *testing.T) {
	s, client, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	// Without a host gate these stay queued in the first daemon.
	queued := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost, HostEnv: map[string]string{secretKey: "sentinel-queued"}})
	legacy := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost})
	attached := submitFixture(t, client, root, Request{Lane: project.LaneHost, HostEnv: map[string]string{secretKey: "sentinel-attached"}})
	requireSnapshot(t, s, queued.ID, "sentinel-queued")
	require.NotContains(t, storedRequest(t, s, legacy.ID), "host_env")
	// Records from a crashed daemon and from before terminal scrubbing existed.
	insertRecord(t, s, "0000000000000001", root, "vm", "running", Request{Root: root, Project: "hello", Argv: []string{"true"}, HostEnv: map[string]string{secretKey: "sentinel-orphan"}})
	insertRecord(t, s, "0000000000000002", root, "vm", "done", Request{Root: root, Project: "hello", Argv: []string{"true"}, HostEnv: map[string]string{secretKey: "sentinel-old-terminal"}})
	require.NoError(t, s.Close())

	var rec envRecorder
	reopened, err := open(config.Default(), func(context.Context, run.Options) run.Result { return run.Result{} }, emptyTart(t), func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	requireScrubbed(t, reopened, "0000000000000001", "sentinel-orphan")
	requireScrubbed(t, reopened, "0000000000000002", "sentinel-old-terminal")
	requireScrubbed(t, reopened, attached.ID, "sentinel-attached")
	requireSnapshot(t, reopened, queued.ID, "sentinel-queued")

	reopened.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, func(_ context.Context, o run.Options) run.Result {
		rec.record(o)
		return run.Result{}
	})
	for _, id := range []string{queued.ID, legacy.ID} {
		require.Eventually(t, func() bool {
			reopened.mu.Lock()
			defer reopened.mu.Unlock()
			return reopened.jobs[id].job.State == "done"
		}, 3*time.Second, 20*time.Millisecond)
	}
	require.Equal(t, "sentinel-queued", rec.calls(queued.ID)[0][secretKey])
	require.Nil(t, rec.calls(legacy.ID)[0], "legacy requests keep the daemon-environment fallback")
	requireScrubbed(t, reopened, queued.ID, "sentinel-queued")
}

func TestTerminalScrubPersistenceFailureFailsClosed(t *testing.T) {
	release := make(chan struct{})
	s, client, root := fixture(t, func(_ context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-release
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	const sentinel = "sentinel-unpersisted"
	job := submitFixture(t, client, root, Request{Detach: true, HostEnv: map[string]string{secretKey: sentinel}})
	waitState(t, client, job.ID, "running")
	_, err := s.db.Exec(`CREATE TRIGGER fail_terminal BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'write denied'); END`)
	require.NoError(t, err)
	close(release)
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.fatalErr != nil
	}, 3*time.Second, 10*time.Millisecond)
	// Neither completion nor a scrubbed snapshot is published.
	requireSnapshot(t, s, job.ID, sentinel)
	s.mu.Lock()
	require.Equal(t, "running", s.jobs[job.ID].job.State)
	s.mu.Unlock()
}

func TestStartupScrubPersistenceFailureFailsClosed(t *testing.T) {
	s, _, root := fixture(t, func(context.Context, run.Options) run.Result { return run.Result{} }, func() (uint64, error) { return 20 << 30, nil })
	insertRecord(t, s, "0000000000000003", root, "vm", "failed", Request{Root: root, Project: "hello", Argv: []string{"true"}, HostEnv: map[string]string{secretKey: "sentinel-locked"}})
	_, err := s.db.Exec(`CREATE TRIGGER fail_scrub BEFORE UPDATE ON jobs BEGIN SELECT RAISE(FAIL, 'write denied'); END`)
	require.NoError(t, err)
	require.NoError(t, s.Close())
	_, err = open(config.Default(), func(context.Context, run.Options) run.Result { return run.Result{} }, emptyTart(t), func() (uint64, error) { return 20 << 30, nil })
	require.ErrorContains(t, err, "scrub caller environment from terminal job 0000000000000003")
}

func TestKeptVMCancelWorksAfterScrub(t *testing.T) {
	s, client, root := fixture(t, keptExecutor, func() (uint64, error) { return 20 << 30, nil })
	dir := retainedTart(t, s)
	const sentinel = "sentinel-kept"
	job := submitFixture(t, client, root, Request{Detach: true, Keep: true, HostEnv: map[string]string{secretKey: sentinel}})
	waitState(t, client, job.ID, "failed")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "job-"+job.ID), []byte("running\n"), 0o600))
	assertHeld(t, s, job.ID, 1)
	requireScrubbed(t, s, job.ID, sentinel)
	cleaned, err := client.Cancel(context.Background(), job.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", cleaned.State)
	require.NoFileExists(t, filepath.Join(dir, "job-"+job.ID))
	s.mu.Lock()
	defer s.mu.Unlock()
	require.False(t, s.jobs[job.ID].retained)
	require.Zero(t, s.vmRunning)
}
