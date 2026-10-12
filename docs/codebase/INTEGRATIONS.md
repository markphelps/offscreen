# External Integrations

Snapshot: checkout `ea6387d` (2026-10-12), reconciled with the concern repairs through `4e31a32`. This inventory describes local OS/VM integrations as well as network transport.

## Core Sections (Required)

### 1) Integration Inventory

| System | Type | Purpose | Auth model | Criticality | Evidence |
|--------|------|---------|------------|-------------|----------|
| Tart | External CLI / macOS VMs | Pull, clone, boot headless, inspect IP, stop/delete VM images | Local user process; base image pulled by Tart | Required for VM lane/image build | `internal/tart/tart.go`; `internal/image/build.go` |
| Cirrus Labs image on GHCR | OCI VM image | Default macOS/Xcode base | Registry access delegated to Tart; app has no registry client | Required when default image must be downloaded | `internal/config/config.go`; `internal/tart/tart.go` |
| SSH guest | TCP/SSH | Stream commands, tar transfers, PTY for attached TTY runs | Password/private key from user config; host key verification disabled for ephemeral guests | Required for VM jobs | `internal/guest/ssh.go`; `internal/guest/stream.go` |
| Daemon socket | Local Unix socket / JSON protocol | CLI submissions, status/logs/cancel and helper controls | Owner-only filesystem socket | Required for daemon-backed jobs | `DAEMON.md`; `internal/daemon/protocol.go`; `internal/daemon/client.go` |
| SQLite | Embedded local DB | Durable jobs (completed jobs expire after `job_retention`, default seven days) and host pause/hold policy | Owner-only local state files | Required for daemon | `internal/daemon/server.go`; `DAEMON.md` |
| launchd / Launch Services / codesign | macOS lifecycle/identity | Install a signed daemon app and LaunchAgent; separately bundle/install helper | User launch agent and local ad-hoc signature | Required for installed background services | `internal/daemon/install.go`; `helper/scripts/bundle.sh`; `helper/scripts/install.sh` |
| Native macOS sensors / event tap / notifications | OS APIs | Host eligibility, input candidate detection, countdown/take-back controls | Helper Input Monitoring and Notifications; no helper Accessibility grant | Required for host lane | `helper/README.md`; `helper/Sources/OffscreenHelper/Sensors.swift`; `helper/Sources/OffscreenHelper/HelperApp.swift` |
| Xcode / System Events / display tools | Guest toolchain/automation | GUI readiness, UI tests, screenshot/display verification | Guest login, Xcode Automation Mode; daemon host Local Network grant for guest connectivity | Required for supplied VM setup/example | `internal/image/build.go`; `internal/image/scripts/check.sh`; `internal/image/scripts/display.sh`; `examples/hello/README.md` |
| GitHub Actions / GoReleaser / Homebrew tap | Development/distribution | CI, release archive/checksums, planned cask publication | Publication credentials not defined in release config | Development/distribution | `.github/workflows/ci.yml`; `.goreleaser.yaml`; `README.md` |

There is no separate broker in the mapped runtime: scheduling is inside the local daemon, and queue state is SQLite (`internal/daemon/server.go`). No API gateway, remote worker transport, or application monitoring backend appears in these runtime/configuration files.

### 2) Data Stores

| Store | Role | Access layer | Key risk | Evidence |
|-------|------|--------------|----------|----------|
| `~/Library/Application Support/offscreen/jobs.sqlite` | Jobs, request snapshots, pause/hold state | `internal/daemon/server.go` using database/sql + SQLite driver | Caller environment (may include secrets) is persisted only while a job is queued/active/requeued and is cleared from the request when the job becomes terminal, including legacy terminal rows at startup (C02); old SQLite pages and backups are not erased. Completed rows and their job directories are expired at startup and hourly after `job_retention` (C13); freed pages are reused, and the file keeps its high-water size | `DAEMON.md`; `docs/config.md` |
| `~/Library/Caches/offscreen/jobs/<id>/` | Stdout/stderr logs and collected artifacts | Daemon log handling + runner collection | Kept until the job is expired (startup and hourly after `job_retention`, default seven days) or removed by `offscreen prune`; protected jobs (queued/active, retained `--keep`, unconfirmed VM cleanup) are kept regardless of age (C13); artifact sensitivity | `internal/daemon/server.go`; `internal/daemon/prune.go`; `internal/daemon/expiry.go`; `internal/run/run.go`; `DAEMON.md` |
| `~/Library/Caches/offscreen/projects/<hash>/` | Per-checkout persistent task cache | `projectKey` hashes absolute root; mounted into VM as cache | Cache persists across disposable jobs | `internal/run/run.go`; `internal/run/host.go`; `internal/run/transport.go` |
| User `config.toml` | Machine settings, image reference, SSH credentials | `internal/config/config.go`; `internal/config/current_image.go` | Password is stored as config text | `docs/config.md` |
| Tart image/clone storage | Golden VM and job disks | Tart owns storage location | Retained failed clone holds admission leases until canceled | `internal/tart/tart.go`; `DAEMON.md` |
| `host-group-*.json` recovery markers | Evidence of process-group cleanup ownership | Guardian / startup cleanup guard | Leftover marker intentionally blocks admission until resolved | `internal/run/process.go`; `internal/run/guardian_darwin.go`; `DAEMON.md` |

`OFFSCREEN_STATE_DIR` overrides database/socket and job/cache locations for isolated runs; the chosen Unix socket path must be short enough (`DAEMON.md`).

### 3) Secrets and Credentials Handling

SSH credentials come from the optional user config. Defaults are `admin` / `admin`; `ssh_key` accepts a private-key path and expands `~/`. At least one authentication method is required (`internal/config/config.go`; `internal/guest/ssh.go`). Config writes use owner-only temporary files and atomic rename; image-reference updates preserve unrelated TOML/comments (`internal/config/config.go`; `internal/config/current_image.go`).

The CLI snapshots its caller environment, including values containing `=`, into the daemon request. Host preparation and host task execution use that snapshot; VM task environment construction supplies only the four runner variables (`cmd/offscreen/main.go`; `internal/run/run.go`; `internal/run/host.go`). The snapshot must persist while a job is queued, active, or requeued, so a job resumed after requeue or daemon restart runs with its own caller's environment (old requests without one fall back to the daemon's environment). When the job becomes terminal, `save` writes the request without `HostEnv` in the same statement that records the terminal state, and `transition` drops it from in-memory state; executors run with their own request copy, so no map they read is mutated. Startup `sweep` rewrites terminal rows from earlier versions before serving. A failed write is fail-closed: no completion is published and startup aborts. Retained `--keep` cleanup uses only the job ID. This is logical minimization only; SQLite free pages, journals, and backups can still hold earlier snapshots (C02; `internal/daemon/server.go:save/transition/sweep`; `internal/daemon/env_scrub_test.go`; `DAEMON.md`).

SSH uses `InsecureIgnoreHostKey` by design. The code validates that the supplied guest address parses as an IP and obtains it from Tart, but does not cryptographically verify guest identity. This trusts the ephemeral Tart network; no violation of that boundary was shown, but it remains a real tradeoff (C10; `internal/guest/ssh.go`; `internal/run/transport.go`). Retention of job history is bounded by `job_retention` (default seven days) and `offscreen prune` (C13; `docs/config.md`; `DAEMON.md`). [TODO] No credential rotation procedure, and no procedure for purging old backups of the state file, is defined in the inspected public guides/config.

### 4) Reliability and Failure Behavior

- Tart operations use context-aware subprocesses and include command/output diagnostics; the wrapper has no generic retry loop (`internal/tart/tart.go`).
- VM readiness retries IP/SSH until a five-minute budget, with two-second intervals in the runner. GUI readiness has a two-minute budget and three-second intervals (`internal/run/transport.go`).
- SSH dialing and handshake have five-second bounds, shortened by caller deadlines. Stream cancellation closes the SSH session/client (`internal/guest/ssh.go`; `internal/guest/stream.go`).
- Artifact collection gets one reconnect/retry under a separate 30-second recovery budget after job cancellation/deadline closes the original connection. Normal clone cleanup uses a separate 90-second budget (`internal/run/run.go`).
- Daemon restart and retained-clone cleanup rules are documented in [ARCHITECTURE.md](ARCHITECTURE.md); absent/stale/unsafe helper signals hold host admission, and cleanup uncertainty stops it (`DAEMON.md`; `internal/daemon/host.go`).
- No automatic remote failover is configured. The supported service is local to the Mac (`README.md`; `internal/daemon/client.go`).

### 5) Observability for Integrations

Go logs use `slog` text on stderr. Tart errors include subprocess output; guest readiness errors include the last attempt, and a no-route-to-host diagnostic points to possible Local Network access without asserting permission denial (`internal/logging/logging.go`; `internal/tart/tart.go`; `internal/run/transport.go`).

The CLI exposes status reasons, logs, artifact directories, and job identity. The guest display LaunchAgent writes `display.log` / `display.err`, cited by image setup errors (`cmd/offscreen/main.go`; `internal/image/build.go`). The helper provides a manual input probe (`helper/README.md`).

No metrics exporter, tracing SDK, or external alert destination is configured in the mapped runtime manifests/config. Each expiry pass logs `deleted`, `failed`, `skipped`, and `recent` counts on stderr, and logs failures at warn/error level (`internal/daemon/expiry.go`). [TODO] Integration latency, real-workload disk-growth baselines, and operational alert expectations are not measured by this map; the only storage measurement is the bounded-page expiry test (`internal/daemon/expiry_test.go`).

### 6) Evidence

- `internal/tart/tart.go`
- `internal/guest/ssh.go`
- `internal/guest/stream.go`
- `internal/run/run.go`
- `internal/run/transport.go`
- `internal/config/config.go`
- `internal/daemon/server.go`
- `internal/daemon/prune.go`
- `internal/daemon/expiry.go`
- `DAEMON.md`
- `helper/README.md`
- `.goreleaser.yaml`
