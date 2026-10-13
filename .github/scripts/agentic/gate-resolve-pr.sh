#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Gate "Resolve PR" step. Writes the `pr`, `head`, and `found` outputs. Env:
# REPO, EVENT_PR, EVENT_SHA, EVENT_DESC, GH_TOKEN (the workflow token — this
# step only reads).
set -euo pipefail
: "${REPO:?}"
PR=""
if [ -n "$EVENT_PR" ]; then
  PR="$EVENT_PR"
elif [ -n "$EVENT_SHA" ]; then
  # status event: prefer the PR that apply stamped into the status
  # description (pr=#N), verified open — this keeps the gate alive when
  # one SHA backs several open PRs. Fall back to mapping the commit to
  # its single open PR, else fail closed.
  DESC_PR="$(printf '%s' "$EVENT_DESC" | sed -nE 's/.*pr=#([0-9]+):.*/\1/p')"
  if [ -n "$DESC_PR" ]; then
    STATE="$(gh api "repos/$REPO/pulls/$DESC_PR" --jq '.state' 2>/dev/null || true)"
    [ "$STATE" = "open" ] && PR="$DESC_PR"
  fi
  if [ -z "$PR" ]; then
    PRS="$(gh api "repos/$REPO/commits/$EVENT_SHA/pulls" \
            --jq '.[] | select(.state=="open") | .number' 2>/dev/null || true)"
    COUNT="$(printf '%s\n' "$PRS" | grep -c . || true)"
    if [ "${COUNT:-0}" -ne 1 ]; then
      echo "status maps to ${COUNT:-0} open PR(s); need exactly one. Not gating."
      echo "found=false" >> "$GITHUB_OUTPUT"; exit 0
    fi
    PR="$PRS"
  fi
fi
if [ -z "$PR" ]; then echo "found=false" >> "$GITHUB_OUTPUT"; exit 0; fi
# PR is interpolated into API paths below; only a plain number is one.
printf '%s' "$PR" | grep -qE '^[0-9]+$' || { echo "found=false" >> "$GITHUB_OUTPUT"; exit 0; }
HEAD="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid)"
{ echo "pr=$PR"; echo "head=$HEAD"; echo "found=true"; } >> "$GITHUB_OUTPUT"
