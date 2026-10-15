package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/alecthomas/kong"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/logging"
	"github.com/markphelps/offscreen/internal/progress"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

type CLI struct {
	LogLevel  string       `help:"Log level (debug, info, warn, error)." env:"OFFSCREEN_LOG_LEVEL" default:"info"`
	Progress  string       `help:"Progress output for image build, image refresh and run phases: auto, tty, plain, quiet or json. Defaults to OFFSCREEN_PROGRESS, else auto." placeholder:"MODE"`
	Run       RunCmd       `cmd:"" help:"Run a project task or command."`
	Init      InitCmd      `cmd:"" help:"Write a starter .offscreen.toml in the current directory."`
	Check     CheckCmd     `cmd:"" help:"Validate .offscreen.toml and list its tasks."`
	Status    StatusCmd    `cmd:"" help:"List queued and active jobs; -a includes completed ones."`
	Logs      LogsCmd      `cmd:"" help:"Print or follow job output."`
	Artifacts ArtifactsCmd `cmd:"" help:"Print a job's artifact directory."`
	Cancel    CancelCmd    `cmd:"" help:"Cancel a job."`
	Prune     PruneCmd     `cmd:"" help:"Delete old completed jobs and their output, asking first; -f skips the prompt."`
	Pause     PauseCmd     `cmd:"" help:"Pause the host lane."`
	Resume    ResumeCmd    `cmd:"" help:"Resume the host lane."`
	Image     ImageCmd     `cmd:"" help:"Manage the golden image."`
	Daemon    DaemonCmd    `cmd:"" help:"Manage the background daemon."`
	Version   VersionCmd   `cmd:"" help:"Print the client and daemon versions and protocols."`
}
type RunCmd struct {
	Lane     string        `help:"Execution lane." enum:"vm,host,auto" default:"auto"`
	Timeout  time.Duration `help:"Maximum time waiting or running." default:"0s"`
	Keep     bool          `help:"Keep the VM after a failed job."`
	Show     bool          `help:"Show the VM in a window on your desktop instead of running headless (VM lane only)."`
	Worktree string        `help:"Path to a git worktree." type:"path"`
	Detach   bool          `help:"Print the job ID without waiting."`
	Project  string        `arg:"" name:"project"`
	Args     []string      `arg:"" optional:"" name:"task-or-command"`
	Direct   bool          `kong:"-"`
}

func (c *RunCmd) Run(setting progressSetting) (runErr error) {
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
	warnOldDaemon(context.Background(), client, os.Stderr)
	var display *phaseDisplay
	if !c.Detach {
		if display, err = newPhaseDisplay(setting, os.Stderr, progress.Options{}); err != nil {
			return err
		}
	}
	req := daemon.Request{Root: contract.Root, Project: c.Project, Lane: project.Lane(c.Lane), Timeout: c.Timeout, Keep: c.Keep, Show: c.Show, Detach: c.Detach, HostEnv: hostEnvironment(os.Environ())}
	if c.Direct {
		req.Argv = c.Args
	} else {
		req.Task = c.Args[0]
	}
	req.TTY = isTerminal(os.Stdin) && !c.Detach
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
	finished, streamErr := streamRun(ctx, client, job.ID, display, os.Stdout, os.Stderr)
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

type LogsCmd struct {
	Job    string `arg:"" help:"Job ID."`
	Follow bool   `help:"Keep printing output until the job ends. Without it, print the output so far and exit." short:"f" negatable:""`
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
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	_, err = client.HostAction(context.Background(), "pause", "")
	return err
}

type ResumeCmd struct{}

func (*ResumeCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	_, err = client.HostAction(context.Background(), "resume", "")
	return err
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
	Ls      ImageLsCmd      `cmd:"" help:"List golden images and mark current_image."`
	Prune   ImagePruneCmd   `cmd:"" help:"Delete golden images other than current_image, asking first; -f skips the prompt."`
}
type ImageBuildCmd struct{}

func (*ImageBuildCmd) Run(setting progressSetting) error { return buildImage(false, setting) }

type ImageRefreshCmd struct{}

func (*ImageRefreshCmd) Run(setting progressSetting) error { return buildImage(true, setting) }

// progressSetting carries the global --progress flag to the commands that
// render progress. The flag wins over OFFSCREEN_PROGRESS.
type progressSetting struct{ flag string }

func (p progressSetting) mode() (progress.Mode, error) {
	return progress.Select(p.flag, os.Getenv)
}

func buildImage(refresh bool, setting progressSetting) error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runBuild(ctx, path, refresh, setting, image.Build, os.Stdout, os.Stderr)
}

type imageBuilder func(ctx context.Context, path string, refresh bool, sink progress.Sink) (string, error)

// runBuild runs build with a progress renderer on stderr. Stdout gets only
// the image name. A failed step prints its last output lines on stderr and
// returns a one-line error with a hint naming the full build log.
func runBuild(ctx context.Context, path string, refresh bool, setting progressSetting, build imageBuilder, stdout, stderr io.Writer) error {
	mode, err := setting.mode()
	if err != nil {
		return err
	}
	renderer := progress.New(stderr, mode, progress.Options{})
	name, err := build(ctx, path, refresh, renderer)
	_ = renderer.Close()
	if err != nil {
		stepErr, ok := errors.AsType[*image.StepError](err)
		if !ok {
			return err
		}
		if len(stepErr.Tail) > 0 {
			_, _ = fmt.Fprintf(stderr, "> %s (last %d lines of output):\n", stepErr.Step, len(stepErr.Tail))
			for _, line := range stepErr.Tail {
				_, _ = fmt.Fprintf(stderr, "  %s\n", progress.Sanitize(line))
			}
		}
		return hintError{err: err, hint: "full build log: " + stepErr.Log}
	}
	_, err = fmt.Fprintln(stdout, name)
	return err
}

// hintError is a one-line error followed by a hint line.
type hintError struct {
	err  error
	hint string
}

func (e hintError) Error() string { return e.err.Error() + "\n" + e.hint }

func (e hintError) Unwrap() error { return e.err }

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
	if cli.Progress != "" {
		if _, err := progress.ParseMode(cli.Progress); err != nil {
			return fmt.Errorf("--progress: %w", err)
		}
	}
	return ctx.Run(progressSetting{flag: cli.Progress})
}

func main() {
	if run.MaybeGuardian(os.Args[1:]) {
		return
	}
	if err := execute(os.Args[1:]); err != nil {
		os.Exit(report(err, os.Stderr))
	}
}

// report prints a failed command's error on w and returns its exit status.
func report(err error, w io.Writer) int {
	if status, ok := errors.AsType[runExit](err); ok {
		return int(status)
	}
	if errors.Is(err, daemon.ErrUnavailable) {
		// The dial error behind it (usually a missing socket) is noise for
		// the user; keep it for debugging.
		slog.Debug("daemon unreachable", "err", err)
		_, _ = fmt.Fprintln(w, daemon.ErrUnavailable)
		return exitcode.RunnerFailure
	}
	_, _ = fmt.Fprintln(w, err)
	if _, ok := errors.AsType[daemon.IncompatibleError](err); ok {
		return exitcode.RunnerFailure
	}
	if _, ok := errors.AsType[runFailure](err); ok {
		return exitcode.RunnerFailure
	}
	return 1
}
