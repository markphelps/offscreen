package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/project"
)

func TestImagePruneRequiresExplicitMode(t *testing.T) {
	for _, args := range [][]string{
		{"image", "prune"},
		{"image", "prune", "--dry-run", "--yes"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(args)
		require.ErrorContains(t, err, "exactly one of --dry-run", "%v", args)
	}
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"image", "prune", "--dry-run"})
	require.NoError(t, err)
	require.True(t, cli.Image.Prune.DryRun)
}

func TestActiveVMJobsIgnoresQueuedTerminalAndHostJobs(t *testing.T) {
	jobs := []daemon.Job{
		{ID: "booting", Lane: project.LaneVM, State: "booting"},
		{ID: "running", Lane: project.LaneVM, State: "running"},
		{ID: "queued", Lane: project.LaneVM, State: "queued"},
		{ID: "done", Lane: project.LaneVM, State: "done"},
		{ID: "failed", Lane: project.LaneVM, State: "failed"},
		{ID: "cancelled", Lane: project.LaneVM, State: "cancelled"},
		{ID: "host", Lane: project.LaneHost, State: "running"},
	}
	require.Equal(t, []string{"booting", "running"}, activeVMJobs(jobs))
}

func TestRequireNoActiveVMJobs(t *testing.T) {
	for _, tc := range []struct {
		name string
		jobs []daemon.Job
		err  error
		want string
	}{
		{name: "daemon down proceeds", err: fmt.Errorf("daemon transport: %w", daemon.ErrUnavailable)},
		{name: "idle proceeds", jobs: []daemon.Job{{ID: "q", Lane: project.LaneVM, State: "queued"}}},
		{name: "other error refuses", err: errors.New("daemon: 500"), want: "check active jobs"},
		{name: "active job refuses", jobs: []daemon.Job{{ID: "abc", Lane: project.LaneVM, State: "running"}}, want: "VM job(s) abc still active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := requireNoActiveVMJobs(context.Background(), func(context.Context) ([]daemon.Job, error) { return tc.jobs, tc.err })
			if tc.want == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.want)
			}
		})
	}
}

func TestPrintImagePrune(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := printImagePrune(image.PruneReport{
		Current:  "cur",
		Eligible: []image.PruneEntry{{Name: "old"}},
		Skipped:  []image.PruneEntry{{Name: "busy", Reason: "running"}},
	}, &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "current  cur  (kept)\nwould-delete  old\nskipped  busy  running\nsummary  dry run: would delete 1, skipped 1; nothing changed\n", stdout.String())
	require.Empty(t, stderr.String())

	stdout.Reset()
	err = printImagePrune(image.PruneReport{
		Current: "cur",
		Apply:   true,
		Deleted: []image.PruneEntry{{Name: "old"}},
		Failed:  []image.PruneEntry{{Name: "stuck", Reason: "boom"}},
	}, &stdout, &stderr)
	require.ErrorContains(t, err, "1 image(s) not deleted")
	require.Contains(t, stdout.String(), "deleted  old\n")
	require.Contains(t, stdout.String(), "summary  deleted 1, failed 1, skipped 0\n")
	require.Equal(t, "image prune: stuck: boom\n", stderr.String())
}
