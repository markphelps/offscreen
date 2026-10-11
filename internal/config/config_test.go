package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	require.Empty(t, cfg.CurrentImage)
	require.Equal(t, 5*time.Minute, cfg.IdleThreshold)
	require.Equal(t, time.Minute, cfg.Grace)
	require.Equal(t, 2, cfg.VMSlots)
	require.Equal(t, 8, cfg.LowMemoryThresholdGB)
	require.Equal(t, defaultBase, cfg.Image.Base)
	require.Equal(t, "1440x900", cfg.Image.Display)
	require.Equal(t, "admin", cfg.Image.SSHUser)
	require.Equal(t, "admin", cfg.Image.SSHPassword)
	require.Empty(t, cfg.Image.SSHKey)
	require.Empty(t, cfg.Image.Setup)
}

func TestLoadFileMissingAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, Default(), cfg)

	const partial = "idle_threshold = '30s'\nvm_slots = 1\n[image]\nssh_key = '~/.ssh/id_ed25519'\n"
	require.NoError(t, os.WriteFile(path, []byte(partial), 0o600))
	cfg, err = LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, cfg.IdleThreshold)
	require.Equal(t, 1, cfg.VMSlots)
	require.Equal(t, time.Minute, cfg.Grace)
	require.Equal(t, defaultBase, cfg.Image.Base)
	require.Equal(t, "~/.ssh/id_ed25519", cfg.Image.SSHKey)
}

func TestSaveFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	want := Default()
	want.CurrentImage = "custom-image"
	want.Grace = 90 * time.Second
	want.Image.Setup = []string{"echo ready", "echo done"}
	require.NoError(t, SaveFile(path, want))

	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), "grace = \"1m30s\"")
	require.Contains(t, string(contents), "[image]")
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	got, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestLoadFileRejectsInvalidDuration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("grace = 'tomorrow'\n"), 0o600))
	_, err := LoadFile(path)
	require.ErrorContains(t, err, "invalid grace")
}
