# Codebase Concerns

Snapshot: checkout `ea6387d` (2026-10-12). Priorities below are operational judgments based on cited behavior. They are not a claim that every risk is a reproduced bug. The coverage-command failure is directly observed; hardware and performance claims remain bounded by the stated evidence.

## Core Sections (Required)

### 1) Top Risks (Prioritized)

| Severity | Concern | Evidence | Impact | Suggested action |
|----------|---------|----------|--------|------------------|
| High | Automatic physical-input detection remains unverified until a human probe enables the candidate filter | `helper/README.md`; `helper/Sources/OffscreenHelper/InputFilter.swift` | Permission/tap readiness alone cannot establish reliable user-return detection; host jobs use the real desktop | Complete physical-vs-injected probe or explicitly use manual return; question 1 below |
| High | Submitted environment is durably stored | `cmd/offscreen/main.go`; `internal/daemon/protocol.go`; `internal/daemon/server.go`; `DAEMON.md` | Secrets can remain in SQLite and backups beyond task completion | Protect state/backups; decide retention requirements in question 2 |
| High | Guardian crash/uncertain cleanup blocks admission and may require manual process inspection | `internal/run/process.go`; `internal/run/guardian_darwin.go`; `internal/daemon/host_cleanup_failure_test.go`; `DAEMON.md` | Service remains unavailable until cleanup is established and leftover markers are resolved | Keep recovery instructions explicit; inspect process identity before removing markers |
| Medium | Coverage instrumentation breaks two guardian stderr assertions | `internal/run/host_test.go`; `internal/run/process_test.go`; terminal `go test ./... -cover` | Whole-suite coverage command fails despite normal test success | Make instrumented guardian test execution handle coverage output without weakening task-output assertions |
| Medium | CI omits native helper, shell, and real GUI checks | `.github/workflows/ci.yml`; `CONTRIBUTING.md` | Go CI can pass while Swift/helper/script/permission behavior is unverified | Consider Swift tests and ShellCheck in CI; retain local Tart/hardware checks |
| Medium | Failed retained VM holds capacity until explicit cancellation | `internal/daemon/server.go`; `internal/daemon/retained_test.go`; `DAEMON.md` | One/two kept jobs can block VM lane and exclusive projects | Use status reasons and cancel after inspection |
| Medium | Release packaging exists but public publication/signing remains separate work | `.goreleaser.yaml`; `README.md`; `CONTRIBUTING.md` | Cask availability and trusted app distribution are not established by build config | Resolve distribution target in question 3 |

### 2) Technical Debt

| Debt item | Why it exists / evidence limit | Where | Risk if ignored | Suggested fix |
|-----------|-------------------------------|-------|-----------------|---------------|
| Coverage subprocess plumbing | Guardian re-executes an instrumented test binary; direct stderr assertions observe Go's missing-GOCOVERDIR warning | `internal/run/process.go`; `internal/run/process_test.go`; `internal/run/host_test.go` | Coverage collection cannot serve as a passing whole-suite signal | Add a coverage-aware subprocess setup; preserve behavioral output checks |
| Large daemon implementation | 1,070 lines combine persistence, recovery, HTTP, scheduling, execution, and streams; no claim about author intent | `internal/daemon/server.go` | Changes span several lifecycle responsibilities | If splitting, keep persistence/order/cleanup tests as behavioral guards |
| No explicit schema version/migration layer in daemon | Schema is initialized with CREATE TABLE IF NOT EXISTS | `internal/daemon/server.go` | Future incompatible schema changes need a migration strategy | Define migrations when persisted schema changes are introduced |
| Native/tooling CI gaps | Current workflow invokes only Go lint/test tasks | `.github/workflows/ci.yml`; `mise.toml` | Contributors rely on local Swift/script checks | Add automated checks that can run on available macOS runners |

A literal search for TODO, FIXME, and HACK in `cmd`, `internal`, `helper/Sources`, and `helper/Tests` found none. The optional real-image smoke test is a deliberate runtime skip, not a production TODO (`internal/image/smoke_test.go`). Coverage failures and omitted hardware tests belong to testing debt, not demonstrated runtime defects.

### 3) Security Concerns

| Risk | OWASP category | Evidence | Current mitigation | Gap |
|------|----------------|----------|--------------------|-----|
| Sensitive request/environment retention | N/A: local tool data exposure | `internal/daemon/server.go`; `DAEMON.md` | Owner-only DB/state; guest receives only four task variables | No environment redaction/retention policy identified; same-user access remains possible |
| Guest identity is not verified cryptographically | N/A: local SSH trust | `internal/guest/ssh.go` | IP parsed and obtained from Tart; disposable guest/private-network assumption documented in code | `InsecureIgnoreHostKey` accepts any presented host key; default admin credentials are config defaults |
| Trusted project commands run on host | N/A: intended command execution | `internal/project/project.go`; `internal/run/run.go`; `internal/run/host.go` | Strict project keys; shell-vs-argv distinction; caller-controlled contracts | VM lane does not sandbox its host preparation command |
| Helper snapshots/actions rely on same-user socket boundary | N/A: local application trust | `internal/daemon/server.go`; `internal/daemon/host.go` | Socket mode 0600; fresh-signal/eligibility gates | Same-user clients are not authenticated as the helper and can submit observations |
| Distribution bypasses/quarantine and local signatures | N/A: distribution trust | `.goreleaser.yaml`; `README.md`; `CONTRIBUTING.md` | Source and caveats documented; installer verifies its ad-hoc bundle | Release binaries unsigned/unnotarized; cask removes binary quarantine; stable permission identity needs separate verification |

These are explicit trust boundaries and exposure risks, not a remotely exploitable web-service finding. [TODO] No security audit, credential rotation procedure, or signed-distribution verification was performed in this map.

### 4) Performance and Scaling Concerns

| Concern | Evidence | Current symptom | Scaling risk | Suggested improvement |
|---------|----------|-----------------|--------------|-----------------------|
| History/log/artifact accumulation | `internal/daemon/server.go`; `internal/daemon/protocol.go` | Terminal records are retained; no pruning/expiry path in mapped implementation | SQLite/request secrets, in-memory jobs, logs and artifact disk usage grow | Choose retention policy before adding pruning; question 2 |
| Lane-head blocking | `internal/daemon/server.go`, `admit` | Blocked head prevents later jobs in that lane; this is scheduling behavior, not measured slow performance | Unrelated projects wait behind an exclusive or unsafe/retained job | Preserve FIFO intent or explicitly decide a fair bypass policy when workload warrants it |
| Bounded VM capacity | `internal/daemon/server.go`; `internal/run/locks.go` | Maximum two; low/unknown memory reduces to one | Burst demand queues; retained clones consume the same slots | Plan throughput around this limit; do not raise macOS guest capacity |
| Full checkout/product tar transfer | `internal/run/transport.go` | Only `.git` and `.build` excluded from worktree tar | Other generated/large trees increase transfer cost | Measure representative checkouts before expanding exclusions |
| Shared startup sweep depends on Tart | `internal/daemon/server.go`, `sweep` | Tart list/cleanup errors fail daemon startup | Host lane also unavailable when VM recovery fails | Preserve cleanup safety; decide any independent recovery mode explicitly |

[TODO] No load/latency benchmark or disk-growth baseline was run. These are code-backed scaling mechanisms, not measured bottlenecks. No Benchmark functions were found by the inspected source search.

### 5) Fragile/High-Churn Areas

Counts are file appearances in `git log --since="90 days ago" --format= --name-only` at this checkout, not a claim of defect frequency. Generated output was excluded from source analysis.

| Area | Complexity signal | Churn signal | Safe change strategy |
|------|-------------------|--------------|----------------------|
| `cmd/offscreen/main.go` / `main_test.go` | Parser, streams, cancellation and exit semantics | 5 appearances each | Preserve argv/stdout/stderr/exit-code behavioral tests |
| `internal/run/run.go` | Preparation, clone lifecycle, guest transport, artifact recovery | 5 appearances | Test cancellation and separate cleanup budgets |
| `internal/image/build.go` | 278-line image orchestration plus embedded display/automation scripts | 4 appearances | Unit script/display checks plus opt-in real-image smoke |
| Helper tests / sensors | Hardware API aggregation and candidate filter verification | Tests 4; `Sensors.swift` 3 | Pure predicate tests plus human hardware probe |
| `internal/daemon/server.go` | 1,070 lines across state, queue, RPC and cleanup | 3 appearances | Preserve restart, persistence-failure, retained-slot, and stream-tail tests |
| `go.mod` / `go.sum` | Shared dependency/toolchain graph | 5 / 4 appearances | Re-run Go checks and verify target toolchain |

Other measured sizes: `internal/daemon/install.go` 530 lines, `HelperApp.swift` 356, and `guardian_darwin.go` 347 (`wc -l` terminal output). Size/churn flags review attention; neither proves fragility by itself.

### 6) `[ASK USER]` Questions

1. [ASK USER] Has the physical-versus-injected input probe been completed on the target Mac, and should host operation use verified automatic detection or the documented manual-return mode? Evidence cannot establish local probe results (`helper/README.md`).
2. [ASK USER] What retention period should apply to job history, captured host environments, logs, artifacts, and persistent caches? The current daemon retains history and has no pruning path (`internal/daemon/server.go`; `DAEMON.md`).
3. [ASK USER] Is the next distribution target source/ad-hoc installs, or a published cask with Apple-issued signing and notarization for the daemon/helper? Current packaging and public instructions leave these as maintainer work (`README.md`; `.goreleaser.yaml`; `CONTRIBUTING.md`).

### 7) Evidence

- `README.md`
- `CONTRIBUTING.md`
- `DAEMON.md`
- `helper/README.md`
- `internal/daemon/server.go`
- `internal/run/process.go`
- `internal/run/process_test.go`
- `internal/run/host_test.go`
- `internal/guest/ssh.go`
- `.github/workflows/ci.yml`
- `.goreleaser.yaml`

Terminal evidence: `go test ./...` passed; Swift's seven tests passed; coverage mode failed the two named assertions; their focused non-coverage rerun passed. File churn/line counts were collected on 2026-10-12. See [TESTING.md](TESTING.md) for detailed limits.

## Intent vs. Reality

The public README's main intent—disposable VM GUI work plus a hardware-oriented host lane—is represented by both implemented runners. Three limits qualify that intent:

- Host operation has admission gates and manual controls, but automatic return detection remains a candidate until the human probe enables it (`README.md`; `helper/README.md`; `helper/Sources/OffscreenHelper/InputFilter.swift`). This is a disclosed verification gap, not a hidden claim of complete hardware safety.
- Contributor instructions include Swift/script and local GUI verification, while CI runs only Go lint/tests (`CONTRIBUTING.md`; `.github/workflows/ci.yml`). The automation scope is narrower than the full contributor verification procedure.
- Cask/release configuration exists, while the README explicitly says the cask is not published and separates trusted signing/notarization work (`README.md`; `.goreleaser.yaml`). Configuration is not evidence of a released distribution.

Private design notes were read for intent comparison but are not reproduced here, in accordance with `AGENTS.md`.
