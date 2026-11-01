# bskyban

Review new Bluesky followers one at a time and block the spam. Works as an
interactive terminal UI ([Bubble Tea](https://github.com/charmbracelet/bubbletea))
or as scriptable CLI commands, from one binary.

By default it only shows **new followers you don't follow back** and haven't
already decided on. For each one you see the profile (age, avatar, post and
follow counts, bio) next to their recent posts, then choose to block or keep.

## Install

Requires Go 1.26+ (see `go.mod`).

```sh
git clone https://github.com/mdfranz/bskyban
cd bskyban
make build        # produces ./bskyban
# or: go install github.com/mdfranz/bskyban@latest
```

## Setup

Create an **app password** in Bluesky: Settings → Privacy and Security → App
Passwords (<https://bsky.app/settings/app-passwords>). Don't use your main
password, and don't enable direct-message access.

```sh
export BSKY_HANDLE=you.bsky.social
export BSKY_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx
```

| Variable | Purpose |
|---|---|
| `BSKY_HANDLE` | your handle (required) |
| `BSKY_APP_PASSWORD` | app password (required; never written to disk) |
| `BSKY_PDS` | PDS URL, default `https://bsky.social` |
| `BSKYBAN_STATE` | state file, default `~/.config/bskyban/state.json` |

## TUI

```sh
bskyban                  # same as: bskyban review
bskyban review --since 20   # only the newest 20 followers
bskyban review --backlog    # also revisit followers you skipped before
```

| Key | Action |
|---|---|
| `b` | block (creates a block on your account immediately) |
| `k` | keep (record as reviewed, no action) |
| `s` | skip (hide from the default view; return with `--backlog`) |
| `u` | undo the last decision (unblocks if it was a block) |
| `q` | quit |

Profile on the left, recent posts on the right (replies, reposts, images, links,
videos and quote posts are labelled). URLs are clickable in terminals that
support OSC 8 hyperlinks. Decisions are saved as you go, so quitting midway
loses nothing.

## CLI

```sh
bskyban list [--since N] [--backlog] [--all] [--json]
bskyban show <handle|did>
bskyban block [--dry-run] <handle|did>...     # "-" reads identifiers from stdin
bskyban keep <handle|did>...
bskyban unblock [--dry-run] <handle|did>...
bskyban baseline
```

- `list` prints new followers; `--backlog` adds skipped ones, `--all` lists everyone with their status.
- `baseline` marks every current unreviewed follower as old, so only followers who arrive afterwards show up by default. Run it once if you don't want to work through your existing backlog.
- Exit codes: `0` ok, `1` error, `2` usage.

Example: dry-run blocking everyone with no posts that follows over 1000 accounts:

```sh
bskyban list --json \
  | jq -r 'select(.posts == 0 and .following > 1000) | .did' \
  | bskyban block --dry-run -
```

## How "new" is decided

A follower is new if you don't follow them back and `state.json` has no entry
for them. `block` and `keep` record a decision; `s` and `baseline` record
"skipped". There is no follow timestamp in the Bluesky API, so this is
tracked locally.

## Development

```sh
make help     # list targets
make test
make run ARGS="--since 10"
```

Talks to the AT Protocol XRPC API directly over `net/http` (no SDK):
`createSession`, `getFollowers`, `getProfile(s)`, `getAuthorFeed`, and
`createRecord`/`deleteRecord` for `app.bsky.graph.block`.
