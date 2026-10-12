package daemon

import (
	"context"
	"log/slog"
	"time"
)

// expiryInterval is how often the daemon expires old completed jobs after its
// startup pass. Tests shorten it before opening a server.
var expiryInterval = time.Hour

// expiryTimeout bounds one automatic pass, mostly the Tart inventory check.
const expiryTimeout = 5 * time.Minute

// expire runs one automatic pass of the expiry engine with the configured
// retention. Errors are logged rather than returned: the engine deletes
// nothing when it cannot verify cleanup, per-job failures keep their original
// completion time, and the next pass retries both.
func (s *Server) expire(ctx context.Context, pass string) {
	s.mu.Lock()
	retention := s.cfg.JobRetention
	cutoff := s.now().Add(-retention)
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, expiryTimeout)
	defer cancel()
	report, err := s.Prune(ctx, PruneOptions{Cutoff: cutoff, Apply: true})
	s.mu.Lock()
	hook := s.onExpiry
	s.mu.Unlock()
	if hook != nil {
		defer hook(report, err)
	}
	if err != nil {
		if s.ctx.Err() != nil {
			slog.Debug("job expiry stopped by shutdown", "pass", pass, "err", err)
			return
		}
		slog.Error("job expiry deleted nothing; retrying next pass", "pass", pass, "retention", retention, "cutoff", cutoff, "err", err)
		return
	}
	for _, f := range report.Failed {
		slog.Warn("expired job not deleted; retrying next pass", "pass", pass, "job", f.Job.ID, "reason", f.Reason)
	}
	level := slog.LevelDebug
	if pass == "startup" || len(report.Deleted) > 0 {
		level = slog.LevelInfo
	}
	if len(report.Failed) > 0 {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "job expiry", "pass", pass, "retention", retention, "cutoff", cutoff,
		"deleted", len(report.Deleted), "failed", len(report.Failed), "skipped", len(report.Skipped), "recent", report.Recent)
}

// maintain runs the periodic expiry pass until the daemon stops. A single
// goroutine owns the ticker, so automatic passes never overlap; Prune also
// serializes them with manual prune requests.
func (s *Server) maintain(interval time.Duration) {
	defer s.maintenance.Done()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.expire(s.ctx, "periodic")
		}
	}
}
