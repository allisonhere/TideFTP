# Changelog

All notable changes to TideFTP. The format follows
[Keep a Changelog](https://keepachangelog.com/); versions follow
[Semantic Versioning](https://semver.org/). Pre-1.0, minor bumps carry
feature batches and may change behaviour.

## Unreleased

### Added

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

### Fixed

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
