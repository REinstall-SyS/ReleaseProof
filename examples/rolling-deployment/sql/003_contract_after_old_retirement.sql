-- Apply only after all old applications, workers, jobs, and rollback paths that
-- reference full_name are retired. It must NOT be added to safe.yaml while the
-- old version remains in the compatibility contract.
DROP TRIGGER releaseproof_sync_user_name ON users;
DROP FUNCTION releaseproof_sync_user_name();
ALTER TABLE users DROP CONSTRAINT consistent_user_name;
ALTER TABLE users DROP COLUMN full_name;
