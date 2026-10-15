---
name: offscreen
description: Run macOS and iOS Simulator UI tests, GUI app launches, screenshots, and other screen-taking commands in throwaway VMs with the offscreen CLI instead of on the user's desktop. Use whenever a repo has an `.offscreen.toml`, before running `xcodebuild test`, XCUITest, the iOS Simulator, `open`-ing an app you built, or anything that opens windows or steals focus on a Mac; when you want to see what an app looks like; when reading offscreen job results, logs, or artifacts; or when the user asks to set up offscreen for a project.
---

# offscreen

Offscreen runs commands in a fresh macOS VM per job, so UI tests, the iOS Simulator and GUI apps don't take over the user's screen while they work. A daemon on the user's Mac queues jobs from every agent, terminal and script, and each job returns its exit status, output and artifacts.

`ofs` is a shorter alias for `offscreen`.

## Core rule

If the repo (or any parent directory) has an `.offscreen.toml`, never run these directly on the host: `xcodebuild test`, XCUITest, the iOS Simulator (`xcrun simctl boot`, `open -a Simulator`), `open`-ing an app you built, `screencapture`, or AppleScript / System Events automation. Run them through offscreen:

```sh
offscreen run <project> <task>          # a named task from .offscreen.toml
offscreen run <project> -- <cmd> [args] # a one-off command, in the VM lane
```

`<project>` is the `project` key in `.offscreen.toml`. Task names are its `[tasks.<name>]` tables. Read the file before running anything; don't guess task names.

Plain builds (`xcodebuild build`, `swift build`) don't take over the screen and can run on the host as usual.

## Seeing the app

To check what a UI looks like, run a task that launches the app and saves a screenshot to `$OFFSCREEN_OUT`, then read the PNG from `offscreen artifacts <job>`. If the project has no such task, suggest adding one (see Setting up a project) rather than opening the app on the host.

## Reading results

`offscreen run` streams the command's stdout and stderr and exits with:

| Exit status | Meaning | What to do |
| --- | --- | --- |
| The command's own code | The command ran. Nonzero means it failed, for example a test failure. | Treat it like any test run: read the output and fix the code. |
| `75` | The job waited in the queue past `--timeout`. | Other jobs hold the VM slots. Check `offscreen status`, then retry or ask the user. |
| `70` | Offscreen itself failed: daemon, VM, `[prepare]`, or cleanup. | Not a test failure. See Troubleshooting. |
| `1` | Usage or config error. | Check the project name, task name, and `.offscreen.toml`. |

Tasks write screenshots and test results to `$OFFSCREEN_OUT`. After a job finishes, find them with:

```sh
offscreen artifacts <job>   # prints the artifact directory on the host
```

Every run, passing or failing, ends with `job <id> artifacts <dir>` on stderr. `offscreen status -a` also lists job IDs. To read an `.xcresult` bundle, use `xcrun xcresulttool` on the host, for example:

```sh
xcrun xcresulttool get test-results summary --path "$(offscreen artifacts <job>)/MyApp.xcresult"
```

## Long or parallel jobs

```sh
offscreen run <project> <task> --detach   # prints the job ID and returns
offscreen status                          # queued and active jobs; -a adds finished ones
offscreen logs <job>                      # output so far; -f follows until the job ends
offscreen cancel <job>                    # cancels and waits for cleanup
```

- At most two VM jobs run at once (Apple's license limit), so a queued job may wait. Use `--timeout 30m` (or similar) when you need a bound.
- Use `--format json` with `status` for machine-readable output. Field names may change before 1.0.
- Stdout belongs to the wrapped command. Offscreen's own progress goes to stderr.

## Ask the user first

These affect the user's machine, desktop, or shared state. Don't run them without explicit permission:

- `--show`: opens the VM in a window that may take focus. That defeats the point.
- `--lane host`, or any task with `lane = "host"`: runs on the user's real desktop when they're idle.
- `--keep`: keeps a failed VM, which holds one of the two VM slots until `offscreen cancel <job>`. If you use it, cancel the job when you're done inspecting.
- `offscreen pause` / `resume`, `prune`, `image build`, `image refresh`, `image prune`, `daemon install`, `daemon uninstall`, and opening, quitting or restarting the Offscreen app (quitting cancels its jobs).
- Editing the user config at `~/Library/Application Support/offscreen/config.toml`.

## Troubleshooting

- **`offscreen: command not found`**: it isn't installed or isn't on `PATH` (Homebrew's `bin` for a cask install, `~/.local/bin` for a source install). Tell the user (`brew install --cask markphelps/tap/offscreen` installs it); don't install it yourself.
- **Exit 70 or a daemon connection error**: run `offscreen version`. If it says `daemon: not running` (the error reads `Offscreen isn't running`), ask the user to open the Offscreen app (`open -a Offscreen`). For a protocol mismatch, ask them to choose Restart Daemon in the Offscreen menu. Don't open or restart it yourself.
- **`tart isn't installed or isn't on PATH`**: VM jobs and image commands need Tart. Ask the user to install it (`brew install cirruslabs/cli/tart`). If the error came from the daemon, also ask them to run `offscreen daemon install` from a shell where `tart` is on `PATH`, because the daemon uses the `PATH` recorded at install. Don't install Tart yourself.
- **No golden image**: VM jobs need one. Ask the user to run `offscreen image build`; it downloads a large macOS + Xcode image and takes a while.
- **Failure before the command starts** (during `preparing`, `cloning` or `booting`): the run repeats the last lines of that phase, and `offscreen logs <job>` has the rest. A `[prepare]` failure is a host build error, so fix it like any build error.
- **Works on the host but not in the VM**: the VM doesn't inherit the host environment. Only the worktree and `[prepare] share` paths are copied in, and offscreen sets only the four `OFFSCREEN_*` variables. Set anything else in the task script or `[guest] setup`.

## Setting up a project

When the user asks to add offscreen to a repo, create `.offscreen.toml` at the repo root:

```toml
project = "myapp"

[prepare]   # runs on the host before each job, with the caller's environment
run = "xcodebuild -scheme MyApp -derivedDataPath .build build"
share = [".build/Build/Products/Debug/MyApp.app"]   # copied into the VM

[tasks.test-ui]   # runs inside the VM from the copied project root
lane = "vm"
run = "sh scripts/test-ui.sh"
```

A task script that runs UI tests in the VM:

```sh
#!/bin/sh
set -eu

: "${OFFSCREEN_CACHE:?OFFSCREEN_CACHE must be set by offscreen}"
: "${OFFSCREEN_OUT:?OFFSCREEN_OUT must be set by offscreen}"
mkdir -p "$OFFSCREEN_CACHE" "$OFFSCREEN_OUT"

# Index-store writes fail on the VM's shared filesystem, so turn them off.
# Derived data lives in the per-project cache, which persists across jobs.
xcodebuild \
    -scheme MyApp \
    -destination 'platform=macOS' \
    -derivedDataPath "$OFFSCREEN_CACHE/DerivedData" \
    -resultBundlePath "$OFFSCREEN_OUT/MyApp.xcresult" \
    -parallel-testing-enabled NO \
    COMPILER_INDEX_STORE_ENABLE=NO \
    test
```

For an iOS Simulator task, use `-destination "platform=iOS Simulator,name=$device"`, with `$device` set to the first available iPhone from `xcrun simctl list devices available`. Device names change between Xcode releases, so don't hard-code one. A fresh VM boots the simulator from scratch, so expect a few minutes per run.

For a launch-and-screenshot task, `open` the app shared from `[prepare]`, wait for its window with `osascript` and System Events, then `screencapture -x "$OFFSCREEN_OUT/app.png"`.

The hello example has working scripts for all three: https://github.com/markphelps/offscreen/tree/main/examples/hello/scripts

Rules for the contract:

- Every `[tasks.<name>]` needs `lane` (`vm` or `host`) and `run`. Unknown keys are rejected.
- Use `lane = "vm"` unless the user says a task needs real hardware.
- `[guest] setup` runs in each VM before the task. The default image has Xcode but no Homebrew, mise, or project packages.
- Set `exclusive = true` if the project's jobs must not overlap.
- Every task gets `OFFSCREEN_OUT`, `OFFSCREEN_JOB`, `OFFSCREEN_LANE` and `OFFSCREEN_CACHE`.

Then verify it with `offscreen run myapp test-ui`. Offer to add a line like this to the repo's `AGENTS.md` or `CLAUDE.md`:

```md
Run UI tests with `offscreen run myapp test-ui`. Never run `xcodebuild test` directly; it takes over the user's screen.
```

## Reference

- Commands, flags and exit codes: https://github.com/markphelps/offscreen/blob/main/docs/cli.md
- Project contract and user config: https://github.com/markphelps/offscreen/blob/main/docs/config.md
- Lanes and safety: https://github.com/markphelps/offscreen/blob/main/docs/lanes.md
