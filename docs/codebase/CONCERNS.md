# Codebase Concerns

Snapshot: checkout `ea6387d` (2026-10-12), with concern dispositions validated at that checkout. Each concern has a stable ID (C01–C17, defined in the [concern ledger](#concern-ledger) below). The tables separate four kinds of finding: reproduced defects, confirmed operational gaps, intentional constraints or trust tradeoffs, and unmeasured performance signals. Only C04 is a reproduced defect. C02, C05, and C13 are confirmed gaps with planned repairs. No concern is a demonstrated breach of a promised security boundary.

## Core Sections (Required)

### 1) Top Risks (Prioritized)

| Severity | Concern | Evidence | Impact | Suggested action |
|----------|---------|----------|--------|------------------|
| High | C02: Full caller environment stays in SQLite after a job ends | `cmd/offscreen/main.go`, `hostEnvironment`; `internal/daemon/server.go`, `save` / `transition`; `DAEMON.md` | Secrets in the submitting environment persist in `jobs.sqlite` and its backups for the life of the record. Owner-only permissions limit who can read them; they do not limit how long they are kept | Planned repair: scrub the environment from terminal records and in-memory state; keep it while queued, active, or requeued |
| Medium | C13: Job records and output have no expiry | `internal/daemon/server.go`, `sweep` / `handleJobs` / `save`; `internal/daemon/protocol.go`, `JobDir` | Rows, request snapshots, logs, and artifacts accumulate; startup and status work grow with history. No bottleneck has been measured | Planned repair: bounded automatic expiry of completed, cleaned-up jobs plus a previewable prune command |
| Medium | C04: Coverage instrumentation breaks two guardian stderr assertions | `internal/run/process.go`, `runGroup`; `internal/run/process_test.go`; `internal/run/host_test.go` | `go test ./... -cover` fails although normal tests pass | Planned repair: make instrumented guardian subprocesses preserve exact task output; do not weaken the assertions |
| Medium | C05: CI runs only Go lint/tests | `.github/workflows/ci.yml`; `mise.toml`; `helper/Package.swift`; `CONTRIBUTING.md` | Swift helper and shell-script regressions can pass CI | Planned repair: add Swift build/test and pinned ShellCheck; Tart, XCUITest, and human permission checks stay local |
| Low (operational) | C03: Unconfirmed guardian cleanup blocks admission until a person resolves it | `internal/run/process.go`, `ErrCleanupUnconfirmed`; `internal/run/guardian_darwin.go`, `WaitForHostCleanup`; `internal/daemon/host_cleanup_failure_test.go` | Service is unavailable until leftover markers are inspected. This is the intended fail-closed barrier against reusing a desktop with a possibly live task | Keep; preserve the manual recovery instructions in `DAEMON.md` |

C06, C14, C15, and C17 also delay or block work. They are intentional constraints, not defects; see the ledger.

### 2) Technical Debt

| Debt item | Why it exists / evidence limit | Where | Risk if ignored | Suggested fix |
|-----------|-------------------------------|-------|-----------------|---------------|
| C04 coverage subprocess plumbing | Guardian re-executes the instrumented test binary; its `GOCOVERDIR` warning lands in the stderr being asserted | `internal/run/process.go`; `internal/run/process_test.go`; `internal/run/host_test.go` | Coverage cannot serve as a passing whole-suite signal | Planned repair (see C04) |
| C05 native/tooling CI gaps | Workflow invokes only `mise run lint` and `mise run test` | `.github/workflows/ci.yml`; `mise.toml` | Contributors rely on local Swift/script checks | Planned repair (see C05) |
| C08 large daemon file | `internal/daemon/server.go` is 1,070 lines covering persistence, recovery, HTTP, scheduling, execution, and streams | `internal/daemon/server.go` | Changes span several lifecycle responsibilities. Size and churn are review signals, not a demonstrated defect | Split only with a concrete reason; keep persistence/order/cleanup tests as guards |
| C09 no schema migration layer | Schema uses `CREATE TABLE IF NOT EXISTS`. The `jobs` table at `ec3b8e2` matches the current one, so no incompatible producer exists today | `internal/daemon/server.go`, `open` | A future incompatible schema change would need a migration | Add migrations in the change that alters the schema |

A literal search for TODO, FIXME, and HACK in `cmd`, `internal`, `helper/Sources`, and `helper/Tests` found none. The optional real-image smoke test is a deliberate runtime skip, not a production TODO (`internal/image/smoke_test.go`).

### 3) Security Concerns

| Risk | OWASP category | Evidence | Current mitigation | Gap |
|------|----------------|----------|--------------------|-----|
| C02 environment retention after completion | N/A: local data minimization | `internal/daemon/server.go`; `DAEMON.md` | Owner-only DB/state; guest receives only four task variables | Confirmed exposure, not a breached guarantee: same-user processes and backups can read old secrets. Planned repair scrubs terminal records; it will not erase older SQLite pages or existing backups |
| C10 guest identity not verified cryptographically | N/A: local SSH trust | `internal/guest/ssh.go`, `Credentials.dialConfig`; `internal/run/transport.go`, `waitGuest` | Guest IP obtained from Tart and parsed as an IP; guests are ephemeral on Tart's private network | Genuine tradeoff: `InsecureIgnoreHostKey` accepts any host key, and default credentials are `admin`/`admin`. No boundary violation within the trusted Tart network was shown |
| C11 project commands run on the host | N/A: intended command execution | `internal/run/run.go`, `Execute`; `internal/project/project.go`; `docs/config.md` (`[prepare] run`) | Strict contract keys; documented host execution | By design, `[prepare] run` runs on the real Mac even for VM jobs. Projects are trusted with host authority; the VM does not sandbox preparation |
| C12 same-user socket authority | N/A: local application trust | `internal/daemon/server.go`, `Serve` (socket mode 0600); `internal/daemon/host.go`, `handleHostSnapshot` | Owner-only socket; fresh-signal and eligibility gates | By design, any same-user process can submit jobs or helper snapshots. No helper identity authentication is promised, so impersonation within that boundary is not a violation |
| C07 distribution trust | N/A: distribution trust | `.goreleaser.yaml`; `README.md`; `CONTRIBUTING.md` | Limits disclosed in README; installer verifies its ad-hoc bundle | Release binaries are unsigned/unnotarized and the unpublished cask removes quarantine. Publishing and Apple signing are a maintainer decision outside current scope |

These are explicit trust boundaries, not a remotely exploitable service. No security audit, credential rotation procedure, or signed-distribution verification was performed for this map.

### 4) Performance and Scaling Concerns

| Concern | Evidence | Current symptom | Scaling risk | Suggested improvement |
|---------|----------|-----------------|--------------|-----------------------|
| C13 history/log/artifact accumulation | `internal/daemon/server.go`; `internal/daemon/protocol.go` | Terminal records and job directories are never pruned. Per-project caches persist deliberately | Disk use, startup load, and `status` output grow with history | Planned repair: completed-job expiry (caches and golden images excluded) |
| C14 lane-head blocking | `internal/daemon/server.go`, `admit` | A blocked head holds later jobs in its lane; the other lane and queue deadlines continue | Unrelated projects wait behind an exclusive or retained job | Intentional FIFO ordering; any bypass would be a new policy decision |
| C15 bounded VM capacity | `internal/daemon/server.go`, `admit`; `internal/run/locks.go`, `acquire` | At most two VMs; low or unknown memory reduces to one | Burst demand queues | Intentional: Apple's two-guest licensing limit and conservative memory admission. Do not raise |
| C06 retained VM holds a slot | `internal/daemon/server.go`, `execute` / `admit` / `cleanupRetained`; `internal/daemon/retained_test.go` | A failed `--keep` job keeps its VM slot and exclusive lease until cancel or startup sweep | One or two kept jobs can block the VM lane | Intentional inspection contract; `status` names the job to cancel |
| C16 full worktree transfer | `internal/run/transport.go`, `sendWorktree` | Only `.git` and `.build` are excluded from the tar | Large generated trees add transfer work | Unclear: the copy mechanism is confirmed, but no representative slowdown or dispensable input has been measured. Defer until measured |
| C17 shared startup sweep depends on Tart | `internal/daemon/server.go`, `open` / `sweep`; `cmd/offscreend/main.go`; `README.md` prerequisites | A Tart list failure fails daemon startup, so the host lane is also unavailable | Host-only work cannot start without Tart | Intentional: Tart is a documented prerequisite and owned-VM recovery runs before either lane. Host-only startup is not promised |

No load/latency benchmark or disk-growth baseline was run. These are code-backed mechanisms, not measured bottlenecks. No Benchmark functions exist in the inspected source.

### 5) Fragile/High-Churn Areas

Counts are file appearances in `git log --since="90 days ago" --format= --name-only` at this checkout, not a claim of defect frequency. Generated output was excluded from source analysis.

| Area | Complexity signal | Churn signal | Safe change strategy |
|------|-------------------|--------------|----------------------|
| `cmd/offscreen/main.go` / `main_test.go` | Parser, streams, cancellation and exit semantics | 5 appearances each | Preserve argv/stdout/stderr/exit-code behavioral tests |
| `internal/run/run.go` | Preparation, clone lifecycle, guest transport, artifact recovery | 5 appearances | Test cancellation and separate cleanup budgets |
| `internal/image/build.go` | 278-line image orchestration plus embedded display/automation scripts | 4 appearances | Unit script/display checks plus opt-in real-image smoke |
| Helper tests / sensors | Hardware API aggregation and candidate filter verification | Tests 4; `Sensors.swift` 3 | Pure predicate tests plus human hardware checks on the target Mac |
| `internal/daemon/server.go` (C08) | 1,070 lines across state, queue, RPC and cleanup | 3 appearances | Preserve restart, persistence-failure, retained-slot, and stream-tail tests |
| `go.mod` / `go.sum` | Shared dependency/toolchain graph | 5 / 4 appearances | Re-run Go checks and verify target toolchain |

Other measured sizes: `internal/daemon/install.go` 530 lines, `HelperApp.swift` 356, and `guardian_darwin.go` 347 (`wc -l`). Size and churn flag review attention; neither proves fragility.

### 6) `[ASK USER]` Questions

1. Resolved (C01): physical-versus-injected input was compared on the maintainer's Mac during host-lane acceptance (see C01). Each new installation still chooses its own input mode through the helper probe (`helper/README.md`).
2. Resolved (C13, C02): the maintainer chose automatic expiry of completed jobs with a seven-day default, and scrubbing of terminal caller environments. Per-project caches and golden images stay outside job expiry. These are planned repairs, not yet shipped behavior.
3. [ASK USER] (C07) Is the next distribution target source/ad-hoc installs, or a published cask with Apple-issued signing and notarization for the daemon and helper? Current packaging and public instructions leave this to the maintainer (`README.md`; `.goreleaser.yaml`; `CONTRIBUTING.md`).

### 7) Evidence

- `README.md`
- `CONTRIBUTING.md`
- `DAEMON.md`
- `docs/config.md`
- `helper/README.md`
- `helper/Sources/OffscreenHelper/InputFilter.swift`
- `helper/Sources/OffscreenHelper/HelperApp.swift`
- `cmd/offscreen/main.go`
- `cmd/offscreend/main.go`
- `internal/daemon/server.go`
- `internal/daemon/host.go`
- `internal/daemon/protocol.go`
- `internal/daemon/compat_test.go`
- `internal/daemon/retained_test.go`
- `internal/daemon/host_cleanup_failure_test.go`
- `internal/run/process.go`
- `internal/run/guardian_darwin.go`
- `internal/run/process_test.go`
- `internal/run/host_test.go`
- `internal/run/transport.go`
- `internal/run/locks.go`
- `internal/guest/ssh.go`
- `.github/workflows/ci.yml`
- `.goreleaser.yaml`

Terminal evidence: `go test ./...` passed; Swift's seven tests passed. The C04 command below failed both named assertions with `warning: GOCOVERDIR not set, no coverage data emitted` in the actual stderr; the same tests passed without `-cover`. File churn and line counts were collected on 2026-10-12. See [TESTING.md](TESTING.md) for detailed limits.

## Concern Ledger

Each concern was argued for and against in separate reviews, then ruled on by a third reviewer, at `ea6387d`. A verdict of "refuted" rejects the defect framing; it does not remove the underlying tradeoff, which the disposition column records.

| ID | Verdict | Disposition | Decisive evidence |
|----|---------|-------------|-------------------|
| C01 | Refuted | Docs correction | Blanket "unverified / no usable return" framing is contradicted by recorded acceptance on real hardware and by manual controls that are always shown. `InputFilter.candidatePhysical` returns nothing unless `VerifiedInputFilter` is on and `ManualReturn` is off, but whatever those preferences say, the menu offers **I'm back** for a running job, which starts the grace period and then offers **Take back now** (`HelperApp.swift`, `OffscreenHelperApp.body`). Explicit selection of an input mode is not enforced. See the acceptance record below |
| C02 | Confirmed (static) | Planned repair: terminal snapshot scrubbing | `hostEnvironment` captures `os.Environ()` into `Request.HostEnv`; `save` serializes the full request on every transition, including terminal ones. `TestSubmissionUsesEachClientsHostEnvironment` (`internal/daemon/compat_test.go`) covers the per-client snapshot |
| C03 | Refuted | Intentional safety boundary | `ErrCleanupUnconfirmed` and `WaitForHostCleanup` stop admission until cleanup is proven; leftover markers deliberately block restart. Recovery is manual by design (`DAEMON.md`) |
| C04 | Confirmed (reproduced) | Planned repair: coverage plumbing | `go test ./internal/run -run '^(TestGuardianOutputExitAndFastLeader\|TestExecuteHostPrepareEnvironmentArgvAndExit)$' -cover -count=1` fails at `ea6387d`; without `-cover` it passes |
| C05 | Confirmed (static) | Planned repair: CI gates | `.github/workflows/ci.yml` runs only `mise run lint` and `mise run test`. Swift build/test and ShellCheck can run on hosted macOS; Tart and human checks cannot |
| C06 | Refuted | Intentional retention constraint | An opted-in `--keep` must hold VM/exclusive capacity until cleanup or startup sweep so the clone can be inspected (`execute`, `admit`, `cleanupRetained`; `retained_test.go`) |
| C07 | Refuted | Maintainer release decision | Publishing and Apple signing were outside the delivered scope; `README.md` discloses the unpublished cask, unsigned binaries, and quarantine removal |
| C08 | Refuted | Maintenance guidance | File size and three commits of history are review signals, not a concrete defect |
| C09 | Refuted | Requirement for future schema changes | The `jobs` schema at `ec3b8e2` is identical to the current one; no incompatible producer exists |
| C10 | Refuted | Document trust model | No boundary violation inside the ephemeral Tart network was shown. Missing host-key checks and default credentials remain real tradeoffs (security table) |
| C11 | Refuted | Document trusted-project authority | Host preparation is the documented execution contract (`docs/config.md`), not a VM sandbox escape |
| C12 | Refuted | Document same-user authority | The 0600 socket intentionally trusts same-user clients; no helper identity boundary is promised |
| C13 | Confirmed (static) | Planned repair: completed-job expiry | No delete/expiry path exists in `internal/daemon/server.go`; `sweep` loads every row at startup. Persistent project caches are deliberate. No bottleneck measured |
| C14 | Refuted | Intentional scheduling tradeoff | Lane-head waiting implements the required FIFO order; the other lane and queue deadlines continue (`admit`) |
| C15 | Refuted | Intentional resource limit | The two-guest cap (`locks.go`, `acquire`; `admit`) follows Apple's macOS virtualization licence; memory admission is conservative by design |
| C16 | Unclear | Defer until measured | The copy mechanism is confirmed (`sendWorktree`); no representative slowdown or dispensable input has been established |
| C17 | Refuted | Document shared availability dependency | Tart is a documented prerequisite (`README.md`), and owned-VM recovery (`sweep`) precedes both lanes. Independent host-only startup is not promised |

### C01 hardware acceptance record

During host-lane acceptance up to commit `c037c4c`, a person tested the installed, signed helper on one Apple silicon Mac with Input Monitoring granted. Physical mouse, key, click, and scroll events arrived with source PID 0 and source state 1. A synthetic mouse move posted by a separate injector arrived with that injector's nonzero PID and source state 0. `VerifiedInputFilter` was enabled only after this comparison. The same session also exercised the idle wait, the lock gate, display sleep (while locked), camera and microphone gates, the ten-second countdown with **Not now**, host and VM jobs running together, and the menu's **Take back now**, which requeued the job at the front of the host queue. Commits `8be6c0d`, `3da456f`, and `c037c4c` fixed issues found during that session.

Limits: this proves only that installation and injector at that time. It does not prove the preferences or grants on any current installation, other hardware or injection tools, a display-asleep state without a lock, or the notification's action button (the menu control was used instead).

## Intent vs. Reality

The public README's main intent—disposable VM GUI work plus a hardware-oriented host lane—is represented by both implemented runners. Three limits qualify that intent:

- Host operation has admission gates and a manual return control that does not depend on input-mode preferences. Automatic return detection is off until each installation runs the probe and enables it (`README.md`; `helper/README.md`; `helper/Sources/OffscreenHelper/InputFilter.swift`). It worked on the maintainer's Mac during acceptance (C01); no universal hardware guarantee is claimed.
- Contributor instructions include Swift/script and local GUI verification, while CI runs only Go lint/tests (C05; `CONTRIBUTING.md`; `.github/workflows/ci.yml`).
- Cask/release configuration exists, while the README explicitly says the cask is not published and separates trusted signing/notarization work (C07; `README.md`; `.goreleaser.yaml`). Configuration is not evidence of a released distribution.

Private design notes were read for intent comparison but are not reproduced here, in accordance with `AGENTS.md`.
