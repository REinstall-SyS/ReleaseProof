# Executed validation

The first complete validation ran on November 18, 2026 in
[GitHub Actions run 44281911800](https://github.com/REinstall-SyS/ReleaseProof/actions/runs/44281911800)
for commit `994d5e16eefe6e0bd3015886447c5b5bd99edbb1`. Both verification and
benchmark jobs succeeded. The artifacts contain executed evidence rather than
generated examples.

| Check | Observed result |
| --- | --- |
| Go formatting and `go vet ./...` | Passed |
| `go test -race -count=1 -coverprofile=coverage.out ./...` | All packages passed |
| Actual Docker/PostgreSQL integration suite | Passed, 74.511 s |
| Unsafe CLI demo | Exit 1; baseline/new version passed, old version received HTTP 500 with SQLSTATE 42703 |
| Safe CLI demo | Exit 0; all six scenarios passed |
| Semantic regression CLI demo | Exit 1; JSON assertions failed despite HTTP 200 responses |
| CLI reproduction of `old-after` | Exit 1; same failing HTTP/SQL operation in a fresh run |
| JSON/HTML evidence checks | Matched executed results |
| Container/network cleanup check | No run-owned resources remained |

The integration suite used real PostgreSQL 17.12 and real old/new Go HTTP
applications. It exercised compatible coexistence, removed columns, stale
cross-version writes, invalid migration SQL, nontransactional
`CREATE INDEX CONCURRENTLY`, missing executable dependencies, application readiness
timeout, database connection failure, baseline gating, cancellation during a
running migration, concurrent executions, cleanup, and recorded-image reproduction.

Unit-only coverage is deliberately not presented as integration coverage. The
Docker package's orchestration runs under the integration tag, and the main
function runs through the CLI end-to-end harness. The artifact contains per-package
unit coverage; this document makes no blanket coverage claim.

## Hardened implementation

[CI run 44283116533](https://github.com/REinstall-SyS/ReleaseProof/actions/runs/44283116533)
passed for commit `af511c38d9f0253cae80417b7986d3a8e0ceb0f1` after dependency
lockfiles, exact large-integer JSON comparisons, SQL lexical checks, psql restricted
mode, and bounded terminal diagnostics were added. `go mod verify`, a clean
`go mod tidy` diff, `gofmt`, `go vet`, unit tests with the race detector, and real
container integration tests with the race detector all passed. The integration
test command completed in **71.475 seconds**, including its real container startup.

The added privilege test verified that neither migration nor application roles
were superusers, application DML worked, application schema/role/database changes
and password-catalog reads were denied, and an application's credentials could
not connect to another scenario's database. A direct execution-layer test also
confirmed that psql restricted mode rejected metacommands independently of the
file lexer. Full CLI demo/report/reproduction checks and the separate benchmark
job passed again. A Windows amd64 CLI was cross-compiled successfully and included
in the validation artifact.

## Machine and dependency identity

The runner used Go 1.27.4, Ubuntu 24.04.5 LTS, Linux x86-64, Docker Engine 28.0.4,
4 logical CPUs and 16,760,348,672 bytes of memory. Each scenario started from two
seed users in a separate database. Reports record actual image IDs and input
fingerprints. Direct and transitive Go versions are locked in `go.mod`/`go.sum`.

See [benchmarks.md](benchmarks.md) for measured setup/migration/scenario/total
timings and the cache methodology.

## Local execution limits

On the originating Windows workstation, Git, Python, and GitHub CLI were
available. Docker was absent, and WSL reported that it was not installed. The
official Go 1.27.4 archive was downloaded with its published SHA-256 verified,
but Windows Application Control blocked `go.exe`, including outside the
filesystem/network sandbox. `gofmt.exe` and Python script syntax checks ran
locally. The build, database tests, demos and benchmarks above therefore ran on
the linked Linux CI runner. Windows Application Control also blocked the
CI-built `bin/releaseproof.exe` when local execution was attempted. No local CLI
or PostgreSQL execution is claimed. There was no GitHub authentication blocker;
the public repository and pushed commits were verified through GitHub CLI.

GitHub authentication worked through the existing keyring outside the network
sandbox. Source was published to the public `REinstall-SyS/ReleaseProof`
repository. No credentials, local toolchain, live database contents, or generated
run directories are tracked in Git.

## Evidence and reproduction lifecycle

Download `validation-evidence` for terminal output and JSON/HTML reports, and
`benchmark-evidence` for the full measurement file. GitHub artifact retention is
finite. Only synthetic, reviewed evidence excerpts are retained in this repository.

Downloaded CI reports retain runner-specific source paths. To experiment on a
different checkout, explicitly use `reproduce ... --report ... --config ...
--allow-changed`; the command labels changed inputs as a fresh experiment. A
normal local `reproduce` requires the recorded local inputs and matching image
IDs. See [reproduction.md](reproduction.md).
