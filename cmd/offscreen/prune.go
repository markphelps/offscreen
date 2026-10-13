package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/daemon"
	"github.com/markphelps/offscreen/internal/image"
	"github.com/markphelps/offscreen/internal/project"
)

// isTerminal reports whether f is a terminal.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetTermios(int(f.Fd()), unix.TIOCGETA)
	return err == nil
}

// confirmer asks the user a yes/no question before a destructive command
// deletes anything. The question goes to out (stderr); the answer comes from
// in, which is a terminal only when terminal is set.
type confirmer struct {
	in       io.Reader
	out      io.Writer
	terminal bool
}

func stdinConfirmer() confirmer {
	return confirmer{in: os.Stdin, out: os.Stderr, terminal: isTerminal(os.Stdin)}
}

// confirm prints warning and a [y/N] prompt and reports whether the answer
// was y or yes. Anything else, including end of input, is no.
func (c confirmer) confirm(warning string) (bool, error) {
	if _, err := fmt.Fprintf(c.out, "WARNING! %s\nAre you sure you want to continue? [y/N] ", warning); err != nil {
		return false, err
	}
	answer, err := bufio.NewReader(c.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read answer: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// pruneMode decides how a prune command proceeds: preview only, delete without
// asking, or ask first. Without a terminal to ask on, it refuses so a script
// never deletes by accident and never hangs on a prompt.
func pruneMode(command string, dryRun, force bool, prompt confirmer) (apply, ask bool, err error) {
	switch {
	case dryRun:
		return false, false, nil
	case force:
		return true, false, nil
	case !prompt.terminal:
		return false, false, fmt.Errorf("%s: stdin is not a terminal, so it cannot ask before deleting; pass -f to delete without asking or --dry-run to preview", command)
	}
	return false, true, nil
}

type PruneCmd struct {
	OlderThan time.Duration `help:"Select completed jobs that finished longer ago than this (for example 720h)." required:""`
	DryRun    bool          `help:"List what would be deleted without changing anything." xor:"mode"`
	Force     bool          `help:"Delete without asking for confirmation." short:"f" xor:"mode"`
}

func (c *PruneCmd) Validate() error {
	if c.OlderThan <= 0 {
		return errors.New("--older-than must be a positive duration")
	}
	return nil
}

func (c *PruneCmd) Run() error {
	client, err := daemon.Dial()
	if err != nil {
		return err
	}
	return c.prune(context.Background(), client.Prune, stdinConfirmer(), os.Stdout, os.Stderr)
}

// prune previews or deletes old jobs through the daemon. On a terminal without
// -f it lists the selection and a warning on stderr and deletes only after the
// user answers y. The final report (selected, skipped and deleted jobs and a
// summary) goes to stdout; per-job failures and retry guidance go to stderr.
func (c *PruneCmd) prune(ctx context.Context, run func(context.Context, daemon.PruneRequest) (daemon.PruneReport, error), prompt confirmer, stdout, stderr io.Writer) error {
	apply, ask, err := pruneMode("prune", c.DryRun, c.Force, prompt)
	if err != nil {
		return err
	}
	report, err := run(ctx, daemon.PruneRequest{OlderThan: c.OlderThan, Apply: apply})
	if err != nil {
		return err
	}
	if ask {
		c.writeJobs(stderr, report)
		if len(report.Eligible) == 0 {
			_, err := fmt.Fprintf(stdout, "summary  nothing to delete, skipped %d, kept %d newer\n", len(report.Skipped), report.Recent)
			return err
		}
		ok, err := prompt.confirm(fmt.Sprintf("This will delete %d completed job(s) with their logs and artifacts.", len(report.Eligible)))
		if err != nil {
			return err
		}
		if !ok {
			_, err := fmt.Fprintln(stdout, "summary  cancelled; nothing changed")
			return err
		}
		// The daemon selects again with a cutoff from its own clock, so a job
		// that crossed the age limit while the user read the prompt is
		// included, as it would be with -f.
		if report, err = run(ctx, daemon.PruneRequest{OlderThan: c.OlderThan, Apply: true}); err != nil {
			return err
		}
	}
	c.writeJobs(stdout, report)
	for _, e := range report.Failed {
		_, _ = fmt.Fprintf(stderr, "prune: job %s: %s\n", e.Job.ID, e.Reason)
	}
	if report.Apply {
		_, _ = fmt.Fprintf(stdout, "summary  deleted %d, failed %d, skipped %d, kept %d newer\n", len(report.Deleted), len(report.Failed), len(report.Skipped), report.Recent)
	} else {
		_, _ = fmt.Fprintf(stdout, "summary  dry run: would delete %d, skipped %d, kept %d newer; nothing changed\n", len(report.Eligible), len(report.Skipped), report.Recent)
	}
	if len(report.Failed) > 0 {
		return fmt.Errorf("prune: %d job(s) not deleted; they keep their completion time, so rerun offscreen prune --older-than %s -f to retry", len(report.Failed), c.OlderThan)
	}
	return nil
}

// writeJobs prints the cutoff, then deleted (or, in a preview, would-delete)
// and skipped jobs, one per line.
func (c *PruneCmd) writeJobs(w io.Writer, report daemon.PruneReport) {
	line := func(action string, e daemon.PruneEntry, detail string) {
		_, _ = fmt.Fprintf(w, "%s  %s  %s  %s  %s  %s\n", action, e.Job.ID, e.Job.Project, e.Job.State, e.Job.Updated.UTC().Format(time.RFC3339), detail)
	}
	_, _ = fmt.Fprintf(w, "cutoff  %s  (completed more than %s ago)\n", report.Cutoff.UTC().Format(time.RFC3339), c.OlderThan)
	if report.Apply {
		for _, e := range report.Deleted {
			line("deleted", e, "")
		}
	} else {
		for _, e := range report.Eligible {
			line("would-delete", e, "")
		}
	}
	for _, e := range report.Skipped {
		line("skipped", e, e.Reason)
	}
}

type ImagePruneCmd struct {
	DryRun bool `help:"List what would be deleted without changing anything." xor:"mode"`
	Force  bool `help:"Delete without asking for confirmation." short:"f" xor:"mode"`
}

func (c *ImagePruneCmd) Run() error {
	path, err := config.Path()
	if err != nil {
		return err
	}
	guard := func(ctx context.Context) error {
		client, err := daemon.Dial()
		if err != nil {
			return err
		}
		return requireNoActiveVMJobs(ctx, client.Jobs)
	}
	run := func(ctx context.Context, apply bool) (image.PruneReport, error) {
		return image.Prune(ctx, path, apply, guard)
	}
	return c.prune(context.Background(), run, guard, stdinConfirmer(), os.Stdout, os.Stderr)
}

// prune previews or deletes golden images other than current_image. On a
// terminal without -f it lists the selection and a warning on stderr, checks
// guard so it does not ask when deletion would be refused, and deletes only
// after the user answers y.
func (c *ImagePruneCmd) prune(ctx context.Context, run func(context.Context, bool) (image.PruneReport, error), guard func(context.Context) error, prompt confirmer, stdout, stderr io.Writer) error {
	apply, ask, err := pruneMode("image prune", c.DryRun, c.Force, prompt)
	if err != nil {
		return err
	}
	report, err := run(ctx, apply)
	if err != nil {
		return err
	}
	if ask {
		writeImages(stderr, report)
		if len(report.Eligible) == 0 {
			_, err := fmt.Fprintf(stdout, "summary  nothing to delete, skipped %d\n", len(report.Skipped))
			return err
		}
		if err := guard(ctx); err != nil {
			return err
		}
		ok, err := prompt.confirm(fmt.Sprintf("This will delete %d golden image(s) from Tart.", len(report.Eligible)))
		if err != nil {
			return err
		}
		if !ok {
			_, err := fmt.Fprintln(stdout, "summary  cancelled; nothing changed")
			return err
		}
		if report, err = run(ctx, true); err != nil {
			return err
		}
	}
	return printImagePrune(report, stdout, stderr)
}

// requireNoActiveVMJobs refuses to delete images while a started VM job may
// still clone the image it read before a rebuild. Queued jobs read
// current_image when they start, so they are safe. A stopped daemon has no jobs.
func requireNoActiveVMJobs(ctx context.Context, list func(context.Context) ([]daemon.Job, error)) error {
	jobs, err := list(ctx)
	if errors.Is(err, daemon.ErrUnavailable) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check active jobs before deleting images: %w", err)
	}
	if ids := activeVMJobs(jobs); len(ids) > 0 {
		return fmt.Errorf("image prune: VM job(s) %s still active; rerun when they finish", strings.Join(ids, ", "))
	}
	return nil
}

func activeVMJobs(jobs []daemon.Job) []string {
	var ids []string
	for _, job := range jobs {
		if job.Lane == project.LaneVM && job.State != "queued" && !daemon.Terminal(job.State) {
			ids = append(ids, job.ID)
		}
	}
	return ids
}

// writeImages prints the kept current image, then deleted (or, in a preview,
// would-delete) and skipped images, one per line.
func writeImages(w io.Writer, report image.PruneReport) {
	_, _ = fmt.Fprintf(w, "current  %s  (kept)\n", report.Current)
	if report.Apply {
		for _, e := range report.Deleted {
			_, _ = fmt.Fprintf(w, "deleted  %s\n", e.Name)
		}
	} else {
		for _, e := range report.Eligible {
			_, _ = fmt.Fprintf(w, "would-delete  %s\n", e.Name)
		}
	}
	for _, e := range report.Skipped {
		_, _ = fmt.Fprintf(w, "skipped  %s  %s\n", e.Name, e.Reason)
	}
}

// printImagePrune prints selected, skipped and deleted images and a summary on
// stdout and per-image failures on stderr.
func printImagePrune(report image.PruneReport, stdout, stderr io.Writer) error {
	writeImages(stdout, report)
	for _, e := range report.Failed {
		_, _ = fmt.Fprintf(stderr, "image prune: %s: %s\n", e.Name, e.Reason)
	}
	if report.Apply {
		_, _ = fmt.Fprintf(stdout, "summary  deleted %d, failed %d, skipped %d\n", len(report.Deleted), len(report.Failed), len(report.Skipped))
	} else {
		_, _ = fmt.Fprintf(stdout, "summary  dry run: would delete %d, skipped %d; nothing changed\n", len(report.Eligible), len(report.Skipped))
	}
	if len(report.Failed) > 0 {
		return fmt.Errorf("image prune: %d image(s) not deleted; rerun offscreen image prune -f to retry", len(report.Failed))
	}
	return nil
}
