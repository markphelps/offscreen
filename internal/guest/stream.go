package guest

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// Stream executes a guest shell command without buffering or rewriting its output.
// Cancellation closes the session (and therefore the remote process's SSH channel).
func Stream(ctx context.Context, client *ssh.Client, command string, stdin io.Reader, stdout, stderr io.Writer, tty bool) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open guest session: %w", err)
	}
	defer func() { _ = session.Close() }()
	if tty {
		if err := session.RequestPty("xterm-256color", 40, 120, ssh.TerminalModes{ssh.ECHO: 1}); err != nil {
			return fmt.Errorf("request guest terminal: %w", err)
		}
	}
	session.Stdin, session.Stdout, session.Stderr = stdin, stdout, stderr
	stop := context.AfterFunc(ctx, func() { _ = session.Close(); _ = client.Close() })
	defer stop()
	if err := session.Run(command); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return nil
}

// RunStream executes shell text like [Run], but copies the guest's stdout and
// stderr to out as they arrive instead of buffering them. The error never
// includes the output; callers that need it for diagnostics keep their own
// bounded tail. Cancelling ctx closes the session.
func RunStream(ctx context.Context, client *ssh.Client, script string, input io.Reader, out io.Writer) error {
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("open guest session: %w", err)
	}
	defer func() { _ = session.Close() }()
	// The SSH session copies stdout and stderr on separate goroutines.
	w := &lockedWriter{w: out}
	session.Stdin, session.Stdout, session.Stderr = input, w, w
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	defer stop()
	if err := session.Run("/bin/bash -e -o pipefail -c " + Quote(script)); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("guest command: %w", err)
	}
	return nil
}

// lockedWriter serializes writes from concurrent goroutines.
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// Command converts a resolved command to guest shell text, quoting every argv
// element separately. Shell scripts retain their shell semantics.
func Command(shell string, argv []string) string {
	if shell != "" {
		return "/bin/bash -o pipefail -c " + Quote(shell)
	}
	quoted := make([]string, len(argv))
	for i, arg := range argv {
		quoted[i] = Quote(arg)
	}
	return strings.Join(quoted, " ")
}
