---
name: newsletter-service-agentic-pr
description: >
  How to drive an open lfx-v2-newsletter-service pull request through the
  agentic review flow: read the lfx-reviewer "Agentic review check" comment,
  fix or rebut every blocking finding, answer every review thread, push one
  batched round at a time, and loop until the check is green on the current
  head with every thread answered — then report the ending: needs-human
  review before merge, or clear for the gate/automerge path. Use whenever a
  PR is open on this repo and the work involves review iteration —
  responding to Copilot or conductor findings, checking why
  `agentic-review/clean` is failing or why the gate has not approved,
  handling the `needs-human` label, or pushing fixes to an open PR. This is
  the PR driver's operating manual: the driver is a worktree-isolated
  background agent that works the PR autonomously (fixes, rebuttals,
  replies, pushes). The main session only launches the driver right after
  opening any PR here — with a minimal prompt pointing at this skill — and
  relays its round notes.
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Working a PR through the agentic review flow

> **Who runs this.** Unless a section says otherwise, "you" is the **PR
> driver**: a worktree-isolated background agent that owns the loop end to
> end. The main session reads only "Launching the PR driver" below.

Every push to an open PR on this repo starts a review round with no human in
the loop: Copilot reviews the diff, an escalation judge decides whether a
human must look (`needs-human` label), and the conductor adjudicates every
AI-reviewer thread (human-authored threads are never adjudicated — they only
count for the tidiness rule below) and posts an **Agentic review check**
comment as `lfx-reviewer`, stamping the `agentic-review/clean` commit status
on the head it judged. A deterministic gate approves the PR as `lfx-reviewer`
once four things hold: the current head's clean status is `success`, the
`needs-human` label is absent, every review thread has at least one reply
beyond the finding, and escalation is satisfied for the current head —
normally a per-head `needs-human: no` verdict comment. The gate deliberately
withholds while that verdict is in flight even with the label absent, so a
green check with no approval minutes after a push is usually the judge
finishing, not a fault.

Your job is a loop, not a conversation: **everything you do is adjudicated
at the next push.** Replies, rebuttals, and resolutions never trigger a
round — only pushes and PR open/reopen do. That is deliberate: it keeps
pipeline cost proportional to code changes and keeps anyone from talking the
gate open.

## Setup

Every snippet below assumes these two variables. Assign `PR` first, from the
PR number in your launch prompt — never derive it from branch state: you
work detached (see "Driver operations"), and a detached HEAD has no current
branch for `gh pr view` to resolve. `REPO` comes from the checkout's remote,
which works detached:

```bash
PR=<pr>   # the PR number from your launch prompt
REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
```

The flow covers **same-repository PRs only**: the conductor and escalation
workflows skip fork PRs (their PAT-holding jobs must never run for a head
they do not own). Check before starting the loop:

```bash
gh pr view "$PR" --repo "$REPO" --json isCrossRepository --jq .isCrossRepository
```

If that prints `true`, no round, check comment, or clean status will ever
appear — the PR follows ordinary human review. Tell the user and stop; do
not poll for a verdict that cannot come.

## The round loop

1. **Read the latest check comment** (commands below) and confirm its hidden
   `head:` line matches the PR's current head. A stale check means the
   current head's round is already running or queued (every push starts
   one) — poll for its verdict and wait. Never push just to refresh a stale
   check: that cancels the running round and burns another. On a reused SHA
   (reopen, force-push back) a matching `head:` alone is not proof the check
   is current — anchor on the round's own pending stamp per "Waiting for the
   verdict".
2. **Triage every blocking row.** `[critical]`, `[high]`, and `[should-fix]`
   block; `[nit]` never does. For each blocking finding: fix it, or reply on
   its thread with a substantive technical rebuttal. Both are adjudicated at
   the next push — an accepted rebuttal clears the thread as
   `rebutted-valid`.
3. **Reply on every thread you act on, then resolve it.** Say what you
   changed and how it addresses the finding, or why it stands as is. Then
   resolve the thread (mutation below — your login can, the pipeline's
   tokens cannot). The reply is the audit record and satisfies tidiness;
   resolution just keeps the conversation view clean for the humans who come
   after the gate — adjudication reads the ledger, not the flag, so
   resolving never hides an insufficient fix or rejected rebuttal from being
   carried forward and re-opened.
4. **Answer every remaining thread that has no reply yet**, nits and human
   comments included. The gate withholds while any thread is unanswered,
   even on a clean change — nothing is dismissed without a recorded reason.
5. **Batch the whole round into one push.** Each push burns a Copilot review
   plus a reconcile model run, and a mid-round push strands the running
   round (its eventual comment is head-bound and inert, but it is noise).
   Write all fixes, post all replies, resolve the answered threads, then
   push once:
   - Code changes: one signed, signed-off commit carrying every fix
     (`git commit -s -S`, this repo's DCO + GPG convention).
   - Rebuttal-only (nothing to change in the tree): rebuttals are only
     adjudicated at a push, so push a signed empty commit —
     `git commit --allow-empty -s -S -m "chore(review): submit rebuttals for adjudication"`.
   - Replies-only on an already-clean head: no push at all — the scheduled
     sweep re-runs the gate within ~10 minutes and releases the approval
     once every thread is answered.
6. **Wait for the verdict** on the new head (command below; a round takes
   10–20 minutes), then loop from step 1.
7. **Stop at the goal**: the check reads `✅ clean` on the current head and
   every thread is answered. Report which ending applies per "Endings".

## Endings

Your final report names one of two endings:

- **"Needs human review before merge"** — the goal reached with the
  `needs-human` label set. Green-plus-answered is the ending itself: only a
  human review can move the PR further.
- **"Clear for the gate/automerge path"** — the goal reached plus an
  authoritative current-head no-human signal: the gate's approving review,
  or the head's `needs-human: no` verdict comment with no `needs-human`
  unlabel event postdating it. The gate rejects a verdict that a later
  unlabel supersedes unless the remover was on its allowlist — a check only
  the gate can make — so with any unlabel after the verdict, only the
  actual approval settles it.

Label absence alone is NEITHER ending: escalation may still be in flight,
and a late `yes` verdict can add the label after clean. Keep waiting (the
approval can lag your last reply by up to ~10 minutes — replies do not
trigger the gate, a scheduled sweep does); if you must report first, say
escalation is pending rather than clear.

## Reading the check comment

The newest `lfx-reviewer` comment containing the machine marker is the
authoritative state. The ledger (per-thread adjudications, `head:`,
`clean:`) is collapsed inside a `<details>` element — read the raw body:

```bash
CID="$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  --jq '.[] | select(.user.login=="lfx-reviewer" and (.body | contains("<!-- agentic:check v1 -->"))) | .id' | tail -1)"
if [ -n "$CID" ]; then
  gh api "repos/$REPO/issues/comments/$CID" --jq .body
else
  echo "no agentic check posted yet"
fi
```

On a fresh PR or right after a push, no check exists yet and `CID` is
empty — never call the comments endpoint with an empty id. An absent check
is not an error: the round is running, so switch to the status poll below.

In the body: the headline gives the blocking count, the **Blocking** table
names what stands between you and clean, and the ledger's `- id:` rows
record every adjudicated thread with its status (`fixed`, `obsolete`,
`outstanding`, `rebutted-valid`, `rebutted-invalid`) and reason.
`outstanding` and `rebutted-invalid` rows are your work list.

## Waiting for the verdict

Poll the head's commit status; the conductor stamps `pending` at round start
and `success`/`failure` when the check posts. Statuses are per-PR-bound via
the description, and the newest status id wins. Select the newest first,
then look at its state — filtering `pending` out before selecting would, on
a reused SHA, surface a stale terminal state while the current round's newer
`pending` is the truth:

```bash
HEAD="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid)"
gh api "repos/$REPO/commits/$HEAD/statuses" --paginate \
  --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" \
  | sort -n | tail -1
```

The output is `<status id>\t<state>` — keep both; the id anchors the
reused-SHA rule. Right after a push or reopen that lands an already-seen
SHA, even the newest status can be a PAST occurrence's terminal state (the
current round has not stamped yet), and a matching `head:` on an old check
comment is equally misleading. Anchor each round on its own stamp, by id:

- **About to push**: record the newest id first; this round's verdict is the
  first terminal state whose id is strictly newer than a `pending` that is
  itself newer than what you recorded.
- **Arrived after the event** (reopen or unobserved push): if the newest
  state is `pending`, that is the current round — record its id and wait for
  a newer terminal stamp. If it is terminal, wait ~5 minutes for a newer
  `pending` (the conductor stamps within a couple of minutes of any
  developer event): one appearing means that round owns the verdict; none
  means the terminal stamp is the standing verdict — or the round is dead,
  per the diagnostics below.
- On a brand-new SHA this is automatic — no history to masquerade as.

`pending` or empty output means the round is still running — poll every
minute or so rather than pushing again. But bound the wait: the pending
stamp lands within a couple of minutes of a push, and a round finishes in
10–20. If no PR-bound stamp appears after ~15 minutes, or `pending` outlives
~40, the round is likely dead (failed workflow run, missing PAT wiring,
cancelled mid-poll) and polling will not revive it — inspect the runs and
report what you find rather than pushing again:

```bash
gh run list --repo "$REPO" --workflow=agentic-conductor.yml --limit 5
gh run list --repo "$REPO" --workflow=agentic-escalation.yml --limit 5
```

## Threads: fixing, rebutting, answering, resolving

List threads and find the unanswered ones (`totalCount < 2` means no reply
beyond the finding). Paginate the full connection, exactly as the gate
does — a long PR can carry more than 100 threads, and a work list built from
page one alone cannot satisfy the answer-every-thread rule:

```bash
gh api graphql --paginate -f query='query($owner:String!,$name:String!,$pr:Int!,$endCursor:String){
  repository(owner:$owner,name:$name){pullRequest(number:$pr){
    reviewThreads(first:100,after:$endCursor){
      nodes{id isResolved path
        comments(first:1){totalCount nodes{databaseId author{login} body}}}
      pageInfo{hasNextPage endCursor}}}}}' \
  -f owner="${REPO%/*}" -f name="${REPO#*/}" -F pr="$PR"
```

Reply on a thread (the `databaseId` of its first comment; pass the body as a
file, never interpolated):

```bash
gh api --method POST "repos/$REPO/pulls/$PR/comments/$ROOT_ID/replies" -F body=@reply.md
```

A good rebuttal argues the technical case — why the finding does not apply,
what invariant already covers it, what trade-off is accepted — on the thread
where the reconcile will adjudicate it. "Won't fix" with no reasoning is
adjudicated too, as `rebutted-invalid`, and keeps blocking.

After answering a thread, resolve it. Replies still land on resolved threads
(resolution is a collapse flag, not a lock), so nothing you resolve is
closed to the reconcile's later replies:

```bash
gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{isResolved}}}' -f t="$THREAD_ID"
```

Never @mention the bot reviewers (`copilot`, `lfx-reviewer`) in any
comment — a mention can summon them outside the flow's control.

## The needs-human label

`needs-human` is the human override surface: sticky and add-only. The
escalation judge sets it, and only an allowlisted human's removal counts —
the gate enforces the allowlist, and an unauthorized removal leaves the gate
withheld. It usually means the PR touches approval machinery,
security-sensitive surface, or something the judge could not bound.

The label blocks the **gate approval only — not review rounds**: the
conductor keeps adjudicating every push and stamping `agentic-review/clean`
regardless, so your goal is unchanged — keep driving rounds until the check
is green on the current head. Never remove or toggle the label, and do not
treat it as a bug. Report it to the main session the moment it appears so
the human review it requests can start in parallel with your remaining
rounds; at the goal, report the needs-human ending per "Endings".

## Launching the PR driver (main session)

This section — and only this section — is addressed to the main session.

The loop is mostly waiting, so the moment a PR enters the flow (right after
`gh pr create`, or on finding an open PR that needs iteration), **launch the
PR driver without waiting to be asked**: spawn a general-purpose agent in
the background with worktree isolation and hand it this skill. Tell the user
the driver is on it; the main session stays free for the next piece of work.
Only skip the launch if the user asked to work the loop in this session, and
even then point out the next feature can start in a separate worktree
(`git worktree add ../lfx-v2-newsletter-service-<feature> main`).

Keep the launch prompt minimal — this document is the driver's operating
manual; do not restate its rules. The prompt needs only:

- The instruction to first load this skill (`newsletter-service-agentic-pr`,
  via the Skill tool) — falling back to reading this SKILL.md **by absolute
  path in the main checkout** if the skill is unavailable (never the
  worktree copy, whose snapshot may be stale) — and drive the PR by it to
  its terminal state: a green `agentic-review/clean` check on the current
  head with every thread answered, then report which ending applies.
- The PR number and current head SHA.
- The newest `pr=#<PR>:`-bound `agentic-review/clean` status id as the
  pending anchor if one exists (it also seeds the monitor's round
  baseline), plus any hazard the main session knows about (e.g. the head
  SHA was previously pushed on another PR).

Example prompt: "You are the PR driver for PR #57 on this repo. Load the
`newsletter-service-agentic-pr` skill — or, if unavailable, read
`<main-checkout>/.claude/skills/newsletter-service-agentic-pr/SKILL.md` —
and drive the PR by it to a green check on the current head with every
thread answered, then report which ending applies. Head: `<sha>`. Pending
anchor: status id `<id>`. Do not merge."

Main-session follow-up: each time the driver stops, a task notification
arrives. A result that says it is polling means it will self-resume — do not
duplicate its work. If no further notification arrives within ~50 minutes of
one that promised a poll, nudge the driver with SendMessage (its agent id
stays addressable after it stops). The driver sends two different
`needs-human` messages — treat them differently. An **interim** note that
the label appeared mid-drive is not the loop ending: the driver keeps
driving to the green check; relay the label to the user so their review can
start in parallel. The **final report** with the needs-human ending (green
check on the current head, every thread answered, label still set) IS the
loop ending: do not nudge or restart the driver — relay that only a human
review can move the PR forward. If it reports blocked (a design decision,
non-convergence), relay that to the user and do not restart it blindly.
**Merging is never the driver's job**: a green, gate-approved PR is merged
from the main session only, and only on explicit human instruction.

## Driver operations

You are the driver from here on — a goal-based agent. The goal is a green
`agentic-review/clean` check on the current head with every thread answered;
implement whatever fixes the rounds demand to reach it, within the authority
bounds below. `needs-human` never pauses your rounds — it only selects which
ending your final report announces (see "Endings").

**Worktree discipline.** Git refuses to check out a branch that another
worktree already has, and the main checkout may still be on the PR branch —
so work detached. Derive the branch name from the PR itself (HEAD is
detached, so never from the local checkout), then check out and push by it:

```bash
BRANCH="$(gh pr view "$PR" --repo "$REPO" --json headRefName --jq .headRefName)"
git fetch origin && git checkout --detach "origin/$BRANCH"
# ...commit as usual...
git push origin "HEAD:$BRANCH"
```

**Fix-commit conventions.** `git commit -s -S` (DCO + GPG), conventional
subject (`fix(review): ...`). New `.go` files start with the repo's two-line
license header. Run the checks **before creating the fix commit**, not
after: `export PATH="$(go env GOPATH)/bin:$PATH"` (the Makefile installs
`golangci-lint` into GOPATH/bin, which is not always `$HOME/go/bin`), then
`make check` (fmt + lint + license-check + go vet) and `make test` — all
must pass, and `make check` mutates the tree (it rewrites formatting in
place), so stage whatever it changed before committing. Before pushing,
`git status --porcelain` must be empty — a dirty tree means the commit does
not match the checked state.

**Liveness — one persistent monitor, armed first.** A round takes 10–20
minutes and you must survive every wait unattended. Never rely on one-shot
background polls — they die silently, and silence is never success. As your
**first act**, arm ONE persistent Monitor (`persistent: true`, 2-minute
interval) as your standing wake source and leave it running for the whole
drive. It fingerprints everything that can change the drive's state — the
head, the newest PR-bound stamp, the `needs-human` label, the gate's
approval **for the current head** (approvals are commit-bound: the gate only
honors one whose `commit_id` is the head, and a previous head's approval can
stay visible until the dismissal run completes, so the fingerprint filters
the same way), the current head's escalation verdict, and the
unanswered-thread count across ALL pages (`totalCount < 2`, matching the
gate's tidiness check — resolution state is cosmetic and deliberately not
fingerprinted) — and emits an event on any change, plus `stall` events when
the current round produces no stamp newer than its baseline or `pending`
outlives its deadline, so it stays a wake source after the clean stamp goes
quiet (including when the authoritative `needs-human: no` verdict lands
without an approval); each event re-invokes you:

```bash
REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"; PR=<pr>   # adjust PR
BASE=<anchor>   # newest PR-bound status id from your launch prompt (0 if none):
                # stamps at or below it belong to a previous round or occurrence
prev=""; fails=0; last_head=""; last_stamp="init"; max_seen=$BASE; waits=0
while true; do
  # Each API call is captured and checked on its own — a failed call must
  # increment the failure counter, never masquerade as an empty-but-valid
  # state behind a pipeline whose exit status comes from the parser.
  ok=1
  head=$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid 2>/dev/null) || ok=0
  if [ "$ok" -eq 1 ]; then
    stamps=$(gh api "repos/$REPO/commits/$head/statuses" --paginate --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" 2>/dev/null) || ok=0
  fi
  if [ "$ok" -eq 1 ]; then
    label=$(gh pr view "$PR" --repo "$REPO" --json labels --jq "[.labels[].name] | contains([\"needs-human\"])" 2>/dev/null) || ok=0
  fi
  if [ "$ok" -eq 1 ]; then
    # Approvals are commit-bound: only an APPROVED lfx-reviewer review whose
    # commit_id is the current head counts, exactly as the gate checks it —
    # a stale approval for a previous head must never read as clear.
    approvals=$(gh api "repos/$REPO/pulls/$PR/reviews" --paginate --jq ".[] | select(.user.login==\"lfx-reviewer\" and .state==\"APPROVED\" and .commit_id==\"$head\") | .id" 2>/dev/null) || ok=0
  fi
  if [ "$ok" -eq 1 ]; then
    verdicts=$(gh api "repos/$REPO/issues/$PR/comments" --paginate --jq ".[] | select(.user.login==\"lfx-reviewer\" and (.body | contains(\"agentic:needs-human\")) and (.body | contains(\"head: $head\"))) | .body" 2>/dev/null) || ok=0
  fi
  if [ "$ok" -eq 1 ]; then
    pages=$(gh api graphql --paginate -f query='query($o:String!,$n:String!,$p:Int!,$endCursor:String){repository(owner:$o,name:$n){pullRequest(number:$p){reviewThreads(first:100,after:$endCursor){nodes{comments(first:1){totalCount}} pageInfo{hasNextPage endCursor}}}}}' -f o="${REPO%/*}" -f n="${REPO#*/}" -F p="$PR" --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.comments.totalCount < 2)] | length' 2>/dev/null) || ok=0
  fi
  if [ "$ok" -ne 1 ]; then
    fails=$((fails+1)); [ "$fails" -ge 5 ] && { echo "poll-error: state queries failing repeatedly"; fails=0; }
    sleep 120; continue
  fi
  fails=0
  # Parsing is separate and unchecked on purpose: every lookup above already
  # succeeded, so an empty parse is a VALID state (no stamp yet, no verdict
  # yet — grep finding nothing must read as "none", not as a failure).
  stamp=$(printf '%s\n' "$stamps" | sort -n | tail -1)
  sid=$(printf '%s' "$stamp" | cut -f1); sid=${sid:-0}
  approved=$([ -n "$approvals" ] && echo true || echo false)
  verdict=$(printf '%s\n' "$verdicts" | grep -o "needs-human: [a-z]*" | tail -1)
  unanswered=$(printf '%s\n' "$pages" | awk '{s+=$1} END{print s+0}')
  # Round epoch + deadline heartbeat: a round is live only once a stamp NEWER
  # than the baseline exists. On a reused SHA the newest visible stamp can be
  # a past occurrence's terminal state — never this round's verdict — so an
  # empty stamp and a stale one count toward the same no-stamp deadline. The
  # baseline advances past everything already seen whenever the head moves
  # (status ids are repo-global and monotonic).
  if [ "$head" != "$last_head" ]; then last_head="$head"; BASE=$max_seen; waits=0; fi
  [ "$sid" -gt "$max_seen" ] && max_seen=$sid
  [ "$stamp" != "$last_stamp" ] && { last_stamp="$stamp"; waits=0; }
  if [ "$sid" -le "$BASE" ]; then
    waits=$((waits+1)); [ "$waits" -eq 8 ]  && echo "stall: no stamp newer than the round baseline after ~15m — run the bounded-wait diagnostics"
  else
    case "$stamp" in
      *pending*) waits=$((waits+1)); [ "$waits" -eq 20 ] && echo "stall: pending outlived ~40m — run the bounded-wait diagnostics";;
    esac
  fi
  fp="head=$head stamp=${stamp:-none} label=$label approved=$approved verdict=${verdict:-none} unanswered=$unanswered"
  [ "$fp" != "$prev" ] && { echo "$fp"; prev="$fp"; }
  sleep 120
done
```

It follows the PR's current head across your own pushes. Record `pending`
stamps newer than the baseline as the round's anchor and treat newer
terminal ones as verdicts. A post-clean fingerprint change — the current
head's approval landing, its escalation verdict arriving, the `needs-human`
label appearing, an unanswered-thread count change — is the cue to re-check
that surface directly: the fingerprint is a wake trigger, not the
authoritative read (threads come from the paginated listing, the
label/approval from the PR itself). Repeated query failures emit
`poll-error` events instead of leaving you in permanent silence, and a
round that never produces a stamp newer than its baseline — including the
reused-SHA case where the only visible stamp is an old occurrence's
terminal state — or whose `pending` outlives its deadline emits a `stall`
event, so the dead-round diagnostics are reachable even when the
fingerprint never changes. (A stall right after launching onto an
already-settled round is a prompt to apply the arrival rule in "Waiting for
the verdict", not proof the round is dead.) On each
wake-up: handle the new state and send a one-line round-transition note to
the main session (SendMessage to `main`, e.g. "round 2: 3 blocking;
fixing") so liveness stays visible. End a turn only with a final report, a
must-stop report, or the armed monitor standing watch — never with neither.
Call TaskStop on the monitor before your final report.

**Authority bounds.**

- **May**: read PR state, commit and push fixes for findings whose
  resolution is clear and mechanical, post thread replies and substantive
  rebuttals, resolve the threads you have answered, poll for verdicts.
- **Must stop and report instead of acting** when: a finding requires a
  design decision or its fix is not obviously safe; the same finding family
  survives two of your fix attempts; roughly five rounds pass without
  convergence; or a round appears dead per the bounded-wait diagnostics
  (report the workflow-run evidence, do not push again). The `needs-human`
  label is NOT a stop condition — report it and keep driving per "The
  needs-human label".
- **Must never**: merge — you have no merge authority under any
  circumstances, even fully green and gate-approved — force-push, approve,
  edit other accounts' comments, add or remove labels, or @mention the bots.

**Final report.** State the terminal state — a green check on the current
head with every thread answered — and which ending applies, under exactly
the conditions in "Endings" (with no authoritative signal yet, report
escalation pending rather than clear). If you stopped on a must-stop
condition instead, state what you are blocked on. Either way include a
one-paragraph round-by-round history: what each round's blocking findings
were and whether each was fixed or rebutted.

## Hard rules

- Rounds fire on developer events only — push, open, reopen. Comments and
  replies never start one; do not wait for a reaction to a comment.
- At most one push per round; batch fixes and rebuttals (signed empty commit
  for rebuttal-only rounds, no push for replies-only on a clean head).
- Fix or rebut every blocking finding; answer every thread; say how you
  fixed it or why it stands, then resolve what you answered.
- Never mention the bots; never touch the `needs-human` label.
- Never edit or imitate `lfx-reviewer` comments — the pipeline trusts that
  account's authorship, and the apply step validates everything anyway.
