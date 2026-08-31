# Changelog

All notable changes to TideFTP. The format follows
[Keep a Changelog](https://keepachangelog.com/); versions follow
[Semantic Versioning](https://semver.org/). Pre-1.0, minor bumps carry
feature batches and may change behaviour.

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
