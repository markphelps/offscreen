# Codebase Structure

Snapshot: checkout `ea6387d` (2026-10-12), reconciled with the concern repairs through `4e31a32`. Paths are relative to the repository root. Generated builds are excluded from source conventions.

## Core Sections (Required)

### 1) Top-Level Map

| Path | Purpose | Evidence |
|------|---------|----------|
| `cmd/offscreen/` | Kong CLI and client-facing behavioral tests | `cmd/offscreen/main.go` |
| `cmd/offscreend/` | Daemon executable startup | `cmd/offscreend/main.go` |
| `internal/config/` | User defaults, TOML IO, syntax-preserving current image updates | `internal/config/config.go`; `internal/config/current_image.go` |
| `internal/project/` | Project contract discovery, validation, job resolution | `internal/project/project.go` |
| `internal/daemon/` | Socket protocol, durable queue, host policy, completed-job expiry engine, client, launchd installation | `internal/daemon/protocol.go`; `internal/daemon/server.go`; `internal/daemon/host.go`; `internal/daemon/prune.go`; `internal/daemon/install.go` |
| `internal/run/` | VM and host execution, transport, advisory locks, Darwin guardian | `internal/run/run.go`; `internal/run/host.go`; `internal/run/process.go`; `internal/run/guardian_darwin.go` |
| `internal/guest/` | SSH credentials, shell quoting, output streaming | `internal/guest/ssh.go`; `internal/guest/stream.go` |
| `internal/tart/` | Tart CLI adapter | `internal/tart/tart.go` |
| `internal/image/` | Golden image build and embedded guest scripts | `internal/image/build.go`; `internal/image/scripts/display.swift` |
| `internal/logging/`, `internal/exitcode/` | Shared logging construction and exit codes | `internal/logging/logging.go`; `internal/exitcode/exitcode.go` |
| `helper/` | Separate native SwiftPM menu helper, sensors, bridge, tests, install scripts | `helper/Package.swift`; `helper/README.md` |
| `examples/hello/` | Checked-in SwiftUI app/Xcode project/XCUITest and VM task contract | `examples/hello/README.md`; `examples/hello/.offscreen.toml` |
| `docs/` | Public config reference and this codebase map; private design notes are ignored | `docs/config.md`; `.gitignore`; `AGENTS.md` |
| `.github/workflows/` | CI: Go lint/tests, Swift helper build/tests, ShellCheck | `.github/workflows/ci.yml` |
| `.vscode/` | Portable Swift helper launch configurations | `.vscode/launch.json` |
| Root guides/config | User instructions, daemon operations, contributor rules, module/tool/lint/release configuration | `README.md`; `DAEMON.md`; `CONTRIBUTING.md`; `AGENTS.md`; `go.mod`; `mise.toml`; `.golangci.yml`; `.goreleaser.yaml` |

`bin/`, `dist/`, `helper/.build/`, `.worktrees/`, and `mise.local.toml` are ignored local output/settings (`.gitignore`). The scanner included Swift build artifacts and missed Go entry points; this map uses source paths rather than those scanner conclusions.

### 2) Entry Points

- `cmd/offscreen/main.go`: `main` checks internal guardian mode before `execute` parses CLI flags and calls command structs' `Run` methods (including `PruneCmd` for `offscreen prune`).
- `cmd/offscreend/main.go`: daemon binary entry; also supports internal guardian mode before normal daemon initialization.
- `helper/Sources/OffscreenHelper/HelperApp.swift`: native helper application entry; built as the executable declared by `helper/Package.swift`.
- `examples/hello/Hello/HelloApp.swift`: example application; selected by the checked-in Xcode project/shared scheme (`examples/hello/Hello.xcodeproj/xcshareddata/xcschemes/Hello.xcscheme`).
- `internal/image/scripts/display.swift`: separate guest display utility compiled by `internal/image/scripts/display.sh`, embedded in the Go image builder.
- Installation entry points: `offscreen daemon install` dispatches to `internal/daemon/install.go`; `helper/scripts/bundle.sh` and `helper/scripts/install.sh` handle the helper bundle/service (`DAEMON.md`; `helper/README.md`).

### 3) Module Boundaries

The exclusions below describe documented or observed boundaries, not an additional enforced architecture policy.

| Boundary | What belongs here | What must not be here |
|----------|-------------------|------------------------|
| `project` | Pure contract parsing/resolution and supplied path/env overlays | Execution and job creation; explicitly excluded by the package comment in `internal/project/project.go` |
| `daemon` | Scheduling, persistence, client RPC, admission policy, expiry of completed job records and their owned job directories | App-specific test logic; tasks come from project contracts (`internal/daemon/server.go`; `README.md`). Expiry never deletes per-project caches, images, VMs or paths read from the database (`internal/daemon/prune.go`) |
| `run` | Already-resolved lane execution, process/VM lifecycle, stream/artifact handling | Helper UI; scheduling is delegated to daemon when `SkipLock` is set (`internal/run/run.go`; `internal/run/host.go`) |
| `tart` / `guest` | External process and SSH adapters | Ownership-free destructive sweeps; Tart adapter explicitly requires caller-owned VM names (`internal/tart/tart.go`) |
| `helper` | Native desktop signals and host controls over socket | VM execution; VM lane is independent of helper (`helper/README.md`; `internal/run/run.go`) |
| `examples/hello` | App-specific GUI/task behavior | Private design notes in tracked examples (`AGENTS.md`) |

### 4) Naming and Organization Rules

Go directories use short lower-case capability names (`config`, `daemon`, `guest`, `run`). Go source uses lower-case names with underscores for multiword topics and `_test.go` for co-located tests; `guardian_darwin.go` selects Darwin-specific code. Swift source uses type-oriented PascalCase filenames, with tests under SwiftPM's `Tests/OffscreenHelperTests/`. Shell utilities use descriptive lower-case names (`internal/run/host_env_test.go`; `helper/Package.swift`; `helper/Sources/OffscreenHelper/InputFilter.swift`; `helper/scripts/bundle.sh`).

Go imports use the full module prefix `github.com/markphelps/offscreen/internal/...`; the contributor rule is three groups: standard library, external, local (`go.mod`; `AGENTS.md`; `cmd/offscreen/main.go`). No alias configuration is needed to resolve these imports. The Go module, SwiftPM helper package, and Xcode example each have their own build entry (`go.mod`; `helper/Package.swift`; `examples/hello/Hello.xcodeproj/project.pbxproj`).

### 5) Evidence

- `AGENTS.md`
- `.gitignore`
- `cmd/offscreen/main.go`
- `cmd/offscreend/main.go`
- `internal/project/project.go`
- `internal/run/run.go`
- `internal/tart/tart.go`
- `helper/Package.swift`
- `examples/hello/README.md`

Terminal evidence: repository file inventory from `rg --files --hidden`; scan limitations were checked against these entry files and `.gitignore`.
