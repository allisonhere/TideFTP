# TideFTP

TideFTP is a keyboard-first, mouse-friendly terminal file transfer client built
with Go, Bubble Tea, and TideUI.

The first slice focuses on the polished app shell:

- FileZilla-style layout: local pane, remote pane, wide transfer pane.
- Whatthedock-style soft modal screens with drop shadows.
- `tide-night` default theme plus a live theme picker.
- Shift-arrow pane resizing.
- Fake remote adapter and simulated transfers for UI iteration.

## Run

This workspace currently has a placeholder `.git` directory, so disable Go's VCS
stamping until the project is initialized as a normal repository:

```bash
GOMODCACHE=$PWD/.cache/gomod GOCACHE=$PWD/.cache/gobuild GOSUMDB=off go run -buildvcs=false ./cmd/tideftp
```

## Keys

- `Tab` / `Shift+Tab`: switch panes
- `Enter`: open directory
- `Backspace`: parent directory
- `Space`: select item
- `Ctrl+A`: select all
- `Esc`: clear selection or close overlay
- `u`: upload selected/local cursor item
- `d`: download selected/remote cursor item
- `c`: connect modal
- `t`: theme picker
- `.`: toggle hidden files
- `Shift+Left` / `Shift+Right`: resize local/remote panes
- `Shift+Up` / `Shift+Down`: resize transfer pane
- `1`-`5`: bottom tabs
- `?`: help
- `q`: quit
