#!/bin/bash
REPO="linuxfoundation/lfx-v2-newsletter-service"
PR=75
BASE=52001019205

prev=""
fails=0
last_head=""
last_stamp="init"
max_seen=$BASE
waits=0

while true; do
  ok=1
  head=$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid 2>/dev/null) || ok=0
  stamps=""
  label="false"
  approvals=""
  verdicts=""
  pages=0

  if [ "$ok" -eq 1 ]; then
    stamps=$(gh api "repos/$REPO/commits/$head/statuses" --paginate --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" 2>/dev/null) || ok=0
  fi

  if [ "$ok" -eq 1 ]; then
    label=$(gh pr view "$PR" --repo "$REPO" --json labels --jq "[.labels[].name] | contains([\"needs-human\"])" 2>/dev/null) || ok=0
  fi

  if [ "$ok" -eq 1 ]; then
    approvals=$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate --jq ".[] | select(.user.login==\"lfx-reviewer\" and .state==\"APPROVED\" and .commit_id==\"$head\") | .id" 2>/dev/null) || ok=0
  fi

  if [ "$ok" -eq 1 ]; then
    verdicts=$(gh api "repos/$REPO/issues/$PR/comments" --paginate --jq ".[] | select(.user.login==\"lfx-reviewer\" and (.body | contains(\"agentic:needs-human\")) and (.body | contains(\"head: $head\"))) | .body" 2>/dev/null) || ok=0
  fi

  if [ "$ok" -eq 1 ]; then
    pages=$(gh api graphql --paginate -f query='query($o:String!,$n:String!,$p:Int!,$endCursor:String){repository(owner:$o,name:$n){pullRequest(number:$p){reviewThreads(first:100,after:$endCursor){nodes{comments(first:1){totalCount}} pageInfo{hasNextPage endCursor}}}}}' -f o="linuxfoundation" -f n="lfx-v2-newsletter-service" -F p="$PR" --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.comments.totalCount < 2)] | length' 2>/dev/null) || ok=0
  fi

  if [ "$ok" -ne 1 ]; then
    fails=$((fails+1))
    if [ "$fails" -ge 5 ]; then
      echo "poll-error: state queries failing repeatedly"
      fails=0
    fi
    sleep 120
    continue
  fi

  fails=0
  stamp=$(printf '%s\n' "$stamps" | sort -n | tail -1)
  sid=$(printf '%s' "$stamp" | cut -f1)
  sid=${sid:-0}
  approved=$([ -n "$approvals" ] && echo true || echo false)
  verdict=$(printf '%s\n' "$verdicts" | grep -o "needs-human: [a-z]*" | tail -1)
  unanswered=$(printf '%s' "$pages" | awk '{s+=$1} END{print s+0}')

  if [ "$head" != "$last_head" ]; then
    last_head="$head"
    BASE=$max_seen
    waits=0
  fi

  [ "$sid" -gt "$max_seen" ] && max_seen=$sid
  [ "$stamp" != "$last_stamp" ] && { last_stamp="$stamp"; waits=0; }

  if [ "$sid" -le "$BASE" ]; then
    waits=$((waits+1))
    if [ "$waits" -eq 8 ]; then
      echo "stall: no stamp newer than the round baseline after ~15m"
    fi
  else
    case "$stamp" in
      *pending*) waits=$((waits+1)); [ "$waits" -eq 20 ] && echo "stall: pending outlived ~40m";;
    esac
  fi

  fp="head=$head stamp=${stamp:-none} label=$label approved=$approved verdict=${verdict:-none} unanswered=$unanswered"
  if [ "$fp" != "$prev" ]; then
    echo "$fp"
    prev="$fp"
  fi

  sleep 120
done
