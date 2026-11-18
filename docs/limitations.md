# Scope and limitations

A passing ReleaseProof run means that the supplied application versions passed
the supplied assertions against the supplied PostgreSQL schema, migration, and
fixtures in the recorded environment. It does not prove that a migration prevents
all outages or that untested business behavior remains correct.

## Behavioral coverage

- The MVP adapter supports independently startable HTTP application containers.
  Workers, message consumers, RPC services, command-line programs, and applications
  needing additional infrastructure require a suitable HTTP test wrapper or future
  adapter work.
- Assertions support expected HTTP status codes and exact values at JSON pointers.
  Captures and substitutions provide small ordered workflows, not a general
  programming or query language. Authentication flows, custom protocols, and
  arbitrary application-specific setup are outside this deliberately small format.
- Cross-version shape validation requires a mutation followed by the other
  version's asserted read. It cannot establish that the fixture, request, or
  expected value represents the important business invariant. Developers supply
  that meaning.
- Mixed-version steps execute sequentially while both versions can be running.
  They do not simulate production load, concurrent requests, distributed races,
  stale caches, replication lag, or application-level lost updates.
- Every scenario starts from fresh deterministic data. Long-running history,
  production-scale data distributions, unusual row values, and data not represented
  by fixtures remain untested.
- The baseline gate establishes that the declared old-version baseline checks
  work. It cannot establish that all old application behavior worked before the
  migration. A failed baseline is an execution failure, not a migration regression.

## Database and migration coverage

- PostgreSQL is the supported database. SQL dialects, permissions, extensions,
  collations, server settings, or managed-service behavior absent from the test
  instance are not covered by a passing local run.
- SQL files run under their own normal psql autocommit semantics. A later error
  does not roll back an earlier committed statement. The tool reports the error
  and discards the isolated database. It is not a deployment migration manager.
- Server SQL such as `CREATE INDEX CONCURRENTLY` can run outside an imposed
  transaction. PostgreSQL still rejects operations that a submitted explicit
  transaction block forbids. psql metacommands and `COPY FROM STDIN` are rejected
  with a specific unsupported-input error rather than interpreted as shell or
  client commands.
- Catalog differences and conservative operation checks are intentionally limited.
  They can flag removed objects, changed types, newly required values, and
  potentially blocking operations. Catalog state alone cannot reliably distinguish
  every rename from a drop/add, explain arbitrary procedural/dynamic SQL, or prove
  that an absent warning means an operation is safe.
- Lock warnings indicate risk based on the operation. They are not measurements
  of production downtime. Fixture-scale execution time does not predict lock waits,
  table rewrite cost, backfill cost, replication impact, or behavior under real
  workloads. No production lock-duration guarantee is made.

## Environment and reproduction

- A local Linux Docker Engine is required. Docker Desktop must be in Linux
  container mode. Remote Docker TCP/SSH endpoints are unsupported because the
  runner relies on local loopback port mappings.
- Application startup and database availability depend on image access, available
  host resources, and declared readiness endpoints. A healthy endpoint must be a
  meaningful readiness signal for the application's test workflow.
- Dockerfiles and images are trusted executable inputs. Container restrictions
  limit accidental damage but do not safely isolate hostile code. Network egress,
  registry changes, external APIs, and daemon/kernel behavior remain outside the
  compatibility contract.
- Configuration, SQL, and local build input fingerprints detect changed inputs;
  they do not archive them. Reproduction requires the retained local files and run
  metadata. Mutable image tags, nondeterministic builds, external dependencies, or
  missing images can prevent an exact rerun. Preserve immutable image references
  and dependency locks when stronger repeatability is needed.
- Local build input fingerprinting rejects symlinks and conservatively includes
  regular source files beyond Docker's exact `.dockerignore` selection. Keep build
  contexts focused. Source/documentation changes inside a broad context can require
  a fresh experiment even when they do not affect the compiled application.
- Reports capture bounded HTTP response bodies and log excerpts. Large responses
  fail the configured check rather than being silently treated as fully inspected.
  Log truncation means earlier application diagnostics may not be present.
- Known credentials and recognized connection strings are redacted. Synthetic
  fixture data and a review before sharing reports are still required: arbitrary
  application output can contain sensitive content that a generic redactor cannot
  identify.
- Cleanup is best effort after ordinary handled interruptions. Hard kills, host
  crashes, or daemon failures can leave resources behind. Use the ownership-label
  recovery procedure in [security.md](security.md); do not run broad prune commands.

## Demo-specific assumptions

The rolling-deployment example uses a reversible, deliberately narrow name
representation: first token and a nonempty remaining string, separated by single
ASCII spaces. Its trigger synchronizes both representations atomically within each
row update and rejects conflicting inputs. This does not claim that all human
names can be split, that generic dual writing is sufficient, or that arbitrary
multirow/domain invariants are preserved. The destructive cleanup is permitted only
after every old reader, writer, job, and rollback path has retired.
