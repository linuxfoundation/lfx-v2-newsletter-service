<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Service Layer and Tests

Patterns for the service layer's project-scoping conventions, the dependency-wiring layering, and the test fakes that are supposed to prove scoping holds. Two of the three are about cross-project data exposure — the failure mode where a test suite stays green while a project can read another project's data.

**Read when:** anything under `internal/service/`, `cmd/newsletter-api/service/implementations.go`, or any `*_test.go` changed.

---

## `service/validate-projectuid-at-every-entry-point` — Important

**Pattern:** a new project-scoped service operation passes `projectUID` straight to persistence without validating it, breaking a convention every other entry point holds. A whitespace-only or empty project then returns `200` with an empty list instead of a `400`.

**Detect:** in `internal/service/`, for every added or changed exported method that takes a `projectUID`, confirm it validates that argument (trim, reject empty) before any repository call — the way the established entry points do at `internal/service/newsletter.go:70,109,144,169` and `internal/service/analytics.go:47`. Flag a new project-scoped method that forwards `projectUID` to persistence unvalidated.

**Empirical citation:** PR #52 (MERGED) `internal/service/unsubscribe.go` — Copilot `3617009972` — *"This is the only project-scoped service operation that passes `projectUID` straight to persistence … a whitespace-only project currently returns `200` with an empty list."* Resolved in `5e98c3ca`. Verified at HEAD `f13d015`: `internal/service/unsubscribe.go:136-138` validates, and the convention holds across the five sites above.

**Failure message:** project-scoped service method forwards `projectUID` to persistence without validating it — a blank project returns `200` and an empty list instead of `400`.

**Fix:** trim and reject an empty `projectUID` with `ErrInvalidRequest` at the top of the method, matching the existing project-scoped entry points.

---

## `test/fake-must-capture-scoping-arguments` — Important

**Pattern:** a test fake or stub ignores the scoping argument it is handed (`projectUID`), returning a fixed result regardless. The tests pass, but they cannot fail if the production query drops or swaps the scope — so the suite gives no protection against exactly the cross-project data exposure it looks like it covers.

**Detect:** in `*_test.go`, for every fake implementing a `internal/domain/port` interface whose method takes a scoping argument, confirm the fake **records** that argument and the test **asserts** on it. Flag a stub that accepts `projectUID` and neither stores nor checks it, especially on paths returning recipient addresses or opt-out lists.

**Empirical citation:** PR #52 (MERGED) — Copilot `3617102503` — *"This stub ignores `projectUID` … A regression that queries or returns opt-outs for the wrong project could expose another project's email addresses while these tests still pass."* Resolved in `865fb80f`; the same class recurred immediately on PR #55 — Copilot `3623395460`, resolved in `c422ca3d`. Verified at HEAD `f13d015`: `internal/handler/unsubscribe_test.go:276-323`.

**Failure message:** test fake ignores the scoping argument — the suite cannot detect a cross-project query regression.

**Fix:** capture the scoping argument in the fake and assert on it in the test; add a case that fails if the wrong project's data is returned.

---

## `wiring/accept-narrow-port-not-concrete-repo` — Nit

**Pattern:** a constructor in the wiring layer accepts a concrete repository type where the documented layering calls for the narrow `internal/domain/port` interface the code actually needs.

**Detect:** in `cmd/newsletter-api/service/implementations.go`, confirm constructors take the `internal/domain/port` interface rather than a concrete `internal/repository` type.

**Empirical citation:** PR #32 (MERGED) `cmd/newsletter-api/service/implementations.go` — Copilot `3534090445` — resolved in `4309260e`. Verified at HEAD `f13d015`: `implementations.go:163-170`. Repo-specific because it maps to the documented `internal/domain/port/` layering.

**Failure message:** wiring constructor accepts a concrete repository type instead of the narrow domain port.

**Fix:** declare the parameter as the `internal/domain/port` interface the constructor uses.
