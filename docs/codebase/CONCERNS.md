# Codebase Concerns

Snapshot: concerns validated at checkout `ea6387d` (2026-10-12), then reconciled with the repairs through `4e31a32`. Each concern has a stable ID (C01–C17, defined in the [concern ledger](#concern-ledger) below). The ledger separates four kinds of finding: reproduced defects, confirmed operational gaps, intentional constraints or trust tradeoffs, and unmeasured performance signals. C04 was the only reproduced defect; it and the three confirmed gaps (C02, C05, C13) are now fixed, with the residual limits listed below. Twelve concerns were refuted as defects and are recorded as intentional safeguards, trust assumptions, or maintenance guidance. C16 stays unresolved until measured. No concern is a demonstrated breach of a promised security boundary.

## Core Sections (Required)

### 1) Top Risks (Prioritized)

| Severity | Concern | Evidence | Impact | Suggested action |
|----------|---------|----------|--------|------------------|
| Low (operational) | C03: Unconfirmed guardian cleanup blocks admission until a person resolves it | `internal/run/process.go`, `ErrCleanupUnconfirmed`; `internal/run/guardian_darwin.go`, `WaitForHostCleanup`; `internal/daemon/host_cleanup_failure_test.go` | Service is unavailable until leftover markers are inspected. This is the intended fail-closed barrier against reusing a desktop with a possibly live task | Keep; preserve the manual recovery instructions in `DAEMON.md` |
| Low (residual of C02) | Logical scrubbing does not erase old copies of caller environments | `internal/daemon/server.go`, `save` / `transition` / `sweep`; `DAEMON.md` | Active jobs still hold the full caller environment in `jobs.sqlite`. After a job ends, SQLite free pages, the journal, and existing backups can still hold the old snapshot until overwritten | Protect the state directory and its backups (`DAEMON.md`). Secure erasure is not promised |
| Low (residual of C13) | Expiry bounds only completed, cleaned-up history | `internal/daemon/prune.go`, `Prune`; `internal/daemon/expiry.go`; `internal/daemon/server.go`, `sweep` | Queued/active jobs, retained `--keep` clones, and jobs with an unconfirmed VM clone are kept whatever their age. If Tart cannot be listed, a pass that has VM candidates deletes nothing. A long-stopped daemon loads all expired rows into memory once at its next startup before the first pass removes them. The database file keeps its high-water size | Cancel retained jobs to release them; the next hourly pass retries failures |
| Low (residual of C05/C04) | Two CI properties are not yet observed or gated | `.github/workflows/ci.yml`; `internal/run/process.go`, `MaybeGuardian` | The helper job's `DEVELOPER_DIR` (`Xcode_26.3` on `macos-15`) has not been seen on a hosted run. CI runs `go test ./...` without `-cover`, so a regression of the C04 fix would only show in a local coverage run | Confirm the first hosted run; optionally add a coverage run of `./internal/run` to CI |

All four planned repairs have shipped (see [Repaired concerns](#repaired-concerns)). C06, C14, C15, and C17 also delay or block work. They are intentional constraints, not defects; see the ledger.

### 2) Technical Debt

| Debt item | Why it exists / evidence limit | Where | Risk if ignored | Suggested fix |
|-----------|-------------------------------|-------|-----------------|---------------|
| C08 large daemon file | `internal/daemon/server.go` is 1,120 lines covering persistence, recovery, HTTP, scheduling, execution, and streams. The prune engine (`prune.go`) and expiry scheduler (`expiry.go`) live in separate files | `internal/daemon/server.go` | Changes span several lifecycle responsibilities. Size and churn are review signals, not a demonstrated defect | Split only with a concrete reason; keep persistence/order/cleanup/expiry tests as guards |
| C09 no schema migration layer | Schema uses `CREATE TABLE IF NOT EXISTS`. The `jobs` table at `ec3b8e2` matches the current one; the C02 and C13 repairs added no columns (`TestPruneHandlesRecordsFromEarlierDaemons` covers older rows) | `internal/daemon/server.go`, `open` | A future incompatible schema change would need a migration | Add migrations in the change that alters the schema |
| Coverage not gated in CI (residual of C04) | The C04 fix only affects coverage-instrumented guardians; CI runs plain `go test ./...` | `.github/workflows/ci.yml`; `mise.toml`, `test` | A regression would pass CI | Run `go test ./internal/run -cover` in CI, or keep running `go test ./... -cover` before commits |

A literal search for TODO, FIXME, and HACK in `cmd`, `internal`, `helper/Sources`, and `helper/Tests` found none. The optional real-image smoke test is a deliberate runtime skip, not a production TODO (`internal/image/smoke_test.go`).

### 3) Security Concerns

| Risk | OWASP category | Evidence | Current mitigation | Gap |
|------|----------------|----------|--------------------|-----|
| C02 caller environment in job state (fixed) | N/A: local data minimization | `internal/daemon/server.go`, `save` / `transition` / `sweep`; `internal/daemon/env_scrub_test.go`; `DAEMON.md` | Owner-only DB/state; guest receives only four task variables. Terminal records drop `HostEnv` in the same write that records the terminal state; startup rewrites older terminal rows before serving | Queued, active, and requeued jobs must keep the snapshot. Old SQLite pages, the journal, and backups are not erased |
| C10 guest identity not verified cryptographically | N/A: local SSH trust | `internal/guest/ssh.go`, `Credentials.dialConfig`; `internal/run/transport.go`, `waitGuest` | Guest IP obtained from Tart and parsed as an IP; guests are ephemeral on Tart's private network | Genuine tradeoff: `InsecureIgnoreHostKey` accepts any host key, and default credentials are `admin`/`admin`. No boundary violation within the trusted Tart network was shown |
| C11 project commands run on the host | N/A: intended command execution | `internal/run/run.go`, `Execute`; `internal/project/project.go`; `docs/config.md` (`[prepare] run`) | Strict contract keys; documented host execution | By design, `[prepare] run` runs on the real Mac even for VM jobs. Projects are trusted with host authority; the VM does not sandbox preparation |
| C12 same-user socket authority | N/A: local application trust | `internal/daemon/server.go`, `Serve` (socket mode 0600); `internal/daemon/host.go`, `handleHostSnapshot` | Owner-only socket; fresh-signal and eligibility gates | By design, any same-user process can submit jobs, helper snapshots, or prune requests. No helper identity authentication is promised, so impersonation within that boundary is not a violation |
| C07 distribution trust | N/A: distribution trust | `.goreleaser.yaml`; `README.md`; `CONTRIBUTING.md` | Limits disclosed in README; installer verifies its ad-hoc bundle | Release binaries are unsigned/unnotarized and the cask, once published, removes quarantine. Publishing and Apple signing are a maintainer decision outside current scope |

These are explicit trust boundaries, not a remotely exploitable service. No security audit, credential rotation procedure, or signed-distribution verification was performed for this map.

### 4) Performance and Scaling Concerns

| Concern | Evidence | Current symptom | Scaling risk | Suggested improvement |
|---------|----------|-----------------|--------------|-----------------------|
| C13 history/log/artifact accumulation (fixed) | `internal/daemon/prune.go`; `internal/daemon/expiry.go`; `internal/daemon/expiry_test.go` | Completed, cleaned-up jobs expire after `job_retention` (default `168h`) at startup and hourly. In `TestRepeatedExpiryCyclesKeepRowsAndPagesBounded`, repeated create/expire cycles return the row count to zero and the database stays at 16 pages | Protected jobs are never expired. A long-stopped daemon pays one full load of old rows at startup. The file never shrinks below its high-water size. Per-project caches persist deliberately | None required; skipping expired rows in `sweep` would duplicate the protection checks in SQL |
| C14 lane-head blocking | `internal/daemon/server.go`, `admit` | A blocked head holds later jobs in its lane; the other lane and queue deadlines continue | Unrelated projects wait behind an exclusive or retained job | Intentional FIFO ordering; any bypass would be a new policy decision |
| C15 bounded VM capacity | `internal/daemon/server.go`, `admit`; `internal/run/locks.go`, `acquire` | At most two VMs; low or unknown memory reduces to one | Burst demand queues | Intentional: Apple's two-guest licensing limit and conservative memory admission. Do not raise |
| C06 retained VM holds a slot | `internal/daemon/server.go`, `execute` / `admit` / `cleanupRetained`; `internal/daemon/retained_test.go` | A failed `--keep` job keeps its VM slot and exclusive lease until cancel or startup sweep, and expiry skips it | One or two kept jobs can block the VM lane | Intentional inspection contract; `status` names the job to cancel |
| C16 full worktree transfer | `internal/run/transport.go`, `sendWorktree` | Only `.git` and `.build` are excluded from the tar | Large generated trees add transfer work | Unresolved: the copy mechanism is confirmed, but no representative slowdown or dispensable input has been measured. Defer until measured |
| C17 shared startup sweep depends on Tart | `internal/daemon/server.go`, `open` / `sweep`; `cmd/offscreend/main.go`; `README.md` prerequisites | A Tart list failure fails daemon startup, so the host lane is also unavailable | Host-only work cannot start without Tart | Intentional: Tart is a documented prerequisite and owned-VM recovery runs before either lane. Host-only startup is not promised |

No load/latency benchmark was run. The only storage measurement is the bounded-page test above, which uses small synthetic jobs; it is not a disk-growth baseline for real artifacts. No Benchmark functions exist in the inspected source.

### 5) Fragile/High-Churn Areas

Counts are file appearances in `git log --since="90 days ago" --format= --name-only` at `ea6387d`, not a claim of defect frequency. The repair commits after it added one appearance each to `internal/daemon/server.go` (three commits) and `cmd/offscreen/main.go` (one). Generated output was excluded from source analysis.

| Area | Complexity signal | Churn signal | Safe change strategy |
|------|-------------------|--------------|----------------------|
| `cmd/offscreen/main.go` / `main_test.go` | Parser, streams, cancellation and exit semantics | 5 appearances each | Preserve argv/stdout/stderr/exit-code behavioral tests |
| `internal/run/run.go` | Preparation, clone lifecycle, guest transport, artifact recovery | 5 appearances | Test cancellation and separate cleanup budgets |
| `internal/image/build.go` | 278-line image orchestration plus embedded display/automation scripts | 4 appearances | Unit script/display checks plus opt-in real-image smoke |
| Helper tests / sensors | Hardware API aggregation and candidate filter verification | Tests 4; `Sensors.swift` 3 | Pure predicate tests plus human hardware checks on the target Mac |
| `internal/daemon/server.go` (C08) | 1,120 lines across state, queue, RPC and cleanup | 3 appearances at `ea6387d` | Preserve restart, persistence-failure, retained-slot, scrub, expiry, and stream-tail tests |
| `go.mod` / `go.sum` | Shared dependency/toolchain graph | 5 / 4 appearances | Re-run Go checks and verify target toolchain |

Other measured sizes: `internal/daemon/install.go` 530 lines, `internal/daemon/prune.go` 255, `HelperApp.swift` 356, and `guardian_darwin.go` 347 (`wc -l`). Size and churn flag review attention; neither proves fragility.

### 6) `[ASK USER]` Questions

1. Resolved (C01): physical-versus-injected input was compared on the maintainer's Mac during host-lane acceptance (see C01). Each new installation still chooses its own input mode through the helper probe (`helper/README.md`).
2. Resolved and shipped (C13, C02): the maintainer chose automatic expiry of completed jobs with a seven-day default (`job_retention`, `docs/config.md`) plus `offscreen prune`, and scrubbing of terminal caller environments. Per-project caches and golden images stay outside job expiry.
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
- `cmd/offscreen/prune_test.go`
- `cmd/offscreend/main.go`
- `internal/config/config.go`
- `internal/daemon/server.go`
- `internal/daemon/prune.go`
- `internal/daemon/expiry.go`
- `internal/daemon/host.go`
- `internal/daemon/protocol.go`
- `internal/daemon/compat_test.go`
- `internal/daemon/env_scrub_test.go`
- `internal/daemon/prune_test.go`
- `internal/daemon/expiry_test.go`
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
- `mise.toml`
- `.goreleaser.yaml`

Terminal evidence at `4e31a32` (Go 1.27.0 darwin/arm64): `go test ./...` and `go test ./... -cover -count=1` passed, including the two C04 assertions; `mise run helper:test` passed seven XCTest cases; `mise run shellcheck` passed on all eight tracked scripts; `mise run lint`, `mise run build`, and `go build ./...` passed. The pre-repair C04 failure at `ea6387d` printed `warning: GOCOVERDIR not set, no coverage data emitted` in the asserted stderr. See [TESTING.md](TESTING.md) for detailed limits.

## Concern Ledger

Each concern was argued for and against in separate reviews, then ruled on by a third reviewer, at `ea6387d`. A verdict of "refuted" rejects the defect framing; it does not remove the underlying tradeoff, which the disposition column records. The confirmed concerns were then repaired; their current status is in the disposition column and in [Repaired concerns](#repaired-concerns).

| ID | Verdict at `ea6387d` | Current disposition | Decisive evidence |
|----|---------|-------------|-------------------|
| C01 | Refuted | Docs corrected: input-mode verification is per installation | Blanket "unverified / no usable return" framing is contradicted by recorded acceptance on real hardware and by manual controls that are always shown. `InputFilter.candidatePhysical` returns nothing unless `VerifiedInputFilter` is on and `ManualReturn` is off, but whatever those preferences say, the menu offers **I'm back** for a running job, which starts the grace period and then offers **Take back now** (`HelperApp.swift`, `OffscreenHelperApp.body`). Explicit selection of an input mode is not enforced. See the acceptance record below |
| C02 | Confirmed (static) | Fixed in `a5972af` | At `ea6387d`, `save` serialized the full request (including `HostEnv` from `hostEnvironment`) on every transition, and terminal rows kept it. Now terminal saves write the request without `HostEnv`, `transition` clears it in memory only after the write succeeds, and `sweep` rewrites older terminal rows at startup (`TestTerminalJobsScrubCallerEnvironment`, `TestRestartKeepsQueuedSnapshotsAndScrubsTerminalRecords`, `TestTerminalScrubPersistenceFailureFailsClosed`, `TestKeptVMCancelWorksAfterScrub` in `internal/daemon/env_scrub_test.go`) |
| C03 | Refuted | Intentional safety boundary | `ErrCleanupUnconfirmed` and `WaitForHostCleanup` stop admission until cleanup is proven; leftover markers deliberately block restart. Recovery is manual by design (`DAEMON.md`) |
| C04 | Confirmed (reproduced) | Fixed in `b48c62b` | `go test ./internal/run -run '^(TestGuardianOutputExitAndFastLeader\|TestExecuteHostPrepareEnvironmentArgvAndExit)$' -cover -count=1` failed at `ea6387d` and passes now; without `-cover` it passed both times. `MaybeGuardian` restores the guardian's own `GOCOVERDIR` just before exit; `TestGuardianCoverageSettingStaysOutOfTask` checks the task never sees it |
| C05 | Confirmed (static) | Fixed in `9279ac2` | At `ea6387d`, `.github/workflows/ci.yml` ran only `mise run lint` and `mise run test`. It now has separate `helper` (`mise run helper:test`) and `shellcheck` (`mise run shellcheck`, ShellCheck 0.11.0) jobs. Tart and human checks stay local |
| C06 | Refuted | Intentional retention constraint | An opted-in `--keep` must hold VM/exclusive capacity until cleanup or startup sweep so the clone can be inspected (`execute`, `admit`, `cleanupRetained`; `retained_test.go`). Expiry skips retained jobs |
| C07 | Refuted | Maintainer release decision | Publishing and Apple signing were outside the delivered scope; `README.md` discloses the unpublished cask, unsigned binaries, and quarantine removal |
| C08 | Refuted | Maintenance guidance | File size and three commits of history are review signals, not a concrete defect |
| C09 | Refuted | Requirement for future schema changes | The `jobs` schema at `ec3b8e2` is identical to the current one; no incompatible producer exists |
| C10 | Refuted | Trust model documented | No boundary violation inside the ephemeral Tart network was shown. Missing host-key checks and default credentials remain real tradeoffs (security table) |
| C11 | Refuted | Trusted-project authority documented | Host preparation is the documented execution contract (`docs/config.md`), not a VM sandbox escape |
| C12 | Refuted | Same-user authority documented | The 0600 socket intentionally trusts same-user clients; no helper identity boundary is promised |
| C13 | Confirmed (static) | Fixed in `57b82a3` (engine and `prune`) and `4e31a32` (automatic expiry) | At `ea6387d` no delete or expiry path existed. Now `Server.Prune` deletes eligible completed jobs, `offscreen prune` previews or applies it, and `expire`/`maintain` run it at startup and hourly with `job_retention` (`TestPrunePreviewsThenDeletesOnlyConfirmedOldTerminalJobs`, `TestPrunePartialFailuresRetryWithoutResettingAge`, `TestStartupExpiryRemovesOldHistoryBeforeServing`, `TestPeriodicExpiryRetriesFailuresAndHonorsProtection`, `TestRepeatedExpiryCyclesKeepRowsAndPagesBounded`) |
| C14 | Refuted | Intentional scheduling tradeoff | Lane-head waiting implements the required FIFO order; the other lane and queue deadlines continue (`admit`) |
| C15 | Refuted | Intentional resource limit | The two-guest cap (`locks.go`, `acquire`; `admit`) follows Apple's macOS virtualization licence; memory admission is conservative by design |
| C16 | Unclear | Unresolved: defer until measured | The copy mechanism is confirmed (`sendWorktree`); no representative slowdown or dispensable input has been established |
| C17 | Refuted | Shared availability dependency documented | Tart is a documented prerequisite (`README.md`), and owned-VM recovery (`sweep`) precedes both lanes. Independent host-only startup is not promised |

### Repaired concerns

- **C04 (coverage).** The guardian re-executes the coverage-instrumented test binary and replaces its environment with the task's, which dropped `GOCOVERDIR`; at exit the coverage runtime wrote a warning into the task's stderr. `MaybeGuardian` (`internal/run/process.go`) now keeps the guardian's own value and restores it only after the task and cleanup finish. The exact-output assertions were not changed. Limit: a coverage binary launched without `GOCOVERDIR` still warns, and CI does not run `-cover`.
- **C05 (CI gates).** CI runs Go lint/tests, the Swift helper build and XCTest suite, and pinned ShellCheck over every tracked `*.sh`. `helper:test` does not bundle, install, or launch the helper. Limit: the `DEVELOPER_DIR` path to Xcode 26.3 on `macos-15` comes from the runner image documentation and has not been observed on a hosted run; Swift unit tests do not prove sensor, permission, or input behavior.
- **C02 (terminal environment).** Queued, preparing, running, and requeued jobs keep the caller snapshot, because host preparation, `PATH`, share expansion, and restart/requeue need it. Terminal states (done, failed, cancelled, queue timeout, lost stdin, orphaned at restart) drop it in the same SQLite write; a failed write stops admission rather than publishing completion. Executors hold their own request copy. Limit: logical removal only (see the security table).
- **C13 (expiry).** One engine serves both paths. A job is deleted only if it is terminal, completed before the cutoff, its worker has returned, it holds no retained clone or cleanup, its stored artifact path is the ID-derived job directory, and Tart lists no `job-<id>` clone. Deletion removes the job directory, then the row, then the memory entry, rechecking under the admission lock. Failures stay listed with their original completion time and are retried. `offscreen prune --older-than <age> --dry-run|--yes` requires a positive age and an explicit mode; a preview changes nothing. Automatic expiry uses `job_retention` (default `168h`, positive only, read at daemon startup), one pass after the startup sweep and before serving, then hourly on one goroutine. Limits: protected jobs are kept regardless of age; an unlistable Tart inventory blocks a pass that has VM candidates; startup loads old rows before the first pass; SQLite reuses freed pages but never shrinks the file (no routine `VACUUM`).

### C01 hardware acceptance record

During host-lane acceptance up to commit `c037c4c`, a person tested the installed, signed helper on one Apple silicon Mac with Input Monitoring granted. Physical mouse, key, click, and scroll events arrived with source PID 0 and source state 1. A synthetic mouse move posted by a separate injector arrived with that injector's nonzero PID and source state 0. `VerifiedInputFilter` was enabled only after this comparison. The same session also exercised the idle wait, the lock gate, display sleep (while locked), camera and microphone gates, the ten-second countdown with **Not now**, host and VM jobs running together, and the menu's **Take back now**, which requeued the job at the front of the host queue. Commits `8be6c0d`, `3da456f`, and `c037c4c` fixed issues found during that session.

Limits: this proves only that installation and injector at that time. It does not prove the preferences or grants on any current installation, other hardware or injection tools, a display-asleep state without a lock, or the notification's action button (the menu control was used instead). It was not repeated for the repairs above, which did not change helper or host-admission code.

## Intent vs. Reality

The public README's main intent—disposable VM GUI work plus a hardware-oriented host lane—is represented by both implemented runners. Three limits qualify that intent:

- Host operation has admission gates and a manual return control that does not depend on input-mode preferences. Automatic return detection is off until each installation runs the probe and enables it (`README.md`; `helper/README.md`; `helper/Sources/OffscreenHelper/InputFilter.swift`). It worked on the maintainer's Mac during acceptance (C01); no universal hardware guarantee is claimed.
- CI enforces Go lint/tests, the Swift helper unit suite, and ShellCheck (C05). Real Tart VMs, Hello's XCUITest, and human permission/physical-input checks remain local contributor steps (`CONTRIBUTING.md`; `.github/workflows/ci.yml`).
- Cask/release configuration exists, while the README explicitly says the cask is not published and separates trusted signing/notarization work (C07; `README.md`; `.goreleaser.yaml`). Configuration is not evidence of a released distribution.

Private design notes were read for intent comparison but are not reproduced here, in accordance with `AGENTS.md`.
