# Offscreen menu bar app

**Offscreen** is the menu bar app that comes with the CLI and daemon; the Homebrew cask and `mise run install` both install all three (see the [install guide](../docs/install.md)). A source install starts it at login from `~/Library/LaunchAgents/io.latentsignal.offscreen.plist`; with Homebrew, add it as a login item yourself. It owns the daemon's lifecycle.

## Daemon controls

Launching Offscreen starts the daemon (`launchctl kickstart` of `io.latentsignal.offscreend`). The top of the menu shows the daemon's state: running with its version, starting, not responding, or a version that doesn't match the app (restart it). **Restart Daemon** and **Quit Offscreen** each list active jobs first; if there are any, they ask "Restart and cancel N jobs?" or "Quit and cancel N jobs?" and, on confirm, cancel them through the daemon's normal cancel path before stopping it. If the daemon doesn't stop within 20 seconds, Restart Daemon shows the error instead of restarting, and Quit asks "Quit Anyway?" before leaving it running. Logout, restart and shutdown don't ask or cancel: Offscreen sends the daemon `SIGTERM`, waits at most 5 seconds, and never holds up the logout, even while a restart or its confirmation is in progress. The daemon fails jobs that were in flight and deletes their clones the next time it starts. Only one copy of Offscreen runs at a time; a second copy exits at launch. If the daemon crashes, the menu says it isn't responding; choose **Restart Daemon**. When Offscreen isn't running, CLI commands that need the daemon print `Offscreen isn't running. Open it with: open -a Offscreen`.

## Jobs menu

The menu lists every queued and running job from both lanes (project · task, lane, state, elapsed time), then the last five finished jobs with their result. Each job has a submenu: **Cancel** for active jobs (the daemon's normal cancel path), **Reveal Artifacts** for finished jobs, and **Copy Job ID** for all. The app reads `GET /jobs` on its one-second heartbeat. While the menu is open, its job rows and elapsed times stay as they were when it opened and update when it closes. A job that fails while the app is running posts a notification (click it to reveal the artifacts) and puts the icon in its attention state, an exclamation mark in the front display, until you open and close a menu that lists it. A pending host action (countdown, Not now, Take back) or a failed menu action also shows attention. The status item's accessibility label reads `Offscreen`, `Offscreen, jobs running` or `Offscreen, needs attention`.

## Host lane

For the host lane, Offscreen shows a nonactivating panel for the ten-second countdown, with a visible **Not now** button; the menu offers the same action. It does not activate the app or request keyboard focus. While the daemon has a host job queued or running, Offscreen sends a fresh desktop signal to the daemon each second over the owner-only Unix socket, and listens for input to offer **Take back now** in a notification. The menu also offers **Pause host jobs** / **Resume host jobs**. VM jobs need none of this.

## Build from source

Install Xcode. From the repository root, run:

```sh
cd app
swift build
swift test
./scripts/bundle.sh
codesign --verify --strict .build/Offscreen.app
codesign -d -r - .build/Offscreen.app
```

The bundle has the fixed identifier `io.latentsignal.offscreen`. Its ad-hoc signature uses an identifier-based designated requirement so local rebuilds keep the same permission identity. It is not notarized. The Info.plist sets `LSUIElement`, so the app stays in the menu bar without a Dock icon. The bundle step creates only `app/.build/Offscreen.app`. Its icon is the committed `Resources/AppIcon.icns`; after changing `docs/assets/icon.svg`, regenerate it with `./scripts/icon.sh`.

`mise run install` from the repository root (without mise: `scripts/install.sh`) builds this bundle, copies it to `~/Applications/Offscreen.app`, registers it with Launch Services and loads its LaunchAgent, together with the CLI and daemon. It first stops any running Offscreen, including one opened with `open -a`, so only one instance runs. `mise run uninstall` removes all of them. Reinstall after a rebuild. The script refuses to replace an app or agent with another identifier. If macOS asks for Local Network access while running VM jobs, grant it to **Offscreen Daemon**, not Offscreen; this is independent of the app's permissions below.

Offscreen asks for **Notifications** the first time it has one to show, and for **Input Monitoring** only once a host job is queued; it runs the event tap and desktop snapshots only while a host job is queued or running. While it does, it opts out of App Nap so its heartbeat stays on time; it doesn't keep the Mac from sleeping. Allow both for Offscreen in System Settings when asked. If you grant Input Monitoring after Offscreen asks, it retries the event tap. You may need to quit and reopen Offscreen if macOS does not apply the grant at once. No Accessibility grant is needed. Missing permission or a stale app signal keeps host jobs queued with an explicit reason in `offscreen status`. If you upgraded from the old **Offscreen Helper**, grant these again and remove its stale rows by hand; see [upgrading](../docs/install.md#upgrading-from-an-older-install).

## Input discrimination probe

Each installation must check physical versus injected input for itself before turning on automatic return detection. A successful build, a PID guess, or a permission denial does not settle that question for your Mac. Run the installed app in probe mode:

```sh
launchctl bootout "gui/$(id -u)" "$HOME/Library/LaunchAgents/io.latentsignal.offscreen.plist"
"$HOME/Applications/Offscreen.app/Contents/MacOS/Offscreen" --probe-input
```

Grant Input Monitoring if asked. Move a physical mouse and press a real key. Then send a synthetic mouse move from a separately trusted input-injection tool. The injector may need its own Accessibility permission; Offscreen does not. Record the printed `pid` and `sourceState` for both events, including the injector's actual PID. Do not count a denied tap or an event-free run as a negative discrimination result. Stop the foreground probe with Ctrl-C. Restart only the app with `launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/io.latentsignal.offscreen.plist"`. The standalone `probe.swift` is the small source version of this experiment; use the installed bundle for a stable Input Monitoring identity.

The current event-tap filter treats `pid == 0` as a candidate for physical input. Automatic return detection stays off until you enable it after your own probe. For reference, one tested Apple silicon Mac reported physical mouse, key, click, and scroll events with `pid` 0 and `sourceState` 1. A synthetic mouse move from a separate injector reported that injector's nonzero PID and `sourceState` 0. That result is from one machine and one injector; it does not show what your Mac reports. If the fields distinguish physical events on your Mac, enable the filter and restart Offscreen:

```sh
defaults write io.latentsignal.offscreen VerifiedInputFilter -bool YES
```

If the observed streams cannot be distinguished, leave that setting off and enable the documented manual fallback:

```sh
defaults write io.latentsignal.offscreen ManualReturn -bool YES
```

Restart Offscreen after changing that setting. In fallback mode, Offscreen does not detect a mid-run return automatically. Select **I'm back (start grace period)** in its menu to get a notification and a 60-second grace period by default. Select **Take back now** to cancel an active host job (including one still preparing) and put it at the front of the queue after cleanup. Failed controls remain visible as errors in the menu until a successful action or dismissal. **I'm back** and **Take back now** are also available in the menu when automatic detection is on. Reset with `defaults delete io.latentsignal.offscreen ManualReturn` only after a successful physical-input test. A return while a job is still preparing cancels preparation before the task launches and requeues it after cleanup. A return during a running task does not stop it immediately. At grace expiry, the daemon requeues it if the desktop is still in use or the app's signal is stale. It lets the current task continue if the desktop is idle again. Later host jobs still wait for a fresh idle gate.

Use a short owner-only `OFFSCREEN_STATE_DIR` for isolated testing. Never point Offscreen at another user's socket. Do not use a hardware-sensitive host task before you have checked lock, display, microphone, camera, countdown, and notification behavior on your own Mac. A harmless `sleep` or `echo` task is sufficient for the first test.
