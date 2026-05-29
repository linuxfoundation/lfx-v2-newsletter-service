<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Chart

Patterns for the service-local Helm chart: secret hygiene in the deployment template, namespace coupling between Heimdall middleware and the HTTPRoute, and fail-fast handling of mode/shape selectors. These are the patterns the maintainer reviewer and Copilot flagged on PRs #3, #4, and #7.

**Read when:** any file under `charts/lfx-v2-newsletter-service/` changed — especially `templates/deployment.yaml`, `templates/heimdall-middleware.yaml`, `templates/httproute.yaml`, and `values.yaml`.

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

**Empirical citation:** PR #7 `charts/lfx-v2-newsletter-service/templates/heimdall-middleware.yaml:15` — Copilot — "The Middleware is referenced from templates/httproute.yaml via an ExtensionRef without a namespace, so it must be created in the same namespace as the HTTPRoute ({{ .Values.lfx.namespace }}). Using {{ .Release.Namespace }} here will break resolution." (Open thread; verify the namespace coupling on any middleware/HTTPRoute change — note `docs/service-helm-chart.md` says routing resources moved namespaces.)

**Failure message:** Heimdall `Middleware` namespace uses `.Release.Namespace` but the HTTPRoute `ExtensionRef` resolves against `.Values.lfx.namespace` — Traefik drops the route.

**Fix:** render the `Middleware` into `{{ .Values.lfx.namespace }}` (the HTTPRoute namespace), matching the unnamespaced `ExtensionRef`.

---

## `chart/mode-shape-selector-no-fail-fast` — Important

**Pattern:** a chart `mode`/`shape` selector (e.g. `database.external.shape`) silently falls back to a default path for any unrecognized value, so a typo (`"field"` instead of `"fields"`) renders the wrong env vars with no Helm error and fails only at runtime. Related: a required secret name defaults to empty string and renders an invalid `secretKeyRef`.

**Detect:** in `charts/lfx-v2-newsletter-service/templates/deployment.yaml`, confirm selector branches explicitly handle each valid value and `fail` on anything else; confirm required values (`database.external.secretName` in external mode) use Helm's `required` so rendering fails with a clear message instead of producing an empty/invalid manifest.

**Empirical citation:** PR #4 `charts/lfx-v2-newsletter-service/templates/deployment.yaml:127` — Copilot — "`database.external.shape` silently falls back to the `DATABASE_URL` path for any value other than 'fields'. A typo like 'field' would render the wrong env vars with no Helm error … call `fail` on any other value." Also PR #3 `…/deployment.yaml:99` — Copilot — "the default value is an empty string. That produces an invalid manifest (empty Secret name). Consider using Helm's `required` function."

**Failure message:** chart mode/shape selector silently falls back on a bad value (or a required secret name defaults empty) — misconfiguration surfaces only at runtime.

**Fix:** explicitly branch on each valid selector value and `fail "<message>"` on others; wrap required values (e.g. `secretName`) in `required` so `helm template`/upgrade fails fast.

---

## `chart/values-doc-drift-on-new-option` — Nit

**Pattern:** a new chart option (e.g. `database.external.shape=fields`) is added but the existing `values.yaml` comment / `docs/service-helm-chart.md` still describes only the old behavior, so chart users miss the new supported shape.

**Detect:** when a chart value is added or renamed, confirm the in-file `values.yaml` comment and `docs/service-helm-chart.md` describe it. Per `docs/service-helm-chart.md` change checklist: "Update `CLAUDE.md` and this document when adding or renaming chart values."

**Empirical citation:** PR #4 `charts/lfx-v2-newsletter-service/values.yaml:121` — Copilot — "The top-level `database.mode` docs still say `external` requires a Secret containing `DATABASE_URL`, but this PR adds `database.external.shape=fields` … Please update the earlier `database.mode` comment so chart users don't miss the new supported external shape."

**Failure message:** new chart value added but `values.yaml` comment / chart docs not updated.

**Fix:** update the `values.yaml` comment and `docs/service-helm-chart.md` in the same change, per the chart-doc change checklist.
