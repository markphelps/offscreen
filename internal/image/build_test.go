package image

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExpandHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := expandHome("~/setup/my script.sh")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, "setup/my script.sh"), got)
	got, err = expandHome("/tmp/setup.sh")
	require.NoError(t, err)
	require.Equal(t, "/tmp/setup.sh", got)
}

func TestEmbeddedScriptsExist(t *testing.T) {
	for _, path := range []string{"scripts/check.sh", "scripts/automation.sh", "scripts/setup.sh"} {
		contents, err := scripts.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(contents), "set -euo pipefail")
	}
	_, err := os.Stat("scripts/check.sh")
	require.NoError(t, err)
}
