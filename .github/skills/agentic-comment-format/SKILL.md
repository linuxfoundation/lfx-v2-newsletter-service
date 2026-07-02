---
name: agentic-comment-format
description: >-
  The exact format for the two verdict comments the agentic review posts on a
  lfx-v2-newsletter-service pull request: the needs-human verdict (escalation) and the
  agentic-check verdict (conductor). Use whenever you post either verdict. Defines the
  human presentation and the machine-readable markers that the deterministic apply
  step parses, so writer and reader stay in sync.
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Agentic verdict comment format

Both agentic roles publish exactly one verdict comment on the pull request, via the
`add_issue_comment` tool. Each comment is two things at once: a clear, useful message
for the engineer, and a machine-readable marker that a deterministic workflow step
(`agentic-apply.yml`) parses to set labels, the commit status, and thread state.

The markers are **load-bearing** — keep them exactly as written here or the
deterministic step stops working. The prose around them is yours to make genuinely
good to read.

Shared rules:

- **One comment per verdict.** Never split it across comments.
- **Markers are HTML comments** (`<!-- ... -->`) so they are invisible in the rendered
  view and never clutter what the engineer sees. The deterministic step greps for them.
- **Write for a busy engineer:** lead with the outcome, be specific, point at the code,
  and do not pad.

## Needs-human verdict (escalation judge)

Posted once, when the PR opens. When a human must sign off before merge:

```
<!-- agentic:needs-human v1 -->
<!-- needs-human: yes -->
### Needs a human before merge

**Why:** <one specific sentence: what a lead needs to know about and why>
```

When no human sign-off is required:

```
<!-- agentic:needs-human v1 -->
<!-- needs-human: no -->
### No human sign-off required

<one specific sentence: what you checked and why this change is routine>
```

The `<!-- needs-human: yes -->` / `<!-- needs-human: no -->` line is the machine signal.
The deterministic step sets the sticky `needs-human` label when it is `yes`, and does
nothing when it is `no`. Do not set the label yourself, and write the marker exactly —
it is the only place the words `needs-human: yes|no` may appear in your comment.

## Agentic-check verdict (conductor)

Posted after each review round. A human summary first, then one fenced machine block:

```
### Agentic review check — <✅ clean | ❌ N blocking>

<one or two lines: the state of the change and what remains to reach clean>

**Blocking**

| Severity | Finding | Next step |
| --- | --- | --- |
| high | <short finding> | <what a real fix needs> |

**Handled well:** <one line on what the change got right, when there is something>

<!-- agentic:check v1 -->
head: <full 40-char commit SHA of the head you judged>
clean: true|false
threads:
- id: <thread_node_id>, status: fixed|obsolete|outstanding|rebutted-valid|rebutted-invalid, severity: critical|high|should-fix|nit, reason: <one short sentence>
```

Rules the deterministic step depends on, so be exact:

- The block begins with the literal `<!-- agentic:check v1 -->` line.
- `head:` is the full commit SHA of the PR head you actually judged. The deterministic
  step sets the clean status on **that** commit, so a commit that lands after you post
  cannot inherit this verdict (it re-derives as not-yet-clean and the gate stays shut).
- `clean:` is `true` only when no thread blocks (none is `outstanding` or
  `rebutted-invalid`); otherwise `false`.
- One `- id:` line per thread you adjudicated, its four fields comma-separated —
  `id`, `status`, `severity`, then a one-sentence `reason` — in that order, all on
  the one line. The commas keep the fields scannable for both the engineer and the
  parser.
- The **Blocking** table lists only the blocking rows and mirrors the block. When
  `clean: true` there are no blocking rows: drop the table and say plainly that it is
  clean.

## What the deterministic step reads

`agentic-apply.yml` acts only on a comment authored by the `lfx-reviewer` machine
account (so a developer cannot forge a verdict). From that comment it:

- sets the sticky `needs-human` label when it sees `<!-- needs-human: yes -->`;
- from the `<!-- agentic:check v1 -->` block: resolves each thread marked `fixed`,
  `obsolete`, or `rebutted-valid`; re-opens each still-blocking thread that was resolved
  prematurely; and sets the `agentic-review/clean` commit status from `clean:`.

Nothing else you write is parsed, so the surrounding prose is entirely for the engineer.
