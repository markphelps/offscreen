package main

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/alecthomas/kong"
	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
)

func TestHostEnvironmentPreservesValuesContainingEquals(t *testing.T) {
	require.Equal(t, map[string]string{"PATH": "/caller/bin", "FOO": "a=b=c"}, hostEnvironment([]string{"PATH=/caller/bin", "FOO=a=b=c", "malformed"}))
}

func TestRunSubmissionCapturesCallerEnvironment(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "os-cli-")
	require.NoError(t, err)
	defer func() { _ = os.RemoveAll(state) }()
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	t.Setenv("OFFSCREEN_CALLER_VALUE", "left=right")
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".offscreen.toml"), []byte("project = 'hello'\n"), 0o600))
	listener, err := net.Listen("unix", filepath.Join(state, "offscreend.sock"))
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()
	received := make(chan daemon.Request, 1)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req daemon.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		received <- req
		_ = json.NewEncoder(w).Encode(daemon.Job{ID: "fake", Artifacts: root})
	})}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()
	require.NoError(t, (&RunCmd{Project: "hello", Args: []string{"test-ui"}, Worktree: root, Detach: true}).Run())
	select {
	case req := <-received:
		require.Equal(t, "left=right", req.HostEnv["OFFSCREEN_CALLER_VALUE"])
		require.Equal(t, "test-ui", req.Task)
	case <-time.After(time.Second):
		t.Fatal("submission was not received")
	}
}

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
	require.ErrorContains(t, execute([]string{"run", "sample", "--", "sh", "-c", "echo hi; exit 3"}), "not inside an offscreen project")
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
		if args[0] == "pause" || args[0] == "resume" {
			require.ErrorContains(t, ctx.Run(), "host lane unavailable")
		}
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
