package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/project"
)

func TestInitWritesTemplateThatLoadsWithNoTasks(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my app")
	require.NoError(t, os.Mkdir(dir, 0o700))
	var stderr bytes.Buffer
	require.NoError(t, (&InitCmd{}).write(dir, &stderr))
	path := filepath.Join(dir, project.FileName)
	require.Equal(t, "wrote "+path+"\nedit it for what your project needs, then run: offscreen check\n", stderr.String())
	contract, err := project.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "my app", contract.Project)
	require.False(t, contract.Exclusive)
	require.Empty(t, contract.Tasks)
	require.Empty(t, contract.Prepare.Run)
}

func TestInitProjectFlagOverridesDirectoryName(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, (&InitCmd{Project: `we"ird`}).write(dir, &bytes.Buffer{}))
	contract, err := project.LoadFile(filepath.Join(dir, project.FileName))
	require.NoError(t, err)
	require.Equal(t, `we"ird`, contract.Project)
}

func TestInitTemplateExampleTaskIsValidWhenUncommented(t *testing.T) {
	contents, err := contractTemplate("hello")
	require.NoError(t, err)
	text := string(contents)
	for _, line := range []string{"# [tasks.example]", `# lane = "vm"`, `# run = "sh scripts/example.sh"`} {
		require.Contains(t, text, line+"\n")
		text = strings.Replace(text, line, strings.TrimPrefix(line, "# "), 1)
	}
	path := filepath.Join(t.TempDir(), project.FileName)
	require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
	contract, err := project.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, project.Task{Lane: project.LaneVM, Run: "sh scripts/example.sh"}, contract.Tasks["example"])
}

func TestInitRefusesToOverwriteWithoutForce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, project.FileName)
	require.NoError(t, os.WriteFile(path, []byte("project = 'mine'\n"), 0o600))
	err := (&InitCmd{}).write(dir, &bytes.Buffer{})
	require.ErrorContains(t, err, "already exists; pass --force")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "project = 'mine'\n", string(data))

	require.NoError(t, (&InitCmd{Project: "new", Force: true}).write(dir, &bytes.Buffer{}))
	contract, err := project.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new", contract.Project)
}

func TestInitCommandExitsOneWhenFileExists(t *testing.T) {
	dir := writeContract(t, "project = 'mine'\n")
	t.Chdir(dir)
	var stderr bytes.Buffer
	require.Equal(t, 1, report(execute([]string{"init"}), &stderr))
	require.Contains(t, stderr.String(), "already exists")
}
