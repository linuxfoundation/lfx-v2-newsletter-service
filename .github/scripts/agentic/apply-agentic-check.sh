#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Apply "Apply agentic-check (validate rows + set status)" step. Env: REPO,
# PR, BODY, GH_TOKEN.
#
# Runs under -e ONLY — deliberately NO pipefail, mirroring the original
# default-shell step: the `grep | head` extraction pipelines below rely on
# no-pipefail so a no-match grep falls through to the empty-string malformed
# checks (and their fail-closed `exit 1` paths) instead of killing the script
# before the verdict is judged.
set -e
: "${REPO:?}" "${PR:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
printf '%s' "$BODY" | grep -q '<!-- agentic:check v1 -->' || { echo "no agentic-check block; skip"; exit 0; }

# Parse ONLY the machine block (from its marker to the end of the
# comment, where /agentic-comment-format puts it), so prose in the
# human summary that happens to start a line with "head:", "clean:",
# or "- id:" can never steer the verdict or the status write.
BLOCK=$(printf '%s\n' "$BODY" | sed -n '/<!-- agentic:check v1 -->/,$p')

# Set the status on the SHA the verdict was produced for (carried in the
# block as `head:`), NOT the PR's current head. Otherwise a commit that
# races in between the verdict and this run could mark a newer, unreviewed
# head clean. If the SHA differs from the current head, the gate re-derives
# from the current head (still pending) and fails closed — which is correct.
# Exactly 40 hex chars: /agentic-comment-format requires the full SHA,
# so the parser rejects abbreviated (ambiguous) ones.
TARGET_SHA=$(printf '%s' "$BLOCK" | grep -iE '^[[:space:]]*head:[[:space:]]*[0-9a-f]{40}[[:space:]]*$' | head -1 | grep -oiE '[0-9a-f]{40}' | head -1)
if [ -z "$TARGET_SHA" ]; then
  # No fallback to the current head: a verdict without a valid head: is
  # malformed, and stamping the current (possibly newer, unreviewed) head
  # with it would defeat the SHA binding below. Reject it outright.
  echo "::error::agentic-check block carries no valid head:; refusing to act on this verdict"
  exit 1
fi
# Bind the SHA to THIS pull request: the agent-produced head: is untrusted
# content, and a SHA from some other PR would receive this PR's status (and
# the gate would then evaluate that other PR). Any commit of this PR is
# allowed — a stale head is fine, the gate re-derives from the current head —
# anything else is malformed: fail closed and touch nothing.
if ! gh api "repos/$REPO/pulls/$PR/commits" --paginate --jq '.[].sha' | grep -qxF "$TARGET_SHA"; then
  echo "::error::head: $TARGET_SHA is not a commit of PR #$PR; refusing to act on this verdict"
  exit 1
fi
# Anchored to the whole line, like head:, so prose mentioning "clean:"
# outside the machine block can never set the verdict.
CLEAN=$(printf '%s' "$BLOCK" | grep -iE '^[[:space:]]*clean:[[:space:]]*(true|false)[[:space:]]*$' | head -1 | grep -oiE 'true|false' | tr '[:upper:]' '[:lower:]')
# Cross-check the flag against the thread rows (the parser enforces the
# writer contract): if any listed thread is still blocking, a
# `clean: true` flag is malformed — fail closed and publish failure,
# never success. Reasons are stripped first so free text cannot match.
ROWS_FULL=$(printf '%s\n' "$BLOCK" | grep -E '^[[:space:]]*-[[:space:]]*id:' || true)
ROWS=$(printf '%s\n' "$ROWS_FULL" | sed -E 's/reason:.*//' || true)
BLOCKING_N=0; INVALID_N=0; UNKNOWN_N=0
if [ -n "$ROWS_FULL" ]; then
  BLOCKING_N=$(printf '%s\n' "$ROWS" | grep -ciE 'status:[[:space:]]*(outstanding|rebutted-invalid)([[:space:],]|$)' || true)
  # Schema check: every row must match the FULL documented shape from
  # /agentic-comment-format — nonempty id, allowed status, allowed
  # severity, nonempty reason, in that order. Anything else (a
  # misspelled status, an empty id that would silently skip the
  # membership loop) is malformed and forces the failure state.
  INVALID_N=$(printf '%s\n' "$ROWS_FULL" | grep -vciE '^[[:space:]]*-[[:space:]]*id:[[:space:]]*[^[:space:],]+,[[:space:]]*status:[[:space:]]*(fixed|obsolete|rebutted-valid|outstanding|rebutted-invalid),[[:space:]]*severity:[[:space:]]*(critical|high|should-fix|nit),[[:space:]]*reason:[[:space:]]*[^[:space:]]' || true)
  # Membership check: the agent's thread ids are untrusted too. Restrict
  # mutations to AI-AUTHORED threads of THIS PR (first comment author is
  # one of the AI reviewers), so a wrong or injected id can never touch
  # a human's thread or another PR's. Paginated, so a PR with more than
  # one page of threads never misclassifies later-page ids as foreign.
  # shellcheck disable=SC2016  # $o/$r/$n/$endCursor are GraphQL variables, not shell expansions
  PR_TIDS=$(gh api graphql --paginate -f query='query($o:String!,$r:String!,$n:Int!,$endCursor:String){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id comments(first:1){nodes{author{login}}}}}}}}' \
    -f o="${REPO%%/*}" -f r="${REPO##*/}" -F n="$PR" \
    --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select((.comments.nodes[0].author.login // "") | test("^(copilot(-pull-request-reviewer)?|github-actions)(\\[bot\\])?$"; "i")) | .id')
  for tid in $(printf '%s\n' "$ROWS" | sed -nE 's/.*id:[[:space:]]*([^[:space:],]+).*/\1/p'); do
    printf '%s\n' "$PR_TIDS" | grep -qxF "$tid" || { echo "::warning::thread $tid does not belong to PR #$PR"; UNKNOWN_N=$((UNKNOWN_N+1)); }
  done
fi
if [ "$CLEAN" = "true" ] && [ $((BLOCKING_N + INVALID_N + UNKNOWN_N)) -gt 0 ]; then
  echo "::warning::agentic-check block inconsistent (blocking=$BLOCKING_N invalid=$INVALID_N foreign=$UNKNOWN_N rows); failing closed"
  CLEAN=false
fi
echo "target=$TARGET_SHA clean=$CLEAN blocking=$BLOCKING_N invalid=$INVALID_N foreign=$UNKNOWN_N"

# No per-thread mutations happen here — see the workflow header: no available
# automation token can resolve or re-open review threads, and none is
# needed. Thread state is never authority (the gate reads this block
# and the reply requirement); the rows above were validated for shape
# and PR membership, and the status write below is the entire effect.

STATE=failure
[ "$CLEAN" = "true" ] && STATE=success
# The description carries the PR number: statuses are SHA-scoped, and
# one head SHA can back multiple open PRs, so the gate only honors a
# clean status stamped for the PR it is evaluating.
post_status "$TARGET_SHA" "$STATE" agentic-review/clean \
  "conductor pr=#$PR: clean=$CLEAN"
echo "agentic-review/clean = $STATE on $TARGET_SHA (pr=#$PR)"
