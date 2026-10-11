package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/logging"
	"github.com/markphelps/offscreen/internal/run"
)

func serve() error {
	level := os.Getenv("OFFSCREEN_LOG_LEVEL")
	if level == "" {
		level = "info"
	}
	logger, err := logging.New(level)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	server, err := daemon.Open(cfg)
	if err != nil {
		return err
	}
	defer func() { _ = server.Close() }()
	server.ConfigureHostController()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return server.Serve(ctx)
}

func main() {
	if run.MaybeGuardian(os.Args[1:]) {
		return
	}
	if err := serve(); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
