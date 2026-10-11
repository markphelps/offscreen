package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"
	"golang.org/x/sys/unix"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/logging"
	"github.com/markphelps/offscreen/internal/project"
)

type CLI struct {
	LogLevel  string       `help:"Log level (debug, info, warn, error)." env:"OFFSCREEN_LOG_LEVEL" default:"info"`
	Run       RunCmd       `cmd:"" help:"Run a project task or command."`
	Status    StatusCmd    `cmd:"" help:"Show jobs including terminal records."`
	Logs      LogsCmd      `cmd:"" help:"Print or follow job output."`
	Artifacts ArtifactsCmd `cmd:"" help:"Print a job's artifact directory."`
	Cancel    CancelCmd    `cmd:"" help:"Cancel a job."`
	Pause     PauseCmd     `cmd:"" help:"Pause the host lane."`
	Resume    ResumeCmd    `cmd:"" help:"Resume the host lane."`
	Image     ImageCmd     `cmd:"" help:"Manage the golden image."`
	Daemon    DaemonCmd    `cmd:"" help:"Manage the background daemon."`
}
type RunCmd struct {
	Lane     string        `help:"Execution lane." enum:"vm,host,auto" default:"auto"`
	Timeout  time.Duration `help:"Maximum time waiting or running." default:"0s"`
	Keep     bool          `help:"Keep the VM after a failed job."`
	Worktree string        `help:"Path to a git worktree." type:"path"`
	Detach   bool          `help:"Print the job ID without waiting."`
	Project  string        `arg:"" name:"project"`
	Args     []string      `arg:"" optional:"" name:"task-or-command"`
	Direct   bool          `kong:"-"`
}

func (c *RunCmd) Run() (runErr error) {
	if len(c.Args) == 0 {
		return fmt.Errorf("run: supply a task or -- followed by a command")
	}
	if !c.Direct && len(c.Args) != 1 {
		return fmt.Errorf("run: supply one task, or use -- before a command")
	}
	contract, err := project.Discover("", c.Worktree, c.Project)
	if err != nil {
		return err
	}
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	req := daemon.Request{Root: contract.Root, Project: c.Project, Lane: project.Lane(c.Lane), Timeout: c.Timeout, Keep: c.Keep, Detach: c.Detach, HostEnv: hostEnvironment(os.Environ())}
	if c.Direct {
		req.Argv = c.Args
	} else {
		req.Task = c.Args[0]
	}
	_, ttyErr := unix.IoctlGetTermios(int(os.Stdin.Fd()), unix.TIOCGETA)
	req.TTY = ttyErr == nil && !c.Detach
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	job, err := client.Submit(ctx, req)
	if err != nil {
		return runFailure{err}
	}
	if c.Detach {
		_, err = fmt.Fprintln(os.Stdout, job.ID)
		return err
	}
	// Keep the accepted identity even when Stream fails and returns a zero Job.
	// Never imply completion if the daemon could not confirm cleanup.
	cleanupConfirmed := false
	defer func() {
		if _, ok := errors.AsType[runFailure](runErr); ok {
			_, _ = fmt.Fprintln(os.Stderr, runErr)
			runErr = runExit(exitcode.RunnerFailure)
		}
		if cleanupConfirmed {
			_, _ = fmt.Fprintf(os.Stderr, "job %s artifacts %s\n", job.ID, job.Artifacts)
		} else {
			_, _ = fmt.Fprintf(os.Stderr, "job %s artifacts %s (cleanup unconfirmed)\n", job.ID, job.Artifacts)
		}
	}()
	go client.Input(ctx, job.ID, os.Stdin)
	finished, streamErr := client.Stream(ctx, job.ID, os.Stdout, os.Stderr)
	if ctx.Err() != nil || streamErr != nil {
		// The server gives cleanup 130 seconds. A shorter client deadline
		// would report completion while the clone is still being destroyed.
		cancelCtx, cancel := context.WithTimeout(context.Background(), 135*time.Second)
		_, cancelErr := client.Cancel(cancelCtx, job.ID)
		cancel()
		if cancelErr != nil {
			return runFailure{fmt.Errorf("cancel job %s: %w", job.ID, errors.Join(cancelErr, streamErr))}
		}
		cleanupConfirmed = true
		if ctx.Err() != nil {
			return runExit(exitcode.RunnerFailure)
		}
		return runFailure{streamErr}
	}
	cleanupConfirmed = true
	if finished.Reason != "" {
		_, _ = fmt.Fprintln(os.Stderr, "offscreen run:", finished.Reason)
	}
	if finished.Code != 0 {
		return runExit(finished.Code)
	}
	return nil
}

// Capture the submitting process's environment for host-only resolution and
// preparation. Split on the first '=' so values may contain '=' unchanged.
func hostEnvironment(entries []string) map[string]string {
	env := make(map[string]string, len(entries))
	for _, entry := range entries {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	return env
}

type runExit int

func (e runExit) Error() string { return fmt.Sprintf("job exited %d", e) }

type runFailure struct{ error }

type StatusCmd struct{}

func (*StatusCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	jobs, err := client.Jobs(context.Background())
	if err != nil {
		return err
	}
	for _, j := range jobs {
		_, _ = fmt.Fprintf(os.Stdout, "%s  %s  %s  %s  %s  %s\n", j.ID, j.Project, j.Task, j.Lane, j.State, j.Reason)
	}
	return nil
}

type LogsCmd struct {
	Job    string `arg:"" help:"Job ID."`
	Follow bool   `help:"Follow output as it arrives." short:"f"`
}

func (c *LogsCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	return client.Logs(context.Background(), c.Job, c.Follow, os.Stdout, os.Stderr)
}

type ArtifactsCmd struct {
	Job string `arg:"" help:"Job ID."`
}

func (c *ArtifactsCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	job, err := client.Job(context.Background(), c.Job)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(os.Stdout, job.Artifacts)
	return err
}

type CancelCmd struct {
	Job string `arg:"" help:"Job ID."`
}

func (c *CancelCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	_, err = client.Cancel(context.Background(), c.Job)
	return err
}

type PauseCmd struct{}

func (*PauseCmd) Run() error {
	return fmt.Errorf("host lane unavailable until host helper is installed")
}

type ResumeCmd struct{}

func (*ResumeCmd) Run() error {
	return fmt.Errorf("host lane unavailable until host helper is installed")
}

type DaemonCmd struct {
	Install   DaemonInstallCmd   `cmd:"" help:"Install and start the launch agent."`
	Uninstall DaemonUninstallCmd `cmd:"" help:"Stop and remove the launch agent."`
}
type DaemonInstallCmd struct{}

func (*DaemonInstallCmd) Run() error { return daemon.Install() }

type DaemonUninstallCmd struct{}

func (*DaemonUninstallCmd) Run() error { return daemon.Uninstall() }

type ImageCmd struct {
	Build   ImageBuildCmd   `cmd:"" help:"Build a new golden image."`
	Refresh ImageRefreshCmd `cmd:"" help:"Rebuild the image after an update."`
}
type ImageBuildCmd struct{}

func (*ImageBuildCmd) Run() error { return buildImage(false) }

type ImageRefreshCmd struct{}

func (*ImageRefreshCmd) Run() error { return buildImage(true) }
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

func execute(args []string) error {
	var cli CLI
	parser, err := kong.New(&cli, kong.Name("offscreen"), kong.Description("Run Mac GUI jobs without taking over your desktop."), kong.UsageOnError())
	if err != nil {
		return err
	}
	ctx, err := parser.Parse(args)
	if err != nil {
		return err
	}
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
		if status, ok := errors.AsType[runExit](err); ok {
			os.Exit(int(status))
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, daemon.ErrUnavailable) {
			os.Exit(exitcode.RunnerFailure)
		}
		if _, ok := errors.AsType[runFailure](err); ok {
			os.Exit(exitcode.RunnerFailure)
		}
		os.Exit(1)
	}
}
