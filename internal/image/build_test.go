package image

import (
	"os"
	"path/filepath"
	"strings"
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
	for _, path := range []string{"scripts/check.sh", "scripts/automation.sh", "scripts/setup.sh", "scripts/display.sh", "scripts/desktop.sh"} {
		contents, err := scripts.ReadFile(path)
		require.NoError(t, err)
		require.Contains(t, string(contents), "set -euo pipefail")
	}
	_, err := scripts.ReadFile("scripts/display.swift")
	require.NoError(t, err)
	_, err = os.Stat("scripts/check.sh")
	require.NoError(t, err)
}

func TestParseDisplay(t *testing.T) {
	for _, test := range []struct {
		input string
		want  displaySpec
	}{
		{"1440x900", displaySpec{1440, 900, "pt"}},
		{"1440x900pt", displaySpec{1440, 900, "pt"}},
		{"2880x1800px", displaySpec{2880, 1800, "px"}},
	} {
		t.Run(test.input, func(t *testing.T) {
			got, err := parseDisplay(test.input)
			require.NoError(t, err)
			require.Equal(t, test.want, got)
		})
	}
	for _, input := range []string{"0x900", "1440x0px", "2x3p", "1440x900; touch /tmp/unsafe", strings.Repeat("9", 40) + "x900pt"} {
		t.Run(input, func(t *testing.T) {
			_, err := parseDisplay(input)
			require.ErrorContains(t, err, "invalid image display")
		})
	}
}
