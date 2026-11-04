#!/usr/bin/env bash
# Smoke test: runs the real CLI against your real Bluesky account WITHOUT changing it.
#
#   BSKY_HANDLE=you.bsky.social BSKY_APP_PASSWORD=xxxx-xxxx-xxxx-xxxx ./scripts/smoke.sh
#
# What it never does:
#   - block, unblock, follow or unfollow for real (they only run with --dry-run)
#   - start the interactive TUI
#   - touch your real state file or audit log (it uses a temp directory)
# What it does write: a throwaway state/audit file in that temp directory, which
# is removed on exit. The only network traffic is read-only requests plus login.

set -u
cd "$(dirname "$0")/.." || exit 2

if [[ -z "${BSKY_HANDLE:-}" || -z "${BSKY_APP_PASSWORD:-}" ]]; then
  echo "set BSKY_HANDLE and BSKY_APP_PASSWORD first" >&2
  exit 2
fi

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
BIN="$TMP/bskyban"
export XDG_STATE_HOME="$TMP/state"
export BSKYBAN_STATE="$TMP/state/state.json"
export BSKYBAN_AUDIT="$TMP/state/audit.jsonl"
STATE="$BSKYBAN_STATE"
AUDIT="$BSKYBAN_AUDIT"

pass=0
fail=0
ok()  { pass=$((pass + 1)); printf '  \033[32mok\033[0m   %s\n' "$1"; }
bad() { fail=$((fail + 1)); printf '  \033[31mFAIL\033[0m %s\n' "$1"; [[ -n "${2:-}" ]] && printf '       %s\n' "$2"; }
section() { printf '\n\033[1m%s\033[0m\n' "$1"; }

# check <description> <condition...>: passes when the command succeeds.
check() { local d=$1; shift; if "$@"; then ok "$d"; else bad "$d"; fi; }

# bsk runs the binary, refusing anything that could change the account or hang on a TUI.
bsk() {
  case "${1:-}" in
    block | unblock | follow | unfollow)
      [[ " $* " == *" --dry-run "* ]] || { echo "smoke.sh refuses: '$1' without --dry-run" >&2; exit 99; }
      ;;
    review | feed | "")
      [[ "${1:-}" == feed && " $* " == *" --list "* ]] || { echo "smoke.sh refuses to start the TUI" >&2; exit 99; }
      ;;
  esac
  "$BIN" "$@"
}

# run <args...> captures stdout in $OUT, stderr in $ERR and the exit code in $RC.
run() {
  OUT=$(bsk "$@" 2>"$TMP/err"); RC=$?
  ERR=$(<"$TMP/err")
}

has() { [[ "$OUT" == *"$1"* ]]; }
lines() { [[ -z "$1" ]] && echo 0 || printf '%s\n' "$1" | wc -l | tr -d ' '; }
field() { grep -o "\"$1\":\"[^\"]*\"" | head -1 | cut -d'"' -f4; }

section "build"
if go build -o "$BIN" ./cmd/bskyban; then ok "go build"; else bad "go build"; exit 1; fi

section "no login needed"
OUT=$(env -u BSKY_HANDLE -u BSKY_APP_PASSWORD "$BIN" help 2>&1); RC=$?
check "help works without credentials (exit 0, shows usage)" bash -c '[[ $1 -eq 0 && $2 == *"Usage"* ]]' _ "$RC" "$OUT"
OUT=$(env -u BSKY_HANDLE -u BSKY_APP_PASSWORD "$BIN" log 2>&1); RC=$?
check "log works without credentials, empty log is fine" test "$RC" -eq 0
OUT=$(env -u BSKY_HANDLE -u BSKY_APP_PASSWORD "$BIN" list 2>&1); RC=$?
check "list without credentials fails cleanly (exit 1)" test "$RC" -eq 1

section "usage errors"
run frobnicate;           check "unknown command exits 2" test "$RC" -eq 2
run block --dry-run;      check "block with no target exits 2" test "$RC" -eq 2
run list --bogus-flag;    check "bad flag exits 2" test "$RC" -eq 2
run log --since soon;     check "bad --since exits 2" test "$RC" -eq 2

section "login + read-only listing"
run list --all --json
if [[ $RC -ne 0 ]]; then
  bad "list --all --json (login or API problem)" "$ERR"
  printf '\n%d passed, %d failed (stopped: cannot reach your account)\n' "$pass" "$fail"
  exit 1
fi
ALL_JSON=$OUT
ALL_N=$(lines "$ALL_JSON")
ok "logged in; list --all --json returned $ALL_N followers"
check "every JSON line has did, handle and status" bash -c '! grep -qvE "^\{.*\"did\":\"did:.*\"handle\":.*\"status\":" <<<"$1"' _ "$ALL_JSON"
if command -v python3 >/dev/null; then
  check "JSON lines parse" python3 -c 'import sys,json; [json.loads(l) for l in sys.stdin if l.strip()]' <<<"$ALL_JSON"
fi

run list;                 NEW_N=$(lines "$OUT"); NEW_OUT=$OUT; check "list exits 0 ($NEW_N new)" test "$RC" -eq 0
run list --backlog;       check "list --backlog exits 0" test "$RC" -eq 0
check "list --all has at least as many as list" test "$ALL_N" -ge "$NEW_N"
run list --since 3;       check "list --since 3 returns at most 3" test "$(lines "$OUT")" -le 3

section "show"
HANDLE=$(printf '%s\n' "$ALL_JSON" | head -1 | field handle)
if [[ -z "$HANDLE" ]]; then
  bad "no follower available to inspect"
else
  run show "$HANDLE"
  check "show @$HANDLE exits 0" test "$RC" -eq 0
  check "show prints DID, Status and Recent posts" bash -c '[[ $1 == *"DID:"* && $1 == *"Status:"* && $1 == *"Recent posts:"* ]]' _ "$OUT"
  run show "@$HANDLE"
  check "show accepts a leading @" test "$RC" -eq 0
  run show "no-such-account-$$.invalid"
  check "show on an unknown account fails (exit 1)" test "$RC" -eq 1
fi

section "dry runs change nothing"
if [[ -n "$HANDLE" ]]; then
  BEFORE=$(bsk show "$HANDLE" | grep '^Status:')
  run block --dry-run "$HANDLE"
  check "block --dry-run exits 0 and says dry-run" bash -c '[[ $1 -eq 0 && $2 == *"dry-run"* ]]' _ "$RC" "$OUT"
  run unblock --dry-run "$HANDLE"
  check "unblock --dry-run exits 0 and says dry-run" bash -c '[[ $1 -eq 0 && $2 == *"dry-run"* ]]' _ "$RC" "$OUT"
  run follow --dry-run "$HANDLE"
  check "follow --dry-run exits 0 and says dry-run or already following" bash -c '[[ $1 -eq 0 && ( $2 == *"dry-run"* || $2 == *"already follow"* ) ]]' _ "$RC" "$OUT"
  run unfollow --dry-run "$HANDLE"
  check "unfollow --dry-run exits 0 and says dry-run" bash -c '[[ $1 -eq 0 && $2 == *"dry-run"* ]]' _ "$RC" "$OUT"
  run follow --dry-run;     check "follow with no target exits 2" test "$RC" -eq 2
  OUT=$(printf '%s\n' "$HANDLE" | bsk block --dry-run - 2>&1); RC=$?
  check "block --dry-run - reads handles from stdin" bash -c '[[ $1 -eq 0 && $2 == *"dry-run"* ]]' _ "$RC" "$OUT"
  AFTER=$(bsk show "$HANDLE" | grep '^Status:')
  check "account status unchanged on the server ($BEFORE)" test "$BEFORE" = "$AFTER"
  check "no local decision recorded by dry runs" bash -c '! test -s "$1" || ! grep -q "\"decision\"" "$1"' _ "$STATE"
fi

section "feed"
run feed --list --refresh 5s;  check "feed rejects a --refresh below 10s (exit 2)" test "$RC" -eq 2
run feed --list --refresh 0;   check "feed accepts --refresh 0 (polling off)" test "$RC" -eq 0
run feed --list --pages 1
check "feed --list --pages 1 exits 0" test "$RC" -eq 0
run feed --list --pages 1 --json
check "feed --list --json lines carry repostedBy" bash -c '[[ -z $1 ]] || ! grep -qv "repostedBy" <<<"$1"' _ "$OUT"

section "local state + audit log (temp files only)"
CAND=$(printf '%s\n' "$NEW_OUT" | awk 'NR==1{print $2}')
if [[ -z "$CAND" ]]; then
  echo "  (no new followers right now; skipping keep/baseline checks)"
else
  run keep "$CAND"
  check "keep @$CAND exits 0" test "$RC" -eq 0
  run list
  check "kept follower disappears from list" bash -c '[[ $1 != *"$2"* ]]' _ "$OUT" "$CAND"
  run list --all
  check "kept follower shows as kept in list --all" bash -c '[[ $1 == *"kept"*"$2"* ]]' _ "$OUT" "$CAND"
fi
run baseline
check "baseline exits 0" test "$RC" -eq 0
run list
check "after baseline nothing is new" test -z "$OUT"
run list --backlog
check "list --backlog brings them back" test "$(lines "$OUT")" -ge "$(( NEW_N > 0 ? NEW_N - 1 : 0 ))"

section "audit log"
check "audit log exists" test -s "$AUDIT"
MODE=$(stat -c '%a' "$AUDIT" 2>/dev/null || stat -f '%Lp' "$AUDIT")
check "audit log is private (0600, is $MODE)" test "$MODE" = 600
check "state file is private (0600)" bash -c '[[ $(stat -c %a "$1" 2>/dev/null || stat -f %Lp "$1") == 600 ]]' _ "$STATE"
run log
check "log shows the dry runs" bash -c '[[ $1 == *"[dry-run]"* ]]' _ "$OUT"
run log --action baseline
check "log --action baseline shows the baseline" bash -c '[[ $1 == *baseline* ]]' _ "$OUT"
run log --json -n 1
check "log --json -n 1 prints one JSON object" bash -c '[[ $(wc -l <<<"$1") -eq 1 && $1 == \{* ]]' _ "$OUT"
check "audit log holds no password or token" bash -c '! grep -qiE "password|accessJwt|eyJ" "$1"' _ "$AUDIT"
check "nothing written under ~/.config/bskyban by this run" bash -c '[[ ! -e "$HOME/.config/bskyban/audit.jsonl" ]]'

printf '\n%d passed, %d failed\n' "$pass" "$fail"
[[ $fail -eq 0 ]]
