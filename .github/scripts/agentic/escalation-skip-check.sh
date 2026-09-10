#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Escalation "Skip when the needs-human label is already set" step. Writes the
# `skip` output. Env: REPO, PR, HEAD_SHA, GH_TOKEN.
set -euo pipefail
: "${REPO:?}" "${PR:?}" "${HEAD_SHA:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
# The label is sticky and add-only: while it is set, a re-run could
# only conclude "yes" again and re-add what is already there. Skip the
# task; the gate handles heads without a verdict via the authorized
# unlabel timing rule, and runs resume on the first push after a
# human removes the label. A failed label read fails this run (loud)
# rather than guessing.
#
# ORDERING GUARANTEE: before deciding to skip, wait for the
# conductor's pending stamp on this head — the gate's round marker
# for the unlabel-timing rule. This makes the marker precede every
# skip: an unlabel BEFORE this check means the label is already
# absent, so no skip happens and the task produces a normal per-head
# verdict; an unlabel AFTER a skip necessarily postdates the marker
# and is honored by the gate. Without the wait, an unlabel landing
# between a skip and a late stamp would be rejected with no verdict
# ever coming. The gate additionally requires the skip marker to be
# NEWER (by numeric status id) than the round's pending stamp — the
# marker-after-stamp order this wait guarantees for fresh heads. On
# a reused SHA an OLD stamp can satisfy this wait before the current
# round's stamp lands; the resulting too-early marker simply fails
# closed at the gate (withhold). Deterministic recovery is a push
# of a NEW SHA (a fresh head has no old stamps to race) or a
# manual rerun of this workflow after the current pending stamp
# exists; a reopen is only a best-effort retry — it re-runs this
# workflow but the same old-stamp race can recur — and a label
# toggle merely re-runs the gate. If the stamp never appears (conductor failed early),
# fire the task rather than skip — fail open toward running the
# judge, never toward silence.
MARKED=false
for _ in $(seq 1 12); do   # ~60s at 5s
  S="$(clean_stamp_id "$HEAD_SHA" "$PR")"
  if [ -n "$S" ]; then MARKED=true; break; fi
  sleep 5
done
if [ "$MARKED" != "true" ]; then
  echo "::notice::no conductor stamp for this head yet; not skipping (the judge runs)"
  echo "skip=false" >> "$GITHUB_OUTPUT"
  exit 0
fi
# Paginate: the label could sit past the first page on a label-heavy
# PR. Capture first so a SIGPIPE from an early-exiting grep can never
# skew the pipeline status under pipefail; grep -qx matches the whole
# line only.
LABELS="$(gh api "repos/$REPO/issues/$PR/labels" --paginate --jq '.[].name')"
if printf '%s\n' "$LABELS" | grep -qx "needs-human"; then
  # Record the skip as a per-head marker status. The gate's
  # verdict-less fallback honors an authorized unlabel ONLY for
  # heads that carry this marker: a missing verdict alone is
  # ambiguous — the judge could be running right now, and an unlabel
  # must never approve ahead of its inbound "yes". SHA-bound like
  # every other trusted artifact here; written once per skipped
  # push, far below the per-SHA/context status cap. A failed write
  # fails this step loud — no marker and no verdict simply withholds
  # the gate until a push or reopen re-runs this workflow.
  post_status "$HEAD_SHA" success agentic-review/escalation \
    "skipped pr=#$PR: needs-human label set" > /dev/null
  echo "needs-human already set; escalation task skipped (sticky label, nothing to add); skip marker status recorded on $HEAD_SHA." >> "$GITHUB_STEP_SUMMARY"
  echo "skip=true" >> "$GITHUB_OUTPUT"
else
  echo "skip=false" >> "$GITHUB_OUTPUT"
fi
