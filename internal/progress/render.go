package progress

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

// Mode selects how a renderer shows events.
type Mode string

// Modes.
const (
	// Auto picks TTY when the output is a terminal, else Plain.
	Auto Mode = "auto"
	// TTY redraws numbered steps in place with a spinner and timers.
	TTY Mode = "tty"
	// Plain prints one line per step start, output line and step end.
	Plain Mode = "plain"
	// Quiet shows nothing; only the command's result and errors appear.
	Quiet Mode = "quiet"
	// JSON prints one JSON object per event. Field names are best effort
	// and may change before 1.0.
	JSON Mode = "json"
)

// EnvVar is the environment variable that sets the mode when no flag does.
const EnvVar = "OFFSCREEN_PROGRESS"

// Modes lists every mode in the order help text shows them.
var Modes = []Mode{Auto, TTY, Plain, Quiet, JSON}

// ParseMode returns the mode named s.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if slices.Contains(Modes, m) {
		return m, nil
	}
	return "", fmt.Errorf("invalid progress mode %q (use auto, tty, plain, quiet or json)", s)
}

// Select returns the mode named by flag, or by the EnvVar variable read with
// getenv when flag is empty, or Auto when neither is set.
func Select(flag string, getenv func(string) string) (Mode, error) {
	if flag != "" {
		return ParseMode(flag)
	}
	v := getenv(EnvVar)
	if v == "" {
		return Auto, nil
	}
	m, err := ParseMode(v)
	if err != nil {
		return "", fmt.Errorf("%s: %w", EnvVar, err)
	}
	return m, nil
}

// Renderer is a Sink that draws events. Close stops any background drawing
// and finishes the display; call it before writing anything else to the
// renderer's output.
type Renderer interface {
	Sink
	Close() error
}

// Options adjusts a renderer. The zero value uses the process environment
// and real terminal.
type Options struct {
	// Getenv reads environment variables. Defaults to os.Getenv.
	Getenv func(string) string
	// IsTerminal reports whether the output is a terminal. Defaults to
	// checking whether it is an *os.File for a terminal.
	IsTerminal func(io.Writer) bool
	// Width returns the terminal width in columns. Defaults to the
	// terminal's size, or 80 when unknown.
	Width func() int
	// Now returns the current time. Defaults to time.Now.
	Now func() time.Time
	// Interval is how often tty mode redraws its spinner. Zero uses 100ms;
	// a negative value disables background redraws so every event redraws
	// immediately.
	Interval time.Duration
}

func (o Options) withDefaults(w io.Writer) Options {
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.IsTerminal == nil {
		o.IsTerminal = IsTerminal
	}
	if o.Width == nil {
		o.Width = func() int { return terminalWidth(w) }
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Interval == 0 {
		o.Interval = 100 * time.Millisecond
	}
	return o
}

// New returns a renderer for mode writing to w, usually os.Stderr. Auto and
// TTY become Plain when w is not a terminal or TERM is "dumb", since
// redrawing in place needs a terminal that understands cursor movement.
func New(w io.Writer, mode Mode, opts Options) Renderer {
	opts = opts.withDefaults(w)
	switch mode {
	case Quiet:
		return quiet{}
	case JSON:
		return &jsonRenderer{w: w, now: opts.Now, starts: map[int]time.Time{}}
	case Auto, TTY:
		if opts.IsTerminal(w) && opts.Getenv("TERM") != "dumb" {
			return newTTY(w, opts, colorEnabled(true, opts.Getenv))
		}
	}
	return &plainRenderer{w: w, now: opts.Now, starts: map[int]time.Time{}, lastPct: map[int]int{}}
}

// IsTerminal reports whether w is a terminal.
func IsTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// ColorEnabled reports whether output written to w may use color: w is a
// terminal, NO_COLOR is unset or empty, and TERM is not "dumb".
func ColorEnabled(w io.Writer) bool {
	return colorEnabled(IsTerminal(w), os.Getenv)
}

func colorEnabled(isTerminal bool, getenv func(string) string) bool {
	return isTerminal && getenv("NO_COLOR") == "" && getenv("TERM") != "dumb"
}

func terminalWidth(w io.Writer) int {
	if f, ok := w.(*os.File); ok {
		if width, _, err := term.GetSize(int(f.Fd())); err == nil && width > 0 {
			return width
		}
	}
	return 80
}

type quiet struct{}

func (quiet) Emit(Event)   {}
func (quiet) Close() error { return nil }

// plainRenderer prints one line per step start and end, output lines
// prefixed by the step's number, and each phase change.
type plainRenderer struct {
	mu      sync.Mutex
	w       io.Writer
	now     func() time.Time
	starts  map[int]time.Time
	lastPct map[int]int
	phase   string
	phaseAt time.Time
}

func (r *plainRenderer) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := eventTime(e, r.now)
	// Plain output may still reach a terminal (TERM=dumb), so command
	// output must not carry escape sequences or control characters.
	e.Title, e.Line, e.Phase, e.Reason = Sanitize(e.Title), Sanitize(e.Line), Sanitize(e.Phase), Sanitize(e.Reason)
	switch e.Kind {
	case KindStepStart:
		r.starts[e.Step] = at
		fmt.Fprintf(r.w, "%s %s\n", label(e.Step, e.Total), e.Title)
	case KindOutput:
		if e.Step == 0 {
			// Output during a phase rather than a numbered step.
			fmt.Fprintf(r.w, "[%s] %s\n", r.phase, e.Line)
			return
		}
		fmt.Fprintf(r.w, "%s %s\n", label(e.Step, e.Total), e.Line)
	case KindProgress:
		// Print only when the percentage reaches a new tenth, so a log does
		// not fill with progress lines.
		pct := percent(e.Current, e.Size)
		if pct < 0 {
			return
		}
		if last, ok := r.lastPct[e.Step]; ok && pct/10 <= last/10 {
			return
		}
		r.lastPct[e.Step] = pct
		fmt.Fprintf(r.w, "%s %d%%\n", label(e.Step, e.Total), pct)
	case KindStepDone:
		fmt.Fprintf(r.w, "%s DONE %s\n", label(e.Step, e.Total), formatDuration(at.Sub(r.start(e.Step, at))))
	case KindStepFailed:
		fmt.Fprintf(r.w, "%s ERROR %s: %s\n", label(e.Step, e.Total), formatDuration(at.Sub(r.start(e.Step, at))), errText(e.Err))
	case KindPhase:
		r.endPhase(at)
		r.phase, r.phaseAt = e.Phase, at
		fmt.Fprintf(r.w, "%s\n", phaseTitle(e.Phase, e.Reason))
	}
}

func (r *plainRenderer) start(step int, fallback time.Time) time.Time {
	start, ok := r.starts[step]
	if !ok {
		return fallback
	}
	delete(r.starts, step)
	delete(r.lastPct, step)
	return start
}

func (r *plainRenderer) endPhase(at time.Time) {
	if r.phase == "" {
		return
	}
	fmt.Fprintf(r.w, "%s DONE %s\n", r.phase, formatDuration(at.Sub(r.phaseAt)))
	r.phase = ""
}

func (r *plainRenderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.endPhase(r.now())
	return nil
}

// jsonRenderer prints one JSON object per event.
type jsonRenderer struct {
	mu     sync.Mutex
	w      io.Writer
	now    func() time.Time
	starts map[int]time.Time
}

type jsonEvent struct {
	Time      time.Time `json:"time"`
	Kind      Kind      `json:"kind"`
	Step      int       `json:"step,omitempty"`
	Total     int       `json:"total,omitempty"`
	Title     string    `json:"title,omitempty"`
	Line      *string   `json:"line,omitempty"`
	Current   int64     `json:"current,omitempty"`
	Size      int64     `json:"size,omitempty"`
	ElapsedMS *int64    `json:"elapsed_ms,omitempty"`
	Error     string    `json:"error,omitempty"`
	Phase     string    `json:"phase,omitempty"`
	Reason    string    `json:"reason,omitempty"`
}

func (r *jsonRenderer) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	at := eventTime(e, r.now)
	out := jsonEvent{
		Time:    at.UTC(),
		Kind:    e.Kind,
		Step:    e.Step,
		Total:   e.Total,
		Title:   e.Title,
		Current: e.Current,
		Size:    e.Size,
		Phase:   e.Phase,
		Reason:  e.Reason,
	}
	switch e.Kind {
	case KindStepStart:
		r.starts[e.Step] = at
	case KindOutput:
		out.Line = &e.Line
	case KindStepDone, KindStepFailed:
		if start, ok := r.starts[e.Step]; ok {
			ms := at.Sub(start).Milliseconds()
			out.ElapsedMS = &ms
			delete(r.starts, e.Step)
		}
		if e.Err != nil {
			out.Error = e.Err.Error()
		}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return
	}
	_, _ = r.w.Write(append(b, '\n'))
}

func (r *jsonRenderer) Close() error { return nil }

func eventTime(e Event, now func() time.Time) time.Time {
	if e.Time.IsZero() {
		return now()
	}
	return e.Time
}

// errText returns err's message without escape sequences, or "<nil>".
func errText(err error) string {
	if err == nil {
		return "<nil>"
	}
	return Sanitize(err.Error())
}
