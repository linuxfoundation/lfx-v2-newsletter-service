#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Post the pr-reviewer's findings as resolvable review threads, reconcile them
# against the threads from earlier runs, refresh a sticky summary, and emit
# whether the head commit is clean (no blocking issue remains).
#
# This is the deterministic half of the pr-reviewer: the model decided WHAT to
# say (findings.json); this script decides HOW it reaches GitHub. It holds the
# GITHUB_TOKEN; the model never does.
#
# Inputs (env):
#   REPO        owner/name
#   PR_NUMBER   pull request number
#   FINDINGS    path to the agent's findings.json
#   HEAD_SHA    head commit (for the summary; the status is set by the caller)
#   GITHUB_OUTPUT  optional; "clean=true|false" is appended when set
# Honors AGENTIC_DRY_RUN / missing token via lib.sh (prints instead of mutating).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source-path=SCRIPTDIR
# shellcheck source=lib.sh
. "$HERE/lib.sh"

: "${REPO:?REPO is required}"
: "${PR_NUMBER:?PR_NUMBER is required}"
: "${FINDINGS:?FINDINGS path is required}"
HEAD_SHA="${HEAD_SHA:-}"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# --- 1. Read and validate the agent output ------------------------------------
findings_json="$(ag_read_agent_json "$FINDINGS")"
jq -e 'has("findings") and (.findings | type == "array")' <<<"$findings_json" >/dev/null \
  || ag_die "findings.json missing a findings array"
summary="$(jq -r '.summary // ""' <<<"$findings_json")"

# Render a comment body: severity tag, the finding, an optional suggestion, and
# the hidden fingerprint marker that lets later runs recognize this thread.
render_body() {
  local sev="$1" comment="$2" suggestion="$3" fp="$4" out
  out="**[${sev}]** ${comment}"
  [ -n "$suggestion" ] && [ "$suggestion" != "null" ] && \
    out="${out}"$'\n\n'"_Suggested fix:_ ${suggestion}"
  printf '%s\n\n%s' "$out" "$(ag_marker "$fp")"
}

# --- 2. Fingerprint each finding; render its body -----------------------------
# current_raw.jsonl: {fp, file, line, severity, blocking, body} (one per finding)
current_raw="$TMP/current_raw.jsonl"; : >"$current_raw"
while IFS= read -r f; do
  [ -z "$f" ] && continue
  file="$(jq -r '.file // ""' <<<"$f")"
  line="$(jq -r '.line // 0' <<<"$f")"
  case "$line" in ''|*[!0-9]*) line=0 ;; esac
  sev="$(jq -r '.severity // "should-fix"' <<<"$f")"
  # Strip any agentic markers the model may have planted in finding text.
  comment="$(jq -r '.comment // ""' <<<"$f" | ag_strip_markers)"
  suggestion="$(jq -r '.suggestion // ""' <<<"$f" | ag_strip_markers)"
  fp="$(ag_fingerprint "$file" "$line")"
  blocking=false; ag_is_blocking "$sev" && blocking=true
  body="$(render_body "$sev" "$comment" "$suggestion" "$fp")"
  jq -nc --arg fp "$fp" --arg file "$file" --argjson line "$line" \
        --arg severity "$sev" --argjson blocking "$blocking" --arg body "$body" \
        '{fp:$fp, file:$file, line:$line, severity:$severity, blocking:$blocking, body:$body}' \
        >>"$current_raw"
done < <(jq -c '.findings[]?' <<<"$findings_json")

# Collapse findings that collide on one fingerprint to a single entry, keeping
# the most severe. Makes reconcile and posting key cleanly on fp: no wrong-
# blocking read, no double-post. current.jsonl holds one row per fp.
current="$TMP/current.jsonl"
jq -s 'def rank: {"critical":3,"high":2,"should-fix":1,"nit":0}[.] // -1;
       group_by(.fp) | map(max_by(.severity | rank)) | .[]' \
   "$current_raw" | jq -c . >"$current"
current_fps="$TMP/current_fps.txt"
jq -r '.fp' "$current" | sort -u >"$current_fps"

# clean = no blocking finding in this run's verdict (nits never block).
blocking_count="$(jq -s '[.[] | select(.blocking)] | length' "$current")"
clean=true; [ "$blocking_count" -gt 0 ] && clean=false

# --- 3. Fetch existing threads and recover their fingerprints -----------------
# Fail closed: a fetch error must not look like "no threads" (would duplicate).
if ! threads_raw="$(ag_fetch_threads "$REPO" "$PR_NUMBER")"; then
  ag_die "could not fetch existing review threads; aborting to avoid duplicates"
fi
threads="$TMP/threads.jsonl"; : >"$threads"
thread_fps="$TMP/thread_fps.txt"; : >"$thread_fps"
while IFS= read -r t; do
  [ -z "$t" ] && continue
  fp="$(jq -r '.body // ""' <<<"$t" | ag_marker_of)"
  [ -z "$fp" ] && continue   # not one of ours; never touch human/Copilot threads
  jq -c --arg fp "$fp" '{id, isResolved, fp:$fp}' <<<"$t" >>"$threads"
  printf '%s\n' "$fp" >>"$thread_fps"
done <<<"$threads_raw"
sort -u "$thread_fps" -o "$thread_fps"

in_set() { grep -qxF "$1" "$2" 2>/dev/null; }

# --- 4. Reconcile prior threads (architecture 3.3) ----------------------------
# Reported again + resolved + blocking -> reopen (resolving without a fix should
# not stick). No longer reported -> resolve (the agent cleaning up a fixed
# finding it re-derived as gone). Nits are never reopened.
while IFS= read -r t; do
  [ -z "$t" ] && continue
  id="$(jq -r '.id' <<<"$t")"
  fp="$(jq -r '.fp' <<<"$t")"
  resolved="$(jq -r '.isResolved' <<<"$t")"
  if in_set "$fp" "$current_fps"; then
    # current has one row per fp, so this matches exactly one finding.
    blocking="$(jq -r --arg fp "$fp" 'select(.fp==$fp) | .blocking' "$current")"
    if [ "$blocking" = "true" ] && [ "$resolved" = "true" ]; then
      ag_log "reopen (still present, blocking): $fp"
      ag_unresolve_thread "$id"
    fi
  else
    if [ "$resolved" != "true" ]; then
      ag_log "resolve (fixed, no longer reported): $fp"
      ag_resolve_thread "$id"
    fi
  fi
done <"$threads"

# --- 5. Post new findings (fp not already a thread) ---------------------------
new_inline="$TMP/new_inline.jsonl"; : >"$new_inline"
unanchored="$TMP/unanchored.txt"; : >"$unanchored"
while IFS= read -r c; do
  [ -z "$c" ] && continue
  fp="$(jq -r '.fp' <<<"$c")"
  in_set "$fp" "$thread_fps" && continue   # already has a thread; reconcile handled it
  line="$(jq -r '.line' <<<"$c")"
  if [ "$line" -gt 0 ]; then
    printf '%s\n' "$c" >>"$new_inline"
  else
    # File-level findings cannot anchor to a diff line; list them in the summary.
    printf -- '- %s\n' "$(jq -r '"**["+.severity+"]** "+.file+": "+(.body|gsub("\n";" "))' <<<"$c")" >>"$unanchored"
  fi
done <"$current"

post_inline_review() {
  [ ! -s "$new_inline" ] && return 0
  local comments payload
  comments="$(jq -s 'map({path:.file, line:.line, side:"RIGHT", body:.body})' "$new_inline")"
  payload="$(jq -nc --argjson comments "$comments" '{event:"COMMENT", comments:$comments}')"
  if ag_is_dry_run; then
    ag_log "[dry-run] POST $(jq length <<<"$comments") inline review comment(s) on ${REPO}#${PR_NUMBER}"
    return 0
  fi
  local err="$TMP/review_err.txt"
  if gh api "repos/${REPO}/pulls/${PR_NUMBER}/reviews" --input - <<<"$payload" >/dev/null 2>"$err"; then
    return 0
  fi
  # Batch failed (often a finding line that is not in the diff). Surface the
  # reason so a token/rate-limit problem is distinguishable from a bad line,
  # then retry each comment alone; collect the ones that still fail.
  ag_log "batch review failed ($(head -1 "$err" 2>/dev/null)); retrying comments individually"
  while IFS= read -r c; do
    [ -z "$c" ] && continue
    local one
    one="$(jq -nc --argjson c "$c" '{event:"COMMENT", comments:[{path:$c.file, line:$c.line, side:"RIGHT", body:$c.body}]}')"
    if ! gh api "repos/${REPO}/pulls/${PR_NUMBER}/reviews" --input - <<<"$one" >/dev/null 2>&1; then
      printf -- '- %s\n' "$(jq -r '"**["+.severity+"]** "+.file+":"+(.line|tostring)+" "+(.body|gsub("\n";" "))' <<<"$c")" >>"$unanchored"
    fi
  done <"$new_inline"
}
post_inline_review

# --- 6. Upsert the sticky summary ---------------------------------------------
summary_file="$TMP/summary.md"
{
  printf '## Agentic review\n\n'
  [ -n "$summary" ] && printf '%s\n\n' "$summary"
  # Literal Markdown backticks around the SHA, not shell expansion.
  # shellcheck disable=SC2016
  printf '%s blocking issue(s) on `%s`. ' "$blocking_count" "${HEAD_SHA:-head}"
  if [ "$clean" = true ]; then
    printf 'No blocking issues remain.\n'
  else
    printf 'Resolve the threads above (with fixes) to turn the agentic-review/clean status green.\n'
  fi
  if [ -s "$unanchored" ]; then
    printf '\n**Findings not anchored to a diff line:**\n\n'
    cat "$unanchored"
  fi
  printf '\n%s\n' "$ag_summary_marker"
} >"$summary_file"

upsert_summary() {
  local existing_id
  existing_id=""
  if [ -n "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]; then
    existing_id="$(gh api "repos/${REPO}/issues/${PR_NUMBER}/comments" --paginate \
      --jq ".[] | select(.body | contains(\"$ag_summary_marker\")) | .id" 2>/dev/null | head -1 || true)"
  fi
  if ag_is_dry_run; then
    ag_log "[dry-run] upsert sticky summary ($([ -n "$existing_id" ] && echo "edit $existing_id" || echo create))"
    return 0
  fi
  if [ -n "$existing_id" ]; then
    gh api -X PATCH "repos/${REPO}/issues/comments/${existing_id}" -F body=@"$summary_file" >/dev/null
  else
    gh api "repos/${REPO}/issues/${PR_NUMBER}/comments" -F body=@"$summary_file" >/dev/null
  fi
}
upsert_summary

# --- 7. Emit the clean verdict ------------------------------------------------
ag_log "clean=${clean} (blocking=${blocking_count})"
if [ -n "${GITHUB_OUTPUT:-}" ]; then
  printf 'clean=%s\n' "$clean" >>"$GITHUB_OUTPUT"
fi
printf '%s\n' "$clean"
