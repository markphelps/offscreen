package image

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrimBuildLogsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	// Names sort opposite to age, so only modification time can order them.
	var names []string
	for i := range 12 {
		name := fmt.Sprintf("offscreen-build-%012x.log", 100-i)
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("log"), 0o600))
		modTime := base.Add(time.Duration(i) * time.Minute)
		require.NoError(t, os.Chtimes(path, modTime, modTime))
		names = append(names, name)
	}
	for _, name := range []string{"notes.txt", "offscreen-build-old.txt", "other.log"} {
		path := filepath.Join(dir, name)
		require.NoError(t, os.WriteFile(path, []byte("keep"), 0o600))
		require.NoError(t, os.Chtimes(path, base.Add(-time.Hour), base.Add(-time.Hour)))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "offscreen-build-dir.log"), 0o700))

	require.NoError(t, trimBuildLogs(dir))

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	var got []string
	for _, entry := range entries {
		got = append(got, entry.Name())
	}
	want := append([]string{"notes.txt", "offscreen-build-dir.log", "offscreen-build-old.txt", "other.log"}, names[2:]...)
	require.ElementsMatch(t, want, got)
}

func TestTrimBuildLogsUnderLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "offscreen-build-abc.log")
	require.NoError(t, os.WriteFile(path, []byte("log"), 0o600))
	require.NoError(t, trimBuildLogs(dir))
	require.FileExists(t, path)
}

func TestTrimBuildLogsMissingDir(t *testing.T) {
	require.NoError(t, trimBuildLogs(filepath.Join(t.TempDir(), "image-builds")))
}
