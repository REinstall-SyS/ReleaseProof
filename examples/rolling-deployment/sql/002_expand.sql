-- Supported coexistence phase: both representations are kept consistent in one
-- row-level BEFORE trigger, within the writer's PostgreSQL transaction.
ALTER TABLE users ADD COLUMN first_name text;
ALTER TABLE users ADD COLUMN last_name text;
UPDATE users SET first_name = split_part(full_name, ' ', 1),
                 last_name = substring(full_name FROM position(' ' IN full_name) + 1);

CREATE FUNCTION releaseproof_sync_user_name() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    full_changed boolean;
    split_changed boolean;
BEGIN
    IF TG_OP = 'INSERT' THEN
        full_changed := NEW.full_name IS NOT NULL;
        split_changed := NEW.first_name IS NOT NULL OR NEW.last_name IS NOT NULL;
    ELSE
        full_changed := NEW.full_name IS DISTINCT FROM OLD.full_name;
        split_changed := NEW.first_name IS DISTINCT FROM OLD.first_name
                      OR NEW.last_name IS DISTINCT FROM OLD.last_name;
    END IF;

    IF full_changed AND NOT split_changed THEN
        NEW.first_name := split_part(NEW.full_name, ' ', 1);
        NEW.last_name := substring(NEW.full_name FROM position(' ' IN NEW.full_name) + 1);
    ELSIF split_changed AND NOT full_changed THEN
        NEW.full_name := NEW.first_name || ' ' || NEW.last_name;
    END IF;

    -- Canonical demo domain: first token plus a nonempty remainder, separated
    -- by exactly one ASCII space. This deliberately does not model human names.
    IF NEW.full_name IS NULL
       OR NEW.full_name !~ '^[^[:space:]]+ [^[:space:]]+( [^[:space:]]+)*$'
       OR NEW.first_name IS NULL OR NEW.last_name IS NULL
       OR position(' ' IN NEW.first_name) > 0
       OR NEW.first_name || ' ' || NEW.last_name IS DISTINCT FROM NEW.full_name THEN
        RAISE EXCEPTION 'inconsistent or noncanonical user name representations'
            USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER releaseproof_sync_user_name
BEFORE INSERT OR UPDATE ON users
FOR EACH ROW EXECUTE FUNCTION releaseproof_sync_user_name();

ALTER TABLE users ALTER COLUMN first_name SET NOT NULL;
ALTER TABLE users ALTER COLUMN last_name SET NOT NULL;
-- Validate the same invariant for the initial backfill, not only future writes.
ALTER TABLE users ADD CONSTRAINT consistent_user_name CHECK (
    full_name ~ '^[^[:space:]]+ [^[:space:]]+( [^[:space:]]+)*$'
    AND position(' ' IN first_name) = 0
    AND first_name || ' ' || last_name = full_name
);
