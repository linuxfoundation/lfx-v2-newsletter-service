<!-- Copyright The Linux Foundation and each contributor to LFX. -->
<!-- SPDX-License-Identifier: MIT -->

# Subscriber Import

This document owns the `newsletter_subscriptions` table and the `cmd/newsletter-import` CLI that loads it. Update it in the same PR as any change to the table schema, the CSV contract, or the importer's behavior.

Delivered under [LFXV2-2713](https://linuxfoundation.atlassian.net/browse/LFXV2-2713), part of the epic [LFXV2-2683](https://linuxfoundation.atlassian.net/browse/LFXV2-2683). This is phase 1 (schema and importer) only. The table is not yet a recipient-resolution source — see the "Not Yet a Resolution Source" note in `docs/recipient-resolution.md` — and there is no list-management CRUD, UI, or suppression/bounce handling here; those are separate, later tickets.

## What this is for

An external list export, e.g. a community's existing mailing-list subscriber roster, needs to land in LFX so a later change (LFXV2-2684) can send newsletters to it. This importer is the one-time (or occasional, re-runnable) bulk load of that CSV export into the `newsletter_subscriptions` table. It is not a general subscribe/unsubscribe API and does not touch `newsletters` or `newsletter_unsubscribes`.

## Table

`newsletter_subscriptions` (see `internal/schema/schema.sql`):

| Column | Type | Notes |
| --- | --- | --- |
| `id` | UUID | primary key, generated |
| `list_id` | TEXT | which imported list this row belongs to; see "list_id enumeration" below |
| `email` | TEXT | lowercased on import |
| `subscribed` | BOOLEAN | current opt-in state; see "Compliance" below |
| `source` | TEXT | how the row was created; the importer always writes `import` |
| `subscribed_at` | TIMESTAMPTZ | defaults to import time |
| `unsubscribed_at` | TIMESTAMPTZ | nullable; not set by the importer |
| `created_at` / `updated_at` | TIMESTAMPTZ | bookkeeping |

A unique index on `(list_id, email)` is the sole idempotency guarantee: importing the same file twice, or two overlapping exports, inserts each `(list_id, email)` pair at most once. A partial index on `list_id WHERE subscribed` supports the common "who's currently subscribed to this list" query shape once a reader exists.

### list_id enumeration

`list_id` is constrained by `newsletter_subscriptions_list_id_check` to the single value `aaif-user-community` today. Widen it the same way `newsletters_status_check` is widened elsewhere in `internal/schema/schema.sql`: a guarded `DO $$ ... IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = '...') THEN ALTER TABLE ... $$` block that drops and re-adds the constraint with the new value list. Do not relax this to an unconstrained TEXT column — the CHECK is what stops a typo'd `list_id` from silently creating a new, unintended list.

## CSV input contract

The importer reads a CSV with a header row. Two columns are required (matched case-insensitively):

- `email` — one address per row. Validated with Go's `net/mail.ParseAddress`, so `Name <email@example.com>` is accepted and normalized down to the bare, lowercased address.
- `subscribed` — the row's opt-in state. Accepts `true`/`false`, `1`/`0`, `t`/`f`, and `yes`/`no`, case-insensitively.

An optional `source` column overrides the default `import` value per row (rarely needed; leave it out unless you have a specific reason to tag rows differently).

Any other columns are ignored.

### Compliance: verbatim subscribed state

`subscribed` must round-trip Gatewaze's own `list_subscriptions.subscribed` value unchanged. The importer requires the column and refuses to run without it — it does not default missing values to `true` — because doing so risks resubscribing someone who already opted out in Gatewaze. When you generate the export CSV from Gatewaze, include `list_subscriptions.subscribed` verbatim as the `subscribed` column.

### Validation and dedup

Per row, in order:

1. `email` must be non-empty and parse as a valid address, or the row is rejected.
2. `subscribed` must parse as one of the accepted boolean spellings, or the row is rejected.
3. Valid rows are deduplicated in memory by lowercased email. If the same email appears more than once in the file, the last occurrence wins (its `subscribed` and `source` values are the ones imported) and the count is reported as `deduped_in_file`.

Rejected rows never stop the import. They are written to the `-rejected-out` CSV (original columns plus a trailing `reason` column) and counted in the summary. The process exits non-zero only for a non-validation failure — a malformed CSV, a missing required column, or a database error — never because some rows failed validation.

## Running the importer

```console
$ export DATABASE_URL=postgres://user:pass@host:5432/dbname
$ go run ./cmd/newsletter-import -file ./aaif-subscribers.csv -list-id aaif-user-community
total_rows=15234 valid=15198 invalid=36 deduped_in_file=12 inserted=15186 already_present=0
rejected rows written to ./rejected.csv
```

Or build it once with `make build-import` and run `./bin/lfx-v2-newsletter-service/newsletter-import`.

Flags:

| Flag | Default | Notes |
| --- | --- | --- |
| `-file` | (required) | path to the input CSV, or `-` to read from stdin |
| `-list-id` | `aaif-user-community` | must satisfy the `list_id` CHECK constraint |
| `-batch-size` | `1000` | rows per database batch (one INSERT per batch, so this is also the effective transaction size) |
| `-rejected-out` | `./rejected.csv` | where rejected rows are written |

Connection: the CLI connects to Postgres the same way `newsletter-api` does — `DATABASE_URL` if set, otherwise composed in-process from `PGHOST`/`PGPORT`/`PGUSER`/`PGPASSWORD`/`PGDATABASE`. It applies the embedded schema (`schema.Apply`) on startup, same as the API service, so it is safe to run against a fresh database.

### Summary fields

| Field | Meaning |
| --- | --- |
| `total_rows` | data rows read from the CSV (excludes the header) |
| `valid` | rows that passed per-row validation |
| `invalid` | rows rejected by validation (written to `-rejected-out`) |
| `deduped_in_file` | valid rows that were a repeat email within this file (not counted again in `inserted`/`already_present`) |
| `inserted` | rows newly written to `newsletter_subscriptions` |
| `already_present` | rows that matched an existing `(list_id, email)` row and were left untouched |

## Re-running

The importer is safe to re-run against the same or an updated export. Rows already present for a given `(list_id, email)` are never overwritten — including their `subscribed` value — so a re-run only adds genuinely new rows. To intentionally update an existing row's `subscribed` state, a future targeted operation is needed; this importer does not do updates.

## Testing

- `internal/service/subscription_importer_test.go` — unit tests over `SubscriptionImporter.Import` against an in-memory fake repository (validation, dedup, batching, already-present handling). No database required.
- `internal/repository/postgres_subscription_test.go` (`-tags integration`, gated on `NEWSLETTER_TEST_DATABASE_URL`) — exercises the real unique constraint, `ON CONFLICT DO NOTHING`, and batching against Postgres. Run via `make test-integration`.
