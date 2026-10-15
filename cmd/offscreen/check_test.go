package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
)

func writeContract(t *testing.T, contents string) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, project.FileName), []byte(contents), 0o600))
	return root
}

func TestCheckListsTasksWithLanes(t *testing.T) {
	root := writeContract(t, "project = 'hello'\n[tasks.ui]\nlane = 'vm'\nrun = 'sh ui.sh'\n[tasks.a-host]\nlane = 'host'\nrun = 'true'\n")
	contract, err := project.Find("", root)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, printContract(contract, &out))
	require.Equal(t, filepath.Join(root, project.FileName)+": ok\nproject: hello\ntasks:\n  a-host  host\n  ui      vm\n", out.String())
}

func TestCheckReportsNoTasks(t *testing.T) {
	root := writeContract(t, "project = 'hello'\n")
	contract, err := project.Find("", root)
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, printContract(contract, &out))
	require.Contains(t, out.String(), "project: hello\ntasks: none;")
}

func TestCheckFailsWithConfigErrorAndExitsOne(t *testing.T) {
	for name, tc := range map[string]struct {
		contents string
		want     string
	}{
		"unknown key": {"project = 'hello'\nbogus = 1\n", "unknown .offscreen.toml keys: bogus"},
		"bad lane":    {"project = 'hello'\n[tasks.ui]\nlane = 'cloud'\nrun = 'true'\n", `invalid lane "cloud"`},
		"missing":     {"", "has no .offscreen.toml"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if tc.contents != "" {
				root = writeContract(t, tc.contents)
			}
			var stderr bytes.Buffer
			code := report(execute([]string{"check", "--worktree", root}), &stderr)
			require.Equal(t, 1, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestCheckWalksUpFromCurrentDirectory(t *testing.T) {
	root := writeContract(t, "project = 'hello'\n")
	child := filepath.Join(root, "sub")
	require.NoError(t, os.Mkdir(child, 0o700))
	t.Chdir(child)
	contract, err := project.Find("", "")
	require.NoError(t, err)
	require.Equal(t, "hello", contract.Project)
	require.NoError(t, execute([]string{"check"}))
}
