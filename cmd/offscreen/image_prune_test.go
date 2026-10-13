package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/project"
)

func TestImagePruneFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		err  string
	}{
		{[]string{"image", "prune", "--dry-run", "-f"}, "can't be used together"},
		{[]string{"image", "prune", "--yes"}, "unknown flag --yes"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(tc.args)
		require.ErrorContains(t, err, tc.err, "%v", tc.args)
	}
	for _, args := range [][]string{
		{"image", "prune"},
		{"image", "prune", "--dry-run"},
		{"image", "prune", "-f"},
		{"image", "prune", "--force"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(args)
		require.NoError(t, err, "%v", args)
	}
}

// fakeImagePrune records each apply flag and confirmed selection and
// answers with a preview or an applied report over the same images.
type fakeImagePrune struct {
	applies   []bool
	confirmed [][]string
	eligible  []image.PruneEntry
}

func (f *fakeImagePrune) run(_ context.Context, apply bool, confirmed []string) (image.PruneReport, error) {
	f.applies = append(f.applies, apply)
	f.confirmed = append(f.confirmed, confirmed)
	report := image.PruneReport{Current: "cur", Apply: apply, Eligible: f.eligible}
	if apply {
		report.Deleted = f.eligible
	}
	return report, nil
}

func TestImagePruneAsksOnTerminal(t *testing.T) {
	old := []image.PruneEntry{{Name: "old"}}
	idle := func(context.Context) error { return nil }
	for _, tc := range []struct {
		name     string
		answer   string
		eligible []image.PruneEntry
		guard    func(context.Context) error
		applies  []bool
		prompted bool
		stdout   string
		err      string
	}{
		{name: "yes deletes", answer: "yes\n", eligible: old, guard: idle, prompted: true, applies: []bool{false, true}, stdout: "current  cur  (kept)\ndeleted  old\nsummary  deleted 1, failed 0, skipped 0\n"},
		{name: "no keeps", answer: "no\n", eligible: old, guard: idle, prompted: true, applies: []bool{false}, stdout: "summary  cancelled; nothing changed\n"},
		{name: "nothing selected skips prompt", guard: idle, applies: []bool{false}, stdout: "summary  nothing to delete, skipped 0\n"},
		{name: "active VM job refuses before asking", answer: "y\n", eligible: old, guard: func(context.Context) error { return errors.New("image prune: VM job(s) abc still active") }, applies: []bool{false}, err: "VM job(s) abc still active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeImagePrune{eligible: tc.eligible}
			var stdout, stderr bytes.Buffer
			prompt := confirmer{in: strings.NewReader(tc.answer), out: &stderr, terminal: true}
			err := (&ImagePruneCmd{}).prune(context.Background(), fake.run, tc.guard, prompt, &stdout, &stderr)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.applies, fake.applies)
			if len(tc.applies) == 2 {
				// Only the images listed before the prompt may be deleted.
				require.Equal(t, [][]string{nil, {"old"}}, fake.confirmed)
			}
			require.Equal(t, tc.stdout, stdout.String())
			require.Contains(t, stderr.String(), "current  cur  (kept)\n")
			if tc.prompted {
				require.Contains(t, stderr.String(), "would-delete  old\nWARNING! This will delete 1 golden image(s) from Tart.\nAre you sure you want to continue? [y/N] ")
			} else {
				require.NotContains(t, stderr.String(), "[y/N]")
			}
		})
	}
}

func TestImagePruneWithoutTerminal(t *testing.T) {
	old := []image.PruneEntry{{Name: "old"}}
	guard := func(context.Context) error { return nil }
	var stdout, stderr bytes.Buffer
	noTTY := confirmer{in: strings.NewReader("y\n"), out: &stderr}

	fake := &fakeImagePrune{eligible: old}
	err := (&ImagePruneCmd{}).prune(context.Background(), fake.run, guard, noTTY, &stdout, &stderr)
	require.ErrorContains(t, err, "image prune: stdin is not a terminal")
	require.ErrorContains(t, err, "pass -f to delete without asking")
	require.Empty(t, fake.applies)
	require.Empty(t, stdout.String())
	require.Empty(t, stderr.String())

	fake = &fakeImagePrune{eligible: old}
	require.NoError(t, (&ImagePruneCmd{Force: true}).prune(context.Background(), fake.run, guard, noTTY, &stdout, &stderr))
	require.Equal(t, []bool{true}, fake.applies)
	require.Equal(t, [][]string{nil}, fake.confirmed)
	require.Equal(t, "current  cur  (kept)\ndeleted  old\nsummary  deleted 1, failed 0, skipped 0\n", stdout.String())
	require.Empty(t, stderr.String())

	fake = &fakeImagePrune{eligible: old}
	stdout.Reset()
	require.NoError(t, (&ImagePruneCmd{DryRun: true}).prune(context.Background(), fake.run, guard, noTTY, &stdout, &stderr))
	require.Equal(t, []bool{false}, fake.applies)
	require.Equal(t, "current  cur  (kept)\nwould-delete  old\nsummary  dry run: would delete 1, skipped 0; nothing changed\n", stdout.String())
	require.Empty(t, stderr.String())
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
	require.ErrorContains(t, err, "offscreen image prune -f")
	require.Contains(t, stdout.String(), "deleted  old\n")
	require.Contains(t, stdout.String(), "summary  deleted 1, failed 1, skipped 0\n")
	require.Equal(t, "image prune: stuck: boom\n", stderr.String())
}
