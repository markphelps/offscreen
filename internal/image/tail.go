package image

import (
	"bytes"
	"strings"
	"sync"
)

// maxPartial is how many bytes of an unterminated line a tailBuffer keeps.
const maxPartial = 64 << 10

// tailBuffer keeps the last n lines written to it, so a failed step can show
// its most recent output without holding all of it in memory.
type tailBuffer struct {
	mu      sync.Mutex
	n       int
	lines   []string
	partial []byte
}

func newTailBuffer(n int) *tailBuffer { return &tailBuffer{n: n} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.partial = append(t.partial, p...)
	for {
		i := bytes.IndexByte(t.partial, '\n')
		if i < 0 {
			break
		}
		t.add(string(t.partial[:i]))
		t.partial = t.partial[i+1:]
	}
	// Bound a line that has no newline yet, so a runaway line cannot grow
	// without limit.
	if len(t.partial) > maxPartial {
		t.partial = t.partial[len(t.partial)-maxPartial:]
	}
	return len(p), nil
}

// add appends one line. The caller holds t.mu.
func (t *tailBuffer) add(line string) {
	t.lines = append(t.lines, redrawn(line))
	if len(t.lines) > t.n {
		t.lines = t.lines[len(t.lines)-t.n:]
	}
}

// Lines returns the last n lines, including a trailing partial line.
func (t *tailBuffer) Lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := append([]string(nil), t.lines...)
	if len(t.partial) > 0 {
		lines = append(lines, redrawn(string(t.partial)))
		if len(lines) > t.n {
			lines = lines[len(lines)-t.n:]
		}
	}
	return lines
}

// redrawn returns what a terminal would show for line: a carriage return
// means the writer redrew it, so only the text after the last one is kept.
func redrawn(line string) string {
	line = strings.TrimRight(line, "\r")
	if i := strings.LastIndexByte(line, '\r'); i >= 0 {
		line = line[i+1:]
	}
	return line
}
