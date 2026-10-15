package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/project"
)

func TestPruneFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"prune", "--dry-run"}, "--older-than"},
		{[]string{"prune", "--older-than", "720h", "--dry-run", "-f"}, "can't be used together"},
		{[]string{"prune", "--older-than", "0s", "-f"}, "positive duration"},
		{[]string{"prune", "--older-than=-1h", "--dry-run"}, "positive duration"},
		{[]string{"prune", "--older-than", "720h", "--yes"}, "unknown flag --yes"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(tc.args)
		require.ErrorContains(t, err, tc.err, "%v", tc.args)
	}
	for _, args := range [][]string{
		{"prune", "--older-than", "720h"},
		{"prune", "--older-than", "720h", "--dry-run"},
		{"prune", "--older-than", "720h", "-f"},
		{"prune", "--older-than", "720h", "--force"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(args)
		require.NoError(t, err, "%v", args)
		require.Equal(t, 720*time.Hour, cli.Prune.OlderThan)
	}
}

// fakeJobPrune records each request and answers with a preview or an applied
// report over the same jobs.
type fakeJobPrune struct {
	requests []daemon.PruneRequest
	eligible []daemon.PruneEntry
}

func (f *fakeJobPrune) run(_ context.Context, req daemon.PruneRequest) (daemon.PruneReport, error) {
	f.requests = append(f.requests, req)
	report := daemon.PruneReport{Cutoff: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Apply: req.Apply, Eligible: f.eligible, Recent: 2}
	if req.Apply {
		report.Deleted = f.eligible
	}
	return report, nil
}

func TestPruneAsksOnTerminal(t *testing.T) {
	old := []daemon.PruneEntry{{Job: daemon.Job{ID: "a1", Project: "hello", State: "done"}}}
	for _, tc := range []struct {
		name     string
		answer   string
		applied  bool
		summary  string
		eligible []daemon.PruneEntry
		prompted bool
	}{
		{name: "yes deletes", answer: "y\n", eligible: old, prompted: true, applied: true, summary: "summary  deleted 1, failed 0, skipped 0, kept 2 newer\n"},
		{name: "YES deletes", answer: "YES\n", eligible: old, prompted: true, applied: true, summary: "summary  deleted 1, failed 0, skipped 0, kept 2 newer\n"},
		{name: "no keeps", answer: "n\n", eligible: old, prompted: true, summary: "summary  cancelled; nothing changed\n"},
		{name: "enter keeps", answer: "\n", eligible: old, prompted: true, summary: "summary  cancelled; nothing changed\n"},
		{name: "end of input keeps", answer: "", eligible: old, prompted: true, summary: "summary  cancelled; nothing changed\n"},
		{name: "nothing selected skips prompt", summary: "summary  nothing to delete, skipped 0, kept 2 newer\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeJobPrune{eligible: tc.eligible}
			var stdout, stderr bytes.Buffer
			prompt := confirmer{in: strings.NewReader(tc.answer), out: &stderr, terminal: true}
			cmd := &PruneCmd{OlderThan: 720 * time.Hour}
			require.NoError(t, cmd.prune(context.Background(), fake.run, prompt, &stdout, &stderr))

			want := []daemon.PruneRequest{{OlderThan: 720 * time.Hour}}
			if tc.applied {
				want = append(want, daemon.PruneRequest{OlderThan: 720 * time.Hour, Apply: true})
			}
			require.Equal(t, want, fake.requests)
			require.True(t, strings.HasSuffix(stdout.String(), tc.summary), stdout.String())
			// The selection, warning and prompt stay off stdout.
			require.Contains(t, stderr.String(), "cutoff  2026-09-01T00:00:00Z")
			require.NotContains(t, stdout.String(), "would-delete")
			if tc.prompted {
				require.Contains(t, stderr.String(), "would-delete  a1  hello  done")
				require.Contains(t, stderr.String(), "WARNING! This will delete 1 completed job(s) with their logs and artifacts.\nAre you sure you want to continue? [y/N] ")
			} else {
				require.NotContains(t, stderr.String(), "[y/N]")
			}
			if tc.applied {
				require.Contains(t, stdout.String(), "deleted  a1  hello  done")
			}
		})
	}
}

func TestPruneWithoutTerminal(t *testing.T) {
	old := []daemon.PruneEntry{{Job: daemon.Job{ID: "a1", Project: "hello", State: "done"}}}
	noTTY := func(stderr *bytes.Buffer) confirmer {
		return confirmer{in: strings.NewReader("y\n"), out: stderr, terminal: false}
	}

	// Neither -f nor --dry-run: refuse before asking the daemon anything.
	fake := &fakeJobPrune{eligible: old}
	var stdout, stderr bytes.Buffer
	err := (&PruneCmd{OlderThan: time.Hour}).prune(context.Background(), fake.run, noTTY(&stderr), &stdout, &stderr)
	require.ErrorContains(t, err, "pass -f to delete without asking")
	require.Empty(t, fake.requests)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())

	// -f deletes without a prompt.
	fake = &fakeJobPrune{eligible: old}
	stdout.Reset()
	require.NoError(t, (&PruneCmd{OlderThan: time.Hour, Force: true}).prune(context.Background(), fake.run, noTTY(&stderr), &stdout, &stderr))
	require.Equal(t, []daemon.PruneRequest{{OlderThan: time.Hour, Apply: true}}, fake.requests)
	require.Contains(t, stdout.String(), "deleted  a1")
	require.Empty(t, stderr.String())

	// --dry-run previews without a prompt.
	fake = &fakeJobPrune{eligible: old}
	stdout.Reset()
	require.NoError(t, (&PruneCmd{OlderThan: time.Hour, DryRun: true}).prune(context.Background(), fake.run, noTTY(&stderr), &stdout, &stderr))
	require.Equal(t, []daemon.PruneRequest{{OlderThan: time.Hour}}, fake.requests)
	require.Contains(t, stdout.String(), "would-delete  a1")
	require.Contains(t, stdout.String(), "nothing changed")
	require.Empty(t, stderr.String())
}

// TestPrunePreviewAndApplyAgainstIsolatedDaemon drives the real daemon through
// its private socket, with an isolated state directory and a Tart double.
func TestPrunePreviewAndApplyAgainstIsolatedDaemon(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "os-prune-") // Unix socket paths are short on macOS.
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(state) }()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	t.Setenv("OFFSCREEN_CONFIG", filepath.Join(state, "config.toml")) // no image, never the user's config
	bin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(bin, "tart"), []byte("#!/bin/sh\nif [ \"$1\" = list ]; then printf '[]\\n'; fi\n"), 0o700)) // #nosec G306 -- test executable
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte("project = 'hello'\n"), 0o600))

	server, err := daemon.Open(config.Default()) // no image: VM jobs fail without touching Tart
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan struct{})
	go func() { _ = server.Serve(ctx); close(served) }()
	defer func() { cancel(); <-served; require.NoError(t, server.Close()) }()
	socket, err := daemon.Socket()
	require.NoError(t, err)
	// The socket file appears at bind, before listen; wait for a connection.
	require.Eventually(t, func() bool {
		conn, err := net.Dial("unix", socket)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 2*time.Second, 10*time.Millisecond)
	client, err := daemon.Dial()
	require.NoError(t, err)

	submit := func(lane project.Lane) daemon.Job {
		j, err := client.Submit(context.Background(), daemon.Request{Root: root, Project: "hello", Argv: []string{"true"}, Lane: lane, Detach: true})
		require.NoError(t, err)
		return j
	}
	// VM jobs finish; the host job settles once it reports why it waits.
	settled := func(id string) daemon.Job {
		var j daemon.Job
		require.Eventually(t, func() bool {
			j, err = client.Job(context.Background(), id)
			return err == nil && (daemon.Terminal(j.State) || j.Lane == project.LaneHost && j.Reason != "")
		}, 5*time.Second, 20*time.Millisecond)
		return j
	}
	oldA, oldB, queued := submit(project.LaneVM), submit(project.LaneVM), submit(project.LaneHost)
	require.Equal(t, "failed", settled(oldA.ID).State)
	require.Equal(t, "failed", settled(oldB.ID).State)
	require.Equal(t, "queued", settled(queued.ID).State) // no host lane is configured
	time.Sleep(1500 * time.Millisecond)
	recent := submit(project.LaneVM)
	require.Equal(t, "failed", settled(recent.ID).State)

	db := filepath.Join(state, "jobs.sqlite")
	digest := func() [32]byte {
		b, err := os.ReadFile(db)
		require.NoError(t, err)
		return sha256.Sum256(b)
	}
	jobsBefore, err := client.Jobs(context.Background())
	require.NoError(t, err)
	dbBefore := digest()
	noTTY := confirmer{in: strings.NewReader(""), out: io.Discard}
	cmd := &PruneCmd{OlderThan: time.Second, DryRun: true}
	var stdout, stderr bytes.Buffer
	require.NoError(t, cmd.prune(context.Background(), client.Prune, noTTY, &stdout, &stderr))
	out := stdout.String()
	require.True(t, strings.HasPrefix(out, "cutoff  "), out)
	require.Contains(t, out, "would-delete  "+oldA.ID)
	require.Contains(t, out, "would-delete  "+oldB.ID)
	require.Contains(t, out, "skipped  "+queued.ID)
	require.Contains(t, out, "job is queued")
	require.NotContains(t, out, recent.ID)
	require.Contains(t, out, "summary  dry run: would delete 2, skipped 1, kept 1 newer; nothing changed")
	require.Empty(t, stderr.String())
	jobsAfter, err := client.Jobs(context.Background())
	require.NoError(t, err)
	require.Equal(t, jobsBefore, jobsAfter)
	require.Equal(t, dbBefore, digest(), "preview must not write the database")
	for _, j := range []daemon.Job{oldA, oldB, recent} {
		require.DirExists(t, j.Artifacts)
	}

	// A partial failure exits nonzero with retry guidance and keeps the job.
	locked := filepath.Join(oldB.Artifacts, "locked")
	require.NoError(t, os.MkdirAll(locked, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(locked, "file"), []byte("x"), 0o600))
	require.NoError(t, os.Chmod(locked, 0o500))    // #nosec G302 -- test makes a directory read-only
	defer func() { _ = os.Chmod(locked, 0o700) }() // #nosec G302 -- restore for cleanup
	stdout.Reset()
	stderr.Reset()
	cmd = &PruneCmd{OlderThan: time.Second, Force: true}
	err = cmd.prune(context.Background(), client.Prune, noTTY, &stdout, &stderr)
	require.ErrorContains(t, err, "1 job(s) not deleted")
	require.ErrorContains(t, err, "offscreen prune --older-than 1s -f")
	require.Contains(t, stdout.String(), "deleted  "+oldA.ID)
	require.Contains(t, stdout.String(), "summary  deleted 1, failed 1, skipped 1, kept 1 newer")
	require.Contains(t, stderr.String(), "prune: job "+oldB.ID+": remove job directory")
	require.NoDirExists(t, oldA.Artifacts)
	_, err = client.Job(context.Background(), oldA.ID)
	require.ErrorContains(t, err, "unknown job")
	_, err = client.Job(context.Background(), oldB.ID)
	require.NoError(t, err)

	require.NoError(t, os.Chmod(locked, 0o700)) // #nosec G302 -- restore
	stdout.Reset()
	stderr.Reset()
	require.NoError(t, cmd.prune(context.Background(), client.Prune, noTTY, &stdout, &stderr))
	require.Contains(t, stdout.String(), "deleted  "+oldB.ID)
	require.Empty(t, stderr.String())
	require.NoDirExists(t, oldB.Artifacts)
	_, err = client.Job(context.Background(), queued.ID)
	require.NoError(t, err)
	_, err = client.Cancel(context.Background(), queued.ID)
	require.NoError(t, err)
}
