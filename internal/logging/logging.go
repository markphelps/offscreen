// Package logging configures the command and daemon loggers.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// New returns a text logger writing only to stderr at the requested level.
func New(level string) (*slog.Logger, error) {
	var parsed slog.Level
	if err := parsed.UnmarshalText([]byte(strings.ToUpper(level))); err != nil {
		return nil, fmt.Errorf("invalid log level %q (use debug, info, warn or error): %w", level, err)
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: parsed})), nil
}
