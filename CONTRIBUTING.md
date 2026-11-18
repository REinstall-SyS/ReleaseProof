# Contributing to ReleaseProof

ReleaseProof is a small Go developer tool for executable PostgreSQL rollout
compatibility checks. Contributions should strengthen actual orchestration,
assertion coverage, diagnostics, reproducibility, or documentation. Keep the CLI
and the evidence contract understandable.

## Development setup

Use Go 1.27.4 and a local Docker Engine running Linux containers. The demo pins
PostgreSQL to `postgres:17.12-alpine`. First-time execution needs network access to
download Go modules and container images. Use a disposable development Docker
daemon, synthetic fixtures, and test credentials.

```sh
go mod download
go build -o bin/releaseproof ./cmd/releaseproof
go test ./...
go vet ./...
```

Formatting uses standard `gofmt`. The Makefile collects the common development,
integration, end-to-end, and benchmark commands.

## Validate a change

Run tests appropriate to the changed behavior, then the required checks for a pull
request:

```sh
go test ./...
go vet ./...
go test -tags integration -count=1 -timeout 25m ./internal/runner
make e2e
```

Integration tests intended to verify PostgreSQL migration behavior must use real
PostgreSQL containers. Do not replace them with a mocked database. Unit tests can
use `httptest` for HTTP assertions, and pure functions for config, planning,
redaction, reports, and verdict decisions.

The unsafe demo must be an observed compatibility failure after a passing
baseline; the safe coexistence demo must pass. The semantic demo must show a value
mismatch even though HTTP succeeds. An infrastructure failure is not an acceptable
substitute for an expected compatibility failure.

When changing reporting or reproduction, verify terminal/JSON/HTML agreement,
escaped HTML, redaction, changed-input detection, and selected-scenario baseline
execution. When changing orchestration, verify failure cleanup, ordinary
interruption, unique resource ownership, and concurrent independent invocations.

## Design rules

- Keep user-authored business assertions separate from tool orchestration. A
  migration linter cannot establish arbitrary application semantics.
- Preserve the baseline gate and the exit-code contract: 0 compatible, 1 observed
  incompatibility, 2 configuration/usage, 3 execution or baseline failure.
- Use contexts, explicit deadlines, error propagation, and owned-resource cleanup.
  Do not interpolate configuration into a host shell or mount the Docker socket
  into application containers.
- Keep application database permissions restricted. Do not add a production
  database target or pass bootstrap superuser credentials to tested applications.
- Distinguish executed failures, catalog observations, static warnings,
  unsupported operations, and untested risks. Do not invent diagnostics, timings,
  or production downtime guarantees.
- Use maintained dependencies for real needs and pin them in `go.mod`/`go.sum`.
  Keep the assertion format small; it is not a general programming language.
- Preserve existing files during initialization and existing repository history
  during maintenance.

## Pull requests and issues

Describe the concrete problem, the resulting behavior, and the validation you
actually ran. Include a minimal deterministic fixture for a new migration or
compatibility bug. Explain any environment blocker rather than marking an
unexecuted check as passed. Update configuration/security/limitations documentation
when behavior changes.

For benchmarks, record machine specifications, Docker and PostgreSQL versions,
fixture size, cache state, run count, and methodology. Report measured values;
do not compare unlike environments as though they were controlled experiments.

Keep `.tools`, binaries, `.releaseproof` reports, credentials, production fixtures,
and large generated artifacts out of commits. Review any selected report excerpt
before adding it as public documentation. See the [security model](docs/security.md)
for security-sensitive reporting and cleanup guidance.

Contributions are provided under the repository's [MIT license](LICENSE).
