# Lanes and safety

Every job runs in one of two lanes. The **VM lane** is the default and never touches your desktop. The **host lane** is opt-in, for work that needs real Mac hardware.

## VM lane

The VM lane clones the current golden image for each job, copies the worktree and selected prepared files to the guest, and collects `$OFFSCREEN_OUT`. It runs headless and never needs Input Monitoring. `offscreen run --show` opens a job's VM in a Tart window instead, for when you want to watch it.

By default, up to two VM jobs can run at once, reduced to one when memory is low. Apple's macOS virtualization license limits a host to **two macOS guests**; do not raise `vm_slots` beyond that. Use `exclusive = true` in a project contract to serialize its jobs.

On daemon restart, orphaned running jobs fail and owned stale VMs are cleaned up. Queued attached runs cannot replay stdin after restart; detach jobs when retries must be safe.

## Host lane

The host lane runs one task at a time on your real desktop, only after the configured idle threshold and checks for an unlocked, awake session, idle camera and microphone, and an unpaused lane. The [Offscreen menu bar app](../app/README.md) supplies the desktop signals, the countdown and the notifications, so it needs Input Monitoring and Notifications. It asks for Input Monitoring only once a host job is queued, and watches input only while one is queued or running. It does **not** require Accessibility permission (an external input-injection tool may require its own).

An automatic, nonactivating ten-second countdown offers **Not now** (30-minute durable delay). If the user returns during a job, a notification offers **Take back now**; the default grace period is 60 seconds. Take back cancels the process group and requeues the job at the front only after cleanup. A same-executable guardian sends TERM/KILL and reaps background descendants on normal exit, cancellation, or daemon death. A retried job has closed stdin; use detached host jobs when interruption is possible.

`offscreen pause` and `offscreen resume` hold or release the host lane. The manual pause survives daemon restarts.

### Return detection

**Automatic physical-input return detection is off by default.** The app's `pid == 0` event filter is a candidate until a physical-versus-injected probe confirms it on *your* Mac. Follow [the probe and manual-return instructions](../app/README.md) before trusting the host lane with hardware-sensitive work. The app's permission request or successful build alone is not proof.

### Crash recovery

If a host guardian crashes, a `host-group-*.json` marker blocks daemon restart. Inspect and resolve the process group yourself before removing that marker; offscreen does not kill a PID just because it appears in an old marker. The VM lane is independent of these markers.

See [daemon operation](../DAEMON.md) for state isolation, restart behavior, and cleanup details.
