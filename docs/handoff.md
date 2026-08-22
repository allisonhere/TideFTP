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
- Local filesystem browsing using real local files
- Fake remote filesystem data under `internal/fakefs`
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
  remote adapter and wires it into the UI as a `remotefs.FS`
- `internal/domain/domain.go`: shared entry and transfer types
- `internal/remotefs/remotefs.go`: protocol-agnostic remote filesystem
  interface (`List`/`Child`/`Parent`) that FTP/FTPS/SFTP adapters will
  implement alongside the fake one
- `internal/fakefs/fakefs.go`: fake remote directory tree, implements
  `remotefs.FS`
- `internal/transfer/transfer.go`: protocol-agnostic transfer engine
  interface (`Start`/`Cancel`/`Events`/`Close`) that FTP/FTPS/SFTP engines
  will implement. Asynchronous by contract: `Start` returns immediately and
  everything afterwards arrives as a `transfer.Event`
- `internal/faketransfer/faketransfer.go`: simulated engine, implements
  `transfer.Engine`; emits timed progress events and fails every fifth
  transfer so the Failed tab stays reachable
- `internal/ui/model.go`: Bubble Tea state, update loop, fake transfer simulation, key/mouse routing; depends only on `remotefs.FS`, not on `fakefs` directly
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
FTP/FTPS/SFTP adapters should implement `remotefs.FS` (browsing) and
`transfer.Engine` (moving bytes), and be constructed in `cmd/tideftp/main.go`
(or a future profile/connect flow), the same way `fakefs.NewRemote()` and
`faketransfer.New()` are today — the UI package itself should never import a
concrete adapter package.

The queue lives in the UI, not the engine. The UI decides what runs and when
(ordering, the `maxParallel` cap, and eventually conflict policy) and hands the
engine one `transfer.Request` per running transfer. Engines own concurrency for
the work they are given but never queue. The contract that keeps this honest:
every accepted Request must produce exactly one terminal event (Completed,
Failed, or Canceled), or the UI's queue stalls waiting for a slot.

`remotefs.FS` has not had the same treatment yet and still returns
`[]domain.Entry` with no error, synchronously. That is fine for `fakefs` and
wrong for a real network filesystem: a blocking `List` freezes the TUI and
there is nowhere to report a timeout or a permission error. Give it the same
command/message shape as `transfer.Engine` before writing a real adapter.

## Suggested Next Steps

1. ~~Initialize or fix Git repository state.~~ Done — real repo on `main`,
   pushed to `git@github.com:allisonhere/TideFTP.git`, `-buildvcs=false` no
   longer needed.

2. ~~Extract a remote filesystem interface.~~ Done — `internal/remotefs.FS`
   defines `List`/`Child`/`Parent`; `fakefs.Remote` implements it;
   `internal/ui` depends only on the interface and takes a `remotefs.FS` via
   `NewModel(remote)`; `main.go` constructs the concrete `fakefs.NewRemote()`
   adapter.

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

6. Make `remotefs.FS` asynchronous and error-returning, mirroring
   `transfer.Engine`. Every call site is in `internal/ui/model.go`
   (`refreshRemote`, `setRemotePath`, `parentDir`, `activateCursor`).
   Do this before step 7, not after.

7. Add protocol adapters.
   - Start with SFTP (`pkg/sftp`): it is testable in-process, unlike FTP,
     which needs a real daemon.
   - Implement both `remotefs.FS` and `transfer.Engine`.
   - The fake adapters and their tests are the contract that proves the UI
     did not regress while real adapters land.

8. Expand TUI tests.
   - Snapshot/golden views for main screen and overlays
   - Key routing tests for focus, selection, tabs, and modals
   - Resize tests for small terminals
   - Transfer queue state tests

## Known Gaps

- No real FTP/FTPS/SFTP networking yet; `faketransfer` moves no bytes, it
  only emits a plausible event stream on a timer
- `remotefs.FS` is still synchronous and cannot report errors (see Design
  Notes) — the one seam that is not ready for a real adapter
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
