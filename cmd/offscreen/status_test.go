package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/project"
)

var listNow = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

func sampleJobs() []daemon.Job {
	return []daemon.Job{
		{ID: "aaaa000000000001", Project: "hello", Task: "test-ui", Lane: project.LaneVM, State: "done", Created: listNow.Add(-3 * time.Hour)},
		{ID: "aaaa000000000002", Project: "hello", Task: "", Lane: project.LaneVM, State: "running", Created: listNow.Add(-5 * time.Minute)},
		{ID: "aaaa000000000003", Project: "longer-project", Task: "lint", Lane: project.LaneHost, State: "queued", Created: listNow.Add(-30 * time.Second), Reason: "waiting for host lane"},
	}
}

func printStatus(t *testing.T, args ...string) string {
	t.Helper()
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse(append([]string{"status"}, args...))
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, cli.Status.print(&out, sampleJobs(), listNow))
	return out.String()
}

func TestStatusDefaultTableShowsQueuedAndActiveJobsNewestFirst(t *testing.T) {
	want := "" +
		"JOB ID             PROJECT          TASK   LANE   STATE     CREATED          REASON\n" +
		"aaaa000000000003   longer-project   lint   host   queued    30 seconds ago   waiting for host lane\n" +
		"aaaa000000000002   hello                   vm     running   5 minutes ago    \n"
	require.Equal(t, want, printStatus(t))
}

func TestStatusAllIncludesCompletedJobs(t *testing.T) {
	out := printStatus(t, "-a")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 4)
	require.True(t, strings.HasPrefix(lines[3], "aaaa000000000001   hello            test-ui   vm     done      3 hours ago"), lines[3])
}

func TestStatusQuietPrintsOnlyIDs(t *testing.T) {
	require.Equal(t, "aaaa000000000003\naaaa000000000002\n", printStatus(t, "-q"))
	require.Equal(t, "aaaa000000000003\naaaa000000000002\naaaa000000000001\n", printStatus(t, "--all", "--quiet", "--format", "json"))
}

func TestStatusJSONPrintsOneObjectPerJob(t *testing.T) {
	out := printStatus(t, "--format", "json")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	require.Len(t, lines, 2)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &row))
	require.Equal(t, "aaaa000000000003", row["ID"])
	require.Equal(t, "waiting for host lane", row["Reason"])
	require.Equal(t, "30 seconds ago", row["Created"])
	require.Equal(t, listNow.Add(-30*time.Second).Format(time.RFC3339), row["CreatedAt"])
}

func TestStatusTemplateFormats(t *testing.T) {
	require.Equal(t, "aaaa000000000003\naaaa000000000002\n", printStatus(t, "--format", "{{.ID}}"))
	require.Equal(t, "aaaa000000000003 queued\naaaa000000000002 running\n", printStatus(t, "--format", "{{.ID}} {{.State}}"))
	want := "" +
		"JOB ID             STATE\n" +
		"aaaa000000000003   queued\n" +
		"aaaa000000000002   running\n"
	require.Equal(t, want, printStatus(t, "--format", `table {{.ID}}\t{{.State}}`))
}

func TestStatusRejectsBadTemplates(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	for _, format := range []string{"{{.ID", "{{.Nope}}"} {
		_, err = parser.Parse([]string{"status", "--format", format})
		require.NoError(t, err)
		require.ErrorContains(t, cli.Status.print(&bytes.Buffer{}, sampleJobs(), listNow), "format:", format)
	}
}

func TestStatusEmptyTablePrintsHeaders(t *testing.T) {
	var out bytes.Buffer
	require.NoError(t, (&StatusCmd{}).print(&out, nil, listNow))
	require.Equal(t, "JOB ID   PROJECT   TASK   LANE   STATE   CREATED   REASON\n", out.String())
}

func TestImageLsMarksCurrentImage(t *testing.T) {
	inv := image.Inventory{Current: "offscreen-20261013-xcode27.0-aaaaaaaaaaaa", Images: []image.Golden{
		{Name: "offscreen-20261013-xcode27.0-aaaaaaaaaaaa", Current: true, Created: time.Date(2026, 10, 13, 0, 0, 0, 0, time.Local), SizeGB: 73},
		{Name: "offscreen-20260901-xcode26.0-bbbbbbbbbbbb", Created: time.Date(2026, 9, 1, 0, 0, 0, 0, time.Local), SizeGB: 70},
		{Name: "my-golden"},
	}}
	run := func(args ...string) string {
		t.Helper()
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		_, err = parser.Parse(append([]string{"image", "ls"}, args...))
		require.NoError(t, err)
		var out bytes.Buffer
		require.NoError(t, cli.Image.Ls.print(&out, inv))
		return out.String()
	}
	want := "" +
		"NAME                                        CURRENT   CREATED      SIZE\n" +
		"offscreen-20261013-xcode27.0-aaaaaaaaaaaa   *         2026-10-13   73 GB\n" +
		"offscreen-20260901-xcode26.0-bbbbbbbbbbbb             2026-09-01   70 GB\n" +
		"my-golden                                                          \n"
	require.Equal(t, want, run())
	require.Equal(t, "offscreen-20261013-xcode27.0-aaaaaaaaaaaa\noffscreen-20260901-xcode26.0-bbbbbbbbbbbb\nmy-golden\n", run("-q"))
	require.Equal(t, "offscreen-20261013-xcode27.0-aaaaaaaaaaaa true\noffscreen-20260901-xcode26.0-bbbbbbbbbbbb false\nmy-golden false\n", run("--format", "{{.Name}} {{.Current}}"))
	lines := strings.Split(strings.TrimSpace(run("--format", "json")), "\n")
	require.Len(t, lines, 3)
	var row map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &row))
	require.Equal(t, true, row["Current"])
	require.InDelta(t, 73, row["SizeGB"], 0)
}

func TestHumanSince(t *testing.T) {
	for _, tc := range []struct {
		ago  time.Duration
		want string
	}{
		{0, "Less than a second ago"},
		{time.Second, "1 second ago"},
		{45 * time.Second, "45 seconds ago"},
		{90 * time.Second, "About a minute ago"},
		{59 * time.Minute, "59 minutes ago"},
		{70 * time.Minute, "About an hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{3 * 24 * time.Hour, "3 days ago"},
		{20 * 24 * time.Hour, "2 weeks ago"},
		{90 * 24 * time.Hour, "3 months ago"},
		{800 * 24 * time.Hour, "2 years ago"},
	} {
		require.Equal(t, tc.want, humanSince(listNow, listNow.Add(-tc.ago)), tc.ago)
	}
	require.Empty(t, humanSince(listNow, time.Time{}))
}
