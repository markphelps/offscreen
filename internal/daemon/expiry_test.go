package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/tart"
)

// logBuffer captures the daemon's slog output for the duration of a test.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs must be registered before the server so the default logger is
// restored only after the server (and its maintenance goroutine) has closed.
func captureLogs(t *testing.T) *logBuffer {
	t.Helper()
	logs := &logBuffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return logs
}

// testClock is a controllable daemon clock: real time plus an offset.
type testClock struct{ offset atomic.Int64 }

func (c *testClock) now() time.Time          { return time.Now().Add(time.Duration(c.offset.Load())) }
func (c *testClock) advance(d time.Duration) { c.offset.Store(int64(d)) }
func (c *testClock) install(s *Server)       { s.mu.Lock(); s.now = c.now; s.mu.Unlock() }
func withExpiryInterval(t *testing.T, d time.Duration) {
	t.Helper()
	previous := expiryInterval
	expiryInterval = d
	t.Cleanup(func() { expiryInterval = previous })
}

// isolatedState points every daemon path at a private short directory and
// returns it with a project root containing a valid contract.
func isolatedState(t *testing.T) (string, string) {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "osd-") // Unix socket paths are limited on macOS.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\n"), 0o600))
	return state, root
}

func openFixture(t *testing.T, vm tart.Client) *Server {
	t.Helper()
	cfg := config.Default()
	s, err := open(cfg, pruneExecutor(nil), vm, func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	return s
}

// ownedRecord writes a job row with a daemon-owned directory holding output.
func ownedRecord(t *testing.T, s *Server, id string, lane project.Lane, state, root string, updated time.Time, req Request) string {
	t.Helper()
	dir, err := JobDir(id)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stream.bin"), []byte{'o', 0, 0, 0, 1, 'x'}, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "result.txt"), []byte("output"), 0o600))
	req.Root, req.Project = root, "hello"
	insertJobRecord(t, s, id, string(lane), state, dir, updated, req)
	return dir
}

func memoryIDs(s *Server) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []string{}
	for id := range s.jobs {
		out = append(out, id)
	}
	return sorted(out...)
}

func TestStartupExpiryRemovesOldHistoryBeforeServing(t *testing.T) {
	logs := captureLogs(t)
	state, root := isolatedState(t)
	start := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	previous := wallClock
	wallClock = func() time.Time { return start }
	t.Cleanup(func() { wallClock = previous })

	// An earlier daemon's history: old, boundary, recent, protected and foreign.
	first := openFixture(t, emptyTart(t))
	const (
		oldDone      = "a000000000000001"
		oldFailed    = "a000000000000002"
		oldCancelled = "a000000000000003"
		inside       = "b000000000000001" // 167h old: within the default retention
		queued       = "c000000000000001"
		orphan       = "c000000000000002"
		foreignID    = "d000000000000001"
	)
	expired := start.Add(-169 * time.Hour)
	dirs := map[string]string{
		oldDone:      ownedRecord(t, first, oldDone, project.LaneVM, "done", root, expired, Request{Argv: []string{"true"}}),
		oldFailed:    ownedRecord(t, first, oldFailed, project.LaneHost, "failed", root, expired.Add(-24*time.Hour), Request{Argv: []string{"true"}}),
		oldCancelled: ownedRecord(t, first, oldCancelled, project.LaneVM, "cancelled", root, expired, Request{Argv: []string{"true"}, Keep: true}),
		inside:       ownedRecord(t, first, inside, project.LaneVM, "done", root, start.Add(-167*time.Hour), Request{Argv: []string{"true"}}),
		// No host gate is configured, so this stays queued after restart.
		queued: ownedRecord(t, first, queued, project.LaneHost, "queued", root, expired, Request{Argv: []string{"true"}, Detach: true}),
		// Orphaned active work is failed at startup and starts aging then.
		orphan: ownedRecord(t, first, orphan, project.LaneVM, "running", root, expired, Request{Argv: []string{"true"}}),
	}
	foreign := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "keep.txt"), []byte("not ours"), 0o600))
	insertJobRecord(t, first, foreignID, "vm", "done", foreign, expired, Request{Root: root, Project: "hello", Argv: []string{"true"}})
	cache := filepath.Join(state, "projects", "abc")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "derived"), []byte("cache"), 0o600))
	require.NoError(t, first.Close())

	s := openFixture(t, emptyTart(t))
	// open has returned but nothing is served yet: the startup pass already ran.
	for _, id := range []string{oldDone, oldFailed, oldCancelled} {
		require.False(t, rowExists(t, s, id), id)
		require.NoDirExists(t, dirs[id])
	}
	for _, id := range []string{inside, queued, orphan, foreignID} {
		require.True(t, rowExists(t, s, id), id)
	}
	for _, id := range []string{inside, queued, orphan} {
		require.FileExists(t, filepath.Join(dirs[id], "stream.bin"), id)
	}
	require.Equal(t, sorted(inside, queued, orphan, foreignID), memoryIDs(s))
	require.FileExists(t, filepath.Join(foreign, "keep.txt"))
	require.FileExists(t, filepath.Join(cache, "derived"))
	require.Contains(t, logs.String(), "msg=\"job expiry\" pass=startup retention=168h0m0s")
	require.Contains(t, logs.String(), "deleted=3 failed=0 skipped=2")

	client := serveFixture(t, s)
	jobs, err := client.Jobs(context.Background())
	require.NoError(t, err)
	listed := []string{}
	for _, j := range jobs {
		listed = append(listed, j.ID)
	}
	require.Equal(t, sorted(inside, queued, orphan, foreignID), sorted(listed...))
	_, err = client.Job(context.Background(), oldDone)
	require.ErrorContains(t, err, "unknown job")
}

func TestStartupRejectsNonPositiveRetention(t *testing.T) {
	isolatedState(t)
	for _, retention := range []time.Duration{0, -time.Hour} {
		cfg := config.Default()
		cfg.JobRetention = retention
		_, err := open(cfg, pruneExecutor(nil), emptyTart(t), func() (uint64, error) { return 20 << 30, nil })
		require.ErrorContains(t, err, "job retention must be a positive duration")
	}
}

func TestPeriodicExpiryRetriesFailuresAndHonorsProtection(t *testing.T) {
	logs := captureLogs(t)
	withExpiryInterval(t, 20*time.Millisecond)
	_, root := isolatedState(t)
	vms, vm := retainedTartClient(t)
	s := openFixture(t, vm)
	clock := &testClock{}
	clock.install(s)
	passes := make(chan PruneReport, 1024)
	s.mu.Lock()
	s.onExpiry = func(r PruneReport, err error) {
		if err == nil {
			select {
			case passes <- r:
			default:
			}
		}
	}
	s.mu.Unlock()
	client := serveFixture(t, s)

	plain := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, plain.ID, "done")
	stuck := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, stuck.ID, "done")
	kept := keptJob(t, client, root, vms)
	assertHeld(t, s, kept.ID, 1)
	active := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"block"}})
	waitState(t, client, active.ID, "running")
	queued := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost}) // no host gate
	waitState(t, client, queued.ID, "queued")

	// Recent jobs survive many periodic passes at the real time.
	drain(passes)
	waitPasses(t, passes, 3)
	require.Equal(t, sorted(plain.ID, stuck.ID, kept.ID, active.ID, queued.ID), memoryIDs(s))

	// The row delete for one expired job fails; the rest of the pass continues.
	_, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER deny_delete BEFORE DELETE ON jobs WHEN old.id = '%s' BEGIN SELECT RAISE(FAIL, 'delete denied'); END`, stuck.ID))
	require.NoError(t, err)
	stuckUpdated, keptUpdated := storedUpdated(t, s, stuck.ID), storedUpdated(t, s, kept.ID)
	clock.advance(169 * time.Hour)
	require.Eventually(t, func() bool { return !rowExists(t, s, plain.ID) }, 5*time.Second, 10*time.Millisecond)
	require.NoDirExists(t, plain.Artifacts)
	waitPasses(t, passes, 2)
	require.Equal(t, sorted(stuck.ID, kept.ID, active.ID, queued.ID), memoryIDs(s))
	require.Equal(t, stuckUpdated, storedUpdated(t, s, stuck.ID), "a failed delete must not reset the job's age")
	require.Contains(t, logs.String(), "msg=\"expired job not deleted; retrying next pass\" pass=periodic job="+stuck.ID)
	require.Contains(t, logs.String(), "delete denied")
	s.mu.Lock()
	require.NoError(t, s.fatalErr)
	s.mu.Unlock()

	// While Tart cannot be listed, nothing VM-backed is deleted and the error is logged.
	// Break Tart before allowing the delete, so no pass can delete the job in between.
	require.NoError(t, os.WriteFile(filepath.Join(vms, "list-fails"), nil, 0o600))
	_, err = s.db.Exec(`DROP TRIGGER deny_delete`)
	require.NoError(t, err)
	seen := len(logs.String())
	require.Eventually(t, func() bool {
		return strings.Contains(logs.String()[seen:], "msg=\"job expiry deleted nothing; retrying next pass\" pass=periodic")
	}, 5*time.Second, 10*time.Millisecond)
	require.True(t, rowExists(t, s, stuck.ID))
	require.Equal(t, stuckUpdated, storedUpdated(t, s, stuck.ID))
	require.NoError(t, os.Remove(filepath.Join(vms, "list-fails")))
	require.Eventually(t, func() bool { return !rowExists(t, s, stuck.ID) }, 5*time.Second, 10*time.Millisecond)

	// Retained, active and queued work stay protected however old the clock says.
	drain(passes)
	report := waitPasses(t, passes, 2)
	require.Contains(t, skipReason(t, report, kept.ID), "retained")
	require.Equal(t, "job is running", skipReason(t, report, active.ID))
	require.Contains(t, skipReason(t, report, queued.ID), "job is queued")
	require.Equal(t, keptUpdated, storedUpdated(t, s, kept.ID))
	assertHeld(t, s, kept.ID, 2)

	// Cancelling --keep releases the clone without touching its completion
	// time, so the next periodic pass expires it.
	_, err = client.Cancel(context.Background(), kept.ID)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return !rowExists(t, s, kept.ID) }, 5*time.Second, 10*time.Millisecond)
	require.NoDirExists(t, kept.Artifacts)
	ops, err := os.ReadFile(filepath.Join(vms, "operations"))
	require.NoError(t, err)
	require.Equal(t, "stop job-"+kept.ID+"\ndelete job-"+kept.ID+"\n", string(ops), "expiry itself never touches VMs")
	require.Equal(t, sorted(active.ID, queued.ID), memoryIDs(s))
	_, err = client.Cancel(context.Background(), active.ID)
	require.NoError(t, err)
}

func TestPeriodicExpiryStopsWithDaemon(t *testing.T) {
	withExpiryInterval(t, 5*time.Millisecond)
	isolatedState(t)
	s := openFixture(t, emptyTart(t))
	var running atomic.Int32
	s.mu.Lock()
	s.onExpiry = func(PruneReport, error) {
		if running.Add(1) != 1 {
			panic("overlapping expiry passes")
		}
		time.Sleep(2 * time.Millisecond)
		running.Add(-1)
	}
	s.mu.Unlock()
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, s.Close())
	// Close waited for the maintenance goroutine; no pass runs afterwards.
	s.mu.Lock()
	s.onExpiry = func(PruneReport, error) { panic("expiry ran after Close") }
	s.mu.Unlock()
	time.Sleep(30 * time.Millisecond)
}

// Repeated equal create/expire workloads must reuse freed SQLite pages rather
// than allocate a new lifetime history. SQLite keeps its high-water file size;
// it does not shrink without VACUUM.
func TestRepeatedExpiryCyclesKeepRowsAndPagesBounded(t *testing.T) {
	withExpiryInterval(t, 20*time.Millisecond)
	_, root := isolatedState(t)
	s := openFixture(t, emptyTart(t))
	clock := &testClock{}
	clock.install(s)
	client := serveFixture(t, s)
	pragma := func(name string) int {
		var n int
		s.mu.Lock()
		defer s.mu.Unlock()
		require.NoError(t, s.db.QueryRow("PRAGMA "+name).Scan(&n))
		return n
	}
	rows := func() int {
		var n int
		s.mu.Lock()
		defer s.mu.Unlock()
		require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM jobs`).Scan(&n))
		return n
	}
	jobsDir := func() int {
		dir, err := JobDir("0000000000000000")
		require.NoError(t, err)
		entries, err := os.ReadDir(filepath.Dir(dir))
		require.NoError(t, err)
		return len(entries)
	}
	const cycles, perCycle = 5, 12
	payload := strings.Repeat("x", 3000) // a few KiB of request per row
	var fullPages []int
	for cycle := range cycles {
		clock.advance(0)
		submitted := make([]Job, 0, perCycle)
		for range perCycle {
			submitted = append(submitted, submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed", payload}}))
		}
		for _, j := range submitted {
			waitState(t, client, j.ID, "done")
		}
		require.Equal(t, perCycle, rows(), "cycle %d", cycle)
		fullPages = append(fullPages, pragma("page_count"))

		clock.advance(169 * time.Hour)
		require.Eventually(t, func() bool { return rows() == 0 }, 5*time.Second, 10*time.Millisecond, "cycle %d", cycle)
		require.Empty(t, memoryIDs(s))
		require.Zero(t, jobsDir(), "cycle %d left job directories", cycle)
		jobs, err := client.Jobs(context.Background())
		require.NoError(t, err)
		require.Empty(t, jobs)
		require.Positive(t, pragma("freelist_count"), "expired rows free pages for reuse")
		s.mu.Lock()
		require.Empty(t, s.order)
		s.mu.Unlock()
	}
	t.Logf("page_count after each full cycle: %v (%d jobs created in total)", fullPages, cycles*perCycle)
	// Later cycles reuse the first cycle's pages instead of growing with the
	// lifetime job count. A little slack covers B-tree layout differences.
	for cycle, pages := range fullPages[1:] {
		require.LessOrEqual(t, pages, fullPages[0]+4, "cycle %d grew the database: %v", cycle+1, fullPages)
	}
}

func drain(passes chan PruneReport) {
	for {
		select {
		case <-passes:
		default:
			return
		}
	}
}

// waitPasses waits for n further successful periodic passes and returns the last.
func waitPasses(t *testing.T, passes chan PruneReport, n int) PruneReport {
	t.Helper()
	var last PruneReport
	for range n {
		select {
		case last = <-passes:
		case <-time.After(5 * time.Second):
			t.Fatal("periodic expiry pass did not run")
		}
	}
	return last
}
