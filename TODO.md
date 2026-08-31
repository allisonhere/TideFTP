# TideFTP — roadmap

Feature backlog, roughly prioritised. Not a spec; each needs its own design pass.

## Tier 1 — makes it a daily driver

- [x] **Edit a file in place** — `e` on a file row checks it out to a temp copy,
      opens an editor, and writes it back if the contents changed. Both panes.
      5 MiB cap, refuses binaries. `vfs.FS` gained `ReadFile`/`WriteFile`.
      Editor picked in Settings (`auto` = $VISUAL/$EDITOR/`git core.editor`/PATH)
      or `editor` in `config.toml`.
- [x] **Filter / search within a pane** — `/` opens a live filter on the
      focused pane: type to narrow the listing, `enter` accepts it (keys
      resume, listing stays narrowed), `esc` clears it. Glob when the query
      has `*?[`, case-insensitive substring otherwise; `..` always stays
      visible. Per pane, dropped on navigation, kept across a refresh. The
      pane keeps the full listing in `filePane.allEntries` and exposes the
      filtered view as `entries`, so cursor/selection/render/mouse code is
      unchanged (`internal/ui/filter.go`).
- [ ] **Directory sync / mirror** — walk both trees, transfer only what differs by
      size/mtime, optionally prune extras. The apex of the recursive-queue +
      conflict-policy work. (`lftp mirror`.)
- [ ] **Queue persistence** — persist the transfer queue (XDG state dir) and offer
      to resume on next launch; the engine's Offset/ResumeFrom already supports
      mid-file resume.
- [x] **Sorting controls** — `s` cycles the focused pane's sort key (name /
      size / date / type), `S` reverses direction. Dirs stay above files for
      every key but `type`, `..` stays pinned. Per pane, kept across
      navigation; the focused pane's order persists as the startup default
      (`sort` in `config.toml`). The UI owns the order now — it sorts
      `filePane.allEntries` before the filter derives `entries`
      (`internal/ui/sort.go`); the fs adapters' own sort is just a starting
      point.

## Tier 2 — server-admin essentials

- [x] **chmod / permissions edit** — `m` opens an octal-mode prompt for the
      selection or highlighted row, pre-filled with the current mode and
      echoing the symbolic form back. `vfs.FS` gained `Chmod`; localfs and
      SFTP implement it, FTP returns the new `vfs.ErrUnsupported` (no
      portable permission command, and jlaffaye/ftp exposes no SITE CHMOD).
      Multi-select applies one mode to every entry.
- [ ] **Per-connection bookmarks** — favourite directories beyond the start path;
      jump straight to `/var/www` etc.
- [ ] **Ignore patterns for recursive queue** — skip `.git`, `node_modules`,
      `*.log` when queuing a folder.
- [ ] **Bandwidth limit** — client-side throttle so a big transfer doesn't
      saturate the link.

## Tier 3 — polish

- [x] **File preview** — `v` peeks at the first 128 KB as text or as a hexdump
      (`x` toggles), never downloading the whole file. `vfs.FS` gained `Open`,
      a streaming reader, for this and for the checksum verify below.
      Syntax-highlighted via chroma's lexers only: the colours are this
      package's (`internal/ui/highlight.go`), run through `readableOn`, on the
      panel's own background — a bundled chroma theme would ignore the active
      TideFTP theme and paint its own surface.
- [x] **Post-transfer checksum verify** — the **Verify** setting
      (`verify_checksums`, off by default) streams both ends of a completed
      transfer through SHA-256; a mismatch demotes the row to Failed, where
      `R` retries it. A verify that cannot run leaves the row Done and
      "unverified" — not being able to check is not the same as finding a
      difference.
- [x] **Per-file and overall ETA** — running rows show throughput and time
      remaining; the transfer pane header carries an ETA for the whole queue.
      Estimated from each transfer's average rate since it started, which
      needs no timer of its own: progress events already redraw the pane.
- [x] **Copy path to clipboard** — `y` copies the selection's full paths.
      OSC 52 over SSH (so the paths reach the user's own clipboard, not the
      server's), a local helper otherwise.
- [x] **Auto-reconnect** — the **Reconnect** setting (`auto_reconnect`, on by
      default) redials after an unrequested drop, backing off 2/4/8/15/30s,
      and returns to the directory the drop interrupted. Transfers the drop
      killed are still Failed; nothing is resumed automatically.

## Smaller / opportunistic

- [ ] `--host-key-policy` startup flag (form + persistence already done).
- [x] Normalise trailing whitespace in the golden files — `-update` already
      wrote trimmed files (see `assertGolden`); regenerating them for the Tier 3
      work committed the trim, so the churn is gone.
- [ ] Symlink handling (follow vs show; create).
- [ ] `!` to run a shell command in the local pane's directory.
