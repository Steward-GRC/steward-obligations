-- A row held because the render sidecar was down carries the render inputs
-- (the vars JSON) instead of a rendered body; the replay renders it first.
-- NULL for every row held after rendering.
ALTER TABLE mail_outbox ADD COLUMN render_vars JSONB;
