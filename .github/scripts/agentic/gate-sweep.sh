#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Gate sweep "Dispatch per-PR gate evaluation" step. Env: REPO, GH_TOKEN (the
# workflow token — dispatch is one of the two events it may trigger),
# GITHUB_REF_NAME.
set -euo pipefail
: "${REPO:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
for PR in $(gh api "repos/$REPO/pulls?state=open&per_page=100" --paginate --jq '.[].number'); do
  HEAD="$(gh api "repos/$REPO/pulls/$PR" --jq '.head.sha')"
  # Only PRs the conductor has stamped for their current head: the
  # gate has no verdict to reach before that, and drafts or foreign
  # PRs stay untouched. Existence check only — the gate re-derives
  # the actual state itself.
  HAS="$(clean_stamp_id "$HEAD" "$PR")"
  [ -n "$HAS" ] || continue
  # One failed dispatch must not starve the rest of the loop. --repo is
  # kept explicit rather than inferred from the sparse checkout's git
  # remote.
  if gh workflow run agentic-gate.yml --repo "$REPO" --ref "${GITHUB_REF_NAME}" -f pr="$PR" 2>/dev/null; then
    echo "dispatched gate for pr=#$PR" >> "$GITHUB_STEP_SUMMARY"
  else
    echo "::warning::could not dispatch gate for PR #$PR"
  fi
done
