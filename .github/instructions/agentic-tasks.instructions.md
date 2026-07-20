---
applyTo: "**"
excludeAgent: "code-review"
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Agent tasks: how to publish, and what you may not do

This file governs the **escalation and conductor agent tasks** only. It is
excluded from Copilot code review (`excludeAgent: "code-review"`), which
publishes inline threads through its own native review pipeline and does not
need these rules.

In the escalation and conductor tasks, publish your output yourself with the
**`add_issue_comment`** tool, which posts a comment on the pull request. The
conductor also has **`add_reply_to_pull_request_comment`** to reply on a review
thread (to explain why a thread is now resolved, or why it still blocks). Those
are the only write tools configured for you; everything else in the GitHub MCP
is read-only, on purpose. Do **not** use the `gh` CLI or `curl`: the tokens in
the session environment (`GITHUB_COPILOT_API_TOKEN`, `COPILOT_SDK_AUTH_TOKEN`)
are model/SDK credentials and cannot write the GitHub REST API. Do not modify
code, push commits, or open a pull request. Labels, statuses, thread
resolutions, and approvals are set by deterministic workflow steps that read
your comment, not by you.
