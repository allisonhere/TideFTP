# TideFTP Handoff

## Current State

TideFTP is a new Go terminal file transfer client in `/home/allieb/Projects/tideftp`.
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
- Simulated uploads/downloads and transfer progress
- Bottom tabs: Queue, Active, Failed, History, Log
- Theme picker on `t`
- Connect, help, and conflict modals
- Keyboard-driven navigation plus initial mouse focus/select behavior
- Shift-arrow pane resizing
- Basic UI tests for layout, theme registration, and resizing

Important caveat:

- The project directory currently contains a placeholder `.git` directory that is
  not a real Git repository. Because of that, `git status`, `git diff --check`,
  and normal Go VCS stamping fail. Use `-buildvcs=false` until the project is
  initialized as a normal repository or the placeholder is removed.

## Run And Verify

Use project-local Go caches because the global Go cache paths were read-only in
the Codex sandbox during setup.

Run the TUI:

```bash
cd /home/allieb/Projects/tideftp
GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOSUMDB=off go run -buildvcs=false ./cmd/tideftp
```

Verification commands used:

```bash
GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOSUMDB=off go test -count=1 ./...
GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOSUMDB=off go vet ./...
GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOSUMDB=off go build -buildvcs=false -o /tmp/tideftp ./cmd/tideftp
```

Last known result:

- `go test -count=1 ./...`: passed
- `go vet ./...`: passed
- `go build -buildvcs=false -o /tmp/tideftp ./cmd/tideftp`: passed

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

- `cmd/tideftp/main.go`: Bubble Tea program entrypoint with alt screen and mouse enabled
- `internal/domain/domain.go`: shared entry and transfer types
- `internal/fakefs/fakefs.go`: fake remote directory tree
- `internal/ui/model.go`: Bubble Tea state, update loop, fake transfer simulation, key/mouse routing
- `internal/ui/view.go`: custom two-over-one layout, panes, overlays, status bars, transfer rows
- `internal/ui/themes.go`: app theme registration, including `tide-night`
- `internal/ui/model_test.go`: UI behavior tests
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

Do not wire real protocol complexity into the UI package directly. The next
major step should introduce a protocol-facing interface and move fake remote
behavior behind that interface.

## Suggested Next Steps

1. Initialize or fix Git repository state.
   - Remove the placeholder `.git` directory or replace it with a normal repo.
   - After that, remove the need for `-buildvcs=false`.

2. Extract a remote filesystem interface.
   - Keep UI code protocol-agnostic.
   - Move fake remote into an adapter that satisfies the same interface planned
     for FTP/FTPS/SFTP.

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
   - Configurable parallelism
   - Completed-transfer aging into History
   - Retry failed transfers
   - Cancel active or queued transfers
   - Recursive folder preflight summary

6. Add protocol adapters.
   - Start with SFTP or FTP/FTPS depending on available libraries and testability.
   - Use fake adapter tests to preserve UI behavior while real adapters land.

7. Expand TUI tests.
   - Snapshot/golden views for main screen and overlays
   - Key routing tests for focus, selection, tabs, and modals
   - Resize tests for small terminals
   - Transfer queue state tests

## Known Gaps

- No real FTP/FTPS/SFTP networking yet
- No config persistence yet
- No saved profiles yet
- No real credential storage yet
- Mouse support is basic: focus/select, not full range selection or context menus
- No recursive transfer summary yet
- Conflict modal is visual/simulated, not wired to transfer policy yet
- Completed transfers do not yet age out of Queue into History
- Failed transfer retry flow is not implemented yet
- No screenshot/PTY visual QA has been run yet
