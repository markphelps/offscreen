package daemon

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/markphelps/offscreen/internal/config"
	"github.com/markphelps/offscreen/internal/exitcode"
	"github.com/markphelps/offscreen/internal/project"
	"github.com/markphelps/offscreen/internal/run"
	"github.com/markphelps/offscreen/internal/tart"
	_ "modernc.org/sqlite"
)

type worker struct {
	job      Job
	req      Request
	contract project.Contract
	cancel   context.CancelFunc
	inputR   *io.PipeReader
	inputW   *io.PipeWriter
	log      *os.File
	done     chan struct{}
	// retained holds admission after a kept VM's worker has exited. Both it
	// and retainedCleanup are guarded by Server.mu, not worker.mu (log I/O).
	retained        bool
	retainedCleanup chan struct{}
	requeueReason   string
	// mu guards log writes and the fields below. written is the size of
	// stream.bin. phases holds the latest state changes, each with the
	// stream.bin size when it happened, so streams that ask for phase frames
	// interleave them with output in order. Phases live only in memory and
	// never reach stream.bin, so logs never replays them.
	mu        sync.Mutex
	written   int64
	phases    []phaseMark
	nextPhase int
}

// phaseMark is one state change at stream.bin offset, numbered by seq.
type phaseMark struct {
	seq    int
	offset int64
	phase  Phase
}

// maxPhases bounds the phases a worker keeps; a stream that falls this far
// behind skips the oldest. A long host-lane queue can change reason often.
const maxPhases = 64

func (w *worker) write(kind byte, p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := WriteFrame(w.log, kind, p); err != nil {
		return 0, err
	}
	w.written += int64(5 + len(p))
	return len(p), nil
}

// recordPhase notes a non-terminal state change for opted-in streams. A job
// admitted at once never changes to "queued", so its first phase is a
// reasonless "queued" from submission; a blocked job's first phase is
// "queued" with the admission reason.
func (w *worker) recordPhase(job Job) {
	if Terminal(job.State) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.nextPhase == 0 && job.State != "queued" {
		w.phases = append(w.phases, phaseMark{seq: w.nextPhase, offset: w.written, phase: Phase{State: "queued", Time: job.Created}})
		w.nextPhase++
	}
	w.phases = append(w.phases, phaseMark{seq: w.nextPhase, offset: w.written, phase: Phase{State: job.State, Reason: job.Reason, Time: job.Updated}})
	w.nextPhase++
	if len(w.phases) > maxPhases {
		w.phases = slices.Delete(w.phases, 0, len(w.phases)-maxPhases)
	}
}

// phasesSince returns the phases numbered from seq and the stream.bin size
// they were recorded against. A reader that stops at that size and then
// sends phases whose offset it has reached keeps both in order.
func (w *worker) phasesSince(seq int) ([]phaseMark, int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := 0
	for i < len(w.phases) && w.phases[i].seq < seq {
		i++
	}
	return slices.Clone(w.phases[i:]), w.written
}

type streamWriter struct {
	w    *worker
	kind byte
}

func (s streamWriter) Write(p []byte) (int, error) { return s.w.write(s.kind, p) }

type Server struct {
	mu                     sync.Mutex
	workers                sync.WaitGroup
	retainedCleanups       sync.WaitGroup
	db                     *sql.DB
	lock                   *os.File
	cfg                    config.Config
	executor               func(context.Context, run.Options) run.Result
	tart                   tart.Client
	memory                 func() (uint64, error)
	jobs                   map[string]*worker
	order                  []string
	vmRunning, hostRunning int
	// The host-lane child supplies a gate (including countdown) and executor.
	hostGate    func(context.Context, Job) (bool, string)
	hostExecute func(context.Context, run.Options) run.Result
	hostPaused  bool
	hostControl hostControl
	ctx         context.Context
	stop        context.CancelFunc
	fatal       chan struct{}
	fatalErr    error
	// pruneMu serializes expiry runs; now (guarded by mu) is the clock used to
	// derive prune cutoffs and is replaceable in tests.
	pruneMu sync.Mutex
	now     func() time.Time
	// maintenance tracks the periodic expiry goroutine; onExpiry (guarded by
	// mu) observes each automatic pass in tests.
	maintenance sync.WaitGroup
	onExpiry    func(PruneReport, error)
}

// wallClock is the clock new servers use to derive expiry cutoffs. Tests
// replace it to control the startup pass; Server.now controls later passes.
var wallClock = time.Now

func Open(cfg config.Config) (*Server, error) {
	return open(cfg, run.Execute, tart.Client{}, run.AvailableMemory)
}

func open(cfg config.Config, executor func(context.Context, run.Options) run.Result, vm tart.Client, memory func() (uint64, error)) (*Server, error) {
	// A nonpositive retention would disable cleanup; config loading rejects it.
	if cfg.JobRetention <= 0 {
		return nil, fmt.Errorf("job retention must be a positive duration, got %s", cfg.JobRetention)
	}
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("daemon already running: %w", err)
	}
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	cleanupErr := run.WaitForHostCleanup(cleanupCtx)
	cleanupCancel()
	if cleanupErr != nil {
		_ = lock.Close()
		return nil, cleanupErr
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "jobs.sqlite"))
	if err != nil {
		_ = lock.Close()
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
 id TEXT PRIMARY KEY, project TEXT NOT NULL, root TEXT NOT NULL, task TEXT NOT NULL,
 lane TEXT NOT NULL, state TEXT NOT NULL, code INTEGER NOT NULL, artifacts TEXT NOT NULL,
 reason TEXT NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL, deadline TEXT NOT NULL,
 request TEXT NOT NULL, exclusive INTEGER NOT NULL)`); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS host_policy (
 id INTEGER PRIMARY KEY CHECK (id = 1), paused INTEGER NOT NULL, paused_until TEXT NOT NULL)`); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, fmt.Errorf("initialize host policy: %w", err)
	}
	if err := os.Chmod(filepath.Join(dir, "jobs.sqlite"), 0o600); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Server{db: db, lock: lock, cfg: cfg, executor: executor, tart: vm, memory: memory, jobs: make(map[string]*worker), ctx: ctx, stop: stop, fatal: make(chan struct{}), now: wallClock}
	if err := s.loadHostPolicy(); err != nil {
		stop()
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	if err := s.sweep(); err != nil {
		stop()
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	// Recovery and terminal scrubbing succeeded; expire old history before
	// admitting work or serving requests, then keep it bounded periodically.
	s.expire(s.ctx, "startup")
	s.maintenance.Add(1)
	go s.maintain(expiryInterval)
	go s.schedule()
	return s, nil
}

func (s *Server) Close() error {
	s.stop()
	s.mu.Lock()
	for _, w := range s.jobs {
		if w.cancel != nil {
			w.cancel()
		}
		if w.inputW != nil {
			_ = w.inputW.Close()
		}
	}
	s.mu.Unlock()
	s.workers.Wait()
	s.retainedCleanups.Wait()
	s.maintenance.Wait()
	err := s.db.Close()
	_ = s.lock.Close()
	return err
}

// save persists job with the worker's request. A terminal record never keeps
// the caller's environment: execution and any requeue decision are settled, and
// retained VM cleanup needs only the job ID. Older SQLite pages or backups may
// still hold earlier snapshots; this is logical data minimization, not erasure.
func (s *Server) save(w *worker, job Job) error {
	req := w.req
	if Terminal(job.State) {
		req.HostEnv = nil
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	exclusive := 0
	if w.contract.Exclusive {
		exclusive = 1
	}
	_, err = s.db.Exec(`INSERT INTO jobs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,code=excluded.code,reason=excluded.reason,updated=excluded.updated,request=excluded.request`,
		job.ID, job.Project, job.Root, job.Task, job.Lane, job.State, job.Code, job.Artifacts, job.Reason,
		job.Created.Format(time.RFC3339Nano), job.Updated.Format(time.RFC3339Nano), job.Deadline.Format(time.RFC3339Nano), string(b), exclusive)
	return err
}

// transition is called with s.mu held. A failed write stops admission and
// cancels workers; no caller may publish a state that SQLite did not accept.
func (s *Server) transition(w *worker, state, reason string, code int) error {
	if s.fatalErr != nil {
		return s.fatalErr
	}
	if w.job.State == state && w.job.Reason == reason && w.job.Code == code {
		return nil
	}
	candidate := w.job
	candidate.State, candidate.Reason, candidate.Code, candidate.Updated = state, reason, code, time.Now()
	if err := s.save(w, candidate); err != nil {
		return s.failLocked(fmt.Errorf("persist job %s transition to %s: %w", w.job.ID, state, err))
	}
	w.job = candidate
	w.recordPhase(candidate)
	if Terminal(state) {
		// Executors receive their own Request copy, so dropping this reference
		// never mutates a map an active executor may still read.
		w.req.HostEnv = nil
	}
	if Terminal(state) && w.inputR != nil {
		// Queued timeouts never execute a worker, so terminal transitions
		// must also release any attached input request waiting on its pipe.
		_ = w.inputR.Close()
	}
	return nil
}

func (s *Server) sweep() error {
	rows, err := s.db.Query(`SELECT id,project,root,task,lane,state,code,artifacts,reason,created,updated,deadline,request,exclusive FROM jobs ORDER BY created,id`)
	if err != nil {
		return err
	}
	type queuedFailure struct {
		worker *worker
		reason string
	}
	var unavailable []queuedFailure
	for rows.Next() {
		var w worker
		var created, updated, deadline, req string
		var exclusive int
		if err = rows.Scan(&w.job.ID, &w.job.Project, &w.job.Root, &w.job.Task, &w.job.Lane, &w.job.State, &w.job.Code, &w.job.Artifacts, &w.job.Reason, &created, &updated, &deadline, &req, &exclusive); err != nil {
			break
		}
		w.job.Created, _ = time.Parse(time.RFC3339Nano, created)
		w.job.Updated, _ = time.Parse(time.RFC3339Nano, updated)
		w.job.Deadline, _ = time.Parse(time.RFC3339Nano, deadline)
		if err = json.Unmarshal([]byte(req), &w.req); err != nil {
			break
		}
		w.contract.Exclusive = exclusive != 0
		if w.job.State == "queued" {
			if !w.req.Detach {
				unavailable = append(unavailable, queuedFailure{&w, "queued attached job lost its stdin attachment on daemon restart; not retried"})
			} else {
				w.contract, err = project.LoadFile(filepath.Join(w.job.Root, project.FileName))
				if err != nil {
					w.contract.Exclusive = exclusive != 0
					unavailable = append(unavailable, queuedFailure{&w, "queued project contract unavailable after restart: " + err.Error()})
					err = nil
				}
			}
		}
		s.jobs[w.job.ID] = &w
		if w.job.State == "queued" {
			s.order = append(s.order, w.job.ID)
		}
	}
	rowErr := rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	// SQLite has one connection; never write while the query rows are open.
	for _, failure := range unavailable {
		if err := s.transition(failure.worker, "failed", failure.reason, exitcode.RunnerFailure); err != nil {
			return err
		}
	}
	// A prior daemon's workers cannot survive it. Mark them before touching their
	// clones; never retry a task that may already have caused side effects.
	for _, w := range s.jobs {
		if !Terminal(w.job.State) && w.job.State != "queued" {
			if err := s.transition(w, "failed", "daemon exited while job was active; orphaned VM cleaned up", exitcode.RunnerFailure); err != nil {
				return err
			}
		}
	}
	// Records written before terminal scrubbing still carry the caller's
	// environment. Rewrite them before serving; startup fails closed if the
	// store rejects the write.
	for _, w := range s.jobs {
		if !Terminal(w.job.State) || len(w.req.HostEnv) == 0 {
			continue
		}
		if err := s.save(w, w.job); err != nil {
			return fmt.Errorf("scrub caller environment from terminal job %s: %w", w.job.ID, err)
		}
		w.req.HostEnv = nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	vms, err := s.tart.List(ctx)
	if err != nil {
		return fmt.Errorf("startup Tart sweep: %w", err)
	}
	for _, vm := range vms {
		if !strings.HasPrefix(vm.Name, "job-") {
			continue
		}
		id := strings.TrimPrefix(vm.Name, "job-")
		// An unrecorded clone is ours only when its random ID also has a job
		// directory in this service's cache. Leave other tools' job-* VMs alone.
		if _, ok := s.jobs[id]; !ok {
			if len(id) != 16 {
				continue
			}
			if _, err := hex.DecodeString(id); err != nil {
				continue
			}
			dir, err := JobDir(id)
			if err != nil {
				return err
			}
			if info, err := os.Stat(dir); err != nil || !info.IsDir() {
				continue
			}
		}
		if vm.Running {
			if err := s.tart.Stop(ctx, vm.Name); err != nil {
				return err
			}
		}
		if err := s.tart.Delete(ctx, vm.Name); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) submit(req Request) (Job, error) {
	contract, err := project.LoadFile(filepath.Join(req.Root, project.FileName))
	if err != nil {
		return Job{}, err
	}
	if contract.Project != req.Project {
		return Job{}, fmt.Errorf("project mismatch")
	}
	var b [8]byte
	if _, err = rand.Read(b[:]); err != nil {
		return Job{}, err
	}
	id := hex.EncodeToString(b[:])
	dir, err := JobDir(id)
	if err != nil {
		return Job{}, err
	}
	hostEnv := req.HostEnv
	if hostEnv == nil { // Old clients did not send a submission-time environment.
		hostEnv = environment()
	}
	spec, err := project.Resolve(contract, project.RunRequest{Task: req.Task, Argv: req.Argv, Lane: req.Lane, JobID: id, OutDir: "/tmp/offscreen", CacheDir: "/tmp/offscreen", HostEnv: hostEnv})
	if err != nil {
		return Job{}, err
	}
	if req.Timeout < 0 {
		return Job{}, fmt.Errorf("timeout must be nonnegative")
	}
	if req.Show && spec.Lane != project.LaneVM {
		return Job{}, fmt.Errorf("--show applies only to VM jobs")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Job{}, err
	}
	now := time.Now()
	w := &worker{job: Job{ID: id, Root: contract.Root, Project: contract.Project, Task: req.Task, Lane: spec.Lane, State: "queued", Artifacts: dir, Created: now, Updated: now}, req: req, contract: contract}
	if req.Timeout > 0 {
		w.job.Deadline = now.Add(req.Timeout)
	}
	if !req.Detach {
		w.inputR, w.inputW = io.Pipe()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatalErr != nil {
		return Job{}, s.fatalErr
	}
	if err := s.save(w, w.job); err != nil {
		return Job{}, err
	}
	s.jobs[id] = w
	s.order = append(s.order, id)
	return w.job, nil
}

func environment() map[string]string {
	m := map[string]string{}
	for _, v := range os.Environ() {
		k, v, ok := strings.Cut(v, "=")
		if ok {
			m[k] = v
		}
	}
	return m
}

// ConfigureHost installs the gate and executor together, without admitting
// host work in the gap between them. The gate can model idle/countdown state
// and return a waiting reason for status.
func (s *Server) ConfigureHost(gate func(context.Context, Job) (bool, string), execute func(context.Context, run.Options) run.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hostGate = gate
	s.hostExecute = execute
}

// All host-policy writes are durable before the candidate is published. A
// persistence failure stops admission rather than acknowledging an unsafe state.
func (s *Server) loadHostPolicy() error {
	var paused int
	var until string
	err := s.db.QueryRow(`SELECT paused, paused_until FROM host_policy WHERE id = 1`).Scan(&paused, &until)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load host policy: %w", err)
	}
	parsed, err := time.Parse(time.RFC3339Nano, until)
	if err != nil {
		return fmt.Errorf("invalid persisted host pause deadline: %w", err)
	}
	s.hostPaused, s.hostControl.pausedUntil = paused != 0, parsed
	return nil
}

// Called with s.mu held, including for the Not now action.
func (s *Server) persistHostPolicy(paused bool, until time.Time) error {
	if s.fatalErr != nil {
		return s.fatalErr
	}
	value := 0
	if paused {
		value = 1
	}
	_, err := s.db.Exec(`INSERT INTO host_policy (id, paused, paused_until) VALUES (1, ?, ?)
 ON CONFLICT(id) DO UPDATE SET paused=excluded.paused, paused_until=excluded.paused_until`, value, until.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return s.failLocked(fmt.Errorf("persist host pause policy: %w", err))
	}
	return nil
}

func (s *Server) failLocked(err error) error {
	if s.fatalErr != nil {
		return s.fatalErr
	}
	s.fatalErr = err
	close(s.fatal)
	s.stop()
	for _, active := range s.jobs {
		if active.cancel != nil {
			active.cancel()
		}
		if active.inputW != nil {
			_ = active.inputW.Close()
		}
	}
	return err
}

// PauseHost and ResumeHost are the control seam for the idle-gate helper.
func (s *Server) PauseHost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistHostPolicy(true, s.hostControl.pausedUntil); err != nil {
		return err
	}
	s.hostPaused = true
	s.clearHostCountdown()
	return nil
}

func (s *Server) ResumeHost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.persistHostPolicy(false, time.Time{}); err != nil {
		return err
	}
	s.hostPaused = false
	s.hostControl.pausedUntil = time.Time{}
	s.clearHostCountdown()
	return nil
}

// RequeueHost takes the desktop back without retrying a VM job. An active host
// worker is cancelled first; it returns to the front only after it has stopped.
func (s *Server) RequeueHost(id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.jobs[id]
	if !ok || w.job.Lane != project.LaneHost || Terminal(w.job.State) {
		return fmt.Errorf("host job %q is not active", id)
	}
	if w.job.State == "queued" {
		if err := s.transition(w, "queued", reason, 0); err != nil {
			return err
		}
		s.order = append([]string{id}, slices.DeleteFunc(s.order, func(entry string) bool { return entry == id })...)
		return nil
	}
	w.requeueReason = reason
	if w.cancel != nil {
		w.cancel()
	}
	return nil
}

func (s *Server) schedule() {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.admit()
		}
	}
}

func (s *Server) admit() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	now := time.Now()
	s.expireHostReturn(now)
	if len(s.order) == 0 {
		return
	}
	limit := min(2, max(1, s.cfg.VMSlots))
	memoryChecked := false
	blocked := map[project.Lane]bool{}
	for _, id := range s.order {
		w := s.jobs[id]
		if w.job.State != "queued" {
			continue
		}
		if w.done != nil {
			select {
			case <-w.done:
			default:
				continue // a requeued worker is still cleaning up
			}
		}
		if !w.job.Deadline.IsZero() && now.After(w.job.Deadline) {
			if s.transition(w, "failed", "queue deadline exceeded", exitcode.QueueTimeout) != nil {
				return
			}
			continue
		}
		if blocked[w.job.Lane] {
			if w.job.Lane == project.LaneHost && s.hostControl.countdownID == w.job.ID {
				s.clearHostCountdown()
			}
			if s.transition(w, "queued", "waiting for earlier "+string(w.job.Lane)+" job", 0) != nil {
				return
			}
			continue
		}
		if w.job.Lane == project.LaneVM {
			if !memoryChecked && limit > 1 && s.cfg.LowMemoryThresholdGB > 0 {
				memoryChecked = true
				if m, err := s.memory(); err != nil || m < uint64(s.cfg.LowMemoryThresholdGB)*1024*1024*1024 {
					limit = 1
				}
			}
			if s.vmRunning >= limit {
				blocked[w.job.Lane] = true
				reason := "waiting for VM slot"
				for _, other := range s.jobs {
					if other.retained {
						reason = "waiting for VM slot (retained VM; cancel job " + other.job.ID + " to release it)"
						break
					}
				}
				if s.transition(w, "queued", reason, 0) != nil {
					return
				}
				continue
			}
		}
		busy, retained := false, false
		for _, other := range s.jobs {
			if other == w || other.job.Project != w.job.Project || other.done == nil || (!w.contract.Exclusive && !other.contract.Exclusive) {
				continue
			}
			if other.retained {
				busy, retained = true, true
				continue
			}
			select {
			case <-other.done:
			default:
				busy = true
			}
		}
		if busy {
			if w.job.Lane == project.LaneHost {
				s.clearHostCountdown()
			}
			blocked[w.job.Lane] = true
			reason := "waiting for exclusive project"
			if retained {
				reason += " (retained VM; cancel its job to release it)"
			}
			if s.transition(w, "queued", reason, 0) != nil {
				return
			}
			continue
		}
		if w.job.Lane == project.LaneHost {
			if s.hostPaused || now.Before(s.hostControl.pausedUntil) {
				s.clearHostCountdown()
				if s.transition(w, "queued", "host lane paused", 0) != nil {
					return
				}
				blocked[w.job.Lane] = true
				continue
			}
			if s.hostRunning > 0 {
				s.clearHostCountdown()
				blocked[w.job.Lane] = true
				if s.transition(w, "queued", "waiting for host slot", 0) != nil {
					return
				}
				continue
			}
			if s.hostGate == nil || s.hostExecute == nil {
				s.clearHostCountdown()
				blocked[w.job.Lane] = true
				if s.transition(w, "queued", "waiting for host idle gate (host lane unavailable)", 0) != nil {
					return
				}
				continue
			}
			ready, reason := s.hostGate(s.ctx, w.job)
			if !ready {
				blocked[w.job.Lane] = true
				if s.transition(w, "queued", reason, 0) != nil {
					return
				}
				continue
			}
		}
		if w.job.Lane == project.LaneHost {
			s.clearHostCountdown()
			s.hostControl.returnUntil = time.Time{}
		}
		if s.transition(w, "preparing", "", 0) != nil {
			return
		}
		if w.job.Lane == project.LaneVM {
			s.vmRunning++
		} else {
			s.hostRunning++
		}
		ctx, cancel := context.WithCancel(s.ctx)
		w.cancel = cancel
		w.done = make(chan struct{})
		s.workers.Add(1)
		// The executor reads its own copy of the request; terminal transitions
		// may clear the worker's caller environment while it is still running.
		go s.execute(ctx, w, w.req)
	}
	// Terminal history remains in SQLite, not in the admission list.
	s.order = slices.DeleteFunc(s.order, func(id string) bool { return s.jobs[id].job.State != "queued" })
}

func (s *Server) execute(ctx context.Context, w *worker, req Request) {
	done := w.done
	// Cancel observes done only after every execution defer (including the
	// admission transfer and log close) has completed.
	defer func() {
		s.workers.Done()
		close(done)
	}()
	if w.inputR != nil {
		// A command may exit without consuming stdin. Release its HTTP writer
		// and the guardian's input copier before publishing cleanup completion.
		defer func() { _ = w.inputR.Close() }()
	}
	if !w.job.Deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, w.job.Deadline)
		defer stop()
	}
	// This defer runs before done is closed. A kept clone transfers the VM
	// lease to retained cleanup; cancellation can only clean it after done.
	retainSlot := false
	defer func() {
		s.mu.Lock()
		if w.job.Lane == project.LaneVM {
			if retainSlot {
				w.retained = true
			} else {
				s.vmRunning--
			}
		} else {
			s.hostRunning--
			s.hostControl.returnUntil = time.Time{}
			s.hostControl.returnID = ""
		}
		w.cancel = nil
		s.mu.Unlock()
	}()
	log, err := os.OpenFile(filepath.Join(w.job.Artifacts, "stream.bin"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		s.mu.Lock()
		_ = s.transition(w, "failed", err.Error(), exitcode.RunnerFailure)
		s.mu.Unlock()
		return
	}
	// A requeued job appends to the earlier attempt's stream.bin.
	if info, err := log.Stat(); err == nil {
		w.mu.Lock()
		w.written = info.Size()
		w.mu.Unlock()
	}
	w.log = log
	defer func() { _ = log.Close() }()
	var input io.Reader = strings.NewReader("")
	if w.inputR != nil {
		input = w.inputR
	}
	cfg := s.cfg
	if w.job.Lane == project.LaneVM {
		// image build records current_image while the daemon runs; read it per job.
		if fresh, err := config.Load(); err != nil {
			slog.Warn("reload current image; using the startup value", "err", err)
		} else if fresh.CurrentImage != "" {
			cfg.CurrentImage = fresh.CurrentImage
		}
	}
	phase := func(state string) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if w.job.State != "cancelled" {
			_ = s.transition(w, state, "", 0) // failure stops the service and cancels ctx
		}
	}
	var result run.Result
	switch {
	case w.job.Lane == project.LaneVM:
		result = s.executor(ctx, run.Options{ID: w.job.ID, Artifacts: w.job.Artifacts, SkipLock: true, Config: cfg, Contract: w.contract, Task: req.Task, Argv: req.Argv, Lane: w.job.Lane, HostEnv: req.HostEnv, Keep: req.Keep, Show: req.Show, Stdin: input, Stdout: streamWriter{w, 'o'}, Stderr: streamWriter{w, 'e'}, TTY: req.TTY, Phase: phase})
	case s.hostExecute != nil:
		result = s.hostExecute(ctx, run.Options{ID: w.job.ID, Artifacts: w.job.Artifacts, Config: cfg, Contract: w.contract, Task: req.Task, Argv: req.Argv, Lane: w.job.Lane, HostEnv: req.HostEnv, Stdin: input, Stdout: streamWriter{w, 'o'}, Stderr: streamWriter{w, 'e'}, TTY: req.TTY, Phase: phase})
	default:
		result = run.Result{Code: exitcode.RunnerFailure, Err: errors.New("host lane executor unavailable")}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Capture the actual executor result even if cancel raced publication.
	// Only this defer decides whether to release the execution's slot.
	retainSlot = w.job.Lane == project.LaneVM && result.Kept
	if errors.Is(result.Err, run.ErrCleanupUnconfirmed) {
		// A bounded guardian timeout cannot release the desktop for another
		// job. Stop this service; the next startup waits on its cleanup lease.
		if w.job.State != "cancelled" {
			_ = s.transition(w, "failed", result.Err.Error(), exitcode.RunnerFailure)
		}
		_ = s.failLocked(fmt.Errorf("unconfirmed process cleanup for job %s: %w", w.job.ID, result.Err))
		w.requeueReason = ""
		return
	}
	if w.job.State == "cancelled" {
		w.requeueReason = ""
		return
	}
	if w.requeueReason != "" {
		if s.transition(w, "queued", w.requeueReason, 0) != nil {
			return
		}
		s.order = append([]string{w.job.ID}, slices.DeleteFunc(s.order, func(entry string) bool { return entry == w.job.ID })...)
		w.requeueReason = ""
		return
	}
	if result.Err != nil {
		_ = s.transition(w, "failed", result.Err.Error(), result.Code)
	} else {
		_ = s.transition(w, "done", "", result.Code)
	}
	if result.Kept {
		slog.Info("VM kept for inspection", "vm", result.VM)
	}
}

func (s *Server) Serve(ctx context.Context) error {
	socket, err := Socket()
	if err != nil {
		return err
	}
	// Only remove a stale socket if no daemon responds; never steal a live server.
	if c, e := net.DialTimeout("unix", socket, 200*time.Millisecond); e == nil {
		_ = c.Close()
		return fmt.Errorf("daemon already listening at %s", socket)
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		return err
	}
	defer func() { _ = listener.Close(); _ = os.Remove(socket) }()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /submit", s.handleSubmit)
	mux.HandleFunc("GET /host/status", s.handleHostStatus)
	mux.HandleFunc("POST /host/snapshot", s.handleHostSnapshot)
	mux.HandleFunc("POST /host/pause", s.handleHostPause)
	mux.HandleFunc("POST /host/resume", s.handleHostResume)
	mux.HandleFunc("POST /host/not-now/{id}", s.handleNotNow)
	mux.HandleFunc("POST /host/return/{id}", s.handleHostReturn)
	mux.HandleFunc("POST /host/take-back/{id}", s.handleTakeBack)
	mux.HandleFunc("GET /jobs", s.handleJobs)
	mux.HandleFunc("GET /jobs/{id}", s.handleJob)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /jobs/{id}/input", s.handleInput)
	mux.HandleFunc("GET /jobs/{id}/stream", s.handleStream)
	mux.HandleFunc("POST /prune", s.handlePrune)
	mux.HandleFunc("GET /version", s.handleVersion)
	server := &http.Server{Handler: logProtocol(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() { // #nosec G118 -- shutdown must run after the serving context is cancelled
		select {
		case <-ctx.Done():
		case <-s.ctx.Done():
		case <-s.fatal:
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	err = server.Serve(listener)
	s.mu.Lock()
	fatalErr := s.fatalErr
	s.mu.Unlock()
	if fatalErr != nil {
		return fatalErr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func bad(w http.ResponseWriter, code int, err error) { http.Error(w, err.Error(), code) }
func (s *Server) handleSubmit(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		bad(w, 400, err)
		return
	}
	job, err := s.submit(req)
	if err != nil {
		bad(w, 400, err)
		return
	}
	respond(w, job)
}

func (s *Server) handleJobs(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]Job, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job.record())
	}
	slices.SortFunc(jobs, func(a, b Job) int { return a.Created.Compare(b.Created) })
	respond(w, jobs)
}

func (s *Server) lookup(id string) (*worker, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[id]
	return job, ok
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.lookup(r.PathValue("id"))
	if !ok {
		bad(w, 404, errors.New("unknown job"))
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	respond(w, job.record())
}

// record is the job as the socket API reports it. Callers hold Server.mu.
func (w *worker) record() Job {
	job := w.job
	job.Retained = w.retained
	return job
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	// One budget covers worker shutdown, any concurrent cleanup already in
	// progress, and this request's Tart cleanup/retry.
	ctx, cancel := context.WithTimeout(r.Context(), 130*time.Second)
	defer cancel()
	s.mu.Lock()
	job, ok := s.jobs[r.PathValue("id")]
	if !ok {
		s.mu.Unlock()
		bad(w, 404, errors.New("unknown job"))
		return
	}
	if !Terminal(job.job.State) {
		if err := s.transition(job, "cancelled", "cancelled by user", exitcode.RunnerFailure); err != nil {
			s.mu.Unlock()
			bad(w, 500, err)
			return
		}
		if job.cancel != nil {
			job.cancel()
		}
		if job.inputW != nil {
			_ = job.inputW.Close()
		}
	}
	done := job.done
	s.mu.Unlock()
	// The response proves the clone has been stopped and deleted, not merely
	// that cancellation was requested. Execute owns bounded cleanup budgets.
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				bad(w, 504, errors.New("VM cleanup timed out"))
			}
			return
		}
	}
	if err := s.cleanupRetained(ctx, job); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			bad(w, 504, errors.New("VM cleanup timed out; retry cancel to release its slot"))
			return
		}
		bad(w, 500, fmt.Errorf("retained VM cleanup: %w; retry cancel to release its slot", err))
		return
	}
	s.mu.Lock()
	record := job.record()
	s.mu.Unlock()
	respond(w, record)
}

// cleanupRetained serializes cancels for one owned clone, without holding the
// admission lock during Tart operations. A failed stop/delete retains both the
// VM slot and the project's exclusion until a later cancel succeeds.
func (s *Server) cleanupRetained(ctx context.Context, job *worker) error {
	for {
		s.mu.Lock()
		if !job.retained {
			s.mu.Unlock()
			return nil
		}
		// Close cancels s.ctx before waiting. Admission of cleanup and its
		// wait-group registration share this lock, so no cleanup can begin
		// after shutdown has started waiting or released the singleton lock.
		if err := s.ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		if pending := job.retainedCleanup; pending != nil {
			s.mu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		pending := make(chan struct{})
		job.retainedCleanup = pending
		s.retainedCleanups.Add(1)
		defer s.retainedCleanups.Done()
		s.mu.Unlock()

		// Never trust an executor-supplied VM name or touch another job's VM.
		// List first: an operator may have stopped or removed this clone.
		cleanup, cancel := context.WithTimeout(ctx, 90*time.Second)
		err := s.deleteRetained(cleanup, "job-"+job.job.ID)
		cancel()
		s.mu.Lock()
		if err == nil {
			job.retained = false
			s.vmRunning--
		}
		job.retainedCleanup = nil
		close(pending)
		s.mu.Unlock()
		return err
	}
}

func (s *Server) deleteRetained(ctx context.Context, name string) error {
	vms, err := s.tart.List(ctx)
	if err != nil {
		return err
	}
	for _, vm := range vms {
		if vm.Name != name {
			continue
		}
		if vm.Running {
			if err := s.tart.Stop(ctx, name); err != nil {
				return err
			}
		}
		return s.tart.Delete(ctx, name)
	}
	return nil // already deleted by the operator
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	job, ok := s.lookup(r.PathValue("id"))
	if !ok {
		bad(w, 404, errors.New("unknown job"))
		return
	}
	if job.inputW == nil {
		bad(w, 409, errors.New("job has no attached stdin"))
		return
	}
	_, err := io.Copy(job.inputW, r.Body)
	_ = job.inputW.Close()
	if err != nil {
		bad(w, 500, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	job, ok := s.lookup(r.PathValue("id"))
	if !ok {
		bad(w, 404, errors.New("unknown job"))
		return
	}
	// Only clients that ask get phase frames; older clients reject any frame
	// kind other than stdout and stderr.
	phases := r.URL.Query().Get(PhasesParam) == "1"
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		bad(w, 500, errors.New("stream unavailable"))
		return
	}
	s.mu.Lock()
	path := filepath.Join(job.job.Artifacts, "stream.bin")
	s.mu.Unlock()
	offset := int64(0)
	nextPhase := 0
	// sendPhases writes pending phases recorded at or before offset, so a
	// phase never overtakes output written before it.
	var pending []phaseMark
	sendPhases := func() bool {
		for len(pending) > 0 && pending[0].offset <= offset {
			b, err := json.Marshal(pending[0].phase)
			if err != nil || WriteFrame(w, FramePhase, b) != nil {
				return false
			}
			nextPhase = pending[0].seq + 1
			pending = pending[1:]
			flusher.Flush()
		}
		return true
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		// Scan once after worker cleanup, even if the previous scan reached
		// EOF just before the last frame was appended.
		s.mu.Lock()
		done, state := job.done, job.job.State
		s.mu.Unlock()
		complete := Terminal(state) && done == nil
		if done != nil {
			select {
			case <-done:
				complete = Terminal(state)
			default:
			}
		}
		// Without phases, read to EOF. With phases, stop at the size the
		// phases were recorded against; later frames wait for the next scan.
		// A complete job records no more phases, so read it to EOF: a job
		// loaded after a restart has neither phases nor a written size.
		limit := int64(-1)
		if phases {
			var recorded int64
			pending, recorded = job.phasesSince(nextPhase)
			if !complete {
				limit = recorded
			}
		}
		f, err := os.Open(path)
		if err == nil {
			_, _ = f.Seek(offset, io.SeekStart)
			for limit < 0 || offset < limit {
				if !sendPhases() {
					_ = f.Close()
					return
				}
				kind, p, e := ReadFrame(f)
				if e != nil {
					break
				}
				if e = WriteFrame(w, kind, p); e != nil {
					_ = f.Close()
					return
				}
				offset += int64(5 + len(p))
				flusher.Flush()
			}
			_ = f.Close()
		}
		if !sendPhases() {
			return
		}
		if complete {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
