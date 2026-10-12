package config

import (
	"os"
	"path/filepath"
	"strings"
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

func TestSetCurrentImagePreservesTOMLExpressions(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, assignment string
	}{
		{"multiline basic", "extra = \"\"\"\n[not-a-table]\ncurrent_image = 'decoy'\n\"\"\"\n", "current_image = 'old' # keep this comment\n"},
		{"multiline literal", "extra = '''\n[not-a-table]\ncurrent_image = 'decoy'\n'''\n", "current_image = 'old'\n"},
		{"array", "extra = [\n  '''[not-a-table]''',\n  \"current_image = 'decoy'\",\n]\n", "current_image = 'old'\n"},
		{"nested array", "extra = [\n  [1, 2],\n  [3],\n]\n", "current_image = 'old'\n"},
		{"escaped key", "", "\"current_\\u0069mage\" = 'old' # keep spacing\n"},
		{"multiline image", "", "current_image = \"\"\"old\n[not-a-table]\n\"\"\" # keep comment\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			const tail = "# image settings\n[image]\ndisplay = '1200x800'\ncurrent_image = 'nested'\n"
			original := tc.prefix + tc.assignment + tail
			require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
			_, err := LoadFile(path)
			require.NoError(t, err, "fixture must be valid configuration")
			require.NoError(t, SetCurrentImageFile(path, "new"))
			cfg, err := LoadFile(path)
			require.NoError(t, err)
			require.Equal(t, "new", cfg.CurrentImage)
			contents, err := os.ReadFile(path)
			require.NoError(t, err)
			oldValue := "'old'"
			if tc.name == "multiline image" {
				oldValue = "\"\"\"old\n[not-a-table]\n\"\"\""
			}
			expected := tc.prefix + strings.Replace(tc.assignment, oldValue, "\"new\"", 1) + tail
			require.Equal(t, expected, string(contents))
		})
	}
}

func TestSetCurrentImageInsertsAfterMultilineString(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const prefix = "extra = '''\n[not-a-table]\ncurrent_image = 'decoy'\n'''\n"
	const table = "[image]\ndisplay = '1200x800'\n"
	require.NoError(t, os.WriteFile(path, []byte(prefix+table), 0o600))
	require.NoError(t, SetCurrentImageFile(path, "new"))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, prefix+"current_image = \"new\"\n"+table, string(contents))
	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new", cfg.CurrentImage)
}

func TestSetCurrentImagePreservesLineEndingsAndEscapes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "# café\r\n  'current_image'\t=\t'old'  # comment\r\n[image]\r\ndisplay = '1200x800'\r\n"
	const name = "image\a\v\n\"\\雪\x7f"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	require.NoError(t, SetCurrentImageFile(path, name))
	cfg, err := LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, name, cfg.CurrentImage)
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	const encoded = `"image\u0007\u000b\n\"\\雪\u007f"`
	require.Equal(t, strings.Replace(original, "'old'", encoded, 1), string(contents))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSetCurrentImageRejectsMalformedConfigWithoutChangingIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "extra = '''\n[not-a-table]\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	require.Error(t, SetCurrentImageFile(path, "new"))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(contents))
}

func TestSetCurrentImageRejectsInvalidUTF8WithoutChangingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	const original = "current_image = 'old'\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	require.Error(t, SetCurrentImageFile(path, "image\xff"))
	contents, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, string(contents))
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
