package run

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/project"
)

func TestPrepareUsesSubmissionEnvironmentWithoutChangingDaemonPath(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "prepared")
	fakeBin := t.TempDir()
	fakeTart := filepath.Join(fakeBin, "tart")
	require.NoError(t, os.WriteFile(fakeTart, []byte("#!/bin/sh\nexit 42\n"), 0o700)) // #nosec G306 -- private test executable
	t.Setenv("PATH", fakeBin+":/usr/bin:/bin")
	t.Setenv("FOO", "daemon-value")
	t.Setenv("OFFSCREEN_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	cfg := config.Default()
	cfg.CurrentImage = "test-image"
	contract := project.Contract{Root: root, Project: "hello", Prepare: project.Prepare{
		Run:   "printf '%s' \"$FOO\" > prepared",
		Share: []string{"$BUILT_PRODUCTS_DIR/App.app"},
	}}
	hostEnv := map[string]string{"PATH": "/usr/bin:/bin", "FOO": "caller=value", "BUILT_PRODUCTS_DIR": filepath.Join(root, "build")}
	result := Execute(context.Background(), Options{Config: cfg, Contract: contract, Argv: []string{"true"}, HostEnv: hostEnv})
	require.ErrorContains(t, result.Err, "tart clone") // Fake Tart is still found through the daemon's PATH.
	b, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "caller=value", string(b))
	require.Equal(t, fakeBin+":/usr/bin:/bin", os.Getenv("PATH"))
	require.Equal(t, "daemon-value", os.Getenv("FOO"))
	require.Equal(t, []string{"BUILT_PRODUCTS_DIR=" + filepath.Join(root, "build"), "FOO=caller=value", "PATH=/usr/bin:/bin"}, hostCommandEnv(hostEnv))
}

func TestStandaloneNilHostEnvironmentFallsBackToCurrentEnvironment(t *testing.T) {
	t.Setenv("OFFSCREEN_STANDALONE_SHARE", t.TempDir())
	fakeBin := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(fakeBin, "tart"), []byte("#!/bin/sh\nexit 42\n"), 0o700)) // #nosec G306 -- private test executable
	t.Setenv("PATH", fakeBin+":/usr/bin:/bin")
	cfg := config.Default()
	cfg.CurrentImage = "test-image"
	contract := project.Contract{Root: t.TempDir(), Project: "hello", Prepare: project.Prepare{Share: []string{"$OFFSCREEN_STANDALONE_SHARE/App.app"}}}
	result := Execute(context.Background(), Options{Config: cfg, Contract: contract, Argv: []string{"true"}})
	require.ErrorContains(t, result.Err, "tart clone")
}
