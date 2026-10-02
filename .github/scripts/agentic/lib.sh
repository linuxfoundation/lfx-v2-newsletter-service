# shellcheck shell=bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Shared primitives for the agentic workflows. SOURCED, never executed, and it
# never calls `set`: every function runs under the CALLER's shell flags, so all
# of them must behave identically with and without pipefail/nounset (guard
# optional variables, keep the exact `|| true` placement of the original call
# sites). Functions read $REPO (owner/name) from the environment; everything
# else arrives as arguments. Extracted byte-for-byte from the workflow steps
# that used to inline them — see the semantic-diff evidence in the PR that
# introduced this file.

# Note on the AI-reviewer login regex: the pattern
# ^(copilot(-pull-request-reviewer)?|github-actions)(\[bot\])?$ appears
# literally inside several jq programs in the step scripts. `gh api --jq` has
# no --arg passthrough, so it cannot be centralized as a shell constant
# without changing each program's quoting; the copies are kept byte-for-byte
# instead. Keep them in sync when the reviewer roster changes (grep for
# "copilot(-pull-request-reviewer" across .github/scripts and workflows).

# Newest agentic-review/clean state for this PR on a SHA, ordered by numeric
# status id (created_at has only second precision, so a same-second
# success/failure pair would tie and sort by state instead). Full status
# history (plural endpoint), not the combined status: the combined payload
# keeps only the newest status per context, so with a shared context another
# PR's later stamp on the same SHA would hide this PR's verdict entirely.
# Prints the state or nothing; never fails.
newest_clean_state() { # $1=sha $2=pr
  gh api "repos/$REPO/commits/$1/statuses" --paginate \
    --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$2:\"))) | [(.id|tostring), .state] | @tsv" \
    2>/dev/null | sort -n | tail -1 | cut -f2 || true
}

# Any agentic-review/clean stamp for this PR on a SHA — existence check only
# (first matching id or nothing); never fails.
clean_stamp_id() { # $1=sha $2=pr
  gh api "repos/$REPO/commits/$1/statuses" --paginate \
    --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$2:\"))) | .id" \
    2>/dev/null | head -1 || true
}

# Newest PENDING agentic-review/clean stamp for this PR on a SHA (the round
# marker), as "id<TAB>created_at"; empty when none; never fails.
round_pending_stamp() { # $1=sha $2=pr
  gh api "repos/$REPO/commits/$1/statuses" --paginate \
    --jq ".[] | select(.state==\"pending\" and .context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$2:\"))) | [(.id|tostring), .created_at] | @tsv" \
    2>/dev/null | sort -n | tail -1 || true
}

# Newest escalation skip-marker status id for this PR on a SHA; empty when
# none; never fails.
skip_marker_id() { # $1=sha $2=pr
  gh api "repos/$REPO/commits/$1/statuses" --paginate \
    --jq ".[] | select(.context==\"agentic-review/escalation\" and .state==\"success\" and ((.description // \"\") | contains(\"skipped pr=#$2:\"))) | .id" \
    2>/dev/null | sort -n | tail -1 || true
}

# needs-human unlabel timeline for a PR, one "id<TAB>created_at<TAB>actor" per
# event. PROPAGATES the gh exit status: the timeline read must COMPLETE before
# any event is trusted (--paginate streams page by page, so a later-page
# failure leaves a partial list whose maximum can be an older authorized event
# while the true newest one is missing) — callers decide whether a failed read
# tracks TL_OK=false or returns 1.
unlabel_events() { # $1=pr
  gh api "repos/$REPO/issues/$1/timeline" --paginate \
    --jq '.[] | select(.event=="unlabeled" and .label.name=="needs-human") | [((.id // 0) | tostring), .created_at, (.actor.login // "")] | @tsv' \
    2>/dev/null
}

# needs-human verdict comments bound to a head, one
# "created_at<TAB>yes|no|malformed" per verdict. PROPAGATES the gh exit
# status: a failed comments call must never be read as "no verdict exists".
needs_human_verdicts() { # $1=pr $2=head-sha
  gh api "repos/$REPO/issues/$1/comments" --paginate \
    --jq ".[] | select(.user.login==\"lfx-reviewer\" and (.body | contains(\"<!-- agentic:needs-human v1 -->\")) and (.body | contains(\"<!-- head: $2 -->\"))) | [.created_at, (if ((.body | contains(\"<!-- needs-human: yes -->\")) and (.body | contains(\"<!-- needs-human: no -->\"))) then \"malformed\" elif (.body | contains(\"<!-- needs-human: yes -->\")) then \"yes\" elif (.body | contains(\"<!-- needs-human: no -->\")) then \"no\" else \"malformed\" end)] | @tsv" \
    2>/dev/null
}

# Count of review threads (AI or human) with no reply beyond the finding.
# Prints the count; RETURNS 1 when the listing fails so callers can fail
# closed (withhold) instead of reading an error as "tidy".
count_unreplied_threads() { # $1=pr
  local t
  # shellcheck disable=SC2016  # $owner/$name/$pr/$endCursor are GraphQL variables, not shell expansions
  t="$(gh api graphql --paginate \
    -f query='query($owner:String!,$name:String!,$pr:Int!,$endCursor:String){
      repository(owner:$owner,name:$name){pullRequest(number:$pr){
        reviewThreads(first:100,after:$endCursor){
          nodes{comments(first:1){totalCount}}
          pageInfo{hasNextPage endCursor}}}}}' \
    -f owner="${REPO%/*}" -f name="${REPO#*/}" -F pr="$1" \
    --jq '.data.repository.pullRequest.reviewThreads.nodes[] | (.comments.totalCount|tostring)')" || return 1
  printf '%s\n' "$t" | awk '($1+0)<2 && NF' | grep -c . || true
}

# Fire an Agent Tasks API task pinned to gpt-5.5 (create_pull_request=false,
# base/head refs from the environment) and print the task id.
agent_task_create() { # $1=prompt — uses $REPO, $BASE_REF, $HEAD_REF
  gh api "/agents/repos/$REPO/tasks" \
    -H "X-GitHub-Api-Version: 2026-03-10" --method POST \
    -f model=gpt-5.5 -F create_pull_request=false \
    -f base_ref="$BASE_REF" -f head_ref="$HEAD_REF" \
    -f prompt="$1" \
    --jq '.id'
}

# Quietly abort the calling script (exit 0) when the PR head moved past
# $HEAD_SHA: the synchronize run for the new head owns the next round.
exit_if_head_moved() { # uses $REPO, $PR, $HEAD_SHA
  local now_head
  now_head="$(gh api "repos/$REPO/pulls/$PR" --jq '.head.sha')"
  if [ "$now_head" != "$HEAD_SHA" ]; then
    echo "::notice::head moved ($HEAD_SHA -> $now_head) after the poll; aborting this round"
    exit 0
  fi
}

# Write a commit status. Callers redirect stdout themselves where the original
# step did.
post_status() { # $1=sha $2=state $3=context $4=description
  gh api --method POST "repos/$REPO/statuses/$1" \
    -f state="$2" -f context="$3" -f description="$4"
}
