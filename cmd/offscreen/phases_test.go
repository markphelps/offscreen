package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/progress"
)

var phaseStart = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// streamScript writes a fake job's stream. phase writes a phase frame only
// when the request asked for phases.
type streamScript func(w io.Writer, phase func(state, reason string, at time.Duration))

// commandStream holds phases around the command's output.
func commandStream(w io.Writer, phase func(string, string, time.Duration)) {
	phase("queued", "waiting for VM slot", 0)
	phase("preparing", "", time.Second)
	phase("cloning", "", 2*time.Second)
	phase("booting", "", 3*time.Second)
	phase("preparing", "", 4*time.Second)
	phase("running", "", 5*time.Second)
	_ = daemon.WriteFrame(w, daemon.FrameStdout, []byte("hi\n"))
	_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("warn\n"))
	phase("collecting", "", 6*time.Second)
}

// currentVersion is what a daemon built from this tree advertises.
var currentVersion = &daemon.VersionInfo{Protocol: daemon.ProtocolVersion, Version: "test", Features: []string{daemon.FeaturePhases}}

// fakeStreamDaemon serves one finished job whose stream script writes, as a
// current daemon. It reports whether each stream request asked for phases.
func fakeStreamDaemon(t *testing.T, script streamScript) <-chan bool {
	t.Helper()
	return fakeDaemon(t, currentVersion, script)
}

// fakeDaemon is fakeStreamDaemon that answers GET /version with version, or
// 404 when version is nil, as a daemon from before negotiation does. Such a
// daemon also ignores PhasesParam.
func fakeDaemon(t *testing.T, version *daemon.VersionInfo, script streamScript) <-chan bool {
	t.Helper()
	state, err := os.MkdirTemp("/tmp", "os-phase-") // Unix socket paths are short on macOS.
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(state) })
	t.Setenv("OFFSCREEN_STATE_DIR", state)
	listener, err := net.Listen("unix", filepath.Join(state, "offscreend.sock"))
	require.NoError(t, err)
	asked := make(chan bool, 4)
	mux := http.NewServeMux()
	if version != nil {
		mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(version)
		})
	}
	mux.HandleFunc("GET /jobs", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]daemon.Job{{ID: "fake", Project: "hello", State: "running", Created: time.Now()}})
	})
	mux.HandleFunc("GET /jobs/fake", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(daemon.Job{ID: "fake", State: "done"})
	})
	mux.HandleFunc("GET /jobs/fake/stream", func(w http.ResponseWriter, r *http.Request) {
		asked <- r.URL.Query().Get(daemon.PhasesParam) == "1"
		phases := version != nil && version.Supports(daemon.FeaturePhases) && r.URL.Query().Get(daemon.PhasesParam) == "1"
		phase := func(state, reason string, at time.Duration) {
			if phases {
				b, _ := json.Marshal(daemon.Phase{State: state, Reason: reason, Time: phaseStart.Add(at)})
				_ = daemon.WriteFrame(w, daemon.FramePhase, b)
			}
		}
		script(w, phase)
	})
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: mux}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return asked
}

func plainDisplay(t *testing.T, flag string, stderr io.Writer, terminal bool) *phaseDisplay {
	t.Helper()
	display, err := newPhaseDisplay(progressSetting{flag: flag}, stderr, progress.Options{
		IsTerminal: func(io.Writer) bool { return terminal },
		Getenv:     func(string) string { return "" },
		Now:        func() time.Time { return phaseStart.Add(5 * time.Second) },
	})
	require.NoError(t, err)
	return display
}

func TestStreamRunShowsPhasesOnStderrBeforeOutput(t *testing.T) {
	asked := fakeStreamDaemon(t, commandStream)
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	display := plainDisplay(t, "plain", &stderr, false)
	require.NotNil(t, display)
	job, err := streamRun(context.Background(), client, "fake", display, &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "done", job.State)
	require.True(t, <-asked)
	require.Equal(t, "hi\n", stdout.String())
	require.Equal(t, "queued (waiting for VM slot)\n"+
		"queued DONE 1.0s\n"+
		"preparing\n"+
		"preparing DONE 1.0s\n"+
		"cloning\n"+
		"cloning DONE 1.0s\n"+
		"booting\n"+
		"booting DONE 1.0s\n"+
		"preparing\n"+
		"preparing DONE 1.0s\n"+
		"warn\n", stderr.String())
}

func TestStreamRunShowsPrepareOutputUnderItsPhase(t *testing.T) {
	fakeStreamDaemon(t, func(w io.Writer, phase func(string, string, time.Duration)) {
		phase("queued", "", 0)
		phase("preparing", "", time.Second)
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("build 1\nbuild"))
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte(" 2\n"))
		phase("cloning", "", 2*time.Second)
		phase("booting", "", 3*time.Second)
		phase("running", "", 4*time.Second)
		_ = daemon.WriteFrame(w, daemon.FrameStdout, []byte("hi\n"))
	})
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", plainDisplay(t, "plain", &stderr, false), &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "hi\n", stdout.String())
	require.Equal(t, "queued\n"+
		"queued DONE 1.0s\n"+
		"preparing\n"+
		"[preparing] build 1\n"+
		"[preparing] build 2\n"+
		"preparing DONE 1.0s\n"+
		"cloning\n"+
		"cloning DONE 1.0s\n"+
		"booting\n"+
		"booting DONE 2.0s\n", stderr.String())
}

func TestStreamRunRepeatsPhaseOutputWhenJobFailsBeforeCommand(t *testing.T) {
	fakeStreamDaemon(t, func(w io.Writer, phase func(string, string, time.Duration)) {
		phase("preparing", "", 0)
		for i := 1; i <= 25; i++ {
			_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte(fmt.Sprintf("line-%d\n", i)))
		}
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("error: no newline"))
	})
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", plainDisplay(t, "plain", &stderr, false), &stdout, &stderr)
	require.NoError(t, err)
	require.Empty(t, stdout.String())
	out := stderr.String()
	_, repeat, ok := strings.Cut(out, "preparing DONE")
	require.True(t, ok)
	lines := strings.Split(strings.TrimSuffix(repeat, "\n"), "\n")
	require.Len(t, lines, 1+phaseTailLines) // the DONE line, then the tail
	require.Equal(t, "line-7", lines[1])
	require.Equal(t, "error: no newline", lines[len(lines)-1])
	require.Contains(t, out, "[preparing] line-1\n")
}

func TestStreamRunWithoutDisplayDoesNotAskForPhases(t *testing.T) {
	asked := fakeStreamDaemon(t, commandStream)
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", nil, &stdout, &stderr)
	require.NoError(t, err)
	require.False(t, <-asked)
	require.Equal(t, "hi\n", stdout.String())
	require.Equal(t, "warn\n", stderr.String())
}

func TestPhaseDisplayFollowsProgressMode(t *testing.T) {
	for _, tc := range []struct {
		flag     string
		terminal bool
		want     bool
	}{
		{flag: "", terminal: true, want: true},
		{flag: "", terminal: false, want: false},
		{flag: "auto", terminal: false, want: false},
		{flag: "quiet", terminal: true, want: false},
		{flag: "plain", terminal: false, want: true},
		{flag: "json", terminal: false, want: true},
		{flag: "tty", terminal: false, want: true},
	} {
		got := plainDisplay(t, tc.flag, io.Discard, tc.terminal)
		require.Equal(t, tc.want, got != nil, "flag %q terminal %v", tc.flag, tc.terminal)
		if got != nil {
			got.Close()
		}
	}
}

func TestPhaseDisplayUsesOffscreenProgress(t *testing.T) {
	t.Setenv(progress.EnvVar, "quiet")
	display, err := newPhaseDisplay(progressSetting{}, io.Discard, progress.Options{IsTerminal: func(io.Writer) bool { return true }})
	require.NoError(t, err)
	require.Nil(t, display)
}

func ttyDisplay(t *testing.T, stderr io.Writer) *phaseDisplay {
	t.Helper()
	display, err := newPhaseDisplay(progressSetting{flag: "tty"}, stderr, progress.Options{
		IsTerminal: func(io.Writer) bool { return true },
		Getenv:     func(string) string { return "" },
		Width:      func() int { return 80 },
		Now:        func() time.Time { return phaseStart },
		Interval:   -1,
	})
	require.NoError(t, err)
	require.NotNil(t, display)
	return display
}

// Regression: a new client against a daemon from before negotiation asked
// for phases, got none, and the tty display swallowed stderr written before
// stdout.
func TestStreamRunAgainstOldDaemonPassesStderrThrough(t *testing.T) {
	asked := fakeDaemon(t, nil, func(w io.Writer, _ func(string, string, time.Duration)) {
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("[prepare] building\n"))
		_ = daemon.WriteFrame(w, daemon.FrameStdout, []byte("hi\n"))
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("warn\n"))
	})
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", ttyDisplay(t, &stderr), &stdout, &stderr)
	require.NoError(t, err)
	require.False(t, <-asked, "a protocol 0 daemon is not asked for phases")
	require.Equal(t, "hi\n", stdout.String())
	require.Equal(t, "[prepare] building\nwarn\n", stderr.String())
}

func TestStreamRunAgainstCurrentDaemonAsksForPhases(t *testing.T) {
	asked := fakeStreamDaemon(t, commandStream)
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", ttyDisplay(t, &stderr), &stdout, &stderr)
	require.NoError(t, err)
	require.True(t, <-asked)
	require.Equal(t, "hi\n", stdout.String())
	require.Contains(t, stderr.String(), "booting")
}

// Defense in depth: stderr before the first phase frame is never captured,
// since a tty renderer with no live phase would drop it.
func TestPhaseDisplayPassesStderrBeforeFirstPhase(t *testing.T) {
	fakeStreamDaemon(t, func(w io.Writer, phase func(string, string, time.Duration)) {
		_ = daemon.WriteFrame(w, daemon.FrameStderr, []byte("early\n"))
		phase("queued", "", 0)
		phase("running", "", time.Second)
		_ = daemon.WriteFrame(w, daemon.FrameStdout, []byte("hi\n"))
	})
	client, err := daemon.Dial()
	require.NoError(t, err)
	var stdout, stderr bytes.Buffer
	_, err = streamRun(context.Background(), client, "fake", ttyDisplay(t, &stderr), &stdout, &stderr)
	require.NoError(t, err)
	require.Equal(t, "hi\n", stdout.String())
	require.True(t, strings.HasPrefix(stderr.String(), "early\n"), "stderr: %q", stderr.String())
	require.Contains(t, stderr.String(), "queued")
}
