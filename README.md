<h1>
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/header-dark.svg">
    <img src="docs/assets/header.svg" alt="offscreen" height="64">
  </picture>
</h1>

[![CI](https://github.com/markphelps/offscreen/actions/workflows/ci.yml/badge.svg)](https://github.com/markphelps/offscreen/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**Run macOS UI tests in throwaway VMs, so your agents stop taking over your mouse.**

When a coding agent or script runs XCUITest on your Mac, it takes over the screen: windows pop up, focus jumps, and you can't type until it finishes. Offscreen sends those jobs to a fresh macOS VM instead. It queues work from any number of agents, terminals or scripts, and gives you back logs, screenshots and `.xcresult` bundles. For the few jobs that need real hardware, it waits until you've stepped away and lets you take the desktop back with one click.

<!--
  TODO demo GIF: replace this comment with
  ![offscreen running a UI test in a VM while the desktop stays usable](docs/assets/demo.gif)

  Shot list (about 15 seconds, looping, under 5 MB):
  1. Split screen: terminal on the left, a text editor on the right with the cursor blinking.
  2. Type `offscreen run hello test-ui` in examples/hello and press Enter.
  3. While xcodebuild output streams, keep typing in the editor. Nothing steals focus.
  4. Output ends with `job <id> artifacts <dir>`.
  5. `open "$(offscreen artifacts <id>)"` shows Hello.xcresult (or run `launch` and show hello.png).
-->

## Quick start

You need an Apple silicon Mac, full Xcode, [Tart](https://tart.run/), and Go 1.27+ or [mise](https://mise.jdx.dev/).

```sh
git clone https://github.com/markphelps/offscreen.git
cd offscreen
mise run install:go        # or without mise: scripts/install.sh
                           # installs to ~/.local/bin and starts the daemon;
                           # if macOS asks, allow "Offscreen Daemon" on the Local Network
offscreen image build      # one-time: pulls a macOS + Xcode image, takes a while

cd examples/hello
offscreen run hello test-ui
```

The last command builds a small SwiftUI app on your Mac, runs its UI test inside a fresh VM, streams the output, and prints the job's artifact directory. Nothing opens on your desktop. The install also adds `ofs` as a shorter name for `offscreen`. If `offscreen` isn't found, add `~/.local/bin` to your `PATH`. Each `offscreen image build` or `image refresh` leaves the previous image in Tart; `offscreen image prune` lists them and asks before deleting. For other problems, see the [install guide](docs/install.md).

## Use it in your repo

Add a `.offscreen.toml` at the repo root:

```toml
project = "myapp"

[prepare]   # runs on your Mac before each job
run = "xcodebuild -scheme MyApp -derivedDataPath .build build"
share = [".build/Build/Products/Debug/MyApp.app"]

[tasks.ui]  # runs inside the VM
lane = "vm"
run = "sh scripts/ui-test.sh"
```

Then tell your agent to run this instead of `xcodebuild test`:

```sh
offscreen run myapp ui
offscreen run myapp -- swift test   # or any one-off command
```

`offscreen run` exits with the wrapped command's status, so agents and scripts read pass/fail the usual way. Offscreen's own failures use separate codes: 75 for a queue timeout, 70 for VM or daemon errors. Tasks write screenshots and results to `$OFFSCREEN_OUT`, and `offscreen artifacts <job>` prints where those ended up. The [config reference](docs/config.md) covers guest setup, caches and environment variables.

## How it compares

- **Running tests on your own desktop.** This works fine until a test opens a window while you're typing. Offscreen runs the same `xcodebuild` commands in a VM, so you keep working.
- **Running Tart yourself.** Tart is the VM layer offscreen uses. Offscreen adds the parts you'd otherwise script by hand: a per-job clone of a golden image, copying the worktree and build products in, collecting artifacts, cleaning up afterwards, and a single queue for every caller, capped at Apple's limit of two macOS VMs.
- **Hosted macOS CI** (GitHub Actions, Cirrus, and others). CI runs after you push. Offscreen runs locally whenever you or your agent call it, before you commit. It doesn't replace CI.

## Commands

| Command | What it does |
| --- | --- |
| `offscreen run <project> <task>` | Queue a named task, stream output, and wait for its result. |
| `offscreen run <project> -- <cmd>` | Run a one-off command in a VM. |
| `offscreen status` | List queued and active jobs; `-a` adds completed ones. |
| `offscreen logs <job>` | Follow a job's output. |
| `offscreen artifacts <job>` | Print a job's artifact directory. |
| `offscreen cancel <job>` | Cancel a job and wait for cleanup. |

Useful `run` flags: `--detach` returns right away, `--keep` keeps a failed VM for inspection, `--show` opens the VM in a window so you can watch it, and `--timeout` limits how long a job can wait and run. See the [command reference](docs/cli.md) for all commands, flags, and exit codes.

## Jobs that need real hardware

Some checks can't run in a VM. A task with `lane = "host"` runs on your actual desktop, one job at a time, and only when you're idle: the screen must be unlocked and awake, the camera and microphone off, and you away for long enough. A countdown lets you say **Not now** first. If you come back mid-job, a notification lets you take the desktop back. This lane needs a separate [menu bar helper](helper/README.md), and automatic return detection stays off until you check it on your Mac. Read [lanes and safety](docs/lanes.md) before turning it on.

## Learn more

- [Install guide](docs/install.md): requirements, permissions, and the unpublished Homebrew cask
- [Configuration](docs/config.md): project contract and user config
- [Command reference](docs/cli.md): every command, flag, and exit code
- [Lanes and safety](docs/lanes.md): how the VM and host lanes isolate work
- [Daemon operation](DAEMON.md): state, restarts, and cleanup
- [Architecture](ARCHITECTURE.md): components, job lifecycle, and invariants
- [Hello example](examples/hello/README.md): the sample app and its tasks
- [Contributing](CONTRIBUTING.md): developer checks and local end-to-end testing

## License

[MIT](LICENSE)
