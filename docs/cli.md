# Command reference

Run from a directory under a repo containing `.offscreen.toml`, or pass `--worktree` to point at one. The `<project>` argument must match the file's `project` key. Run `offscreen --help` or `offscreen <command> --help` for flag syntax.

| Command | What it does |
| --- | --- |
| `offscreen run <project> <task>` | Queue a named task, stream output, and wait for its result. |
| `offscreen run <project> -- <cmd> [args...]` | Run an argv command directly; defaults to the VM lane. |
| `offscreen status` | List queued and active jobs, newest first, with lane, state, age, and waiting reason. `-a` also lists completed jobs. |
| `offscreen logs <job>` | Follow stdout/stderr while active; return after a completed job. `-f` also follows; `--no-follow` prints a snapshot. |
| `offscreen artifacts <job>` | Print the job's artifact directory. |
| `offscreen cancel <job>` | Cancel an active/queued job and wait for cleanup; also delete a retained `--keep` clone without changing its recorded result. |
| `offscreen prune --older-than 720h` | Delete completed jobs (record, logs and artifacts) that finished longer ago than the given age. Queued, active and retained jobs are skipped. Asks first; see [Prune](#prune). |
| `offscreen pause` / `offscreen resume` | Pause/release the host lane; the manual pause survives daemon restarts. Resume also clears a **Not now** delay. |
| `offscreen image build` / `offscreen image refresh` | Build a golden image; refresh pulls the base image again before rebuilding. |
| `offscreen image ls` | List golden images, newest first, with build date and size. `*` in the CURRENT column marks `current_image`. |
| `offscreen image prune` | Delete golden images other than `current_image`. Only names `image build` produces are selected; running images are skipped. Refuses if `current_image` is missing from Tart, or (when deleting) while a VM job is active. Asks first; see [Prune](#prune). |
| `offscreen daemon install` / `offscreen daemon uninstall` | Register/start or stop/remove the user's daemon LaunchAgent. |

## List output

`status` and `image ls` print an aligned table with a header row on stdout. Both take the same output flags:

| Flag | Meaning |
| --- | --- |
| `-q`, `--quiet` | Print only job IDs (`status`) or image names (`image ls`), one per line. Overrides `--format`. |
| `--format table` | The default table. |
| `--format json` | One JSON object per row, one per line. Field names may change before 1.0. |
| `--format '<template>'` | A Go [text/template](https://pkg.go.dev/text/template) applied to each row, for example `'{{.ID}} {{.State}}'`. |
| `--format 'table <template>'` | A table with headers built from the template, for example `'table {{.ID}}\t{{.State}}'`. `\t` and `\n` are expanded. |

Template fields for `status`: `.ID`, `.Project`, `.Task`, `.Lane`, `.State`, `.Created` (relative, such as `5 minutes ago`), `.CreatedAt` (a time), and `.Reason`.

Template fields for `image ls`: `.Name`, `.Current` (true or false), `.Running`, `.Created` (the build date in the image name), `.Size` (such as `73 GB`), and `.SizeGB`.

## Prune

`prune` and `image prune` list what they would delete and a warning on stderr, then ask `Are you sure you want to continue? [y/N]`. Only `y` or `yes` deletes; anything else, including Enter, changes nothing. If nothing is selected, they do not ask. Stdout gets the result: the cutoff (`prune`) or kept `current_image` (`image prune`), each `deleted` and `skipped` entry, and a `summary` line.

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
