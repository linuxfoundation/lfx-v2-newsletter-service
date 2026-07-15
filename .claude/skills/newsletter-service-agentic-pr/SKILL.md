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
  fixes to an open PR. Also use it right after opening any PR here, and offer
  to babysit the loop in a background agent so the user can keep working.
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Working a PR through the agentic review flow

Every push to an open PR on this repo starts a review round with no human in
the loop: Copilot reviews the diff, an escalation judge decides whether a
human must look (`needs-human` label), and the conductor adjudicates every
review thread and posts an **Agentic review check** comment as `lfx-reviewer`,
stamping the `agentic-review/clean` commit status on the head it judged. A
deterministic gate approves the PR as `lfx-reviewer` once three things hold:
the current head's clean status is `success`, the `needs-human` label is
absent, and every review thread has at least one reply beyond the finding.

Your job on the developer side is therefore a loop, not a conversation:
**everything you do is adjudicated at the next push.** Replies, rebuttals, and
resolutions never trigger a round by themselves — only pushes (and PR
open/reopen) do. That is deliberate: it keeps the pipeline's cost proportional
to code changes and keeps humans from being able to talk the gate open.

## The round loop

1. **Read the latest check comment** (see commands below). Confirm its hidden
   `head:` line matches the PR's current head — a check for an older head is
   stale and the next push owns the verdict.
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
5. **Batch the whole round into one commit and one push.** Each push burns a
   Copilot review plus a reconcile model run, and a mid-round push strands the
   running round (its eventual comment is head-bound and inert, but it is
   noise). Write all fixes, post all replies, resolve the answered threads,
   then push once.
6. **Wait for the verdict** on the new head (command below; a round typically
   takes 10–20 minutes), then loop from step 1.
7. **Stop when green**: the check reads `✅ clean` and the gate posts the
   approving review. The approval can lag your last reply by up to ~10
   minutes — reply events do not trigger the gate, a scheduled sweep does.

## Reading the check comment

The newest `lfx-reviewer` comment containing the machine marker is the
authoritative state. The ledger (per-thread adjudications, `head:`, `clean:`)
is collapsed inside a `<details>` element — read the raw body, not the
rendered page.

```bash
CID="$(gh api "repos/$REPO/issues/$PR/comments" --paginate \
  --jq '.[] | select(.user.login=="lfx-reviewer" and (.body | contains("<!-- agentic:check v1 -->"))) | .id' | tail -1)"
gh api "repos/$REPO/issues/comments/$CID" --jq .body
```

In the body: the headline gives the blocking count, the **Blocking** table
names what stands between you and clean, and the ledger's `- id:` rows record
every adjudicated thread with its status (`fixed`, `obsolete`, `outstanding`,
`rebutted-valid`, `rebutted-invalid`) and the reason. `outstanding` and
`rebutted-invalid` rows are your work list.

## Waiting for the verdict

Poll the head's commit status; the conductor stamps `pending` at round start
and `success`/`failure` when the check posts. Statuses are per-PR-bound via
the description, and the newest status id wins:

```bash
HEAD="$(gh pr view "$PR" --repo "$REPO" --json headRefOid --jq .headRefOid)"
gh api "repos/$REPO/commits/$HEAD/statuses" --paginate \
  --jq ".[] | select(.context==\"agentic-review/clean\" and .state!=\"pending\" and ((.description // \"\") | contains(\"pr=#$PR:\"))) | [(.id|tostring), .state] | @tsv" \
  | sort -n | tail -1 | cut -f2
```

Empty output means the round is still running — poll every minute or so
rather than pushing again.

## Threads: fixing, rebutting, answering, resolving

List threads and find the unanswered ones (`totalCount < 2` means the finding
has no reply yet):

```bash
gh api graphql -f query='query($owner:String!,$name:String!,$pr:Int!){
  repository(owner:$owner,name:$name){pullRequest(number:$pr){
    reviewThreads(first:100){nodes{id isResolved path
      comments(first:1){totalCount nodes{databaseId author{login} body}}}}}}}' \
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
withheld). If it is set on your PR, the gate will not approve no matter how
green the check is. **Stop and tell the user** — do not remove the label, do
not toggle it, and do not treat it as a bug. It usually means the PR touches
approval machinery, security-sensitive surface, or something the judge could
not bound; a human decides, and after their unlabel the next push resumes
per-head judging.

## Babysitting the loop in the background

The loop is mostly waiting. When a PR enters the flow, offer to babysit it in
a background agent so the user's session stays free for real work — for
example: "Want me to babysit this PR in the background? I'll fix or rebut
each round and report back when it's green, or when something needs you."

If accepted, spawn a general-purpose agent in the background with worktree
isolation, have it check out the PR branch, and hand it this loop with clear
authority bounds:

- **May**: read PR state, commit and push fixes for findings whose resolution
  is clear and mechanical (following this repo's conventions and commit
  signing: `git commit -s -S`), post thread replies and substantive
  rebuttals, resolve the threads it has answered, poll for verdicts.
- **Must stop and report instead of acting** when: the `needs-human` label
  appears; a finding requires a design decision or its fix is not obviously
  safe; the same finding family survives two of its fix attempts; or roughly
  five rounds pass without convergence.
- **Must never**: force-push, merge, approve, edit other accounts' comments,
  or add/remove labels.

Have it report the final state either way: green and gate-approved, or what
it is blocked on and the round-by-round history in one paragraph.

## Hard rules

- Rounds fire on pushes only. Do not wait for a reaction to a comment.
- One push per round; batch fixes and rebuttals.
- Fix or rebut every blocking finding; answer every thread; say how you fixed
  it or why it stands, then resolve what you answered.
- Never mention the bots; never touch the `needs-human` label.
- Never edit or imitate `lfx-reviewer` comments — the pipeline trusts that
  account's authorship, and the apply step validates everything anyway.
