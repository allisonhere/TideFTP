# TideFTP handover — transfers, recovery, test lab, and scripting

## What changed

- **Scriptable CLI at lftp parity.** The `tideftp` binary runs commands without
  the TUI — file operations, `--json`, `sync`/`mirror`, bandwidth limit,
  auto-reconnect with resume, `pget`, and a `script`/`shell` language with
  background jobs — for scripts, CI and cron. See the *Non-interactive CLI*
  section below; it has been tested against in-process SFTP/FTP servers only.
- **Queue is the live-transfer view.** The former Active tab is gone. Keys are
  `1` Queue, `2` Failed, `3` History, `4` Log, and `5` Stats.
- Queue shows two meters when work is pending: **Queue** (the full draining
  batch, including files already completed) and **Active** (only currently
  moving files). This keeps the top-level percent from moving backward as
  parallel files finish. Rows use segmented Unicode meters with ASCII
  fallbacks.
- Recursive upload/download and delete now open a working modal before their
  normal confirmation. It shows spinner, phase, file/folder/byte counts,
  current path, and elapsed time. `Esc` cancels the scan; late worker messages
  are retired and cannot reopen a prompt.
- Settings are grouped as Appearance, Transfer performance, Workflow,
  Reliability, and Updates. Reconnect now displays its effective window and
  interrupted-transfer recovery is independently configurable.

## Flaky connection behavior

- `auto_reconnect` defaults to on. Its deterministic retry delays are 2s, 4s,
  8s, 15s, 30s, 1m, 2m, and 5m (about nine minutes total). The top bar shows
  reconnect state and attempt number.
- `recover_interrupted_transfers` defaults to on. After a successful
  reconnect, each transfer interrupted by that drop is checked and the batch
  is presented in a review panel: `enter` resumes the safe ones, `r` restarts
  the mismatched/full ones from zero, `s` skips them, `↓` inspects the
  individual Failed rows, and `esc` decides later. Nothing is queued until the
  user resolves the panel.
  - partial destination up to 16 MB: safe to resume, but only after a
    byte-for-byte comparison proves it is an exact source prefix;
  - missing destination: safe to restart from zero;
  - full, mismatched, inaccessible, or larger partial destination: stays in
    Failed with an explanation; the panel's `r` restarts all of them, or `R`
    retries one row.
- A deliberate disconnect and ordinary per-file failure never trigger this
  recovery path.

## Connectivity checks

- `check_connectivity` defaults to on. When two transfers fail back to back,
  the UI spends one probe before starting more of the queue:
  - `internal/netcheck` checks for a usable local interface, then TCP-dials
    the target host:port with a 3s timeout;
  - unreachable pauses the queue, shows a pinned banner naming the problem
    ("no network connection" vs "server unreachable"), and re-probes on a
    `2s, 5s, 10s, 20s, 30s` backoff;
  - reachable resumes the queue and clears the streak.
- While `internal/ui/connectivity.go` is checking or paused, `startQueuedTransfers`
  promotes nothing, so the queue is held rather than failed item by item. A
  drop, a redial, or switching the setting off clears the pause.
- SFTP now sends `keepalive@openssh.com` every 30s (`default_keepalive_interval`
  in the Config; tests set it short), so a silently dead TCP path reaches
  `Conn.Done()` instead of looking alive forever. FTP already polled with NOOP.

## Transfer Lab

Run `tideftp --transfer-lab` to force the demo adapter and reveal **Transfer
Lab** in the Command Palette. It never contacts a real server or writes real
transfer data. The included scenarios exercise the normal Queue/reconnect
code paths:

- Tiny-file storm — 120 × 4 KB uploads
- Large-file batch — 4 × 2 GB uploads
- Mixed batch — 24 files from 8 KB to 512 MB
- Drop mid-transfer — 8 × 64 MB and a simulated demo-connection drop after
  900 ms

The lab validates UI/state behavior, not real FTP/SFTP/FTPS wire behavior.
For protocol-level fault testing, add a local real server behind a fault proxy
as a separate integration layer.

## Non-interactive CLI

The same binary runs commands without the TUI. Any non-flag first argument
goes to `cli.Run`; otherwise the app opens unchanged. User-facing docs are the
README "Scripting" section (including a "Coming from lftp" table).

Layout of `internal/cli`:

- `cli.go` — `App`, `dispatch` (command table), `report`/`exitCode`, shared
  flags (`connFlags`), `resolveLimit`, `live` (wraps a conn in `liveConn`).
- `commands.go` — ls/get/put/rm/mkdir/mv; `open`/`openWith` return the conn
  (or the script's shared one).
- `extra.go` — exit codes (`codedError`, `classifyDial`), globs, `stat`/`exists`.
- `inspect.go` / `pget.go` — cat, du, find, tree, chmod, pget, rmdir, ln, readlink.
- `sync.go`, `syncfilter.go`, `location.go` — the sync/mirror engine.
- `redial.go` — `liveConn`, `resilientFS`, `resilientEngine`.
- `script.go`, `jobs.go` — script/shell language and background jobs.
- Tests: fakefs-based unit tests, plus `e2e_test.go` over `internal/testserver`.

Design points worth knowing before changing it:

- **Exit codes:** `0` ok, `1` failed, `2` usage, `3` connect, `4` auth,
  `5` not found. `classifyDial` sorts dial failures by message text, so a server
  that words an auth failure unusually lands on `3`.
- **Reconnect/resume:** `liveConn` implements `session.Conn`, so no command
  knows it is there. `resilientFS` retries each call after a redial and treats
  the "already done" answer to a repeated mkdir/remove/rename/symlink as
  success. `resilientEngine.Start` runs `transfer.Copy` on the live conn and, on
  a transient error (`isTransient`), redials and resumes: downloads from the
  local partial file's size, uploads from `Stat(.part)`, ranged segments from
  the last progress report. A source whose size changed is a permanent error.
- **One transfer per engine at a time.** `transfer.Copy` owns `Events()`, so
  parallelism means one connection per worker (sync workers, pget segments, job
  sessions). Never run two Copies on one engine.
- **Limiter:** `transfer.Request.Limit` is read in the three byte-copy loops
  (`sftpsession/engine.go`, `ftpsession/engine.go` download loop and
  `progressReader`). One limiter shared between requests is a total cap.
- **Ranged downloads:** `Request.Length`/`NoTruncate`. The FTP engine cuts a
  segment short by closing the data connection and discards that control
  connection (`errSegmentDone`); the last segment reads to EOF normally.
- **Atomic writes:** `put`/`sync` upload to `NAME.part` and rename; sync
  downloads do the same and stamp the source mtime. A stale `.part` is
  discarded unless `--resume`.
- **Sync safety:** deletions run only after every copy succeeded; an empty
  source refuses `--delete`; delete candidates are computed against the
  *unfiltered* source tree. `applyMeta` copies mtime (`vfs.MtimeSetter`) and
  mode best-effort; FTP without MFMT just skips it.
- **Optional vfs interfaces** (`vfs.MtimeSetter`, `vfs.Symlinker`) avoid
  touching every `FS`. `resilientFS` must forward each one, or type assertions
  on the wrapper silently fail.
- **Scripts:** `App.shared` is the session; `open()` hands out `keepOpen`
  around it. Background jobs and the `queue` get their own session
  (`childSession`) with the cwd copied. `parseScript` is the tokenizer.
- **Location syntax:** `prefix:path` is remote when the prefix is two or more
  characters with no slash; a one-letter profile name is therefore unusable.

- **Issue #3** ("adds @domain to the username"): the FTP login always sent the
  typed name; the misleading part was the failed-login error text, now
  `login as "user" on host:port`. `ftpsession/login_test.go` pins both.
- **Short flags differ by command on purpose:** `-r` is recursive for
  get/put/rm, no-recursion only on `mirror` (lftp's meaning), absent on `sync`.
- `--progress` is opt-in and only drawn by `get`/`put` (`cli/progress.go`).
- The user guide is `docs/scripting.md`; keep it in step with flag changes.

Known limits are in TODO.md ("CLI follow-ups"). The big one: it has only run
against the in-process test servers, never a third-party FTP/SFTP server.

## Verification and useful entry points

- Last full verification: `go test ./...` and `git diff --check` pass.
- Scripting work: `internal/cli` unit tests (over `fakefs`), real-protocol tests
  in `internal/cli/e2e_test.go` (`go test -short` skips the paced ones),
  `internal/transfer/{copy,limiter}_test.go`, and `internal/testserver`.
- UI rendering and state live under `internal/ui/`; key areas are
  `view.go`, `model.go`, `recovery.go`, and `transfer_lab.go`.
- Golden snapshots are in `internal/ui/testdata/`. Refresh intentionally with
  `go test ./internal/ui -update`.
- The working tree includes an unrelated untracked `annotator.php`; do not add
  or remove it as part of TideFTP work without checking with its owner.
