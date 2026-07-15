---
name: newsletter-service-agentic-pr
description: >
  How to drive an open lfx-v2-newsletter-service pull request through the
  agentic review flow: read the lfx-reviewer "Agentic review check" comment,
  fix or rebut every blocking finding, answer every review thread, push one
  batched round at a time, wait for the conductor's verdict, and loop until
  the check is green and the gate approves. Use this whenever a PR is open on
  this repo and the work involves review iteration — responding to Copilot or
  conductor findings, checking why `agentic-review/clean` is failing or why
  the gate has not approved, handling the `needs-human` label, or pushing
  fixes to an open PR. This document is the PR driver's operating manual:
  the loop is executed by the PR driver, a worktree-isolated background
  agent that works the PR autonomously (fixes, rebuttals, replies, pushes).
  The main session's job is only to launch the driver right after opening
  any PR here — with a minimal prompt pointing at this skill — and relay
  its round notes.
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Working a PR through the agentic review flow

> **Who runs this.** This document is the **PR driver's** operating manual —
> unless a section says otherwise, "you" is the driver: a worktree-isolated
> background agent that owns the loop end to end. The main session reads only
> "Launching the PR driver" below; everything else is addressed to the
> driver.

Every push to an open PR on this repo starts a review round with no human in
the loop: Copilot reviews the diff, an escalation judge decides whether a
human must look (`needs-human` label), and the conductor adjudicates every
AI-reviewer thread — human-authored threads are never adjudicated, they only
count for the tidiness rule below — and posts an **Agentic review check**
comment as `lfx-reviewer`,
stamping the `agentic-review/clean` commit status on the head it judged. A
deterministic gate approves the PR as `lfx-reviewer` once four things hold:
the current head's clean status is `success`, the `needs-human` label is
absent, every review thread has at least one reply beyond the finding, and
escalation is satisfied for the current head — normally a per-head
`needs-human: no` verdict comment. The gate deliberately withholds while that
verdict is still in flight even with the label absent, so a green check with
no approval a few minutes after a push is usually just the judge finishing,
not a fault.

Your job on the developer side is therefore a loop, not a conversation:
**everything you do is adjudicated at the next push.** Replies, rebuttals, and
resolutions never trigger a round by themselves — only pushes (and PR
open/reopen) do. That is deliberate: it keeps the pipeline's cost proportional
to code changes and keeps humans from being able to talk the gate open.

## Setup

Every snippet below assumes these two variables, derived from the PR branch
checkout:

```bash
REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"
PR="$(gh pr view --json number --jq .number)"
```

The flow covers **same-repository PRs only**: the conductor and escalation
workflows explicitly skip fork PRs (their PAT-holding jobs must never run for
a head they do not own). Check before starting the loop:

```bash
gh pr view "$PR" --repo "$REPO" --json isCrossRepository --jq .isCrossRepository
```

If that prints `true`, no round will ever run and no check comment or clean
status will appear — the PR follows ordinary human review. Tell the user and
stop; do not poll for a verdict that cannot come.

## The round loop

1. **Read the latest check comment** (see commands below). Confirm its hidden
   `head:` line matches the PR's current head. A check for an older head is
   stale, and the current head's round is already running or queued (every
   push starts one) — poll for its verdict and wait. Never push just to
   refresh a stale check: that cancels the running round and burns another.
   On a reused SHA (reopen, force-push back) a matching `head:` alone is not
   proof the check is current — an old occurrence's check carries the same
   SHA; anchor on the current round's own pending stamp per the reused-SHA
   note in the waiting section.
2. **Triage every blocking row.** `[critical]`, `[high]`, and `[should-fix]`
   findings block; `[nit]` never blocks. For each blocking finding decide: fix
   it, or reply on its thread with a substantive technical rebuttal. Both
   paths are legitimate and both are adjudicated at the next push — a rebuttal
   the reconcile accepts clears the thread as `rebutted-valid`.
3. **Reply on every thread you act on, then resolve it.** For a finding you
   fixed, say what you changed and how it addresses the finding; for one that
   does not need fixing, say why it stands as is. Then resolve the thread
   (mutation below — your own login can, the pipeline's tokens cannot). The
   reply is the audit record on the thread itself and satisfies tidiness
   immediately; the resolution keeps the conversation view clean for the
   human reviewers who come after the gate. The pipeline ignores resolution
   state, so resolving never hides anything from adjudication — an
   insufficient fix or rejected rebuttal is carried forward and re-opened in
   the ledger regardless.
4. **Answer every remaining thread that has no reply yet**, nits and human
   comments included. The gate withholds its approval while any thread is
   unanswered, even on a clean change — nothing gets dismissed without a
   recorded reason.
5. **Batch the whole round into one push.** Each push burns a Copilot review
   plus a reconcile model run, and a mid-round push strands the running round
   (its eventual comment is head-bound and inert, but it is noise). Write all
   fixes, post all replies, resolve the answered threads, then push once.
   What that push is depends on the round:
   - Code changes: one signed and signed-off commit carrying every fix
     (`git commit -s -S`, this repo's DCO + GPG convention).
   - Rebuttal-only (nothing to change in the tree): rebuttals are only
     adjudicated at a push, so push a signed empty commit —
     `git commit --allow-empty -s -S -m "chore(review): submit rebuttals for adjudication"`.
   - Replies-only on an already-clean head (answering nits or human threads
     when nothing blocks): no push at all — the scheduled sweep re-runs the
     gate within ~10 minutes and releases the approval once every thread is
     answered.
6. **Wait for the verdict** on the new head (command below; a round typically
   takes 10–20 minutes), then loop from step 1.
7. **Stop when the goal is reached**: the check reads `✅ clean` on the
   current head and every thread is answered. Your final report states which
   ending applies. With `needs-human` present, green-plus-answered is the
   ending itself: a human must review before this PR can merge (see "The
   needs-human label"). With the label absent, absence alone is NOT the
   ending — escalation may still be in flight and a late `yes` verdict can
   add the label after clean. Report "clear for the gate/automerge path"
   only once an authoritative current-head signal exists: the gate's
   approving review, or the head's `needs-human: no` verdict comment. Until
   one appears, keep waiting (the approval can lag your last reply by up to
   ~10 minutes — reply events do not trigger the gate, a scheduled sweep
   does) and, if you must report first, say escalation is pending rather
   than clear.

## Reading the check comment

The newest `lfx-reviewer` comment containing the machine marker is the
authoritative state. The ledger (per-thread adjudications, `head:`, `clean:`)
is collapsed inside a `<details>` element — read the raw body, not the
rendered page.

```bash
CID="$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  --jq '.[] | select(.user.login=="lfx-reviewer" and (.body | contains("<!-- agentic:check v1 -->"))) | .id' | tail -1)"
if [ -n "$CID" ]; then
  gh api "repos/$REPO/issues/comments/$CID" --jq .body
else
  echo "no agentic check posted yet"
fi
```

On a fresh PR, or in the minutes right after a push, no check exists yet and
`CID` is empty — never call the comments endpoint with an empty id (the URL
would be malformed). An absent check is not an error state: the round is
running, so switch to the status poll below until it completes.

In the body: the headline gives the blocking count, the **Blocking** table
names what stands between you and clean, and the ledger's `- id:` rows record
every adjudicated thread with its status (`fixed`, `obsolete`, `outstanding`,
`rebutted-valid`, `rebutted-invalid`) and the reason. `outstanding` and
`rebutted-invalid` rows are your work list.

## Waiting for the verdict

Poll the head's commit status; the conductor stamps `pending` at round start
and `success`/`failure` when the check posts. Statuses are per-PR-bound via
the description, and the newest status id wins. Select the newest first and
only then look at its state — filtering `pending` out before selecting would,
on a reused SHA (reopen, force-push back), surface a stale `success`/`failure`
from a past occurrence while the current round's newer `pending` is the truth:

```bash
HEAD="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid)"
gh api "repos/$REPO/commits/$HEAD/statuses" --paginate \
  --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" \
  | sort -n | tail -1
```

The output is `<status id>\t<state>` — keep both, the id is the anchor for
the reused-SHA rule below.

One more reused-SHA hazard: right after a push or reopen that lands an
already-seen SHA, even the newest status can still be a PAST occurrence's
terminal `success`/`failure`, because the current round has not stamped yet —
and the matching `head:` on an old check comment is equally misleading (the
SHA is the same). Anchor each round on its own stamp, by status id:

- **You are about to push**: record the newest id first; this round's verdict
  is the first terminal state whose id is strictly newer than a `pending`
  that is itself newer than what you recorded.
- **You arrived after the event** (reopen or push you did not observe): if
  the newest state is `pending`, that is the current round — record its id
  and wait for a newer terminal stamp. If it is terminal, wait ~5 minutes
  for a newer `pending` to appear (the conductor stamps within a couple of
  minutes of any developer event): if one appears, that round owns the
  verdict; if none does, either the terminal stamp is the standing verdict
  (no newer event happened) or the round is dead — apply the bounded-wait
  diagnostics below.
- On a brand-new SHA this is automatic — it has no history to masquerade as.

`pending` or empty output means the round is still running — poll every
minute or so rather than pushing again. But bound the wait: the pending stamp
lands within a couple of minutes of the push, and a round normally finishes
in 10–20. If no PR-bound stamp has appeared after ~15 minutes, or `pending`
outlives ~40, the round is likely dead (failed workflow run, missing PAT
wiring, cancelled mid-poll) and no amount of polling revives it — inspect
the runs instead and report what you find rather than pushing again:

```bash
gh run list --repo "$REPO" --workflow=agentic-conductor.yml --limit 5
gh run list --repo "$REPO" --workflow=agentic-escalation.yml --limit 5
```

## Threads: fixing, rebutting, answering, resolving

List threads and find the unanswered ones (`totalCount < 2` means the finding
has no reply yet). Paginate the full connection, exactly as the gate does — a
long PR can carry more than 100 threads, and a work list built from page one
alone cannot satisfy the answer-every-thread rule:

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
what invariant already covers it, what the accepted trade-off is — and says
so on the thread where the reconcile will adjudicate it. "Won't fix" with no
reasoning is adjudicated too, as `rebutted-invalid`, and keeps blocking.

After answering a thread, resolve it. The pipeline's tokens cannot resolve
threads, but your own `gh` login can, and replies still land on resolved
threads (resolution is a collapse flag, not a lock), so nothing you resolve
is closed to the reconcile's later replies. Adjudication never reads
resolution state — the ledger, not the flag, decides what still blocks — so
this is purely for the humans reading the PR:

```bash
gh api graphql -f query='mutation($t:ID!){resolveReviewThread(input:{threadId:$t}){thread{isResolved}}}' -f t="$THREAD_ID"
```

Never @mention the bot reviewers (`copilot`, `lfx-reviewer`) in any comment —
a mention can summon them outside the flow's control.

## The needs-human label

`needs-human` is the human override surface. It is sticky and add-only: the
escalation judge can set it, and only an allowlisted human removing it counts
(the gate enforces the allowlist; an unauthorized removal leaves the gate
withheld). It usually means the PR touches approval machinery,
security-sensitive surface, or something the judge could not bound.

The label blocks the **gate approval only — not review rounds**. The
conductor keeps adjudicating every push and stamping `agentic-review/clean`
regardless of the label, so the label does not change your goal: keep
driving rounds — fixing, rebutting, answering — until the check is green on
the current head. Never remove or toggle the label, and do not treat it as a
bug. Report it to the main session the moment it appears so the human review
it requests can start in parallel with your remaining rounds; once the check
is green and every thread is answered, stop and report that ending: a human
must review before this PR can merge. Beyond that, this skill takes no
position on gate mechanics: while the label is set, the gate will not
approve.

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
manual, so do not restate its rules in the prompt. The prompt needs only:

- The instruction to first read this SKILL.md **by absolute path in the main
  checkout** (its worktree snapshot may be stale) and drive the PR by it to
  its terminal state: a green `agentic-review/clean` check on the current
  head with every thread answered, then report which ending applies.
- The PR number and current head SHA.
- The newest `pr=#<PR>:`-bound `agentic-review/clean` status id as the
  pending anchor if one exists, plus any hazard the main session knows about
  (e.g. the head SHA was previously pushed on another PR).

Example prompt: "You are the PR driver for PR #57 on this repo. Read
`<main-checkout>/.claude/skills/newsletter-service-agentic-pr/SKILL.md` and
drive the PR by it to a green check on the current head with every thread
answered, then report which ending applies. Head: `<sha>`. Pending anchor:
status id `<id>`. Do not merge."

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

You are the driver from here on. You are a goal-based agent: your goal is a
green `agentic-review/clean` check on the current head with every thread
answered. Implement whatever fixes the rounds demand to reach it, within
the authority bounds below; `needs-human` never pauses your rounds — it
only decides which ending your final report announces: "needs human review
before merge" when present, "clear for the gate/automerge path" when absent
(noting whether the gate's approval has already landed).

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
license header. Before any push: `export PATH="$(go env GOPATH)/bin:$PATH"`
(the Makefile installs `golangci-lint` into GOPATH/bin, which is not always
`$HOME/go/bin`), then `make check` (fmt + lint + license-check + go vet) and
`make test` — all must pass.

**Liveness — one persistent monitor, armed first.** A round takes 10–20
minutes and you must survive every wait unattended. Never rely on one-shot
background polls — they die silently, and silence is never success. As your
**first act**, arm ONE persistent Monitor (`persistent: true`, 2-minute
interval) as your standing wake source and leave it running for the whole
drive. It fingerprints everything that can change the drive's state — the
head, the newest PR-bound stamp, the `needs-human` label, the gate's
approval, and the unresolved-thread count — and emits an event on any
change, so it stays a wake source after the clean stamp goes quiet; each
event re-invokes you:

```bash
REPO="$(gh repo view --json nameWithOwner --jq .nameWithOwner)"; PR=<pr>   # adjust PR
prev=""; fails=0
while true; do
  fp=$(
    head=$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid) &&
    stamp=$(gh api "repos/$REPO/commits/$head/statuses" --paginate --jq ".[] | select(.context==\"agentic-review/clean\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" | sort -n | tail -1) &&
    gate=$(gh pr view "$PR" --repo "$REPO" --json labels,latestReviews --jq "{label: ([.labels[].name] | contains([\"needs-human\"])), approved: ([.latestReviews[] | select(.author.login==\"lfx-reviewer\" and .state==\"APPROVED\")] | length > 0)}") &&
    unresolved=$(gh api graphql -f query='query($o:String!,$n:String!,$p:Int!){repository(owner:$o,name:$n){pullRequest(number:$p){reviewThreads(first:100){nodes{isResolved}}}}}' -f o="${REPO%/*}" -f n="${REPO#*/}" -F p="$PR" --jq '[.data.repository.pullRequest.reviewThreads.nodes[] | select(.isResolved|not)] | length') &&
    echo "head=$head stamp=$stamp gate=$gate unresolved=$unresolved"
  ) 2>/dev/null
  if [ -z "$fp" ]; then
    fails=$((fails+1)); [ "$fails" -ge 5 ] && { echo "poll-error: state queries failing repeatedly"; fails=0; }
  else
    fails=0; [ "$fp" != "$prev" ] && { echo "$fp"; prev="$fp"; }
  fi
  sleep 120
done
```

It follows the PR's current head across your own pushes. Record `pending`
stamps as the round's anchor and work terminal ones as verdicts; treat
post-clean fingerprint changes — the gate's approval landing, the
`needs-human` label appearing, an unresolved-thread count change — as the
cue to re-check that surface directly (the fingerprint is a wake trigger,
not the authoritative read: threads still come from the paginated listing,
and the label/approval from the PR itself). Repeated query failures emit
`poll-error` events instead of leaving you in permanent silence. On each
wake-up: handle the new state and send a one-line round-transition note to
the main session (SendMessage to `main`, e.g. "round 2: 3 blocking;
fixing") so liveness stays visible. End a turn only with a final report, a
must-stop report, or the armed monitor standing watch — never with neither.
Call TaskStop on the monitor before your final report.

**Authority bounds.**

- **May**: read PR state, commit and push fixes for findings whose resolution
  is clear and mechanical, post thread replies and substantive rebuttals,
  resolve the threads you have answered, poll for verdicts.
- **Must stop and report instead of acting** when: a finding requires a
  design decision or its fix is not obviously safe; the same finding family
  survives two of your fix attempts; roughly five rounds pass without
  convergence; or a round appears dead per the bounded-wait diagnostics
  (report the workflow-run evidence, do not push again). The `needs-human`
  label is NOT a stop condition — report it and keep driving to the green
  check per "The needs-human label".
- **Must never**: merge — you have no merge authority under any
  circumstances, even fully green and gate-approved — force-push, approve,
  edit other accounts' comments, add or remove labels, or @mention the bots.

**Final report.** State the terminal state — a green check on the current
head with every thread answered — and which ending applies: "needs human
review before merge" when the label is present, or "clear for the
gate/automerge path" only once the gate's approval or the current head's
`needs-human: no` verdict has actually appeared (with neither yet, report
escalation pending rather than clear). If you stopped on a must-stop
condition instead, state what you are blocked on. Either way include a
one-paragraph round-by-round history: what each round's blocking findings
were and whether each was fixed or rebutted.

## Hard rules

- Rounds fire on developer events only — push, open, reopen. Comments and
  replies never start one; do not wait for a reaction to a comment.
- At most one push per round; batch fixes and rebuttals (signed empty commit
  for rebuttal-only rounds, no push for replies-only on a clean head).
- Fix or rebut every blocking finding; answer every thread; say how you fixed
  it or why it stands, then resolve what you answered.
- Never mention the bots; never touch the `needs-human` label.
- Never edit or imitate `lfx-reviewer` comments — the pipeline trusts that
  account's authorship, and the apply step validates everything anyway.
