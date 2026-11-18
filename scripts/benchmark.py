"""Measure real checks. First-run and warm samples are kept separate, never invented."""
import argparse
import json
import os
from pathlib import Path
import platform
import statistics
import subprocess
import time
import uuid

ROOT = Path(__file__).resolve().parents[1]


def command(args):
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True, check=True, timeout=60)
    return result.stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--runs", type=int, default=3, help="total runs, including the first-run sample (minimum 2)")
    parser.add_argument("--output", type=Path, default=ROOT / ".releaseproof" / "benchmark")
    args = parser.parse_args()
    if args.runs < 2:
        parser.error("--runs must be at least 2")
    binary = ROOT / "bin" / ("releaseproof.exe" if os.name == "nt" else "releaseproof")
    args.output.mkdir(parents=True, exist_ok=True)
    environment = {
        "os": platform.platform(), "cpu": platform.processor(), "logical_cpus": os.cpu_count(),
        "docker_version": command(["docker", "version", "--format", "{{json .}}"]),
        "docker_info": command(["docker", "info", "--format", "{{json .}}"]),
        "go": command(["go", "version"]),
        "image_cache_before": command(["docker", "image", "ls", "--format", "{{.Repository}}:{{.Tag}} {{.ID}}"]),
        "builder_cache_before": command(["docker", "system", "df"]),
    }
    cpuinfo = Path('/proc/cpuinfo')
    if cpuinfo.exists():
        environment['cpu_model'] = next((line.split(':', 1)[1].strip() for line in cpuinfo.read_text().splitlines() if line.startswith('model name')), platform.processor())
    samples = []
    for index in range(args.runs):
        output = args.output / f"run-{index + 1}-{uuid.uuid4().hex[:12]}"
        start = time.perf_counter()
        result = subprocess.run([str(binary), "check", "--config", "examples/rolling-deployment/safe.yaml", "--output", str(output)], cwd=ROOT, text=True, capture_output=True, timeout=600)
        elapsed = (time.perf_counter() - start) * 1000
        (args.output / f"run-{index + 1}.txt").write_text(result.stdout + result.stderr, encoding="utf-8")
        if result.returncode:
            raise RuntimeError(f"benchmark run {index + 1} failed with exit {result.returncode}: {result.stdout}\n{result.stderr}")
        paths = list(output.glob("*/report.json"))
        assert len(paths) == 1, paths
        report = json.loads(paths[0].read_text(encoding="utf-8"))
        sample = {"run": index + 1, "phase": "first_run" if index == 0 else "warm", "wall_ms": round(elapsed, 2), "report_duration_ms": report["duration_ms"], "setup_ms": report["timings"]["setup_ms"], "cleanup_ms": report["timings"]["cleanup_ms"], "migration_total_ms": sum(s["migration_ms"] for s in report["scenarios"]), "scenarios": [{"id": s["id"], "duration_ms": s["duration_ms"], "migration_ms": s["migration_ms"]} for s in report["scenarios"]], "images": report["images"]}
        samples.append(sample)
        print(f"{sample['phase']}: {sample['wall_ms']} ms (setup {sample['setup_ms']} ms)", flush=True)
    measurement = {"methodology": "Sequential complete safe checks, isolated PostgreSQL per run, six isolated scenario databases, two seed users per scenario. First sample includes image pulls/builds absent from the recorded initial cache; no global Docker prune is performed. Warm samples reuse Docker layers/base images but recreate all runtime state. Not a production workload or lock/traffic benchmark.", "fixture_users_per_scenario": 2, "runs": args.runs, "environment": environment, "samples": samples, "first_run_wall_ms": samples[0]["wall_ms"], "warm_median_wall_ms": statistics.median(s["wall_ms"] for s in samples[1:])}
    target = args.output / "measurements.json"
    target.write_text(json.dumps(measurement, indent=2) + "\n", encoding="utf-8")
    print(f"Measurements: {target}")


if __name__ == "__main__":
    main()
