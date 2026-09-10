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
- [x] **Directory sync / mirror** — `M` mirrors the focused pane (or the
      highlighted subdirectory) onto the other side. Walks both trees, queues
      only files missing or differing by size / a newer mtime (2s skew
      window), and shows a new/updated/unchanged plan to confirm. `p` arms
      **prune**, deleting destination entries with no source counterpart
      bottom-up (off by default). Copies reuse `commitScan`; see
      `internal/ui/sync.go`.
- [x] **In-app updates** — a startup check against the GitHub releases API,
      a topbar notice, and `U` / Settings → *Updates* to download, verify
      against `SHA256SUMS`, install in place and restart via `syscall.Exec`.
      Ported from TideMail (`internal/update` is the same package with the
      repo constants changed); the release pipeline already published the
      matching artifacts, so nothing there changed. Off with
      `check_on_startup = false`. Installing with transfers in flight warns
      and asks twice rather than refusing.
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
- [x] **Per-connection bookmarks** — `B` bookmarks the focused pane's current
      directory (and un-bookmarks it: it is a toggle), `b` opens a picker where
      `enter` jumps, `shift+b` adds the current directory without leaving, and
      `dd` removes a row. Remote bookmarks hang off the saved profile they were
      taken on, so they need one — a CLI-dialled connection has nowhere to keep
      them and `B` says so rather than inventing a profile. Local bookmarks are
      global. A bookmark is just a path; `navigateTo` already refuses to commit
      a listing that fails, so a stale one leaves the pane put
      (`internal/ui/bookmarks.go`). Persisted as `bookmarks` per profile and
      `local_bookmarks` at the top level. Note this made `session.Target`
      non-comparable — `==` on it became `SameConnection`.
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

## Protocols

- [x] **Implicit FTPS** (`ftps-implicit`) — TLS before the greeting, port 990.
      A separate protocol rather than a mode of `ftps`: a server offers one
      flavour or the other on a port and an implicit server stays silent until
      it gets a `ClientHello`, so there is nothing to negotiate or detect.
      Shares every TLS setting with explicit FTPS. Fixed the related bug where
      `ftps` defaulted to 990 — the one port an AUTH TLS client cannot use.
- [ ] **WebDAV / WebDAVS** — the one genuinely new protocol worth adding: it
      maps cleanly onto `vfs.FS` (PROPFIND → List, MKCOL → Mkdir, MOVE →
      Rename, ranged GET → Open and resume) and covers Nextcloud, ownCloud and
      IIS. No chmod, so it takes the `vfs.ErrUnsupported` path FTP already uses.
- [ ] **SCP fallback** — only worth it if we actually meet SSH servers with no
      sftp subsystem. Transfer-only; listing would have to shell out to `ls`.
- [ ] **S3 / S3-compatible** — biggest reach, worst fit. No real directories,
      no rename, no settable mtime, no permissions; `domain.Entry` and the
      mirror logic would need "directory" to become synthetic. A deliberate
      decision, not a drive-by.

## Smaller / opportunistic

- [ ] `--host-key-policy` startup flag (form + persistence already done).
- [x] Normalise trailing whitespace in the golden files — `-update` already
      wrote trimmed files (see `assertGolden`); regenerating them for the Tier 3
      work committed the trim, so the churn is gone.
- [x] Symlink handling (navigate + transfer safety). A symlink whose target is
      a directory is marked `LinksToDir` by the local and SFTP adapters (one
      extra `Stat` per link, never per entry) and `Entry.IsDirLike()` makes it
      openable — `enter` used to do nothing at all on one. `Entry.IsDir()`
      deliberately stays false for every symlink, so tree walks and recursive
      deletes still never follow one; the preflight and mirror scans skip
      linked directories and say how many they skipped, rather than queuing a
      transfer that could only fail at open. Still open: *creating* a symlink,
      and a follow-vs-show toggle.
- [ ] `!` to run a shell command in the local pane's directory.
