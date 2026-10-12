# Architecture

Snapshot: checkout `ea6387d` (2026-10-12). Boundaries and flows are inferred only from the identified source responsibilities; they are not a claim of a formal architecture framework.

## Core Sections (Required)

### 1) Architectural Style

Offscreen is a local CLI/daemon system with a durable queue, two execution lanes, and a separate native observation/control helper. Go packages are divided by capability (`project`, `daemon`, `run`, `guest`, `tart`, `image`) rather than controller/service/repository directories (`cmd/offscreen/main.go`; `internal/daemon/server.go`; `helper/Package.swift`).

The main constraints are a real macOS GUI session in each guest, one host job at a time, at most two runner VM slots, and confirmed cleanup before host admission resumes (`internal/run/transport.go`; `internal/daemon/server.go`; `internal/run/process.go`). The helper sends observations; the daemon makes admission decisions (`helper/Sources/OffscreenHelper/Bridge.swift`; `internal/daemon/host.go`).

### 2) System Flow

```text
CLI -> project contract -> HTTP/JSON over owner-only Unix socket
    -> SQLite-backed daemon queue -> lane admission
    -> VM runner -> Tart + tar/SSH -> guest command
    -> host runner -> guardian -> local command
    -> framed stdout/stderr + artifacts + terminal result -> CLI

Swift helper -> desktop snapshot / actions -> daemon host policy
```

1. The CLI discovers and validates `.offscreen.toml`, checks task/direct-command form, captures the caller's environment, and submits a request. Named commands retain shell semantics; direct commands retain argv boundaries (`cmd/offscreen/main.go`; `internal/project/project.go`).
2. The daemon loads the contract and resolves the request, assigns identity/artifact paths, persists it in `jobs.sqlite`, and queues it. SQLite stores job metadata plus request JSON, including the environment snapshot; a separate singleton `host_policy` table persists manual pause and timed hold (`internal/daemon/server.go`; `internal/daemon/protocol.go`).
3. Every 200 ms the scheduler examines queued work. Blocked work holds later jobs in its lane; the other lane can proceed. VM capacity is clamped to one/two, with a failed/low memory probe reducing it to one. Host capacity is one. If either job is exclusive, jobs with the same project name are serialized across lanes (`internal/daemon/server.go`, `schedule` / `admit`).
4. VM execution runs optional host preparation, clones `job-<id>`, boots headless with read-only worktree and writable artifact/cache shares, waits for SSH/GUI, then copies the worktree/prepared products using tar over SSH. The worktree copy excludes `.git` and `.build`. Guest setup precedes the command; only four explicit runner environment variables are supplied (`internal/run/run.go`; `internal/run/transport.go`).
5. Host admission checks fresh helper observations, permissions/tap, unlocked/awake session, idle camera/microphone, idle threshold, and pause state, then gives a ten-second countdown. `ExecuteHost` runs preparation followed by the task in the real checkout with the caller environment and four task variables. It skips guest setup and VM copies (`internal/daemon/host.go`; `internal/run/host.go`).
6. Workers append output frames to `stream.bin`; HTTP streaming preserves stdout/stderr identity and follows until cleanup completes. VM output is collected and the clone deleted unless a failed started VM is retained. Terminal status carries code/reason/artifact path back to the CLI; attached runs print job/artifact identity on stderr (`internal/daemon/server.go`; `internal/daemon/protocol.go`; `internal/daemon/client.go`; `cmd/offscreen/main.go`).
7. `offscreen prune --older-than <age> --dry-run|--yes` asks the daemon (`POST /prune`) to run its expiry engine, `Server.Prune`, with a cutoff from the daemon clock. Under the admission lock it selects terminal jobs completed before the cutoff whose worker has returned and that hold no retained clone or cleanup; Tart must show no `job-<id>` clone left. Deletion removes the ID-derived job directory, then the row, then the memory entry, rechecking eligibility under the lock first. Partial failures stay listed with their original completion time and are retried by the next run; they do not stop the daemon. Persistent project caches and images are outside the engine (`internal/daemon/prune.go`; `cmd/offscreen/main.go`, `PruneCmd`).
8. Automatic expiry drives the same engine with cutoff = daemon clock minus `job_retention` (default `168h`). `open` runs one pass after `sweep` succeeds and before scheduling or serving; `maintain` then runs one pass per hour on a single ticker goroutine until the daemon context ends, and `Close` waits for it. Engine errors (for example an unlistable Tart inventory) and per-job failures are logged and retried on the next pass; they never stop the daemon or rewrite a completion time (`internal/daemon/expiry.go`; `internal/daemon/server.go`, `open` / `Close`).

### 3) Layer/Module Responsibilities

These are observed/documented boundaries, not additional enforceable rules.

| Module | Owns | Must not own | Evidence |
|--------|------|--------------|----------|
| CLI | Parsing, submission, input/output forwarding, exit status | App-specific behavior beyond contract commands | `cmd/offscreen/main.go`; `README.md` |
| Project | Discovery, strict validation, task/lane/share/env resolution | Execution or job creation | `internal/project/project.go` package comment |
| Daemon | SQLite state, ordering, admission/exclusion, HTTP API, durable host controls, completed-job expiry | Native sensor/UI implementation | `internal/daemon/server.go`; `internal/daemon/host.go` |
| Runner | Preparation, VM/host command lifecycle, artifacts, cleanup | Scheduling when daemon owns admission (`SkipLock`) | `internal/run/run.go`; `internal/run/host.go` |
| Guardian | Independent process-group ownership, cleanup protocol/lease/markers | CLI flag parsing or queue admission | `internal/run/process.go`; `internal/run/guardian_darwin.go` |
| Guest/Tart | SSH and external VM CLI adapters | Global unowned clone deletion | `internal/guest/ssh.go`; `internal/tart/tart.go` package comment |
| Helper | One-second observations, candidate event filter, menu/countdown/notifications/actions | Queue execution | `helper/Sources/OffscreenHelper/HelperApp.swift`; `helper/Sources/OffscreenHelper/Sensors.swift`; `helper/Sources/OffscreenHelper/Bridge.swift` |
| Image | Embedded display/automation checks, custom setup, image publication | Project-specific task tooling by default | `internal/image/build.go`; `docs/config.md` |

### 4) Reused Patterns

| Pattern | Where | Concrete purpose |
|---------|-------|------------------|
| Explicit function seams | `internal/daemon/server.go`, `open`; `internal/run/run.go`, `collectArtifacts` | Substitute executor, memory, cleanup/collection behavior in tests |
| Adapter wrappers | `internal/tart/tart.go`; `internal/guest/ssh.go` | Centralize external command/transport behavior and diagnostics |
| Persist before publish | `internal/daemon/server.go`, `transition` / host policy persistence | Do not acknowledge state changes SQLite failed to record; failure stops admission |
| Context-bound work, independent cleanup budget | `internal/run/run.go`; `internal/run/process.go` | Cancel tasks while still allowing artifact/process/VM cleanup |
| Same-executable guardian | Both command entrypoints; `internal/run/process.go` | Cleanup survives daemon death through a separate process and liveness pipe |
| Atomic config replacement | `internal/config/config.go`; `internal/config/current_image.go` | Protect complete writes and preserve unrelated TOML on image updates |
| Native shared UI model | `helper/Sources/OffscreenHelper/HelperApp.swift` | Main-actor helper state drives menu, panel, and notifications |

Startup obtains the daemon lock, waits for prior host cleanup, opens SQLite, loads host policy, and sweeps owned VM leftovers before scheduling. HTTP serves on the per-user Unix socket (`cmd/offscreend/main.go`; `internal/daemon/server.go`).

A positive submission timeout sets one absolute deadline covering both queue wait and execution. Queue expiry returns 75; command execution preserves its exit status, while infrastructure failure uses 70. States are `queued`, `preparing`, `running`, `collecting`, then `done`, `failed`, or `cancelled`; host take-back can requeue the same identity after cleanup (`internal/daemon/server.go`; `internal/daemon/protocol.go`; `internal/exitcode/exitcode.go`).

Restart fails active jobs and queued attached jobs; detached queued jobs may resume after reloading their contracts. Stored queued work can therefore observe later contract edits after restart. Retained clones hold slots/exclusive leases during a daemon lifetime and are swept at restart (`internal/daemon/server.go`; `DAEMON.md`).

### 5) Known Architectural Risks

- Owner-only socket access is the intended local trust boundary (C12). Same-user processes can submit arbitrary contracts or helper snapshots; no application-identity authentication is promised. Host preparation executes on the real Mac even for VM jobs, as the project contract specifies (C11) (`internal/daemon/server.go`; `internal/run/run.go`; `docs/config.md`).
- Automatic return detection depends on a PID-zero filter that each installation enables after its own probe. The helper defaults it off, and the menu's manual return works either way. The filter separated physical from injected input on the maintainer's Mac during acceptance; that does not prove other hardware or a given installation's settings (C01; `helper/Sources/OffscreenHelper/InputFilter.swift`; `helper/README.md`).
- Host take-back/requeue restarts preparation and the task with closed stdin. Task side effects need to tolerate interruption/re-execution; input cannot be replayed (`internal/daemon/server.go`; `DAEMON.md`).
- A leftover guardian marker deliberately blocks restart until manual inspection/removal (C03, fail-closed by design). Tart list/cleanup failure during the shared startup sweep also prevents daemon startup; Tart is a prerequisite for both lanes (C17) (`internal/run/guardian_darwin.go`; `internal/daemon/server.go`; `DAEMON.md`).
- Retained VMs (C06) and FIFO lane-head waiting (C14) can delay unrelated projects; both are intentional. Terminal records no longer keep the caller environment (C02). Completed-job history, logs and artifacts expire automatically after `job_retention` (default seven days) at startup and hourly, and can be removed earlier with the previewable `prune` command; both use the shared expiry engine (C13). Protected jobs (queued/active, retained `--keep`, unconfirmed VM cleanup) are kept regardless of age, so history is bounded only for completed, cleaned-up work. Deleted rows free SQLite pages for reuse without shrinking the file (`internal/daemon/server.go`; `internal/daemon/prune.go`; see [CONCERNS.md](CONCERNS.md)).

### 6) Evidence

- `cmd/offscreen/main.go`
- `cmd/offscreend/main.go`
- `internal/project/project.go`
- `internal/daemon/server.go`
- `internal/daemon/host.go`
- `internal/daemon/protocol.go`
- `internal/daemon/prune.go`
- `internal/daemon/expiry.go`
- `internal/run/run.go`
- `internal/run/process.go`
- `helper/Sources/OffscreenHelper/HelperApp.swift`
- `DAEMON.md`
