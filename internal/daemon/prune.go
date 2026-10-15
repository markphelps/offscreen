package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/markphelps/offscreen/internal/project"
)

// PruneRequest is the socket form of a manual prune. OlderThan must be positive;
// the daemon derives the cutoff from its own clock.
type PruneRequest struct {
	OlderThan time.Duration `json:"older_than"`
	Apply     bool          `json:"apply"`
}

// PruneOptions drives the expiry engine directly. Jobs whose terminal
// completion time is before Cutoff are candidates; Apply false previews.
type PruneOptions struct {
	Cutoff time.Time
	Apply  bool
}

// PruneEntry describes one job the engine selected, skipped, deleted or failed.
type PruneEntry struct {
	Job    Job    `json:"job"`
	Reason string `json:"reason,omitempty"`
}

// PruneReport is the engine result. Eligible lists jobs a preview would delete
// (or, when applying, jobs selected before deletion). Recent counts terminal
// jobs kept because they completed at or after the cutoff.
type PruneReport struct {
	Cutoff   time.Time    `json:"cutoff"`
	Apply    bool         `json:"apply"`
	Eligible []PruneEntry `json:"eligible"`
	Deleted  []PruneEntry `json:"deleted"`
	Skipped  []PruneEntry `json:"skipped"`
	Failed   []PruneEntry `json:"failed"`
	Recent   int          `json:"recent"`
}

// Prune removes terminal jobs whose execution and ownership cleanup are
// confirmed and that completed before opts.Cutoff: the owned job directory
// (stream log and artifacts), then the SQLite row, then the in-memory entry.
// Protected work is never touched: queued or active jobs, workers still
// cleaning up, retained (--keep) clones and their cleanup, and any job whose
// Tart clone still exists. Persistent project caches and golden images live
// outside job directories and are never examined.
//
// A preview changes nothing. Applying is idempotent: a job whose directory or
// row could not be removed stays listed with its original completion time, so
// the next run with the same cutoff retries it. Per-job failures are reported
// in PruneReport.Failed; the returned error is for failures that prevented
// evaluation (a stopped daemon or an unverifiable Tart inventory).
func (s *Server) Prune(ctx context.Context, opts PruneOptions) (PruneReport, error) {
	s.pruneMu.Lock()
	defer s.pruneMu.Unlock()
	report := PruneReport{Cutoff: opts.Cutoff, Apply: opts.Apply, Eligible: []PruneEntry{}, Deleted: []PruneEntry{}, Skipped: []PruneEntry{}, Failed: []PruneEntry{}}

	s.mu.Lock()
	if err := s.stoppedLocked(); err != nil {
		s.mu.Unlock()
		return report, err
	}
	type candidate struct {
		w   *worker
		job Job
	}
	var candidates []candidate
	for _, w := range s.jobs {
		if !w.job.Updated.Before(opts.Cutoff) {
			if Terminal(w.job.State) {
				report.Recent++
			}
			continue
		}
		if reason := s.protectedLocked(w); reason != "" {
			report.Skipped = append(report.Skipped, PruneEntry{Job: w.job, Reason: reason})
			continue
		}
		candidates = append(candidates, candidate{w, w.job})
	}
	s.mu.Unlock()

	// A terminal VM job whose clone still exists has unconfirmed ownership
	// cleanup (for example a failed delete). Deleting its record and directory
	// would hide the clone from the startup sweep, so keep it.
	if slices.ContainsFunc(candidates, func(c candidate) bool { return c.job.Lane != project.LaneHost }) {
		vms, err := s.tart.List(ctx)
		if err != nil {
			return report, fmt.Errorf("verify VM cleanup with Tart: %w", err)
		}
		present := map[string]bool{}
		for _, vm := range vms {
			present[vm.Name] = true
		}
		candidates = slices.DeleteFunc(candidates, func(c candidate) bool {
			if c.job.Lane == project.LaneHost || !present["job-"+c.job.ID] {
				return false
			}
			report.Skipped = append(report.Skipped, PruneEntry{Job: c.job, Reason: "VM clone job-" + c.job.ID + " still exists; cancel the job or remove the clone"})
			return true
		})
	}
	slices.SortFunc(candidates, func(a, b candidate) int { return a.job.Updated.Compare(b.job.Updated) })
	slices.SortFunc(report.Skipped, func(a, b PruneEntry) int { return a.Job.Updated.Compare(b.Job.Updated) })
	for _, c := range candidates {
		report.Eligible = append(report.Eligible, PruneEntry{Job: c.job})
	}
	if !opts.Apply {
		return report, nil
	}
	for _, c := range candidates {
		if err := ctx.Err(); err != nil {
			report.Failed = append(report.Failed, PruneEntry{Job: c.job, Reason: err.Error()})
			continue
		}
		if err := s.pruneOne(c.w); err != nil {
			report.Failed = append(report.Failed, PruneEntry{Job: c.job, Reason: err.Error()})
			continue
		}
		report.Deleted = append(report.Deleted, PruneEntry{Job: c.job})
	}
	return report, nil
}

// stoppedLocked reports a daemon that has failed or begun shutting down; prune
// must not write to a store that is closing. Called with s.mu held.
func (s *Server) stoppedLocked() error {
	if s.fatalErr != nil {
		return s.fatalErr
	}
	if s.ctx.Err() != nil {
		return errors.New("daemon is shutting down")
	}
	return nil
}

// protectedLocked returns why w must be kept, or "" when its execution and
// ownership cleanup are complete and its owned directory is identifiable.
// Called with s.mu held.
func (s *Server) protectedLocked(w *worker) string {
	switch {
	case !Terminal(w.job.State):
		return "job is " + w.job.State
	case w.job.Updated.IsZero():
		return "completion time unknown"
	case w.retained:
		return "VM retained for inspection; cancel the job to release it"
	case w.retainedCleanup != nil:
		return "retained VM cleanup in progress"
	case w.cancel != nil:
		return "worker still running"
	}
	if w.done != nil {
		select {
		case <-w.done:
		default:
			return "worker still cleaning up"
		}
	}
	if _, err := ownedJobDir(w.job); err != nil {
		return err.Error()
	}
	return ""
}

// ownedJobDir derives the job directory from its validated ID and refuses a
// record whose stored artifacts path points anywhere else. Prune never removes
// a path read from the database.
func ownedJobDir(job Job) (string, error) {
	if len(job.ID) != 16 {
		return "", errors.New("job ID is not daemon-generated; not removing")
	}
	if _, err := hex.DecodeString(job.ID); err != nil {
		return "", errors.New("job ID is not daemon-generated; not removing")
	}
	dir, err := JobDir(job.ID)
	if err != nil {
		return "", err
	}
	if job.Artifacts != dir {
		return "", errors.New("artifacts path is outside this daemon's job directory; not removing")
	}
	return dir, nil
}

// pruneOne deletes files before the row so a partial failure leaves a record
// that a later prune retries. Neither failure stops the daemon: no unsafe state
// is published, and the completion time is never rewritten.
func (s *Server) pruneOne(w *worker) error {
	s.mu.Lock()
	if err := s.stoppedLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	// Eligibility cannot regress once terminal and released, but recheck under
	// the admission lock in case the entry was replaced or removed.
	if s.jobs[w.job.ID] != w {
		s.mu.Unlock()
		return errors.New("job record changed during prune")
	}
	if reason := s.protectedLocked(w); reason != "" {
		s.mu.Unlock()
		return errors.New(reason)
	}
	dir, _ := ownedJobDir(w.job)
	s.mu.Unlock()

	// RemoveAll does not follow symlinks, so links inside the job directory
	// never reach other data.
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove job directory: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.stoppedLocked(); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, w.job.ID); err != nil {
		return fmt.Errorf("delete job record: %w", err)
	}
	delete(s.jobs, w.job.ID)
	s.order = slices.DeleteFunc(s.order, func(id string) bool { return id == w.job.ID })
	return nil
}

func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var req PruneRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		bad(w, 400, err)
		return
	}
	if req.OlderThan <= 0 {
		bad(w, 400, errors.New("older_than must be a positive duration"))
		return
	}
	s.mu.Lock()
	now := s.now()
	s.mu.Unlock()
	report, err := s.Prune(r.Context(), PruneOptions{Cutoff: now.Add(-req.OlderThan), Apply: req.Apply})
	if err != nil {
		bad(w, 503, fmt.Errorf("prune: %w; nothing was deleted, retry later", tartHint(err)))
		return
	}
	respond(w, report)
}
