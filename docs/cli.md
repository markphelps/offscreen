# Command reference

Run from a directory under a repo containing `.offscreen.toml`, or pass `--worktree` to point at one. The `<project>` argument must match the file's `project` key. Run `offscreen --help` or `offscreen <command> --help` for flag syntax.

| Command | What it does |
| --- | --- |
| `offscreen run <project> <task>` | Queue a named task, stream output, and wait for its result. |
| `offscreen run <project> -- <cmd> [args...]` | Run an argv command directly; defaults to the VM lane. |
| `offscreen status` | List queued and active jobs, newest first, with lane, state, age, and waiting reason. Failed `--keep` jobs whose clone is still retained are listed too, as `failed (retained)`, until cancelled. `-a` also lists completed jobs. |
| `offscreen logs <job>` | Print the job's stdout/stderr so far and exit. `-f`/`--follow` keeps printing until the job ends. |
| `offscreen artifacts <job>` | Print the job's artifact directory. |
| `offscreen cancel <job>` | Cancel an active/queued job and wait for cleanup; also delete a retained `--keep` clone without changing its recorded result. |
| `offscreen prune --older-than 720h` | Delete completed jobs (record, logs and artifacts) that finished longer ago than the given age. Queued, active and retained jobs are skipped. Asks first; see [Prune](#prune). |
| `offscreen pause` / `offscreen resume` | Pause/release the host lane; the manual pause survives daemon restarts. Resume also clears a **Not now** delay. |
| `offscreen image build` / `offscreen image refresh` | Build a golden image; refresh pulls the base image again before rebuilding. Shows each step on stderr and prints only the new image name on stdout; see [Image build output](#image-build-output). |
| `offscreen image ls` | List golden images, newest first, with build date and size. `*` in the CURRENT column marks `current_image`. |
| `offscreen image prune` | Delete golden images other than `current_image`. Only names `image build` produces are selected; running images are skipped. Refuses if `current_image` is missing from Tart, or (when deleting) while a VM job is active. Asks first; see [Prune](#prune). |
| `offscreen daemon install` / `offscreen daemon uninstall` | Register/start or stop/remove the user's daemon LaunchAgent. |
| `offscreen version` | Print this client's build version and protocol, then the daemon's. See [Versions](#versions). |

## List output

`status` and `image ls` print an aligned table with a header row on stdout. Both take the same output flags:

| Flag | Meaning |
| --- | --- |
| `-q`, `--quiet` | Print only job IDs (`status`) or image names (`image ls`), one per line. Overrides `--format`. |
| `--format table` | The default table. A cell shows only the first line of a multi-line value, such as a failed job's reason, ending in `…`; templates and JSON get the whole value. |
| `--format json` | One JSON object per row, one per line. Field names may change before 1.0. |
| `--format '<template>'` | A Go [text/template](https://pkg.go.dev/text/template) applied to each row, for example `'{{.ID}} {{.State}}'`. |
| `--format 'table <template>'` | A table with headers built from the template, for example `'table {{.ID}}\t{{.State}}'`. `\t` and `\n` are expanded. |

Template fields for `status`: `.ID`, `.Project`, `.Task`, `.Lane`, `.State`, `.Created` (relative, such as `5 minutes ago`), `.CreatedAt` (a time), `.Reason`, and `.Retained` (true while a failed `--keep` job's clone is retained; `.State` stays `failed`).

Template fields for `image ls`: `.Name`, `.Current` (true or false), `.Running`, `.Created` (the build date in the image name), `.Size` (such as `73 GB`), and `.SizeGB`.

## Image build output

`image build` and `image refresh` show numbered steps on stderr as they run: pull the base image, clone it, boot the VM, wait for guest SSH, configure the display, check auto-login and GUI, enable Xcode automation, run each `[image] setup` script, clean up the guest desktop, and seal the image. Stdout gets only the new image name, so `name=$(offscreen image build)` works.

The global `--progress` flag, or `OFFSCREEN_PROGRESS` when the flag is not given, picks how steps are shown:

| Mode | Output |
| --- | --- |
| `auto` (default) | `tty` when stderr is a terminal, else `plain`. |
| `tty` | Steps redrawn in place with a spinner and timer. The running step shows its last few output lines; a finished step shrinks to one line with its duration. Falls back to `plain` when stderr is not a terminal or `TERM=dumb`, since it needs cursor movement. |
| `plain` | One line per step start and end, and every output line prefixed with its step number, such as `[8/11] ...`. |
| `quiet` | No progress; only the image name and errors. |
| `json` | One JSON object per event. Field names may change before 1.0. |

Color is off when `NO_COLOR` is set, `TERM=dumb`, or stderr is not a terminal. Tart has no supported progress output for `tart pull`, so the pull step shows only its timer; Tart's output goes to the build log.

Every build writes its full log, including Tart's pull output and all guest command output, to `image-builds/offscreen-build-<id>.log` in the config file's directory (`~/Library/Application Support/offscreen/` by default, or beside `OFFSCREEN_CONFIG`). When a step fails, stderr shows that step's last 20 output lines, a one-line error, and the log path. Logs are kept after the build; delete old ones by hand.

## Run phases

While a `run` job waits and starts, stderr shows its phases (when the daemon supports them; see [Versions](#versions)): `queued` (with the reason, such as `waiting for VM slot`), `preparing` (the host `[prepare]` command), `cloning`, `booting`, and `preparing` again (copying the worktree and running `[guest] setup`). The host lane has no `cloning` or `booting`. Output before the command starts, such as `[prepare]` build logs, is shown under its phase (the last few lines in a terminal); if the job fails before the command starts, the last 20 lines are repeated after the phases, and `offscreen logs <job>` has all of it. The display ends when the job reaches `running` or writes to stdout; from then on stdout and stderr belong to the command, and stdout never carries phases.

`--progress` picks how phases are shown, as for image builds. `auto` shows them only when stderr is a terminal, so `offscreen run ... 2>log` and scripts see only the command's output. `quiet` hides them; an explicit `tty`, `plain` or `json` shows them even when stderr is not a terminal. `--detach` shows no phases and prints only the job ID. `offscreen logs` never shows phases, because they are not stored with the job's output. The same states, including `cloning` and `booting`, appear in the STATE column of `offscreen status`.

## Versions

`offscreen version` prints two lines on stdout:

```text
client: 0.3.0 (protocol 1)
daemon: 0.2.0 (protocol 1)
```

The daemon line is `daemon: not running` when the daemon is down and `daemon: protocol 0 (pre-negotiation)` for a daemon built before version negotiation. Builds made with plain `go build` report `dev`. Compatibility depends only on the protocol number.

When the CLI and daemon differ:

- If the daemon is older but still usable, `status` and `run` print one line on stderr, `warning: offscreend speaks protocol N, older than this offscreen's M; ...`. Features the daemon lacks stay off; against a protocol 0 daemon, `run` shows no phases. Stdout is unchanged.
- If the daemon is too old for this client, every command that talks to it fails with `offscreend speaks protocol N, but this offscreen needs protocol M or later` and a hint line, and exits 70.

Either way, restart the daemon with this build: choose Restart Daemon in the Offscreen menu, or quit and reopen Offscreen. When the daemon isn't running at all, commands that need it exit 70 with `Offscreen isn't running. Open it with: open -a Offscreen`. See [versions and upgrades](../DAEMON.md#versions-and-upgrades).

## Prune

`prune` and `image prune` list what they would delete and a warning on stderr, then ask `Are you sure you want to continue? [y/N]`. Only `y` or `yes` deletes; anything else, including Enter, changes nothing. If nothing is selected, they do not ask. After `y`, `image prune` deletes only the images it listed; an image that became eligible while it waited, such as the previous `current_image` after a build finished, is reported as `skipped`. Stdout gets the result: the cutoff (`prune`) or kept `current_image` (`image prune`), each `deleted` and `skipped` entry, and a `summary` line.

| Flag | Meaning |
| --- | --- |
| `-f`, `--force` | Delete without asking. |
| `--dry-run` | List what would be deleted (`would-delete`) and change nothing. |

When stdin is not a terminal, pass `-f` or `--dry-run`; without either, both commands fail before deleting anything. If some deletions fail, the command exits nonzero; rerun with `-f` to retry.

## `run` flags

| Flag | Meaning |
| --- | --- |
| `--lane vm\|host\|auto` | Default `auto`: a named task uses its configured lane, a direct command uses VM. |
| `--timeout 1m` | Limits waiting and running; default none. |
| `--worktree /path/to/worktree` | Use the contract in this exact directory. |
| `--detach` | Print the job ID and return; stdin is closed. |
| `--keep` | Retain a failed VM clone for inspection. |
| `--show` | Open the VM in a Tart window on your desktop so you can watch the job. VM lane only; the default is headless. The window may take focus. |

A kept clone **holds its VM slot and any exclusive-project lease** until `offscreen cancel <job>` deletes it.

`--log-level` or `OFFSCREEN_LOG_LEVEL` selects debug, info, warn, or error logs on stderr. Stdout belongs to the wrapped command.

## Exit status

| Exit status | Meaning |
| --- | --- |
| Wrapped command's code | The command ran, including a nonzero test failure. |
| `75` | Queue deadline expired before execution. |
| `70` | Runner, daemon, VM, preparation, or cleanup failed (also a lost attached stdin after daemon restart). |
| `1` | CLI usage, config, or other client-side error. |
