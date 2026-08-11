-- Copyright The Linux Foundation and each contributor to LFX.
-- SPDX-License-Identifier: MIT

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS newsletters (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    project_uid       TEXT         NOT NULL,
    subject           TEXT         NOT NULL,
    body_html         TEXT         NOT NULL,
    ed_reply_email    TEXT         NOT NULL,
    committee_uids    TEXT[]       NOT NULL DEFAULT '{}',
    status            TEXT         NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','sending','sent')),
    sent_at           TIMESTAMPTZ,
    total_recipients  INT          NOT NULL DEFAULT 0,
    created_by        TEXT         NOT NULL,
    version           BIGINT       NOT NULL DEFAULT 1,
    created_at        TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Forward-compatibility shim for environments that ran the previous schema
-- (foundation/project context, no project_uid). Drops the now-defunct context
-- columns and renames context_uid → project_uid in place so existing rows survive.
-- Safe to run on a fresh DB because the columns won't exist.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'newsletters' AND column_name = 'context_type'
    ) THEN
        ALTER TABLE newsletters DROP CONSTRAINT IF EXISTS newsletters_context_type_check;
        ALTER TABLE newsletters DROP COLUMN context_type;
    END IF;
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = 'newsletters' AND column_name = 'context_uid'
    ) THEN
        ALTER TABLE newsletters RENAME COLUMN context_uid TO project_uid;
    END IF;
END$$;

-- group_id is the lfx-v2-email-service correlation identifier. Minted by
-- the SendOrchestrator when a draft is marked sent so analytics can aggregate
-- per-recipient engagement records keyed by this id. Nullable on drafts;
-- immutable once set. Stored as TEXT (rather than UUID) so existing deployments
-- don't require a column-type alter, but the CHECK constraints below enforce
-- UUID format and the status='sent' ⇒ group_id NOT NULL invariant at the DB
-- layer in case a caller misbehaves.
ALTER TABLE newsletters
    ADD COLUMN IF NOT EXISTS group_id TEXT;

-- PG has no native IF NOT EXISTS on ADD CONSTRAINT; check pg_constraint first
-- so re-running schema.sql is a no-op.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'newsletters_group_id_uuid_format'
    ) THEN
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_group_id_uuid_format
            CHECK (group_id IS NULL OR group_id ~ '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$');
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'newsletters_sent_requires_group_id'
    ) THEN
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_sent_requires_group_id
            CHECK (status <> 'sent' OR group_id IS NOT NULL);
    END IF;
END$$;

-- Widen the status CHECK on databases created before the 'sending' state was
-- introduced. The inline column CHECK above auto-names itself
-- newsletters_status_check; if the existing constraint definition doesn't
-- mention 'sending', drop and re-add it with the widened value list. Purely
-- additive for existing rows ('draft'/'sent' both still pass), and a no-op on
-- fresh databases or on re-runs (the definition then contains 'sending').
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'newsletters_status_check'
          AND conrelid = 'newsletters'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%sending%'
    ) THEN
        ALTER TABLE newsletters DROP CONSTRAINT newsletters_status_check;
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_status_check
            CHECK (status IN ('draft','sending','sent'));
    END IF;
END$$;

-- send_provider records which provider actually dispatched each newsletter, so
-- analytics can route engagement reads to the store that holds the data:
-- 'email-service' (SES engagement read back over NATS) or 'sendgrid' (the local
-- engagement store populated by the SendGrid event webhook). Every historical
-- row predates SendGrid, so the column defaults to 'email-service' and the
-- backfill below stamps any pre-existing rows; the SendOrchestrator stamps the
-- active provider on every new send.
ALTER TABLE newsletters
    ADD COLUMN IF NOT EXISTS send_provider TEXT NOT NULL DEFAULT 'email-service';

-- Defensive backfill for a DB where an earlier partial run added the column
-- nullable (a first-time ADD COLUMN ... DEFAULT already backfills existing rows).
UPDATE newsletters SET send_provider = 'email-service'
    WHERE send_provider IS NULL OR send_provider = '';

-- Re-assert the default and NOT NULL. A first run's ADD COLUMN ... NOT NULL
-- DEFAULT sets both, but ADD COLUMN IF NOT EXISTS is a no-op if a prior partial
-- run added the column nullable, and the CHECK below permits NULL — so a nullable
-- column could keep accepting NULL providers. Safe now the backfill cleared any
-- NULLs; both statements are idempotent.
ALTER TABLE newsletters ALTER COLUMN send_provider SET DEFAULT 'email-service';
ALTER TABLE newsletters ALTER COLUMN send_provider SET NOT NULL;

-- Constrain to the known providers (mirrors the model.SendProvider* constants).
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'newsletters_send_provider_check'
    ) THEN
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_send_provider_check
            CHECK (send_provider IN ('email-service','sendgrid'));
    END IF;
END$$;

-- scheduled_at / batch_id support scheduled sends (LFXV2-2685). scheduled_at
-- carries two meanings depending on status: while draft, it is the author's
-- saved intent (set on create/update, does not by itself send anything);
-- once a schedule is armed it is the committed release time handed to the
-- provider as send_at, and batch_id is the provider batch identifier shared
-- by every recipient in that fan-out so the provider releases them together
-- and the whole batch can be cancelled.
ALTER TABLE newsletters
    ADD COLUMN IF NOT EXISTS scheduled_at TIMESTAMPTZ;
ALTER TABLE newsletters
    ADD COLUMN IF NOT EXISTS batch_id TEXT;

-- Widen the status CHECK to add 'scheduled', mirroring the 'sending' widening
-- above: additive for existing rows, a no-op once the constraint already
-- mentions 'scheduled'.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'newsletters_status_check'
          AND conrelid = 'newsletters'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%scheduled%'
    ) THEN
        ALTER TABLE newsletters DROP CONSTRAINT newsletters_status_check;
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_status_check
            CHECK (status IN ('draft','sending','scheduled','sent'));
    END IF;
END$$;

-- A scheduled row is armed for exactly one provider batch, so it needs a
-- group_id too — widen the sent-only invariant to cover it. Drop-and-re-add
-- inside a definition-guarded block so re-running schema.sql is a no-op.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conname = 'newsletters_sent_requires_group_id'
          AND conrelid = 'newsletters'::regclass
          AND pg_get_constraintdef(oid) NOT LIKE '%scheduled%'
    ) THEN
        ALTER TABLE newsletters DROP CONSTRAINT newsletters_sent_requires_group_id;
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_sent_requires_group_id
            CHECK (status NOT IN ('sent','scheduled') OR group_id IS NOT NULL);
    END IF;
END$$;

-- A scheduled row must carry both the release time and the provider batch
-- id — that pair is what "armed" means. Deliberately one-directional: a draft
-- may carry scheduled_at with no batch_id (the author's saved-but-unarmed
-- intent), which this constraint permits.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'newsletters_scheduled_requires_schedule'
    ) THEN
        ALTER TABLE newsletters
            ADD CONSTRAINT newsletters_scheduled_requires_schedule
            CHECK (status <> 'scheduled' OR (scheduled_at IS NOT NULL AND batch_id IS NOT NULL));
    END IF;
END$$;

-- Prevent legacy pods (running old code before the scheduled-sends feature)
-- from mutating armed scheduled rows during a rolling deploy. An armed row
-- (batch_id IS NOT NULL) has been handed to a provider scheduler; only the
-- new code that understands the scheduler contract should touch it. This guard
-- uses a session-local GUC flag to distinguish legitimate app mutations from
-- legacy pod mutations: the new binary sets 'app.allow_armed_mutation' before
-- each legitimate update, allowing it to pass; legacy pods never set it and
-- are unconditionally blocked. CREATE OR REPLACE + DROP/CREATE keep re-running
-- schema.sql a no-op.
CREATE OR REPLACE FUNCTION reject_armed_scheduled_mutation() RETURNS trigger AS $$
BEGIN
    -- Only reject if the row was armed (OLD.batch_id IS NOT NULL) and the
    -- caller did not explicitly authorize the mutation via the GUC flag.
    -- The new binary always sets app.allow_armed_mutation = 'true' before
    -- legitimate mutations, so it passes; legacy pods never set it and are blocked.
    IF OLD.batch_id IS NOT NULL AND current_setting('app.allow_armed_mutation', true) IS DISTINCT FROM 'true' THEN
        RAISE EXCEPTION 'cannot mutate armed scheduled row (batch_id is set) — service version mismatch during rolling deploy';
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_reject_armed_scheduled_mutation ON newsletters;
CREATE TRIGGER trg_reject_armed_scheduled_mutation
    BEFORE UPDATE ON newsletters
    FOR EACH ROW EXECUTE FUNCTION reject_armed_scheduled_mutation();

-- Supports the due-schedule sweep (status = 'scheduled' AND scheduled_at <= now()).
CREATE INDEX IF NOT EXISTS idx_newsletters_scheduled_due
    ON newsletters (scheduled_at) WHERE status = 'scheduled';

-- Replace the old (context_type, context_uid) indexes with project-scoped
-- equivalents. The composite list index supports the (project_uid, updated_at
-- DESC, id DESC) keyset pagination used by ListAll.
DROP INDEX IF EXISTS idx_newsletters_context;
DROP INDEX IF EXISTS idx_newsletters_list;
CREATE INDEX IF NOT EXISTS idx_newsletters_project ON newsletters (project_uid);
CREATE INDEX IF NOT EXISTS idx_newsletters_status  ON newsletters (status);
CREATE INDEX IF NOT EXISTS idx_newsletters_list
    ON newsletters (project_uid, updated_at DESC, id DESC);

-- GIN index on the committee audience array supports the committee-scoped
-- containment query (committee_uids @> ARRAY[uid]) used by ListSentByCommittee.
-- The query's (sent_at DESC, id DESC) keyset ordering is deliberately NOT
-- index-backed: array containment can't participate in a btree, and the GIN
-- filter narrows to one committee's sent newsletters — a bounded set (tens,
-- not thousands) that Postgres sorts in memory per page without issue.
CREATE INDEX IF NOT EXISTS idx_newsletters_committee_uids
    ON newsletters USING GIN (committee_uids);

-- newsletter_opens captures one row per open event. recipient_hash is a SHA-256
-- of the lowercased recipient email so we can compute unique opens without
-- persisting PII in this table beyond what the newsletters table already holds.
-- The CHECK constraint enforces the same shape as the handler / service layer
-- regex (64-char lowercase hex), so a bug in any caller can't grow this table
-- with arbitrary text.
CREATE TABLE IF NOT EXISTS newsletter_opens (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    newsletter_id   UUID         NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    recipient_hash  TEXT         NOT NULL,
    opened_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- PG has no native IF NOT EXISTS on ADD CONSTRAINT; check pg_constraint
-- before adding so re-running schema.sql against an existing DB is a no-op.
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint WHERE conname = 'newsletter_opens_recipient_hash_format'
    ) THEN
        ALTER TABLE newsletter_opens
            ADD CONSTRAINT newsletter_opens_recipient_hash_format
            CHECK (recipient_hash ~ '^[a-f0-9]{64}$');
    END IF;
END$$;

CREATE INDEX IF NOT EXISTS idx_opens_newsletter            ON newsletter_opens (newsletter_id);
CREATE INDEX IF NOT EXISTS idx_opens_newsletter_recipient  ON newsletter_opens (newsletter_id, recipient_hash);
CREATE INDEX IF NOT EXISTS idx_opens_opened_at             ON newsletter_opens (newsletter_id, opened_at);

-- Bound runaway growth on the unauthenticated open-tracking pixel: collapse
-- repeat hits from the same recipient within the same hour into a single row.
-- opened_at_hour stores the UTC hour bucket so the unique index below can use
-- a plain column (date_trunc is STABLE not IMMUTABLE and cannot appear in an
-- index expression directly).
ALTER TABLE newsletter_opens
    ADD COLUMN IF NOT EXISTS opened_at_hour TIMESTAMPTZ
        GENERATED ALWAYS AS (date_trunc('hour', opened_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC') STORED;

-- The application is expected to use ON CONFLICT DO NOTHING when inserting.
CREATE UNIQUE INDEX IF NOT EXISTS uq_opens_newsletter_recipient_hour
    ON newsletter_opens (newsletter_id, recipient_hash, opened_at_hour);

-- newsletter_unsubscribes records project-scoped opt-outs. A row means the
-- given email address has unsubscribed from all newsletters for that
-- project_uid; the address may still receive newsletters for other projects.
-- Email is stored lowercased so the unique index makes the insert idempotent
-- without needing a CITEXT extension.
--
-- updated_at is currently write-once (set equal to created_at on insert and
-- never touched). It is reserved for a future re-subscribe / preference-update
-- flow where an opt-out row may be mutated (e.g. soft-deleted with a
-- resubscribed_at timestamp) rather than hard-deleted, so the column is
-- declared now to avoid a later ALTER TABLE.
CREATE TABLE IF NOT EXISTS newsletter_unsubscribes (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_uid TEXT        NOT NULL,
    email       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_unsubscribes_project_email
    ON newsletter_unsubscribes (project_uid, email);

-- ---------------------------------------------------------------------------
-- SendGrid engagement (LFXV2-2388)
--
-- With SendGrid the newsletter service brokers sends directly and therefore
-- owns engagement (the SES path reads it back from email-service over NATS).
-- The SendGrid dispatcher records one row per send below; the SendGrid event
-- webhook applies delivered / open / failed events. Rows are keyed by the
-- email_id minted into custom_args on mail/send, which the webhook echoes back.
-- ---------------------------------------------------------------------------
CREATE TABLE IF NOT EXISTS sendgrid_recipient_engagement (
    email_id       TEXT        PRIMARY KEY,
    group_id       TEXT        NOT NULL,
    to_email       TEXT        NOT NULL DEFAULT '',
    sent_at        TIMESTAMPTZ,
    delivered      BOOLEAN     NOT NULL DEFAULT FALSE,
    delivered_at   TIMESTAMPTZ,
    opened         BOOLEAN     NOT NULL DEFAULT FALSE,
    open_count     INTEGER     NOT NULL DEFAULT 0,
    last_opened_at TIMESTAMPTZ,
    failed         BOOLEAN     NOT NULL DEFAULT FALSE,
    failed_at      TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_sg_engagement_group ON sendgrid_recipient_engagement (group_id);

-- Individual open events, for unique opens and the daily-opens series.
-- Deduplicated by SendGrid's sg_event_id so a webhook redelivery is a no-op;
-- group_id is derived by joining to sendgrid_recipient_engagement on email_id.
CREATE TABLE IF NOT EXISTS sendgrid_open_events (
    sg_event_id TEXT        PRIMARY KEY,
    email_id    TEXT        NOT NULL,
    opened_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sg_open_events_email ON sendgrid_open_events (email_id);

-- Tombstone of reverted send groups. A fully-failed send reverts to draft,
-- clears its group_id, and purges the engagement rows above. But an ambiguous
-- transport/5xx send may still have been accepted by SendGrid, so a delayed
-- delivered/open/bounce webhook can arrive AFTER the purge. The webhook Apply*
-- methods upsert (self-heal), which would recreate a row — re-persisting
-- recipient PII (to_email) under a group_id no newsletter references. A row here
-- durably marks a group reverted so the trigger below rejects any late write.
CREATE TABLE IF NOT EXISTS sendgrid_reverted_groups (
    group_id    TEXT        PRIMARY KEY,
    reverted_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Reject an INSERT of an engagement row for a reverted group. This fires BEFORE
-- INSERT and returns NULL to skip the row, so a delayed webhook's self-healing
-- upsert cannot recreate purged PII: a reverted group's rows were already
-- deleted, so every webhook write for it is a fresh INSERT that this stops. Rows
-- for a live (non-reverted) group are unaffected.
--
-- It first takes a transaction-scoped advisory lock keyed by the group, the same
-- lock RevertGroup takes, so the tombstone check and this insert are serialized
-- against a concurrent revert rather than racing under READ COMMITTED. Ordering:
-- if the revert commits first the webhook then sees the tombstone and skips; if
-- the webhook inserts first the revert blocks on the lock, then deletes the
-- just-inserted row. Either way no engagement row survives a revert. The lock is
-- released on commit/rollback. CREATE OR REPLACE + DROP/CREATE keep re-running
-- schema.sql a no-op.
CREATE OR REPLACE FUNCTION sendgrid_reject_reverted_engagement() RETURNS trigger AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended('sendgrid_group:' || NEW.group_id, 0));
    IF EXISTS (SELECT 1 FROM sendgrid_reverted_groups g WHERE g.group_id = NEW.group_id) THEN
        RETURN NULL;
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_sg_reject_reverted_engagement ON sendgrid_recipient_engagement;
CREATE TRIGGER trg_sg_reject_reverted_engagement
    BEFORE INSERT ON sendgrid_recipient_engagement
    FOR EACH ROW EXECUTE FUNCTION sendgrid_reject_reverted_engagement();
