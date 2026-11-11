-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

DROP INDEX IF EXISTS notifications_inbox_unread_idx;
DROP INDEX IF EXISTS notifications_inbox_idx;
ALTER TABLE notifications DROP COLUMN IF EXISTS read_at;
