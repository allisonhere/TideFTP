# TideFTP Handoff

## Current State

TideFTP is a new Go terminal file transfer client in
`/home/allie/Projects/whatthedock/TideFTP`.
The first implementation slice is a polished UI shell with a fake remote adapter,
not real FTP/FTPS/SFTP networking yet.

Implemented:

- Go module: `module tideftp`
- Bubble Tea executable: `cmd/tideftp`
- TideUI-based visual system with `tide-night` as the default theme
- FileZilla-style layout: local pane, remote pane, wide bottom transfer pane
- Whatthedock-style soft overlays with drop shadows
- Asynchronous filesystem interface (`internal/vfs`) shared by both file
  panes, over real disk (`internal/localfs`) and a fake remote
  (`internal/fakefs`)
- Asynchronous transfer engine interface (`internal/transfer`) with a
  simulated implementation under `internal/faketransfer`
- Bottom tabs: Queue, Active, Failed, History, Log
- Theme picker on `t`
- Connect, help, and conflict modals
- Keyboard-driven navigation plus initial mouse focus/select behavior
- Shift-arrow pane resizing
- Basic UI tests for layout, theme registration, and resizing

Git repository state:

- Initialized as a normal Git repository on `main`, pushed to
  `git@github.com:allisonhere/TideFTP.git`. The old placeholder `.git`
  directory is gone, so `-buildvcs=false` and the project-local Go caches
  are no longer needed.

## Run And Verify

Run the TUI:

```bash
cd /home/allie/Projects/whatthedock/TideFTP
./start.sh
# or: go run ./cmd/tideftp
```

Verification commands used:

```bash
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go build -o /tmp/tideftp ./cmd/tideftp
```

Last known result:

- `go test -count=1 ./...`: passed
- `go test -race -count=1 ./...`: passed
- `go vet ./...`: passed
- `go build -o /tmp/tideftp ./cmd/tideftp`: passed

Run `-race` from now on: `faketransfer` is the first concurrent code in the
project, and the UI consumes its channel from a Bubble Tea command goroutine.

Driving the built binary from a synthetic PTY has not worked: the process
renders, but plain rune keypresses written to the pty master never reach it
(Ctrl+C arrives only as SIGINT from the line discipline). This reproduces on
older commits too, so it is a harness problem, not a regression — but it means
there is still no automated smoke test of the real binary. Manual runs are the
only coverage there.

## Product Decisions

Core direction:

- Language: Go
- TUI stack: Bubble Tea plus TideUI
- Visual reference: Whatthedock setup screens, with TideMail-grade theming
- Main layout: two top file panes and one full-width bottom operational pane
- Default theme: `tide-night`
- Modal shadows: global setting, default on
- Mouse: on by default, keyboard fully supported
- Pane resizing: Shift+arrows, global saved layout eventually, per-profile override later

Supported protocol goals:

- FTP
- FTPS
- SFTP

Other transfer protocols are out of scope for the first full product pass.

Transfer behavior:

- Conflict modal should mirror FileZilla options:
  - Overwrite
  - Overwrite if source newer
  - Overwrite if different size
  - Overwrite if different size or source newer
  - Resume
  - Rename
  - Skip
- Conflict scopes:
  - This file
  - Current queue
  - This session
- Resume partial transfers: yes, best effort
- Parallel transfers: configurable, default 2
- Recursive folders: supported with a preflight summary
- Metadata preservation: configurable, default best effort

Selection behavior:

- Actions apply to selected items; if nothing is selected, act on cursor item
- Range selection: keyboard and mouse
- Hidden files: hidden by default, toggle with `.`

Profiles and security:

- Save connection profiles
- Password storage options should be offered, defaulting to prompt each time
- Preferred saved secret storage: OS keyring
- Config-file password storage only with explicit insecure warning
- SFTP auth: password, SSH agent, and key file
- Profile start paths: configured paths if set, otherwise remembered paths
- FTPS certificate verification: per-profile setting, default verify
- SFTP known hosts: per-profile strict/ask/off, default ask
- Logs: redacted normal logs plus opt-in full protocol logs

Bottom pane behavior:

- Tabs: Queue, Active, Failed, History, Log
- Completed transfers: stay in Queue briefly, then move to History
- Failed transfers: keep in Failed tab with retry

CLI:

- Design for future non-interactive CLI mode, but do not build it in the first
  UI-focused slice.

First build slice:

- Gorgeous shell plus fake protocol adapter first
- Real FTP/FTPS/SFTP backends later behind the same interface

## Code Map

- `cmd/tideftp/main.go`: Bubble Tea program entrypoint; constructs the fake
  remote adapter and the fake transfer engine, and wires them into the UI as
  a `vfs.FS` pair plus a `transfer.Engine`
- `internal/domain/domain.go`: shared entry and transfer types
- `internal/vfs/vfs.go`: filesystem interface both panes browse through
  (`List`/`Child`/`Parent`). `List` blocks and takes a `context.Context`;
  `Child`/`Parent` are pure path math. FTP/FTPS/SFTP adapters implement
  this alongside `localfs` and `fakefs`
- `internal/localfs/localfs.go`: the machine's own disk, implements `vfs.FS`
- `internal/fakefs/fakefs.go`: fake remote directory tree, implements
  `vfs.FS`; `NewRemoteWithLatency` adds fake round-trip time so the panes'
  loading state is visible when running by hand
- `internal/transfer/transfer.go`: protocol-agnostic transfer engine
  interface (`Start`/`Cancel`/`Events`/`Close`) that FTP/FTPS/SFTP engines
  will implement. Asynchronous by contract: `Start` returns immediately and
  everything afterwards arrives as a `transfer.Event`
- `internal/faketransfer/faketransfer.go`: simulated engine, implements
  `transfer.Engine`; emits timed progress events and fails every fifth
  transfer so the Failed tab stays reachable
- `internal/ui/model.go`: Bubble Tea state, update loop, listing requests and
  replies, transfer queue, key/mouse routing; depends only on `vfs.FS` and
  `transfer.Engine`, never on a concrete adapter
- `internal/ui/view.go`: custom two-over-one layout, panes, overlays, status bars, transfer rows
- `internal/ui/themes.go`: app theme registration, including `tide-night`
- `internal/ui/model_test.go`: UI behavior tests, driven by a hand-scripted
  `transfer.Engine` stub so they never depend on goroutine timing
- `internal/ui/integration_test.go`: real model plus real `faketransfer`
  engine, pumping actual events end to end
- `README.md`: quick run instructions and keybindings

## Design Notes

TideUI v0.2.2 has a built-in `ThreeColumn` layout, but TideFTP needs a
FileZilla-like two-over-one layout. The current implementation uses TideUI for
themes, pane frame styles, rows, soft panels, theme picker, and ratio helpers,
while composing the actual local/remote/top plus transfer/bottom layout inside
`internal/ui/view.go`.

The fake adapter is intentionally realistic enough to exercise the UI:

- nested remote directories
- hidden files
- mixed file sizes
- simulated queued and active transfers
- redacted log entries

Do not wire real protocol complexity into the UI package directly. Real
FTP/FTPS/SFTP adapters should implement `vfs.FS` (browsing) and
`transfer.Engine` (moving bytes), and be constructed in `cmd/tideftp/main.go`
(or a future profile/connect flow), the same way `localfs.New()`,
`fakefs.NewRemote()` and `faketransfer.New()` are today — the UI package
itself should never import a concrete adapter package.

Both seams are now asynchronous, but in different shapes, because the two jobs
differ. A transfer is long-lived and reports progress, so `transfer.Engine`
streams events over a channel. A listing is one-shot request/response, so
`vfs.FS.List` is an ordinary blocking call taking a `context.Context`, and
`internal/ui` wraps it in a `tea.Cmd`. Keeping `vfs` free of any UI framework
leaves the adapters usable from the planned non-interactive CLI mode.

Asynchronous listing brings three problems the old synchronous code never had,
all handled in `applyListing`:

- **Stale replies.** Two quick Enter presses put two listings in flight, and
  the slower one must not overwrite the newer directory. Every request carries
  a token; replies whose token is not the pane's latest are dropped.
- **Failure.** The pane's path is not committed until the listing succeeds, so
  a directory that cannot be read leaves the pane exactly where it was.
- **Latency.** A pane keeps showing its current contents while a listing is in
  flight, with the directory being opened shown in its header and a marker on
  the title. Nothing blocks.

The local pane goes through the same path as the remote one even though local
reads are usually instant, because "usually" is doing real work there: a
stalled network mount blocks `os.ReadDir` just as hard as a dead FTP server.

The queue lives in the UI, not the engine. The UI decides what runs and when
(ordering, the `maxParallel` cap, and eventually conflict policy) and hands the
engine one `transfer.Request` per running transfer. Engines own concurrency for
the work they are given but never queue. The contract that keeps this honest:
every accepted Request must produce exactly one terminal event (Completed,
Failed, or Canceled), or the UI's queue stalls waiting for a slot.

## Suggested Next Steps

1. ~~Initialize or fix Git repository state.~~ Done — real repo on `main`,
   pushed to `git@github.com:allisonhere/TideFTP.git`, `-buildvcs=false` no
   longer needed.

2. ~~Extract a filesystem interface.~~ Done — `internal/vfs.FS` defines
   `List`/`Child`/`Parent`, asynchronous and error-returning;
   `localfs.FS` and `fakefs.Remote` implement it; `internal/ui` takes both
   via `NewModel(local, remote, engine)` and never imports an adapter.

3. Add real layout/config persistence.
   - XDG config path: `~/.config/tideftp/config.toml`
   - State/log path: `~/.local/state/tideftp/`
   - Cache path: `~/.cache/tideftp/`

4. Build profile model and connect form.
   - Protocol: FTP, FTPS, SFTP
   - Host, port, username
   - Password mode: prompt/keyring/config
   - SFTP agent/key file
   - FTPS certificate verification mode
   - SFTP known-host mode

5. Improve transfer queue behavior.
   - Configurable parallelism — the cap is now `Model.maxParallel`
     (default `defaultParallelTransfers` = 2); it still needs a config
     source and a UI to change it
   - Completed-transfer aging into History
   - Retry failed transfers
   - ~~Cancel active transfers.~~ Done — `x` cancels everything in flight
     through `transfer.Engine.Cancel`. Per-row cancel still needs a cursor
     in the transfers pane (it scrolls but has no selected row), and
     `domain` has no Canceled status yet, so canceled transfers land in the
     Failed tab
   - Recursive folder preflight summary

6. Add protocol adapters.
   - Start with SFTP (`pkg/sftp`): it is testable in-process, unlike FTP,
     which needs a real daemon.
   - Implement both `vfs.FS` and `transfer.Engine`.
   - The fake adapters and their tests are the contract that proves the UI
     did not regress while real adapters land.

7. Expand TUI tests.
   - Snapshot/golden views for main screen and overlays
   - Key routing tests for focus, selection, tabs, and modals
   - Resize tests for small terminals
   - Transfer queue state tests

## Known Gaps

- No real FTP/FTPS/SFTP networking yet; `faketransfer` moves no bytes, it
  only emits a plausible event stream on a timer
- Both adapter seams (`vfs.FS`, `transfer.Engine`) are ready for real
  implementations; nothing else blocks an SFTP adapter
- A listing that hangs is bounded only by `listTimeout` (20s) and cannot be
  cancelled from the UI — there is no key to abandon a slow directory
- No config persistence yet
- No saved profiles yet
- No real credential storage yet
- Mouse support is basic: focus/select, not full range selection or context
  menus. The click-to-row mapping depends on the chrome above the file panes
  (`firstFileRow` in `internal/ui/model.go`) and on `topPaneHeight`, which
  `View` and the hit test must keep sharing — changing the topbar or pane
  header height means updating `firstFileRow`
- No recursive transfer summary yet
- Conflict modal is visual/simulated, not wired to transfer policy yet;
  it is a demo opened with `o` (it used to be `delete`, which read as
  "delete this file")
- Transfer failures are simulated deterministically by `failsAt` in
  `internal/faketransfer` so the Failed tab and error styling are reachable
  without real networking; the whole package goes away once real engines land
- Completed transfers do not yet age out of Queue into History
- Failed transfer retry flow is not implemented yet
- No screenshot/PTY visual QA has been run yet, and synthetic PTY input does
  not currently work (see Run And Verify)
