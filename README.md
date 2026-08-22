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

# FTP
TIDEFTP_FTP_PASSWORD=... go run ./cmd/tideftp --protocol ftp \
    --host files.example.com --user allie --path /pub
```

SFTP authenticates with the SSH agent, the usual `~/.ssh` keys, or `--identity`
for a specific key file. Host keys are checked strictly against
`~/.ssh/known_hosts` (`--known-hosts` to point elsewhere); there is no option to
skip that check.

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
- `x`: cancel active transfers
- `o`: conflict prompt (demo)
- `c`: connect / disconnect (pick a server)
- `t`: theme picker
- `i`: toggle icons (falls back to ASCII glyphs, same as the vt52 theme)
- `.`: toggle hidden files
- `Shift+Left` / `Shift+Right`: resize local/remote panes
- `Shift+Up` / `Shift+Down`: resize transfer pane
- `1`-`5`: bottom tabs
- `?`: help
- `q`: quit
