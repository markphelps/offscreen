# Testing Patterns

Snapshot: checkout `ea6387d` (2026-10-12), reconciled with the concern repairs through `4e31a32`. Results below describe documentation and repair sessions, not a guarantee about physical hardware behavior.

## Core Sections (Required)

### 1) Test Stack and Commands

Go tests use standard `testing` and Testify `require` 1.12.1 (`go.mod`; representative `_test.go` files). The helper is a separate SwiftPM/XCTest suite; Hello has a checked-in XCUITest target (`helper/Package.swift`; `helper/Tests/OffscreenHelperTests/HelperTests.swift`; `examples/hello/HelloUITests/HelloUITests.swift`).

```sh
# Default Go suite; also available as mise run test
go test ./...

# Focused package/suite examples
go test ./internal/project
go test ./internal/run -run 'TestExecuteHostPrepareEnvironmentArgvAndExit|TestGuardianOutputExitAndFastLeader' -count=1

# Separate native helper suite (swift build + swift test, as CI runs it)
mise run helper:test

# Pinned ShellCheck over every tracked *.sh (as CI runs it)
mise run shellcheck

# Coverage; guardian subprocess counters are merged into the run package result (C04 repaired)
go test ./... -cover
go test ./internal/run -cover -coverprofile=run.cov

# Optional real image smoke check; choose an existing image and isolated config
OFFSCREEN_IMAGE_SMOKE=<image-name> OFFSCREEN_CONFIG=<config-path> go test ./internal/image -run TestImageSmoke -count=1

# Real VM example, from examples/hello after image/daemon setup
offscreen run hello launch
offscreen run hello test-ui
```

Smoke-check placeholders must be replaced with real paths/names. `OFFSCREEN_IMAGE_SMOKE_MARKER` optionally checks for a setup-produced file (`internal/image/smoke_test.go`). Image/Hello checks boot actual VMs; follow `CONTRIBUTING.md`, `README.md`, and `examples/hello/README.md` for prerequisites. Do not run Hello's GUI tests directly on the host when validating VM isolation.

### 2) Test Layout

Go behavioral tests are co-located with source (`cmd/offscreen/main_test.go`; `internal/config/config_test.go`; `internal/daemon/server_test.go`; `internal/run/process_test.go`). Contract fixtures live under `internal/project/testdata/sample/.offscreen.toml`. Guardian subprocess entry is explicit in `TestMain`, including test crash modes (`internal/run/process_test.go`; `internal/daemon/guardian_entry_test.go`).

Swift tests live under `helper/Tests/OffscreenHelperTests/`; Hello UI tests under `examples/hello/HelloUITests/`. Hello's shared Xcode scheme includes its test target and `scripts/test-ui.sh` supplies the guest test command (`examples/hello/README.md`; `examples/hello/Hello.xcodeproj/xcshareddata/xcschemes/Hello.xcscheme`).

### 3) Test Scope Matrix

| Scope | Covered? | Typical target | Notes |
|-------|----------|----------------|-------|
| Unit/behavioral | Yes | Contract parsing, config preservation, quoting, memory parsing, helper signal/geometry logic | `internal/project/project_test.go`; `internal/config/current_image_test.go`; `internal/guest/ssh_test.go`; `internal/run/locks_test.go`; `helper/Tests/OffscreenHelperTests/HelperTests.swift` |
| Local integration | Yes | SQLite/socket queue, output streaming, guardian process lifecycle | `internal/daemon/server_test.go`; `internal/daemon/retained_test.go`; `internal/run/process_test.go` |
| Data minimization and expiry | Yes | Terminal caller-environment scrubbing (C02), prune preview/apply and retry (C13), startup/hourly expiry with a controlled clock, bounded rows/pages over repeated cycles | `internal/daemon/env_scrub_test.go`; `internal/daemon/prune_test.go`; `internal/daemon/expiry_test.go`; `cmd/offscreen/prune_test.go`; `internal/config/config_test.go` |
| VM smoke | Opt-in | Real guest GUI, Automation Mode, display/capture size, setup persistence | `TestImageSmoke` skips unless `OFFSCREEN_IMAGE_SMOKE` is set (`internal/image/smoke_test.go`) |
| GUI end-to-end | Local workflow supplied | Hello launch/screenshot and button/label XCUITest in VM | `examples/hello/README.md`; `examples/hello/HelloUITests/HelloUITests.swift` |
| Hardware/permissions | Human verification required | Physical vs injected input, permissions, display lock, microphone/camera, notifications | `helper/README.md`; `CONTRIBUTING.md`; unit tests cannot establish this. A one-time acceptance on one Mac is recorded in [CONCERNS.md](CONCERNS.md#c01-hardware-acceptance-record) |

### 4) Mocking and Isolation Strategy

Tests use temporary directories, environment overrides, and explicit caller-environment maps. Host runner tests execute real short shell processes and verify cleanup/output semantics (`internal/run/host_test.go`; `internal/run/process_test.go`). Daemon tests isolate state with real SQLite/socket fixtures, inject executor and memory functions, and use a substitute Tart executable. Host gates/execution can also be supplied explicitly. These seams exercise queue and retained-resource behavior without running app GUI tasks (`internal/daemon/server_test.go`; `internal/daemon/retained_test.go`). Scrub, prune, and expiry tests use the same isolated fixtures with sentinel caller environments, forced filesystem/database failures, a failing substitute Tart, and a `testClock` installed as the daemon clock with a shortened `expiryInterval`; they never touch the user's installed daemon or state (`internal/daemon/env_scrub_test.go`; `internal/daemon/prune_test.go`; `internal/daemon/expiry_test.go`). The CLI prune test runs against an isolated daemon socket (`cmd/offscreen/prune_test.go`). Guest transport tests use a loopback SSH server with an ephemeral key; quoting tests run generated commands through Bash (`internal/guest/ssh_test.go`; `internal/guest/stream_test.go`).

`TestImageSmoke` clones the explicitly selected image and cleans only that owned clone (`internal/image/smoke_test.go`). Helper tests use supplied values for sensor/filter decisions; they do not prove the input provenance filter on any particular Mac. Real-hardware evidence comes only from the human acceptance recorded under C01 (`helper/Tests/OffscreenHelperTests/HelperTests.swift`; `helper/README.md`).

Guardian tests re-execute the coverage-instrumented test binary, and `guardianMain` replaces that process's environment with the task's. Before the C04 repair, the guardian exited without `GOCOVERDIR`, so the coverage runtime wrote `warning: GOCOVERDIR not set, no coverage data emitted` into the task's stderr. `MaybeGuardian` now restores the guardian's own launch value of `GOCOVERDIR` after the task and cleanup finish, just before exit. The task still sees only its submitted environment, exact stdout/stderr assertions are unchanged, and guardian counters land in the directory `go test` provides (`internal/run/process.go`; `TestGuardianCoverageSettingStaysOutOfTask` in `internal/run/process_test.go`; `internal/run/host_test.go`). Limit: a coverage-instrumented binary launched without `GOCOVERDIR` still prints that warning from its guardians, because the guardian's launch environment has no directory to restore. Release builds are not coverage-instrumented.

### 5) Coverage and Quality Signals

No percentage threshold is configured in the inspected `mise.toml` or `.github/workflows/ci.yml`. CI runs for pushes and pull requests in three jobs: `go` on `macos-15` (`mise run lint`, `mise run test`, and `go test ./internal/run -cover -count=1`, which guards the C04 fix), `helper` on `macos-26` (`mise run helper:test` with that image's default Xcode), and `shellcheck` on `macos-15` (`mise run shellcheck`). This closes C05. There are no race or build steps, and whole-suite coverage remains a local check. The `macos-26` helper job has not yet been observed on a hosted run. Real Tart and XCUITest checks are explicitly local because GitHub-hosted macOS runners do not support Tart VMs (`.github/workflows/ci.yml`; `CONTRIBUTING.md`).

Verification at `4e31a32` on Go 1.27.0 darwin/arm64 (earlier results noted):

| Check | Observed result |
|-------|-----------------|
| `mise run test` (`go test ./...`) | Passed |
| `go test ./... -cover -count=1` | Passed. At `ea6387d` (before the C04 repair) it failed in `internal/run` at `TestExecuteHostPrepareEnvironmentArgvAndExit` and `TestGuardianOutputExitAndFastLeader` |
| Focused output tests (`TestGuardianOutputExitAndFastLeader`, `TestExecuteHostPrepareEnvironmentArgvAndExit`, `TestGuardianCoverageSettingStaysOutOfTask`), `-count=1` | Passed with `-cover`; also passed without it in the C04 repair session |
| `mise run helper:test` | Passed: 7 XCTest cases, zero failures |
| `mise run shellcheck` | Passed on all 8 tracked `*.sh` files |
| `mise run fmt`, `mise run go:modernize`, `mise run lint`, `mise run build`, `go build ./...` | Passed; formatter and modernizer made no edits |
| `go test ./internal/daemon -race` | Passed during the C02, C13 engine, and automatic-expiry repair sessions; not repeated at `4e31a32` |
| Real VM smoke/Hello/hardware probe | [TODO] Not executed during the repairs or this reconciliation. The repairs did not change helper, host-admission, or VM runner code. Earlier human host-lane acceptance up to `c037c4c` is summarized under C01 in [CONCERNS.md](CONCERNS.md) |

Package coverage from the passing `go test ./... -cover -count=1` run at `4e31a32`: CLI 55.0%, config 70.3%, daemon 80.8%, guest 37.8%, image 11.0%, project 89.9%, run 62.3%. The run figure includes guardian subprocess counters (`guardianMain` 78.0% in the C04 repair session's profile); the failing run at `ea6387d` reported 45.8% without them. Each figure counts only its own package's statements, so guardian code executed by daemon tests is not credited to `internal/run`. Daemon entrypoint, logging, and Tart have no tests; exitcode has no test files. These are observed values, not an enforced threshold.

[TODO] No historical flake catalog or benchmark baseline was established. Production TODO/FIXME/HACK search also found no test TODO markers in the searched Go/helper source trees; lack of markers does not imply complete coverage.

### 6) Evidence

- `go.mod`
- `mise.toml`
- `.github/workflows/ci.yml`
- `CONTRIBUTING.md`
- `internal/run/process.go`
- `internal/run/process_test.go`
- `internal/run/host_test.go`
- `internal/daemon/server_test.go`
- `internal/daemon/env_scrub_test.go`
- `internal/daemon/prune_test.go`
- `internal/daemon/expiry_test.go`
- `cmd/offscreen/prune_test.go`
- `internal/image/smoke_test.go`
- `helper/Tests/OffscreenHelperTests/HelperTests.swift`
- `examples/hello/README.md`

Terminal evidence collected on 2026-10-12: the Go/Swift/ShellCheck commands and their outcomes recorded in section 5. The pre-repair coverage failure was about the instrumented test setup; it was not evidence that normal task output is corrupted in release binaries.
