package progress

import (
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// tailLines is how many recent output lines a running step shows.
	tailLines = 5

	ansiReset = "\x1b[0m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiBlue  = "\x1b[34m"
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// liveItem is a running step or the current phase.
type liveItem struct {
	step  int // 0 for a phase
	title string
	start time.Time
	tail  []string
	pct   int
}

// ttyRenderer redraws running steps in place. Finished steps are printed
// once above the live region and never redrawn.
type ttyRenderer struct {
	mu    sync.Mutex
	w     io.Writer
	width func() int
	now   func() time.Time
	color bool

	live     []*liveItem
	finished []string
	// drawn holds the display width of each line in the live region as last
	// drawn, so the next redraw knows how many rows to erase even after the
	// terminal is resized.
	drawn   []int
	drawnW  int
	dirty   bool
	closed  bool
	ticking bool

	stop chan struct{}
	wg   sync.WaitGroup
}

func newTTY(w io.Writer, opts Options, color bool) *ttyRenderer {
	r := &ttyRenderer{w: w, width: opts.Width, now: opts.Now, color: color, stop: make(chan struct{})}
	if opts.Interval > 0 {
		r.ticking = true
		r.wg.Go(func() { r.tick(opts.Interval) })
	}
	return r
}

func (r *ttyRenderer) tick(interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			r.mu.Lock()
			if len(r.live) > 0 || r.dirty {
				r.redraw()
			}
			r.mu.Unlock()
		}
	}
}

func (r *ttyRenderer) Emit(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	at := eventTime(e, r.now)
	switch e.Kind {
	case KindStepStart:
		r.live = append(r.live, &liveItem{step: e.Step, title: label(e.Step, e.Total) + " " + e.Title, start: at, pct: -1})
	case KindOutput:
		item := r.find(e.Step)
		if item == nil {
			return
		}
		item.tail = append(item.tail, sanitize(e.Line))
		if len(item.tail) > tailLines {
			item.tail = item.tail[len(item.tail)-tailLines:]
		}
		// Output can arrive thousands of lines at a time; let the ticker
		// batch the redraws unless there is no ticker.
		r.dirty = true
		if r.ticking {
			return
		}
	case KindProgress:
		if item := r.find(e.Step); item != nil {
			item.pct = percent(e.Current, e.Size)
		}
	case KindStepDone, KindStepFailed:
		item := r.find(e.Step)
		if item == nil {
			return
		}
		r.finish(item, at, e.Err, e.Kind == KindStepFailed)
	case KindPhase:
		if item := r.find(0); item != nil {
			r.finish(item, at, nil, false)
		}
		r.live = append(r.live, &liveItem{title: phaseTitle(e.Phase, e.Reason), start: at, pct: -1})
	}
	r.redraw()
}

func (r *ttyRenderer) find(step int) *liveItem {
	for _, item := range r.live {
		if item.step == step {
			return item
		}
	}
	return nil
}

func (r *ttyRenderer) finish(item *liveItem, at time.Time, err error, failed bool) {
	for i, live := range r.live {
		if live == item {
			r.live = append(r.live[:i], r.live[i+1:]...)
			break
		}
	}
	elapsed := formatDuration(at.Sub(item.start))
	if failed {
		line := r.paint(ansiRed, "✘") + " " + item.title + "  " + r.paint(ansiRed, "ERROR "+elapsed)
		if err != nil {
			line += r.paint(ansiRed, ": "+sanitize(err.Error()))
		}
		r.finished = append(r.finished, line)
		return
	}
	r.finished = append(r.finished, r.paint(ansiGreen, "✔")+" "+item.title+"  "+r.paint(ansiDim, elapsed))
}

// Close finishes the current phase, prints any step still running as
// interrupted, and stops background redraws.
func (r *ttyRenderer) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	at := r.now()
	if item := r.find(0); item != nil {
		r.finish(item, at, nil, false)
	}
	for _, item := range r.live {
		r.finished = append(r.finished, r.paint(ansiDim, "-")+" "+item.title+"  "+r.paint(ansiDim, formatDuration(at.Sub(item.start))))
	}
	r.live = nil
	r.redraw()
	r.closed = true
	r.mu.Unlock()
	close(r.stop)
	r.wg.Wait()
	return nil
}

// redraw erases the live region, prints newly finished lines, and draws the
// live region again. The caller holds r.mu.
func (r *ttyRenderer) redraw() {
	r.dirty = false
	width := max(r.width(), 20)
	var b strings.Builder
	if rows := r.drawnRows(width); rows > 0 {
		fmt.Fprintf(&b, "\x1b[%dA\r\x1b[J", rows)
	}
	// Finished lines are never redrawn, so they may wrap instead of losing
	// their timing or error to truncation.
	for _, line := range r.finished {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	r.finished = r.finished[:0]

	r.drawn = r.drawn[:0]
	r.drawnW = width
	now := r.now()
	for _, item := range r.live {
		elapsed := now.Sub(item.start)
		frame := spinner[int(elapsed/(100*time.Millisecond))%len(spinner)]
		suffix := "  " + r.paint(ansiDim, formatDuration(elapsed))
		if item.pct >= 0 {
			suffix += fmt.Sprintf("  %d%%", item.pct)
		}
		prefix := r.paint(ansiBlue, frame) + " "
		// Shorten the title rather than the timer when the line is too wide.
		room := max(width-1-displayWidth(prefix)-displayWidth(suffix), 1)
		r.drawLine(&b, prefix+truncate(item.title, room)+suffix, width)
		for _, line := range item.tail {
			r.drawLine(&b, r.paint(ansiDim, "    "+line), width)
		}
	}
	_, _ = io.WriteString(r.w, b.String())
}

func (r *ttyRenderer) drawLine(b *strings.Builder, line string, width int) {
	line = truncate(line, width-1)
	r.drawn = append(r.drawn, displayWidth(line))
	b.WriteString(line)
	b.WriteByte('\n')
}

// drawnRows returns how many terminal rows the last live region occupies at
// the current width. A terminal that shrank since the last draw wraps long
// lines onto extra rows.
func (r *ttyRenderer) drawnRows(width int) int {
	rows := 0
	for _, w := range r.drawn {
		rows += max(1, (w+width-1)/width)
	}
	return rows
}

func (r *ttyRenderer) paint(code, s string) string {
	if !r.color {
		return s
	}
	return code + s + ansiReset
}

// sanitize removes escape sequences and control characters from s so that
// command output cannot move the cursor or break the layout.
func sanitize(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscape(s, i)
		case c == '\t':
			b.WriteString("    ")
		case c < 0x20 || c == 0x7f:
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// skipEscape returns the index of the last byte of the escape sequence that
// starts at s[i].
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return i
	}
	switch s[i+1] {
	case '[': // CSI: parameters, then a final byte in 0x40-0x7e.
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j
			}
		}
		return len(s) - 1
	case ']': // OSC: ends with BEL or ESC \.
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 1
			}
		}
		return len(s) - 1
	default:
		return i + 1
	}
}

// truncate shortens s to at most width visible characters, keeping any
// color codes intact and resetting color when it cuts.
func truncate(s string, width int) string {
	if displayWidth(s) <= width {
		return s
	}
	var b strings.Builder
	visible := 0
	colored := false
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			end := skipEscape(s, i) + 1
			b.WriteString(s[i:end])
			colored = s[i:end] != ansiReset
			i = end
			continue
		}
		if visible == width-1 {
			b.WriteString("…")
			break
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		b.WriteString(s[i : i+size])
		visible++
		i += size
	}
	if colored {
		b.WriteString(ansiReset)
	}
	return b.String()
}

// displayWidth counts the runes of s outside escape sequences.
func displayWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i) + 1
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		n++
		i += size
	}
	return n
}
