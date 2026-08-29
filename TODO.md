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
- [ ] **Directory sync / mirror** — walk both trees, transfer only what differs by
      size/mtime, optionally prune extras. The apex of the recursive-queue +
      conflict-policy work. (`lftp mirror`.)
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

- [ ] **File preview** — `v` for a text / hexdump peek without a full download.
- [ ] **Post-transfer checksum verify.**
- [ ] **Per-file and overall ETA** in the transfer pane (Stats has throughput only).
- [ ] **Copy path to clipboard.**
- [ ] **Auto-reconnect** on a dropped connection (drop detection exists; redial does not).

## Smaller / opportunistic

- [ ] `--host-key-policy` startup flag (form + persistence already done).
- [ ] Normalise trailing whitespace in the golden files (`-update` churns 7 of them).
- [ ] Symlink handling (follow vs show; create).
- [ ] `!` to run a shell command in the local pane's directory.
