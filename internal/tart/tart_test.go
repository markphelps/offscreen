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
