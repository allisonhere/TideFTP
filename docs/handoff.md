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
- SFTP auth choice and a password field: Auth cycles agent/key vs password
  for SFTP (FTP/FTPS always show Password, since they have nothing else);
  never persisted, only ever passed to that one connect attempt (see
  **Connect form**)
- SFTP Identity/Known Hosts and FTPS Verify/CA File fields: everything that
  used to be CLI-flag-only (`--identity`, `--known-hosts`, `--ftps-ca`,
  `--ftps-insecure`) now has a form field too, shown only when it applies
  and overriding the flag-configured default for that one attempt (see
  **Connect form**)
- Keyboard-driven navigation plus initial mouse focus/select behavior
- Shift-arrow pane resizing
- Config persistence: XDG paths plus a `config.toml` under `internal/config`,
  loaded on startup and saved on change (see **Config persistence**)
- Saved connection profiles: a `Profile` field in the connect form cycles
  through profiles persisted in `config.toml`, `ctrl+s`/`ctrl+x` save and
  delete them (see **Connect form**)
- Opt-in credential storage: a Remember field next to Password, backed by
  the OS keyring (`internal/credstore`) — a saved profile's password is
  remembered only if Remember was "yes" when it was saved with `ctrl+s`
  (see **Connect form**)
- Settings overlay on `,` (`internal/ui/settings.go`): Theme/Density/Shadow/
  Icons/Max Parallel in one flat, cursor-driven list, styled like
  whatthedock's own settings screen — each row applies and persists live on
  `h`/`l`, the same as pressing that setting's existing standalone key
  (`i`, `+`/`-`) already did. Theme cycles live too, one at a time,
  without leaving the overlay; `enter` on it opens the same picker `t`
  does for the full browse/search/preview experience
- Basic UI tests for layout, theme registration, and resizing
- SFTP host-key ask/accept-once flow: an SSH host key with no known_hosts
  entry — including a missing known_hosts file, now auto-created empty
  rather than a hard failure — surfaces as an `overlayHostKey` confirm
  overlay (fingerprint plus algorithm) instead of failing the connect
  outright. `y` trusts it for this one connection, `r` trusts and remembers
  it (appends to known_hosts), `n`/`esc` cancels. A key that mismatches an
  *already-known* host still always fails closed, unconditionally — this
  flow only ever applies to a host with no entry at all (see **Host key
  trust**)
- Real conflict resolution (see **Conflict resolution**): queuing a file now
  checks its destination, and any that already exists opens `overlayConflict`
  with the full FileZilla-style option list from Product Decisions —
  Overwrite / Overwrite if source newer / Overwrite if different size /
  Overwrite if different size or source newer / Resume / Rename / Skip.
  All three product-decision scopes are real: `enter` resolves just the one
  conflicting file shown and advances to the next (**this file**), `a`
  applies the choice to every remaining conflict in the batch at once
  (**current queue**), `s` does that and also remembers it for later
  batches (**this session**). Resume is real: both `internal/sftpsession`
  and `internal/ftpsession` now move bytes starting from an offset instead
  of always truncating the destination
- A Stats tab (`6`, see **Stats tab**) alongside Queue/Active/Failed/
  History/Log: a live snapshot line, a realtime throughput graph — a
  smoothed, connected Unicode Braille line plot, gradient-colored by
  intensity with a highlighted peak, on a fixed black background
  regardless of theme — session totals, averages over completed transfers,
  and a per-protocol breakdown. Sampling runs on a 1-second tick — the
  first periodic ticker anywhere in `internal/ui` — only while the tab is
  open

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

- `cmd/tideftp/main.go`: Bubble Tea program entrypoint. `buildSession` wires
  the demo fakes when no `--host` is given; otherwise it builds all three
  real protocol dialers (SFTP, FTP, FTPS) and wraps them in a
  `router.Dialer`, since the connect form can pick any of them per attempt
  regardless of `--protocol` (see **Protocol routing**)
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
  implement. Asynchronous by contract: `Start` returns immediately and
  everything afterwards arrives as a `transfer.Event`
- `internal/transfer/runner.go`: `Runner` implements that whole interface —
  starting, canceling, closing, exactly-one-terminal-event — over a
  protocol-supplied `MoveFunc` that only knows how to move one transfer's
  bytes. `internal/sftpsession` and `internal/ftpsession` each embed a
  `*Runner` in their `Engine` rather than reimplementing the bookkeeping
  (see **Shared transfer runner**)
- `internal/faketransfer/faketransfer.go`: simulated engine, implements
  `transfer.Engine`; emits timed progress events and fails every fifth
  transfer so the Failed tab stays reachable
- `internal/session/session.go`: `Target`, `Conn`, `Dialer`, and
  `Credentials` — the lifecycle the two adapter seams live inside. A `Conn`
  hands out a `vfs.FS` and a `transfer.Engine` that are valid only while it
  is. `Credentials` is `Dial`'s other parameter: unlike `Target` it is never
  persisted, since it is how a single connect attempt authenticates rather
  than where to connect and as whom
- `internal/fakesession/fakesession.go`: simulated dialer, implements
  `session.Dialer`; succeeds only for hosts it was told about, so the
  connect-failure path is reachable, and `Conn.Drop` simulates a server
  going away
- `internal/config/`: XDG path resolution and the `config.toml` schema.
  `Default`/`Load`/`Save` (atomic write) plus a `SaveFunc` seam the UI calls
  so it never touches the filesystem. The state and cache roots are resolved
  but unused. `Profile` mirrors `session.Target`'s shape (minus credentials)
  rather than importing it, keeping this package free of that dependency;
  `internal/ui` converts between the two
- `internal/credstore/`: `Store` (`Get`/`Set`/`Delete` by an opaque key) plus
  `Keyring`, the real implementation over `github.com/zalando/go-keyring`
  (macOS Keychain, Windows Credential Manager, Linux Secret Service). Lets a
  saved profile's password be remembered opt-in, per profile — see the
  Remember design note under **Connect form**
- `internal/fakecredstore/fakecredstore.go`: in-memory `Store` for tests,
  with an `Err` field to exercise what the UI does when the keyring is
  unavailable
- `internal/sftpsession/`: the real SFTP adapter. `sftpsession.go` dials
  (agent, key-file, and password auth, strict known_hosts), `fs.go`
  implements `vfs.FS`, `engine.go` wraps a `transfer.Runner` with the
  SFTP-specific `move`/`open`. All three share one `sftp.Client`, which is
  safe for concurrent use
- `internal/sftpsession/testserver_test.go`: a real SSH server with pkg/sftp's
  server half, on a loopback listener rooted at a temp directory. The adapter
  is tested against genuine protocol traffic, with no external sshd
- `internal/ftpsession/`: the real FTP/FTPS adapter, over jlaffaye/ftp.
  `pool.go` is the part that differs from SFTP and drives the package;
  `engine.go` wraps a `transfer.Runner` with the FTP-specific
  `move`/`download`/`upload`
- `internal/router/router.go`: `Dialer` picks a protocol-specific
  `session.Dialer` by `Target.Protocol` (see **Protocol routing**)
- `internal/ui/model.go`: Bubble Tea state, update loop, listing requests and
  replies, transfer queue, key/mouse routing; depends only on `vfs.FS` and
  `transfer.Engine`, never on a concrete adapter
- `internal/ui/view.go`: custom two-over-one layout, panes, overlays, status bars, transfer rows
- `internal/ui/connect_form.go`: the editable connect form (Profile/Name/
  Protocol/Host/Port/Username/Auth/Password/Identity/Known Hosts/Verify/CA
  File/Path) and its key grammar, styled like whatthedock's soft forms
- `internal/ui/settings.go`: the settings overlay (Theme/Density/Shadow/
  Icons/Max Parallel), a flat cursor-driven row list — the same
  field-enum-plus-label/value-functions shape `connect_form.go` uses,
  scaled down since every row is a fixed, always-visible cycle
- `internal/ui/conflict.go`: the conflict overlay's policy enum/labels, key
  handling (`handleConflictKey`), and `commitScan` — the one place a
  resolved `preflightScan` becomes queued transfers (see **Conflict
  resolution**)
- `internal/ui/stats.go`: the Stats tab's data model (`statsSnapshot`),
  1-second ticking (`applyStatsTick`), aggregation (`computeStats`), and
  the throughput graph renderer (`renderThroughputLine`) — see **Stats
  tab**
- `internal/ui/themes.go`: app theme registration, including `tide-night`
- `internal/ui/model_test.go`: UI behavior tests, driven by a hand-scripted
  `transfer.Engine` stub so they never depend on goroutine timing
- `internal/ui/integration_test.go`: real model plus real `faketransfer`
  engine, pumping actual events end to end
- `internal/ui/golden_test.go` + `internal/ui/testdata/*.golden`:
  ANSI-stripped full-frame snapshots of the main screen and every overlay,
  over a fixed-timestamp fixture (`goldenModel`) so they never drift day to
  day; `-update` regenerates them
- `internal/ui/key_routing_test.go`: focus cycling, selection, and
  modal-vs-quit key routing
- `internal/ui/resize_test.go`: panic smoke tests across tiny/degenerate
  terminal sizes
- `README.md`: quick run instructions and keybindings

## Design Notes

### Protocol routing

`model.dialer` is one `session.Dialer` — always was, and still is a single
field with a single interface value. What changed is what that value is. It
used to be whichever concrete adapter `--protocol` picked at startup, which
meant the connect form's Protocol field was partly cosmetic: cycling it to a
different protocol and connecting dialed the new target through the old
adapter (an FTP target handed to the SFTP client, say), silently, since
`fakesession` — the only dialer exercised by hand without a real server —
ignores `Target.Protocol` entirely and so never surfaced the bug.

`internal/router` fixes this by being the thing `model.dialer` actually
holds when a real server is in play: `router.Dialer` wraps one real
`session.Dialer` per protocol and picks between them by `Target.Protocol` on
every call. `buildSession` in `cmd/tideftp/main.go` builds all three real
adapters unconditionally now — not just the one `--protocol` names — because
the form can reach any of them for any target, not only the one flags
describe. `--protocol` still matters, just for less: it is now only what the
one flag-described target uses to auto-connect at startup; every other
target the form dials goes through whatever Protocol field it was given.

FTP and FTPS are two separate `*ftpsession.Dialer` instances in the routing
map — `ExplicitTLS` is a `Config` field fixed at construction, not a per-Dial
parameter, so there is no single FTP dialer that could serve both. This is
safe because `ftpsession.Dialer` is stateless: `Dial` builds a fresh pool per
call, so nothing is shared between the two instances that construction-time
config could leak across.

Building three real adapters up front also meant relaxing a check: `--host`
with `ftp`/`ftps` used to hard-fail at startup if `TIDEFTP_FTP_PASSWORD` was
unset, because a password was the only way FTP could ever authenticate. Now
the connect form's Password field is another way, supplied per attempt (see
**Connect form**), so failing the whole process before the form ever opens
would foreclose the case it exists for. A target with no password anywhere
still fails to dial — just asynchronously, as a `connectFailedMsg` the user
can react to by opening the form, rather than a startup error.

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

`MKD`, unlike SFTP's `MkdirAll`, only ever creates the one directory it is
given — it fails if that directory's own parent does not exist yet. An
upload's destination directory used to get exactly one `MakeDir` call, which
is enough when the parent already exists but silently fails to create
anything for a destination nested two or more levels under directories that
do not exist, where SFTP's `MkdirAll` would succeed. `makeDirAll` in
`engine.go` walks the path from the root down, `MakeDir`-ing each level —
mirroring what `MkdirAll` does for SFTP.

`FS.List` runs `conn.List` on its own goroutine, since jlaffaye/ftp's calls
are blocking with no context of their own, and races that goroutine against
`ctx.Done()` to decide how long to wait. The result channel must be sent to
exactly once, unconditionally — not selected against another case on the
send side, which an earlier version of this did (`select { case done <-
result{...}: case <-abandoned: discard }` inside the goroutine, racing a
*buffered* send that could never actually block against an abandonment
signal). A buffered send is always ready, so that select could go either
way; picking the send left both the `ctx.Done()` branch in `List` (which had
already returned) and the goroutine done with nothing claiming the
connection — leaked, along with its pool slot, forever. The fix: the
goroutine always sends; `List` picks exactly one of "got the result" or
"gave up," and giving up spawns a cleanup goroutine that waits for the
result and discards the connection once it arrives, rather than trying to
race the decision at the point of giving up.

### Shared transfer runner

`internal/sftpsession/engine.go` and `internal/ftpsession/engine.go` used to
each implement the whole `transfer.Engine` interface from scratch:
`Start`/`Cancel`/`Close`/`run`/`canceled`/`emit`/`done`, the `running` map,
the `wg`/`closed`/`quit` bookkeeping, even the `progressInterval`/`copyChunk`
constants — identical between the two, because none of it is actually
protocol-specific. Only how to move one transfer's bytes differs: FTP splits
that into `download`/`upload` because `Stor` drains a reader itself rather
than being driven in a loop, where SFTP's `copy` handles both directions the
same way.

`transfer.Runner` (`internal/transfer/runner.go`) is that shared bookkeeping,
factored out once and used by both. It takes one `MoveFunc` — `func(req
Request, stop, quit <-chan struct{}, report func(int64)) (int64, error)` —
and implements the rest of `transfer.Engine` around it: starting, canceling,
closing, and translating the `MoveFunc`'s result into exactly one terminal
event (`Completed`, `Failed`, or `Canceled` when the error wraps
`transfer.ErrCanceled`), which is the contract the UI's queue depends on.
Each protocol's `Engine` is now just `*transfer.Runner` embedded alongside
whatever it needs to actually move bytes (an `*sftp.Client`, a `*pool`), plus
a `move` method passed to `transfer.NewRunner` as the `MoveFunc`. A fix to
cancellation or close ordering now lives in one place instead of needing to
be re-applied twice and — history being what it is — probably diverging.

`transfer.IsCanceled(stop, quit)` and the `ProgressInterval`/`CopyChunk`
constants moved to the `transfer` package alongside `Runner` for the same
reason: both engines' copy loops used identical logic, just copy-pasted.

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
- **In-flight transfers fail on a drop — including a reconnect the user asked
  for.** The bytes stopped moving whether or not the user asked them to, so
  queued and active rows are marked Failed with the reason; finished rows
  are left alone. This has to happen synchronously in `clearConnection`
  itself, not by waiting for the old connection's `disconnectedMsg`:
  `connect` calls `clearConnection` to tear down the previous connection
  *before* dialing the next one, and by the time that old connection's own
  disconnect message eventually arrives, `m.conn` already names the new
  connection — so the staleness check above would discard it, and anything
  it was carrying would never get marked Failed at all, staying Active
  forever. `applyDisconnected` calls `clearConnection` too, for the ordinary
  drop-or-user-disconnect path, so both routes funnel through the one place
  that actually does the failing.
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

`applyTransferEvent` clamps `BytesDone` to `BytesTotal` to keep a
misreported total from showing over 100% — but only when `BytesTotal` is
actually positive. A listing can report a size of 0 for a file that turns
out not to be empty (an FTP `LIST` parser that could not tell, say);
clamping to that 0 would pin the row's `BytesDone` at 0 for the whole
transfer, including the terminal `Completed` event, which used to overwrite
it with `BytesTotal` unconditionally. With an untrustworthy total, the row
now just tracks the engine's own count of bytes moved instead.

`domain.TransferStatus` has a `Canceled` value distinct from `Failed`, so a
transfer the user canceled can be told apart from one that genuinely broke —
`retrySelectedTransfer` and the row's own message both depend on it. It
still shares the Failed tab, though; there is no tab of its own.

### Recursive folders

Queuing a folder used to just refuse — `transfer.Engine` has no
folder-copy primitive, and no adapter can read a directory as a file. It
still can't, and never will: the fix is that the UI never asks it to.
`beginPreflightScan` walks every folder in the selection with `vfs.FS.List`
(the same blocking-by-contract call `listCmd` already wraps for one
directory, just looped depth-first over a stack instead) and flattens what
it finds into `preflightFile`s — plain `domain.Transfer`-shaped facts, one
per file, with nested source/destination paths built by chaining
`fs.Child` (pure path math, safe to call repeatedly). `transfer.Engine`
never sees a folder; by the time anything is queued, there is only ever a
flat list of individual file transfers, exactly like queuing files
directly always worked.

A plain-files-only selection is untouched — instant queue, no overlay,
same as before folders were supported at all. The moment the selection
contains any folder, though, the *whole* batch (including any plain files
alongside it) waits on one scan and one `overlayPreflight` confirm, rather
than queuing the files immediately and the folder's contents later: one
predictable path beats a partial one. `EntrySymlink` is queued as a leaf
during the walk, matching `entry.IsDir()`'s existing meaning elsewhere —
never followed, so a symlink cannot turn the walk into a cycle. A hard cap
(`preflightScanCap`, 5000 files) stops an enormous or pathological tree
from hanging the UI or ballooning memory; `preflightScan.truncated` marks
the result as a lower bound when it fires. If the scan finds nothing to
queue — every selected folder was empty and the selection had no plain
files — there's nothing to confirm, so it skips the overlay and reports
directly instead.

### Config persistence

`internal/config` owns the three XDG roots and a small `Config` struct
serialised to `~/.config/tideftp/config.toml` (go-toml/v2). `Load` layers the
file over `Default`, so a missing, corrupt, or hand-edited file falls back to
defaults rather than crashing; `Save` writes atomically (temp file + rename)
and creates the directory first.

The UI never touches the filesystem. `NewModel` takes the loaded `Config` plus
a `config.SaveFunc`, and applies what it can — theme, density, shadow, icons,
`maxParallel`, saved profiles, and the two pane splits (rebuilt through
`PaneRatio`, which clamps out-of-range values back into bounds). `persist()`
returns a `tea.Cmd` that calls the save func after each user change: theme
confirm, icon toggle, the four shift-arrow resizes, the layout reset, and
saving/deleting a connect-form profile. The save func is the seam that keeps
this testable — UI tests pass `nil` and persistence is skipped, while main
wires it to `config.Save`.

`persist()` builds the `Config` snapshot synchronously — so it always
captures the settings as they were at the moment of the change, not
whatever they happen to be by the time the command runs — but returns the
actual disk write as a command rather than calling `config.Save` inline.
`config.Save`'s mkdir, marshal, temp file, and rename are blocking disk I/O;
running them straight inside `Update` stalled the whole redraw loop on every
settings change, most visibly holding a shift-arrow resize key, which fires
a `persist()` on every repeat. Every call site changed to route through the
returned `tea.Cmd` — `cmd = m.persist()` inside `updateKey`'s switch,
`return m, m.persist()` where a case returns directly, same in
`connect_form.go`'s save/delete-profile handlers — rather than calling it
and discarding the result, which would silently turn persistence back into
a no-op.

Three scope calls to remember:

- `config.Default()` must stay in step with the values `NewModel` used to
  hardcode, or a first run would differ from a later saved run. The two
  cross-reference in comments.
- The file is written on the first change, not on first launch: a run that
  changes nothing leaves no config file behind.
- `showHidden` is per-pane and deliberately not persisted here. Saved
  connection profiles live in their own `[[profiles]]` array table via
  `config.Profile`, not in this struct — see **Connect form**.

### Connect form

`internal/ui/connect_form.go` replaces the old connect *menu* with an editable
form that dials a `session.Target`. It is styled like whatthedock's soft forms
— `SoftPanel` + `RenderSoftRow` (label left, value right, the selected row
highlighted) + `RenderSoftHints` — with the same inline `|` caret and key
grammar: `tab`/`up`/`down` move fields, `h`/`l`/`left`/`right` cycle a picker
field or move the caret, `ctrl/alt+enter` connects, `ctrl+d` disconnects a
live connection, `ctrl+u` clears a field. `alt+enter` is the reliable confirm
— a terminal cannot distinguish `ctrl+enter` from plain `enter` — so the hint
names both but the handler keys on `alt+enter`.

Profile is the first field, a picker like Protocol: `(new)` plus the label of
each saved profile. Cycling to a saved profile loads its Name/Protocol/Host/
Port/Username/Path into the rest of the form; cycling to `(new)` leaves them
alone, so it means "not tied to a saved profile" rather than "blank". Name is
free text, right below Profile; leaving it blank falls back to
`Target.Label()` (`user@host (protocol)`) rather than saving nothing. `ctrl+s`
saves the form's current values, upserting rather than always appending:
profiles are keyed by protocol+host+port+user (`targetKey`/`profileKey`), not
by name, so editing the path or renaming and re-saving the same account
updates that profile in place instead of piling up duplicates. `ctrl+x`
deletes the profile the Profile field currently points at; both are no-ops on
`(new)`, and both persist immediately through the same `save` seam as every
other setting (see **Config persistence**). Profiles carry no credentials,
matching `session.Target`'s doc comment that credentials
deliberately live elsewhere.

Every free-text field starts "fresh" when prefilled — from the current
target on open, or from a profile on cycling into it — and the first
character typed replaces its value rather than landing at the end of it, the
way selecting a form field's text before typing over it would.
`connectFormValue.fresh` tracks this per field; any other edit (backspace,
delete, `ctrl+u`) clears the flag, so a field only ever wipes itself on that
first keystroke, never mid-edit.

Auth and Password are conditionally shown, not always-present rows:
`connectFieldVisible` decides per field, and `moveConnectField` skips
whatever it hides rather than tabbing onto a row that is not drawn.
`connectAuthMode` is what visibility is built on: FTP and FTPS always
resolve to `"password"`, since they have no other method and so never show
Auth at all; SFTP resolves to whichever `connectAuthChoices` ("agent/key" /
"password") the Auth field is cycled to, and only then does Password become
visible. Password is masked in `connectFieldDisplay` (`•` per rune) whether
or not it is the focused field, since it is drawn every render pass once
visible, not only while being edited.

Password is deliberately the one field nothing else in the form treats like
the others: `openConnectForm` and `loadConnectProfile` never set it — a
profile has no password to load, by design — and `upsertProfile` never reads
it, so `ctrl+s` cannot accidentally write a plaintext password into
`config.toml`. It exists for exactly one `Dial` call. `credentialsFromForm`
turns the resolved auth mode into a `session.Credentials` — Password, and for
SFTP a `PasswordOnly` flag — and `connectFromForm` passes it straight to
`connect`/`dialCmd`/`Dial` without ever touching `session.Target`.
`PasswordOnly` matters only for SFTP: choosing "password" there means
skipping the agent and key files entirely, not merely offering Password as a
fallback after they fail, so `sftpsession.authMethods` branches on it before
building any other auth method. Choosing "password" with the field left
blank is caught in the form itself (`creds.PasswordOnly && creds.Password ==
""`) rather than surfacing as a dial failure, since it can never succeed.

Identity, Known Hosts, Verify, and CA File follow the same shape as Password
— conditionally visible, never touched by `openConnectForm`/
`loadConnectProfile`/`upsertProfile`, folded into `session.Credentials` by
`credentialsFromForm` — because they answer the same question a password
does: not where to connect or as whom, but how to prove it and how to trust
what answers, both of which end at `Dial` and nowhere else. Identity is
SFTP-only and only when the auth mode is not password (there is nothing to
name a key file for otherwise); Known Hosts is SFTP-only regardless of auth
mode, since host-key verification happens either way; Verify and CA File are
FTPS-only, and CA File further hides once Verify is set to "insecure" — a
certificate to trust is moot once every certificate is accepted. Each
overrides — replaces, not merges with — whatever the Dialer was configured
with at startup (`sftpsession.Config.IdentityFiles`/`KnownHostsPath`,
`ftpsession.Config.RootCAFile`) when the form's field is non-empty, the same
override-wins-else-fall-back pattern `Dialer.password` already used for
Password. `FTPSInsecure` is the one exception: it is ORed with the Dialer's
own `InsecureSkipVerify` rather than overriding it, so a blank form field can
never quietly turn a Dialer already configured insecure back on — the field
can only ever make a connection *less* strict than its Dialer was
configured, never more.

This is also why `session.Dialer.Dial` takes a `session.Credentials`
parameter now, alongside `Target`: credentials have to reach `Dial` somehow,
and putting them on `Target` would mean either persisting them (Target is
what a profile keeps) or awkwardly stripping them back out before saving.
`fakesession` ignores the parameter — the demo adapter authenticates nothing
— but every real `Dialer` and every call site had to change in step, which is
the cost of extending an interface all three adapters implement.

Remember is the one exception to "credentials are never persisted": it lets
the user opt in, per saved profile, to having the OS keyring do exactly that
— `internal/credstore.Store`, a new seam alongside `session.Dialer`/`vfs.FS`/
`transfer.Engine`/`config.SaveFunc`, real-backed by
`github.com/zalando/go-keyring` (`internal/credstore.Keyring`) and faked by
`internal/fakecredstore` for tests. `Model.creds` is nil-able exactly like
`Model.save`: nil means the feature is unavailable, and `connectFieldVisible`
hides Remember entirely rather than showing a control that does nothing.
Storage is keyed by the same `protocol|host|port|user` identity
`targetKey`/`profileKey` already use for profile matching — not the
profile's `Name` — via `credentialKey`, so renaming a profile never orphans
its stored password. The write only happens on `ctrl+s`
(`saveConnectProfile`, via `rememberCredentialCmd`): a bare connect never
touches the keyring, matching how host/user/path are not persisted without
an explicit save either. `ctrl+x` (`deleteConnectProfile`, via
`forgetCredentialCmd`) always scrubs the stored password, regardless of what
Remember was last set to — a deleted profile should not leave its password
behind. Selecting a profile (`loadConnectProfile`) or opening the form
(`openConnectForm`) both call `beginCredentialLookup`, which prefills
Password/Remember from whatever is stored, if anything.

All of this runs as a `tea.Cmd`, never inline in `Update` — keyring I/O can
block (a dbus round-trip, or a macOS Keychain prompt), the same concern
`persist`'s doc comment raises for disk I/O, more so here. A lookup is
guarded by `connectFormValue.credToken`, the same pattern
`filePane.requestToken` uses for stale listings: switching profiles twice
before the first lookup returns must not let the first reply clobber the
second selection's fields. A save/delete failure reaches the user through
`setError` (`credentialSyncMsg`) without stomping the optimistic "saved
profile X"/"deleted profile X" status unless it actually fails — success
says nothing new, since that status already covers it.

tideui was bumped from v0.2.2 to the pseudo-version whatthedock pins
(`v0.2.3-0.20260820020614-441c283e776f`) for two things the older release
lacks: `SoftRow`'s selected-row background highlight and the `ModalShadow`
style option. At first the app kept compositing overlays itself
(`overlayOnBase`) with its own flat shadow — a fixed glyph and color appended
to the modal's edge, blind to whatever it actually fell across — because
`ModalShadow`'s real behavior (`blendShadowRect`, alpha-blending the shadow
into the base view's actual per-cell colors) was only reachable through
`Renderer.Render`'s own `Layout.Modal`, and tideftp's hand-assembled
local/remote/transfer layout does not go through that. Rather than duplicate
that blending math the way `internal/ui/contrast.go` had to duplicate
`readableText` (see **Contrast** above — same problem, same lesson), tideui
gained a second exported entry point, `Renderer.OverlayModal(base,
overlayContent, width, height)`, that performs the identical modal-shadow
compositing step `Render` does internally but takes an already-assembled
base view instead of requiring `Layout.Panes`/`Mode`. `internal/ui/view.go`'s
own `overlayOnBase`/`addShadow`/`replaceAt` are gone; `View` now sets
`ModalShadow: m.shadow` in `StyleOptions` and calls
`renderer.OverlayModal(view, overlay.Content, m.width, m.height)`, the same
contrast-aware shadow whatthedock gets from `Render` directly.

### Host key trust

`session.UntrustedHostKeyError` and `Credentials.TrustedHostKey`/
`RememberHostKey` live in `internal/session`, not `internal/sftpsession`,
even though SFTP is the only Dialer that produces or consumes them today.
That's not an accident: `internal/ui` must never import a concrete adapter
package (see the note under **Config persistence** and the design ethos
running through this whole document), and `applyConnectFailed` has to branch
on this error type to decide whether to open `overlayHostKey`. Putting it in
`internal/session` — the neutral seam both sides already import — keeps that
branch possible without breaking the rule. It's the same reasoning that
already put `IdentityFile`/`KnownHostsPath` on `Credentials` as plain data
rather than `ssh` types.

`internal/sftpsession/sftpsession.go`'s `trustingCallback` wraps the real
`knownhosts` callback. On a real mismatch — a host whose already-known key
changed — it always fails closed, full stop, ignoring `TrustedHostKey`
entirely; that's the case a downgrade attack would try to exploit, so
nothing about the accept-once flow is allowed to touch it. Only when the
host has *no* known_hosts entry at all does `TrustedHostKey` get a say, and
only if its bytes match **exactly** what the server actually just presented
— never an arbitrary blob the caller hands in. That exact-match check is
what makes "accept once" safe to wire up: the key being trusted is
necessarily the same one the user was just shown in the overlay, because it
came from the `UntrustedHostKeyError` that overlay displayed.

A missing known_hosts file used to be `TestDialFailsWithoutAKnownHostsFile`'s
hard-failure case — deliberately, so "no known_hosts" could never silently
mean "accept anything." That test still exists
(`TestDialWithNoKnownHostsFileTreatsTheHostAsUnknown` now) and still proves
the dial doesn't quietly succeed, but the failure it proves changed shape:
`ensureKnownHostsFile` now creates an empty file (and its parent directory)
on first use rather than erroring, so a completely fresh machine reaches the
exact same "unknown host" prompt an existing-but-empty file would, instead
of a dead end demanding the user go create one by hand first. This mirrors
what OpenSSH itself does on a first connection.

`RememberHostKey` writes are deliberately best-effort: `Dial` has already
succeeded by the time `rememberHostKey` runs, so a write failure there (a
permissions problem, say) does not fail the connection — it just means the
same prompt reappears next time, which is safe, if a little repetitive.

Not covered by this: the per-profile strict/ask/off *mode* from Product
Decisions above. There's no config-level way to say "never prompt, just
fail" or "never prompt, just accept" for a given profile or globally —
every unknown host reaches `overlayHostKey` today, with no override.

### Conflict resolution

Detecting a conflict does not need a new `vfs.FS` method. `internal/ui`
already has `dstFS.List(ctx, dir, true)` — the same call
`beginPreflightScan` (`internal/ui/model.go`) already uses to walk the
*source* side of a folder queue — so checking the *destination* side is
just a second pass over the same scan: group the flattened file list by
`dstFS.Parent(f.dst)`, list each unique directory once, and match names.
A directory that fails to list (doesn't exist yet) just means nothing
conflicts there, not a scan failure. This is why `internal/vfs`,
`internal/localfs`, `internal/fakefs`, `internal/sftpsession/fs.go`, and
`internal/ftpsession/fs.go` needed no changes at all for this feature.

Every queue action goes through `beginPreflightScan` now, not just a folder
selection — a plain single file has to be checked for a conflict too, and
the only way to check is a `List` of its destination directory. This costs
one extra async round trip even in the common no-conflict case, trading
away the old synchronous "instant queue" for a plain file. It's a
deliberate trade, not an oversight: the alternative is not checking, which
defeats the feature. `applyPreflightScan` is what keeps the *outcome*
unchanged for that common case — no conflicts and no folder still queues
immediately, no overlay, exactly as before; only when a conflict is
actually found does anything visibly change.

`commitScan` (`internal/ui/conflict.go`) is the one place a resolved
`preflightScan` becomes queued `domain.Transfer` rows. It replaces three
previously-separate copies of nearly the same append loop: the plain-file
instant-queue path (now gone), `confirmPreflightQueue`'s folder-summary
confirm, and the new conflict-resolved path all funnel through it now. It
takes no policy parameter — every conflicting `preflightFile` already
carries its own `resolution *conflictPolicy` by the time `commitScan` ever
sees it, set per-file rather than once for the whole batch (see below). A
fix to how a `domain.Transfer` gets built from a scan now lives in one
place.

The overlay resolves conflicts one file at a time, matching FileZilla's own
"this file" scope literally rather than only in name.
`preflightScan.currentConflictIndex()` is the first file with a conflict
and no `resolution` yet — the one `overlayConflict` is showing. `enter`
(`resolveOneConflict`) sets just that file's `resolution` to the row under
the cursor and, if another unresolved conflict remains, leaves the overlay
open on it rather than committing; the overlay's title line
("file 2 of 5: ...") comes from `resolvedConflictCount()`. `a`
(`resolveAllConflicts`) instead calls `resolveAllRemaining`, which does the
same assignment to every still-unresolved conflict at once — the "current
queue" scope — and `s` does that plus sets `Model.sessionConflictPolicy` —
"this session". Both `resolveOneConflict` and `resolveAllConflicts` commit
once nothing is left unresolved, so a batch of one conflict behaves
identically to a plain `enter` under all three scopes: nothing to advance
to, nothing left over, straight to committing.

Six of the seven policies are themselves per-file rules regardless of which
scope picked them: "Overwrite if source newer" still only overwrites the
files that are actually newer, "Resume" still only resumes files with
something left to resume, and so on — `commitScan`'s loop evaluates each
file's own `*f.resolution` against its own conflict, not some batch-wide
average. `conflictResume`'s guard (`f.conflict.Size >= f.size`) matters
here: a destination already at least as large as the source has nothing
left to continue, so it's treated as already complete and skipped rather
than resumed into a corrupt state or silently truncated.

`Model.sessionConflictPolicy` (the "s" key's scope) is a bare
`*conflictPolicy` field, not persisted through `config.SaveFunc` the way
every other setting in `internal/config` is. That's deliberate: "this
session" in the product decision means exactly that — it resets on
restart — and there's no precedent anywhere in `internal/config` for a
transient, non-persisted override, so inventing one would be solving a
problem the feature doesn't have.

`renameDestination` (`internal/ui/conflict.go`) tries `"stem (1)ext"`,
`"stem (2)ext"`, and so on, checking both the destination directory's real
listing (gathered during the scan, so no extra round trip) and a `claimed`
set of names already handed out earlier in the same batch — two renamed
files landing on the same new name would be its own silent conflict
otherwise. `path.Ext`/`strings.TrimSuffix` on the bare leaf name is safe
regardless of whether the destination is local or remote, since a leaf name
has no directory separator to complicate the split either way.

Resume needed real changes in both protocol engines, not just the UI:
`transfer.Request` gained an `Offset int64` (0 means an ordinary full
transfer), and both `internal/sftpsession/engine.go` and
`internal/ftpsession/engine.go` open their destination without truncating
and seek both ends to `Offset` when it's non-zero, starting their `sent`
counter there too so progress reports the *total* bytes done, not just
what this run copied. Neither engine needed a new capability from its
underlying library: `*sftp.File`/`*os.File` already implement `io.Seeker`
and `sftp.Client.OpenFile` already supports opening without `O_TRUNC`, and
`github.com/jlaffaye/ftp`'s `ServerConn` already has native
`RetrFrom(path, offset)`/`StorFrom(path, r, offset)` built for exactly
this.

### Stats tab

`domain.Transfer` gained a `Protocol string` field, set from
`m.target.Protocol` at both places a `Transfer` gets built
(`commitScan` in `internal/ui/conflict.go`, `retrySelectedTransfer` in
`internal/ui/model.go`). `Model.target` itself wasn't usable for a
per-protocol breakdown: it's one value for the whole connection, and it
changes on reconnect, so a transfer already sitting in history could end
up misreported under whatever protocol the user most recently connected
with rather than the one it actually ran over. Capturing it per-transfer,
once, at queue time, is what keeps a session-long breakdown honest across
a reconnect.

Adding `tabStats` to the `bottomTab` enum wasn't a single extension point.
Before this, `tabLog` was the only tab with no selectable
`domain.Transfer` rows, and the codebase expressed that with ad-hoc
`== tabLog`/`!= tabLog` comparisons scattered across five different
functions (`clampBottomCursor`, `cancelActiveTransfers`,
`retrySelectedTransfer`, `bottomRowCount`, `moveCursor`'s queue-focus
branch). `tabStats` is a second rowless tab, so rather than scatter a
second comparison at each of those five spots, `bottomTabHasRows()`
(`internal/ui/model.go`) replaced every one of them: `false` for `tabLog`
*or* `tabStats`, `true` for the four real transfer-row tabs. `bottomRowCount`
and `moveCursor` still need their own per-tab behavior beyond the
boolean — `tabLog` scrolls raw text via `bottomOffset`, `tabStats` doesn't
scroll at all — so those two kept their own `switch`/case-by-case handling
rather than collapsing into the helper.

Sampling is gated to run only while the Stats tab is actually open —
`applyStatsTick` (`internal/ui/stats.go`) checks `m.bottomTab != tabStats`
on every tick and simply doesn't re-arm itself if it's no longer true, a
self-terminating chain rather than something that needs cancelling from
outside. `setBottomTab` restarts sampling from scratch every time
`tabStats` is entered, including switching back after looking away. The
trade-off, deliberately: the graph shows a gap across a tab switch rather
than continuous background history. Keeping the ticker running unconditionally
in the background would give a gap-free graph, at the cost of a timer that
never stops for the rest of the process — this is the first periodic
ticker anywhere in `internal/ui` (`tea.Tick`), and everything else in the
package redraws only in response to something that already happened (a
key, a transfer event, a listing reply, a resize), so "always on" would
have been a new category of cost, not just a bigger one.

`computeStats` (`internal/ui/stats.go`) is a single pass over
`m.transfers` — the existing queue/history list is already the complete
record of what happened this session, so there is no separate running
accumulator to keep in sync with it. The one number that can't be
recovered from a single pass over current state is throughput, since a
rate is a derivative: `applyStatsTick` keeps exactly two pieces of
carried state (`statsLastBytes`, `statsLastSampleAt`) to diff the total
bytes moved against the previous tick.

`renderThroughputLine` (`internal/ui/stats.go`) draws a true connected
line with Unicode Braille dots rather than the stacked-block bar chart the
first pass shipped with — a deliberate reversal of that pass's own
reasoning (a block bar's fill height is a pure function of one value; a
connected line has to reason about slope between columns to look
continuous rather than speckled), made because the visual payoff turned
out to be worth it on request. Each braille cell packs a 2-wide by 4-tall
grid of sub-pixels (`brailleBits`, the standard Unicode Braille Patterns
dot numbering), so the plotted resolution — and how much throughput
history fits across the same terminal width — is double what one sample
per column gave the bar chart. Two adjacent sub-columns whose values jump
by more than one sub-row are connected with `bresenhamRun`, the standard
integer line algorithm, so a sudden spike still reads as one stroke
instead of two disconnected dots. `smoothSamples` runs a trailing 3-sample
moving average over the window before any of this, since a raw 1-second
reading is noisy enough on its own to make an unsmoothed connected line
look jittery rather than fluid — this is also why a flat-zero history
still draws a visible baseline along the bottom rather than rendering as
nothing (`TestRenderThroughputLineFlatZeroDrawsABaseline` pins this): a
connected line at zero is a real reading, not the absence of one, the way
an ECG's flat baseline is still a trace.

Height and color both scale against `peak`, and `peak` is deliberately
measured across the *whole* history passed in (`samples`), not just the
sub-columns currently visible in the window. The first version measured it
from only the visible window, which meant every tick the window slid
forward — a new sample appended, the oldest one dropped — could change
which value was the window's local max, rescaling everything still on
screen taller/hotter (or shorter/cooler) relative to a ceiling that had
nothing to do with any new data actually arriving. Anchoring `peak` to the
whole session history instead means it only moves on a genuine new record,
which is a real, meaningful event worth reflecting — not a side effect of
old data quietly scrolling out of view.
`TestRenderThroughputLinePeakIsScopedToTheWholeHistoryNotJustTheWindow`
pins this: a real peak parked outside the visible window still has to set
the scale, or a flat run of small values would wrongly pin itself at full
height just for being the tallest thing currently on screen.

`renderStatsTab` packs everything but the graph into exactly two lines —
a live snapshot on top (active/queued counts, current throughput, and
overall progress as `bytesTransferred of totalBytes (percent)`), totals/
averages/per-protocol breakdown combined into one line on the bottom —
specifically so the graph gets as much of the available height as the
pane has to give, rather than losing rows to one-line-per-fact formatting.
Below `height == 2` it drops to just those two lines (no graph), and at
`height == 1`, just the first — the same "keep the most useful thing
longest" instinct `renderBottomPane`'s own `"no rows yet"` fallback
already has for an empty tab, just with a lower floor now that there's
less fixed content to protect. `statsSnapshot.totalBytes` sums
`BytesTotal` the same way `bytesTransferred` already summed `BytesDone` —
across every transfer in `m.transfers` regardless of status — so
`percentDone()` reads as overall session progress, not just "how far
along is whatever's active right now."

The whole tab paints a fixed black background with green text
(`statsBackground`/`statsForeground`/`statsMeta` in `internal/ui/stats.go`)
regardless of the active theme — on request, the one deliberate exception
to "everything follows the theme" anywhere in this app, the same way a
terminal monitoring widget (htop, an old oscilloscope-green VU meter)
usually commits to one look rather than adapting to its surroundings.
`statsLine` and `renderThroughputLine` both go through `segment`/
`clampView` (the same explicit-background-per-span technique
`renderTransferRow`'s progress bar already used) rather than
`renderer.Styles`, so nothing here can accidentally inherit the theme's
colors. The graph is additionally tinted per terminal column along
`statsGradient`, a low-to-high ramp from blue through violet and magenta
to hot pink, using whichever of that column's two sub-columns sits higher
— a column near the window's peak is hotter, not just taller, so the
color is another encoding of the data rather than decoration on top of
it. An earlier pass gave the single highest point its own near-white
highlight color distinct from the gradient, but at that lightness it just
read as plain white rather than a tinted glow, so it's gone — the peak
now simply reaches the gradient's own hottest step like any other high
point. `lipgloss`'s color-profile detection
falls back to plain text when stdout isn't a real terminal (true of every
`go test` run), which is why `TestRenderThroughputLineProducesRealANSIColor`
has to force a profile with `lipgloss.SetColorProfile` to actually see the
escape codes it's asserting on — everything else here is verified through
`ansi.Strip`ped goldens, which were never going to catch a missing color
in the first place.

The same auto-detection is a real, general risk for the running app, not
just a testing inconvenience: `internal/ui` and `tideui` both render
through lipgloss's shared global renderer (`tideui.NewRenderer`'s `Styles`
are built from plain `lipgloss.NewStyle()` calls, no renderer of its own —
confirmed by `tideui`'s own `activeColorProfile()`, which reads that same
global profile to truecolor-aware-blend the modal shadow), and termenv's
`TERM`/`COLORTERM`-based heuristics are known to under-detect in tmux, some
SSH sessions, and terminals that are truecolor-capable but never set
`COLORTERM`. Under-detection would quietly clip the Stats tab's 24-bit
gradient down to the nearest ANSI256 entry rather than fail loudly.
`cmd/tideftp/main.go` now calls `lipgloss.SetColorProfile(termenv.TrueColor)`
at startup — trusting that the deployment target really is truecolor,
rather than whatever termenv's heuristics conclude — unless `NO_COLOR` is
set, which is still honored.

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
   - `--host/--user/--port/--path` still reach a real server at startup; the
     form is the interactive path into the same dialers for that target and
     every other one
   - ~~Profile persistence.~~ Done — a `[[profiles]]` array table in
     `config.toml`, via `config.Profile`; the form's Profile field cycles
     through them and `ctrl+s`/`ctrl+x` save/delete (see **Connect form**)
   - ~~SFTP auth choice and a password field.~~ Done — Auth picks agent/key
     or password for SFTP; FTP/FTPS always show Password, since they have no
     other method. `session.Credentials` carries it from the form to `Dial`
     for that one attempt only — password mode is "prompt every time" by
     design, since profiles and config.toml never see it (see
     **Connect form**)
   - ~~SFTP identity/known-hosts and FTPS verify/CA fields.~~ Done — Identity
     and Known Hosts (SFTP), Verify and CA File (FTPS) are form fields now,
     shown only when they apply, each overriding the CLI-flag-configured
     default for that one attempt via `session.Credentials` (see
     **Connect form**)
   - ~~Keyring password storage.~~ Done — a **Remember** field next to
     Password, visible whenever Password is and a credstore was wired in;
     `ctrl+s` stores or forgets the typed password in the OS keyring
     accordingly, `ctrl+x` always forgets it. See the design note below.
     Nothing else waits on credential handling now — every setting
     `--identity`/`--known-hosts`/`--ftps-ca`/`--ftps-insecure` configure at
     startup can also be set per attempt from the form

5. ~~Improve transfer queue behavior.~~ Done — all five items below.
   - ~~Configurable parallelism.~~ Done — `+`/`-` adjust `Model.maxParallel`
     at runtime (clamped to `[1, maxParallelCap]`), persisted, shown as
     `(Nx)` in the Queue tab label
   - ~~Completed-transfer aging into History.~~ Done — `bottomTabFilter`
     excludes `Done` from the Queue tab, so a transfer that finishes simply
     stops appearing there on its next render; `tabHistory` is its
     permanent home from then on. No timer involved
   - ~~Retry failed transfers.~~ Done — `R`, with the queue pane focused on
     a `Failed`/`Canceled` row, queues a brand-new `Transfer` with the same
     source/destination rather than mutating the original, which stays put
     as a record of what happened
   - ~~Cancel active transfers.~~ Done, and now per-row too — `x` cancels
     (or, for a `Queued` row that never reached the engine, just drops)
     only the row under `Model.bottomCursor` when the queue pane is focused
     on a transfer tab; everywhere else it's still "cancel everything in
     flight" through `transfer.Engine.Cancel`. `bottomTabFilter`/
     `bottomTabTransfers` are the one place both rendering and row
     targeting agree on what row N in the current tab actually is
   - ~~Recursive folder preflight summary.~~ Done — see the design note
     below. Selecting a folder now scans it with `vfs.FS.List` and queues
     every file it finds after a confirm overlay, rather than refusing it

6. Add protocol adapters.
   - ~~SFTP.~~ Done — `internal/sftpsession`. Parallel transfers share one
     `sftp.Client`; if that turns out to throttle, giving each transfer its
     own client is the next thing to try.
   - ~~FTP and FTPS.~~ Done — `internal/ftpsession`, both verified against a
     real vsftpd server.
   - The fake adapters and their tests are the contract that proves the UI
     did not regress while real adapters land.

7. ~~Expand TUI tests.~~ Done.
   - ~~Snapshot/golden views for main screen and overlays.~~ Done —
     `internal/ui/golden_test.go` + `internal/ui/testdata/*.golden`, ANSI
     stripped so files stay plain text and don't churn across themes.
     `goldenModel` uses fixed timestamps rather than a real fakefs/localfs
     listing, whose `Modified` times are wall-clock-relative and would
     make every golden file drift day to day. Regenerate with
     `go test ./internal/ui/... -run TestGolden -update` and review the
     diff before committing
   - ~~Key routing tests for focus, selection, tabs, and modals.~~ Done —
     `internal/ui/key_routing_test.go`. Coverage-guided: `toggleSelection`
     (space), `clearSelection` (esc), `queueFocusedTransfer` (the "o" demo
     conflict prompt's confirm path), tab/shift-tab's actual cycling order
     across all three panes, and that an open overlay's `q` closes it
     rather than falling through to the top-level quit binding were all at
     0% coverage before this
   - ~~Resize tests for small terminals.~~ Done —
     `internal/ui/resize_test.go`: every overlay rendered across a sweep
     down to 1x1 and 0x0, and a shrink-then-grow `WindowSizeMsg` sequence,
     both asserting only "does not panic" — `TestFirstFileRowMatchesTheRenderedLayout`
     already pins exact layout at realistic sizes; this is for sizes no
     layout math was ever written against
   - ~~Transfer queue state tests.~~ Already substantially covered by
     `internal/ui/transfer_queue_test.go` (parallelism, row cursor,
     cancel, retry, aging) and the recursive-folder tests in
     `internal/ui/model_test.go` from the transfer-queue-polish work

## Known Gaps

- SFTP, FTP, and FTPS all work and are verified against real servers.
  `faketransfer` moves no bytes, it only emits a plausible event stream
- `internal/ftpsession` has no hermetic tests: its unit tests cover listing
  conversion, paths, and the pool, but everything protocol-level needs the
  live server. SFTP has an in-process server and FTP should get one too
- The SFTP test server's `/upload` is not readable by `ftp_test`, so the SFTP
  live tests cannot do a round trip until that is fixed on the server (see
  **Test Servers**)
- Passwords come from the connect form's Password field or the environment
  (`TIDEFTP_FTP_PASSWORD`, `TIDEFTP_SFTP_PASSWORD`), never a flag. A
  passphrase-protected SSH key is reported rather than prompted for
- Host keys now have an ask/accept-once flow (see **Host key trust**) for the
  one case that matters most — a host with no known_hosts entry at all,
  including a missing known_hosts file. A real mismatch against an
  already-known host still always fails closed with no prompt, which is
  deliberate. What's still missing is the broader per-profile
  strict/ask/off *mode* from Product Decisions above: there is no way to
  force pure-strict (never prompt, fail on anything unknown) or
  pure-accept-anything (no known_hosts check at all) globally or per
  profile — every unknown host always prompts today
- Conflict resolution is real now (see **Conflict resolution**): a queued
  file whose destination already exists opens `overlayConflict` with every
  option from Product Decisions above, including Resume, and all three
  scopes work as described — this file, current queue, this session
- Folders now queue recursively — a preflight scan flattens them into
  individual file transfers before confirming (see **Recursive folders**);
  no adapter changes needed, since `transfer.Engine` still only ever sees
  files
- Cancelling a transfer closes its file handles to interrupt a parked read.
  A listing parked on a dead connection is only abandoned, not interrupted:
  pkg/sftp has no context-aware API, so the request occupies the connection
  until the server answers or the connection closes
- All three seams (`session.Dialer`, `vfs.FS`, `transfer.Engine`) now have
  both a fake and a real implementation, which is the evidence they are the
  right shape
- The connect form has Auth/Password, Identity/Known Hosts, and Verify/CA
  File fields (see **Connect form**), covering everything `--identity`,
  `--known-hosts`, `--ftps-ca`, and `--ftps-insecure` configure at startup.
  Still missing: no way to point at a different agent socket than
  `SSH_AUTH_SOCK`, and known-host verification is strict-or-nothing — a
  known_hosts override lets a different file be strict against, but there is
  still no ask/accept-once flow for a host key that file does not have
- Credential storage now exists but is opt-in per profile: a saved profile's
  password is remembered in the OS keyring only if the form's Remember field
  was "yes" when `ctrl+s` saved it (see **Connect form**); the default
  remains "prompt every time" for anything not explicitly remembered, and
  `fakesession` needs no credentials at all
- A connection is never retried automatically after a drop
- A listing that hangs is bounded only by `listTimeout` (20s) and cannot be
  cancelled from the UI — there is no key to abandon a slow directory
- Config persistence covers global prefs (theme, density, shadow, icons,
  `maxParallel`, the pane splits) and saved connection profiles.
  `showHidden` and any use of the state/cache directories are not persisted
  yet
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
- Transfer failures are simulated deterministically by `failsAt` in
  `internal/faketransfer` so the Failed tab and error styling are reachable
  without real networking; the whole package goes away once real engines land
- Completed transfers do not yet age out of Queue into History
- Failed transfer retry flow is not implemented yet
- No screenshot/PTY visual QA has been run yet, and synthetic PTY input does
  not currently work (see Run And Verify)
