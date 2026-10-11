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
	job           Job
	req           Request
	contract      project.Contract
	cancel        context.CancelFunc
	inputR        *io.PipeReader
	inputW        *io.PipeWriter
	log           *os.File
	done          chan struct{}
	requeueReason string
	mu            sync.Mutex
}

func (w *worker) write(kind byte, p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := WriteFrame(w.log, kind, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

type streamWriter struct {
	w    *worker
	kind byte
}

func (s streamWriter) Write(p []byte) (int, error) { return s.w.write(s.kind, p) }

type Server struct {
	mu                     sync.Mutex
	workers                sync.WaitGroup
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
	ctx         context.Context
	stop        context.CancelFunc
	fatal       chan struct{}
	fatalErr    error
}

func Open(cfg config.Config) (*Server, error) {
	return open(cfg, run.Execute, tart.Client{}, run.AvailableMemory)
}

func open(cfg config.Config, executor func(context.Context, run.Options) run.Result, vm tart.Client, memory func() (uint64, error)) (*Server, error) {
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
	if err := os.Chmod(filepath.Join(dir, "jobs.sqlite"), 0o600); err != nil {
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	s := &Server{db: db, lock: lock, cfg: cfg, executor: executor, tart: vm, memory: memory, jobs: make(map[string]*worker), ctx: ctx, stop: stop, fatal: make(chan struct{})}
	if err := s.sweep(); err != nil {
		stop()
		_ = db.Close()
		_ = lock.Close()
		return nil, err
	}
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
	err := s.db.Close()
	_ = s.lock.Close()
	return err
}

func (s *Server) save(w *worker, job Job) error {
	b, err := json.Marshal(w.req)
	if err != nil {
		return err
	}
	exclusive := 0
	if w.contract.Exclusive {
		exclusive = 1
	}
	_, err = s.db.Exec(`INSERT INTO jobs VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,code=excluded.code,reason=excluded.reason,updated=excluded.updated`,
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
		s.fatalErr = fmt.Errorf("persist job %s transition to %s: %w", w.job.ID, state, err)
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
		return s.fatalErr
	}
	w.job = candidate
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

// PauseHost and ResumeHost are the control seam for the idle-gate helper.
func (s *Server) PauseHost()  { s.mu.Lock(); defer s.mu.Unlock(); s.hostPaused = true }
func (s *Server) ResumeHost() { s.mu.Lock(); defer s.mu.Unlock(); s.hostPaused = false }

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
	limit := min(2, max(1, s.cfg.VMSlots))
	if limit > 1 && s.cfg.LowMemoryThresholdGB > 0 {
		if m, err := s.memory(); err != nil || m < uint64(s.cfg.LowMemoryThresholdGB)*1024*1024*1024 {
			limit = 1
		}
	}
	now := time.Now()
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
			if s.transition(w, "queued", "waiting for earlier "+string(w.job.Lane)+" job", 0) != nil {
				return
			}
			continue
		}
		if w.job.Lane == project.LaneVM && s.vmRunning >= limit {
			blocked[w.job.Lane] = true
			if s.transition(w, "queued", "waiting for VM slot", 0) != nil {
				return
			}
			continue
		}
		if w.job.Lane == project.LaneHost {
			if s.hostPaused {
				if s.transition(w, "queued", "host lane paused", 0) != nil {
					return
				}
				blocked[w.job.Lane] = true
				continue
			}
			if s.hostRunning > 0 {
				blocked[w.job.Lane] = true
				if s.transition(w, "queued", "waiting for host slot", 0) != nil {
					return
				}
				continue
			}
			if s.hostGate == nil || s.hostExecute == nil {
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
		busy := false
		for _, other := range s.jobs {
			if other == w || other.job.Project != w.job.Project || other.done == nil {
				continue
			}
			select {
			case <-other.done:
			default:
				if w.contract.Exclusive || other.contract.Exclusive {
					busy = true
				}
			}
		}
		if busy {
			blocked[w.job.Lane] = true
			if s.transition(w, "queued", "waiting for exclusive project", 0) != nil {
				return
			}
			continue
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
		go s.execute(ctx, w)
	}
	// Terminal history remains in SQLite, not in the admission list.
	s.order = slices.DeleteFunc(s.order, func(id string) bool { return s.jobs[id].job.State != "queued" })
}

func (s *Server) execute(ctx context.Context, w *worker) {
	defer s.workers.Done()
	done := w.done
	defer close(done)
	if !w.job.Deadline.IsZero() {
		var stop context.CancelFunc
		ctx, stop = context.WithDeadline(ctx, w.job.Deadline)
		defer stop()
	}
	defer func() {
		s.mu.Lock()
		if w.job.Lane == project.LaneVM {
			s.vmRunning--
		} else {
			s.hostRunning--
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
	w.log = log
	defer func() { _ = log.Close() }()
	var input io.Reader = strings.NewReader("")
	if w.inputR != nil {
		input = w.inputR
	}
	cfg := s.cfg
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
		result = s.executor(ctx, run.Options{ID: w.job.ID, Artifacts: w.job.Artifacts, SkipLock: true, Config: cfg, Contract: w.contract, Task: w.req.Task, Argv: w.req.Argv, Lane: w.job.Lane, HostEnv: w.req.HostEnv, Keep: w.req.Keep, Stdin: input, Stdout: streamWriter{w, 'o'}, Stderr: streamWriter{w, 'e'}, TTY: w.req.TTY, Phase: phase})
	case s.hostExecute != nil:
		result = s.hostExecute(ctx, run.Options{ID: w.job.ID, Artifacts: w.job.Artifacts, Config: cfg, Contract: w.contract, Task: w.req.Task, Argv: w.req.Argv, Lane: w.job.Lane, HostEnv: w.req.HostEnv, Stdin: input, Stdout: streamWriter{w, 'o'}, Stderr: streamWriter{w, 'e'}, TTY: w.req.TTY, Phase: phase})
	default:
		result = run.Result{Code: exitcode.RunnerFailure, Err: errors.New("host lane executor unavailable")}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
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
	mux.HandleFunc("GET /jobs", s.handleJobs)
	mux.HandleFunc("GET /jobs/{id}", s.handleJob)
	mux.HandleFunc("POST /jobs/{id}/cancel", s.handleCancel)
	mux.HandleFunc("POST /jobs/{id}/input", s.handleInput)
	mux.HandleFunc("GET /jobs/{id}/stream", s.handleStream)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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
		jobs = append(jobs, job.job)
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
	respond(w, job.job)
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
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
		case <-time.After(130 * time.Second):
			bad(w, 504, errors.New("VM cleanup timed out"))
			return
		case <-r.Context().Done():
			return
		}
	}
	s.mu.Lock()
	record := job.job
	s.mu.Unlock()
	respond(w, record)
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
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	flusher, ok := w.(http.Flusher)
	if !ok {
		bad(w, 500, errors.New("stream unavailable"))
		return
	}
	path := filepath.Join(job.job.Artifacts, "stream.bin")
	offset := int64(0)
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
		f, err := os.Open(path)
		if err == nil {
			_, _ = f.Seek(offset, io.SeekStart)
			for {
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
