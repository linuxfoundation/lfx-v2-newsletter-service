-- Copyright The Linux Foundation and each contributor to LFX.
-- SPDX-License-Identifier: MIT

-- Snapshot the audience size at send time so analytics queries don't have to
-- re-resolve recipients against the committee service.
ALTER TABLE newsletters
    ADD COLUMN total_recipients INT NOT NULL DEFAULT 0;

-- newsletter_opens captures one row per open event. recipient_hash is a SHA-256
-- of the lowercased recipient email so we can compute unique opens without
-- persisting PII in this table beyond what the newsletters table already holds.
CREATE TABLE newsletter_opens (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    newsletter_id   UUID         NOT NULL REFERENCES newsletters(id) ON DELETE CASCADE,
    recipient_hash  TEXT         NOT NULL,
    opened_at       TIMESTAMPTZ  NOT NULL DEFAULT now()
);

CREATE INDEX idx_opens_newsletter ON newsletter_opens (newsletter_id);
CREATE INDEX idx_opens_newsletter_recipient ON newsletter_opens (newsletter_id, recipient_hash);
CREATE INDEX idx_opens_opened_at ON newsletter_opens (newsletter_id, opened_at);
