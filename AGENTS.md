# Offscreen

Offscreen queues GUI and UI-test jobs from agents and scripts, then runs them in disposable Tart VMs or on the real desktop when the user is away. Go 1.27 module `github.com/markphelps/offscreen`, macOS-only.

## Tooling

- Contributor tools are pinned in `mise.toml`. Users build with plain Go, so don't make mise a build requirement. Put personal settings in the ignored `mise.local.toml`.
- Before committing, run `mise run fmt` (it edits files, so check the diff) and then `mise run check` (lint, test, build).
- Name tasks `<verb>` or `<verb>:<part>`, where the part is `go`, `app` or `sh`. A bare verb runs all its parts.
- Go tests need a Mac because `internal/run`'s guardian is darwin-only. Lint works anywhere with `GOOS=darwin`.
- `mise run test:app` needs Swift 6.2+.

## Map

- `cmd/offscreen`: Kong CLI. Each command is a struct with a `Run` method.
- `cmd/offscreend` + `internal/daemon`: daemon with SQLite state, the scheduler, and an HTTP/JSON API on an owner-only Unix socket.
- `internal/run`: VM and host execution, the host guardian, artifacts. `internal/tart` and `internal/guest` wrap the Tart CLI and SSH.
- `internal/image`: golden image build and prune. Runs in the CLI, not the daemon.
- `app/`: Swift menu bar app, Offscreen.app (separate SwiftPM package). GoReleaser's before hook bundles it with `app/scripts/bundle.sh {{ .Version }}` and puts it in the release archive.
- `skills/offscreen/SKILL.md`: user-facing agent skill. Update it when CLI behavior or exit codes change.
- `examples/hello`: end-to-end Tart/XCUITest example.

## Rules

- Use `log/slog` text output on stderr. Stdout belongs to the wrapped command.
- Google Go style, inline error checks, three import groups (stdlib, external, local).
- Put behavioral tests next to the code (testify `require`).
- Socket API changes bump `daemon.ProtocolVersion`. New behavior must be opt-in per request and advertised in `GET /version` features, because the CLI and daemon upgrade separately. See `DAEMON.md` §Versions and upgrades.
- Shell scripts must pass shellcheck. `lint:sh` only checks tracked files, so `git add` new scripts first.

## Never

- Modify the TCC database or script a Local Network permission bypass.
- Run a GUI test on the host by accident when checking the VM path.

## Gotchas

- Rebuilding the CLI doesn't restart the running daemon. `mise run install` (`scripts/install.sh`, which installs CLI, daemon and app together) and Restart Daemon in the Offscreen menu do. The app starts the daemon at launch and stops it at quit; the daemon's LaunchAgent has no `RunAtLoad`/`KeepAlive`.
- The guardian restores `GOCOVERDIR` after the task exits so coverage warnings stay out of task output (`internal/run/process.go`). CI guards this with `go test ./internal/run -cover -count=1`.
- GitHub-hosted runners can't run Tart VMs. Run end-to-end checks locally with `examples/hello`. Isolate local runs with `OFFSCREEN_STATE_DIR` and `OFFSCREEN_CONFIG`.
- Example Xcode builds set `COMPILER_INDEX_STORE_ENABLE=NO` because VirtioFS doesn't support index-store atomic renames.
- Swift unit tests don't prove sensor, event-tap or permission behavior. Host-lane changes need a manual check on a real Mac (`app/README.md`).

## Deeper docs

- `ARCHITECTURE.md`: components, job lifecycle, admission rules, invariants.
- `DAEMON.md`: operations, protocol, upgrades.
- `CONTRIBUTING.md`: local setup, app signing, release snapshots.
- `docs/cli.md`, `docs/config.md`, `docs/lanes.md`: user-facing behavior.
