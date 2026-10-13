// Package daemon owns persistent jobs, admission, and the Unix-socket API.
package daemon

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/markphelps/offscreen/internal/project"
)

type Request struct {
	Root    string            `json:"root"`
	Project string            `json:"project"`
	Task    string            `json:"task"`
	Argv    []string          `json:"argv"`
	HostEnv map[string]string `json:"host_env,omitempty"` // Submission-time host environment; never sent to the guest.
	Lane    project.Lane      `json:"lane"`
	Timeout time.Duration     `json:"timeout"`
	Keep    bool              `json:"keep"`
	Show    bool              `json:"show,omitempty"` // VM lane only: open the VM window on the host.
	TTY     bool              `json:"tty"`
	Detach  bool              `json:"detach"`
}

type Job struct {
	ID        string       `json:"id"`
	Project   string       `json:"project"`
	Root      string       `json:"root"`
	Task      string       `json:"task"`
	Lane      project.Lane `json:"lane"`
	State     string       `json:"state"`
	Code      int          `json:"code"`
	Artifacts string       `json:"artifacts"`
	Reason    string       `json:"reason"`
	Created   time.Time    `json:"created"`
	Updated   time.Time    `json:"updated"`
	Deadline  time.Time    `json:"deadline"`
	// Retained reports a failed --keep VM job whose clone still holds its VM
	// slot and any exclusive lease until cancel deletes it. It lives only in
	// the daemon's memory and is never persisted.
	Retained bool `json:"retained,omitempty"`
}

func StateDir() (string, error) {
	if dir := os.Getenv("OFFSCREEN_STATE_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "offscreen"), nil
}

func Socket() (string, error) {
	dir, err := StateDir()
	return filepath.Join(dir, "offscreend.sock"), err
}

func JobDir(id string) (string, error) {
	if dir := os.Getenv("OFFSCREEN_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "jobs", id), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Caches", "offscreen", "jobs", id), nil
}

// Frame kinds. stream.bin holds only FrameStdout and FrameStderr. FramePhase
// is sent only to stream requests that opt in with PhasesParam.
const (
	FrameStdout byte = 'o'
	FrameStderr byte = 'e'
	// FramePhase carries a JSON-encoded Phase.
	FramePhase byte = 'p'
)

// PhasesParam is the query parameter (set to "1") on GET /jobs/{id}/stream
// that asks for FramePhase frames. Without it the stream carries only
// FrameStdout and FrameStderr, which is all clients from before phase frames
// understand.
const PhasesParam = "phases"

// Phase is a job state change sent in a FramePhase frame, such as "queued"
// with a reason, then "preparing", "cloning", "booting" and "running".
type Phase struct {
	State  string    `json:"state"`
	Reason string    `json:"reason,omitempty"`
	Time   time.Time `json:"time"`
}

// Frames retain the stream identity; unlike a line-oriented log, bytes are not rewritten.
// Format: one kind byte (see FrameStdout and friends), big-endian uint32 length, payload.
func WriteFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > 1<<20 {
		return fmt.Errorf("frame too large")
	}
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func ReadFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(header[1:])
	if n > 1<<20 {
		return 0, nil, fmt.Errorf("frame too large: %d", n)
	}
	payload := make([]byte, n)
	_, err := io.ReadFull(r, payload)
	return header[0], payload, err
}
func Terminal(s string) bool { return s == "done" || s == "failed" || s == "cancelled" }
