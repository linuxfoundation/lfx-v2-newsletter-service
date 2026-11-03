#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Conductor "Wait for all AI reviewers to finish for the current head" step.
# Env: REPO, PR, HEAD_SHA, GH_TOKEN. Runs under -e ONLY (no pipefail),
# mirroring the original default-shell step: the `grep -c . || true` counting
# idiom below reasons explicitly about per-page jq output and must keep its
# original pipeline semantics.
set -e
: "${REPO:?}" "${PR:?}" "${HEAD_SHA:?}"
# AI reviewers to wait for, as login regexes. Add 'github-actions' when pi is
# enabled on the repo. We wait only for AI reviewers; humans review on their
# own clock and the conductor never touches human threads.
REVIEWERS='copilot-pull-request-reviewer'
echo "waiting for [$REVIEWERS] to review head $HEAD_SHA"
missing="$REVIEWERS"
for _ in $(seq 1 48); do   # ~12 min at 15s
  missing=""
  for rv in $REVIEWERS; do
    # Emit one line per matching review across ALL pages, then count lines.
    # (gh --paginate applies --jq per page, so `[...] | length` would print
    # one count per page and break the numeric test once reviews span pages.)
    done_n=$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
      --jq ".[] | select((.user.login|test(\"$rv\";\"i\")) and .commit_id==\"$HEAD_SHA\") | .id" \
      2>/dev/null | grep -c . || true)
    [ "${done_n:-0}" -gt 0 ] || missing="$missing $rv"
  done
  if [ -z "$missing" ]; then echo "all AI reviewers done for head"; break; fi
  echo "  still waiting on:$missing"
  sleep 15
done
if [ -n "$missing" ]; then
  # Fail closed: reviewer events cannot trigger another conductor run, so
  # a reconcile fired now would judge an incomplete round and a late
  # review would never be reconciled for this head. Leave the head
  # pending (set above) and fail visibly; the next push retries.
  echo "::error::timed out waiting for reviewers:$missing — failing closed, head stays pending"
  exit 1
fi
