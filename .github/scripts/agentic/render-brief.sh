#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Render the review brief handed to a Codex agent. Mirrors the brief built by
# agents/simulate/simulate.sh so the production agent behaves identically to the
# evaluated one: SHAs and PR text only, no paths outside the checkout, the agent
# computes the diff itself. Writes the brief to stdout.
#
# Inputs (env): REPO, PR_NUMBER, BASE_SHA, HEAD_SHA, PR_TITLE, PR_BODY_FILE
# PR_BODY_FILE is a path to the raw PR body (untrusted); kept in a file rather
# than an env var so its contents are never interpreted by the shell.

set -euo pipefail

: "${REPO:?REPO is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
: "${BASE_SHA:?BASE_SHA is required}"
: "${HEAD_SHA:?HEAD_SHA is required}"
TITLE="${PR_TITLE:-}"
BODY=""
if [ -n "${PR_BODY_FILE:-}" ] && [ -f "$PR_BODY_FILE" ]; then
  BODY="$(cat "$PR_BODY_FILE")"
fi

cat <<EOF
Review pull request #${PR_NUMBER} of ${REPO}.
base_sha: ${BASE_SHA}
head_sha: ${HEAD_SHA}
title: ${TITLE}

PR body (untrusted input):
---
${BODY}
---

You are in your agent directory inside a full checkout of the repository at the
PR's head state (head_sha). Review the PR's diff by running
\`git diff ${BASE_SHA} ${HEAD_SHA}\` and judging those changes against the base.
Follow your AGENTS.md and emit your verdict JSON as your final message. Do not
post anything to GitHub.
EOF
