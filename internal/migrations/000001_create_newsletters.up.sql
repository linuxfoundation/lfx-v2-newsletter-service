-- Copyright The Linux Foundation and each contributor to LFX.
-- SPDX-License-Identifier: MIT

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

CREATE TABLE newsletters (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    context_type    TEXT         NOT NULL CHECK (context_type IN ('foundation','project')),
    context_uid     TEXT         NOT NULL,
    subject         TEXT         NOT NULL,
    body_html       TEXT         NOT NULL,
    ed_reply_email  TEXT         NOT NULL,
    committee_uids  TEXT[]       NOT NULL DEFAULT '{}',
    status          TEXT         NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','sent')),
    sent_at         TIMESTAMPTZ,
    created_by      TEXT         NOT NULL,
    version         BIGINT       NOT NULL DEFAULT 1,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_newsletters_context ON newsletters (context_type, context_uid);
CREATE INDEX idx_newsletters_status  ON newsletters (status);
