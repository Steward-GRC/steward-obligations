-- A person's own quiet hours: the daily window, in the local hours of an IANA
-- timezone, in which email and push are held. No row means the default
-- 22:00 to 07:00 applies. The window may cross midnight (start > end); an empty
-- one (start = end) is refused.
CREATE TABLE notification_quiet_hours (
    user_id    TEXT        NOT NULL PRIMARY KEY,
    start_hour SMALLINT    NOT NULL CHECK (start_hour BETWEEN 0 AND 23),
    end_hour   SMALLINT    NOT NULL CHECK (end_hour BETWEEN 0 AND 23),
    timezone   TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (start_hour <> end_hour)
);
