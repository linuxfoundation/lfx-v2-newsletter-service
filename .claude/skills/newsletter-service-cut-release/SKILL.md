---
name: newsletter-service-cut-release
description: >
  Cut a new GitHub release/tag for lfx-v2-newsletter-service from main, verify
  the tagged build published the image and Helm chart, then open a version-bump
  PR on lfx-v2-argocd pinning the target environments to the new version. Use
  when the user asks to "cut a release", "release a new version", or "bump the
  newsletter service in argocd".
allowed-tools: Bash, Read, Edit, AskUserQuestion
---

<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Newsletter Service — Cut Release + ArgoCD Bump

Repeatable procedure for releasing `lfx-v2-newsletter-service` and rolling the
new version out via `lfx-v2-argocd`. Two repos, two parts. Requires local
checkouts of both `lfx-v2-newsletter-service` (this repo) and `lfx-v2-argocd`
as siblings (same parent directory as this repo, e.g. `../lfx-v2-argocd`) —
if the argocd checkout isn't found at that path, ask the user where it lives.

## How release publishing works here (don't re-derive each time)

- `.github/workflows/ko-build-tag.yaml` ("Publish Tagged Release") triggers on
  push of any `v*` tag on this repo. It builds+pushes the container image and
  publishes the Helm chart as an OCI artifact to
  `ghcr.io/linuxfoundation/lfx-v2-newsletter-service/chart/lfx-v2-newsletter-service`,
  both tagged at the release version.
- `charts/lfx-v2-newsletter-service/Chart.yaml` `version: 0.0.1` is a
  placeholder replaced at build time — never edit it manually.
- The workflow has a known-flaky `create-ghcr-helm-provenance / final` sub-job
  that fails even on successful releases (confirmed on v0.1.19, v0.1.20,
  v0.1.21) — don't treat that alone as a blocker. What matters is that the
  `Publish Tagged Release` job and `release-helm-chart` job (image build, Helm
  publish, chart signing) are green.
- ArgoCD pins this service in `lfx-v2-argocd` by two file pairs per
  environment: `apps/<env>/lfx-v2-applications.yaml` (`targetRevision:`, the
  chart version) and `values/<env>/lfx-v2-newsletter-service.yaml`
  (`image.tag:`). `dev` tracks `HEAD`/`development` and is never bumped.

## Steps

### 1. Determine the version

```bash
git fetch origin --tags --quiet
git tag --sort=-v:refname | head -1          # current latest tag
git log <latest-tag>..origin/main --oneline  # what's shipping in this release
```

Default to a **patch bump** (this repo's convention so far — v0.1.20 shipped a
`feat` under a patch bump too) unless the unreleased commits are clearly
significant enough to warrant a minor/major bump, or the user specifies a
version. If genuinely ambiguous, use `AskUserQuestion` — don't guess silently
on a version number.

### 2. Cut the release

Target `origin/main`'s tip explicitly — don't rely on local `HEAD`, which may
be a detached/stale checkout:

```bash
gh release create vX.Y.Z --target main --title vX.Y.Z --generate-notes
```

### 3. Verify the build

```bash
gh run list --workflow ko-build-tag.yaml --limit 3
gh run watch <run-id>
```

Confirm `Publish Tagged Release` and `release-helm-chart` jobs are green (see
the known-flaky provenance caveat above — that job failing alone is fine).
If either of those two jobs actually fails, stop and diagnose before touching
argocd — the chart/image won't exist for ArgoCD to pull.

### 4. Determine environment scope

Ask the user (via `AskUserQuestion`) which environments to bump if not
stated — options are typically staging-only (soak first) or staging+prod
together. Never bump `dev`.

### 5. Bump argocd

In the `lfx-v2-argocd` checkout:

```bash
git checkout main && git pull --quiet
git checkout -b chore/bump-newsletter-service-vX.Y.Z
```

For each selected environment, replace the old version with the new one in
both files:
- `apps/<env>/lfx-v2-applications.yaml` — the `lfx-v2-newsletter-service`
  block's `targetRevision:`
- `values/<env>/lfx-v2-newsletter-service.yaml` — `image.tag:`

Do not touch `values/global/lfx-v2-newsletter-service.yaml` or any `dev` file.

Verify the diff touches only the intended lines (`git diff`) before
committing.

```bash
git add apps/<env>/lfx-v2-applications.yaml values/<env>/lfx-v2-newsletter-service.yaml  # per env
git commit -s -S -m "feat(newsletter-service): bump chart pin to vX.Y.Z"
git push -u origin chore/bump-newsletter-service-vX.Y.Z
gh pr create --repo linuxfoundation/lfx-v2-argocd --base main \
  --title "feat(newsletter-service): bump chart pin to vX.Y.Z" \
  --body "Bump <envs> to newsletter-service vX.Y.Z (<one-line summary of what's shipping>). dev unchanged (tracks HEAD)."
```

## Boundaries

- This skill opens the argocd PR; it does **not** merge it. Merging is a
  human/reviewed step, and should only happen after the release build (image +
  chart) is confirmed green.
- This skill does not touch this repo's code, `Chart.yaml`, or run the
  newsletter-service preflight/PR-readiness pipeline — those are for feature
  PRs, not releases.
