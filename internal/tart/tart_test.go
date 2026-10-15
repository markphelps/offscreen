package tart

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeTart records each invocation's arguments, one line per call.
func fakeTart(t *testing.T) (Client, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "args.log")
	binary := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\necho \"$*\" >> '"+log+"'\n"), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- private test executable
	return Client{Binary: binary}, log
}

func TestStartSharedIsHeadlessUnlessShown(t *testing.T) {
	for _, tc := range []struct {
		name string
		show bool
		want string
	}{
		{"headless", false, "run --no-graphics job-1 --dir=worktree:/src:ro"},
		{"shown", true, "run job-1 --dir=worktree:/src:ro"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, log := fakeTart(t)
			cmd, err := client.StartShared(context.Background(), "job-1", []string{"worktree:/src:ro"}, tc.show)
			require.NoError(t, err)
			require.NoError(t, cmd.Wait())
			got, err := os.ReadFile(log)
			require.NoError(t, err)
			require.Equal(t, tc.want, strings.TrimSpace(string(got)))
		})
	}
}

// scriptTart returns a client whose binary runs body as a shell script.
func scriptTart(t *testing.T, body string) Client {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "tart")
	require.NoError(t, os.WriteFile(binary, []byte("#!/bin/sh\n"+body), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- private test executable
	return Client{Binary: binary}
}

func TestPullStreamsOutputWithCIAndKeepsItOutOfTheError(t *testing.T) {
	client := scriptTart(t, `echo "pulling $2 CI=$CI"; echo "50%" >&2; echo "100%"; exit 3`+"\n")
	var out strings.Builder
	err := client.Pull(context.Background(), "base:latest", &out)
	require.Equal(t, "pulling base:latest CI=1\n50%\n100%\n", out.String())
	require.EqualError(t, err, "tart pull base:latest: exit status 3")
}

func TestPullAcceptsNilOutput(t *testing.T) {
	client := scriptTart(t, "echo hidden\n")
	require.NoError(t, client.Pull(context.Background(), "base", nil))
}

func TestMissingBinaryReportsNotInstalled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		binary string
	}{
		{"path", filepath.Join(t.TempDir(), "tart")},
		{"bare name", "offscreen-test-missing-tart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := Client{Binary: tc.binary}
			_, listErr := client.List(context.Background())
			pullErr := client.Pull(context.Background(), "base", nil)
			_, startErr := client.StartShared(context.Background(), "job-1", nil, false)
			for _, err := range []error{listErr, pullErr, startErr} {
				require.ErrorIs(t, err, ErrNotInstalled)
				require.ErrorContains(t, err, "tart isn't installed or isn't on PATH")
				require.ErrorContains(t, err, "install it with: brew install openai/tools/tart")
			}
			require.True(t, strings.HasPrefix(listErr.Error(), "tart list --format json: "))
		})
	}
}

func TestFailingTartIsNotReportedAsNotInstalled(t *testing.T) {
	client := scriptTart(t, "echo 'no such file or directory' >&2; exit 1\n")
	err := client.Clone(context.Background(), "a", "b")
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrNotInstalled)
	require.EqualError(t, err, "tart clone a b: exit status 1: no such file or directory")
}

func TestCommandErrorKeepsOnlyTheLastLines(t *testing.T) {
	client := scriptTart(t, "i=1; while [ $i -le 100 ]; do echo \"line $i\"; i=$((i+1)); done; exit 1\n")
	err := client.Clone(context.Background(), "a", "b")
	require.Error(t, err)
	require.Contains(t, err.Error(), "line 81\nline 82")
	require.True(t, strings.HasSuffix(err.Error(), "line 100"))
	require.NotContains(t, err.Error(), "line 80\n")
}
