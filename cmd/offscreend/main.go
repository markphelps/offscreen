package main

import (
	"os"

	"github.com/markphelps/offscreen/internal/logging"
)

func main() {
	level := os.Getenv("OFFSCREEN_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	logger, err := logging.New(level)
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	logger.Error("offscreend: not implemented yet")
	os.Exit(1)
}
