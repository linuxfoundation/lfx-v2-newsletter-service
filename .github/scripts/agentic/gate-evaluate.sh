#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Gate "Evaluate gate" step. Writes the `approve` output. Env: REPO, PR, HEAD,
# NEEDS_HUMAN_UNLABELERS, GH_TOKEN (the workflow token — this step only
# reads).
#
# CAUTION: the ${V%%<TAB>*} / ${V##*<TAB>} parameter expansions below contain
# LITERAL TAB characters (the TSV field separator). Editors must not convert
# them to spaces.
set -euo pipefail
: "${REPO:?}" "${PR:?}" "${HEAD:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
# The status must be bound to THIS PR, not just this SHA: one head SHA
# can back multiple open PRs (same branch, two bases), and PR A's clean
# must never approve PR B. apply stamps the PR number into the status
# description; require it here. Newest stamp for THIS PR wins, ordered
# by the numeric status id — see newest_clean_state in lib.sh.
CLEAN="$(newest_clean_state "$HEAD" "$PR")"
NEEDS_HUMAN="$(gh pr view "$PR" --repo "$REPO" --json labels \
                --jq 'any(.labels[].name; . == "needs-human")')"
# The escalation condition. A verdict for THIS head decides when one
# exists: label absence is not a completed "no", because the apply run
# that sets the label may not have fired yet when the clean status
# lands. A per-head "yes" verdict withholds until an AUTHORIZED human
# removes the needs-human label AFTER that verdict. Heads pushed while
# the label was set have NO verdict (escalation skips labeled PRs —
# the sticky label cannot be added twice) and carry the skip marker
# status escalation records when it skips; for those, an authorized
# unlabel newer than the head's round marker (the NEWEST pending
# conductor stamp for this head+PR, which on a reoccurring SHA
# belongs to the current occurrence) is the sanctioned override — it
# vouches only for code that existed when the human looked, and the
# next push resumes escalation. The marker is what distinguishes a
# skipped head from one whose verdict is merely still in flight: an
# empty verdict lookup also happens while the judge is running, and
# an unlabel must never approve ahead of an inbound "yes".
# The verdict carries a `head:` marker (see /agentic-comment-format),
# so a stale verdict from an earlier push never vouches for newer
# commits. Exactly one valid yes/no marker, or the verdict is
# malformed and withholds.
#
# Who may unlabel: GitHub has no per-label permissions, so the gate is
# the enforcement point. Only unlabels by the allowlisted logins
# (NEEDS_HUMAN_UNLABELERS, defined once at the job level so this step
# and the pre-approval recheck can never drift) count; an unauthorized
# removal leaves the label gone but the gate withheld, and escalation
# re-labels on the next push. The allowlist lives in the
# default-branch workflow file, so changing it takes a reviewed PR
# through this very pipeline.
# The MOST RECENT unlabel overall must be authorized — the last
# removal is what produced the current label state, and an older
# authorized unlabel must never vouch for a state an unauthorized
# hand created later. Unauthorized (or unattributed) newest event →
# no authorized unlabel at all. Ordered by the numeric timeline
# event id: created_at has second precision, and a same-second tie
# must never fall through to the actor login.
# The timeline read must COMPLETE before any event is trusted —
# see unlabel_events in lib.sh. A failed read therefore leaves
# AUTH_UNLABEL empty, which withholds every branch that needs an
# unlabel — and TL_OK false, which withholds the "no"-verdict branch
# too: with no complete timeline, "no unlabel ever happened" cannot
# be distinguished from "the read failed", and only the former may
# stand on its own.
AUTH_UNLABEL=""; LAST_TS=""; TL_OK=false
if EVENTS="$(unlabel_events "$PR")"; then
  TL_OK=true
  LAST_EVENT="$(printf '%s\n' "$EVENTS" | grep -v '^$' | sort -n | tail -1 || true)"
  if [ -n "$LAST_EVENT" ]; then
    LAST_TS="$(printf '%s' "$LAST_EVENT" | cut -f2)"
    LAST_ACTOR="$(printf '%s' "$LAST_EVENT" | cut -f3)"
    case " $NEEDS_HUMAN_UNLABELERS " in
      *" $LAST_ACTOR "*) [ -n "$LAST_ACTOR" ] && AUTH_UNLABEL="$LAST_TS" ;;
    esac
  fi
fi
# Lookup success is tracked separately from lookup emptiness: a failed
# comments call must never be read as "no verdict exists" — that would
# fail OPEN into the unlabel-fallback branch. Only a successful,
# complete lookup that returns nothing takes that branch.
V_OK=false; V=""
if VRAW="$(needs_human_verdicts "$PR" "$HEAD")"; then
  V_OK=true
  V="$(printf '%s\n' "$VRAW" | grep -v '^$' | sort | tail -1 || true)"
fi
ESC_OK=false
if [ -n "$V" ]; then
  V_TS="${V%%	*}"; V_VAL="${V##*	}"
  if [ "$V_VAL" = "no" ]; then
    # A "no" verdict does not stand on its own: the needs-human
    # label is also a MANUAL override surface (anyone with triage
    # can raise it), and the allowlist invariant — the most recent
    # unlabel overall must be authorized — applies to that flag no
    # matter what the per-head judge said. The verdict prevails
    # only over unlabels OLDER than itself (the judge's later "no"
    # is fresher evidence, and that round already adjudicated the
    # earlier removal); an unlabel AFTER the verdict must be
    # authorized. Requires a complete timeline read (TL_OK).
    if [ "$TL_OK" = "true" ]; then
      if [ -z "$LAST_TS" ] || [ -n "$AUTH_UNLABEL" ] || [ "$V_TS" \> "$LAST_TS" ]; then
        ESC_OK=true
      fi
    fi
  elif [ "$V_VAL" = "yes" ]; then
    if [ -n "$AUTH_UNLABEL" ] && [ "$AUTH_UNLABEL" \> "$V_TS" ]; then
      ESC_OK=true
    fi
  fi
elif [ "$V_OK" = "true" ]; then
  # No verdict for this head (confirmed by a successful lookup):
  # legitimate only when the head arrived while the label was set —
  # which escalation PROVES by recording a skip marker status on the
  # head. No marker means the judge actually ran and its verdict is
  # still in flight (or was lost): withhold, and let the verdict
  # comment (or the next push) re-trigger this gate. A failed marker
  # lookup reads as "no marker" — the fail-closed direction.
  # The round reference is the NEWEST pending stamp for this head+PR
  # (max status id): the conductor stamps pending at every round
  # start, so on a SHA that re-occurs (force-push back, reopen) the
  # newest pending stamp belongs to the CURRENT occurrence — an
  # unlabel from a past occurrence of the same SHA never vouches.
  # The stamp lands seconds after the push; an unlabel inside that
  # tiny window is rejected (fail closed). What recovers it is a
  # FRESH authorized unlabel that lands after the stamp exists —
  # re-adding the label and removing it again mints one — or the
  # next push. (That is unlike the stale-marker case below, where
  # relabeling cannot help because the missing evidence is
  # escalation's, not the human's.)
  ROUND="$(round_pending_stamp "$HEAD" "$PR")"
  ROUND_ID="$(printf '%s' "$ROUND" | cut -f1)"
  ROUND_TS="$(printf '%s' "$ROUND" | cut -f2)"
  # The skip marker must belong to THIS round, not to a past
  # occurrence of a reused SHA whose escalation may be running right
  # now: escalation records the marker only after the round's
  # pending stamp exists (its pre-skip wait), so a genuine skip's
  # marker id is always NEWER than the round stamp's. Compare
  # numeric status ids — both live on the same SHA's append-only
  # status list. A stale marker therefore fails closed here.
  # Deterministic recovery is a push of a NEW SHA (no old stamps to
  # race) or a manual escalation rerun after the current pending
  # stamp exists; a reopen is only a best-effort retry (escalation
  # re-runs, but on a reused SHA its pre-skip wait can be satisfied
  # by the old stamp again); a label toggle merely re-runs this
  # gate.
  SKIP_ID="$(skip_marker_id "$HEAD" "$PR")"
  if [ -n "$SKIP_ID" ] && [ -n "$ROUND_ID" ] && [ "$SKIP_ID" -gt "$ROUND_ID" ] && [ -n "$AUTH_UNLABEL" ] && [ "$AUTH_UNLABEL" \> "$ROUND_TS" ]; then
    ESC_OK=true
  fi
fi
# Tidiness: every review thread (AI or human) must carry at least one
# reply beyond the finding itself, so nothing is dismissed without a
# recorded reason. Resolution state is deliberately ignored — see the
# workflow header. A failed listing must NOT fail this step: the
# approve/dismiss decision below has to be written to the outputs so the
# dismissal backstop still runs — a listing error therefore degrades to
# "untidy" (withhold + dismiss), which is the fail-closed direction for a
# PR that already carries an approval.
THREADS_OK=false
NOREPLY="?"
if N="$(count_unreplied_threads "$PR")"; then
  NOREPLY="$N"
  [ "${NOREPLY:-0}" -eq 0 ] && THREADS_OK=true
else
  echo "::warning::review-thread listing failed; treating the PR as untidy so the verdict stays withheld and stale approvals are dismissed."
fi
echo "clean=$CLEAN needs-human=$NEEDS_HUMAN escalation=${V:-none} esc_ok=$ESC_OK threads-without-reply=$NOREPLY (head $HEAD)" >> "$GITHUB_STEP_SUMMARY"
if [ "$ESC_OK" != "true" ]; then
  echo "Escalation not satisfied: no per-head 'no' verdict, and no authorized needs-human unlabel newer than the verdict (or, for a head escalation skipped — proven by its skip marker status — newer than the head's round start; a verdict-less head WITHOUT the marker means the judge is still in flight and always withholds)." >> "$GITHUB_STEP_SUMMARY"
fi
if [ "$THREADS_OK" != "true" ]; then
  echo "Review threads are not tidy: every thread needs at least one reply before the gate approves." >> "$GITHUB_STEP_SUMMARY"
fi
if [ "$CLEAN" = "success" ] && [ "$NEEDS_HUMAN" != "true" ] && [ "$ESC_OK" = "true" ] && [ "$THREADS_OK" = "true" ]; then
  echo "approve=true" >> "$GITHUB_OUTPUT"
else
  echo "approve=false" >> "$GITHUB_OUTPUT"
  echo "Withholding approval." >> "$GITHUB_STEP_SUMMARY"
fi
