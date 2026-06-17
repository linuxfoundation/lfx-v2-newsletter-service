#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Run one Codex agent over a brief and capture its verdict JSON. This is the
# exact invocation proven in agents/simulate/simulate.sh: the agent's cwd is its
# own folder (its identity), workspace-write lets it write only inside that
# folder, and reasoning effort is pinned so CI matches the evaluation.
#
# The agent holds no token that can comment, approve, or merge. It only produces
# judgment as data; a separate deterministic step decides how that reaches
# GitHub. The verdict is written outside the agent directory so a compromised
# run cannot tamper with it.
#
# Usage: run-agent.sh <agent-dir-name> <output-json> <brief-file>

set -euo pipefail

AGENT="${1:?agent dir name required}"
OUT="${2:?output json path required}"
BRIEF="${3:?brief file required}"

REPO_ROOT="$(git rev-parse --show-toplevel)"
AGENT_DIR="$REPO_ROOT/agents/$AGENT"
[ -d "$AGENT_DIR" ] || { echo "ERROR: agent dir not found: $AGENT_DIR" >&2; exit 1; }
[ -f "$BRIEF" ] || { echo "ERROR: brief not found: $BRIEF" >&2; exit 1; }

EFFORT="${CODEX_EFFORT:-xhigh}"

codex exec \
  --cd "$AGENT_DIR" \
  --sandbox workspace-write \
  -c model_reasoning_effort="$EFFORT" \
  --output-last-message "$OUT" \
  - < "$BRIEF" > "$OUT.transcript.log" 2>&1
