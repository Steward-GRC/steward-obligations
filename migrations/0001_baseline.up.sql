-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- One row per (user, policy version): an acknowledgement is an attestation and
-- is recorded once.
CREATE TABLE acknowledgments (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID        NOT NULL,
    policy_version_id UUID        NOT NULL,
    acked_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (user_id, policy_version_id)
);
CREATE INDEX ON acknowledgments (policy_version_id);

-- Append-only: a user may open a version many times; reports count distinct
-- users.
CREATE TABLE policy_views (
    id                UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           UUID        NOT NULL,
    policy_version_id UUID        NOT NULL,
    viewed_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX policy_views_version_idx ON policy_views (policy_version_id);
CREATE INDEX policy_views_version_user_idx ON policy_views (policy_version_id, user_id);

CREATE TYPE notification_channel AS ENUM ('email', 'in_app', 'push');
CREATE TYPE notification_status  AS ENUM ('pending', 'sent', 'failed');

-- The in-app inbox.
CREATE TABLE notifications (
    id         UUID                 PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID                 NOT NULL,
    type       TEXT                 NOT NULL,
    channel    notification_channel NOT NULL,
    status     notification_status  NOT NULL DEFAULT 'pending',
    payload    JSONB                NOT NULL DEFAULT '{}',
    sent_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ          NOT NULL DEFAULT now()
);
CREATE INDEX ON notifications (user_id, status);

-- The first time the ack-reminder path saw a user as obligated, for the
-- new-user grace period.
CREATE TABLE user_notify_first_seen (
    user_id       UUID        PRIMARY KEY,
    first_seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The first ack demand per (user, version), which tells an ack-required email
-- from a reminder, and the reminder back-off and escalate-once state.
CREATE TABLE user_version_notified (
    user_id           UUID        NOT NULL,
    policy_version_id UUID        NOT NULL,
    first_notified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    reminder_count    INT         NOT NULL DEFAULT 0,
    last_reminded_at  TIMESTAMPTZ,
    escalated_at      TIMESTAMPTZ,
    PRIMARY KEY (user_id, policy_version_id)
);

-- Rendered messages held while no mail transport works. Rendered bodies, not
-- render inputs, so a replay sends exactly what would have gone out.
CREATE TABLE mail_outbox (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            TEXT        NOT NULL,
    recipient       TEXT        NOT NULL,
    from_addr       TEXT        NOT NULL DEFAULT '',
    subject         TEXT        NOT NULL DEFAULT '',
    html            TEXT        NOT NULL DEFAULT '',
    text_body       TEXT        NOT NULL DEFAULT '',
    -- TEXT, not UUID: site-wide notices hold rows with no subject user.
    user_id         TEXT        NOT NULL DEFAULT '',
    dedup_key       TEXT,
    status          TEXT        NOT NULL DEFAULT 'pending',
    attempts        INT         NOT NULL DEFAULT 0,
    last_error      TEXT        NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at         TIMESTAMPTZ,
    CONSTRAINT mail_outbox_status_chk CHECK (status IN ('pending', 'sent', 'failed'))
);
CREATE INDEX mail_outbox_drain_idx ON mail_outbox (status, next_attempt_at, created_at);
-- A failed row is left out so the same business key can be held again later.
CREATE UNIQUE INDEX mail_outbox_dedup_idx ON mail_outbox (dedup_key)
    WHERE dedup_key IS NOT NULL AND status <> 'failed';

-- Once per account, shared by the welcome and the SSO welcome.
CREATE TABLE welcome_email_sent (
    user_id UUID        PRIMARY KEY,
    sent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- task_id is "<policy_version_id>:<stage_index>", or "<run_id>:<kind>" for
-- the submitter notices.
CREATE TABLE approval_notified (
    task_id           TEXT        NOT NULL,
    approver_user_id  UUID        NOT NULL,
    first_notified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (task_id, approver_user_id)
);

CREATE TABLE policy_retired_notified (
    policy_id   TEXT        NOT NULL,
    user_id     TEXT        NOT NULL,
    notified_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (policy_id, user_id)
);

-- cadence is immediate, daily, weekly or off; off is refused for the
-- mandatory categories at the write.
CREATE TABLE notification_category_prefs (
    user_id  TEXT NOT NULL,
    category TEXT NOT NULL,
    cadence  TEXT NOT NULL,
    PRIMARY KEY (user_id, category)
);

CREATE TABLE notification_channel_prefs (
    user_id TEXT    NOT NULL PRIMARY KEY,
    email   BOOLEAN NOT NULL DEFAULT TRUE,
    in_app  BOOLEAN NOT NULL DEFAULT TRUE,
    push    BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE notification_type_overrides (
    user_id TEXT NOT NULL,
    kind    TEXT NOT NULL,
    cadence TEXT NOT NULL,
    PRIMARY KEY (user_id, kind)
);

-- daily_hour is a local hour 0 to 23; weekly_dow is the ISO weekday, 1 = Monday.
CREATE TABLE notification_digest_windows (
    user_id    TEXT NOT NULL PRIMARY KEY,
    daily_hour INT  NOT NULL DEFAULT 8,
    weekly_dow INT  NOT NULL DEFAULT 1
);

-- Cross-replica email idempotency. Windowed keys expire by sent_at; date-keyed
-- once-guards carry the date in the key.
CREATE TABLE notification_sent (
    dedup_key TEXT        PRIMARY KEY,
    sent_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX notification_sent_sent_at_idx ON notification_sent (sent_at);

-- Digest-bound notifications, drained per user_id:category:window into one
-- digest email at the user's window.
CREATE TABLE notification_outbox (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     TEXT        NOT NULL,
    kind        TEXT        NOT NULL,
    category    TEXT        NOT NULL,
    severity    TEXT        NOT NULL DEFAULT 'normal',
    dedup_ref   TEXT        NOT NULL DEFAULT '',
    vars        JSONB       NOT NULL,
    window_kind TEXT        NOT NULL DEFAULT 'daily',
    digest_key  TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at     TIMESTAMPTZ
);
CREATE INDEX notification_outbox_pending_idx ON notification_outbox (user_id, sent_at)
    WHERE sent_at IS NULL;
CREATE UNIQUE INDEX notification_outbox_dedup_idx
    ON notification_outbox (user_id, kind, dedup_ref)
    WHERE sent_at IS NULL AND dedup_ref <> '';
