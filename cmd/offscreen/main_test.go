package main

import (
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"
)

func TestRunCommandParsesTaskAndFlags(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	ctx, err := parser.Parse([]string{"run", "sample", "--lane", "host", "--timeout", "10s", "--keep", "--worktree", "/tmp", "--detach", "test-ui"})
	require.NoError(t, err)
	require.Equal(t, "run <project> <task-or-command>", ctx.Command())
	require.Equal(t, "sample", cli.Run.Project)
	require.Equal(t, []string{"test-ui"}, cli.Run.Args)
	require.Equal(t, "host", cli.Run.Lane)
	require.Equal(t, 10*time.Second, cli.Run.Timeout)
	require.True(t, cli.Run.Keep)
	require.Equal(t, "/tmp", cli.Run.Worktree)
	require.True(t, cli.Run.Detach)
}

func TestRunCommandAcceptsFlagsAfterTask(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"run", "sample", "test-ui", "--lane", "host"})
	require.NoError(t, err)
	require.Equal(t, "host", cli.Run.Lane)
	require.Equal(t, []string{"test-ui"}, cli.Run.Args)
}

func TestRunCommandPassesThroughRawCommand(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"run", "sample", "--", "sh", "-c", "echo hi; exit 3"})
	require.NoError(t, err)
	require.Equal(t, []string{"sh", "-c", "echo hi; exit 3"}, cli.Run.Args)
	require.ErrorContains(t, execute([]string{"run", "sample", "--", "sh", "-c", "echo hi; exit 3"}), "not implemented")
}

func TestCommandsAreAvailable(t *testing.T) {
	for _, args := range [][]string{
		{"status"},
		{"logs", "job-1", "--follow"},
		{"artifacts", "job-1"},
		{"cancel", "job-1"},
		{"pause"},
		{"resume"},
	} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		ctx, err := parser.Parse(args)
		require.NoError(t, err, "%v", args)
		require.ErrorContains(t, ctx.Run(), "not implemented")
	}
}

func TestImageCommandsParse(t *testing.T) {
	for _, args := range [][]string{{"image", "build"}, {"image", "refresh"}} {
		var cli CLI
		parser, err := kong.New(&cli)
		require.NoError(t, err)
		ctx, err := parser.Parse(args)
		require.NoError(t, err)
		require.Contains(t, ctx.Command(), "image ")
	}
}

func TestInvalidLane(t *testing.T) {
	var cli CLI
	parser, err := kong.New(&cli)
	require.NoError(t, err)
	_, err = parser.Parse([]string{"run", "sample", "--lane", "invalid", "task"})
	require.Error(t, err)
}
