# Configuration reference

ReleaseProof reads one strict YAML document with `version: 1`. Unknown fields,
duplicate/invalid scenario identifiers, missing required matrix kinds, undefined
response variables, and invalid input paths produce configuration errors before
Docker work begins.

Start with `releaseproof init`, or copy the complete
[rolling-deployment configuration](../examples/rolling-deployment/safe.yaml).
`releaseproof validate --config PATH` checks configuration and computes its input
fingerprint without requiring Docker.

## Top-level fields

| Field | Required/default | Meaning |
| --- | --- | --- |
| `version` | Required, `1` | Configuration format version |
| `postgres.image` | `postgres:17.12-alpine` | PostgreSQL container image; pin an immutable digest for stricter input control |
| `baseline` | Required | One baseline SQL file |
| `seed` | Optional | One deterministic SQL fixture file |
| `migrations` | Required, nonempty list | SQL files applied in order after the baseline and seed |
| `apps` | Required | Exactly the `old` and `new` application definitions |
| `scenarios` | Required | Ordered scenario definitions covering all five required kinds |
| `timeout` | `60s` | Deadline for each scenario, including SQL setup, readiness, and requests; 1s–30m |
| `request_timeout` | `5s` | Timeout of an individual HTTP request; 1ms–30m |
| `startup_timeout` | `30s` | Readiness timeout for PostgreSQL and the default application readiness timeout; 1ms–30m |
| `report` | `.releaseproof` | Report root directory relative to the configuration |

Paths in `baseline`, `seed`, `migrations`, `apps.*.build`, and `report` resolve
relative to the configuration file. Absolute paths also work. The CLI's explicit
`--output` argument resolves relative to the current working directory and overrides
`report`. A check creates a unique run subdirectory under that root.

Timeouts are Go duration strings such as `250ms`, `30s`, or `2m`. A request or
readiness deadline cannot extend the scenario's overall deadline. Image
preparation has a separate bounded setup phase; `timeout` is not the entire
invocation's wall-clock budget.

Example setup fields, before the required `scenarios` list:

```yaml
version: 1
postgres:
  image: postgres:17.12-alpine
baseline: sql/baseline.sql
seed: sql/seed.sql
migrations:
  - sql/002_expand.sql
timeout: 90s
request_timeout: 5s
startup_timeout: 30s
report: .releaseproof
apps:
  old:
    version: v1.4.2
    image: my-team/users:v1.4.2
    port: 8080
    health: /health
    env:
      LOG_LEVEL: info
  new:
    version: v1.5.0
    build: .
    dockerfile: Dockerfile
    port: 8080
    health: /health
    startup_timeout: 45s
```

This is a setup excerpt, not a complete configuration. The complete demo provides
all required scenarios and local build inputs.

## Application definitions

| Field | Required/default | Meaning |
| --- | --- | --- |
| `version` | Required | Human-readable version shown in evidence |
| `image` | Exactly one of `image` / `build` | Existing local or pullable image reference |
| `build` | Exactly one of `image` / `build` | Docker build context directory |
| `dockerfile` | Docker default | Path relative to the build context |
| `port` | Required, 1–65535 | Internal HTTP port |
| `health` | `/health` | Local HTTP path; readiness requires HTTP 200 |
| `env` | Optional string map | Additional environment entries |
| `command` | Optional string list | Arguments supplied after the image, using Docker's normal ENTRYPOINT/CMD behavior |
| `startup_timeout` | Top-level `startup_timeout` | Per-application readiness deadline |

Use `command: ["./server", "--mode", "test"]` when the image's entrypoint policy
requires those arguments. ReleaseProof does not expand shell syntax, `$VARIABLE`,
or `$(commands)` in this array. If an image has an ENTRYPOINT, the array becomes
its command arguments under normal Docker semantics; it does not replace that
entrypoint. Review the image rather than assuming it invokes a shell.

ReleaseProof supplies `DATABASE_URL` and `PORT`; configuring either in `env` is an
error. Applications receive a PostgreSQL URL for their isolated scenario database.
They use a restricted role with normal table and sequence privileges in `public`.
Database-owner or superuser access is not available to application containers.

Environment values are literal strings; host environment substitution is not an
MVP feature. Keep production secrets out of configuration and use synthetic test
credentials. Values under recognized sensitive keys are redacted from reports,
but arbitrary application output may still contain sensitive fixture data.

Applications must be independently startable and expose HTTP on the configured
port. A health endpoint should verify the dependencies needed for the scenario.
Health probes retry until ready, exited, cancelled, or timed out. Test requests do
not retry. There is no dependency-container declaration beyond PostgreSQL in this
MVP.

## Scenario definitions

Each scenario has `id`, `name`, `kind`, and a nonempty `steps` list. Identifiers
must match `[a-z][a-z0-9_-]{0,62}` and be unique in the configuration.

| Kind | Required | Application and operation constraints |
| --- | --- | --- |
| `baseline` | Yes | Old-only steps against baseline and seed |
| `old_after` | Yes | Old-only steps against migrated state |
| `new_after` | Yes | New-only steps against migrated state |
| `old_to_new` | Yes | Old mutation, then new GET with at least one JSON assertion |
| `new_to_old` | Yes | New mutation, then old GET with at least one JSON assertion |
| `mixed` | Optional | Any additional ordered old/new sequence against migrated state |

You can declare multiple scenarios of the same kind. All baseline scenarios run
first, preserving their relative order; migrated scenarios then preserve their
relative order. A baseline failure prevents migration scenarios from running.
Every scenario receives a new database and new variable namespace, so one
scenario cannot rely on another's writes or captures.

## HTTP steps and assertions

| Field | Required | Meaning |
| --- | --- | --- |
| `name` | Yes | Human-readable operation name |
| `app` | Yes | `old` or `new` |
| `method` | Yes | GET, POST, PUT, PATCH, DELETE, or HEAD |
| `path` | Yes | Absolute local HTTP path, optionally with a query string |
| `body` | Optional | JSON-compatible YAML data sent as JSON |
| `expect.status` | Yes | Exact HTTP status, 100–599 |
| `expect.json` | Optional | Map of JSON pointers to exact expected values |
| `capture` | Optional | Map of variable names to response JSON pointers |

ReleaseProof adds `Accept: application/json` and, when a body is present,
`Content-Type: application/json`. Custom headers and authentication adapters are
outside the current format. Paths must begin with `/` and cannot redirect the
request to a different host. HTTP redirects are returned as responses rather than
followed. A response body is limited to 1 MiB; an oversized response fails the
step and is not treated as fully verified.

Example cross-version scenario:

```yaml
- id: old-to-new
  name: Old writes are understood by the new application
  kind: old_to_new
  steps:
    - name: Old application creates a user
      app: old
      method: POST
      path: /users
      body:
        full_name: Alan Turing
      expect:
        status: 201
      capture:
        created_id: /id
    - name: New application reads the same user
      app: new
      method: GET
      path: /users/{{created_id}}
      expect:
        status: 200
        json:
          /first_name: Alan
          /last_name: Turing
```

JSON assertions compare values, including their JSON types. Number values loaded
from YAML and JSON are normalized so an expected `7` matches a returned JSON `7`.
String `"7"` remains different. Unmentioned object fields are not asserted; an
assertion at a pointer targeting an entire object compares that complete object.
Missing fields differ from JSON `null`.

Pointers follow RFC 6901:

| Pointer | Selection |
| --- | --- |
| `""` | Entire response document |
| `/user/name` | Nested object field |
| `/items/0/id` | First array element's identifier |
| `/a~1b` | Object key `a/b` |
| `/a~0b` | Object key `a~b` |

Quote an empty pointer key in YAML. Array indexes are zero-based canonical decimal
integers; missing fields and out-of-range indexes fail clearly. There are no
wildcards, filters, regular expressions, or implicit transformations.

## Response variables

Capture names use the same lowercase identifier form as scenario IDs and cannot
be reassigned within one scenario. Captures become available only after all
assertions in the capturing step pass. References must point to a capture in an
earlier step; undefined or malformed expressions are configuration errors.

- `path: /users/{{created_id}}` substitutes a scalar value with URL path escaping.
- A whole body value such as `user_id: "{{created_id}}"` preserves the captured
  JSON type, including a number, object, array, boolean, or null.
- An embedded body string such as `label: "user-{{created_id}}"` requires a scalar
  capture and becomes text.
- Expected JSON values can reference previous captures too.

Variable expressions are deliberately limited to `{{name}}`; they do not execute
functions, arithmetic, shell commands, or SQL.

## SQL and schema assumptions

Baseline, seed, and migration files are server SQL. psql client commands and
`COPY FROM STDIN` are unsupported. Prefer `INSERT` fixture statements. SQL is
executed by PostgreSQL, so dollar-quoted functions and supported nontransactional
DDL do not require ReleaseProof to parse statement boundaries.

Each migration file runs separately without a tool-imposed encompassing
transaction. `CREATE INDEX CONCURRENTLY` can therefore execute; a file that
explicitly wraps it in `BEGIN` still gets PostgreSQL's real error. Earlier
statements may commit before a later failure. All execution is isolated and the
scenario database is discarded afterward.

The MVP's application grants and catalog analysis target the `public` schema.
Applications requiring extensions, custom schema grants, unusual server settings,
superuser rights, or external services need explicit future adapter/setup work;
passing the basic fixture does not verify those environments.
