-- Copyright The Linux Foundation and each contributor to LFX.
-- SPDX-License-Identifier: MIT

DROP TABLE IF EXISTS newsletter_opens;

ALTER TABLE newsletters
    DROP COLUMN IF EXISTS total_recipients;
