# Testing Patterns

Snapshot: checkout `ea6387d` (2026-10-12). Results below describe this documentation session, not a guarantee about physical hardware behavior.

## Core Sections (Required)

### 1) Test Stack and Commands

Go tests use standard `testing` and Testify `require` 1.12.1 (`go.mod`; representative `_test.go` files). The helper is a separate SwiftPM/XCTest suite; Hello has a checked-in XCUITest target (`helper/Package.swift`; `helper/Tests/OffscreenHelperTests/HelperTests.swift`; `examples/hello/HelloUITests/HelloUITests.swift`).

```sh
# Default Go suite; also available as mise run test
go test ./...

# Focused package/suite examples
go test ./internal/project
go test ./internal/run -run 'TestExecuteHostPrepareEnvironmentArgvAndExit|TestGuardianOutputExitAndFastLeader' -count=1

# Separate native helper suite, from repository root
(cd helper && swift test)

# Coverage diagnostic: fails two guardian stderr assertions at this checkout (C04, planned repair)
go test ./... -cover

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
| VM smoke | Opt-in | Real guest GUI, Automation Mode, display/capture size, setup persistence | `TestImageSmoke` skips unless `OFFSCREEN_IMAGE_SMOKE` is set (`internal/image/smoke_test.go`) |
| GUI end-to-end | Local workflow supplied | Hello launch/screenshot and button/label XCUITest in VM | `examples/hello/README.md`; `examples/hello/HelloUITests/HelloUITests.swift` |
| Hardware/permissions | Human verification required | Physical vs injected input, permissions, display lock, microphone/camera, notifications | `helper/README.md`; `CONTRIBUTING.md`; unit tests cannot establish this. A one-time acceptance on one Mac is recorded in [CONCERNS.md](CONCERNS.md#c01-hardware-acceptance-record) |

### 4) Mocking and Isolation Strategy

Tests use temporary directories, environment overrides, and explicit caller-environment maps. Host runner tests execute real short shell processes and verify cleanup/output semantics (`internal/run/host_test.go`; `internal/run/process_test.go`). Daemon tests isolate state with real SQLite/socket fixtures, inject executor and memory functions, and use a substitute Tart executable. Host gates/execution can also be supplied explicitly. These seams exercise queue and retained-resource behavior without running app GUI tasks (`internal/daemon/server_test.go`; `internal/daemon/retained_test.go`). Guest transport tests use a loopback SSH server with an ephemeral key; quoting tests run generated commands through Bash (`internal/guest/ssh_test.go`; `internal/guest/stream_test.go`).

`TestImageSmoke` clones the explicitly selected image and cleans only that owned clone (`internal/image/smoke_test.go`). Helper tests use supplied values for sensor/filter decisions; they do not prove the input provenance filter on any particular Mac. Real-hardware evidence comes only from the human acceptance recorded under C01 (`helper/Tests/OffscreenHelperTests/HelperTests.swift`; `helper/README.md`).

An observed failure mode is coverage instrumentation on the re-executed guardian test binary: it writes `warning: GOCOVERDIR not set, no coverage data emitted` to the same stderr being asserted as task output (`internal/run/process.go`; `internal/run/process_test.go`; `internal/run/host_test.go`; terminal results below).

### 5) Coverage and Quality Signals

No percentage threshold is configured in the inspected `mise.toml` or `.github/workflows/ci.yml`. CI runs on `macos-15` for pushes and pull requests and invokes only `mise run lint` and `mise run test`; there are no explicit Swift, ShellCheck, coverage, race, or build steps (`.github/workflows/ci.yml`). Swift build/test and ShellCheck are feasible on hosted macOS and are planned CI additions (C05). Real Tart and XCUITest checks are explicitly local because GitHub-hosted macOS runners do not support Tart VMs (`CONTRIBUTING.md`).

Session verification on Go 1.27.0 darwin/arm64:

| Check | Observed result |
|-------|-----------------|
| `go test ./...` | Passed; Go reported cached package results |
| `(cd helper && swift test)` | Passed: 7 XCTest cases, zero failures |
| `go test ./... -cover` | Failed in `internal/run` at `TestExecuteHostPrepareEnvironmentArgvAndExit` and `TestGuardianOutputExitAndFastLeader`; additional coverage warnings broke exact stderr comparisons |
| Focused two tests without coverage, `-count=1` | Passed in 1.284 seconds |
| Real VM smoke/Hello/hardware probe | [TODO] Not executed in this documentation session. Earlier human host-lane acceptance up to `c037c4c` is summarized under C01 in [CONCERNS.md](CONCERNS.md) |

Partial coverage from the failing run: CLI 48.5%, config 66.4%, daemon 77.7%, guest 37.8%, image 11.0%, project 89.9%, run 45.8% (failed suite). Daemon entrypoint, logging, and Tart reported 0.0%; exitcode had no test files. These are diagnostic package percentages, not a successful whole-suite coverage baseline.

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
- `internal/image/smoke_test.go`
- `helper/Tests/OffscreenHelperTests/HelperTests.swift`
- `examples/hello/README.md`

Terminal evidence collected on 2026-10-12: the four Go/Swift commands and their outcomes recorded in section 5. Coverage failure is reproducible evidence about the instrumented test setup; it is not evidence that normal task output is corrupted in release binaries.
