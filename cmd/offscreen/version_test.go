package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/exitcode"
)

func versionOutput(t *testing.T) string {
	t.Helper()
	client, err := daemon.Dial()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, printVersion(context.Background(), client, &out))
	return out.String()
}

func TestVersionWithCurrentDaemon(t *testing.T) {
	fakeStreamDaemon(t, commandStream)
	require.Equal(t, "client: dev (protocol 1)\ndaemon: test (protocol 1)\n", versionOutput(t))
}

func TestVersionWithPreNegotiationDaemon(t *testing.T) {
	fakeDaemon(t, nil, commandStream)
	require.Equal(t, "client: dev (protocol 1)\ndaemon: protocol 0 (pre-negotiation)\n", versionOutput(t))
}

func TestVersionWithDaemonDown(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "os-none-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	require.Equal(t, "client: dev (protocol 1)\ndaemon: not running\n", versionOutput(t))
}

func runStatus(t *testing.T) (stdout, stderr string) {
	t.Helper()
	client, err := daemon.Dial()
	require.NoError(t, err)
	var out, errOut bytes.Buffer
	require.NoError(t, (&StatusCmd{Quiet: true}).run(context.Background(), client, &out, &errOut))
	return out.String(), errOut.String()
}

func TestStatusWarnsOnceAboutOlderCompatibleDaemon(t *testing.T) {
	fakeDaemon(t, nil, commandStream)
	stdout, stderr := runStatus(t)
	require.Equal(t, "fake\n", stdout)
	require.Equal(t, 1, strings.Count(stderr, "\n"), "one warning line: %q", stderr)
	require.True(t, strings.HasPrefix(stderr, "warning: "))
	require.Contains(t, stderr, "Restart Daemon in the Offscreen menu")
}

func TestStatusDoesNotWarnAboutCurrentDaemon(t *testing.T) {
	fakeStreamDaemon(t, commandStream)
	stdout, stderr := runStatus(t)
	require.Equal(t, "fake\n", stdout)
	require.Empty(t, stderr)
}

func TestCommandsNeedingDaemonTellUserToOpenOffscreen(t *testing.T) {
	state, err := os.MkdirTemp("/tmp", "os-none-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	for _, args := range [][]string{{"status"}, {"cancel", "0123456789abcdef"}} {
		var stderr bytes.Buffer
		code := report(execute(args), &stderr)
		require.Equal(t, exitcode.RunnerFailure, code, args)
		require.Equal(t, "Offscreen isn't running. Open it with: open -a Offscreen\n", stderr.String(), args)
	}
}
