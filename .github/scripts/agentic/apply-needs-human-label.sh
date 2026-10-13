#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Apply "Set sticky needs-human label" step. Env: REPO, PR, BODY, GH_TOKEN.
# Runs under -e ONLY (no pipefail), mirroring the original default-shell step.
set -e
: "${REPO:?}" "${PR:?}"
# Match the exact hidden markers from /agentic-comment-format, not any
# prose that happens to contain "needs-human: yes". Require BOTH the
# format marker and the signal line, so an unrelated trusted comment
# that quotes the signal string can never set the sticky label.
if printf '%s' "$BODY" | grep -qiE '<!--[[:space:]]*agentic:needs-human[[:space:]]+v1[[:space:]]*-->' && \
   printf '%s' "$BODY" | grep -qiE '<!--[[:space:]]*needs-human:[[:space:]]*yes[[:space:]]*-->'; then
  present=$(gh api "repos/$REPO/issues/$PR/labels" --jq '[.[].name] | index("needs-human")')
  if [ "$present" = "null" ] || [ -z "$present" ]; then
    # Bootstrap the repo label idempotently: a fresh repo has no
    # needs-human label and the add-to-issue call does not create it.
    gh api "repos/$REPO/labels/needs-human" >/dev/null 2>&1 || \
      gh api --method POST "repos/$REPO/labels" \
        -f name=needs-human -f color=b60205 \
        -f description="A human must sign off before merge (agentic escalation)" \
        >/dev/null 2>&1 || true
    gh api --method POST "repos/$REPO/issues/$PR/labels" -f "labels[]=needs-human"
    echo "needs-human label set"
  else
    echo "needs-human already present (sticky) — no-op"
  fi
fi
