# offscreen

Run Mac GUI tests and app checks in disposable Tart VMs instead of letting agents and scripts take over your desktop. A daemon queues jobs from multiple callers. Jobs that need actual Mac hardware can use a separate, idle-gated host lane.

## Quick start (build from source)

You need an Apple silicon Mac, full Xcode selected as the developer directory, [Tart](https://tart.run/) on `PATH`, and Go 1.27 or newer. Check that `xcode-select -p` points to Xcode's `Contents/Developer` directory and that `xcodebuild -version` reports Xcode. The default guest image includes macOS and Xcode and may take time and disk space to download. You do **not** need mise or Homebrew to use offscreen.

```sh
git clone https://github.com/markphelps/offscreen.git
cd offscreen
go install ./cmd/offscreen ./cmd/offscreend
offscreen_bin="$(go env GOBIN)"
if [ -z "$offscreen_bin" ]; then offscreen_bin="$(go env GOPATH)/bin"; fi
export PATH="$offscreen_bin:$PATH"
offscreen image build
offscreen daemon install
# If macOS prompts, allow Offscreen Daemon access to the Local Network.
# Check System Settings > Privacy & Security > Local Network before VM jobs.
cd examples/hello
offscreen run hello test-ui
```

`go install` puts both executables in `$(go env GOPATH)/bin` (or `GOBIN`, if you set it). Add that directory to your shell's persistent `PATH` for later sessions. `daemon install` finds `offscreend` beside the `offscreen` executable, or on `PATH`, installs it in the signed user-owned `OffscreenDaemon.app` under the state directory, registers it with Launch Services, and starts a launchd agent. VM jobs need the daemon's Local Network permission to reach the Tart guest. If prompted, allow **Offscreen Daemon** in System Settings > Privacy & Security > Local Network before running Hello. If SSH repeatedly reports “no route to host,” check that setting and retry; the message can also indicate other routing problems. A grant is not established by the install command alone. It does not auto-start if you skip that step. To build into a local directory instead, from the repo root run `go build -o bin/offscreen ./cmd/offscreen` and `go build -o bin/offscreend ./cmd/offscreend`, then use `bin/offscreen` or add `bin` to `PATH`. Keep both binaries together before `daemon install`; reinstall the daemon after rebuilding it. `offscreen daemon uninstall` removes its LaunchAgent.

The Hello example builds its small macOS app on the host, then runs XCUITest in the guest. You can also run `offscreen run hello launch` to save a screenshot. A blocking run streams command output and ends with a job ID and artifact directory on stderr; the guest clone is normally deleted. See [the example](examples/hello/README.md) for the tasks and [the config reference](docs/config.md) for your own repo.

A Homebrew cask is configured for `markphelps/homebrew-tap`, but **has not been published**. Once a release and the tap exist, `brew install --cask markphelps/tap/offscreen` will be an alternative to building from source. The cask is for the two Go binaries only; the native host helper is built and installed separately. The release binaries are unsigned and unnotarized; the locally installed daemon app uses ad-hoc signing only. The cask removes quarantine from the two staged binaries, which bypasses Gatekeeper's verification. Install only if you trust the source; signing and notarization are preferable.

## Commands

Run from a directory under a repo containing `.offscreen.toml`, or pass `--worktree` to point at one. The `<project>` argument must match the file's `project` key.

| Command | What it does |
| --- | --- |
| `offscreen run <project> <task>` | Queue a named task, stream output, and wait for its result. |
| `offscreen run <project> -- <cmd> [args...]` | Run an argv command directly; defaults to the VM lane. |
| `offscreen status` | List queued, active, and completed jobs with lane, state, and waiting reason. |
| `offscreen logs <job>` | Follow stdout/stderr while active; return after a completed job. `-f` also follows; `--no-follow` prints a snapshot. |
| `offscreen artifacts <job>` | Print the job's artifact directory. |
| `offscreen cancel <job>` | Cancel an active/queued job and wait for cleanup; also delete a retained `--keep` clone without changing its recorded result. |
| `offscreen prune --older-than 720h --dry-run` / `--yes` | Preview, or delete, completed jobs (record, logs and artifacts) that finished longer ago than the given age. Queued, active and retained jobs are skipped. |
| `offscreen pause` / `offscreen resume` | Pause/release the host lane; the manual pause survives daemon restarts. Resume also clears a **Not now** delay. |
| `offscreen image build` / `offscreen image refresh` | Build a golden image; refresh pulls the base image again before rebuilding. |
| `offscreen daemon install` / `offscreen daemon uninstall` | Register/start or stop/remove the user's daemon LaunchAgent. |

`run` accepts `--lane vm|host|auto` (default `auto`: named task's lane, direct command uses VM), `--timeout 1m` (limits waiting and running; default none), `--worktree /path/to/worktree`, `--detach` (print ID and return; stdin is closed), and `--keep` (retain a failed VM clone for inspection). A kept clone **holds its VM slot and any exclusive-project lease** until `offscreen cancel <job>` deletes it. Run `offscreen --help` or `offscreen run --help` for flag syntax. `--log-level` or `OFFSCREEN_LOG_LEVEL` selects debug, info, warn, or error logs on stderr.

| Exit status | Meaning |
| --- | --- |
| Wrapped command's code | The command ran, including a nonzero test failure. |
| `75` | Queue deadline expired before execution. |
| `70` | Runner, daemon, VM, preparation, or cleanup failed (also a lost attached stdin after daemon restart). |
| `1` | CLI usage, config, or other client-side error. |

## Lanes and safety

The VM lane clones the current golden image for each job, copies the worktree and selected prepared files to the guest, and collects `$OFFSCREEN_OUT`. It runs headless and needs no host helper. By default, up to two VM jobs can run at once, reduced to one when memory is low. Apple's macOS virtualization license limits a host to **two macOS guests**; do not raise `vm_slots` beyond that. Use `exclusive = true` in a project contract to serialize its jobs. On daemon restart, orphaned running jobs fail and owned stale VMs are cleaned up. Queued attached runs cannot replay stdin after restart; detach jobs when retries must be safe.

The host lane runs one task at a time on your real desktop, only after the configured idle threshold and checks for an unlocked, awake session, idle camera and microphone, and an unpaused lane. It requires the separately installed [menu bar helper](helper/README.md) with Input Monitoring and Notifications. It does **not** require Accessibility permission for the helper (an external input-injection tool may require its own). An automatic, nonactivating ten-second countdown offers **Not now** (30-minute durable delay). If the user returns during a job, a notification offers **Take back now**; the default grace period is 60 seconds. Take back cancels the process group and requeues the job at the front only after cleanup. A same-executable guardian sends TERM/KILL and reaps background descendants on normal exit, cancellation, or daemon death. A retried job has closed stdin; use detached host jobs when interruption is possible.

**Automatic physical-input return detection is off by default.** The helper's `pid == 0` event filter is a candidate until a physical-versus-injected probe confirms it on *your* Mac. Follow [the probe and manual-return instructions](helper/README.md) before trusting the host lane with hardware-sensitive work. The helper's permission request or successful build alone is not proof. If a host guardian crashes, a `host-group-*.json` marker blocks daemon restart. Inspect and resolve the process group yourself before removing that marker; offscreen does not kill a PID just because it appears in an old marker. The VM lane is independent of this helper.

See [daemon operation](DAEMON.md) for state isolation, restart behavior, and cleanup details. See [contributing](CONTRIBUTING.md) for developer checks and local end-to-end testing.
