#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Conductor "Post the baseline agentic-check deterministically (first round)"
# step. Env: REPO, PR, HEAD_SHA, GH_TOKEN, RUNNER_TEMP.
set -euo pipefail
: "${REPO:?}" "${PR:?}" "${HEAD_SHA:?}"
# shellcheck disable=SC1091  # lib.sh lives next to this script; checked separately
. "$(dirname "$0")/lib.sh"
# The branch may have moved between the poll and this step; a baseline
# posted now would carry a head whose reviews were never awaited. Abort
# quietly: the synchronize run for the new head owns the next round.
exit_if_head_moved
# EVERY thread, AI and human, with its comment count: machine rows are
# built from the AI threads only, but the Remaining tidiness prose must
# mirror the gate's actual predicate — any thread (human included) with
# no reply beyond the finding withholds the approval, and only those
# threads. Resolution state is irrelevant to the gate (no automation
# token can toggle it — see agentic-gate.yml). Severity comes from the
# [severity] prefix the review skills mandate on every finding; an
# unprefixed finding defaults to should-fix (blocking, fail closed)
# and gets adjudicated by the agent in the next round.
# shellcheck disable=SC2016  # $o/$r/$n/$endCursor are GraphQL variables, not shell expansions
TH="$(gh api graphql --paginate \
  -f query='query($o:String!,$r:String!,$n:Int!,$endCursor:String){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{id comments(first:1){totalCount nodes{author{login} path body}}}}}}}' \
  -f o="${REPO%%/*}" -f r="${REPO##*/}" -F n="$PR" \
  --jq '.data.repository.pullRequest.reviewThreads.nodes[] | {id: .id, count: .comments.totalCount, ai: ((.comments.nodes[0].author.login // "") | test("^(copilot(-pull-request-reviewer)?|github-actions)(\\[bot\\])?$"; "i")), path: (.comments.nodes[0].path // ""), body: (.comments.nodes[0].body // "")}')"
# Paths are PR-author-controlled (a hostile filename could smuggle a
# machine marker or break the table), so they get the same reduction
# as finding text before any prose insertion — done inside the jq
# below via the sanitized `spath` field.
BODY_FILE="$RUNNER_TEMP/agentic-check.md"
# The whole comment is assembled in jq: finding text is untrusted, so it
# is reduced to a sanitized one-line excerpt (HTML-comment delimiters
# stripped so quoted text can never open or close a machine marker) and
# only ever lands inside table cells, which the apply parser's anchored
# ^head:/^clean:/^- id: patterns never match.
printf '%s\n' "$TH" | jq -s -r --arg head "$HEAD_SHA" '
  map(. + {sev: (
    if .ai | not then "human"
    elif (.body | test("^\\s*\\[nit\\]"; "i"))       then "nit"
    elif (.body | test("^\\s*\\[critical\\]"; "i"))  then "critical"
    elif (.body | test("^\\s*\\[high\\]"; "i"))      then "high"
    else "should-fix" end)})
  | map(. + {excerpt: (.body
      | gsub("<!--"; "") | gsub("-->"; "") | gsub("\\|"; "/")
      | split("\n") | (first // "")
      | sub("^\\s*\\[(critical|high|should-fix|nit)\\]\\s*"; ""; "i")
      | .[0:120])})
  | map(. + {spath: (.path
      | gsub("<!--"; "") | gsub("-->"; "") | gsub("\\|"; "/") | gsub("`"; "")
      | split("\n") | (first // "") | .[0:120])})
  | (map(select(.ai and .sev != "nit"))) as $blocking
  | (map(select(.ai))) as $aithreads
  # The gate withholds for any thread with no reply beyond the
  # finding; this prose mirrors exactly that predicate.
  | (map(select(.count < 2))) as $untidy
  | [
    "### Agentic review check — " + (if ($blocking | length) == 0 then "✅ clean" else "❌ \($blocking | length) blocking" end),
    "",
    (if ($aithreads | length) == 0
     then "Baseline for the initial review round on `\($head[0:7])`: the AI reviewers reported no findings."
     else "Baseline for the initial review round on `\($head[0:7])`: \($aithreads | length) AI-reviewer finding(s) — \($blocking | length) blocking, \(($aithreads | length) - ($blocking | length)) nit(s). Non-nit findings block until fixed or adjudicated; nits never block and only need an answer on their thread. This baseline is derived directly from the review threads." end),
    (if ($blocking | length) > 0 then
      "\n**Blocking**\n\n| Severity | Finding | Next step |\n| --- | --- | --- |\n" +
      ($blocking | map("| " + .sev + " | `" + .spath + "` — " + .excerpt + " | Fix it, or reply with a substantive rebuttal — both are adjudicated at the next push |") | join("\n"))
     else empty end),
    (if ($blocking | length) == 0 and ($untidy | length) > 0 then
      "\n**Remaining tidiness:** " + ($untidy | length | tostring) + " thread(s) have no reply yet — the gate approves once each is answered (fix it and say so, or reply with the reason it stands as is):\n" +
      ($untidy | map("- " + (if .spath == "" then "discussion" else "`" + .spath + "`" end) + " (thread " + .id + ")") | join("\n"))
     else empty end),
    "",
    "<details>",
    "<summary>Machine ledger (conductor state)</summary>",
    "",
    "<!-- agentic:check v1 -->",
    "head: " + $head,
    "clean: " + (if ($blocking | length) == 0 then "true" else "false" end),
    "threads:",
    ($blocking | map("- id: " + .id + ", status: outstanding, severity: " + .sev + ", reason: New finding from the initial review round; blocks until fixed or adjudicated.") | join("\n")),
    "",
    "</details>"
  ] | map(select(. != null)) | join("\n")
' > "$BODY_FILE"
gh api "repos/$REPO/issues/$PR/comments" -F "body=@$BODY_FILE" --jq '.html_url'
echo "baseline agentic-check posted deterministically (agentic-apply reacts to it)"
