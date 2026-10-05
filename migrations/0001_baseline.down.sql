-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

DROP TABLE IF EXISTS notification_outbox;
DROP TABLE IF EXISTS notification_sent;
DROP TABLE IF EXISTS notification_digest_windows;
DROP TABLE IF EXISTS notification_type_overrides;
DROP TABLE IF EXISTS notification_channel_prefs;
DROP TABLE IF EXISTS notification_category_prefs;
DROP TABLE IF EXISTS policy_retired_notified;
DROP TABLE IF EXISTS approval_notified;
DROP TABLE IF EXISTS welcome_email_sent;
DROP TABLE IF EXISTS mail_outbox;
DROP TABLE IF EXISTS user_version_notified;
DROP TABLE IF EXISTS user_notify_first_seen;
DROP TABLE IF EXISTS notifications;
DROP TYPE  IF EXISTS notification_status;
DROP TYPE  IF EXISTS notification_channel;
DROP TABLE IF EXISTS policy_views;
DROP TABLE IF EXISTS acknowledgments;
