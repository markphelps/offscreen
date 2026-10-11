package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/logging"
)

// CLI declares the public command surface. The implementation is added in later stages.
type CLI struct {
	LogLevel  string       `help:"Log level (debug, info, warn, error)." env:"OFFSCREEN_LOG_LEVEL" default:"info"`
	Run       RunCmd       `cmd:"" help:"Run a project task or command."`
	Status    StatusCmd    `cmd:"" help:"Show running and queued jobs."`
	Logs      LogsCmd      `cmd:"" help:"Print or follow a job's output."`
	Artifacts ArtifactsCmd `cmd:"" help:"Print a job's artifact directory."`
	Cancel    CancelCmd    `cmd:"" help:"Cancel a queued or running job."`
	Pause     PauseCmd     `cmd:"" help:"Pause the host lane."`
	Resume    ResumeCmd    `cmd:"" help:"Resume the host lane."`
	Image     ImageCmd     `cmd:"" help:"Manage the golden image."`
}

// RunCmd accepts either a named task or -- followed by an arbitrary command.
type RunCmd struct {
	Lane     string        `help:"Execution lane." enum:"vm,host,auto" default:"auto"`
	Timeout  time.Duration `help:"Maximum time waiting or running." default:"0s"`
	Keep     bool          `help:"Keep the VM after a failed job."`
	Worktree string        `help:"Path to a git worktree (defaults to the current directory)." type:"path"`
	Detach   bool          `help:"Print the job ID without waiting for completion."`
	Project  string        `arg:"" name:"project" help:"Project name from .offscreen.toml."`
	Args     []string      `arg:"" optional:"" name:"task-or-command" help:"Task name, or -- followed by a command."`
	Direct   bool          `kong:"-"` // True when -- separates the project from a raw command.
}

func (c *RunCmd) Run() error {
	if len(c.Args) == 0 {
		return fmt.Errorf("run: supply a task or -- followed by a command; see offscreen run --help")
	}
	if !c.Direct && len(c.Args) != 1 {
		return fmt.Errorf("run: supply one task, or use -- before a command; see offscreen run --help")
	}
	return notImplemented("run")
}

type StatusCmd struct{}

func (c *StatusCmd) Run() error { return notImplemented("status") }

type LogsCmd struct {
	Job    string `arg:"" help:"Job ID."`
	Follow bool   `help:"Follow output as it arrives." short:"f"`
}

func (c *LogsCmd) Run() error { return notImplemented("logs") }

type ArtifactsCmd struct {
	Job string `arg:"" help:"Job ID."`
}

func (c *ArtifactsCmd) Run() error { return notImplemented("artifacts") }

type CancelCmd struct {
	Job string `arg:"" help:"Job ID."`
}

func (c *CancelCmd) Run() error { return notImplemented("cancel") }

type PauseCmd struct{}

func (c *PauseCmd) Run() error { return notImplemented("pause") }

type ResumeCmd struct{}

func (c *ResumeCmd) Run() error { return notImplemented("resume") }

type ImageCmd struct {
	Build   ImageBuildCmd   `cmd:"" help:"Build a new golden image."`
	Refresh ImageRefreshCmd `cmd:"" help:"Rebuild the golden image after a system update."`
}

type ImageBuildCmd struct{}

func (c *ImageBuildCmd) Run() error { return buildImage(false) }

type ImageRefreshCmd struct{}

func (c *ImageRefreshCmd) Run() error { return buildImage(true) }

func buildImage(refresh bool) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	name, err := image.Build(ctx, path, refresh)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, name)
	return err
}

func notImplemented(command string) error {
	return fmt.Errorf("offscreen %s: not implemented yet", command)
}

func execute(args []string) error {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("offscreen"),
		kong.Description("Run Mac GUI jobs without taking over your desktop."),
		kong.UsageOnError(),
	)
	if err != nil {
		return fmt.Errorf("create CLI: %w", err)
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		return fmt.Errorf("parse command: %w", err)
	}
	// Kong consumes -- during parsing. Retain it so the runner can distinguish
	// a named task from an arbitrary command with the same first argument.
	cli.Run.Direct = slices.Contains(args, "--")
	logger, err := logging.New(cli.LogLevel)
	if err != nil {
		return err
	}
	slog.SetDefault(logger)
	return ctx.Run()
}

func main() {
	if err := execute(os.Args[1:]); err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}
