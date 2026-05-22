-- Copyright The Linux Foundation and each contributor to LFX.
-- SPDX-License-Identifier: MIT

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE IF NOT EXISTS newsletters (
    id                UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    context_type      TEXT         NOT NULL CHECK (context_type IN ('foundation','project')),
    context_uid       TEXT         NOT NULL,
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

CREATE INDEX IF NOT EXISTS idx_newsletters_context ON newsletters (context_type, context_uid);
CREATE INDEX IF NOT EXISTS idx_newsletters_status  ON newsletters (status);

-- newsletter_opens captures one row per open event. recipient_hash is a SHA-256
-- of the lowercased recipient email so we can compute unique opens without
-- persisting PII in this table beyond what the newsletters table already holds.
CREATE TABLE IF NOT EXISTS newsletter_opens (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    newsletter_id   UUID         NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    recipient_hash  TEXT         NOT NULL,
    opened_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_opens_newsletter            ON newsletter_opens (newsletter_id);
CREATE INDEX IF NOT EXISTS idx_opens_newsletter_recipient  ON newsletter_opens (newsletter_id, recipient_hash);
CREATE INDEX IF NOT EXISTS idx_opens_opened_at             ON newsletter_opens (newsletter_id, opened_at);
