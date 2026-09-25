---
name: newsletter-service-preflight
description: >
  Repo-local Go mechanical pre-PR pipeline for lfx-v2-newsletter-service.
  Checks working tree status, license headers, Go formatting, lint/vet, build,
  tests, protected newsletter-service files, commit verification, and PR change
  summary. Run after /newsletter-service-pr-readiness. Supports report-only or
  dry-run mode when the contributor does not want file mutations.
allowed-tools: Bash, Read, Glob, Grep, Edit, Write, AskUserQuestion
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service Preflight

You are running the mechanical pre-PR pipeline for `lfx-v2-newsletter-service`. This skill is Go-specific and repo-specific. It validates the current branch before PR creation and helps fix mechanical issues such as formatting or license headers.

Run this after `/newsletter-service-pr-readiness` has passed or after its shape issues have been addressed. This skill does not perform architecture review or code-quality judgment beyond shell-driven mechanical checks.

## Modes

Args format: `[base-branch] [--dry-run|--report-only] [extra instructions]`.

- Default base: `origin/main`.
- If the base contains no `/`, normalize it to `origin/<base>`.
- Default mode may apply mechanical fixes, especially formatting.
- `--dry-run` or `--report-only` means report without mutating files. Do not run `make fmt`, do not add headers, do not create commits, and do not create a PR.

## Check 0: Working Tree Status

Run before any validation:

```bash
git fetch origin
git status --short
git diff --stat <base>...HEAD
git diff --name-only <base>...HEAD
git log --format="%h %s%n%b" <base>..HEAD
```

Evaluate:

- Uncommitted changes: ask whether to continue with the dirty tree, commit now, or stash.
- No commits ahead of `<base>`: stop and ask whether the contributor is on the right branch.
- Commit subjects missing conventional format: flag for PR-readiness cleanup.
- Commit messages missing `LFXV2-`: flag for PR-readiness cleanup.
- Commit bodies missing `Signed-off-by:`: flag for DCO cleanup.

## Check 1: License Headers

Go files must start with:

```go
// Copyright The Linux Foundation and each contributor to LFX.
// SPDX-License-Identifier: MIT
```

Run:

```bash
make license-check
```

If new or changed Markdown docs are in the diff, check the repo convention as well:

```html
<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->
```

For `.claude/skills/**/SKILL.md`, keep YAML frontmatter first. If adding the Markdown license comments, place them immediately after the closing `---`.

In dry-run mode, report missing headers only. In default mode, add obvious missing headers, then rerun the relevant check.

## Check 2: Go Formatting

Default mode:

```bash
make fmt
```

Dry-run/report-only mode:

```bash
gofmt -l $(find . -name '*.go' -not -path './vendor/*')
```

If formatting changes files in default mode, include the changed file list in the final report.

## Check 3: Lint And Vet

Run:

```bash
make lint
go vet ./...
```

`make lint` uses the repo-pinned `GOLANGCI_LINT_VERSION` from `Makefile` and may install `golangci-lint` if it is missing. If installation fails because the environment is offline, report that as an environment limitation instead of replacing the lint step with unrelated checks.

Fix straightforward mechanical lint issues in default mode when the fix is local and obvious. For behavior-changing lint fixes, stop and report the issue.

## Check 4: Build Verification

Run:

```bash
make build
```

This service builds `./cmd/newsletter-api` into `bin/lfx-v2-newsletter-service/newsletter-api`. Do not include build artifacts in the PR.

If the build fails, report the failing package, file, and compiler error. Fix only obvious mechanical problems.

## Check 5: Tests

Run:

```bash
make test
```

The target runs `go test -v -race -coverprofile=coverage.out ./...`. Report failures with the package and failing test. Do not hide race detector failures.

## Check 6: Protected Files Check

Use the same repo-specific protected list as `/newsletter-service-pr-readiness`. Protected files are not automatically blocked, but they must be called out in the final report and usually in the PR description.

Run:

```bash
git diff --name-only <base>...HEAD
```

Flag changes in:

- Newsletter DTO/API contracts: `pkg/api/newsletter.go`, `internal/handler/http.go`, `internal/handler/*.go`, `docs/newsletter-service-contract.md`
- Embedded schema: `internal/schema/schema.go`, `internal/schema/schema.sql`
- Postgres/Bun repository: `internal/repository/postgres.go`
- Recipient resolution and send fan-out: `internal/service/send_orchestrator.go`, `internal/infrastructure/nats/*.go`, `docs/recipient-resolution.md`
- Charts: `charts/lfx-v2-newsletter-service/**`
- Go module metadata: `go.mod`, `go.sum`
- Build system: `Makefile`
- Claude guidance: `CLAUDE.md`, `.claude/skills/**`
- Contract docs: `docs/newsletter-service-contract.md`, `docs/recipient-resolution.md`, `docs/service-helm-chart.md`

## Check 7: Commit Verification

Run after any default-mode fixes:

```bash
git status --short
git log --format="%h %s%n%b" <base>..HEAD
git log --format="%G? %h %s" <base>..HEAD
```

Verify:

- All intended changes are committed, or uncommitted mechanical fixes are clearly reported.
- Commit subjects follow conventional commit format.
- Every commit contains `Signed-off-by:`.
- Every commit is GPG-signed with status `G` or `U`.
- At least one commit subject or body references `LFXV2-<number>`.

If default-mode fixes created uncommitted changes, ask before committing anything.

## Check 8: Change Summary

Generate the PR summary inputs:

```bash
git diff --stat <base>...HEAD
git diff --name-status <base>...HEAD
```

Group the summary by newsletter-service ownership area:

1. Public API or DTO contract changes.
2. Handler or HTTP behavior changes.
3. Domain/service state-transition changes.
4. Postgres/Bun repository or embedded schema changes.
5. Recipient resolution, NATS upstream, or email fan-out behavior changes.
6. Helm chart or deployment changes.
7. Go module, build, Claude guidance, or contract-doc changes.

## Results Report

Render:

```text
PREFLIGHT RESULTS
----------------------------------------
[PASS] Working tree      - Clean, N commits ahead of origin/main
[PASS] License headers   - Go and changed docs have expected headers
[PASS] Formatting        - Applied, no remaining gofmt changes
[PASS] Lint / vet        - make lint and go vet passed
[PASS] Build             - make build passed
[PASS] Tests             - make test passed
[PASS] Protected files   - 2 touched: pkg/api/newsletter.go, docs/newsletter-service-contract.md
[PASS] Commits           - Conventional, signed off, GPG-signed, JIRA referenced
----------------------------------------
READY FOR PR

Change summary:
- ...

Protected-file callouts:
- ...
```

If issues remain:

```text
PREFLIGHT RESULTS
----------------------------------------
[PASS] Working tree      - Dirty tree acknowledged by contributor
[FAIL] License headers   - 1 Go file missing header
[PASS] Formatting        - Dry-run only, 0 files need gofmt
[FAIL] Lint / vet        - make lint failed
[PASS] Build             - make build passed
[FAIL] Tests             - make test failed in internal/service
[WARN] Protected files   - go.mod and Makefile touched
[PASS] Commits           - Shape checks already addressed
----------------------------------------
NOT READY - fix remaining mechanical failures before PR
```

## Boundaries

- Do not run Angular, Yarn, Goa, or generated-code checks; this service is a Go HTTP/Postgres service with no Goa design layer.
- Do not make architectural decisions from preflight output.
- Do not create commits, pushes, or PRs unless the contributor explicitly asks after the report.
- Keep protected-file knowledge repo-local. Do not move newsletter-service rules into central skills.
