# TideFTP

TideFTP is a keyboard-first, mouse-friendly terminal file transfer client built
with Go, Bubble Tea, and TideUI.

The first slice focuses on the polished app shell:

- FileZilla-style layout: local pane, remote pane, wide transfer pane.
- Whatthedock-style soft modal screens with drop shadows.
- `tide-night` default theme plus a live theme picker.
- Shift-arrow pane resizing.
- Real SFTP and FTP over the same adapter interfaces, plus fakes for UI work.

## Run

```bash
./start.sh
```

or directly:

```bash
go run ./cmd/tideftp
```

That runs on the fake demo adapter. To reach a real server:

```bash
# SFTP (the default protocol)
go run ./cmd/tideftp --host files.example.com --user allie --path /srv/www

# FTP, or FTPS with explicit TLS
TIDEFTP_FTP_PASSWORD=... go run ./cmd/tideftp --protocol ftps \
    --host files.example.com --user allie --path /pub
```

SFTP authenticates with the SSH agent, the usual `~/.ssh` keys, or `--identity`
for a specific key file. Host keys are checked strictly against
`~/.ssh/known_hosts` (`--known-hosts` to point elsewhere); there is no option to
skip that check.

FTPS verifies the server certificate. For a self-signed one, trust it with
`--ftps-ca cert.pem` rather than turning verification off; `--ftps-insecure`
exists but accepts anything. FTPS is capped at TLS 1.2 by default because some
servers mishandle TLS 1.3 on data connections, corrupting uploads over 16 KB —
`--ftps-allow-tls13` lifts the cap.

FTP and FTPS need a password. It is read from `TIDEFTP_FTP_PASSWORD`, and SFTP
will use `TIDEFTP_SFTP_PASSWORD` if key-based methods do not work. Passwords are
deliberately not flags: a flag puts the secret in the process table for every
other user on the machine to read.

Check the version of a built binary with `tideftp --version` (release builds inject it
via `-ldflags "-X main.version=$VERSION"`; `go run`/`go build` without that flag reports
`dev`).

## Keys

- `Tab` / `Shift+Tab`: switch panes
- `Enter`: open directory
- `Backspace`: parent directory
- `Space`: select item
- `Ctrl+A`: select all
- `Esc`: clear selection or close overlay
- `u`: upload selected/local cursor item
- `d`: download selected/remote cursor item
- `e`: edit the highlighted file in an editor (writes it back if you change it).
  The editor is the **Editor** row in Settings (`,`) — `auto` resolves `$VISUAL`,
  `$EDITOR`, `git config core.editor`, then a common editor on `PATH`. Set
  `editor` in `config.toml` to anything, including flags, e.g. `editor = "code -w"`
- `v`: preview the highlighted file — reads the first 128 KB and shows it as
  syntax-highlighted text, or as a hexdump for binary content (`x` toggles,
  `esc` closes). It never downloads the whole file. The header names the
  language that was recognised, so a file that comes out uncoloured says why
- `y`: copy the selection's full paths to the clipboard, one per line. Over SSH
  this uses OSC 52 so the paths land on *your* clipboard, not the server's;
  locally it prefers `wl-copy`/`pbcopy`/`xclip`/`xsel` and falls back to OSC 52
- `M`: mirror the focused pane (or the directory under the cursor) onto the
  other side. Walks both trees, queues only files that are missing or differ
  by size / a newer mtime, and shows a plan — new, updated, unchanged — to
  confirm. `p` in that overlay arms **prune**, which then also deletes
  anything at the destination with no source counterpart (off by default).
- `x`: cancel active transfers
- `c`: connect (opens the server list: Enter connects, `e` edits, `n` / the last row adds a new one)
- `t`: theme picker
- `i`: toggle icons (falls back to ASCII glyphs, same as the vt52 theme)
- `.`: toggle hidden files
- `Shift+Left` / `Shift+Right`: resize local/remote panes
- `Shift+Up` / `Shift+Down`: resize transfer pane
- `1`-`6`: bottom tabs
- `?`: help
- `q`: quit

## Transfers

A running transfer's row shows its live throughput and time remaining, and the
transfers pane header carries an ETA for everything still queued or running.
Both are estimated from the average rate of the transfers actually in flight,
so they settle down after the first few seconds rather than being right
immediately.

Two settings in `,` change what happens around a transfer:

- **Verify** (`verify_checksums`, off by default) re-reads both ends of every
  completed transfer and compares SHA-256 sums. A mismatch moves the transfer
  to the Failed tab, where `R` retries it. This doubles what a transfer costs
  in time and bytes — it is correctness you opt into, not a free check.
- **Reconnect** (`auto_reconnect`, on by default) redials after a connection
  drops on its own, backing off 2s, 4s, 8s, 15s, 30s before giving up, and
  puts you back in the directory you were in. A disconnect you asked for is
  never undone, and connecting somewhere by hand calls off a redial in
  progress. Transfers interrupted by the drop still fail — they are not
  resumed automatically; retry them with `R`.

## Development

`docs/handoff.md` carries the design notes, the adapter architecture, and the
LAN test servers the real FTP/FTPS/SFTP adapters are verified against.

```bash
go test ./...             # hermetic; safe anywhere
go test -race ./...       # the adapters and the transfer engines are concurrent
go vet ./...
```
