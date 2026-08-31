# TideFTP — roadmap

Feature backlog, roughly prioritised. Not a spec; each needs its own design pass.

## Tier 1 — makes it a daily driver

- [x] **Edit a file in place** — `e` on a file row checks it out to a temp copy,
      opens an editor, and writes it back if the contents changed. Both panes.
      5 MiB cap, refuses binaries. `vfs.FS` gained `ReadFile`/`WriteFile`.
      Editor picked in Settings (`auto` = $VISUAL/$EDITOR/`git core.editor`/PATH)
      or `editor` in `config.toml`.
- [ ] **Filter / search within a pane** — type to narrow the visible listing
      (glob or substring), like the help overlay's search. Essential for large dirs.
- [x] **Directory sync / mirror** — `M` mirrors the focused pane (or the
      highlighted subdirectory) onto the other side. Walks both trees, queues
      only files missing or differing by size / a newer mtime (2s skew
      window), and shows a new/updated/unchanged plan to confirm. `p` arms
      **prune**, deleting destination entries with no source counterpart
      bottom-up (off by default). Copies reuse `commitScan`; see
      `internal/ui/sync.go`.
- [ ] **Queue persistence** — persist the transfer queue (XDG state dir) and offer
      to resume on next launch; the engine's Offset/ResumeFrom already supports
      mid-file resume.
- [ ] **Sorting controls** — name / size / date / type, asc/desc, per pane.

## Tier 2 — server-admin essentials

- [ ] **chmod / permissions edit** — `m` on a row to change the mode shown in the
      listing. SFTP and FTP both support it.
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
