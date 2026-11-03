#!/usr/bin/env bash
# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Conductor "Detect first round" step. Writes the `first` output. Env: REPO,
# PR, HEAD_SHA, GH_TOKEN.
set -euo pipefail
: "${REPO:?}" "${PR:?}" "${HEAD_SHA:?}"
# The baseline (first) round is deterministic; later rounds need the
# agent. "No prior agentic-check comment" alone is NOT equivalent to
# "first round": a prior run can fail after polling but before posting,
# and a developer can reply to a finding before this step runs. The
# deterministic path is therefore reserved for a strictly fresh state —
# no prior check, no AI review for any EARLIER head, and no replies on
# any AI thread. Anything else goes to the reconcile agent, which can
# actually adjudicate. Every lookup fails LOUD (no suppression): a
# transient listing error must fail this run — defaulting to baseline
# would mechanically mark adjudicable threads outstanding.
COMMENTS="$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  --jq '.[] | select(.user.login=="lfx-reviewer" and (.body | contains("<!-- agentic:check v1 -->"))) | .id')"
PRIOR_N="$(printf '%s\n' "$COMMENTS" | grep -c . || true)"
EARLIER="$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate \
  --jq ".[] | select((.user.login | test(\"^(copilot(-pull-request-reviewer)?(\\\\[bot\\\\])?|github-actions(\\\\[bot\\\\])?)$\"; \"i\")) and .commit_id != \"$HEAD_SHA\") | .id")"
EARLIER_N="$(printf '%s\n' "$EARLIER" | grep -c . || true)"
# shellcheck disable=SC2016  # $o/$r/$n/$endCursor are GraphQL variables, not shell expansions
REPLIED="$(gh api graphql --paginate \
  -f query='query($o:String!,$r:String!,$n:Int!,$endCursor:String){repository(owner:$o,name:$r){pullRequest(number:$n){reviewThreads(first:100,after:$endCursor){pageInfo{hasNextPage endCursor}nodes{comments(first:1){totalCount nodes{author{login}}}}}}}}' \
  -f o="${REPO%%/*}" -f r="${REPO##*/}" -F n="$PR" \
  --jq '.data.repository.pullRequest.reviewThreads.nodes[] | select(((.comments.nodes[0].author.login // "") | test("^(copilot(-pull-request-reviewer)?|github-actions)(\\[bot\\])?$"; "i")) and .comments.totalCount > 1) | 1')"
REPLIED_N="$(printf '%s\n' "$REPLIED" | grep -c . || true)"
if [ "${PRIOR_N:-0}" -eq 0 ] && [ "${EARLIER_N:-0}" -eq 0 ] && [ "${REPLIED_N:-0}" -eq 0 ]; then
  echo "first=true" >> "$GITHUB_OUTPUT"
  echo "baseline round: no prior check, no earlier-head AI review, no thread replies"
else
  echo "first=false" >> "$GITHUB_OUTPUT"
  echo "not a fresh baseline (prior-checks=$PRIOR_N earlier-head-reviews=$EARLIER_N replied-threads=$REPLIED_N); the reconcile agent adjudicates"
fi
