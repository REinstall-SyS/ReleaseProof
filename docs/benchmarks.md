# Measured demo performance

These are executed measurements, not estimates or production performance claims.

Source: [CI run 44281911800](https://github.com/REinstall-SyS/ReleaseProof/actions/runs/44281911800),
commit `994d5e16eefe6e0bd3015886447c5b5bd99edbb1`, November 18, 2026.
The downloadable `benchmark-evidence` artifact contains the complete
`measurements.json`, every scenario timing, image IDs, initial image/build-cache
inventory, and terminal/JSON/HTML reports.

## Environment and workload

- GitHub-hosted Ubuntu 24.04.5 LTS, Linux x86-64, 4 logical CPUs.
- Docker Engine 28.0.4, overlay2, 16,760,348,672 bytes RAM reported by Docker.
- Go 1.27.4; PostgreSQL `17.12-alpine`.
- Six scenarios; two seed users per scenario. Every scenario loads its own
  baseline and fixtures into a fresh database. Five apply the safe migration.
- One initial run and two warm repetitions, sequentially on the same runner.

The initial inventory contained neither the Go/PostgreSQL demo images nor a Docker
build cache. Thus the first run includes cold pulls and application compilation
for these images. This is not a claim that registries, network caches, or the
entire machine were cold. No Docker prune is performed by the benchmark.

## Results

| Measurement | First run | Warm run 1 | Warm run 2 |
| --- | ---: | ---: | ---: |
| Complete CLI wall time | 51.589 s | 9.881 s | 7.849 s |
| Setup: Docker checks, image preparation, PostgreSQL readiness | 44.563 s | 2.142 s | 2.122 s |
| Migration execution, total over five databases | 0.276 s | 0.406 s | 0.296 s |
| Run cleanup | 0.789 s | 1.083 s | 0.795 s |

Warm median complete wall time: **8.865 seconds** (two samples).
Individual first-run scenarios took 0.751–1.624 seconds, including their schema
initialization, migration when applicable, application startup, checks, and
application cleanup. Full per-scenario measurements remain in the artifact.

The Python harness measures complete subprocess wall time using
`time.perf_counter`. The Go report records phase durations using `time.Since`.
Migration timing includes psql startup and sending each migration file, not just
server CPU time. Scenario durations overlap the migration duration, so the table's
rows are not independent totals to add together. The external wall time also
includes report generation. Warm runs reuse Docker layers and base images while
recreating all databases, application containers, and network resources.

Two warm observations cannot establish a latency distribution or a reliable p95.
Hosted-runner noise, image caches, registry latency, fixture size, and application
startup all affect results. This tiny fixture does not measure production locks,
backfill throughput, contention, or high-volume deployment behavior.

## Reproduce

```sh
go build -o bin/releaseproof ./cmd/releaseproof
python scripts/benchmark.py --runs 3
```

The command writes `.releaseproof/benchmark/measurements.json` and one report
directory per run. Use a fresh disposable runner to measure first-run setup.
Record its initial inventory instead of deleting unrelated Docker resources.
