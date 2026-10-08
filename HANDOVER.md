# TideFTP handover — transfers, recovery, test lab, and scripting

## What changed

- **Scriptable CLI.** The `tideftp` binary runs commands without the TUI —
  file operations, `--json` listings, `sync`/`mirror`, and one-connection
  `script`/`shell` — for scripts, CI and cron. See the *Non-interactive CLI*
  section below; it has never been run against a real server.
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
goes to `cli.Run`; otherwise the app opens unchanged. User-facing docs are in
the README "Scripting" section.

```
ls stat exists cat du find tree      read-only; ls/stat/find take --json
get put rm mkdir mv chmod            get/put take globs and several sources
sync (alias mirror) SRC DST          one-way mirror, PROFILE:/path locations
script [-c CMDS | FILE]  shell       many commands over one connection
```

- **Exit codes** (`internal/cli/extra.go`): `0` ok, `1` failed, `2` usage,
  `3` connect, `4` auth, `5` not found. `codedError` carries an explicit code
  (a nil inner error is silent — `exists` uses that); `classifyDial` sorts dial
  failures by message, so a server that words its auth error unusually will
  land on `3`.
- **Connections:** `App.open` dials per command, or returns `App.shared` (a
  `keepOpen` wrapper whose Close is a no-op) when running inside `script`/
  `shell`. `sync` always dials itself — one connection per `--transfers`
  worker, because a `transfer.Engine` serves one transfer at a time and
  `transfer.Copy` owns its event channel (never run two Copies on one engine).
- **Writes are atomic:** `put` and `sync` upload to `NAME.part` and rename;
  downloads in `sync` do the same and stamp the source mtime. `--resume`
  continues a leftover download `.part` only when asked, since the source may
  have changed.
- **Sync safety:** deletions run only after every copy succeeded; an empty
  source refuses `--delete` without `--allow-empty-source`; a file filtered out
  by size/age is never treated as deleted from the source (delete candidates
  are computed against the unfiltered source tree).
- **Location syntax** (`location.go`): `prefix:path` is remote when the prefix
  is two or more characters with no slash, so a one-letter profile name cannot
  be used (Windows drive letters win).
- **Not done:** `--bwlimit` (engines cannot throttle), redial mid-transfer,
  `**` globs, `ln`, empty-dir mirroring under filters. See TODO.md.
- **Never run against a real server.** The CLI tests use `fakefs` plus a
  disk-copying test engine; `fakefs.Remote` is not goroutine-safe, so the sync
  tests wrap it in a locking `vfs.FS`. Run `sync --dry-run`, `put`, `get` and
  `script` against a QA host before trusting it.

## Verification and useful entry points

- Last full verification: `go test ./...` and `git diff --check` pass.
- Scripting work: unit tests in `internal/cli` (over `fakefs` plus a real
  disk-copying test engine) and `internal/transfer/copy_test.go`; the FTP
  adapter path was smoke-tested end to end against an in-process server.
- UI rendering and state live under `internal/ui/`; key areas are
  `view.go`, `model.go`, `recovery.go`, and `transfer_lab.go`.
- Golden snapshots are in `internal/ui/testdata/`. Refresh intentionally with
  `go test ./internal/ui -update`.
- The working tree includes an unrelated untracked `annotator.php`; do not add
  or remove it as part of TideFTP work without checking with its owner.
