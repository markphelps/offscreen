package image

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/progress"
	"github.com/markphelps/offscreen/internal/tart"
)

// scriptedTart writes a fake tart binary that records its arguments, lists
// the VMs in listJSON, prints pull output, and boots instantly.
func scriptedTart(t *testing.T, listJSON string) (tart.Client, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> '%s'
case "$1" in
list) echo '%s' ;;
pull) echo "pulling $2"; echo "100%%" ;;
ip) echo 192.168.64.2 ;;
esac
`, calls, listJSON)
	binary := filepath.Join(dir, "tart")
	require.NoError(t, os.WriteFile(binary, []byte(script), 0o600))
	require.NoError(t, os.Chmod(binary, 0o700)) // #nosec G302 -- private test executable
	return tart.Client{Binary: binary}, calls
}

// fakeShell stands in for the guest. Setup scripts run on the host through
// the real wrapper, since tests supply them; every other command succeeds
// and prints one line, and xcodebuild reports a version.
type fakeShell struct {
	mu      sync.Mutex
	scripts []string
}

func (f *fakeShell) Run(ctx context.Context, script string, input io.Reader, out io.Writer) error {
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	f.mu.Unlock()
	switch {
	case script == "xcodebuild -version":
		_, _ = io.WriteString(out, "Xcode 26.1\nBuild version 17B55\n")
	case strings.Contains(script, "offscreen-image-setup"):
		cmd := exec.CommandContext(ctx, "/bin/bash", "-c", script) // #nosec G204 -- the embedded setup wrapper
		cmd.Stdin, cmd.Stdout, cmd.Stderr = input, out, out
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("guest command: %w", err)
		}
	default:
		if input != nil {
			_, _ = io.Copy(io.Discard, input)
		}
		_, _ = fmt.Fprintf(out, "ran %s\n", strings.Fields(script)[0])
	}
	return nil
}

func (f *fakeShell) Close() error { return nil }

// recorder is a sink that keeps every event.
type recorder struct {
	mu     sync.Mutex
	events []progress.Event
}

func (r *recorder) Emit(e progress.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) titles() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var titles []string
	for _, e := range r.events {
		if e.Kind == progress.KindStepStart {
			titles = append(titles, fmt.Sprintf("%d/%d %s", e.Step, e.Total, e.Title))
		}
	}
	return titles
}

func (r *recorder) output(step int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var lines []string
	for _, e := range r.events {
		if e.Kind == progress.KindOutput && e.Step == step {
			lines = append(lines, e.Line)
		}
	}
	return lines
}

func (r *recorder) ends() []progress.Kind {
	r.mu.Lock()
	defer r.mu.Unlock()
	var kinds []progress.Kind
	for _, e := range r.events {
		if e.Kind == progress.KindStepDone || e.Kind == progress.KindStepFailed {
			kinds = append(kinds, e.Kind)
		}
	}
	return kinds
}

// buildFixture writes a config whose setup list holds the given scripts.
func buildFixture(t *testing.T, setup ...string) (string, config.Config) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for i, body := range setup {
		path := filepath.Join(dir, fmt.Sprintf("setup-%d.sh", i+1))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		paths = append(paths, path)
	}
	cfg := config.Default()
	cfg.Image.Base = "base:latest"
	cfg.Image.Setup = paths
	path := filepath.Join(dir, "config.toml")
	require.NoError(t, config.SaveFile(path, cfg))
	return path, cfg
}

func newTestBuilder(client tart.Client, shell *fakeShell, sink progress.Sink) builder {
	return builder{
		tart: client,
		dial: func(context.Context, string) (guestShell, error) { return shell, nil },
		sink: sink,
	}
}

func TestBuildReportsStepsInOrderAndRecordsImage(t *testing.T) {
	path, cfg := buildFixture(t, "echo first setup\n", "echo second setup\n")
	client, calls := scriptedTart(t, "[]")
	sink := &recorder{}
	name, err := newTestBuilder(client, &fakeShell{}, sink).build(context.Background(), path, cfg, false)
	require.NoError(t, err)
	require.Regexp(t, `^offscreen-\d{8}-xcode26\.1-[0-9a-f]{12}$`, name)

	setup1, setup2 := cfg.Image.Setup[0], cfg.Image.Setup[1]
	require.Equal(t, []string{
		"1/11 Pull base image base:latest",
		"2/11 Clone base image",
		"3/11 Boot VM",
		"4/11 Wait for guest SSH",
		"5/11 Configure display 1440x900",
		"6/11 Check auto-login and GUI",
		"7/11 Enable Xcode automation",
		"8/11 Run setup script " + setup1,
		"9/11 Run setup script " + setup2,
		"10/11 Clean up guest desktop",
		"11/11 Seal image",
	}, sink.titles())
	require.Len(t, sink.ends(), 11)
	require.NotContains(t, sink.ends(), progress.KindStepFailed)
	require.Equal(t, []string{"first setup"}, sink.output(8))
	require.Equal(t, []string{"second setup"}, sink.output(9))
	// Tart's pull output is not shown.
	require.Empty(t, sink.output(1))

	got, err := config.LoadFile(path)
	require.NoError(t, err)
	require.Equal(t, name, got.CurrentImage)
	log, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(log), "pull base:latest\n")
	require.Contains(t, string(log), "rename offscreen-build-")

	logs, err := filepath.Glob(filepath.Join(LogDir(path), "*.log"))
	require.NoError(t, err)
	require.Len(t, logs, 1)
	buildLog, err := os.ReadFile(logs[0])
	require.NoError(t, err)
	require.Contains(t, string(buildLog), "pulling base:latest\n100%\n", "pull output belongs in the log")
	require.Contains(t, string(buildLog), "first setup\n")
}

func TestBuildSkipsPullWhenBaseIsPresent(t *testing.T) {
	path, cfg := buildFixture(t)
	client, calls := scriptedTart(t, `[{"Name":"base:latest"}]`)
	sink := &recorder{}
	_, err := newTestBuilder(client, &fakeShell{}, sink).build(context.Background(), path, cfg, false)
	require.NoError(t, err)
	require.Equal(t, []string{"already present"}, sink.output(1))
	log, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.NotContains(t, string(log), "pull ")
}

func TestBuildSetupFailureKeepsTailAndFullLog(t *testing.T) {
	path, cfg := buildFixture(t, "for i in $(seq 1 100); do echo \"setup line $i\"; done\nexit 1\n")
	client, calls := scriptedTart(t, `[{"Name":"base:latest"}]`)
	sink := &recorder{}
	_, err := newTestBuilder(client, &fakeShell{}, sink).build(context.Background(), path, cfg, false)
	require.Error(t, err)

	stepErr, ok := errors.AsType[*StepError](err)
	require.True(t, ok, "want a *StepError, got %T", err)
	require.Equal(t, "Run setup script "+cfg.Image.Setup[0], stepErr.Step)
	require.Len(t, stepErr.Tail, FailureLines)
	require.Equal(t, "setup line 81", stepErr.Tail[0])
	require.Equal(t, "setup line 100", stepErr.Tail[FailureLines-1])
	require.NotContains(t, err.Error(), "setup line", "the error must not carry the output")
	require.NotContains(t, err.Error(), "\n")

	buildLog, err := os.ReadFile(stepErr.Log)
	require.NoError(t, err)
	for i := 1; i <= 100; i++ {
		require.Contains(t, string(buildLog), fmt.Sprintf("setup line %d\n", i))
	}
	require.Equal(t, filepath.Dir(stepErr.Log), LogDir(path))

	require.Equal(t, progress.KindStepFailed, sink.ends()[len(sink.ends())-1])
	require.Len(t, sink.titles(), 8, "no step starts after the failure")
	// The failed build deletes its working clone.
	log, err := os.ReadFile(calls)
	require.NoError(t, err)
	require.Contains(t, string(log), "delete offscreen-build-")
	got, err := config.LoadFile(path)
	require.NoError(t, err)
	require.Empty(t, got.CurrentImage)
}

func TestTailBufferKeepsLastLines(t *testing.T) {
	tail := newTailBuffer(3)
	_, _ = io.WriteString(tail, "one\ntwo\nthree\nfour\nfi")
	_, _ = io.WriteString(tail, "ve\rFIVE")
	require.Equal(t, []string{"three", "four", "FIVE"}, tail.Lines())
}
