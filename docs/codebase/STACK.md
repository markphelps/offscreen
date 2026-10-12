# Technology Stack

Snapshot: checkout `ea6387d` (2026-10-12), reconciled with the concern repairs through `4e31a32`. Paths below are relative to the repository root.

## Core Sections (Required)

### 1) Runtime Summary

| Area | Value | Evidence |
|------|-------|----------|
| Primary language | Go, module language version 1.27; local compiler reports go1.27.0 darwin/arm64 | `go.mod`; `go version` |
| Native helper | Swift tools 6.2; minimum package platform macOS 14 | `helper/Package.swift` |
| Package managers | Go modules and Swift Package Manager | `go.mod`; `helper/Package.swift` |
| Build systems | Two Go commands, one SwiftPM executable/test target, one Xcode example project | `cmd/offscreen/main.go`; `cmd/offscreend/main.go`; `helper/Package.swift`; `examples/hello/Hello.xcodeproj/project.pbxproj` |
| Supported release target | macOS on Apple silicon, darwin/arm64, CGO disabled | `.goreleaser.yaml`; `README.md` |

### 2) Production Frameworks and Dependencies

All direct Go requirements are listed; Testify is used by tests rather than runtime code. Versions come from `go.mod`.

| Dependency | Version | Role | Evidence |
|------------|---------|------|----------|
| github.com/alecthomas/kong | 1.14.0 | CLI command/flag parsing | `cmd/offscreen/main.go` |
| github.com/BurntSushi/toml | 1.5.0 | User config and strict project contract decoding | `internal/config/config.go`; `internal/project/project.go` |
| github.com/pelletier/go-toml/v2 | 2.4.3 | Syntax ranges for preserving unrelated config when updating current_image | `internal/config/current_image.go` |
| golang.org/x/crypto | 0.58.0 | Guest SSH authentication, sessions, streams | `internal/guest/ssh.go`; `internal/guest/stream.go` |
| golang.org/x/sys | 0.49.0 | Terminal detection and Darwin process handling | `cmd/offscreen/main.go`; `internal/run/guardian_darwin.go` |
| modernc.org/sqlite | 1.60.1 | Local daemon persistence via database/sql. Default `auto_vacuum=NONE`: expired rows return pages to the freelist for reuse and the file keeps its high-water size; nothing runs `VACUUM` | `internal/daemon/server.go`; `internal/daemon/expiry_test.go` |
| github.com/stretchr/testify | 1.12.1 | Development: test assertions | `internal/project/project_test.go` |

Indirect requirements in `go.mod`: go-humanize 1.0.1, uuid 1.6.0, go-isatty 0.0.24, go-strftime 1.0.0, bigfft 0.0.0-20230129092748-24d4a6f8daec, yaml/v3 3.0.5, libc 1.77.1, mathutil 1.7.1, memory 1.12.1. Their declaration does not imply a direct application integration. The helper declares no third-party package dependencies (`helper/Package.swift`).

### 3) Development Toolchain

| Tool | Purpose | Evidence |
|------|---------|----------|
| Go 1.27.0 | Compile/test client and daemon | `mise.toml` |
| golangci-lint 2.13.2 | Lint, format, modernize | `mise.toml`; `.golangci.yml` |
| shellcheck 0.11.0 | Shell script checks | `mise.toml`; `CONTRIBUTING.md` |
| GoReleaser 2.17.1 | Archives, checksums, cask generation | `mise.toml`; `.goreleaser.yaml` |
| Xcode / Swift tools | Helper and Hello example; Swift 6.2 or newer for the helper | `CONTRIBUTING.md`; `helper/Package.swift`; `.github/workflows/ci.yml` |
| VS Code Swift launch configurations | Debug/release helper launch tasks | `.vscode/launch.json` |

### 4) Key Commands

`go install ./cmd/offscreen ./cmd/offscreend` installs the user binaries. Contributors can use:

```sh
mise install
mise run build
mise run test
mise run lint
mise run fmt
mise run go:modernize
go build ./...
go test ./... -cover
mise run helper:test   # swift build + swift test for helper/
mise run shellcheck    # pinned ShellCheck on every tracked *.sh
```

Formatting and modernize tasks can edit source (`CONTRIBUTING.md`). CI on `macos-15` runs lint, tests, `helper:test` (with Xcode 26.3 selected through `DEVELOPER_DIR`), and `shellcheck`; it does not run coverage (`.github/workflows/ci.yml`). Real VM checks are described in [TESTING.md](TESTING.md).

### 5) Environment and Config

- Project contract: `.offscreen.toml`, discovered upward from the working directory or loaded at the exact `--worktree` directory (`internal/project/project.go`).
- Optional user config: `~/Library/Application Support/offscreen/config.toml`; `OFFSCREEN_CONFIG` overrides it. Defaults include five-minute idle, one-minute grace, two VM slots, 8 GiB low-memory threshold, seven-day `job_retention` (`168h`; must be positive, read at daemon startup), and `1440x900` display points (`internal/config/config.go`; `docs/config.md`).
- No environment variable is required for default config lookup. `OFFSCREEN_STATE_DIR` isolates state/cache paths; `OFFSCREEN_LOG_LEVEL` controls Go stderr logs. Tasks receive `OFFSCREEN_OUT`, `OFFSCREEN_CACHE`, `OFFSCREEN_JOB`, and `OFFSCREEN_LANE` (`docs/config.md`; `cmd/offscreen/main.go`; `internal/project/project.go`).
- Runtime prerequisites are an Apple silicon Mac, full Xcode, Tart on PATH, an existing golden image for VM jobs, and an installed daemon. Host jobs additionally require the helper and its permission/input-mode configuration (`README.md`; `helper/README.md`).
- The default base is the Tart OCI image `ghcr.io/cirruslabs/macos-tahoe-xcode:latest`; it is a macOS VM image. The build applies display/GUI/automation checks and optional user setup scripts (`internal/config/config.go`; `internal/image/build.go`).

### 6) Evidence

- `go.mod`
- `helper/Package.swift`
- `mise.toml`
- `.golangci.yml`
- `.goreleaser.yaml`
- `.github/workflows/ci.yml`
- `internal/config/config.go`
- `internal/project/project.go`
- `README.md`

Terminal evidence: `go version` reported Go 1.27.0 on darwin/arm64.
