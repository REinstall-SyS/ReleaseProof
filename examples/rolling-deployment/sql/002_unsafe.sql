-- Executable, valid SQL; incompatible with every running old application.
ALTER TABLE users ADD COLUMN first_name text;
ALTER TABLE users ADD COLUMN last_name text;
UPDATE users SET first_name = split_part(full_name, ' ', 1),
                 last_name = substring(full_name FROM position(' ' IN full_name) + 1);
ALTER TABLE users ALTER COLUMN first_name SET NOT NULL;
ALTER TABLE users ALTER COLUMN last_name SET NOT NULL;
ALTER TABLE users DROP COLUMN full_name;
