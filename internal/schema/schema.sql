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
    status            TEXT         NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','sent')),
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

-- Replace the old (context_type, context_uid) indexes with project-scoped
-- equivalents. The composite list index supports the (project_uid, updated_at
-- DESC, id DESC) keyset pagination used by ListAll.
DROP INDEX IF EXISTS idx_newsletters_context;
DROP INDEX IF EXISTS idx_newsletters_list;
CREATE INDEX IF NOT EXISTS idx_newsletters_project ON newsletters (project_uid);
CREATE INDEX IF NOT EXISTS idx_newsletters_status  ON newsletters (status);
CREATE INDEX IF NOT EXISTS idx_newsletters_list
    ON newsletters (project_uid, updated_at DESC, id DESC);

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
CREATE TABLE IF NOT EXISTS newsletter_unsubscribes (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    project_uid TEXT        NOT NULL,
    email       TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_unsubscribes_project_email
    ON newsletter_unsubscribes (project_uid, email);
