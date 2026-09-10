# Changelog

All notable changes to TideFTP. The format follows
[Keep a Changelog](https://keepachangelog.com/); versions follow
[Semantic Versioning](https://semver.org/). Pre-1.0, minor bumps carry
feature batches and may change behaviour.

## Unreleased

### Added

- **Bookmarks.** `B` bookmarks the focused pane's current directory and `b`
  opens a picker to jump back to one, so the directories a session actually
  lives in are two keys away instead of a walk down the tree every time. A
  profile's `start_path` already covered one such directory, but only one, and
  only by reconnecting.

  Remote bookmarks belong to the **saved server profile** they were taken on,
  not to the app: `/var/www` names something different on every host, and a
  list that followed you between them would be a way to land in the wrong
  place on a machine where that matters. The consequence is that a connection
  with no saved profile — one dialled straight from the command line — has
  nowhere to keep them, and `B` says so rather than quietly creating a saved
  server the user never asked for. Local bookmarks are global for the
  mirror-image reason: the local pane is not a property of whichever server
  you happened to dial.

  `B` is a toggle, so the same key takes a directory off the list, and it
  works inside the picker too (`shift+b`) — building a list up is one key
  repeated rather than a close-navigate-reopen cycle. `dd` in the picker
  removes the highlighted entry, matching the server list's two-press
  confirmation. A bookmark is just a path, so there is no name to invent or
  keep in step with a directory that moved, and jumping to one that has since
  gone leaves the pane where it was and reports the error. They persist as
  `bookmarks` under each `[[profiles]]` table and `local_bookmarks` at the top
  level of `config.toml`.

- **Implicit FTPS (`ftps-implicit`).** A fourth protocol alongside
  `sftp`/`ftp`/`ftps`, for servers that expect the TLS handshake before any
  FTP command rather than an `AUTH TLS` upgrade — the older port-990 flavour
  that plenty of long-lived FTPS deployments still speak. It is a separate
  protocol rather than a flag on `ftps` because a server offers one or the
  other on a given port and there is nothing to negotiate: an implicit server
  sends no greeting until it has a `ClientHello`, so an explicit client cannot
  detect it, only hang. Available from `--protocol ftps-implicit` and the
  connect form's protocol picker, and it shares every TLS setting with
  explicit FTPS — `--ftps-ca`, `--ftps-insecure`, `--ftps-allow-tls13`, and
  the form's Verify and CA fields.

- **In-app updates.** TideFTP now checks GitHub for a newer release at launch
  and can install it itself: `U` (or Settings → *Updates*) downloads the
  release archive, verifies it against the release's `SHA256SUMS`, replaces
  the binary, and restarts into it. Downloads are accepted only from
  `github.com`, and an archive with a missing or mismatched checksum is
  refused. Installing while transfers are running warns first and asks again,
  rather than refusing — it is your call. `i` ignores a version until the next
  release. This is TideFTP's first outbound request to anything other than the
  server you connect to; it is one call to `api.github.com` per launch and is
  switched off with `check_on_startup = false` under `[updates]`, or in
  Settings. A build that is not a tagged release never checks.

- **`match-omarchy` theme.** For [Omarchy](https://omarchy.org) users, a theme
  that follows the current Omarchy desktop theme: it reads the live palette
  (via `omarchy-theme-color`, falling back to
  `~/.local/state/omarchy/current/theme/colors.toml`), remaps it, and
  contrast-corrects it so file and transfer rows stay readable whatever palette
  Omarchy is on. Works for light and dark Omarchy themes and repaints within a
  couple of seconds when you switch your desktop theme. Falls back to
  `tide-night` when Omarchy isn't installed. Pick it with `t` or in Settings.

### Changed

- **The Stats graph now samples for as long as the connection lasts**, rather
  than only while its tab is on screen, and samples four times a second
  instead of once. Switching to the queue while a transfer ran and then
  switching back used to show an empty graph starting from the moment you
  returned — the stretch worth looking at was exactly the stretch it threw
  away. The history belongs to the connection now: it survives every tab
  switch, and a disconnect is what clears it and stops the sampler.

  Each reading now measures across a one-second window rather than between
  two consecutive ticks, which is what makes sampling that fast meaningful. A
  running transfer only updates its byte count every 200 ms, so a reading
  taken between two ticks depended on how many of those updates happened to
  land in that particular tick — and at any tick interval that is not an exact
  multiple of the reporting interval, the two beat against each other. At
  250 ms against 200 ms a perfectly constant transfer read 0.8×, 0.8×, 0.8×,
  1.6×, forever. The graph scaled itself to a 1.6× that was pure artifact and
  drew the real rate as a flat band two thirds up the box, with no peaks at
  all. Measuring across several reports averages that beat out.

  The graph's ceiling also comes from a bounded lookback now rather than the
  whole history, and from the smoothed curve that actually gets drawn rather
  than the raw maximum. With sampling running for the life of the connection,
  a ceiling anchored to all of history let one early spike flatten every later
  transfer into the bottom row.

- **`Tab` now toggles between the local and remote panes** instead of cycling
  through the transfer pane as a third stop. The two file panes are what a
  session is spent moving between, and passing through the queue on the way
  back cost a keystroke every time. `Shift+Tab` does the same thing, since
  with two panes there is no forwards or backwards.

  The transfer pane is still focusable, so nothing it owns became
  unreachable: `1`-`6` now take focus there along with selecting a tab, a
  mouse click still works, and `R` goes there by itself. Pressed from a file
  pane, `R` moves focus to the transfer pane, switches to the Failed tab when
  the current one holds no failures, and retries the first failure it finds —
  where it used to just report "select a failed transfer to retry". Pressed
  while already on a failed row it retries that row, so repeated presses do
  not snap back to the top.

### Fixed

- **Every FTP and FTPS connection dropped the instant it opened**, reporting
  "operation was canceled". Taking over the control dial to bound the greeting
  (see the implicit FTPS entry above) meant handing jlaffaye/ftp a dial
  function — and it reuses that function for every *data* connection too, not
  just the control connection it was written for. So each data connection was
  opened with the connect context, which the UI cancels as soon as the dial
  returns, since it is there to bound connecting and nothing more. The first
  listing after connect needed a data connection and failed immediately.

  Data connections now dial on their own terms: no connect context, and no
  socket deadline. Both mattered — the deadline is an absolute time chosen at
  dial, so a transfer still running when it passed would have been cut off
  mid-stream on a connection the pool considered healthy. Supplying a dial
  function also stopped the library from wrapping data connections in TLS
  itself, so that moved with them; without it an explicit-FTPS data channel
  would have gone out in the clear after `PROT P`.

- **Explicit FTPS defaulted to port 990, which cannot work.** A `ftps` target
  with no port dialled 990 — implicit FTPS's port — while the client only ever
  spoke explicit FTPS (`AUTH TLS`). That put an upgrade-in-place client in
  front of a server waiting for a `ClientHello`, so the connection could not
  succeed by any route. Explicit FTPS now defaults to 21, where it belongs;
  990 is the default for the new `ftps-implicit`. A profile that had worked
  around this by pinning port 990 explicitly should now use the
  `ftps-implicit` protocol instead.

- **A dial could hang forever on a server that went quiet.** `ftp.Dial` reads
  the server's greeting with no deadline — the dial timeout bounds only the
  TCP connect — so a server that accepted the connection and then said nothing
  left the connect spinning with no way out but killing the app. The control
  connection is now opened with a deadline that covers the greeting too, so
  this fails with a timeout the UI can report. It is what the port bug above
  produced, but it applies to plain FTP just as much.

## v0.2.0

The first release with real networking. `v0.1.0` was the UI shell over a
simulated adapter; this is the working client.

### Added

- **Real protocol adapters** — SFTP, FTP and FTPS over one set of
  interfaces, with a connection lifecycle around the seams. Directory
  listing and the transfer engine are asynchronous and error-returning.
- **Connect flow** — an editable connect form, saved connection profiles,
  per-attempt protocol choice, SFTP identity / `known_hosts` / FTPS-CA
  fields, and opt-in password storage in the OS keyring.
- **SFTP host keys** — checked against `known_hosts` by default, with an
  explicit trust-once / trust-and-remember prompt for an unknown host and a
  per-profile `ask` / `strict` / `off` policy. Encrypted keys accept a
  passphrase.
- **Transfer queue** — adjustable parallelism (`+`/`-`), per-row cancel,
  retry, and queue aging into a History tab; live per-file and overall
  throughput and ETA.
- **Recursive folder transfers** with a pre-flight confirm summary.
- **Conflict policies** — overwrite / overwrite-if-newer / -if-different-size
  / resume / rename / skip, resolved one file at a time or applied to the
  whole batch, with an optional remember-for-session.
- **Directory mirror** (`M`) — walks both trees, queues only what's missing
  or differs by size or a newer mtime, shows a new / updated / unchanged
  plan, and offers opt-in prune of destination extras.
- **Post-transfer verify** (`verify_checksums`, off by default) — re-reads
  both ends and compares SHA-256.
- **Auto-reconnect** (`auto_reconnect`, on by default) — redials after an
  unrequested drop with 2/4/8/15/30 s backoff and returns to the directory
  you were in.
- **Edit in place** (`e`) — checks a file out to a temp copy, opens
  `$EDITOR`, writes it back on change. Editor resolved from Settings or
  `config.toml`.
- **Streaming preview** (`v`) — the first 128 KB as syntax-highlighted text
  or a hexdump (`x` toggles), never downloading the whole file.
- **Filter** (`/`) — narrow a pane's listing live by glob or substring, per
  pane.
- **Sort** (`s` / `S`) — cycle name / size / date / type and reverse, per
  pane, persisted as the startup default.
- **chmod** (`m`) — octal-mode prompt for the selection; local and SFTP.
- **File operations** — new folder, rename (refuses an existing target, with
  a confirm-and-overwrite path), and delete, which now removes a non-empty
  directory and its contents.
- **Copy paths** (`y`) — OSC 52 over SSH so paths reach *your* clipboard.
- **Command palette** (`Ctrl+K`) and a **settings overlay** (`,`).
- **Stats tab** — a real-time throughput graph.
- Config persistence in `config.toml` under the XDG paths.
- `←` / `→` (and `h` / `l`) move focus between the local and remote panes.

### Changed

- The panes re-list themselves when the transfer queue drains, so files a
  batch just moved appear without navigating away and back.
- The mirror scan never lists a destination subtree it already knows is
  absent — one `LIST` instead of one per missing folder, which kept FTP
  scans from hanging.
- `backspace` is now the only "parent directory" key (`h` moved to
  pane-focus).

### Fixed

- A pooled FTP control-connection leak, and a non-recursive `mkdir`.
- Reconnect no longer strands in-flight transfers or freezes their progress.
- Config persistence no longer blocks the UI goroutine.
- Mouse hit-testing, pane scrolling, and stale selections after a refresh.

## v0.1.0

First tagged release — the UI shell, built against a fake remote adapter
with no real networking.

- FileZilla-style two-over-one layout: local pane, remote pane, wide
  transfer pane.
- `tide-night` default theme and a live theme picker.
- Connect, help, conflict, and theme overlays.
- Transfer queue with Queue / Active / Failed / History / Log tabs, live
  scrolling, and `tail -f`-style auto-follow.
- Selection highlighting, an icon toggle with ASCII fallback, coloured
  file and transfer rows.
- Shift-arrow pane resizing; keyboard and basic mouse navigation.
