package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
)

// The helper supplies a fresh, complete observation. Unknown or stale signals
// fail closed. The daemon never makes UI/IO calls while its scheduler lock is held.
type HostSnapshot struct {
	IdleSeconds     float64 `json:"idle_seconds"`
	Unlocked        bool    `json:"unlocked"`
	DisplayAwake    bool    `json:"display_awake"`
	CameraIdle      bool    `json:"camera_idle"`
	MicrophoneIdle  bool    `json:"microphone_idle"`
	InputMonitoring bool    `json:"input_monitoring"`
	EventTapReady   bool    `json:"event_tap_ready"`
}

type HostStatus struct {
	Queued           int    `json:"queued"`
	Running          *Job   `json:"running,omitempty"`
	WaitingReason    string `json:"waiting_reason"`
	CountdownJob     string `json:"countdown_job,omitempty"`
	NotNowJob        string `json:"not_now_job,omitempty"`
	CountdownSeconds int    `json:"countdown_seconds,omitempty"`
	ReturnJob        string `json:"return_job,omitempty"`
	GraceSeconds     int    `json:"grace_seconds,omitempty"`
	Paused           bool   `json:"paused"`
}

type hostControl struct {
	snapshot    HostSnapshot
	seen        time.Time
	countdownID string
	countdownAt time.Time
	// notNowID survives an idle-sensor reset caused by clicking Not now.
	// It is never eligible for another job or after an admission blocker.
	notNowID    string
	pausedUntil time.Time
	returnID    string
	returnUntil time.Time
}

func (s *Server) ConfigureHostController() {
	s.ConfigureHost(s.hostGateSnapshot, run.ExecuteHost)
}

// Called only under Server.mu. Countdown is an admission predicate, never a
// blocking sleep inside the scheduler. A click may reset HID idle before its
// action arrives, so an eligible queued job keeps its job-bound action.
func (s *Server) hostGateSnapshot(_ context.Context, job Job) (bool, string) {
	h := &s.hostControl
	now := time.Now()
	reset := func(reason string, keepAction bool) (bool, string) {
		if h.countdownID == job.ID {
			h.countdownAt = time.Time{}
			if !keepAction {
				s.clearHostCountdown()
			}
		}
		return false, reason
	}
	if h.seen.IsZero() || now.Sub(h.seen) > 3*time.Second {
		return reset("waiting for menu bar helper (signal unavailable)", false)
	}
	a := h.snapshot
	if !a.InputMonitoring {
		return reset("waiting for Input Monitoring permission", false)
	}
	if !a.EventTapReady {
		return reset("waiting for event tap", false)
	}
	if !a.Unlocked {
		return reset("waiting for unlocked session", false)
	}
	if !a.DisplayAwake {
		return reset("waiting for awake display", false)
	}
	if !a.CameraIdle || !a.MicrophoneIdle {
		return reset("waiting for camera/microphone to be idle", false)
	}
	if a.IdleSeconds < s.cfg.IdleThreshold.Seconds() {
		return reset("waiting for idle desktop", true)
	}
	if h.countdownID != job.ID || h.countdownAt.IsZero() {
		h.countdownID, h.countdownAt = job.ID, now
	}
	h.notNowID = job.ID
	if now.Sub(h.countdownAt) < 10*time.Second {
		return false, "starting host job in 10 seconds (Not now available)"
	}
	return true, ""
}

// Called with Server.mu held. All non-idle admission blockers invalidate both
// the timer and any outstanding Not now action from the blocked countdown.
func (s *Server) clearHostCountdown() {
	s.hostControl.countdownID = ""
	s.hostControl.countdownAt = time.Time{}
	s.hostControl.notNowID = ""
}

func (s *Server) hostStatus() HostStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	status := HostStatus{Paused: s.hostPaused || now.Before(s.hostControl.pausedUntil)}
	for _, w := range s.jobs {
		if w.job.Lane != project.LaneHost {
			continue
		}
		if w.job.State == "queued" {
			status.Queued++
			if w.job.ID == s.hostControl.countdownID || status.WaitingReason == "" {
				status.WaitingReason = w.job.Reason
			}
		} else if !Terminal(w.job.State) {
			job := w.job
			status.Running = &job
		}
	}
	h := s.hostControl
	if h.notNowID != "" {
		if w := s.jobs[h.notNowID]; w != nil && w.job.State == "queued" && !status.Paused {
			status.NotNowJob = h.notNowID
		}
	}
	if h.countdownID != "" && !h.countdownAt.IsZero() {
		if w := s.jobs[h.countdownID]; w != nil && w.job.State == "queued" {
			status.CountdownJob = h.countdownID
			status.CountdownSeconds = max(0, int((10*time.Second-now.Sub(h.countdownAt)+time.Second-1)/time.Second))
		}
	}
	if h.returnID != "" && now.Before(h.returnUntil) {
		status.ReturnJob = h.returnID
		status.GraceSeconds = max(0, int((h.returnUntil.Sub(now)+time.Second-1)/time.Second))
	}
	if status.Paused && status.Queued > 0 {
		status.WaitingReason = "host lane paused"
	}
	return status
}

func (s *Server) handleHostStatus(w http.ResponseWriter, _ *http.Request) { respond(w, s.hostStatus()) }

func (s *Server) handleHostSnapshot(w http.ResponseWriter, r *http.Request) {
	var snapshot HostSnapshot
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&snapshot); err != nil {
		bad(w, 400, err)
		return
	}
	if snapshot.IdleSeconds < 0 || snapshot.IdleSeconds > 1e9 {
		bad(w, 400, errors.New("invalid idle time"))
		return
	}
	s.mu.Lock()
	s.hostControl.snapshot, s.hostControl.seen = snapshot, time.Now()
	s.mu.Unlock()
	respond(w, s.hostStatus())
}

func (s *Server) handleHostPause(w http.ResponseWriter, _ *http.Request) {
	if err := s.PauseHost(); err != nil {
		bad(w, 500, err)
		return
	}
	respond(w, s.hostStatus())
}

func (s *Server) handleHostResume(w http.ResponseWriter, _ *http.Request) {
	if err := s.ResumeHost(); err != nil {
		bad(w, 500, err)
		return
	}
	respond(w, s.hostStatus())
}

func (s *Server) handleNotNow(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	h := &s.hostControl
	id := r.PathValue("id")
	job := s.jobs[id]
	if job == nil || job.job.Lane != project.LaneHost || job.job.State != "queued" || h.notNowID != id ||
		s.hostPaused || time.Now().Before(h.pausedUntil) || s.fatalErr != nil {
		s.mu.Unlock()
		bad(w, 409, errors.New("countdown is no longer active for this job"))
		return
	}
	until := time.Now().Add(30 * time.Minute)
	if err := s.persistHostPolicy(s.hostPaused, until); err != nil {
		s.mu.Unlock()
		bad(w, 500, err)
		return
	}
	h.pausedUntil = until
	s.clearHostCountdown()
	s.mu.Unlock()
	respond(w, s.hostStatus())
}

func (s *Server) handleHostReturn(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	id := r.PathValue("id")
	job := s.jobs[id]
	if job == nil || job.job.Lane != project.LaneHost ||
		(job.job.State != "preparing" && job.job.State != "running" && job.job.State != "collecting") {
		s.mu.Unlock()
		bad(w, 409, fmt.Errorf("host job %q is not active", id))
		return
	}
	if s.hostControl.returnID != id {
		s.hostControl.returnID = id
		s.hostControl.returnUntil = time.Now().Add(s.cfg.Grace)
	}
	// A prepare phase can finish and launch a GUI task before grace expires.
	// Once the user is back, stop preparation now and requeue after cleanup.
	if job.job.State == "preparing" && job.cancel != nil {
		job.requeueReason = "desktop returned during prepare"
		job.cancel()
	}
	s.mu.Unlock()
	respond(w, s.hostStatus())
}

func (s *Server) handleTakeBack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	eligible := s.hostControl.returnID == id && !s.hostControl.returnUntil.IsZero()
	s.mu.Unlock()
	if !eligible {
		bad(w, 409, errors.New("return notice no longer belongs to this job"))
		return
	}
	// RequeueHost cancels the group; never claim cleanup before worker.done.
	if err := s.RequeueHost(id, "desktop taken back"); err != nil {
		bad(w, 409, err)
		return
	}
	s.mu.Lock()
	done := s.jobs[id].done
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			bad(w, 504, errors.New("host cleanup not finished"))
			return
		case <-r.Context().Done():
			return
		}
	}
	respond(w, s.hostStatus())
}

// Called under s.mu by scheduler. A returning user gets the full configured
// grace period. At expiry, requeue only while the desktop is still claimed;
// otherwise the task continues and later jobs still require a fresh idle gate.
func (s *Server) expireHostReturn(now time.Time) {
	h := &s.hostControl
	if h.returnID == "" || now.Before(h.returnUntil) {
		return
	}
	w := s.jobs[h.returnID]
	if w != nil && w.cancel != nil && !Terminal(w.job.State) &&
		(now.Sub(h.seen) > 3*time.Second || h.snapshot.IdleSeconds < s.cfg.IdleThreshold.Seconds() || !h.snapshot.Unlocked) {
		w.requeueReason = "desktop taken back after grace"
		w.cancel()
	}
	h.returnUntil = time.Time{}
	h.returnID = ""
}
