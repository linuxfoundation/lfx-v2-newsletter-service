<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Chart

Patterns for the service-local Helm chart: secret hygiene in the deployment template, namespace coupling between Heimdall middleware and the HTTPRoute, authorization fallbacks on routes that return PII, wiring a new route through both the HTTPRoute and the Heimdall RuleSet, and fail-fast handling of mode/shape selectors. These are the patterns the maintainer reviewer and Copilot flagged on PRs #3, #4, #7, #52, #57, and #58.

**Read when:** any file under `charts/lfx-v2-newsletter-service/` **or `internal/handler/http.go`** changed — especially `templates/deployment.yaml`, `templates/heimdall-middleware.yaml`, `templates/httproute.yaml`, `templates/ruleset.yaml`, and `values.yaml`.

---

## `chart/db-password-composed-into-pod-spec` — Critical

**Pattern:** the deployment template composes `DATABASE_URL` from `$(PGUSER):$(PGPASSWORD)@…`. Kubernetes resolves the `$(VAR)` interpolation, so the resolved password lands in the Pod spec, visible to anyone with `kubectl describe pod` / `pods/get` RBAC.

**Detect:** in `charts/lfx-v2-newsletter-service/templates/deployment.yaml`, flag a `DATABASE_URL` env value that interpolates `$(PGPASSWORD)` (or any secret value) into a composed string. The CNPG / `external.shape=fields` path should forward the `PG*` env vars from secret refs only and let the app compose the DSN in-process (`net/url.UserPassword`).

**Empirical citation:** PR #3 `charts/lfx-v2-newsletter-service/templates/deployment.yaml:91` — dealako (blocking) — "`DATABASE_URL` is assembled in plain env via `$(PGUSER):$(PGPASSWORD)@…`. Kubernetes resolves the `$(VAR)` interpolation and the result — including the password — is stored in the Pod spec and visible to anyone with `kubectl describe pod`." Resolved in `959e23d`: "went with the in-app DSN construction path … The CNPG deployment template now forwards the four secret refs … and no longer composes a `DATABASE_URL` env-var string."

**Failure message:** DB password interpolated into a composed `DATABASE_URL` env in the Pod spec — visible via `kubectl describe pod`.

**Fix:** forward `PGHOST/PGPORT/PGUSER/PGPASSWORD/PGDATABASE` as individual `secretKeyRef` env vars and compose the DSN in-process; or use the CloudNativePG secret's `uri` key directly. Do not interpolate the password into a template string.

---

## `chart/middleware-namespace-must-match-httproute` — Critical

**Pattern:** a Heimdall/Traefik `Middleware` CRD referenced by the HTTPRoute via an `ExtensionRef` without a namespace is created with `{{ .Release.Namespace }}` instead of the HTTPRoute's namespace (`{{ .Values.lfx.namespace }}`). When routing resources live in a different namespace than the chart release, Traefik can't resolve the middleware and drops the route.

**Detect:** in `charts/lfx-v2-newsletter-service/templates/heimdall-middleware.yaml`, confirm the `Middleware` `metadata.namespace` matches the namespace the HTTPRoute's `ExtensionRef` resolves against (`{{ .Values.lfx.namespace }}`), not `{{ .Release.Namespace }}`. Cross-check `templates/httproute.yaml` for the unnamespaced `ExtensionRef`.

**Caution when checking this — read the right block.** `charts/lfx-v2-newsletter-service/templates/httproute.yaml` carries a `namespace: {{ .Release.Namespace }}` line inside its `backendRefs`, *not* inside the `ExtensionRef`. Skimming the file for "is the ExtensionRef namespaced" lands on that `backendRefs` line and reads as fixed when it is not. Verify against the `filters`/`extensionRef` block specifically.

**Current anchor** (the empirical quote below is from PR #7 and is preserved verbatim, including the chart-relative path it used; these are the repo-relative anchors for the same premise today, and the line numbers may legitimately differ from the ones in that quote). Audited at HEAD `f13d015`, re-checked unchanged at `origin/main@b39d98b`: `charts/lfx-v2-newsletter-service/templates/heimdall-middleware.yaml:14,29` renders the Middleware into `{{ .Release.Namespace }}`; `charts/lfx-v2-newsletter-service/templates/httproute.yaml:9` is `{{ .Values.lfx.namespace }}`; the ExtensionRef at `charts/lfx-v2-newsletter-service/templates/httproute.yaml:47-51` is still unnamespaced — the coupling is unfixed — and the `backendRefs` namespace at `charts/lfx-v2-newsletter-service/templates/httproute.yaml:57` is a different block.

**Empirical citation:** PR #7 `charts/lfx-v2-newsletter-service/templates/heimdall-middleware.yaml:15` — Copilot — "The Middleware is referenced from templates/httproute.yaml via an ExtensionRef without a namespace, so it must be created in the same namespace as the HTTPRoute ({{ .Values.lfx.namespace }}). Using {{ .Release.Namespace }} here will break resolution." (Open thread; verify the namespace coupling on any middleware/HTTPRoute change — `templates/heimdall-middleware.yaml` still renders the Middleware into `{{ .Release.Namespace }}` while `templates/httproute.yaml` lives in `{{ .Values.lfx.namespace }}`.)

**Failure message:** Heimdall `Middleware` namespace uses `.Release.Namespace` but the HTTPRoute `ExtensionRef` resolves against `.Values.lfx.namespace` — Traefik drops the route.

**Fix:** render the `Middleware` into `{{ .Values.lfx.namespace }}` (the HTTPRoute namespace), matching the unnamespaced `ExtensionRef`.

---

## `chart/pii-route-must-not-fall-back-to-allow-all` — Critical

**Pattern:** a new Heimdall RuleSet rule guarding a route that returns PII is written with the chart's usual `{{- if .Values.openfga.enabled }} … openfga_check … {{- else }} … allow_all` shape. Because `values.yaml` ships `openfga.enabled: false`, the `else` branch is what actually renders, so every caller Heimdall authenticates can read any project's data on that route.

**Detect:** in `charts/lfx-v2-newsletter-service/templates/ruleset.yaml`, for any added or changed rule whose route returns project-scoped personal data (unsubscribe opt-out lists, recipient addresses), confirm the rule uses `openfga_check` **unconditionally** — no `{{- if .Values.openfga.enabled }}` guard and no `allow_all` fallback. Check the current default in `values.yaml` (`openfga.enabled`) before assuming the guarded branch renders.

**Empirical citation:** PR #52 (MERGED) `charts/lfx-v2-newsletter-service/templates/ruleset.yaml` — Copilot `3617160945` — "`values.yaml` sets `openfga.enabled: false`, so this branch renders `allow_all` … callers accepted by Heimdall can read any project's opt-out list." Resolved in `865fb80f`. Took two rounds to close (first the relation was tightened viewer→auditor, then the fallback was removed). Verified at HEAD `f13d015`: `values.yaml:307` still defaults `openfga.enabled: false`, and the two opt-out rules (`ruleset.yaml:271-284`, `:294-307`) are the only two of 14 rules with no `allow_all` fallback, with an explicit comment at `:267-270` recording why.

**Failure message:** PII route's RuleSet rule falls back to `allow_all` when `openfga.enabled` is false (the shipped default) — cross-project data readable by any authenticated caller.

**Fix:** call `openfga_check` unconditionally on PII routes; do not wrap it in `{{- if .Values.openfga.enabled }}` and do not provide an `allow_all` else-branch. Follow the two existing opt-out rules as the reference shape.

---

## `chart/mode-shape-selector-no-fail-fast` — Important

**Pattern:** a chart `mode`/`shape` selector (e.g. `database.external.shape`) silently falls back to a default path for any unrecognized value, so a typo (`"field"` instead of `"fields"`) renders the wrong env vars with no Helm error and fails only at runtime. Related: a required secret name defaults to empty string and renders an invalid `secretKeyRef`.

**Detect:** in `charts/lfx-v2-newsletter-service/templates/deployment.yaml`, confirm selector branches explicitly handle each valid value and `fail` on anything else; confirm required values (`database.external.secretName` in external mode) use Helm's `required` so rendering fails with a clear message instead of producing an empty/invalid manifest.

**Empirical citation:** PR #4 `charts/lfx-v2-newsletter-service/templates/deployment.yaml:127` — Copilot — "`database.external.shape` silently falls back to the `DATABASE_URL` path for any value other than 'fields'. A typo like 'field' would render the wrong env vars with no Helm error … call `fail` on any other value." Also PR #3 `…/deployment.yaml:99` — Copilot — "the default value is an empty string. That produces an invalid manifest (empty Secret name). Consider using Helm's `required` function."

**Failure message:** chart mode/shape selector silently falls back on a bad value (or a required secret name defaults empty) — misconfiguration surfaces only at runtime.

**Fix:** explicitly branch on each valid selector value and `fail "<message>"` on others; wrap required values (e.g. `secretName`) in `required` so `helm template`/upgrade fails fast.

---

## `chart/new-route-must-be-wired-in-httproute-and-ruleset` — Important

**Pattern:** a new HTTP route is added to `internal/handler/http.go` but not to the chart, so it works locally and is unreachable in every deployed environment — the HTTPRoute only forwards the path prefixes it lists, and the Heimdall RuleSet only authorizes the rules it declares. The mirror-image failure is gating a new route or env var on a *correlated* toggle rather than on the config it actually depends on.

**Detect:** when `internal/handler/http.go` registers a new path, confirm both `charts/lfx-v2-newsletter-service/templates/httproute.yaml` (a matching path/regex in `rules`) and `charts/lfx-v2-newsletter-service/templates/ruleset.yaml` (an authorization rule for it) cover it. When a template gates a new env var or route, confirm the condition names the config the feature actually needs, not a neighbouring flag that merely tends to be set alongside it.

**Empirical citation:** PR #52 (MERGED) — Copilot `3617009921` — "This route is unreachable in deployed environments. The chart's HTTPRoute only forwards `/projects/.../newsletters...` and `/newsletter-opens/...`." Resolved in `5e98c3ca`. The same class recurred once more on an eligible PR: PR #58 — Copilot `3647215361`, resolved in `b3f0ba29`, where the sub-lesson was to gate on the dependency itself (`app.send.provider == "sendgrid"` was used where the webhook key ref was what mattered). **Two** independent eligible PRs (#52, #58); the clearest repeat-miss on this repo. PR #57 (archive endpoints) matched the same class but was closed unmerged and never fixed, so under the eligibility rule in `README.md` ("matches on open or unmerged branches stay in the held set above and never raise a promotion count") it is held evidence and is **not** counted here. Verified at HEAD `f13d015`: `httproute.yaml:35` carries the opt-outs regex.

**Failure message:** new route registered in the handler but not wired into the chart's HTTPRoute and Heimdall RuleSet — unreachable once deployed.

**Fix:** add the path to `templates/httproute.yaml` `rules` and a matching authorization rule to `templates/ruleset.yaml` in the same change; gate any conditional on the config the feature actually depends on.

---

<!--
Removed 2026-07-31 (LFXV2-2903, PR #65 review): the
`chart/allowlist-default-must-match-peer-service` entry, demoted from the firing set
back to held/unpromoted evidence.

Why: its only locally-decidable predicate flagged EVERY non-empty peer-validated
default as a mismatch, including a value the peer already permits, and `Important`
matches always report — so the entry produced a blocking false positive on correctly
coordinated defaults. Four reformulations failed to fix that. Reading the peer's
actual allowlist is unrunnable (a detached single-repo snapshot never contains the
peer), and requiring evidence that a value *was* uncoordinated makes the rule
unfirable on exactly the new values it exists to catch, because that evidence lives
in the peer repo too.

Held, not deleted: the underlying miss is real — PR #56 (MERGED)
`charts/lfx-v2-newsletter-service/values.yaml`, Copilot `3626458835`, "This chart now
permits `aaif.io`, but the current email-service chart … configure
`SMTP_ALLOWED_REPLY_TO_DOMAINS` as only `linuxfoundation.org` … after which
email-service rejects every recipient", resolved in `3f0025ef`. The entry returns if a
locally decidable predicate is found. The cross-service sync requirement itself remains
documented where it is owned, in `docs/service-helm-chart.md`.

Published counts in `README.md` were reduced with this removal.
-->

## `chart/values-doc-drift-on-new-option` — Nit

**Pattern:** a new chart option (e.g. `database.external.shape=fields`) is added but the existing `values.yaml` comment / `docs/service-helm-chart.md` still describes only the old behavior, so chart users miss the new supported shape.

**Detect:** when a chart value is added or renamed, confirm the in-file `values.yaml` comment and `docs/service-helm-chart.md` describe it. Per `docs/service-helm-chart.md` change checklist: "Update `CLAUDE.md` and this document when adding or renaming chart values."

**Empirical citation:** PR #4 `charts/lfx-v2-newsletter-service/values.yaml:121` — Copilot — "The top-level `database.mode` docs still say `external` requires a Secret containing `DATABASE_URL`, but this PR adds `database.external.shape=fields` … Please update the earlier `database.mode` comment so chart users don't miss the new supported external shape."

**Failure message:** new chart value added but `values.yaml` comment / chart docs not updated.

**Fix:** update the `values.yaml` comment and `docs/service-helm-chart.md` in the same change, per the chart-doc change checklist.
