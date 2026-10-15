# Installing offscreen

Install with Homebrew unless you're working on Offscreen itself. [Building from source](#build-from-source) is the contributor path.

## Requirements

- An Apple silicon Mac running macOS 14 or later.
- Full Xcode selected as the developer directory, for Xcode projects such as the [Hello example](../examples/hello/README.md). Check that `xcode-select -p` points to Xcode's `Contents/Developer` directory and that `xcodebuild -version` reports Xcode. A source build also needs the Swift 6.2+ that Xcode provides for the Offscreen menu bar app; check with `swift --version`.
- [Tart](https://tart.run/) on `PATH`. The Homebrew cask installs it for you. For a source install, run `brew install openai/tools/tart`.
- For a source install only: Go 1.27 or newer.

The default guest image includes macOS and Xcode and may take time and disk space to download.

If Tart is missing, VM jobs and image commands fail with `tart isn't installed or isn't on PATH`. Install it with `brew install openai/tools/tart`. If the error came from the daemon, also run `offscreen daemon install` from a shell where `tart` is on `PATH`: the daemon finds Tart through the `PATH` recorded when it was installed.

## Install with Homebrew

```sh
brew install --cask markphelps/tap/offscreen
open -a Offscreen
```

The cask installs all three parts of Offscreen, plus Tart:

| Part | Installed as |
| --- | --- |
| `offscreen` CLI | In Homebrew's `bin` directory, plus `ofs` as a shorter name |
| `offscreend` daemon | In Homebrew's `bin` directory. The cask runs `offscreen daemon install`, which copies it into a signed `OffscreenDaemon.app` in the state directory, run by the `io.latentsignal.offscreend` LaunchAgent |
| **Offscreen** menu bar app | `/Applications/Offscreen.app` |
| Tart | The `openai/tools/tart` formula, if it isn't installed already |

Offscreen is ad-hoc signed, not signed with a Developer ID or notarized. The cask removes the quarantine attribute from `offscreen`, `offscreend` and Offscreen.app so that macOS opens them, which skips Gatekeeper's check. Install it only if you trust this repository.

Offscreen doesn't start at login until you ask it to. Turn on **Start at Login** in the Offscreen menu: Offscreen registers its own LaunchAgent (`io.latentsignal.offscreen`, inside the app) with macOS, which then starts Offscreen, and with it the daemon, at every login, and relaunches it if it crashes. **Quit Offscreen** still quits it for good. macOS lists it under System Settings > General > Login Items & Extensions; if it says the item needs approval, choose **Approve Start at Login…** in the menu. If you added Offscreen to **Open at Login** by hand, remove that entry once Start at Login is on, or a second copy starts at login and exits.

Turning Start at Login on from a copy you opened yourself switches to the copy macOS started, so the menu bar icon may blink. Turning it off from the copy started at login quits Offscreen and stops the daemon, after asking; open Offscreen again to keep using it.

### Upgrade

```sh
brew upgrade --cask offscreen
open -a Offscreen
```

An upgrade quits Offscreen and reinstalls the daemon so that it matches the new CLI; open Offscreen again afterward. If Start at Login is on, opening Offscreen also renews it: macOS won't start an upgraded app from the old registration. Because the build is ad-hoc signed, macOS may ask again for Local Network, Notifications or Input Monitoring after an upgrade. Grant them as described in [Permissions](#permissions).

### Uninstall

Turn off **Start at Login** in the Offscreen menu first, so macOS doesn't keep a login item for a deleted app. Then run:

```sh
brew uninstall --cask offscreen
```

This quits Offscreen, runs `offscreen daemon uninstall`, and removes the binaries, the `ofs` alias and the app. Your job history, config and images stay. `brew uninstall --zap --cask offscreen` also deletes `~/Library/Application Support/offscreen` (job history, artifacts, logs and the user config) and Offscreen's preferences. Neither removes Tart or your Tart images; use `tart list` and `tart delete` for those, and `brew uninstall openai/tools/tart` for Tart itself.

### Don't mix Homebrew and source installs

A source install and the cask install the same app identifier and the same daemon LaunchAgent, so having both leaves you with two copies of Offscreen and whichever CLI comes first on `PATH`. Before you switch, remove the other one: run `mise run uninstall` (or `scripts/install.sh uninstall`) in your checkout before installing the cask, and `brew uninstall --cask offscreen` before a source install. Turn off **Start at Login** before you remove the cask: its login agent has the same label as the source install's, and a leftover registration can make the source install fail to load its own. While a source install is present, the Start at Login checkbox is greyed out, because its LaunchAgent already starts Offscreen at login.

## Build from source

Install Tart first (`brew install openai/tools/tart`). You don't need mise to build.

```sh
git clone https://github.com/markphelps/offscreen.git
cd offscreen
mise run install   # or without mise: scripts/install.sh
```

One command installs all three parts of Offscreen:

| Part | Installed as |
| --- | --- |
| `offscreen` CLI | `~/.local/bin/offscreen`, plus an `ofs` symlink as a shorter name |
| `offscreend` daemon | `~/.local/bin/offscreend`, run from a signed `OffscreenDaemon.app` in the state directory by the `io.latentsignal.offscreend` LaunchAgent |
| **Offscreen** menu bar app | `~/Applications/Offscreen.app`, started at login by the `io.latentsignal.offscreen` LaunchAgent |

The script builds everything first, so a failed build leaves your current install running. It then stops any running Offscreen (including one you opened by hand), installs the daemon, and starts Offscreen. An existing `ofs` that isn't offscreen's own symlink is left alone. Set `OFFSCREEN_BIN_DIR` to use another directory for the binaries. If that directory isn't on your `PATH`, the script says so; add it to your shell profile.

`mise run uninstall` (or `scripts/install.sh uninstall`) stops Offscreen and the daemon and removes the app, both LaunchAgents, both binaries and the `ofs` symlink. Your job history, config and images stay.

Rerun the install after pulling changes. `offscreen version` shows whether the running daemon matches the CLI, and the Offscreen menu says when they differ.

### Upgrading from an older install

Older installs called the menu bar app **Offscreen Helper** (`~/Applications/OffscreenHelper.app`, identifier `dev.offscreen.helper`) and the daemon `dev.offscreen.offscreend`. `mise run install` removes the old app and both old LaunchAgents when they are offscreen's own. Because the identifiers changed, macOS treats Offscreen as a new app:

- Grant **Local Network** to Offscreen Daemon and **Input Monitoring** and **Notifications** to Offscreen again when asked.
- Stale **Offscreen Helper** rows (Input Monitoring, Notifications) can stay in System Settings > Privacy & Security and System Settings > Notifications. Offscreen never edits the permission database, so remove them yourself with the **-** button or turn them off.
- An old **Offscreen Daemon** row can also stay under Local Network. That pane has no remove button, and the old row is harmless. If VM jobs fail at guest SSH, turn every **Offscreen Daemon** switch off and on again.
- The host-lane settings `VerifiedInputFilter` and `ManualReturn` were stored under the old identifier. If you had set either, set it again under the new one after rerunning the [input probe](../app/README.md#input-discrimination-probe), for example `defaults write io.latentsignal.offscreen ManualReturn -bool YES`, then remove the old ones with `defaults delete dev.offscreen.helper`.

## Running Offscreen

Offscreen owns the daemon, the way Docker Desktop does. Opening Offscreen (at login if it's set up to start then, or with `open -a Offscreen`) starts the daemon; **Quit Offscreen** in its menu stops it. If jobs are queued or running, Quit asks "Quit and cancel N jobs?" first and cancels them cleanly. **Restart Daemon** in the menu restarts it the same way, for example after you change the [user config](config.md#user-config).

The CLI never starts Offscreen for you. When it isn't running, commands that need the daemon fail with:

```text
Offscreen isn't running. Open it with: open -a Offscreen
```

## Permissions

Offscreen asks for each permission only when it needs it:

| Permission | Granted to | When macOS asks |
| --- | --- | --- |
| Local Network | **Offscreen Daemon** | The first VM job, when the daemon connects to the Tart guest |
| Notifications | **Offscreen** | The first time Offscreen has a notification to show (a failed job, or **Take back now** on the host lane) |
| Input Monitoring | **Offscreen** | Once a host-lane job is queued. A user who only runs VM jobs is never asked. |

No Accessibility permission is needed. When macOS asks, allow **Offscreen Daemon** in System Settings > Privacy & Security > Local Network; installing alone does not establish that grant. If guest SSH repeatedly reports "no route to host," check that setting and retry; the message can also indicate other routing problems.

## Build the golden image

```sh
offscreen image build
```

This pulls the base image if needed, checks guest auto-login and GUI automation, turns off app relaunch and window restoration so every job starts on an empty desktop, and records the resulting image in your user config. `offscreen image refresh` pulls the base image again before rebuilding. See [the config reference](config.md#user-config) to change the base image or add image setup scripts. Each build or refresh leaves the previous image in Tart; `offscreen image prune` lists them and asks before deleting.

## Try the example

```sh
git clone https://github.com/markphelps/offscreen.git   # if you installed with Homebrew
cd offscreen/examples/hello
offscreen run hello test-ui
```

The Hello example builds its small macOS app on the host, then runs XCUITest in the guest. You can also run `offscreen run hello launch` to save a screenshot. A blocking run streams command output and ends with a job ID and artifact directory on stderr; the guest clone is normally deleted. See [the example](../examples/hello/README.md) for the tasks.

## Host lane

The VM lane needs no extra setup. Before you queue host-lane jobs, read [lanes and safety](lanes.md) and the [Offscreen app guide](../app/README.md), which covers the input probe and manual return.
