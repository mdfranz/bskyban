# bskyban — agent working agreement

Minimal rules for every agent (human-run or autonomous) working in this repo. Iterate here as
conventions settle. Design lives in [ARCHITECTURE.md](ARCHITECTURE.md), usage in
[README.md](README.md), dependencies in [PKG.md](PKG.md); when this file and ARCHITECTURE.md
disagree on design, ARCHITECTURE.md wins.

## Picking up an issue

Several agents work in parallel, often on different machines. Before starting:

1. `git pull` on `main`.
2. Read the issue, its comments, and its **blocked by** links
   (`gh api repos/mdfranz/bskyban/issues/<n>/dependencies/blocked_by`). Don't start a blocked issue.
3. Check nobody else has it: assignees, recent comments, `gh pr list`, and remote branches
   (`gh api repos/mdfranz/bskyban/branches`).
4. Claim it: assign yourself and comment `Agent hostname: <output of hostname>` and
   `Agent Name: <Claude/codex/opencode/agy>`.

While working:

- Prefix every issue/PR comment with your hostname line, so the team can tell agents apart.
- Comment on the issue when you are blocked, when scope changes, or when you find a design
  contradiction. Ask there instead of guessing; don't silently redesign.
- One issue = one branch `issue-<n>-<slug>` (a git worktree is fine) = one PR whose body says
  `Closes #<n>`. Never commit directly to `main`.
- Exception: small, unrelated issues may share one branch (`issue-<n>-<m>-<slug>`) and one PR. Claim
  each issue, put a `Closes #<n>` line per issue in the PR body, and say the PR bundles them. Large or
  risky issues stay in their own PR.
- Keep changes inside the packages the issue concerns. If you must edit a shared file (`go.mod`,
  `go.sum`, `Makefile`, `.github/workflows/ci.yml`, `.golangci.yml`, `internal/testpds`), keep the
  hunk minimal and say so in the PR.
- Unassign yourself and comment if you stop before finishing.

## Reviewing PRs

- Any agent other than the author may review. Prefix the review with your hostname line.
- All agents push as the same GitHub account, and GitHub refuses to let an account approve its own
  PR. Post the review as `gh pr review <n> --comment` stating **LGTM** or the blocking findings; the
  human owner gives the formal approval and merges. (Dependabot and other bot PRs can be approved
  directly with `--approve`.)
- Check that CI is green and that the PR satisfies the definition of done below, not just that the
  diff reads well.

## Environment

- Go version is set by `go.mod` (currently **1.26.11**); don't lower it or add a `toolchain` line.
- Use the Makefile targets: `make build`, `make test`, `make vet`, `make fmt`, `make tidy`.
- Dependency bumps are their own PR (as with Dependabot / `chore/update-go-deps`), never folded into
  a feature or fix. `go.mod` and `go.sum` must stay tidy.

## Definition of done

Mirror CI (`.github/workflows/ci.yml`) locally; all must pass:

- `gofmt -l .` prints nothing
- `go vet ./...`
- `go build ./...`
- `go test -race ./...`
- `go mod tidy` leaves no diff in `go.mod` / `go.sum`
- `golangci-lint run` (v2.15.0, config in `.golangci.yml`)
- `go run golang.org/x/vuln/cmd/govulncheck@latest ./...`

Also: the issue's acceptance checklist is ticked, and the PR description lists anything skipped.
Update README.md / ARCHITECTURE.md when keys, flags, subcommands or package structure change.

## Non-negotiables

- Tests are hermetic: use `internal/testpds` for PDS traffic. No non-loopback network and no real
  Bluesky credentials in tests.
- `make smoke` hits a live account; it is not part of the definition of done, and agents run it only
  when the human asks.
- Credentials (`BSKY_HANDLE`, `BSKY_APP_PASSWORD`) come from the environment; never commit them, and
  never commit `state.json` or `audit.jsonl`.
- Respect the package layering: `cmd/bskyban -> internal/tui, internal/cli -> internal/app ->
  internal/store -> internal/bsky -> internal/termtext`. Packages depend only downward.
- Only `internal/bsky` makes HTTP requests to the PDS (`internal/testpds` is the test-side
  exception).
- `audit.jsonl` is append-only; never rewrite or truncate it.

## Git notes

- Don't block on a commit-signing prompt; if signing is configured globally and gets in the way,
  set `git config --local commit.gpgsign false` in your clone.
- If SSH push/fetch fails, use the gh credential helper over HTTPS:
  `git -c credential.helper= -c credential.helper='!gh auth git-credential' push https://github.com/mdfranz/bskyban.git <branch>`
