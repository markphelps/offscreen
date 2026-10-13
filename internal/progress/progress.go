// Package progress reports long-running work as step and phase events and
// renders those events on stderr.
//
// Producers such as image builds and the daemon describe what they are doing
// through a [Sink] and never write to a terminal themselves. The command
// line picks a renderer with [New] for one of the modes auto, tty, plain,
// quiet or json.
package progress

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Kind names the type of an [Event].
type Kind string

// Event kinds.
const (
	// KindStepStart starts a numbered step.
	KindStepStart Kind = "step_start"
	// KindOutput carries one line of a step's output.
	KindOutput Kind = "output"
	// KindProgress reports how much of a step's work is complete.
	KindProgress Kind = "progress"
	// KindStepDone finishes a step successfully.
	KindStepDone Kind = "step_done"
	// KindStepFailed finishes a step with an error.
	KindStepFailed Kind = "step_failed"
	// KindPhase moves an unnumbered sequence, such as a job's lifecycle, to
	// a new phase. It ends the previous phase.
	KindPhase Kind = "phase"
)

// Event is one thing that happened to a step or phase.
type Event struct {
	Kind Kind
	// Time is when the event happened. Renderers use their own clock when
	// it is zero.
	Time time.Time
	// Step is the 1-based step number for step events. A KindOutput event
	// with Step 0 is output of the current phase.
	Step int
	// Total is the number of steps expected, or 0 when unknown.
	Total int
	// Title names the step for KindStepStart.
	Title string
	// Line is one output line, without its newline, for KindOutput.
	Line string
	// Current and Size describe progress for KindProgress. Size is 0 when
	// the total amount of work is unknown.
	Current, Size int64
	// Err is the failure for KindStepFailed.
	Err error
	// Phase and Reason describe a KindPhase event, such as "queued" with
	// reason "waiting for a VM slot".
	Phase, Reason string
}

// Sink receives events. Implementations must be safe for concurrent use.
type Sink interface {
	Emit(Event)
}

// Discard is a Sink that drops every event.
var Discard Sink = discard{}

type discard struct{}

func (discard) Emit(Event) {}

// Phase reports that an unnumbered sequence entered a new phase.
func Phase(sink Sink, phase, reason string) {
	sink.Emit(Event{Kind: KindPhase, Phase: phase, Reason: reason})
}

// Steps numbers the steps of one operation and sends their events to a sink.
type Steps struct {
	sink  Sink
	total int

	mu   sync.Mutex
	next int
}

// NewSteps returns a Steps that expects total steps. Use 0 when the number
// of steps is unknown.
func NewSteps(sink Sink, total int) *Steps {
	return &Steps{sink: sink, total: total}
}

// Start begins the next step.
func (s *Steps) Start(title string) *Step {
	s.mu.Lock()
	s.next++
	n := s.next
	s.mu.Unlock()
	s.sink.Emit(Event{Kind: KindStepStart, Step: n, Total: s.total, Title: title})
	return &Step{sink: s.sink, n: n, total: s.total}
}

// Step is one running step. Its Write method lets it receive a command's
// stdout and stderr, and is safe for concurrent use.
type Step struct {
	sink  Sink
	n     int
	total int

	mu      sync.Mutex
	partial []byte
	ended   bool
}

// Number returns the step's 1-based number.
func (s *Step) Number() int { return s.n }

// Write splits p into lines and emits each complete line as output. A
// trailing partial line is held until more data, Done or Fail arrives.
func (s *Step) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.partial = append(s.partial, p...)
	for {
		i := bytes.IndexByte(s.partial, '\n')
		if i < 0 {
			break
		}
		s.emitLine(s.partial[:i])
		s.partial = s.partial[i+1:]
	}
	return len(p), nil
}

// Line emits one output line.
func (s *Step) Line(line string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLine([]byte(line))
}

// Progress reports that current of size units of work are complete. Use a
// size of 0 when the total is unknown.
func (s *Step) Progress(current, size int64) {
	s.sink.Emit(Event{Kind: KindProgress, Step: s.n, Total: s.total, Current: current, Size: size})
}

// Done finishes the step successfully. Only the first Done or Fail counts.
func (s *Step) Done() { s.end(Event{Kind: KindStepDone}) }

// Fail finishes the step with err. Only the first Done or Fail counts.
func (s *Step) Fail(err error) { s.end(Event{Kind: KindStepFailed, Err: err}) }

func (s *Step) end(e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ended {
		return
	}
	s.ended = true
	if len(s.partial) > 0 {
		s.emitLine(s.partial)
		s.partial = nil
	}
	e.Step, e.Total = s.n, s.total
	s.sink.Emit(e)
}

// emitLine sends one line. A carriage return inside a line means the writer
// redrew it, so only the text after the last one is kept. The caller holds
// s.mu.
func (s *Step) emitLine(b []byte) {
	line := strings.TrimRight(string(b), "\r")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	s.sink.Emit(Event{Kind: KindOutput, Step: s.n, Total: s.total, Line: line})
}

// label returns "[n/N]" or "[n]" when the total is unknown.
func label(step, total int) string {
	if total > 0 {
		return fmt.Sprintf("[%d/%d]", step, total)
	}
	return fmt.Sprintf("[%d]", step)
}

// formatDuration formats d like "0.4s", "12.3s" or "2m05s".
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	d = d.Round(time.Second)
	return fmt.Sprintf("%dm%02ds", int(d/time.Minute), int(d%time.Minute/time.Second))
}

// phaseTitle returns "phase" or "phase (reason)".
func phaseTitle(phase, reason string) string {
	if reason == "" {
		return phase
	}
	return phase + " (" + reason + ")"
}

// percent returns the completed percentage, or -1 when size is unknown.
func percent(current, size int64) int {
	if size <= 0 {
		return -1
	}
	return int(min(max(current*100/size, 0), 100))
}
