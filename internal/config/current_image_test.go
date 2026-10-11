package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetCurrentImagePreservesUserFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "# user's settings\ncurrent_image = 'old'\nextra_future_setting = 42\n[image]\nsetup = ['~/tools.sh']\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	require.NoError(t, SetCurrentImageFile(path, "offscreen-20261011-xcode27-aaa"))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(contents), "# user's settings")
	require.Contains(t, string(contents), "extra_future_setting = 42")
	require.Contains(t, string(contents), "setup = ['~/tools.sh']")
	require.NotContains(t, string(contents), "'old'")
	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "offscreen-20261011-xcode27-aaa", cfg.CurrentImage)
}

func TestSetCurrentImageRecognizesIndentedAndQuotedKeys(t *testing.T) {
	for _, key := range []string{"  current_image", "\t\"current_image\"", "  'current_image'"} {
		t.Run(key, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			original := "# keep this comment\n" + key + " = 'old' # previous image\nfuture_setting = 42\n[image]\ndisplay = '1200x800'\n"
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			require.NoError(t, SetCurrentImageFile(path, "new"))
			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Contains(t, string(contents), "# keep this comment")
			require.Contains(t, string(contents), "future_setting = 42")
			require.Contains(t, string(contents), "[image]\ndisplay = '1200x800'")
			require.NotContains(t, string(contents), "'old'")
			cfg, err := LoadFile(path)
			require.NoError(t, err, "rewritten config must remain valid TOML")
			require.Equal(t, "new", cfg.CurrentImage)
			require.Equal(t, "1200x800", cfg.Image.Display)
		})
	}
}

func TestSetCurrentImageInsertsBeforeImageTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte("[image]\ndisplay = '1200x800'\n"), 0o600))
	require.NoError(t, SetCurrentImageFile(path, "new"))
	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new", cfg.CurrentImage)
	require.Equal(t, "1200x800", cfg.Image.Display)
}
