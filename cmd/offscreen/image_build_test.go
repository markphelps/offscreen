package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/progress"
)

// fakeBuild runs two steps; when fail is set, the second prints 100 lines
// and fails the way image.Build reports a failed step.
func fakeBuild(fail bool) imageBuilder {
	return func(_ context.Context, _ string, _ bool, sink progress.Sink) (string, error) {
		steps := progress.NewSteps(sink, 2)
		step := steps.Start("Clone base image")
		step.Done()
		step = steps.Start("Run setup script ~/setup.sh")
		if !fail {
			_, _ = fmt.Fprintln(step, "setup ok")
			step.Done()
			return "offscreen-20261013-xcode26.1-abcdef012345", nil
		}
		var tail []string
		for i := 1; i <= 100; i++ {
			_, _ = fmt.Fprintf(step, "setup line %d\n", i)
			if i > 100-image.FailureLines {
				tail = append(tail, fmt.Sprintf("setup line %d", i))
			}
		}
		err := errors.New(`image setup script "~/setup.sh": guest command: Process exited with status 1`)
		step.Fail(err)
		return "", &image.StepError{Step: "Run setup script ~/setup.sh", Tail: tail, Log: "/state/image-builds/offscreen-build-abc.log", Err: err}
	}
}

func TestRunBuildPrintsOnlyTheImageNameOnStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runBuild(context.Background(), "config.toml", false, progressSetting{flag: "plain"}, fakeBuild(false), &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "offscreen-20261013-xcode26.1-abcdef012345\n", stdout.String())
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	require.Equal(t, "[1/2] Clone base image", lines[0])
	require.Regexp(t, `^\[1/2\] DONE `, lines[1])
	require.Equal(t, "[2/2] Run setup script ~/setup.sh", lines[2])
	require.Equal(t, "[2/2] setup ok", lines[3])
	require.Regexp(t, `^\[2/2\] DONE `, lines[4])
}

func TestRunBuildFailureShowsTailAndLogPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := runBuild(context.Background(), "config.toml", false, progressSetting{flag: "quiet"}, fakeBuild(true), &stdout, &stderr)
	require.Error(t, err)
	require.Empty(t, stdout.String())
	require.Equal(t, `image setup script "~/setup.sh": guest command: Process exited with status 1`+"\nfull build log: /state/image-builds/offscreen-build-abc.log", err.Error())
	out := stderr.String()
	require.True(t, strings.HasPrefix(out, "> Run setup script ~/setup.sh (last 20 lines of output):\n  setup line 81\n"), out)
	require.True(t, strings.HasSuffix(out, "  setup line 100\n"), out)
	require.NotContains(t, out, "setup line 80\n")
}

func TestRunBuildRejectsInvalidProgressEnv(t *testing.T) {
	t.Setenv(progress.EnvVar, "fancy")
	err := runBuild(context.Background(), "config.toml", false, progressSetting{}, fakeBuild(false), &bytes.Buffer{}, &bytes.Buffer{})
	require.ErrorContains(t, err, "OFFSCREEN_PROGRESS")
}

func TestExecuteRejectsInvalidProgressFlag(t *testing.T) {
	err := execute([]string{"--progress", "fancy", "image", "ls"})
	require.ErrorContains(t, err, "--progress")
}
