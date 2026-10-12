# Coding Conventions

Snapshot: checkout `ea6387d` (2026-10-12), reconciled with the concern repairs through `4e31a32`. Normative rules come from `AGENTS.md` and `CONTRIBUTING.md`; observed patterns are identified by source paths.

## Core Sections (Required)

### 1) Naming Rules

| Item | Rule/pattern | Example | Evidence |
|------|--------------|---------|----------|
| Go files | Lower-case names; multiword topics use underscores; behavioral tests are co-located | `host_env_test.go`, `current_image.go` | `internal/run/host_env_test.go`; `internal/config/current_image.go` |
| Go functions/methods | Exported names begin uppercase; internal helpers begin lowercase | `Discover`, `Resolve`, `matchProject` | `internal/project/project.go` |
| CLI types | Command structs implement `Run`; Kong tags describe commands/arguments | `RunCmd.Run`, `LogsCmd.Run` | `AGENTS.md`; `cmd/offscreen/main.go` |
| Go types/constants | Descriptive Go names, exported types and typed lane constants | `JobSpec`, `LaneVM` | `internal/project/project.go` |
| Environment | Uppercase `OFFSCREEN_*` names for runner settings/task paths | `OFFSCREEN_OUT`, `OFFSCREEN_CONFIG` | `internal/project/project.go`; `internal/config/config.go` |
| Swift files/types | PascalCase type-oriented files; lowerCamelCase methods/properties | `InputFilter.swift` | `helper/Sources/OffscreenHelper/InputFilter.swift`; `helper/Sources/OffscreenHelper/Sensors.swift` |

Go visibility is capitalization-based; representative internal functions are not underscore-prefixed (`internal/project/project.go`). This is a Go/Swift repository, so TypeScript strictness settings do not apply (`go.mod`; `helper/Package.swift`).

### 2) Formatting and Linting

`AGENTS.md` requests Google Go style and inline error checks. `.golangci.yml` enables gofmt, goimports, and gofumpt; `mise run fmt` runs the formatter configuration. The lint task runs `golangci-lint run ./...` with a five-minute limit.

The explicitly enabled linters are asciicheck, bidichk, durationcheck, errorlint, gocritic, gosec, misspell, modernize, nilerr, staticcheck, testifylint, unconvert, and unparam. Gosec excludes G115; generated-code exclusions are lax and common false-positive presets are enabled (`.golangci.yml`). Source-level `#nosec` comments explain deliberate project-command execution and owner-selected path use (`internal/run/run.go`).

Use `mise run go:modernize` to apply modern Go idioms. Both formatting and modernization may modify files (`mise.toml`; `CONTRIBUTING.md`). Shell scripts must pass the pinned ShellCheck 0.11.0. `mise run shellcheck` checks every tracked `*.sh` file (`git ls-files -z '*.sh' | xargs -0 shellcheck`), so new scripts are covered without editing a path list (`mise.toml`).

[TODO] No dedicated Swift formatter/linter configuration was identified in the tracked configuration inventory. Swift compilation/testing is the helper check: `mise run helper:test` runs `swift build` and `swift test` with `--package-path helper`. It never bundles, installs, launches the helper or requests permissions (`helper/Package.swift`; `mise.toml`; `CONTRIBUTING.md`).

CI (`.github/workflows/ci.yml`, `macos-15`) enforces these gates in separate jobs: Go lint and tests (`mise run lint`, `mise run test`), the Swift helper (`mise run helper:test`, with `DEVELOPER_DIR` pointing at Xcode 26.3 because `Package.swift` needs Swift 6.2 and the image default is Xcode 16.4), and `mise run shellcheck`. Tart VM, Hello XCUITest and human permission/physical-input checks stay local (`CONTRIBUTING.md`).

### 3) Import and Module Conventions

The required Go grouping is standard library, external dependencies, then local imports (`AGENTS.md`). Local imports use `github.com/markphelps/offscreen/internal/...`; representative commands follow this order (`cmd/offscreen/main.go`).

Public symbols use ordinary Go exports, with comments on shared package functions and types. There is no barrel-export mechanism; source packages define their own exported API (`internal/project/project.go`; `internal/guest/ssh.go`). Import aliases are used where needed, including the SQLite driver's blank import (`internal/daemon/server.go`).

### 4) Error and Logging Conventions

- Shared code returns errors; layers add context with `fmt.Errorf` and `%w`, and callers use `errors.Is` or `errors.AsType` to identify causes (`internal/config/config.go`; `internal/run/run.go`; `cmd/offscreen/main.go`).
- Runner results contain identity, artifacts, code, error, VM name, and retention state. Guest SSH command errors preserve command exit status; infrastructure failures use 70. Queue expiry uses 75; client-side usage/config errors normally use 1 (`internal/run/run.go`; `internal/exitcode/exitcode.go`; `cmd/offscreen/main.go`).
- Cleanup uncertainty is explicit. The CLI labels an artifact line with “cleanup unconfirmed” when cancellation cannot confirm cleanup; host guardian status failures use `ErrCleanupUnconfirmed` (`cmd/offscreen/main.go`; `internal/run/process.go`).
- Go logging uses `log/slog` text output on stderr, selected by log level. Task stdout is streamed as task output; host preparation sends both streams to stderr (`AGENTS.md`; `internal/logging/logging.go`; `internal/run/run.go`; `internal/run/host.go`). Control commands such as status, artifact lookup, and detached submission intentionally print their result to stdout (`cmd/offscreen/main.go`).
- [TODO] A general logging redaction rule or centrally required context-field schema is not defined in the inspected guides/config. Full caller environments are persisted with each request while it is queued, active, or requeued; the daemon removes them from the record and memory when the job becomes terminal (C02, fixed; `internal/daemon/server.go`, `save` / `transition`). Removal is logical, so public guides still require protecting the state file, its journal, and backups (`DAEMON.md`; `docs/config.md`). Do not infer redaction from owner-only permissions.

### 5) Testing Conventions

Go tests are co-located `_test.go` files using standard `testing` plus Testify `require`; source changes should include behavioral tests (`AGENTS.md`; `CONTRIBUTING.md`; `internal/project/project_test.go`). Test inputs use temporary files/directories and explicit environment changes rather than app-specific production settings (`internal/run/host_test.go`). Guardian tests enter subprocess mode through `TestMain`, not package initialization (`internal/run/process_test.go`).

Swift tests are in `helper/Tests/OffscreenHelperTests/HelperTests.swift`; Hello UI tests are separate under `examples/hello/HelloUITests/`. No numerical coverage threshold is configured in `mise.toml` or `.github/workflows/ci.yml`, and CI does not run coverage. `go test ./... -cover` passes since the C04 repair; run it before changing guardian or subprocess code. Daemon tests use isolated SQLite/socket state and a controlled clock for expiry (`internal/daemon/expiry_test.go`), never the user's installed daemon. Coverage commands and hardware checks are documented in [TESTING.md](TESTING.md).

### 6) Evidence

- `AGENTS.md`
- `CONTRIBUTING.md`
- `.golangci.yml`
- `mise.toml`
- `.github/workflows/ci.yml`
- `cmd/offscreen/main.go`
- `internal/logging/logging.go`
- `internal/project/project.go`
- `internal/run/process.go`
- `internal/run/host_test.go`
- `internal/daemon/expiry_test.go`
- `helper/Tests/OffscreenHelperTests/HelperTests.swift`
