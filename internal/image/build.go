// Package image builds a reproducible golden image from a Tart base.
package image

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/guest"
	"github.com/markphelps/offscreen/internal/progress"
	"github.com/markphelps/offscreen/internal/tart"
)

//go:embed scripts/*.sh scripts/*.swift
var scripts embed.FS

var (
	displayPattern = regexp.MustCompile(`^[1-9][0-9]*x[1-9][0-9]*(pt|px)?$`)
	versionPattern = regexp.MustCompile(`(?m)^Xcode ([0-9]+(?:\.[0-9]+)*)$`)
)

// FailureLines is how many trailing output lines a failed step reports.
const FailureLines = 20

// StepError is a failed build step. Its message is one line; the step's
// last output lines and the path of the full build log are separate fields
// so the caller can show them without the error carrying all the output.
type StepError struct {
	// Step is the failed step's title.
	Step string
	// Tail holds at most FailureLines trailing lines of the step's output.
	Tail []string
	// Log is the path of the full build log.
	Log string
	Err error
}

func (e *StepError) Error() string { return e.Err.Error() }

func (e *StepError) Unwrap() error { return e.Err }

// guestShell runs shell text in the build guest.
type guestShell interface {
	// Run executes script with optional stdin, copying combined output to
	// out as it arrives. The error does not include the output.
	Run(ctx context.Context, script string, input io.Reader, out io.Writer) error
	Close() error
}

type sshShell struct{ client *ssh.Client }

func (s sshShell) Run(ctx context.Context, script string, input io.Reader, out io.Writer) error {
	return guest.RunStream(ctx, s.client, script, input, out)
}

func (s sshShell) Close() error { return s.client.Close() }

// builder holds what a build talks to, so tests can replace the guest.
type builder struct {
	tart tart.Client
	dial func(ctx context.Context, ip string) (guestShell, error)
	sink progress.Sink
}

// Build creates a new image and records it only after all steps succeed.
// Refresh unconditionally pulls the base; build pulls only when it is absent.
// Each step is reported to sink as it runs. The full output goes to a log
// file beside the config (see [LogDir]); a failed step returns a *StepError.
func Build(ctx context.Context, path string, refresh bool, sink progress.Sink) (string, error) {
	cfg, err := config.LoadFile(path)
	if err != nil {
		return "", err
	}
	creds := guest.Credentials{User: cfg.Image.SSHUser, Password: cfg.Image.SSHPassword, Key: cfg.Image.SSHKey}
	b := builder{
		tart: tart.Client{},
		dial: func(ctx context.Context, ip string) (guestShell, error) {
			client, err := creds.Dial(ctx, ip)
			if err != nil {
				return nil, err
			}
			return sshShell{client}, nil
		},
		sink: sink,
	}
	return b.build(ctx, path, cfg, refresh)
}

// LogDir returns the directory that holds image build logs for the config
// at configPath.
func LogDir(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), "image-builds")
}

// buildRun is one build in progress: its step numbering and log file.
type buildRun struct {
	steps   *progress.Steps
	log     io.Writer
	logPath string
}

// stepRun is one running step. out receives command output for the
// display, the log and the failure tail; hidden skips the display.
type stepRun struct {
	run    *buildRun
	title  string
	step   *progress.Step
	tail   *tailBuffer
	out    io.Writer
	hidden io.Writer
}

func (r *buildRun) start(title string) *stepRun {
	step := r.steps.Start(title)
	_, _ = fmt.Fprintf(r.log, "==> [%d] %s\n", step.Number(), title)
	tail := newTailBuffer(FailureLines)
	return &stepRun{
		run:    r,
		title:  title,
		step:   step,
		tail:   tail,
		out:    io.MultiWriter(step, r.log, tail),
		hidden: io.MultiWriter(r.log, tail),
	}
}

func (s *stepRun) done() { s.step.Done() }

// fail ends the step with err and returns the error Build reports.
func (s *stepRun) fail(err error) error {
	s.step.Fail(err)
	_, _ = fmt.Fprintf(s.run.log, "ERROR: %v\n", err)
	return &StepError{Step: s.title, Tail: s.tail.Lines(), Log: s.run.logPath, Err: err}
}

func (b builder) build(ctx context.Context, path string, cfg config.Config, refresh bool) (string, error) {
	display, err := parseDisplay(cfg.Image.Display)
	if err != nil {
		return "", err
	}
	if cfg.Image.Base == "" {
		return "", fmt.Errorf("image base must not be empty")
	}
	if cfg.Image.SSHUser == "" {
		return "", fmt.Errorf("image ssh_user must not be empty")
	}
	sink := b.sink
	if sink == nil {
		sink = progress.Discard
	}
	id := make([]byte, 6)
	if _, err := rand.Read(id); err != nil {
		return "", fmt.Errorf("generate image ID: %w", err)
	}
	suffix := hex.EncodeToString(id)
	working := "offscreen-build-" + suffix
	logDir := LogDir(path)
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", fmt.Errorf("create image build log directory: %w", err)
	}
	logPath := filepath.Join(logDir, working+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) // #nosec G304 -- random name in the log directory.
	if err != nil {
		return "", fmt.Errorf("create image build log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	// Steps: pull, clone, boot, wait, display, check, automation, one per
	// setup script, desktop cleanup, seal.
	run := &buildRun{steps: progress.NewSteps(sink, 9+len(cfg.Image.Setup)), log: logFile, logPath: logPath}
	client := b.tart

	step := run.start("Pull base image " + cfg.Image.Base)
	pull := refresh
	if !refresh {
		vms, err := client.List(ctx)
		if err != nil {
			return "", step.fail(err)
		}
		pull = !slices.ContainsFunc(vms, func(vm tart.VM) bool { return vm.Name == cfg.Image.Base })
	}
	if pull {
		// Tart's pull output has no supported progress format, so it goes
		// to the log and failure tail only; the step shows its timer.
		if err := client.Pull(ctx, cfg.Image.Base, step.hidden); err != nil {
			return "", step.fail(err)
		}
	} else {
		_, _ = fmt.Fprintln(step.out, "already present")
	}
	step.done()

	step = run.start("Clone base image")
	if err := client.Clone(ctx, cfg.Image.Base, working); err != nil {
		return "", step.fail(err)
	}
	owned := working
	defer func() {
		if owned != "" {
			cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			_ = client.Stop(cleanup, owned)
			_ = client.Delete(cleanup, owned)
		}
	}()
	step.done()

	step = run.start("Boot VM")
	if err := client.SetDisplay(ctx, working, cfg.Image.Display); err != nil {
		return "", step.fail(err)
	}
	bootCtx, cancelBoot := context.WithCancel(ctx)
	vm, err := client.Start(bootCtx, working)
	if err != nil {
		cancelBoot()
		return "", step.fail(err)
	}
	// Reap the tart run process before the owned VM is deleted on every exit.
	// The same wait is used after a successful stop, so Wait is never called twice.
	waitVM := sync.OnceValue(vm.Wait)
	defer func() {
		cancelBoot()
		_ = waitVM()
	}()
	step.done()

	step = run.start("Wait for guest SSH")
	shell, err := waitForGuest(ctx, client, working, b.dial)
	if err != nil {
		return "", step.fail(err)
	}
	defer func() { _ = shell.Close() }()
	step.done()

	// Apply the mode before driving System Events: an unsupported Tart hint
	// can leave WindowServer unable to answer Apple Events at all.
	step = run.start("Configure display " + cfg.Image.Display)
	if err := installDisplay(ctx, shell, display, step.out); err != nil {
		return "", step.fail(fmt.Errorf("configure guest display %q: %w (guest LaunchAgent logs: ~/Library/Logs/offscreen/display.err and display.log)", cfg.Image.Display, err))
	}
	step.done()

	step = run.start("Check auto-login and GUI")
	check, err := scripts.ReadFile("scripts/check.sh")
	if err != nil {
		return "", step.fail(err)
	}
	if err := shell.Run(ctx, "/bin/bash -e -s -- "+guest.Quote(cfg.Image.SSHUser), bytes.NewReader(check), step.out); err != nil {
		return "", step.fail(fmt.Errorf("confirm auto-login and GUI: %w", err))
	}
	step.done()

	step = run.start("Enable Xcode automation")
	automation, err := scripts.ReadFile("scripts/automation.sh")
	if err != nil {
		return "", step.fail(err)
	}
	checkAutomation := func() error {
		return shell.Run(ctx, "/bin/bash -e -s", bytes.NewReader(automation), step.out)
	}
	if err := checkAutomation(); err != nil {
		command := "sudo -n /usr/bin/automationmodetool enable-automationmode-without-authentication"
		var passwordInput io.Reader
		if cfg.Image.SSHPassword != "" {
			command = "sudo -S -p '' /usr/bin/automationmodetool enable-automationmode-without-authentication"
			passwordInput = strings.NewReader(cfg.Image.SSHPassword + "\n")
		}
		if err := shell.Run(ctx, command, passwordInput, step.out); err != nil {
			return "", step.fail(fmt.Errorf("enable Xcode automation without authentication: %w", err))
		}
		if err := checkAutomation(); err != nil {
			return "", step.fail(fmt.Errorf("verify Xcode automation without authentication: %w", err))
		}
	}
	step.done()

	setupWrapper, err := scripts.ReadFile("scripts/setup.sh")
	if err != nil {
		return "", err
	}
	for _, path := range cfg.Image.Setup {
		step = run.start("Run setup script " + path)
		if err := runSetup(ctx, shell, string(setupWrapper), path, step.out); err != nil {
			return "", step.fail(err)
		}
		step.done()
	}

	// Last, so apps opened by setup scripts are closed too.
	step = run.start("Clean up guest desktop")
	desktop, err := scripts.ReadFile("scripts/desktop.sh")
	if err != nil {
		return "", step.fail(err)
	}
	if err := shell.Run(ctx, "/bin/bash -e -s", bytes.NewReader(desktop), step.out); err != nil {
		return "", step.fail(fmt.Errorf("disable app relaunch and window restoration: %w", err))
	}
	step.done()

	step = run.start("Seal image")
	var version bytes.Buffer
	if err := shell.Run(ctx, "xcodebuild -version", nil, io.MultiWriter(step.out, &version)); err != nil {
		return "", step.fail(fmt.Errorf("read guest Xcode version: %w", err))
	}
	match := versionPattern.FindStringSubmatch(version.String())
	if match == nil {
		return "", step.fail(fmt.Errorf("unrecognized guest Xcode version: %q", lastLine(version.String())))
	}
	name := fmt.Sprintf("offscreen-%s-xcode%s-%s", time.Now().Format("20060102"), match[1], suffix)
	// The golden disk is cloned immediately after stopping Tart. Flush guest
	// filesystem writes so setup scripts and login services survive that clone.
	if err := shell.Run(ctx, "/bin/sync", nil, step.out); err != nil {
		return "", step.fail(fmt.Errorf("flush guest filesystem before sealing image: %w", err))
	}
	_ = shell.Close()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancelStop()
	if err := client.Stop(stopCtx, working); err != nil {
		return "", step.fail(err)
	}
	if err := waitVM(); err != nil {
		return "", step.fail(fmt.Errorf("VM exited: %w", err))
	}
	// Hold the image lock from rename until current_image names the new image,
	// so a concurrent image prune never mistakes it for an old one.
	unlock, err := lockImages(path)
	if err != nil {
		return "", step.fail(err)
	}
	defer unlock()
	if err := client.Rename(ctx, working, name); err != nil {
		return "", step.fail(err)
	}
	owned = name
	if err := config.SetCurrentImageFile(path, name); err != nil {
		return "", step.fail(err)
	}
	owned = ""
	_, _ = fmt.Fprintln(step.out, name)
	step.done()
	return name, nil
}

// runSetup streams one host setup script into the guest through wrapper.
func runSetup(ctx context.Context, shell guestShell, wrapper, path string, out io.Writer) error {
	expanded, err := expandHome(path)
	if err != nil {
		return err
	}
	file, err := os.Open(expanded) // #nosec G304 -- a setup script the user configured.
	if err != nil {
		return fmt.Errorf("open image setup script %q: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if err := shell.Run(ctx, "/bin/bash -e -o pipefail -c "+guest.Quote(wrapper), file, out); err != nil {
		return fmt.Errorf("image setup script %q: %w", path, err)
	}
	return nil
}

// lastLine returns the last non-empty line of s, for short error messages.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

type displaySpec struct {
	width, height int
	unit          string
}

func parseDisplay(value string) (displaySpec, error) {
	if !displayPattern.MatchString(value) {
		return displaySpec{}, fmt.Errorf("invalid image display %q: expected WIDTHxHEIGHT[pt|px]", value)
	}
	unit := "pt"
	dimensions := value
	if strings.HasSuffix(value, "px") || strings.HasSuffix(value, "pt") {
		unit = value[len(value)-2:]
		dimensions = value[:len(value)-2]
	}
	parts := strings.Split(dimensions, "x")
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width > int(^uint(0)>>1)/2 || height > int(^uint(0)>>1)/2 {
		return displaySpec{}, fmt.Errorf("invalid image display %q: dimensions exceed supported range", value)
	}
	return displaySpec{width: width, height: height, unit: unit}, nil
}

func installDisplay(ctx context.Context, shell guestShell, display displaySpec, out io.Writer) error {
	source, err := scripts.ReadFile("scripts/display.swift")
	if err != nil {
		return err
	}
	command := `/bin/mkdir -p "$HOME/Library/Application Support/offscreen" && /bin/cat > "$HOME/Library/Application Support/offscreen/display.swift"`
	if err := shell.Run(ctx, command, bytes.NewReader(source), out); err != nil {
		return fmt.Errorf("copy display selector: %w", err)
	}
	installer, err := scripts.ReadFile("scripts/display.sh")
	if err != nil {
		return err
	}
	args := fmt.Sprintf("/bin/bash -e -s -- %d %d %s", display.width, display.height, display.unit)
	if err := shell.Run(ctx, args, bytes.NewReader(installer), out); err != nil {
		return fmt.Errorf("install, apply and verify display: %w", err)
	}
	return nil
}

// waitForGuest polls Tart for the VM's IP and dials it until the guest
// accepts a connection or five minutes pass.
func waitForGuest[T any](ctx context.Context, client tart.Client, name string, dial func(context.Context, string) (T, error)) (T, error) {
	deadline, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var zero T
	var last error
	for deadline.Err() == nil {
		ip, err := client.IP(deadline, name)
		if err == nil && ip != "" {
			connection, dialErr := dial(deadline, ip)
			if dialErr == nil {
				return connection, nil
			}
			last = dialErr
		} else {
			last = err
		}
		select {
		case <-deadline.Done():
			return zero, fmt.Errorf("guest SSH not ready: %w (last attempt: %w)", deadline.Err(), last)
		case <-time.After(3 * time.Second):
		}
	}
	return zero, fmt.Errorf("guest SSH not ready: %w (last attempt: %w)", deadline.Err(), last)
}

func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
}
