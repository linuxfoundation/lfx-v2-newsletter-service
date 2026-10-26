#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Gate "Post approving review (lfx-reviewer)" step. Env: REPO, PR, HEAD,
# NEEDS_HUMAN_UNLABELERS, GH_TOKEN (the lfx-reviewer PAT — empty when the
# fetch failed soft). Runs under -e ONLY (no pipefail), mirroring the
# original default-shell step.
#
# CAUTION: the ${V%%<TAB>*} / ${V##*<TAB>} parameter expansions below contain
# LITERAL TAB characters (the TSV field separator). Editors must not convert
# them to spaces.
set -e
: "${REPO:?}" "${PR:?}" "${HEAD:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
if [ -z "${GH_TOKEN:-}" ]; then
  echo "lfx-reviewer PAT unavailable (OIDC role or secret not wired); not approving." >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi
# Approve as lfx-reviewer, but only if it has not already approved this exact
# head, so repeated gate events for the same commit do not stack approvals.
# Paginate and count matching lines (--paginate applies --jq per page, so
# the `[...] | length` form would emit one count per page and break).
already="$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
  --jq ".[] | select(.user.login==\"lfx-reviewer\" and .state==\"APPROVED\" and .commit_id==\"$HEAD\") | .id" \
  | grep -c . || true)"
if [ "${already:-0}" -gt 0 ]; then
  echo "lfx-reviewer already approved $HEAD; no-op." >> "$GITHUB_STEP_SUMMARY"
else
  # Re-validate ALL gate inputs at the last moment, not just the head:
  # status-event and label-event gate runs race on different concurrency
  # keys, so a regression run may have dismissed while this run was
  # queued behind its evaluation. Any change since evaluation aborts
  # (fail closed) — the signal that changed things re-derives on its own.
  esc_ok() {
    # Same rule as Evaluate gate: an explicit per-head "no" (which
    # stands only if the latest needs-human unlabel is authorized,
    # absent, or older than the verdict — the label is also a
    # manual override surface and the allowlist invariant applies
    # regardless of the judge's answer); or a
    # "yes" with a later AUTHORIZED unlabel; or — for a head pushed
    # while the label was set, proven by escalation's per-head skip
    # marker status — an authorized unlabel newer than the head's
    # round start. A verdict-less head WITHOUT the marker means the
    # judge is still in flight and always withholds. Malformed
    # verdicts withhold. The allowlist is the job-level
    # NEEDS_HUMAN_UNLABELERS, shared with Evaluate gate, and — same
    # rule — the MOST RECENT unlabel overall must be the authorized
    # one, ordered by numeric timeline event id (created_at ties at
    # second precision and must never fall through to actor login).
    # Same completeness rule as Evaluate gate: a partial paginated
    # timeline must never authorize (see unlabel_events in lib.sh),
    # and a complete read is a precondition of EVERY branch here —
    # the "no" branch must distinguish "no unlabel ever" from "read
    # failed" — so a failed read withholds outright.
    AUTH_UNLABEL=""; LAST_TS=""
    EVENTS="$(unlabel_events "$PR")" || return 1
    LAST_EVENT="$(printf '%s\n' "$EVENTS" | grep -v '^$' | sort -n | tail -1 || true)"
    if [ -n "$LAST_EVENT" ]; then
      LAST_TS="$(printf '%s' "$LAST_EVENT" | cut -f2)"
      LAST_ACTOR="$(printf '%s' "$LAST_EVENT" | cut -f3)"
      case " $NEEDS_HUMAN_UNLABELERS " in
        *" $LAST_ACTOR "*) [ -n "$LAST_ACTOR" ] && AUTH_UNLABEL="$LAST_TS" ;;
      esac
    fi
    # A failed verdict lookup withholds outright — never read an API
    # error as "no verdict exists" (that would fail open into the
    # unlabel-fallback branch).
    VRAW="$(needs_human_verdicts "$PR" "$HEAD")" || return 1
    V="$(printf '%s\n' "$VRAW" | grep -v '^$' | sort | tail -1 || true)"
    if [ -z "$V" ]; then
      # No verdict: only a head escalation SKIPPED — proven by its
      # skip marker status — may take the unlabel fallback; an empty
      # lookup also happens while the judge's verdict is in flight,
      # and a failed marker lookup reads as "no marker" (fail
      # closed). Newest pending stamp = current occurrence's round
      # start, and the marker must be NEWER than it by numeric
      # status id so a stale marker from a past occurrence of a
      # reused SHA never vouches; same reasoning as Evaluate gate.
      ROUND="$(round_pending_stamp "$HEAD" "$PR")"
      ROUND_ID="$(printf '%s' "$ROUND" | cut -f1)"
      ROUND_TS="$(printf '%s' "$ROUND" | cut -f2)"
      SKIP_ID="$(skip_marker_id "$HEAD" "$PR")"
      [ -n "$SKIP_ID" ] && [ -n "$ROUND_ID" ] && [ "$SKIP_ID" -gt "$ROUND_ID" ] && [ -n "$AUTH_UNLABEL" ] && [ "$AUTH_UNLABEL" \> "$ROUND_TS" ]
      return
    fi
    if [ "${V##*	}" = "no" ]; then
      # The "no" stands only over an unlabel history it can vouch
      # for: latest unlabel absent, authorized, or older than the
      # verdict (same rule as Evaluate gate).
      [ -z "$LAST_TS" ] || [ -n "$AUTH_UNLABEL" ] || [ "${V%%	*}" \> "$LAST_TS" ]
      return
    fi
    [ "${V##*	}" = "yes" ] || return 1
    [ -n "$AUTH_UNLABEL" ] && [ "$AUTH_UNLABEL" \> "${V%%	*}" ]
  }
  threads_ok() {
    # Same rule as Evaluate gate: every thread has at least one reply
    # (resolution ignored). A failed listing withholds (fail closed).
    BAD="$(count_unreplied_threads "$PR")" || return 1
    [ "${BAD:-0}" -eq 0 ]
  }
  recheck() {
    NOW_HEAD="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid)"
    # Full status history, newest stamp for THIS PR by numeric status
    # id — see newest_clean_state in lib.sh.
    NOW_CLEAN="$(newest_clean_state "$HEAD" "$PR")"
    NOW_LABEL="$(gh pr view "$PR" --repo "$REPO" --json labels \
      --jq 'any(.labels[].name; . == "needs-human")')"
    [ "$NOW_HEAD" = "$HEAD" ] && [ "$NOW_CLEAN" = "success" ] && [ "$NOW_LABEL" != "true" ] && esc_ok && threads_ok
  }
  if ! recheck; then
    echo "Gate inputs changed since evaluation (head=$NOW_HEAD clean=$NOW_CLEAN needs-human=$NOW_LABEL); not approving." >> "$GITHUB_STEP_SUMMARY"
    exit 0
  fi
  RID="$(gh api --method POST "repos/$REPO/pulls/$PR/reviews" \
    -f commit_id="$HEAD" -f event=APPROVE \
    -f body="Agentic gate: no blocking AI-review findings remain, every review thread has a reply, and no needs-human label is set. Approving on behalf of the automated review; a human review is still expected." \
    --jq '.id')"
  echo "Gate: APPROVED PR #$PR as lfx-reviewer (head $HEAD, review $RID)." >> "$GITHUB_STEP_SUMMARY"
  # Compensating check: if an input regressed in the instant between the
  # re-check and the POST, revoke our own approval right here instead of
  # waiting for the next signal. Together with branch protection's
  # "dismiss stale approvals on push" this closes the approve-time race.
  if ! recheck; then
    gh api --method PUT "repos/$REPO/pulls/$PR/reviews/$RID/dismissals" \
      -f message="Agentic gate: inputs changed while approving; revoking the automated approval." \
      -f event=DISMISS >/dev/null
    echo "Gate: inputs regressed during approval; self-dismissed review $RID." >> "$GITHUB_STEP_SUMMARY"
  fi
fi
