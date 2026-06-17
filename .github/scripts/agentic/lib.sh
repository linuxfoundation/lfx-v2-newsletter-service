# Copyright The Linux Foundation and each contributor to LFX.
# SPDX-License-Identifier: MIT
#
# Shared helpers for the agentic review scripts. Source this file; it defines
# ag_* functions and exposes no side effects on its own. These run in the
# deterministic (no-model) workflow steps, so they hold the write token: keep
# them simple and auditable.

# shellcheck shell=bash
# GraphQL queries below quote $-prefixed variables that are GraphQL variables,
# not shell variables, so they are deliberately single-quoted (SC2016).
# shellcheck disable=SC2016

# Print to stderr.
ag_log() { printf '%s\n' "$*" >&2; }
ag_die() { ag_log "ERROR: $*"; exit 1; }

# True when we must not make mutating GitHub calls: explicit dry-run, or no
# token available. Read-only GraphQL/REST reads still run when a token exists.
# Fails closed: any AGENTIC_DRY_RUN value other than the clearly-off set is
# treated as dry-run, so a typo like AGENTIC_DRY_RUN=true still suppresses
# mutations rather than silently arming them.
ag_is_dry_run() {
  case "${AGENTIC_DRY_RUN:-0}" in
    ''|0|false|FALSE|no|off) : ;;   # not forced on; fall through to token check
    *) return 0 ;;                  # any other value => dry-run
  esac
  [ -z "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ]
}

# Strip Markdown code fences and any prose around a JSON object, mirroring the
# lenient read_agent_json in agents/simulate. Emits the cleaned JSON on stdout,
# or exits non-zero if nothing parseable is found.
ag_read_agent_json() {
  local path="$1" text
  [ -f "$path" ] || ag_die "agent output not found: $path"
  text="$(cat "$path")"
  # Drop a pure-fence first or last line (```json / ```), removing the whole
  # line so no leading blank line remains for downstream byte-exact consumers.
  text="$(printf '%s' "$text" | sed -E '1{/^[[:space:]]*```([a-zA-Z]+)?[[:space:]]*$/d;}; ${/^[[:space:]]*```[[:space:]]*$/d;}')"
  if printf '%s' "$text" | jq -e . >/dev/null 2>&1; then
    printf '%s' "$text"
    return 0
  fi
  # Fallback: the outermost {...} span (first '{' to last '}'). Handles a single
  # JSON object wrapped in prose; multi-object output will fail to parse.
  local block
  block="$(printf '%s' "$text" | tr '\n' '\f' | grep -oE '\{.*\}' | head -1 | tr '\f' '\n')"
  if [ -n "$block" ] && printf '%s' "$block" | jq -e . >/dev/null 2>&1; then
    printf '%s' "$block"
    return 0
  fi
  ag_die "could not parse JSON from $path"
}

# Collapse whitespace runs to single spaces and trim ends. Reads stdin.
ag_normalize() {
  tr '\t' ' ' | tr -s ' ' | sed -E 's/^ +//; s/ +$//'
}

# The code at file:line from the checked-out head, normalized. Empty for
# file-level findings (line 0) or when the file/line is out of range.
ag_snippet_at() {
  local file="$1" line="$2"
  case "$line" in ''|*[!0-9]*) line=0 ;; esac
  if [ "$line" -gt 0 ] && [ -f "$file" ]; then
    sed -n "${line}p" "$file" 2>/dev/null | ag_normalize
  fi
}

# Stable fingerprint for a finding: hash of file + normalized snippet, NOT the
# comment text, so the model rewording a finding between runs still matches the
# same thread. When there is no usable snippet (file-level line 0, or a stale /
# out-of-range line number), fall back to file + line so distinct unanchored
# findings on one file stay distinct instead of all collapsing into one bucket.
ag_fingerprint() {
  local file="$1" line="$2" snip
  case "$line" in ''|*[!0-9]*) line=0 ;; esac
  snip="$(ag_snippet_at "$file" "$line")"
  if [ -n "$snip" ]; then
    printf '%s\n%s' "$file" "$snip" | shasum -a 256 | cut -c1-16
  else
    printf '%s\n%s' "$file" "$line" | shasum -a 256 | cut -c1-16
  fi
}

# Hidden marker embedded in each posted comment so later runs can recover the
# fingerprint and reconcile threads.
ag_marker() { printf '<!-- agentic:id=%s -->' "$1"; }

# Extract the fingerprint from a comment body, or empty if unmarked. Takes the
# LAST marker: render_body appends the real marker last, and finding text is
# stripped of any agentic markers before posting, so a spoofed marker in the
# model's comment cannot win even if stripping is ever bypassed.
ag_marker_of() {
  grep -oE 'agentic:id=[0-9a-f]{16}' | tail -1 | cut -d= -f2
}

# Remove any agentic markers a model may have embedded in finding text, so it
# cannot forge the fingerprint or summary marker. Reads stdin, writes stdout.
ag_strip_markers() {
  sed -E 's/<!--[[:space:]]*agentic:[^>]*-->//g; s/agentic:(id=[0-9a-f]*|summary)//g'
}

# Used by post-comments.sh after sourcing this file.
# shellcheck disable=SC2034
ag_summary_marker='<!-- agentic:summary -->'

# Fetch the PR's review threads as JSONL of {id, isResolved, body} (body is the
# first comment, which carries our marker). Read-only, so it runs even in
# dry-run when a token is present. With no token it yields nothing and succeeds
# (the dry-run case). A real API failure returns non-zero so the caller fails
# closed rather than mistaking an error for "this PR has no threads."
ag_fetch_threads() {
  local repo="$1" pr="$2" owner name raw total
  owner="${repo%%/*}"; name="${repo#*/}"
  [ -z "${GH_TOKEN:-${GITHUB_TOKEN:-}}" ] && return 0
  if ! raw="$(gh api graphql \
      -f query='query($owner:String!,$name:String!,$pr:Int!){
        repository(owner:$owner,name:$name){
          pullRequest(number:$pr){
            reviewThreads(first:100){
              totalCount
              nodes{ id isResolved comments(first:1){ nodes{ body } } }
            }
          }
        }
      }' \
      -F owner="$owner" -F name="$name" -F pr="$pr" 2>/dev/null)"; then
    ag_log "ERROR: failed to fetch review threads for ${repo}#${pr}"
    return 1
  fi
  total="$(printf '%s' "$raw" | jq -r '.data.repository.pullRequest.reviewThreads.totalCount // 0')"
  if [ "${total:-0}" -gt 100 ]; then
    ag_log "WARNING: PR has ${total} review threads; only the first 100 are reconciled"
  fi
  printf '%s' "$raw" | jq -c '.data.repository.pullRequest.reviewThreads.nodes[]
    | {id, isResolved, body: (.comments.nodes[0].body // "")}'
}

# Reopen a resolved thread (a blocking issue the model still reports).
ag_unresolve_thread() {
  local id="$1"
  if ag_is_dry_run; then ag_log "[dry-run] unresolveReviewThread $id"; return 0; fi
  gh api graphql -f query='mutation($id:ID!){ unresolveReviewThread(input:{threadId:$id}){ thread{ id } } }' \
    -F id="$id" >/dev/null
}

# Resolve a thread the model no longer reports (the issue is fixed); the agent
# cleaning up after itself, since it re-derived that the finding is gone.
ag_resolve_thread() {
  local id="$1"
  if ag_is_dry_run; then ag_log "[dry-run] resolveReviewThread $id"; return 0; fi
  gh api graphql -f query='mutation($id:ID!){ resolveReviewThread(input:{threadId:$id}){ thread{ id } } }' \
    -F id="$id" >/dev/null
}

# Blocking severities gate the clean status; nit does not.
ag_is_blocking() {
  case "$1" in critical|high|should-fix) return 0 ;; *) return 1 ;; esac
}
