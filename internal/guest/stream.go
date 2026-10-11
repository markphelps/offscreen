package guest

import (
	"context"
	"fmt"
	"io"
	"strings"

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
