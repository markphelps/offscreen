package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

const expiredAge = 40 * 24 * time.Hour // older than the 720h prune age used below

// pruneExecutor dispatches on argv so one fixture holds every kind of job.
func pruneExecutor(release <-chan struct{}) func(context.Context, run.Options) run.Result {
	return func(ctx context.Context, o run.Options) run.Result {
		if o.Keep {
			return keptExecutor(ctx, o)
		}
		switch o.Argv[0] {
		case "fail":
			return run.Result{Code: 9, Err: errors.New("task failed")}
		case "block":
			o.Phase("running")
			<-ctx.Done()
			return run.Result{Code: exitcode.RunnerFailure, Err: ctx.Err()}
		case "linger": // keeps cleaning up after cancellation
			o.Phase("running")
			<-ctx.Done()
			<-release
			return run.Result{Code: exitcode.RunnerFailure, Err: ctx.Err()}
		}
		_ = os.WriteFile(filepath.Join(o.Artifacts, "result.txt"), []byte("output"), 0o600)
		return run.Result{}
	}
}

// backdate moves a job's completion time into the past in SQLite and memory,
// as if it had finished long ago.
func backdate(t *testing.T, s *Server, id string, age time.Duration) {
	t.Helper()
	at := time.Now().Add(-age)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.Exec(`UPDATE jobs SET updated=? WHERE id=?`, at.Format(time.RFC3339Nano), id)
	require.NoError(t, err)
	s.jobs[id].job.Updated = at
}

func storedUpdated(t *testing.T, s *Server, id string) string {
	t.Helper()
	var updated string
	require.NoError(t, s.db.QueryRow(`SELECT updated FROM jobs WHERE id=?`, id).Scan(&updated))
	return updated
}

func rowExists(t *testing.T, s *Server, id string) bool {
	t.Helper()
	var n int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM jobs WHERE id=?`, id).Scan(&n))
	return n == 1
}

// observe captures everything prune could change: rows, memory and files.
type observation struct {
	rows   []string
	memory map[string]string
	files  []string
}

func observe(t *testing.T, s *Server, dirs ...string) observation {
	t.Helper()
	var o observation
	s.mu.Lock()
	rows, err := s.db.Query(`SELECT id,state,code,reason,updated,request FROM jobs ORDER BY id`)
	require.NoError(t, err)
	for rows.Next() {
		var id, state, reason, updated, request string
		var code int
		require.NoError(t, rows.Scan(&id, &state, &code, &reason, &updated, &request))
		o.rows = append(o.rows, strings.Join([]string{id, state, fmt.Sprint(code), reason, updated, request}, "|"))
	}
	require.NoError(t, rows.Err())
	_ = rows.Close()
	o.memory = map[string]string{}
	for id, w := range s.jobs {
		b, err := json.Marshal(w.job)
		require.NoError(t, err)
		o.memory[id] = fmt.Sprintf("%s retained=%t", b, w.retained)
	}
	s.mu.Unlock()
	for _, dir := range dirs {
		require.NoError(t, filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			o.files = append(o.files, fmt.Sprintf("%s %v %d %d", path, info.Mode(), info.Size(), info.ModTime().UnixNano()))
			return nil
		}))
	}
	return o
}

func ids(entries []PruneEntry) []string {
	out := []string{}
	for _, e := range entries {
		out = append(out, e.Job.ID)
	}
	slices.Sort(out)
	return out
}

func sorted(values ...string) []string {
	slices.Sort(values)
	return values
}

func skipReason(t *testing.T, report PruneReport, id string) string {
	t.Helper()
	for _, e := range report.Skipped {
		if e.Job.ID == id {
			return e.Reason
		}
	}
	t.Fatalf("job %s was not reported as skipped: %+v", id, report.Skipped)
	return ""
}

func TestPrunePreviewsThenDeletesOnlyConfirmedOldTerminalJobs(t *testing.T) {
	s, client, root := fixture(t, pruneExecutor(nil), func() (uint64, error) { return 20 << 30, nil })
	vms := retainedTart(t, s)
	state, err := StateDir()
	require.NoError(t, err)

	oldDone := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, oldDone.ID, "done")
	oldFailed := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"fail"}})
	waitState(t, client, oldFailed.ID, "failed")
	recent := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, recent.ID, "done")
	leaked := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, leaked.ID, "done")
	// A clone whose delete failed: execution finished but ownership cleanup did not.
	require.NoError(t, os.WriteFile(filepath.Join(vms, "job-"+leaked.ID), []byte("stopped\n"), 0o600))
	kept := keptJob(t, client, root, vms)
	assertHeld(t, s, kept.ID, 1)
	active := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"block"}})
	waitState(t, client, active.ID, "running")
	queued := submitFixture(t, client, root, Request{Detach: true, Lane: project.LaneHost}) // no host gate
	require.Eventually(t, func() bool {
		j, err := client.Job(context.Background(), queued.ID)
		return err == nil && j.State == "queued" && j.Reason != ""
	}, 2*time.Second, 20*time.Millisecond)
	// A record pointing its artifacts outside the daemon's job directories.
	foreign := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(foreign, "keep.txt"), []byte("not ours"), 0o600))
	insertJobRecord(t, s, "00000000000000ff", "vm", "done", foreign, time.Now().Add(-expiredAge), Request{Root: root, Project: "hello", Argv: []string{"true"}})
	s.mu.Lock()
	s.jobs["00000000000000ff"] = &worker{job: Job{ID: "00000000000000ff", Project: "hello", Lane: project.LaneVM, State: "done", Artifacts: foreign, Updated: time.Now().Add(-expiredAge)}}
	s.mu.Unlock()

	for _, id := range []string{oldDone.ID, oldFailed.ID, leaked.ID, kept.ID, active.ID, queued.ID} {
		backdate(t, s, id, expiredAge)
	}
	// A symlink in an expiring job directory must not lead deletion elsewhere.
	outside := filepath.Join(t.TempDir(), "outside.txt")
	require.NoError(t, os.WriteFile(outside, []byte("unrelated"), 0o600))
	require.NoError(t, os.Symlink(filepath.Dir(outside), filepath.Join(oldDone.Artifacts, "link")))
	// Persistent per-project caches sit beside job directories and never expire.
	cache := filepath.Join(state, "projects", "abc")
	require.NoError(t, os.MkdirAll(cache, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cache, "derived"), []byte("cache"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(vms, "golden-image"), []byte("stopped\n"), 0o600))

	before := observe(t, s, state, foreign, vms)
	preview, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour})
	require.NoError(t, err)
	require.False(t, preview.Apply)
	require.Equal(t, sorted(oldDone.ID, oldFailed.ID), ids(preview.Eligible))
	require.Empty(t, preview.Deleted)
	require.Empty(t, preview.Failed)
	require.GreaterOrEqual(t, preview.Recent, 1)
	require.WithinDuration(t, time.Now().Add(-720*time.Hour), preview.Cutoff, time.Minute)
	require.Contains(t, skipReason(t, preview, leaked.ID), "still exists")
	require.Contains(t, skipReason(t, preview, kept.ID), "retained")
	require.Equal(t, "job is running", skipReason(t, preview, active.ID))
	require.Equal(t, "job is queued", skipReason(t, preview, queued.ID))
	require.Contains(t, skipReason(t, preview, "00000000000000ff"), "outside this daemon's job directory")
	require.NotContains(t, ids(preview.Skipped), recent.ID)
	require.Equal(t, before, observe(t, s, state, foreign, vms), "preview must not change rows, memory, files or VMs")

	applied, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
	require.NoError(t, err)
	require.Equal(t, sorted(oldDone.ID, oldFailed.ID), ids(applied.Deleted))
	require.Empty(t, applied.Failed)
	for _, id := range []string{oldDone.ID, oldFailed.ID} {
		require.False(t, rowExists(t, s, id))
		dir, err := JobDir(id)
		require.NoError(t, err)
		require.NoDirExists(t, dir)
		_, err = client.Job(context.Background(), id)
		require.ErrorContains(t, err, "unknown job")
	}
	for _, id := range []string{recent.ID, leaked.ID, kept.ID, active.ID, queued.ID, "00000000000000ff"} {
		require.True(t, rowExists(t, s, id), id)
		_, err := client.Job(context.Background(), id)
		require.NoError(t, err, id)
	}
	require.FileExists(t, outside)
	require.FileExists(t, filepath.Join(foreign, "keep.txt"))
	require.FileExists(t, filepath.Join(cache, "derived"))
	require.FileExists(t, filepath.Join(recent.Artifacts, "stream.bin"))
	require.FileExists(t, filepath.Join(vms, "golden-image"))
	assertHeld(t, s, kept.ID, 2) // plus the active job
	ops, _ := os.ReadFile(filepath.Join(vms, "operations"))
	require.Empty(t, string(ops), "prune never stops or deletes VMs")

	// Once protection ends, the original completion time still applies.
	keptUpdated := storedUpdated(t, s, kept.ID)
	_, err = client.Cancel(context.Background(), kept.ID)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(vms, "job-"+leaked.ID)))
	require.Equal(t, keptUpdated, storedUpdated(t, s, kept.ID))
	again, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
	require.NoError(t, err)
	require.Equal(t, sorted(kept.ID, leaked.ID), ids(again.Deleted))
	_, err = client.Cancel(context.Background(), active.ID)
	require.NoError(t, err)
}

func TestPruneRejectsNonPositiveAge(t *testing.T) {
	_, client, _ := fixture(t, pruneExecutor(nil), func() (uint64, error) { return 20 << 30, nil })
	for _, age := range []time.Duration{0, -time.Hour} {
		_, err := client.Prune(context.Background(), PruneRequest{OlderThan: age, Apply: true})
		require.ErrorContains(t, err, "older_than must be a positive duration")
	}
}

func TestPrunePartialFailuresRetryWithoutResettingAge(t *testing.T) {
	s, client, root := fixture(t, pruneExecutor(nil), func() (uint64, error) { return 20 << 30, nil })
	fsFail := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, fsFail.ID, "done")
	dbFail := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, dbFail.ID, "done")
	backdate(t, s, fsFail.ID, expiredAge)
	backdate(t, s, dbFail.ID, expiredAge+time.Hour)
	fsUpdated, dbUpdated := storedUpdated(t, s, fsFail.ID), storedUpdated(t, s, dbFail.ID)

	// An undeletable entry makes directory removal fail part way.
	locked := filepath.Join(fsFail.Artifacts, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o600))
	require.NoError(t, os.Chmod(locked, 0o500))       // #nosec G302 -- test makes a directory read-only
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) }) // #nosec G302 -- restore for cleanup
	// The row delete fails after its files were removed.
	_, err := s.db.Exec(fmt.Sprintf(`CREATE TRIGGER deny_delete BEFORE DELETE ON jobs WHEN old.id = '%s' BEGIN SELECT RAISE(FAIL, 'delete denied'); END`, dbFail.ID))
	require.NoError(t, err)

	first, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
	require.NoError(t, err)
	require.Empty(t, first.Deleted)
	require.Equal(t, sorted(fsFail.ID, dbFail.ID), ids(first.Failed))
	for _, e := range first.Failed {
		switch e.Job.ID {
		case fsFail.ID:
			require.Contains(t, e.Reason, "remove job directory")
		case dbFail.ID:
			require.Contains(t, e.Reason, "delete job record")
		}
	}
	// Failed jobs stay visible and keep their age; the daemon keeps serving.
	for id, updated := range map[string]string{fsFail.ID: fsUpdated, dbFail.ID: dbUpdated} {
		require.True(t, rowExists(t, s, id))
		require.Equal(t, updated, storedUpdated(t, s, id))
		_, err := client.Job(context.Background(), id)
		require.NoError(t, err)
	}
	require.NoDirExists(t, dbFail.Artifacts)
	s.mu.Lock()
	require.NoError(t, s.fatalErr)
	s.mu.Unlock()
	later := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"succeed"}})
	waitState(t, client, later.ID, "done")

	require.NoError(t, os.Chmod(locked, 0o700)) // #nosec G302 -- restore
	_, err = s.db.Exec(`DROP TRIGGER deny_delete`)
	require.NoError(t, err)
	retry, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
	require.NoError(t, err)
	require.Equal(t, sorted(fsFail.ID, dbFail.ID), ids(retry.Deleted))
	require.Empty(t, retry.Failed)
	require.NoDirExists(t, fsFail.Artifacts)
	require.False(t, rowExists(t, s, fsFail.ID))
	require.False(t, rowExists(t, s, dbFail.ID))
	require.True(t, rowExists(t, s, later.ID))
}

func TestPruneWaitsForCancelledWorkerCleanup(t *testing.T) {
	release := make(chan struct{})
	s, client, root := fixture(t, pruneExecutor(release), func() (uint64, error) { return 20 << 30, nil })
	job := submitFixture(t, client, root, Request{Detach: true, Argv: []string{"linger"}})
	waitState(t, client, job.ID, "running")
	cancelled := make(chan error, 1)
	go func() { _, err := client.Cancel(context.Background(), job.ID); cancelled <- err }()
	waitState(t, client, job.ID, "cancelled")
	backdate(t, s, job.ID, expiredAge)

	report, err := s.Prune(context.Background(), PruneOptions{Cutoff: time.Now(), Apply: true})
	require.NoError(t, err)
	require.Empty(t, report.Deleted)
	require.Contains(t, []string{"worker still running", "worker still cleaning up"}, skipReason(t, report, job.ID))
	require.True(t, rowExists(t, s, job.ID))
	require.DirExists(t, job.Artifacts)

	close(release)
	require.NoError(t, <-cancelled)
	report, err = s.Prune(context.Background(), PruneOptions{Cutoff: time.Now(), Apply: true})
	require.NoError(t, err)
	require.Equal(t, []string{job.ID}, ids(report.Deleted))
}

func TestPruneRacesSafelyWithAdmissionAndCancel(t *testing.T) {
	var mu sync.Mutex
	returned := map[string]bool{}
	s, client, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		if o.Argv[0] == "block" {
			<-ctx.Done()
		} else {
			time.Sleep(5 * time.Millisecond)
		}
		mu.Lock()
		returned[o.ID] = true
		mu.Unlock()
		return run.Result{}
	}, func() (uint64, error) { return 20 << 30, nil })
	// The injectable clock makes everything already finished look 60 days old.
	s.mu.Lock()
	s.now = func() time.Time { return time.Now().Add(60 * 24 * time.Hour) }
	s.mu.Unlock()

	stop := make(chan struct{})
	var deleted []PruneEntry
	var pruneErr error
	pruned := make(chan struct{})
	go func() {
		defer close(pruned)
		for {
			select {
			case <-stop:
				return
			default:
			}
			report, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
			if err != nil {
				pruneErr = err
				return
			}
			mu.Lock()
			for _, e := range report.Deleted {
				if !Terminal(e.Job.State) || !returned[e.Job.ID] {
					pruneErr = fmt.Errorf("pruned job %s in state %s before its worker returned", e.Job.ID, e.Job.State)
				}
			}
			mu.Unlock()
			deleted = append(deleted, report.Deleted...)
		}
	}()
	var submitted []Job
	for i := range 12 {
		argv := "succeed"
		if i%4 == 0 {
			argv = "block"
		}
		j := submitFixture(t, client, root, Request{Detach: true, Argv: []string{argv}})
		submitted = append(submitted, j)
		if argv == "block" {
			waitState(t, client, j.ID, "running")
			_, err := client.Cancel(context.Background(), j.ID)
			require.NoError(t, err)
		}
	}
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, j := range submitted {
			if w, ok := s.jobs[j.ID]; ok && !Terminal(w.job.State) {
				return false
			}
		}
		return true
	}, 5*time.Second, 20*time.Millisecond)
	close(stop)
	<-pruned
	require.NoError(t, pruneErr)
	final, err := client.Prune(context.Background(), PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
	require.NoError(t, err)
	require.Empty(t, final.Failed)
	deleted = append(deleted, final.Deleted...)
	require.ElementsMatch(t, func() []string {
		out := []string{}
		for _, j := range submitted {
			out = append(out, j.ID)
		}
		return out
	}(), ids(deleted))
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Empty(t, s.jobs)
	require.Zero(t, s.vmRunning)
}

func TestPruneHandlesRecordsFromEarlierDaemons(t *testing.T) {
	s, _, root := fixture(t, pruneExecutor(nil), func() (uint64, error) { return 20 << 30, nil })
	// A pre-scrub terminal record with its stream log, and a crashed active one.
	legacy, orphan := "1111111111111111", "2222222222222222"
	for _, id := range []string{legacy, orphan} {
		dir, err := JobDir(id)
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "stream.bin"), []byte{'o', 0, 0, 0, 1, 'x'}, 0o600))
	}
	legacyDir, _ := JobDir(legacy)
	orphanDir, _ := JobDir(orphan)
	insertJobRecord(t, s, legacy, "vm", "done", legacyDir, time.Now().Add(-expiredAge), Request{Root: root, Project: "hello", Argv: []string{"true"}, HostEnv: map[string]string{secretKey: "sentinel"}})
	insertJobRecord(t, s, orphan, "vm", "running", orphanDir, time.Now().Add(-expiredAge), Request{Root: root, Project: "hello", Argv: []string{"true"}})
	require.NoError(t, s.Close())

	// Keep the startup expiry pass away from these records; this test drives
	// the engine directly.
	cfg := config.Default()
	cfg.JobRetention = 1000 * 24 * time.Hour
	reopened, err := open(cfg, pruneExecutor(nil), emptyTart(t), func() (uint64, error) { return 20 << 30, nil })
	require.NoError(t, err)
	defer func() { require.NoError(t, reopened.Close()) }()
	report, err := reopened.Prune(context.Background(), PruneOptions{Cutoff: time.Now().Add(-720 * time.Hour), Apply: true})
	require.NoError(t, err)
	// The orphan was failed at startup just now, so its expiry age starts then.
	require.Equal(t, []string{legacy}, ids(report.Deleted))
	require.Equal(t, 1, report.Recent)
	require.NoDirExists(t, legacyDir)
	require.DirExists(t, orphanDir)
	report, err = reopened.Prune(context.Background(), PruneOptions{Cutoff: time.Now().Add(time.Minute), Apply: true})
	require.NoError(t, err)
	require.Equal(t, []string{orphan}, ids(report.Deleted))
	require.NoDirExists(t, orphanDir)
}

func TestPruneRefusesStoppedDaemon(t *testing.T) {
	s, _, _ := fixture(t, pruneExecutor(nil), func() (uint64, error) { return 20 << 30, nil })
	s.mu.Lock()
	_ = s.failLocked(errors.New("store failed"))
	s.mu.Unlock()
	_, err := s.Prune(context.Background(), PruneOptions{Cutoff: time.Now(), Apply: true})
	require.ErrorContains(t, err, "store failed")
}

func insertJobRecord(t *testing.T, s *Server, id, lane, state, artifacts string, updated time.Time, request Request) {
	t.Helper()
	b, err := json.Marshal(request)
	require.NoError(t, err)
	_, err = s.db.Exec(`INSERT INTO jobs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "hello", request.Root, "", lane, state, 0, artifacts, "",
		updated.Format(time.RFC3339Nano), updated.Format(time.RFC3339Nano), time.Time{}.Format(time.RFC3339Nano), string(b), 0)
	require.NoError(t, err)
}
