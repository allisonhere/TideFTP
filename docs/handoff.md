# TideFTP Handoff

## Current State

TideFTP is a Go terminal file transfer client in
`/home/allieb/Projects/tideftp`.
The UI shell is polished, and real SFTP/FTP/FTPS adapters now sit behind the
same interfaces as the fakes — see **Implemented** below.

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
- Connection lifecycle (`internal/session`) with a simulated dialer under
  `internal/fakesession`: connect, disconnect, reconnect, connect failure,
  and dropped connections
- Real SFTP under `internal/sftpsession` and real FTP/FTPS under
  `internal/ftpsession`, reachable with `--protocol`/`--host`, and verified
  against the LAN servers described under **Test Servers**
- Bottom tabs: Queue, Active, Failed, History, Log
- Theme picker on `t`
- Connect, help, and conflict modals
- Editable connect form (`c`) over Protocol/Host/Port/Username/Path, styled
  like whatthedock's soft forms (see **Connect form**)
- Keyboard-driven navigation plus initial mouse focus/select behavior
- Shift-arrow pane resizing
- Config persistence: XDG paths plus a `config.toml` under `internal/config`,
  loaded on startup and saved on change (see **Config persistence**)
- Basic UI tests for layout, theme registration, and resizing

Git repository state:

- Initialized as a normal Git repository on `main`, pushed to
  `git@github.com:allisonhere/TideFTP.git`. The old placeholder `.git`
  directory is gone, so `-buildvcs=false` and the project-local Go caches
  are no longer needed.

## Run And Verify

Run the TUI:

```bash
cd /home/allieb/Projects/tideftp
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

See **Test Servers** for the live-test invocations against real servers.

Driving the built binary from a synthetic PTY has not worked: the process
renders, but plain rune keypresses written to the pty master never reach it
(Ctrl+C arrives only as SIGINT from the line discipline). This reproduces on
older commits too, so it is a harness problem, not a regression — but it means
there is still no automated smoke test of the real binary. Manual runs are the
only coverage there.

## Test Servers

A LAN-only box runs the servers the real adapters are verified against. It is
not reachable from outside the network, and the credentials below are
throwaway ones for it.

**Host `192.168.86.52`, user `ftp_test`, password `ftp-test-2026` for every
service below.**

### FTPS — port 21, verified working

- Explicit TLS / STARTTLS (`AUTH TLS`), passive ports `21000`–`21010`
- vsftpd, via the `delfer/alpine-ftp-server` image
- Home directory `/ftp/ftp_test`; test files persist in `ftp-test-server/data`

Plain FTP no longer works on this box for this account: enabling FTPS turned on
`force_local_logins_ssl`, so a plaintext login is refused with "Non-anonymous
sessions must use encryption." The adapter still supports plain FTP; only the
server changed.

Quirks, each of which cost real debugging time:

- `AUTH TLS` is **not** advertised in `FEAT` even though it works. `PBSZ` and
  `PROT` are the tell. Do not autodetect FTPS from `FEAT`.
- No `MLSD`, so listings come from `LIST` parsing.
- The certificate is self-signed, `CN=192.168.86.52` with an IP SAN, so it
  verifies properly once trusted. Fetch it with:
  `openssl s_client -connect 192.168.86.52:21 -starttls ftp -showcerts`
- vsftpd requires the data connection to resume the control connection's TLS
  session (`require_ssl_reuse`); see the FTPS design note.
- TLS 1.3 uploads fail at exactly 16384 bytes and above; see the same note.

### SFTP — port 2222, connects but `/upload` is unreadable

- OpenSSH 8.4p1 Debian, chrooted: the account sees only `/`, containing
  `upload`
- `/upload` is `drwxr-x---` and `ftp_test` is neither its owner nor in its
  group, so listing it returns "permission denied". **This needs a `chown` or
  group grant on the server** before round-trip tests can run against it.
  Everything up to that point works: password auth, host key verification, and
  listing `/`.

The host key must be in a `known_hosts` file; get one with
`ssh-keyscan -p 2222 192.168.86.52`. That is trust-on-first-use, which is fine
for a box on your own LAN but is not a pattern to carry into the app — it
verifies strictly and has no accept-once flow.

### Running the live tests

```bash
# FTPS
TIDEFTP_TEST_FTP_ADDR=192.168.86.52:21 \
TIDEFTP_TEST_FTP_USER=ftp_test \
TIDEFTP_TEST_FTP_PASSWORD=ftp-test-2026 \
TIDEFTP_TEST_FTP_PATH=/ftp/ftp_test \
TIDEFTP_TEST_FTP_TLS=1 \
TIDEFTP_TEST_FTP_CA=/path/to/server-cert.pem \
go test -run Live ./internal/ftpsession

# SFTP
TIDEFTP_TEST_SFTP_ADDR=192.168.86.52:2222 \
TIDEFTP_TEST_SFTP_USER=ftp_test \
TIDEFTP_TEST_SFTP_PASSWORD=ftp-test-2026 \
TIDEFTP_TEST_SFTP_PATH=/upload \
TIDEFTP_TEST_SFTP_KNOWN_HOSTS=/path/to/known_hosts \
go test -run Live ./internal/sftpsession
```

Both suites skip unless their `_ADDR` variable is set, so `go test ./...` stays
green off the LAN.

### Running the app against them

```bash
TIDEFTP_FTP_PASSWORD=ftp-test-2026 go run ./cmd/tideftp --protocol ftps \
  --host 192.168.86.52 --user ftp_test --path /ftp/ftp_test \
  --ftps-ca /path/to/server-cert.pem

TIDEFTP_SFTP_PASSWORD=ftp-test-2026 go run ./cmd/tideftp --protocol sftp \
  --host 192.168.86.52 --port 2222 --user ftp_test --path / \
  --known-hosts /path/to/known_hosts
```

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
- `internal/session/session.go`: `Target`, `Conn`, and `Dialer` — the
  lifecycle the two adapter seams live inside. A `Conn` hands out a `vfs.FS`
  and a `transfer.Engine` that are valid only while it is
- `internal/fakesession/fakesession.go`: simulated dialer, implements
  `session.Dialer`; succeeds only for hosts it was told about, so the
  connect-failure path is reachable, and `Conn.Drop` simulates a server
  going away
- `internal/config/`: XDG path resolution and the `config.toml` schema.
  `Default`/`Load`/`Save` (atomic write) plus a `SaveFunc` seam the UI calls
  so it never touches the filesystem. The state and cache roots are resolved
  but unused
- `internal/sftpsession/`: the real SFTP adapter. `sftpsession.go` dials
  (agent and key-file auth, strict known_hosts), `fs.go` implements
  `vfs.FS`, `engine.go` implements `transfer.Engine`. All three share one
  `sftp.Client`, which is safe for concurrent use
- `internal/sftpsession/testserver_test.go`: a real SSH server with pkg/sftp's
  server half, on a loopback listener rooted at a temp directory. The adapter
  is tested against genuine protocol traffic, with no external sshd
- `internal/ftpsession/`: the real FTP/FTPS adapter, over jlaffaye/ftp.
  `pool.go` is the part that differs from SFTP and drives the package
- `internal/ui/model.go`: Bubble Tea state, update loop, listing requests and
  replies, transfer queue, key/mouse routing; depends only on `vfs.FS` and
  `transfer.Engine`, never on a concrete adapter
- `internal/ui/view.go`: custom two-over-one layout, panes, overlays, status bars, transfer rows
- `internal/ui/connect_form.go`: the editable connect form (Protocol/Host/Port/
  Username/Path) and its key grammar, styled like whatthedock's soft forms
- `internal/ui/themes.go`: app theme registration, including `tide-night`
- `internal/ui/model_test.go`: UI behavior tests, driven by a hand-scripted
  `transfer.Engine` stub so they never depend on goroutine timing
- `internal/ui/integration_test.go`: real model plus real `faketransfer`
  engine, pumping actual events end to end
- `README.md`: quick run instructions and keybindings

## Design Notes

### FTP connections are borrowed, not shared

An SSH connection multiplexes, so `internal/sftpsession` shares one
`sftp.Client` between both panes and every transfer. An FTP control connection
does not: it carries one command at a time, so a listing and two running
transfers each need their own. `internal/ftpsession/pool.go` caps how many
exist and lends them out, which also keeps the count inside the range of data
ports a passive-mode server publishes.

Two consequences worth remembering. The pool's cap has to cover the UI's
transfer parallelism *plus* browsing, or a listing waits behind transfers. And
a connection whose command failed or was cancelled is discarded rather than
returned: its control stream may be mid-response, and reusing it corrupts every
later command.

FTP also has no equivalent of `ssh.Client.Wait`, so a dropped connection is
only noticed by trying to use one. `Conn.keepalive` sends a periodic `NOOP`;
if the pool is too busy to lend a connection for it, that is itself evidence
the connection is alive and the tick is skipped.

### FTPS

Three things had to be right before an upload worked against vsftpd, and each
is a trap worth knowing:

- **Session reuse.** vsftpd defaults to `require_ssl_reuse=YES`: the data
  connection must resume the control connection's TLS session or it answers
  522 "session reuse required". jlaffaye/ftp has no option for it, but it
  hands the same `tls.Config` to both connections, so a `ClientSessionCache`
  lets crypto/tls resume by itself.
- **TLS 1.3.** Uploads over a TLS 1.3 data connection fail at exactly 16384
  bytes and above with 426 "Failure reading network stream"; anything smaller
  succeeds, and TLS 1.2 works at every size. curl over TLS 1.3 is fine, so
  this is crypto/tls against vsftpd's data connection handling, not something
  fixable here. FTPS is capped at TLS 1.2 (`DefaultMaxTLSVersion`), which
  `Config.MaxTLSVersion` and `--ftps-allow-tls13` can lift.
- **Self-signed certificates.** `--ftps-ca` trusts a PEM in addition to the
  system roots, which is the way to use one. `--ftps-insecure` exists and
  accepts anything; it is off by default and should stay a lab-only escape
  hatch.

`AUTH TLS` is not advertised in vsftpd's `FEAT` even when it works, so FTPS is
a setting rather than something to autodetect.

### Live tests

Both real adapters have live tests that skip unless their address variable is
set, so `go test ./...` stays green anywhere. They complement the hermetic
tests rather than replacing them: SFTP has a full in-process server, FTP does
not yet. See **Test Servers** above for the variables and the box to run them
against.

### Contrast

tideui runs every foreground in `BuildStyles` through its own `readableText`,
but that helper is **unexported**, and v0.2.2 is the latest release. Anywhere
`internal/ui` picks a colour of its own — directory and symlink names, transfer
status, the progress bar — it bypassed tideui's styles entirely and wrote a raw
theme colour onto whatever background the row had. Selected and marked rows
change that background, and nothing checked the result: 304 theme/row/kind
combinations failed the 4.5 floor, the worst being a directory name painted in
exactly the marked-row background colour.

`internal/ui/contrast.go` mirrors tideui's maths (same WCAG relative luminance,
same 4.5 and 3.0 thresholds) so that this package can apply the same check.
**It is a duplicate, and should not stay one**: exporting `readableText` from
tideui turns `readableOn` into a one-line call and deletes the rest of the
file. Until then, keep the two in step.

Colours are resolved in `entryPalette` and `transferPalette` rather than inline,
so `TestEntryRowColoursAreReadable` and `TestTransferRowColoursAreReadable` can
check every theme against every row state directly. Any new coloured text must
go through `readableOn` against the background it is actually painted on, and
gain a case in those tests.

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

Nothing in the UI holds an adapter it was handed at startup any more. `vfs.FS`
and `transfer.Engine` both come from a live `session.Conn` and are cleared with
it, because the alternative — a stale adapter still wired in after the
connection ended — is the failure mode that produces confusing bugs later. The
local pane is the exception: `localfs` needs no connection and keeps working
while the remote side is down.

Four things the lifecycle has to get right, all in `model.go`:

- **A late connection is closed, not used.** If the user moves on while a dial
  is in flight, the `Conn` that eventually arrives is closed rather than wired
  in, or it leaks a live connection nobody can reach.
- **A stale disconnect is ignored.** Each connection has its own watcher, so an
  old one reporting in must not tear down the connection that replaced it.
  Both checks compare against the current `conn`/`target`.
- **In-flight transfers fail on a drop.** The bytes stopped moving whether or
  not the user asked them to, so queued and active rows are marked Failed with
  the reason. Finished rows are left alone.
- **Transfers refuse to start while disconnected.** `startQueuedTransfers` is
  guarded as well as `queueUpload`/`queueDownload`, since a queue can outlive
  the connection that filled it.

Both adapter seams are asynchronous, but in different shapes, because the jobs
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

### Config persistence

`internal/config` owns the three XDG roots and a small `Config` struct
serialised to `~/.config/tideftp/config.toml` (go-toml/v2). `Load` layers the
file over `Default`, so a missing, corrupt, or hand-edited file falls back to
defaults rather than crashing; `Save` writes atomically (temp file + rename)
and creates the directory first.

The UI never touches the filesystem. `NewModel` takes the loaded `Config` plus
a `config.SaveFunc`, and applies what it can — theme, density, shadow, icons,
`maxParallel`, and the two pane splits (rebuilt through `PaneRatio`, which
clamps out-of-range values back into bounds). `persist()` calls that func after
each user change: theme confirm, icon toggle, the four shift-arrow resizes, and
the layout reset. The save func is the seam that keeps this testable — UI tests
pass `nil` and persistence is skipped, while main wires it to `config.Save`.

Three scope calls to remember:

- `config.Default()` must stay in step with the values `NewModel` used to
  hardcode, or a first run would differ from a later saved run. The two
  cross-reference in comments.
- The file is written on the first change, not on first launch: a run that
  changes nothing leaves no config file behind.
- `showHidden` is per-pane and deliberately not persisted here; the
  `[profiles]` table is item 4 and the schema is shaped to take it.

### Connect form

`internal/ui/connect_form.go` replaces the old connect *menu* with an editable
form that dials a `session.Target`. It is styled like whatthedock's soft forms
— `SoftPanel` + `RenderSoftRow` (label left, value right, the selected row
highlighted) + `RenderSoftHints` — with the same inline `|` caret and key
grammar: `tab`/`up`/`down` move fields, `h`/`l`/`left`/`right` cycle the
Protocol picker or move the caret, `ctrl/alt+enter` connects, `ctrl+d`
disconnects a live connection, `ctrl+u` clears a field. `alt+enter` is the
reliable confirm — a terminal cannot distinguish `ctrl+enter` from plain
`enter` — so the hint names both but the handler keys on `alt+enter`.

tideui was bumped from v0.2.2 to the pseudo-version whatthedock pins
(`v0.2.3-0.20260820020614-441c283e776f`) for two things the older release
lacks: `SoftRow`'s selected-row background highlight and the `ModalShadow`
style option. The app still composites overlays itself (`overlayOnBase`), so
`ModalShadow` is unused here — the drop shadow keeps coming from the app's own
path.

## Suggested Next Steps

1. ~~Initialize or fix Git repository state.~~ Done — real repo on `main`,
   pushed to `git@github.com:allisonhere/TideFTP.git`, `-buildvcs=false` no
   longer needed.

2. ~~Extract a filesystem interface.~~ Done — `internal/vfs.FS` defines
   `List`/`Child`/`Parent`, asynchronous and error-returning;
   `localfs.FS` and `fakefs.Remote` implement it; `internal/ui` takes both
   via `NewModel(local, remote, engine)` and never imports an adapter.

3. ~~Add real layout/config persistence.~~ Done — `internal/config` resolves
   the XDG paths (config/state/cache) and persists theme, density, shadow,
   icons, `maxParallel`, and the pane splits to
   `~/.config/tideftp/config.toml` (go-toml/v2), loaded on startup and saved
   on change. The state and cache roots are resolved but unused; see
   **Config persistence** under Design Notes.

4. Build profile model and connect form.
   - ~~Editable form.~~ Done — `internal/ui/connect_form.go` is a
     whatthedock-styled form over Protocol/Host/Port/Username/Path, opened
     with `c`; it dials a `session.Target` and replaces the old hardcoded
     target menu.
   - `--host/--user/--port/--path/--identity/--known-hosts` still reach a real
     server; the form is the interactive path into the same dialers
   - Still missing: profile persistence (a `[profiles]` table in config.toml),
     and everything credential-related — password mode (prompt/keyring/config),
     SFTP agent/key file, FTPS certificate verification, SFTP known-host mode

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
   - ~~SFTP.~~ Done — `internal/sftpsession`. Parallel transfers share one
     `sftp.Client`; if that turns out to throttle, giving each transfer its
     own client is the next thing to try.
   - ~~FTP and FTPS.~~ Done — `internal/ftpsession`, both verified against a
     real vsftpd server.
   - The fake adapters and their tests are the contract that proves the UI
     did not regress while real adapters land.

7. Expand TUI tests.
   - Snapshot/golden views for main screen and overlays
   - Key routing tests for focus, selection, tabs, and modals
   - Resize tests for small terminals
   - Transfer queue state tests

## Known Gaps

- SFTP, FTP, and FTPS all work and are verified against real servers.
  `faketransfer` moves no bytes, it only emits a plausible event stream
- `internal/ftpsession` has no hermetic tests: its unit tests cover listing
  conversion, paths, and the pool, but everything protocol-level needs the
  live server. SFTP has an in-process server and FTP should get one too
- The SFTP test server's `/upload` is not readable by `ftp_test`, so the SFTP
  live tests cannot do a round trip until that is fixed on the server (see
  **Test Servers**)
- Passwords come from the environment (`TIDEFTP_FTP_PASSWORD`,
  `TIDEFTP_SFTP_PASSWORD`), never a flag. A passphrase-protected SSH key is
  reported rather than prompted for; real credential handling waits on the
  connect form
- Host keys are verified strictly against known_hosts, with no ask or
  accept-once flow: both need a prompt that does not exist yet. A missing
  known_hosts fails the connection closed, which is deliberate
- Transfers overwrite the destination. Resume and the rest of the conflict
  policy are not wired to the UI, so nothing can ask for anything else
- Folders are skipped when queuing, with a message, since recursive
  transfers do not exist and a real adapter cannot read a directory as a file
- Cancelling a transfer closes its file handles to interrupt a parked read.
  A listing parked on a dead connection is only abandoned, not interrupted:
  pkg/sftp has no context-aware API, so the request occupies the connection
  until the server answers or the connection closes
- All three seams (`session.Dialer`, `vfs.FS`, `transfer.Engine`) now have
  both a fake and a real implementation, which is the evidence they are the
  right shape
- The connect form has no credential fields yet: password, SFTP agent/key
  file, FTPS certificate, and known-host mode all wait on credential handling
  (passwords still come from the environment)
- No credential handling of any kind: nothing prompts, stores, or sends a
  password, and `fakesession` needs none
- A connection is never retried automatically after a drop
- A listing that hangs is bounded only by `listTimeout` (20s) and cannot be
  cancelled from the UI — there is no key to abandon a slow directory
- Config persistence covers only global prefs (theme, density, shadow, icons,
  `maxParallel`, and the pane splits). Profiles, `showHidden`, and any use of
  the state/cache directories are not persisted yet
- No saved profiles yet
- No real credential storage yet
- The connect form is keyboard-only; mouse clicks do not edit its fields
- Mouse support is basic: focus/select, not full range selection or context
  menus. The click-to-row mapping depends on the chrome above the file panes
  (`firstFileRow` in `internal/ui/model.go`) and on `topPaneHeight`, which
  `View` and the hit test must keep sharing.
  `TestFirstFileRowMatchesTheRenderedLayout` pins `firstFileRow` to what the
  view actually draws, across several terminal sizes — keep it passing rather
  than adjusting the constant by hand.
  Every row in the panes is drawn at a fixed width, and a row longer than that
  width wraps onto a second line, shifting everything below it. The final view
  is clamped to the terminal height, so a wrap is invisible: it pushes the
  status bar off the bottom instead of showing up. `fitRow` and `align` exist
  to stop that, and any new fixed-width row should go through one of them
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
