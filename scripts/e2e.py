"""Run the actual CLI and verify the machine and human reports against evidence."""
import json
import os
from pathlib import Path
import subprocess
import uuid

ROOT = Path(__file__).resolve().parents[1]
BIN = ROOT / "bin" / ("releaseproof.exe" if os.name == "nt" else "releaseproof")
ARTIFACTS = ROOT / ".releaseproof" / "e2e"


def run(label, config, expected, extra=None):
    output = ARTIFACTS / f"{label}-{uuid.uuid4().hex[:12]}"
    args = [str(BIN), "check", "--config", str(ROOT / config), "--output", str(output)]
    if extra:
        args = extra + ["--output", str(output)]
    result = subprocess.run(args, cwd=ROOT, text=True, capture_output=True, timeout=600)
    print(result.stdout, end="")
    print(result.stderr, end="")
    ARTIFACTS.mkdir(parents=True, exist_ok=True)
    (ARTIFACTS / f"{label}.txt").write_text(result.stdout + result.stderr, encoding="utf-8")
    assert result.returncode == expected, (label, result.returncode, result.stdout, result.stderr)
    paths = list(output.glob("*/report.json"))
    assert len(paths) == 1, paths
    report = json.loads(paths[0].read_text(encoding="utf-8"))
    html = paths[0].with_suffix(".html").read_text(encoding="utf-8")
    assert report["run_id"] in html
    assert report["status"] in html
    assert report["fingerprint"] in html
    for scenario in report["scenarios"]:
        assert scenario["id"] in html
        assert scenario["name"] in html
        assert scenario["status"] in html
    return paths[0], report


def main():
    unsafe_path, unsafe = run("unsafe", "examples/rolling-deployment/unsafe.yaml", 1)
    cases = {s["kind"]: s for s in unsafe["scenarios"]}
    assert unsafe["status"] == "incompatible"
    assert cases["baseline"]["status"] == "pass"
    assert cases["new_after"]["status"] == "pass"
    assert cases["old_after"]["status"] == "fail"
    assert any(step["status"] == 500 and "full_name" in step["body"] for step in cases["old_after"]["steps"])
    assert cases["old_after"]["migration_ms"] >= 0
    _, safe = run("safe", "examples/rolling-deployment/safe.yaml", 0)
    assert safe["status"] == "compatible"
    assert all(s["status"] == "pass" for s in safe["scenarios"])
    semantic_config = "examples/rolling-deployment/semantic.yaml"
    _, semantic = run("semantic", semantic_config, 1)
    assert semantic["status"] == "incompatible"
    assert any(s["status"] == "fail" and any(step["status"] == 200 and any(not a["passed"] for a in step["assertions"]) for step in s["steps"]) for s in semantic["scenarios"])
    selected = cases["old_after"]["id"]
    _, reproduced = run("reproduced", "", 1, [str(BIN), "reproduce", selected, "--report", str(unsafe_path)])
    assert reproduced["fingerprint"] == unsafe["fingerprint"]
    assert reproduced["images"] == unsafe["images"]
    assert [s["id"] for s in reproduced["scenarios"]] == [cases["baseline"]["id"], selected]
    assert reproduced["scenarios"][-1]["status"] == "fail"
    print("E2E PASS: unsafe, safe, semantic, JSON/HTML evidence, exact-input reproduction")


if __name__ == "__main__":
    main()
