-- Deliberately broken: successful updates silently leave the other
-- representation stale. This is the semantic regression demonstration.
ALTER TABLE users ADD COLUMN first_name text;
ALTER TABLE users ADD COLUMN last_name text;
UPDATE users SET first_name = split_part(full_name, ' ', 1),
                 last_name = substring(full_name FROM position(' ' IN full_name) + 1);
-- There is intentionally no synchronization trigger.
