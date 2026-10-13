package main

import (
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/progress"
)

// phaseDisplay shows a job's phases (queued, preparing, cloning, booting) on
// stderr until the wrapped command starts. Stderr written before then, such
// as host [prepare] output, is shown as the current phase's output. The
// running phase, or any stdout, closes the renderer, so from then on stdout
// and stderr belong to the command.
type phaseDisplay struct {
	mu       sync.Mutex
	renderer progress.Renderer
	closed   bool
	started  bool     // the command started: running phase or stdout
	partial  []byte   // unterminated stderr line
	tail     []string // last phase output lines, for a job that fails early
}

// phaseTailLines bounds the phase output repeated when a job ends before
// its command starts.
const phaseTailLines = 20

// newPhaseDisplay returns a display for run, or nil when phases are off.
// Auto shows phases only when stderr is a terminal; quiet never does; an
// explicit tty, plain or json mode always does.
func newPhaseDisplay(setting progressSetting, stderr io.Writer, opts progress.Options) (*phaseDisplay, error) {
	mode, err := setting.mode()
	if err != nil {
		return nil, err
	}
	isTerminal := opts.IsTerminal
	if isTerminal == nil {
		isTerminal = progress.IsTerminal
	}
	if mode == progress.Quiet || mode == progress.Auto && !isTerminal(stderr) {
		return nil, nil
	}
	return &phaseDisplay{renderer: progress.New(stderr, mode, opts)}, nil
}

// phase renders p, or closes the display once the command is running.
func (d *phaseDisplay) phase(p daemon.Phase) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if p.State != "queued" && p.State != "preparing" && p.State != "cloning" && p.State != "booting" {
		d.started = true
		d.closeLocked()
		return
	}
	d.flushLocked()
	d.renderer.Emit(progress.Event{Kind: progress.KindPhase, Time: p.Time, Phase: p.State, Reason: p.Reason})
}

// Close finishes the last phase. Later phases are ignored.
func (d *phaseDisplay) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
}

func (d *phaseDisplay) closeLocked() {
	if !d.closed {
		d.flushLocked()
		d.closed = true
		_ = d.renderer.Close()
	}
}

// flushLocked emits an unterminated stderr line before the phase changes.
func (d *phaseDisplay) flushLocked() {
	if len(d.partial) > 0 {
		d.emitLocked(string(d.partial))
		d.partial = nil
	}
}

func (d *phaseDisplay) emitLocked(line string) {
	d.renderer.Emit(progress.Event{Kind: progress.KindOutput, Line: line})
	d.tail = append(d.tail, line)
	if len(d.tail) > phaseTailLines {
		d.tail = d.tail[len(d.tail)-phaseTailLines:]
	}
}

// finish closes the display. If the command never started, it repeats the
// last phase output on w, since the renderer may have shown only part of it.
func (d *phaseDisplay) finish(w io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closeLocked()
	if d.started || len(d.tail) == 0 {
		return
	}
	for _, line := range d.tail {
		_, _ = io.WriteString(w, line+"\n")
	}
	d.tail = nil
}

// phaseOutput shows p as the current phase's output and reports true, or
// reports false once the display is closed.
func (d *phaseDisplay) phaseOutput(p []byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return false
	}
	d.partial = append(d.partial, p...)
	for {
		i := bytes.IndexByte(d.partial, '\n')
		if i < 0 {
			return true
		}
		line := string(bytes.TrimSuffix(d.partial[:i], []byte("\r")))
		d.partial = d.partial[i+1:]
		d.emitLocked(line)
	}
}

// stdoutWriter returns w wrapped so the first write closes the display.
func (d *phaseDisplay) stdoutWriter(w io.Writer) io.Writer { return closingWriter{d: d, w: w} }

// stderrWriter returns w wrapped so writes go to the display while it is
// open and to w after.
func (d *phaseDisplay) stderrWriter(w io.Writer) io.Writer { return phaseWriter{d: d, w: w} }

type closingWriter struct {
	d *phaseDisplay
	w io.Writer
}

func (c closingWriter) Write(p []byte) (int, error) {
	c.d.mu.Lock()
	c.d.started = true
	c.d.closeLocked()
	c.d.mu.Unlock()
	return c.w.Write(p)
}

type phaseWriter struct {
	d *phaseDisplay
	w io.Writer
}

func (c phaseWriter) Write(p []byte) (int, error) {
	if c.d.phaseOutput(p) {
		return len(p), nil
	}
	return c.w.Write(p)
}

// streamRun follows job id to completion. With a display, it asks the daemon
// for phase frames and shows them until the command starts; the display is
// closed before streamRun returns.
func streamRun(ctx context.Context, client daemon.Client, id string, display *phaseDisplay, stdout, stderr io.Writer) (daemon.Job, error) {
	if display == nil {
		return client.Stream(ctx, id, stdout, stderr)
	}
	job, err := client.StreamPhases(ctx, id, display.stdoutWriter(stdout), display.stderrWriter(stderr), display.phase)
	display.finish(stderr)
	return job, err
}
