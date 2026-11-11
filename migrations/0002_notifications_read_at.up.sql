-- Copyright 2026 The Steward Authors
-- SPDX-License-Identifier: Apache-2.0

-- When the person read the notice in their in-app inbox; NULL while unread.
ALTER TABLE notifications ADD COLUMN read_at TIMESTAMPTZ;

-- The inbox lists a person's in-app notices newest first.
CREATE INDEX notifications_inbox_idx ON notifications (user_id, created_at DESC, id DESC)
    WHERE channel = 'in_app';

-- The bell's badge counts the unread ones.
CREATE INDEX notifications_inbox_unread_idx ON notifications (user_id)
    WHERE channel = 'in_app' AND read_at IS NULL;
