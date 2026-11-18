# Reproducing a scenario

Every executed check writes a JSON report containing its scenario IDs,
configuration location, source input fingerprint, and resolved PostgreSQL/old/new
image identities. A failed scenario's terminal and HTML sections include its
reproduction command.

```sh
releaseproof reproduce old-after --report path/to/report.json
```

Use the real path printed by the original check. The command creates a new isolated
run, revalidates the baseline gate, and then runs the selected scenario. It writes
new JSON/HTML reports without modifying the original evidence. It does not resume
the old database, preserve failed containers, or reuse mutations from the old run.

## Required local artifacts

Keep these available:

1. The original `report.json`.
2. The matching configuration, baseline, seed, and migration files.
3. Matching local build sources for applications configured with `build`, or
   accessible matching images for applications configured with `image`.
4. A local Linux Docker daemon and any declared external dependencies.

Reports do not contain a source archive or a database snapshot. Generated database
credentials and temporary container state are not reproduction inputs. Retain the
source commit alongside the report and keep matching immutable application images;
the report's input fingerprint does not itself archive or name a Git commit.

Default evidence is written to the configuration-relative
`.releaseproof/<run-id>/report.json` and `report.html`. These files persist until
you remove them. Containers, databases, networks, and run-generated image tags are
cleaned up after ordinary execution. Preexisting images and Docker's normal build
cache are not a complete reproduction archive.

## Input checks

Before execution, the CLI loads the original configuration and recomputes the
configuration/SQL/build input fingerprint. Changed content causes an explicit
configuration error rather than silently treating a different experiment as a
reproduction.

The fingerprint includes local build inputs, with generated/runtime directories
such as `.git`, `.tools`, `bin`, `.releaseproof`, and the configured report directory
excluded. It is intentionally conservative and is not a full implementation of
Docker's `.dockerignore` rules. An unrelated source/documentation change inside a
broad build context can change the fingerprint. Keep build contexts focused and
restore the recorded checkout when the goal is the same experiment.

During setup, resolved image IDs are compared with the recorded PostgreSQL and
application identities. A different available image produces an execution error.
Version tags can move, and rebuilt images can differ because of base images or
other uncontrolled build inputs. Use immutable image digests and dependency locks
where possible, and retain access to the recorded images if reproduction matters.

These checks make changes visible. They do not promise bit-for-bit repeatability
across different Docker engines, kernels, external services, clocks, random
application behavior, registry state, or hardware.

## Moving a checkout

The report records the original configuration location. If the checkout has moved,
provide the new path:

```sh
releaseproof reproduce old-after \
  --report evidence/report.json \
  --config examples/rolling-deployment/unsafe.yaml
```

Input paths still resolve relative to that configuration. Keep the same relative
layout and file contents. The override changes location; it does not waive the
source or image identity checks.

Use `--output` to place new evidence elsewhere:

```sh
releaseproof reproduce old-after \
  --report evidence/report.json \
  --output .releaseproof-reproductions
```

An explicit output path is relative to the current working directory. Keep it out
of version control and outside a build context if changing that directory would
affect future fingerprints.

## Trying a fix with changed inputs

After modifying a migration or application, a normal `check` evaluates the full
current contract. To run the chosen scenario and baseline with deliberately
changed inputs, use:

```sh
releaseproof reproduce old-after \
  --report evidence/report.json \
  --allow-changed
```

`--allow-changed` explicitly skips the recorded source and image identity checks.
The result is a **fresh experiment**, not evidence that the original failure was
reproduced under identical inputs. The new report records the inputs actually used.

## Interpreting a different result

A scenario ID must exist in both the selected configuration plan and the original
report. The original baseline still has to pass; a broken baseline or missing
dependency is exit 3, not a compatibility regression. An asserted incompatibility
after a valid baseline is exit 1. A passing selected scenario is exit 0 for that
selected experiment; run the complete `check` before concluding that the whole
matrix passes.

If a rerun diverges, compare source fingerprints, resolved images, actual HTTP/SQL
errors, readiness evidence, and recorded environment details. Time measurements
will normally differ. Keep fixture data deterministic and remove uncontrolled
external calls from the compatibility contract when possible.
