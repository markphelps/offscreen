package progress

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "rewrite golden files")

// clock is a fake time source that advances only when told.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock {
	return &clock{t: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)}
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func testOptions(c *clock, terminal bool, vars map[string]string) Options {
	return Options{
		Getenv:     env(vars),
		IsTerminal: func(io.Writer) bool { return terminal },
		Width:      func() int { return 40 },
		Now:        c.Now,
		Interval:   -1,
	}
}

// build runs a three-step operation: a pull with progress, a clone with
// output, and a setup step that fails.
func build(c *clock, sink Sink) {
	steps := NewSteps(sink, 3)

	pull := steps.Start("Pull base image")
	pull.Progress(0, 200)
	c.Advance(500 * time.Millisecond)
	pull.Progress(50, 200)
	pull.Progress(60, 200)
	c.Advance(500 * time.Millisecond)
	pull.Progress(200, 200)
	pull.Done()

	clone := steps.Start("Clone VM")
	_, _ = io.WriteString(clone, "cloning\nalmost ")
	c.Advance(300 * time.Millisecond)
	_, _ = io.WriteString(clone, "there\r\n")
	clone.Done()

	setup := steps.Start("Run setup script setup.sh")
	for _, line := range []string{"one", "two", "three", "four", "five", "six"} {
		setup.Line(line)
		c.Advance(100 * time.Millisecond)
	}
	_, _ = io.WriteString(setup, "partial")
	setup.Fail(errors.New("exit status 1"))
	setup.Done() // ignored: the step already ended
}

func TestPlainOutput(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	r := New(&buf, Auto, testOptions(c, false, nil))
	build(c, r)
	require.NoError(t, r.Close())

	assert.Equal(t, `[1/3] Pull base image
[1/3] 0%
[1/3] 25%
[1/3] 30%
[1/3] 100%
[1/3] DONE 1.0s
[2/3] Clone VM
[2/3] cloning
[2/3] almost there
[2/3] DONE 0.3s
[3/3] Run setup script setup.sh
[3/3] one
[3/3] two
[3/3] three
[3/3] four
[3/3] five
[3/3] six
[3/3] partial
[3/3] ERROR 0.6s: exit status 1
`, buf.String())
}

func TestPlainPhases(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	r := New(&buf, Plain, testOptions(c, true, nil))
	Phase(r, "queued", "waiting for a VM slot")
	c.Advance(2 * time.Second)
	Phase(r, "cloning", "")
	c.Advance(90 * time.Second)
	Phase(r, "booting", "")
	c.Advance(time.Second)
	require.NoError(t, r.Close())

	assert.Equal(t, `queued (waiting for a VM slot)
queued DONE 2.0s
cloning
cloning DONE 1m30s
booting
booting DONE 1.0s
`, buf.String())
}

func TestUnknownStepTotal(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	r := New(&buf, Plain, testOptions(c, false, nil))
	step := NewSteps(r, 0).Start("Pull")
	step.Progress(10, 0) // unknown size: nothing to print
	step.Done()
	assert.Equal(t, "[1] Pull\n[1] DONE 0.0s\n", buf.String())
}

func TestJSONOutput(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	r := New(&buf, JSON, testOptions(c, true, nil))
	steps := NewSteps(r, 2)
	first := steps.Start("Clone VM")
	first.Line("")
	c.Advance(1500 * time.Millisecond)
	first.Done()
	second := steps.Start("Boot")
	second.Progress(5, 10)
	second.Fail(errors.New("timed out"))
	Phase(r, "queued", "busy")
	require.NoError(t, r.Close())

	var events []map[string]any
	for line := range strings.Lines(buf.String()) {
		var e map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &e), line)
		events = append(events, e)
	}
	require.Len(t, events, 7)

	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:00Z", "kind": "step_start", "step": 1.0, "total": 2.0, "title": "Clone VM"}, events[0])
	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:00Z", "kind": "output", "step": 1.0, "total": 2.0, "line": ""}, events[1])
	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:01.5Z", "kind": "step_done", "step": 1.0, "total": 2.0, "elapsed_ms": 1500.0}, events[2])
	assert.Equal(t, "step_start", events[3]["kind"])
	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:01.5Z", "kind": "progress", "step": 2.0, "total": 2.0, "current": 5.0, "size": 10.0}, events[4])
	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:01.5Z", "kind": "step_failed", "step": 2.0, "total": 2.0, "elapsed_ms": 0.0, "error": "timed out"}, events[5])
	assert.Equal(t, map[string]any{"time": "2026-10-13T12:00:01.5Z", "kind": "phase", "phase": "queued", "reason": "busy"}, events[6])
}

func TestQuietOutput(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	r := New(&buf, Quiet, testOptions(c, true, nil))
	build(c, r)
	Phase(r, "queued", "")
	require.NoError(t, r.Close())
	assert.Empty(t, buf.String())
}

func TestTTYGolden(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		run  func(*clock, Sink)
	}{
		{name: "build", run: build},
		{name: "build_no_color", env: map[string]string{"NO_COLOR": "1"}, run: build},
		{name: "phases", env: map[string]string{"NO_COLOR": "1"}, run: func(c *clock, s Sink) {
			Phase(s, "queued", "waiting for a VM slot to open up soon")
			c.Advance(1200 * time.Millisecond)
			Phase(s, "booting", "")
			c.Advance(300 * time.Millisecond)
		}},
		{name: "escapes_and_wide_lines", env: map[string]string{"NO_COLOR": "1"}, run: func(c *clock, s Sink) {
			step := NewSteps(s, 1).Start("A title long enough to be cut at forty columns")
			step.Line("\x1b[31mred\x1b[0m\ttab \x1b]0;title\x07bell\x07")
			c.Advance(time.Second)
			// Left running: Close prints it as interrupted.
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClock()
			var buf bytes.Buffer
			r := New(&buf, Auto, testOptions(c, true, tt.env))
			_, ok := r.(*ttyRenderer)
			require.True(t, ok, "auto on a terminal should pick tty")
			tt.run(c, r)
			require.NoError(t, r.Close())
			r.Emit(Event{Kind: KindPhase, Phase: "ignored after close"})

			got := strings.ReplaceAll(buf.String(), "\x1b", `\e`)
			path := filepath.Join("testdata", tt.name+".golden")
			if *update {
				require.NoError(t, os.MkdirAll("testdata", 0o750))
				require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
			}
			want, err := os.ReadFile(path) //nolint:gosec // fixed test path
			require.NoError(t, err)
			assert.Equal(t, string(want), got)
		})
	}
}

func TestTTYRedrawsAfterShrink(t *testing.T) {
	c := newClock()
	var buf bytes.Buffer
	width := 40
	opts := testOptions(c, true, map[string]string{"NO_COLOR": "1"})
	opts.Width = func() int { return width }
	r := New(&buf, TTY, opts)
	step := NewSteps(r, 1).Start("Build")
	step.Line(strings.Repeat("x", 60)) // drawn as 39 columns
	width = 20
	buf.Reset()
	step.Line("y")
	// Two live lines, the 39-column one now wrapping onto two rows: erase 3.
	assert.True(t, strings.HasPrefix(buf.String(), "\x1b[3A\r\x1b[J"), "%q", buf.String())
	require.NoError(t, r.Close())
}

func TestTTYBackgroundRedraw(t *testing.T) {
	var buf syncBuffer
	opts := Options{
		Getenv:     env(map[string]string{"NO_COLOR": "1"}),
		IsTerminal: func(io.Writer) bool { return true },
		Width:      func() int { return 80 },
		Interval:   time.Millisecond,
	}
	r := New(&buf, TTY, opts)
	step := NewSteps(r, 1).Start("Wait")
	step.Line("batched")
	require.Eventually(t, func() bool { return strings.Contains(buf.String(), "batched") }, time.Second, time.Millisecond)
	step.Done()
	require.NoError(t, r.Close())
	assert.Contains(t, buf.String(), "✔ [1/1] Wait")
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestNewPicksMode(t *testing.T) {
	tests := []struct {
		name     string
		mode     Mode
		terminal bool
		env      map[string]string
		want     Renderer
	}{
		{name: "auto terminal", mode: Auto, terminal: true, want: &ttyRenderer{}},
		{name: "auto pipe", mode: Auto, terminal: false, want: &plainRenderer{}},
		{name: "auto dumb terminal", mode: Auto, terminal: true, env: map[string]string{"TERM": "dumb"}, want: &plainRenderer{}},
		{name: "tty on a pipe falls back", mode: TTY, terminal: false, want: &plainRenderer{}},
		{name: "plain on a terminal", mode: Plain, terminal: true, want: &plainRenderer{}},
		{name: "quiet", mode: Quiet, terminal: true, want: quiet{}},
		{name: "json", mode: JSON, terminal: true, want: &jsonRenderer{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New(io.Discard, tt.mode, testOptions(newClock(), tt.terminal, tt.env))
			assert.IsType(t, tt.want, r)
			require.NoError(t, r.Close())
		})
	}
}

func TestColor(t *testing.T) {
	assert.True(t, colorEnabled(true, env(nil)))
	assert.False(t, colorEnabled(false, env(nil)))
	assert.False(t, colorEnabled(true, env(map[string]string{"NO_COLOR": "1"})))
	assert.False(t, colorEnabled(true, env(map[string]string{"TERM": "dumb"})))
	assert.False(t, ColorEnabled(&bytes.Buffer{}))
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name    string
		flag    string
		env     string
		want    Mode
		wantErr string
	}{
		{name: "default", want: Auto},
		{name: "env", env: "plain", want: Plain},
		{name: "flag wins over env", flag: "JSON", env: "plain", want: JSON},
		{name: "bad flag", flag: "fancy", wantErr: `invalid progress mode "fancy"`},
		{name: "bad env", env: "fancy", wantErr: `OFFSCREEN_PROGRESS: invalid progress mode "fancy"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Select(tt.flag, env(map[string]string{EnvVar: tt.env}))
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestStepWriteConcurrent(t *testing.T) {
	var buf syncBuffer
	r := New(&buf, Plain, testOptions(newClock(), false, nil))
	step := NewSteps(r, 1).Start("Run")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				_, _ = io.WriteString(step, "line\n")
			}
		})
	}
	wg.Wait()
	step.Done()
	assert.Equal(t, 200, strings.Count(buf.String(), "[1/1] line\n"))
}

func TestDiscard(t *testing.T) {
	step := NewSteps(Discard, 1).Start("Nothing")
	step.Line("dropped")
	step.Done()
	assert.Equal(t, 1, step.Number())
}
