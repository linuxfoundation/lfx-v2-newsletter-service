<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Persistence and Schema

Patterns specific to this repo's embedded-`schema.sql` model and its Bun/Postgres repository: the schema is applied idempotently on every startup (`CREATE … IF NOT EXISTS`, advisory-lock serialized), so every constraint, index, and generated column has to survive re-application; and the open-tracking dedup mechanism has a precise index/`ON CONFLICT` coupling that broke twice across PRs. These are the database-layer patterns Copilot and the maintainer flagged on PRs #3, #6, and #9.

**Read when:** `internal/schema/schema.sql`, `internal/schema/schema.go`, or `internal/repository/postgres.go` changed.

---

## `persistence/non-immutable-generated-column-expr` — Critical

**Pattern:** a Postgres generated stored column or index expression uses a `STABLE` (not `IMMUTABLE`) function — e.g. `date_trunc(...)` over a `TIMESTAMPTZ`, or a bare `AT TIME ZONE` on a `timestamptz`. Postgres rejects non-`IMMUTABLE` expressions in generated stored columns and index expressions, so the schema fails to apply at startup.

**Detect:** in `internal/schema/schema.sql`, for any `GENERATED ALWAYS AS (...) STORED` column or `CREATE … INDEX … (expr)`, confirm the expression is `IMMUTABLE`. `date_trunc('hour', ts)` directly in an index expression is not allowed; bucket into a generated column whose expression is immutable (the repo uses `date_trunc('hour', opened_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`) and index the plain column.

**Empirical citation:** PR #6 title — "fix(schema): replace non-IMMUTABLE index expression with generated column". PR #6 `internal/schema/schema.sql:69` — Copilot — "Generated stored columns require an `IMMUTABLE` expression, so this may still fail at migration time with 'generation expression is not immutable'." (The shipped `… AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'` form is immutable and applies cleanly; verify any change keeps that property.)

**Failure message:** generated column / index expression is not `IMMUTABLE` — `schema.sql` will fail to apply at startup.

**Fix:** move the time-bucketing into a generated stored column with an immutable expression and build the unique index on the plain column. Do not place `date_trunc` or a bare `AT TIME ZONE` directly in an index expression.

---

## `persistence/on-conflict-target-mismatch` — Critical

**Pattern:** repository code uses `ON CONFLICT ON CONSTRAINT <name>` but `<name>` is created in `schema.sql` as a UNIQUE INDEX, not a UNIQUE CONSTRAINT. Postgres `ON CONFLICT ON CONSTRAINT` only resolves named `pg_constraint` entries, so the insert errors at runtime with "constraint … does not exist".

**Detect:** for every `ON CONFLICT ON CONSTRAINT <name>` in `internal/repository/postgres.go`, check `schema.sql`: if `<name>` is a `CREATE UNIQUE INDEX`, the insert will fail. Either switch to index-inference (`ON CONFLICT (col, col, col) DO NOTHING`) matching the unique index, or define a real `ADD CONSTRAINT … UNIQUE`.

**Empirical citation:** PR #3 `internal/repository/postgres.go:232` — Copilot — "RecordOpen uses `ON CONFLICT ON CONSTRAINT uq_opens_newsletter_recipient_hour DO NOTHING`, but `uq_opens_newsletter_recipient_hour` is created as a UNIQUE INDEX in schema.sql, not a UNIQUE CONSTRAINT … this insert will error at runtime." Re-raised on PR #6 `internal/schema/schema.sql:73`.

**Failure message:** `ON CONFLICT ON CONSTRAINT` names a UNIQUE INDEX (not a constraint) — insert fails at runtime.

**Fix:** use index-inference `ON CONFLICT (newsletter_id, recipient_hash, opened_at_hour) DO NOTHING` to match the unique index, or create a real named UNIQUE constraint.

---

## `persistence/non-idempotent-constraint` — Important

**Pattern:** a new `ALTER TABLE … ADD CONSTRAINT` is added to `schema.sql` without an idempotency guard. Postgres has no native `ADD CONSTRAINT IF NOT EXISTS`, so re-applying the embedded schema on the next pod startup errors with "constraint already exists".

**Detect:** in `internal/schema/schema.sql`, every `ADD CONSTRAINT` must be wrapped in a `DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '<name>') THEN … END$$;` block (the repo's established pattern). Plain `CREATE … IF NOT EXISTS` is fine for tables/indexes; constraints need the `pg_constraint` guard.

**Empirical citation:** PR #9 `internal/schema/schema.sql:31` — Copilot asked for a CHECK tying `status='sent'` to `group_id IS NOT NULL`; resolved in `7232a31` by adding two CHECKs "following the same `pg_constraint` lookup pattern already used for `newsletter_opens_recipient_hash_format` … Wrapped in a `DO$$ … IF NOT EXISTS … END$$` block because PG has no native `ADD CONSTRAINT IF NOT EXISTS`, so re-running `schema.sql` against an existing DB stays a no-op."

**Failure message:** `ADD CONSTRAINT` not guarded by a `pg_constraint` IF-NOT-EXISTS block — re-applying `schema.sql` will error.

**Fix:** wrap the `ADD CONSTRAINT` in a `DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '<name>') THEN … END$$;` block.

---

## `persistence/sent-state-data-integrity-checks` — Important

**Pattern:** a value documented as a required invariant on a state transition (e.g. `group_id` is a UUID and `status='sent' ⇒ group_id IS NOT NULL`) is persisted as unconstrained `TEXT` with no DB-level enforcement, so a misbehaving caller can write a malformed or missing value.

**Detect:** for columns documented as a UUID / "immutable once set" / required-on-send in `schema.sql` comments or `docs/newsletter-service-contract.md`, confirm a CHECK constraint enforces the format and the cross-column invariant (`status <> 'sent' OR group_id IS NOT NULL`). Flag a documented invariant that lives only in application code.

**Empirical citation:** PR #9 `internal/schema/schema.sql:31` — Copilot — "The new `group_id` column is documented as a correlation identifier (UUID) and 'immutable once set', but the schema does not enforce format or invariants. Consider … a CHECK constraint (and optionally a constraint tying `status='sent'` to `group_id IS NOT NULL`)." Resolved in `7232a31` with `newsletters_group_id_uuid_format` and `newsletters_sent_requires_group_id`.

**Failure message:** documented send-state invariant (UUID format / `sent ⇒ group_id NOT NULL`) not enforced by a DB CHECK.

**Fix:** add idempotent CHECK constraints enforcing the UUID format and the `status='sent' ⇒ group_id IS NOT NULL` invariant. `TEXT` + CHECK is acceptable (avoids a column-type alter on existing deployments) as long as the constraints exist.

---

## `persistence/missing-keyset-pagination-index` — Important

**Pattern:** a keyset (`ORDER BY updated_at DESC, id DESC`, cursor `(updated_at, id) < (?, ?)`) list query has no composite index covering the filter columns plus the cursor order, so every list call does a heap scan + sort.

**Detect:** for `ListAll` keyset queries in `internal/repository/postgres.go`, confirm `schema.sql` has an index covering the filter plus cursor order — currently `(project_uid, updated_at DESC, id DESC)` (the repo's `idx_newsletters_list`; the original was `(context_type, context_uid, …)` before project scoping). Flag a new keyset query whose ordering/filter columns aren't covered by an index.

**Empirical citation:** PR #3 `internal/repository/postgres.go:119` — dealako (minor) — "the schema has no composite index covering `(context_type, context_uid, updated_at DESC, id DESC)`. List queries will do a heap scan + sort on every call." Resolved in `959e23d` by adding `idx_newsletters_list`.

**Failure message:** keyset list query not backed by a composite index covering filter + cursor order — heap scan + sort per call.

**Fix:** add `CREATE INDEX IF NOT EXISTS … ON newsletters (project_uid, updated_at DESC, id DESC)` (or the equivalent for the new query) covering both filter and cursor columns.

---

## `persistence/advisory-lock-no-timeout` — Important

**Pattern:** the startup schema bootstrap takes `pg_advisory_xact_lock` to serialize concurrent pods, but without a statement timeout. If a peer pod holds the lock and hangs (slow rolling deploy), the new pod blocks indefinitely and every subsequent pod queues behind it.

**Detect:** in `internal/schema/schema.go::Apply`, confirm `SET LOCAL statement_timeout = '60s'` runs in the same transaction before `SELECT pg_advisory_xact_lock(...)`.

**Empirical citation:** PR #3 `internal/schema/schema.go:40` — dealako (minor) — "if the lock is already held by another pod … the new pod blocks indefinitely waiting for the advisory lock … Add a statement timeout on the lock acquisition to bound the wait." Resolved in `959e23d`: "`Apply` now runs `SET LOCAL statement_timeout = '60s'` before `pg_advisory_xact_lock`."

**Failure message:** advisory-lock bootstrap has no `statement_timeout` — a hung peer pod stalls all subsequent rollouts.

**Fix:** run `SET LOCAL statement_timeout = '60s'` in the bootstrap transaction before acquiring `pg_advisory_xact_lock`, so a hung peer surfaces as a timeout instead of an indefinite stall.
