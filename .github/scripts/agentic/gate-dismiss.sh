#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Gate "Dismiss stale approval (withheld verdict)" step. Env: REPO, PR,
# GH_TOKEN (the lfx-reviewer PAT — empty when the fetch failed soft). Runs
# under -e ONLY (no pipefail), mirroring the original default-shell step; the
# "fails LOUDLY" guarantee below depends on -e.
set -e
: "${REPO:?}" "${PR:?}"
if [ -z "${GH_TOKEN:-}" ]; then
  echo "lfx-reviewer PAT unavailable (OIDC role or secret not wired); nothing dismissed." >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi
# If lfx-reviewer approved earlier but the PR has since regressed (a blocking
# finding reappeared, needs-human was set, or a new commit is not yet clean),
# revoke that approval so the gate never leaves stale sign-off on the PR.
# This step is the safety backstop, so it fails LOUDLY: a failed listing or
# a failed dismissal fails the job (bash -e), never a silent green.
ids="$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
  --jq '.[] | select(.user.login=="lfx-reviewer" and .state=="APPROVED") | .id')"
if [ -z "$ids" ]; then
  echo "Gate: WITHHOLD (no prior lfx-reviewer approval to dismiss)." >> "$GITHUB_STEP_SUMMARY"
else
  for id in $ids; do
    gh api --method PUT "repos/$REPO/pulls/$PR/reviews/$id/dismissals" \
      -f message="Agentic gate: PR is no longer clean, has review threads without a reply, or now needs a human; dismissing the automated approval." \
      -f event=DISMISS >/dev/null
    echo "dismissed review $id" >> "$GITHUB_STEP_SUMMARY"
  done
fi
