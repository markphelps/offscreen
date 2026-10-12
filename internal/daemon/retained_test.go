package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
)

// The Tart double models an independent VM inventory. Admission tests observe
// actual executor starts and Tart operations rather than setting retained flags.
func retainedTart(t *testing.T, s *Server) string {
	t.Helper()
	dir, client := retainedTartClient(t)
	s.tart = client
	return dir
}

// retainedTartClient returns the Tart double and its inventory directory
// without installing it, for servers opened with it from the start.
func retainedTartClient(t *testing.T) (string, tart.Client) {
	t.Helper()
	dir := t.TempDir()
	binary := filepath.Join(dir, "tart")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
cd '%s'
case "$1" in
list)
  [ ! -f list-fails ] || exit 1
  printf '['
  separator=''
  for file in job-*; do
    [ -f "$file" ] || continue
    printf '%%s{"Name":"%%s","Running":%%s}' "$separator" "$file" "$(test "$(cat "$file")" = running && printf true || printf false)"
    separator=,
  done
  printf ']\n'
  ;;
stop)
  printf 'stop %%s\n' "$2" >> operations
  [ ! -f stop-fails ] || exit 1
  while [ -f stop-blocks ]; do sleep 0.02; done
  printf 'stopped\n' > "$2"
  ;;
delete)
  printf 'delete %%s\n' "$2" >> operations
  [ ! -f delete-fails ] || exit 1
  rm "$2"
  ;;
esac
`, dir)
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o700)) // #nosec G306 -- private test executable
	return dir, tart.Client{Binary: binary}
}

func keptExecutor(_ context.Context, o run.Options) run.Result {
	o.Phase("running")
	return run.Result{Code: 3, Err: errors.New("guest failed"), Kept: true, VM: "job-" + o.ID}
}

func keptJob(t *testing.T, c Client, root, dir string) Job {
	t.Helper()
	j := submitFixture(t, c, root, Request{Detach: true, Keep: true})
	record := waitState(t, c, j.ID, "failed")
	require.Equal(t, 3, record.Code)
	require.Equal(t, "guest failed", record.Reason)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "job-"+j.ID), []byte("running\n"), 0o600))
	return record
}

func assertHeld(t *testing.T, s *Server, id string, count int) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.jobs[id].retained && s.vmRunning == count
	}, time.Second, 10*time.Millisecond)
}

func TestRetainedVMsConsumeSlotsUntilCancelled(t *testing.T) {
	for _, lowMemory := range []bool{false, true} {
		t.Run(fmt.Sprintf("lowMemory=%t", lowMemory), func(t *testing.T) {
			memory := uint64(20 << 30)
			limit := 2
			if lowMemory {
				memory, limit = 1<<30, 1
			}
			var mu sync.Mutex
			var starts []string
			s, c, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
				mu.Lock()
				starts = append(starts, o.ID)
				mu.Unlock()
				return keptExecutor(ctx, o)
			}, func() (uint64, error) { return memory, nil })
			dir := retainedTart(t, s)
			var kept []Job
			for range limit {
				j := keptJob(t, c, root, dir)
				assertHeld(t, s, j.ID, len(kept)+1)
				kept = append(kept, j)
			}
			queued := submitFixture(t, c, root, Request{Detach: true, Keep: true})
			require.Eventually(t, func() bool {
				j, err := c.Job(context.Background(), queued.ID)
				return err == nil && j.State == "queued" && strings.Contains(j.Reason, "retained VM") && slices.ContainsFunc(kept, func(k Job) bool { return strings.Contains(j.Reason, k.ID) })
			}, 2*time.Second, 20*time.Millisecond)
			mu.Lock()
			require.Len(t, starts, limit)
			mu.Unlock()
			// A retained clone still consumes its lease when manually stopped.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "job-"+kept[0].ID), []byte("stopped\n"), 0o600))
			s.admit()
			current, err := c.Job(context.Background(), queued.ID)
			require.NoError(t, err)
			require.Equal(t, "queued", current.State)
			cleaned, err := c.Cancel(context.Background(), kept[0].ID)
			require.NoError(t, err)
			require.Equal(t, kept[0].State, cleaned.State)
			require.Equal(t, kept[0].Reason, cleaned.Reason)
			require.Equal(t, kept[0].Code, cleaned.Code)
			require.Equal(t, kept[0].Artifacts, cleaned.Artifacts)
			require.NoFileExists(t, filepath.Join(dir, "job-"+kept[0].ID))
			waitState(t, c, queued.ID, "failed")
			assertHeld(t, s, queued.ID, limit)
			ops, err := os.ReadFile(filepath.Join(dir, "operations"))
			require.NoError(t, err)
			require.NotContains(t, string(ops), "stop job-"+kept[0].ID)
			for _, j := range kept[1:] {
				_, err := c.Cancel(context.Background(), j.ID)
				require.NoError(t, err)
			}
			_, err = c.Cancel(context.Background(), queued.ID)
			require.NoError(t, err)
		})
	}
}

func TestRetainedExclusiveProjectBlocksAcrossLanesAfterDone(t *testing.T) {
	s, c, root := fixture(t, keptExecutor, func() (uint64, error) { return 20 << 30, nil })
	dir := retainedTart(t, s)
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\nexclusive = true\n"), 0o600))
	kept := keptJob(t, c, root, dir)
	assertHeld(t, s, kept.ID, 1)
	other := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(other, project.FileName), []byte("project = 'hello'\n"), 0o600))
	s.ConfigureHost(func(context.Context, Job) (bool, string) { return true, "" }, func(context.Context, run.Options) run.Result { return run.Result{} })
	second, err := c.Submit(context.Background(), Request{Root: other, Project: "hello", Lane: project.LaneHost, Argv: []string{"true"}, Detach: true})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		j, e := c.Job(context.Background(), second.ID)
		return e == nil && j.State == "queued" && strings.Contains(j.Reason, "exclusive project (retained VM")
	}, 2*time.Second, 20*time.Millisecond)
	_, err = c.Cancel(context.Background(), kept.ID)
	require.NoError(t, err)
	waitState(t, c, second.ID, "done")
}

func TestCancelRacingKeptResultReleasesExactlyOnce(t *testing.T) {
	returnResult := make(chan struct{})
	s, c, root := fixture(t, func(ctx context.Context, o run.Options) run.Result {
		o.Phase("running")
		<-ctx.Done()
		<-returnResult
		return run.Result{Code: 3, Err: errors.New("guest failed"), Kept: true, VM: "job-" + o.ID}
	}, func() (uint64, error) { return 20 << 30, nil })
	dir := retainedTart(t, s)
	j := submitFixture(t, c, root, Request{Detach: true, Keep: true})
	waitState(t, c, j.ID, "running")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "job-"+j.ID), []byte("running\n"), 0o600))
	cancelDone := make(chan error, 1)
	go func() { _, err := c.Cancel(context.Background(), j.ID); cancelDone <- err }()
	waitState(t, c, j.ID, "cancelled")
	close(returnResult)
	require.NoError(t, <-cancelDone)
	s.mu.Lock()
	require.Equal(t, 0, s.vmRunning)
	require.False(t, s.jobs[j.ID].retained)
	s.mu.Unlock()
	require.NoFileExists(t, filepath.Join(dir, "job-"+j.ID))
	_, err := c.Cancel(context.Background(), j.ID)
	require.NoError(t, err)
	s.mu.Lock()
	require.Equal(t, 0, s.vmRunning)
	s.mu.Unlock()
}

func TestRetainedConcurrentCancelAndFailureKeepLease(t *testing.T) {
	s, c, root := fixture(t, keptExecutor, func() (uint64, error) { return 20 << 30, nil })
	dir := retainedTart(t, s)
	kept := keptJob(t, c, root, dir)
	assertHeld(t, s, kept.ID, 1)
	// Even a similarly named clone not owned by this job is never touched.
	require.NoError(t, os.WriteFile(filepath.Join(dir, "job-unrelated"), []byte("running\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "stop-blocks"), nil, 0o600))
	first := make(chan error, 1)
	go func() { _, err := c.Cancel(context.Background(), kept.ID); first <- err }()
	require.Eventually(t, func() bool {
		ops, err := os.ReadFile(filepath.Join(dir, "operations"))
		return err == nil && strings.Contains(string(ops), "stop job-"+kept.ID)
	}, time.Second, 10*time.Millisecond)
	second := make(chan error, 1)
	go func() { _, err := c.Cancel(context.Background(), kept.ID); second <- err }()
	assertHeld(t, s, kept.ID, 1)
	require.NoError(t, os.Remove(filepath.Join(dir, "stop-blocks")))
	require.NoError(t, <-first)
	require.NoError(t, <-second)
	_, err := c.Cancel(context.Background(), kept.ID)
	require.NoError(t, err)
	ops, err := os.ReadFile(filepath.Join(dir, "operations"))
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(ops), "stop job-"+kept.ID))
	require.Equal(t, 1, strings.Count(string(ops), "delete job-"+kept.ID))
	require.NotContains(t, string(ops), "job-unrelated")
	require.FileExists(t, filepath.Join(dir, "job-unrelated"))
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.vmRunning == 0 && !s.jobs[kept.ID].retained
	}, time.Second, 10*time.Millisecond)

	broken := keptJob(t, c, root, dir)
	assertHeld(t, s, broken.ID, 1)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "delete-fails"), nil, 0o600))
	_, err = c.Cancel(context.Background(), broken.ID)
	require.ErrorContains(t, err, "retry cancel")
	assertHeld(t, s, broken.ID, 1)
	require.FileExists(t, filepath.Join(dir, "job-"+broken.ID))
	require.NoError(t, os.Remove(filepath.Join(dir, "delete-fails")))
	_, err = c.Cancel(context.Background(), broken.ID)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, "job-"+broken.ID))
	// A clone removed outside offscreen must still be explicitly cancelled to
	// release its lease; cleanup of a missing clone is idempotent.
	missing := keptJob(t, c, root, dir)
	assertHeld(t, s, missing.ID, 1)
	require.NoError(t, os.Remove(filepath.Join(dir, "job-"+missing.ID)))
	_, err = c.Cancel(context.Background(), missing.ID)
	require.NoError(t, err)
	require.Equal(t, 0, s.vmRunning)
}

func TestCloseWaitsForRetainedCleanup(t *testing.T) {
	s, c, root := fixture(t, keptExecutor, func() (uint64, error) { return 20 << 30, nil })
	dir := retainedTart(t, s)
	kept := keptJob(t, c, root, dir)
	assertHeld(t, s, kept.ID, 1)
	block := filepath.Join(dir, "stop-blocks")
	require.NoError(t, os.WriteFile(block, nil, 0o600))
	t.Cleanup(func() { _ = os.Remove(block) })
	cancelled := make(chan error, 1)
	go func() { _, err := c.Cancel(context.Background(), kept.ID); cancelled <- err }()
	require.Eventually(t, func() bool {
		ops, err := os.ReadFile(filepath.Join(dir, "operations"))
		return err == nil && strings.Contains(string(ops), "stop job-"+kept.ID)
	}, time.Second, 10*time.Millisecond)
	closed := make(chan error, 1)
	go func() { closed <- s.Close() }()
	require.Never(t, func() bool { return len(closed) > 0 }, 100*time.Millisecond, 10*time.Millisecond,
		"shutdown released the daemon lock while retained cleanup was still active")
	require.NoError(t, os.Remove(block))
	require.NoError(t, <-cancelled)
	require.NoError(t, <-closed)
	require.NoFileExists(t, filepath.Join(dir, "job-"+kept.ID))
}
