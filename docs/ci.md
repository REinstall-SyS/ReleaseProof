# GitHub Actions integration

ReleaseProof is a CLI deployment gate. A Linux runner needs Go and a local Docker
daemon, permission to build/pull the configured images, and the repository's
baseline, fixtures, migrations, and application checks. The tool provisions its own
PostgreSQL container; do not point a workflow at a production database.

## A compatibility gate in an application repository

This example installs a reviewed ReleaseProof commit and executes the application's
own `releaseproof.yaml`. Replace the installation commit before enabling it. Keep
the reviewed version pinned rather than implicitly accepting future CLI changes.

```yaml
name: Database compatibility
on:
  pull_request:
  push:
    branches: [main]
permissions:
  contents: read
jobs:
  compatibility:
    runs-on: ubuntu-latest
    timeout-minutes: 25
    steps:
      - uses: actions/checkout@v7
        with:
          persist-credentials: false
      - uses: actions/setup-go@v7
        with:
          go-version: '1.27.4'
      - name: Verify Docker
        run: docker info
      - name: Install reviewed ReleaseProof version
        run: go install github.com/REinstall-SyS/ReleaseProof/cmd/releaseproof@<reviewed-commit>
      - name: Check database compatibility
        run: releaseproof check --config releaseproof.yaml --output .releaseproof
      - name: Retain executed evidence
        if: always()
        uses: actions/upload-artifact@v6
        with:
          name: releaseproof-evidence
          path: .releaseproof/
          include-hidden-files: true
          if-no-files-found: warn
          retention-days: 7
```

The action major versions follow the current
[checkout](https://github.com/actions/checkout),
[setup-go](https://github.com/actions/setup-go), and
[upload-artifact](https://github.com/actions/upload-artifact) documentation. For a
stricter supply-chain policy, pin each action to a reviewed full commit SHA and
maintain those pins.

The configuration must identify a real old application version and the proposed
new version. Do not accidentally build the same source into both definitions and
assume the resulting matrix represents a rollout. Use retained release images or
explicit build contexts appropriate to the application's release process.

Do not use `continue-on-error` on the actual gate. Exit 1 means the declared
coexistence contract failed. Exit 2 means configuration/usage is invalid. Exit 3
means the baseline or execution could not establish a compatibility result,
including invalid migration SQL. All must block a gate until understood.

## Testing ReleaseProof itself

The repository workflow builds the CLI, checks Go formatting and static analysis,
runs unit tests, exercises real PostgreSQL integration tests, and runs the
mandatory demonstrations. The primary commands are:

```sh
go test ./...
go vet ./...
go test -tags integration -count=1 -timeout 25m ./internal/runner
make e2e
```

The unsafe and semantic demonstrations are expected compatibility failures. An
end-to-end harness must assert those nonzero exit codes; it must also assert that
the safe demonstration exits 0. Treating every nonzero result as success would
conceal missing Docker, invalid SQL, or startup errors. Use the repository's
`make e2e` target for this distinction and report/reproduction verification.

Benchmark execution is separate from compatibility assertions:

```sh
python scripts/benchmark.py --runs 3
```

The benchmark records the actual machine and Docker environment. Comparing numbers
across different hosted runners or cache states requires care; CI measurements are
not fixed product performance guarantees.

## CI trust and artifacts

Application images and Dockerfiles are executable code with normal Docker network
access. Use disposable runners and trusted inputs. A fork's migration or build
context should not receive production credentials or privileged self-hosted runner
access. Avoid `pull_request_target` workflows that execute unreviewed fork code in
the base repository's privileged context.

Use synthetic fixtures. JSON/HTML can contain actual HTTP response data and log
excerpts even after credential redaction. Keep artifact retention appropriate to
that data. Generated `.releaseproof` artifacts are ignored by Git; review them
before deliberately copying selected sanitized evidence into documentation.

An abruptly terminated job may skip application cleanup. Hosted ephemeral runners
limit the lifetime of those resources. On a persistent self-hosted runner, use the
owned-run inspection procedure in [security.md](security.md) rather than a broad
Docker prune that could disrupt other work.
