# Scripting TideFTP

TideFTP is a terminal app, but the same `tideftp` binary also runs with no
interface: give it a command word and it connects, does the job, prints the
result and exits. That makes it usable from shell scripts, cron and CI, and for
long-running work (big uploads, mirrors) where you want resume, reconnect and a
speed limit rather than a screen.

It speaks **SFTP, FTP and FTPS** (explicit and implicit). It is not an S3 or
HTTP client.

> **Status.** Everything here is covered by tests that run against real
> in-process SFTP and FTP servers. It has not yet been through a wide range of
> third-party servers, so run `--dry-run` first on anything that matters, and
> report oddities.

Contents

1. [Quick start](#1-quick-start)
2. [Connecting](#2-connecting)
3. [Using it from shell scripts](#3-using-it-from-shell-scripts)
4. [Everyday commands](#4-everyday-commands)
5. [Big transfers: resume, reconnect, speed limits](#5-big-transfers-resume-reconnect-speed-limits)
6. [sync and mirror](#6-sync-and-mirror)
7. [Script files and the shell](#7-script-files-and-the-shell)
8. [Recipes](#8-recipes)
9. [Cron and CI](#9-cron-and-ci)
10. [Troubleshooting](#10-troubleshooting)
11. [Reference](#11-reference)

---

## 1. Quick start

```bash
# build once (or use the installed binary)
go build -o tideftp ./cmd/tideftp

# list a directory
tideftp ls --host files.example.com --user deploy /var/www

# upload, then download it back
tideftp put --host files.example.com --user deploy ./build.tar /releases/
tideftp get --host files.example.com --user deploy /releases/build.tar ./copy.tar

# mirror a folder, previewing first
tideftp sync --dry-run ./site prod:/var/www    # 'prod' = a saved profile, see §2
```

Typing `--host --user …` every time is tedious, so save the server once as a
**profile** and use `--profile NAME` (next section).

---

## 2. Connecting

### Profiles

A profile is a saved connection: protocol, host, port, user and a start
directory. They live in `config.toml` (`~/.config/tideftp/config.toml`). Create
one in the app (`tideftp`, press `c`) or write it by hand:

```toml
[[profiles]]
name = "prod"
protocol = "sftp"        # sftp | ftp | ftps | ftps-implicit
host = "files.example.com"
port = 22
user = "deploy"
start_path = "/var/www"  # where relative remote paths begin
```

Then:

```bash
tideftp ls  --profile prod
tideftp get --profile prod logs/app.log ./      # = /var/www/logs/app.log
```

Any flag overrides the profile for that one run: `--profile prod --user other`.

### Flags instead of a profile

```
--protocol sftp|ftp|ftps|ftps-implicit    (default sftp)
--host HOST  --port N  --user NAME  --path START_DIR
--identity ~/.ssh/key     SFTP private key
--known-hosts FILE        SFTP known_hosts to verify against
--host-key-policy strict|off
--ftps-ca FILE  --ftps-insecure  --ftps-allow-tls13
```

### Passwords — never a flag

A password on the command line is visible to every user via `ps`, so it is
deliberately not an option. Use one of:

| Source | How |
|---|---|
| SSH agent / key files | automatic for SFTP; `--identity FILE` to be specific |
| Environment | `TIDEFTP_SFTP_PASSWORD`, `TIDEFTP_FTP_PASSWORD` |
| Standard input | `--password-stdin` (first line) |
| OS keyring | a password saved from the app is read for that profile |

```bash
# from a secret manager
vault read -field=password secret/ftp | tideftp ls --profile prod --password-stdin
```

`--password-stdin` cannot be combined with a script that is itself read from
stdin (both would read the same stream).

The username is sent to the server **exactly as you type it** — nothing is
added. Some servers (cPanel, for instance) want the full `user@domain.com`; others
want a bare name. Use whatever your server expects.

### Host keys (SFTP)

By default an unknown host key **fails** with its fingerprint rather than
waiting for a prompt that a script cannot answer. Connect once with `ssh host`
to add it to `known_hosts`, or pass `--host-key-policy off` to accept any key
(fine for a throwaway test box, not for production).

---

## 3. Using it from shell scripts

### Exit codes

| Code | Meaning |
|---|---|
| `0` | success |
| `1` | an operation failed (including "already exists" without `--force`) |
| `2` | usage error: bad flag or operand |
| `3` | could not connect (network, DNS, refused, unknown host key) |
| `4` | the server rejected the credentials |
| `5` | a named remote path does not exist |

Because they differ, a script can react to the cause:

```bash
tideftp put --profile prod build.tar /releases/
case $? in
  0) echo uploaded ;;
  3) echo "server unreachable - will retry later" ;;
  4) echo "bad credentials - check the secret" >&2; exit 1 ;;
  *) echo "upload failed" >&2; exit 1 ;;
esac
```

### Output

Results go to **stdout**; notices go to **stderr**. `-q` silences everything but
errors. `--progress` adds a live percent / size / speed / ETA line to `get` and
`put` (redrawn in place on a terminal, a plain line every 5 s otherwise).

### JSON

`ls`, `stat` and `find` take `--json`: an array of
`{name, path, type, size, mode, modified}` (`type` is `file`, `dir` or
`symlink`; `modified` is RFC 3339 UTC).

```bash
# newest file in a directory
tideftp ls --json --profile prod /backups | jq -r 'sort_by(.modified) | last | .path'

# total bytes of *.log files
tideftp find --json --name '*.log' --profile prod /var/log | jq 'map(.size) | add'
```

### Testing for existence

`exists` prints nothing; the exit code is the answer (`0` all exist, `5` not).
`-f` requires a regular file, `-d` a directory.

```bash
if tideftp exists -f --profile prod /srv/ready.flag; then
  echo "ready"
fi
```

### Safety nets worth knowing

* Existing destinations are never overwritten without `--force`.
* `put` and `sync` upload to `NAME.part` and rename on success, so a reader of
  the destination never sees a half-written file, and a failed run leaves the
  old file alone. (`--no-part` writes straight to the final name.)
* `sync` never deletes unless you pass `--delete` (see §6).

---

## 4. Everyday commands

```bash
tideftp ls    [-l] [--json] [PATH...]
tideftp stat  [--json] PATH...
tideftp exists [-f|-d] PATH...
tideftp cat   PATH...                      # file contents to stdout (pipe it)
tideftp get   [-r] [-c] [--force] [-O DIR] REMOTE... [LOCAL]
tideftp put   [-r] [-c] [--force] [-p] [-O DIR] LOCAL... [REMOTE]
tideftp mkdir [-p] PATH...        tideftp rmdir PATH...
tideftp rm    [-r] PATH...        tideftp mv OLD NEW
tideftp du    [-h] [-d N] PATH...          # total size, per-directory with -d
tideftp find  [PATH] [--name GLOB] [--type f|d|l] [--min-size 10M] [--max-age 7d] [--maxdepth N] [--json]
tideftp tree  [-L N] [-d] [PATH]
tideftp chmod [-R] 755|u+x,go-w PATH...   # SFTP; FTP has no chmod
tideftp ln -s TARGET LINK                  # SFTP; FTP has no symlinks
tideftp readlink PATH
```

Aliases from lftp: `mget` = `get`, `mput` = `put`, `mrm` = `rm`, `reget` =
`get -c`, `reput` = `put -c`.

### Globs

A wildcard is allowed in the **last** path component. Like a shell, it skips
dotfiles unless the pattern starts with a dot. A pattern that matches nothing
is exit `5` — a typo cannot make a backup "succeed" having copied nothing.
Quote it so your own shell doesn't expand it first:

```bash
tideftp get --profile prod '/logs/*.gz' ./logs
tideftp rm  --profile prod '/tmp/*.part'
tideftp ls  --profile prod '/data/2026-*'
```

### Several sources

With more than one source, or a wildcard, the last operand is the destination
directory — or give it with `-O`:

```bash
tideftp put --profile prod a.txt b.txt c.txt /incoming/
tideftp get --profile prod -O ./out /a/one.txt /b/two.txt
```

A lone `put LOCAL` goes into the profile's start directory.

---

## 5. Big transfers: resume, reconnect, speed limits

### Resume a partial file

If a transfer is interrupted, run it again with `-c` / `--resume` (or use the
`reget` / `reput` spelling). It continues from where the partial file stopped.
Without `-c`, a leftover partial file is thrown away, because the source may
have changed.

```bash
tideftp put --profile prod -c big.7z /backups/
```

### Survive dropped connections

`--retries N` makes a command **redial and carry on** when the connection dies
mid-way, with 1 s, 2 s, 4 s … (max 30 s) backoff, resuming the file in flight.

```bash
tideftp put --profile prod --retries 10 big.7z /backups/
```

* Default: `3` for `sync`, `mirror` and `script`; `0` for the rest. `-1` retries
  forever.
* Only genuine connection trouble is retried. A real answer from the server —
  "no such file", "permission denied", bad password — fails straight away.
* If the source file changed size while you were reconnecting, it stops with an
  error rather than splicing two versions together.
* `--timeout 30s` bounds each connect attempt (default 60 s).

### Limit the speed

```bash
tideftp put --profile prod --bwlimit 2M big.7z /backups/     # 2 MiB/s
tideftp sync --bwlimit 500k --transfers 4 ./data prod:/data   # 500 KiB/s TOTAL
```

One limit is shared by parallel files, so it caps the combined rate.

### Fetch one big file over several connections

```bash
tideftp pget -n 4 --profile prod /backups/huge.iso ./huge.iso
```

Each connection downloads its own slice. Files under about 2 MiB are fetched
normally. After a failure a segmented download restarts rather than resumes.
`sync --use-pget-n 4 --pget-min-size 8M` does this for big files inside a sync.

### Watch it

`--progress` shows percent, bytes, speed and ETA for `get`/`put`.

---

## 6. sync and mirror

`sync SRC DST` makes DST match SRC, one way. `mirror` is the same engine with
lftp's argument shape and short options. Either side can be a **local
directory** or a **server**:

| Written as | Means |
|---|---|
| `./site`, `/abs/dir` | local directory |
| `prod:/var/www` | saved profile `prod`, path `/var/www` |
| `:/var/www` | the server given by `--host` / `--profile` flags |

(Put `./` before a local path that has a colon in it. A one-letter prefix is a
Windows drive, so a profile can't be named with a single letter.) Both sides
remote works too — the bytes relay through a temp file on this machine.

```bash
tideftp sync ./site prod:/var/www                 # upload what changed
tideftp sync prod:/var/log ./logs                 # download
tideftp sync prod:/data staging:/data             # server to server
```

### Always preview first

```bash
tideftp sync --dry-run --delete ./site prod:/var/www
```

`--dry-run` prints one line per action — `copy`, `update`, `delete`, `mkdir` —
and changes nothing.

### How "changed" is decided

* A file is copied if the destination lacks it, or **its size differs**, or the
  **source is more than 2 s newer** (the slack absorbs clock skew).
* `--checksum` compares same-size files by SHA-256 instead of by time (slower,
  but catches same-size edits). `--size-only` / `--ignore-time` ignore time.
* `--only-newer` updates only when the source is newer, never because of size.
  `--only-missing` only adds files; it never replaces one.
* Uploads copy the source's **modification time and permissions** (best effort —
  an FTP server without `MFMT` just keeps its own timestamps); downloads stamp
  the source's time. `--no-perms` leaves modes alone. This is what makes the
  second run a no-op.

### Deleting

Nothing is deleted unless you pass `--delete`, and then:

* deletions run **only after every copy succeeded** (`--delete-first` reverses
  that, to free space first);
* an **empty source refuses to delete anything** unless you add
  `--allow-empty-source` — so a mis-mounted source can't wipe the destination;
* a file that is merely filtered out (by size or age) is **not** treated as
  deleted from the source.

### Filtering

| Flag | Meaning |
|---|---|
| `--include GLOB`, `--exclude GLOB` | name match with no slash; path match with a slash. Repeatable. Excludes win |
| `--include-regex RE`, `--exclude-regex RE` | regular expression on the relative path |
| `--min-size 10k`, `--max-size 5M` | size limits (K/M/G, ×1024) |
| `--min-age 2h`, `--max-age 7d` | by modification age (`s m h d w`) |
| `--newer-than`, `--older-than` | a date (`2026-10-01`) or a file whose mtime to use |
| `--no-recursion` | top level only |
| `--no-empty-dirs` | don't create directories that would stay empty |
| `-L`, `--dereference` (mirror) / `--dereference` (sync) | follow symlinked directories (skipped by default) |

An exclude that names a directory skips the whole subtree.

### Control and reporting

`--transfers N` (alias `--parallel`; default 4) moves N files at once, one
connection each. Also: `--verify` (re-read and hash each file after transfer),
`--max-errors N` (stop starting new files after N failures), `--resume`,
`--on-change 'CMD'` (run a shell command **only if** something changed),
`--log FILE` (append each action with a timestamp).

A failure on one file does not stop the others; the exit code is `1` if
anything failed.

### mirror — the lftp spelling

```bash
tideftp mirror REMOTE [LOCAL]         # download; LOCAL defaults to REMOTE's name
tideftp mirror -R LOCAL [REMOTE]      # upload
```

Its short options follow lftp: `-x`/`--exclude` is a **regular expression** and
`-X`/`--exclude-glob` a glob (`-i`/`-I` likewise), `-n` only-newer, `-e` delete,
`-c` continue, `-P N` parallel, `-L` dereference, `-p` no-perms, `-r`
no-recursion. Use `--dry-run` (or `--just-print`) for the preview.

---

## 7. Script files and the shell

Every command above reconnects, which is slow and can trip servers' login
limits. `script` connects **once** and runs a list of commands over that
connection.

```bash
tideftp script --profile prod deploy.tide           # a file
tideftp script --profile prod -c 'cd /var/www; lcd ./build; put index.html'
tideftp shell  --profile prod                       # interactive
tideftp script --profile prod < deploy.tide         # stdin
```

### A script file

```
# deploy.tide - one command per line
cd /var/www
lcd ./build
mkdir -p releases/new
put -r . releases/new
chmod -R 755 releases/new
mv current previous
mv releases/new current
```

### Language

* One command per line, or several separated by `;`. `#` starts a comment. A
  trailing `\` continues a line.
* Quoting: `'literal'`, `"with \" escapes"`, and `\x` for a single character.
* `!command` runs the rest of the line in your local shell.
* `cmd &` runs `cmd` in the background (see below). `&&` / `||` are not
  supported — the script already stops at the first failure.

### Everything from §4–§6 works, plus these builtins

| Builtin | Does |
|---|---|
| `cd DIR` / `pwd` | remote working directory; relative paths follow it |
| `lcd DIR` / `lpwd` | local working directory |
| `open PROFILE\|HOST` | connect (or switch to) a server; a script may start with no connection at all |
| `close` | disconnect |
| `set NAME VALUE` | change a setting (`set` alone lists them) |
| `alias NAME CMD…` | define a shortcut (`alias` lists, `alias NAME` removes) |
| `source FILE` | run another script |
| `echo [-n] WORDS` | print |
| `help [CMD]` | usage |
| `exit [N] [kill]` | stop with exit code N; `kill` also stops background jobs |

`sync` and `mirror` inside a script use the script's own connection for the
unnamed (`:`) side.

### Settings (`set`)

| Name | Effect |
|---|---|
| `net:limit-rate` / `net:limit-total-rate` | speed cap, e.g. `1M`; `0` = none |
| `net:max-retries` | reconnects per operation; `-1` = unlimited |
| `net:reconnect-interval-base` | first reconnect delay in seconds |
| `net:timeout` | connect timeout, e.g. `30s` |
| `mirror:parallel` | default `--parallel` for `sync`/`mirror` |
| `cmd:fail-exit` | `yes` stop at first failure; `no` keep going |

### Failure handling

A script **stops at the first failing command** and exits with that command's
code. `-k` keeps going and exits with the **first** failure's code (`shell` does
this by default). `-x` echoes each command to stderr before running it.

### Background jobs

```
put big1.iso /backups/ &        # runs on its own connection
put big2.iso /backups/ &
jobs                            # [1] Running  put big1.iso …
wait                            # block until everything finishes
```

* `queue CMD` runs commands **one after another** in the background on a single
  connection — good for "upload these in order while I carry on".
* `wait [N]`, `kill N|all`, `jobs`.
* A script **waits for its jobs at the end**, unless it exits with `exit kill`.
* A job that fails prints `[N] Failed: …` and sets the script's exit code.

### The rc file

A file named `cli.rc` beside `config.toml` (or `--rc FILE`; `--no-rc` skips it)
is read first. It may contain only `set`, `alias`, `echo` and `source` lines —
so it can't connect anywhere on its own.

```
# ~/.config/tideftp/cli.rc
set net:max-retries 5
set net:limit-rate 4M
alias ll ls -l
```

---

## 8. Recipes

### Nightly backup of a remote directory

```bash
#!/bin/sh
set -eu
tideftp mirror --profile prod \
  --delete --parallel 4 --retries 5 \
  --log /var/log/tideftp-backup.log \
  --on-change 'echo "backup changed" | mail -s backup me@example.com' \
  /var/www/uploads /srv/backups/uploads
```

### Deploy a build into a dated release directory

```bash
rel=/var/www/releases/$(date +%F-%H%M)
tideftp script --profile prod -c "
  lcd ./build
  mkdir -p $rel
  mirror -R --delete . $rel
  ln -s -f $rel /var/www/current
"
```

Everything runs over one connection and stops at the first failure, so
`current` is only repointed after the upload succeeded. (`ln -s` needs SFTP.)

### Upload a huge file safely over a bad link

```bash
tideftp put --profile prod --retries -1 --bwlimit 3M --progress -c backup.7z /backups/
```

### Wait for a file to appear, then fetch it

```bash
until tideftp exists -f --profile prod /outbox/report.csv; do sleep 30; done
tideftp get --profile prod /outbox/report.csv ./
```

### Fetch only the last day's logs

```bash
tideftp sync --profile prod --max-age 1d --include '*.log' :/var/log ./logs
```

### Prune old remote files

```bash
tideftp find --json --profile prod --min-age 30d --type f /tmp/exports \
  | jq -r '.[].path' | while read -r p; do tideftp rm --profile prod "$p"; done
```

(Or, in one connection: build a script and feed it to `tideftp script`.)

### Stream a remote file into another tool

```bash
tideftp cat --profile prod /data/dump.sql.gz | gunzip | psql mydb
```

### Several moves over one connection

```bash
tideftp script --profile prod -k -c 'mv a b; mv c d; chmod 640 b d; ls -l'
```

---

## 9. Cron and CI

* **Passwords:** supply them from your secret store as `TIDEFTP_*_PASSWORD` (or
  `--password-stdin`), not in the crontab line.
* **Host keys:** run `ssh host` once as the cron user, or pin it with
  `--known-hosts`. The default `strict` policy will fail loudly otherwise, which
  is what you want.
* **No overlapping runs:** wrap long jobs in `flock`:
  ```
  15 2 * * * flock -n /tmp/tideftp-backup.lock /usr/local/bin/backup.sh
  ```
* **Quiet by default in cron:** add `-q` so only failures produce mail.
* **Exit codes in CI:** any non-zero fails the step; branch on `3`/`4`/`5` if you
  want to retry or alert differently.
* **Long jobs:** use `--retries` so a network blip doesn't fail the whole run,
  and `--log FILE` for an audit trail.

---

## 10. Troubleshooting

| Symptom | Likely cause |
|---|---|
| `login as "name" on host:21: 530 …` | wrong username or password. The name shown is exactly what was sent — check whether your server wants `user` or `user@domain.com` |
| exit `3`, "host key" message | SFTP host key not in `known_hosts`; add it or `--host-key-policy off` |
| exit `4` | credentials rejected; check the env var / keyring entry / key file |
| exit `5` on a glob | the pattern matched nothing (quote it so *your* shell doesn't expand it) |
| `already exists (use --force…)` | add `--force` (or `-c` to continue a partial upload/download) |
| sync copies everything every run | FTP server doesn't report/accept timestamps; use `--size-only` or `--checksum` |
| `chmod`/`ln -s` "not supported" | FTP has neither; use SFTP |
| a script says "not connected" | give `script` a `--host`/`--profile`, or `open PROFILE` first |
| `-r` does something different in `mirror` | in `mirror`, lftp's `-r` means *no recursion*; `get`/`put`/`rm` use `-r` for recursive. `sync` has no `-r` |

`tideftp help COMMAND` shows every flag of a command.

---

## 11. Reference

### Connection flags (every command)

`--profile --protocol --host --port --user --path --identity --known-hosts
--host-key-policy --ftps-ca --ftps-insecure --ftps-allow-tls13`
`--password-stdin --bwlimit RATE --retries N --timeout D --progress -q`

### Coming from lftp

| lftp | TideFTP |
|---|---|
| `lftp -c 'open h; get f'` | `tideftp get --host h f` · `tideftp script --host h -c 'get f'` |
| `mirror -R --delete src dst` | `tideftp mirror -R --delete src dst` |
| `set net:limit-rate 1M` | `--bwlimit 1M` · `set net:limit-rate 1M` |
| `set net:max-retries N` | `--retries N` · `set net:max-retries N` |
| `get -c` / `put -c` / `reget` / `reput` | the same |
| `pget -n 4 f` | `tideftp pget -n 4 f` |
| `mget mput mrm rmdir ln -s find du cat chmod` | the same |
| `cmd &`, `queue`, `jobs`, `wait`, `kill` | the same, inside `script`/`shell` |
| `alias`, `source`, `!cmd`, `~/.lftprc` | `alias`, `source`, `!cmd`, `cli.rc` |

**Not supported:** HTTP/HTTPS, FISH, BitTorrent, proxies, `site`/raw `quote`
commands, FTP `chmod`, hard links, and `&&`/`||`.
