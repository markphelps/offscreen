package main

import (
	"context"
	"io"
	"sync"

	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/progress"
)

// phaseDisplay shows a job's phases (queued, preparing, cloning, booting) on
// stderr until the wrapped command starts. The command's first output, or
// the running phase, closes the renderer, so from then on stdout and stderr
// belong to the command.
type phaseDisplay struct {
	mu       sync.Mutex
	renderer progress.Renderer
	closed   bool
}

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
		d.closeLocked()
		return
	}
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
		d.closed = true
		_ = d.renderer.Close()
	}
}

// writer returns w wrapped so the first write closes the display first.
func (d *phaseDisplay) writer(w io.Writer) io.Writer { return closingWriter{d: d, w: w} }

type closingWriter struct {
	d *phaseDisplay
	w io.Writer
}

func (c closingWriter) Write(p []byte) (int, error) {
	c.d.Close()
	return c.w.Write(p)
}

// streamRun follows job id to completion. With a display, it asks the daemon
// for phase frames and shows them until the command's output starts; the
// display is closed before streamRun returns.
func streamRun(ctx context.Context, client daemon.Client, id string, display *phaseDisplay, stdout, stderr io.Writer) (daemon.Job, error) {
	if display == nil {
		return client.Stream(ctx, id, stdout, stderr)
	}
	defer display.Close()
	return client.StreamPhases(ctx, id, display.writer(stdout), display.writer(stderr), display.phase)
}
