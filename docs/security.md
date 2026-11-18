# Security model

Run ReleaseProof only with configurations, SQL, application images, and Docker
build contexts that you trust. It starts arbitrary application processes and runs
developer-supplied migrations. Docker access is a powerful host capability;
container isolation and ReleaseProof's resource limits do not make hostile code
safe to execute.

## Database boundaries

ReleaseProof creates its own ephemeral PostgreSQL instance. It does not accept a
production database connection string as a target. `DATABASE_URL` and `PORT` are
managed variables rather than application configuration overrides.

Each scenario gets fresh database state and separate generated logins. Applications
use a restricted database role with ordinary table and sequence access. A separate
migration owner manages schema changes. The PostgreSQL bootstrap administrator is
used for database/role setup and is not passed to application containers. These
boundaries prevent accidental cross-scenario state reuse; they are not a security
boundary between mutually hostile applications on a shared Docker daemon.

Migration SQL is interpreted by PostgreSQL through `psql -X` with
`ON_ERROR_STOP=1`. Unsupported psql metacommands and `COPY FROM STDIN` are rejected.
Migration SQL is trusted code even with restricted database ownership: it can
consume resources, manipulate its objects, or exercise available database features.
Statement and lock timeouts bound operations but cannot predict production cost.

## Process and container boundaries

The runner uses `exec.CommandContext` and explicit argument arrays. It does not
interpolate configuration into a host shell command. SQL is sent on standard input.
Environment values are supplied as process environment entries rather than shell
expressions. An application's configured command is still executable code inside
its container; review the image and configuration before running it.

Containers connect to a unique Docker network for the run. Published ports bind to
`127.0.0.1` and use random host ports. Application containers drop Linux capabilities,
enable `no-new-privileges`, and have PID, memory, and CPU limits. ReleaseProof does
not mount the host Docker socket or application-controlled host directories into
the running application containers. The demo images run as UID/GID 65532; review
the `USER` policy in your own images.

These settings do not provide a complete sandbox. Container processes share the
host kernel through the Docker runtime. Normal network egress may reach external
services, containers on the run's network can communicate, and a Dockerfile build
can execute arbitrary build instructions. Build contexts can also contain files
you did not intend to send to the daemon. Maintain a restrictive `.dockerignore`
and keep credentials outside the context. CI should use ephemeral runners and
avoid exposing repository secrets to untrusted pull-request code.

Only local Linux container daemons using `unix://` or `npipe://` endpoints are
supported. A Docker context name or environment variable is not permission to run
against a production daemon; select a disposable local development daemon.

## Credentials and reports

Generated database passwords are unique to temporary resources. Reports redact
known credentials, configured environment values, and recognized database URLs
before rendering. The same sanitized report feeds terminal, JSON, and HTML output.

Redaction is defense in depth, not a guarantee that arbitrary application output
is free of sensitive information. An application can encode or transform a secret,
and response bodies or fixture values may contain personal or confidential data.
Use synthetic fixtures and test credentials. Review reports before sharing them.
The default `.releaseproof/` artifact directory is ignored by Git; keep generated
reports and connection metadata out of commits unless deliberately reviewed.

Docker daemon administrators can inspect container environment variables. Reports
being sanitized does not hide the running container's credentials from a user who
already controls that daemon. Do not supply production secrets to this test tool.

## Cleanup and manual recovery

ReleaseProof tracks resource ownership with a random identifier and the Docker
label `io.releaseproof.run`. Normal success, failure, and handled interruption
attempt bounded cleanup. Removing resources checks ownership so a run does not
delete another invocation's containers. The runner does not use broad Docker prune
commands.

An uncatchable process kill, daemon failure, power loss, or host crash can prevent
deferred cleanup. To inspect resources left by a specific run, replace `RUN_ID`
with that run's identifier:

```sh
docker ps --all --filter label=io.releaseproof.run=RUN_ID
docker network ls --filter label=io.releaseproof.run=RUN_ID
docker image ls --filter label=io.releaseproof.run=RUN_ID
```

Inspect each candidate before deleting it. Verify its label, run identifier, and
name, and confirm that the run is no longer active:

```sh
docker inspect EXACT_CONTAINER_NAME
docker network inspect EXACT_NETWORK_NAME
docker image inspect EXACT_BUILT_IMAGE_TAG
```

Then remove only the verified, explicitly named resources, containers first:

```sh
docker rm --force --volumes EXACT_CONTAINER_NAME
docker network rm EXACT_NETWORK_NAME
docker image rm EXACT_BUILT_IMAGE_TAG
```

Repeat the container command separately for the run's verified container names.
Do not delete resources belonging to active runs or unrelated projects. Preexisting
base images and shared build cache are not owned temporary application images.

## Reporting a vulnerability

Avoid publishing credentials or private application logs in a public issue. Report
security-sensitive findings through GitHub's private vulnerability reporting
feature if available for this repository, or contact the repository owner through
an existing private channel. Include a minimal synthetic reproduction and the
affected ReleaseProof commit.
