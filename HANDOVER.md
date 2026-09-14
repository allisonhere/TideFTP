# TideFTP handover — transfer visibility, recovery, and test lab

## What changed

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

## Verification and useful entry points

- Last full verification: `go test ./...` and `git diff --check` pass.
- UI rendering and state live under `internal/ui/`; key areas are
  `view.go`, `model.go`, `recovery.go`, and `transfer_lab.go`.
- Golden snapshots are in `internal/ui/testdata/`. Refresh intentionally with
  `go test ./internal/ui -update`.
- The working tree includes an unrelated untracked `annotator.php`; do not add
  or remove it as part of TideFTP work without checking with its owner.
