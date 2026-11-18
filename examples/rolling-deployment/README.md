# Rolling deployment demonstration

From the repository root, with Go 1.27 and a local Linux Docker Engine:

```sh
go build -o bin/releaseproof ./cmd/releaseproof
bin/releaseproof check --config examples/rolling-deployment/unsafe.yaml
# Expected compatibility failure: SQL succeeds, old HTTP operations fail.
bin/releaseproof check --config examples/rolling-deployment/safe.yaml
# Expected success: the full coexistence matrix passes.
bin/releaseproof check --config examples/rolling-deployment/semantic.yaml
# Expected compatibility failure: HTTP 200, but cross-version values are stale.
```

On Windows use `bin/releaseproof.exe` and Linux containers in Docker Desktop.
Each scenario receives the same two fixture rows in a fresh database. Captured
identifiers are local to the scenario. The applications execute real PostgreSQL
queries through pgx; ReleaseProof does not simulate their responses.

The two scratch container images run the same small HTTP program with separate,
fixed `APP_VERSION` values. The old version's SQL knows only `full_name`. The new
version's SQL knows only `first_name` and `last_name`. Both health checks verify a
database connection, allowing the actual user operation to expose a removed-column
error even if process startup succeeds. Demo errors intentionally include the real
PostgreSQL error in HTTP responses; public production APIs should use a different
error disclosure policy.

## Why the unsafe migration breaks a rollout

`sql/002_unsafe.sql` backfills split fields and drops `full_name`. It is valid SQL.
The new application can read the backfill. Every old application instance still
selects `full_name`, so its requests fail until those instances are retired. Old
inserts and reads following new inserts fail too.

## Why the safe expansion supports this contract

`sql/002_expand.sql` retains all three fields and installs a PostgreSQL `BEFORE
INSERT OR UPDATE` trigger. It determines which representation changed, updates the
other representation within the same row write and transaction, and rejects
conflicting writes that explicitly change both representations inconsistently.
Both insert directions and repeated update directions are tested. PostgreSQL row
locking serializes competing updates to a row; the demo does not prove correctness
for arbitrary multirow invariants or application-level lost-update behavior.

The deliberately narrow name domain is **one first token plus a nonempty remainder
separated by single ASCII spaces**. For example, `Mary Ann Smith` maps to first name
`Mary`, last name `Ann Smith`. Tabs, leading/trailing/repeated spaces, a one-token
name, empty fields, and conflicting representations are rejected. This is a
reversible fixture representation, not a model of real human names. Changing that
business rule requires different fixtures, synchronization, and assertions.

An invariant check constraint also verifies the initial backfill and every later
write. Application users do not need superuser, schema ownership, or privileges to
create the synchronization function: migrations run through ReleaseProof's
separate ephemeral migration connection.

## The final contract step requires old-version retirement

1. Apply the expansion while the old version is still in service.
2. Run `safe.yaml`, deploy the new application, and keep synchronization active
   while both versions, jobs, workers, and rollback paths can write.
3. Retire every old application, worker, scheduled job, and rollback target that
   references `full_name`.
4. In a later deployment, apply `sql/003_contract_after_old_retirement.sql` and
   update the compatibility contract to the versions still supported.

The cleanup script is intentionally excluded from `safe.yaml`. Adding it while the
old version remains in the matrix should fail, because that rollout is unsafe.

## Semantic incompatibility without an HTTP error

`semantic.yaml` uses `002_dual_write_only.sql`, which backfills both representations
but installs no synchronization mechanism. Both applications successfully update
their own fields. Cross-version reads return HTTP 200 with stale values. Expected
JSON fields catch the divergence. This is why a successful migration and healthy
HTTP status codes do not establish compatibility.

## Dependency and resource notes

Application dependencies are pinned by the root `go.mod` and `go.sum`. The build
stage pins Go to `golang:1.27.4`; PostgreSQL is `postgres:17.12-alpine`. These are
version tags, not immutable registry digests; reports record resolved image
identities when available. The final application image contains only the static Go
binary and runs as UID/GID 65532. There are no host mounts or external services.
The fixture is two rows, so this demonstration does not measure production-scale
backfill performance or lock duration.
