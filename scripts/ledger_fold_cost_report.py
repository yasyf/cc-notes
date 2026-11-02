import hashlib
import json
from collections import Counter
from pathlib import Path
import re
import statistics
import sys


root = Path(sys.argv[1])
expected = {
    *(f"BenchmarkLedgerFoldRows/{n}" for n in (32, 10000)),
    *(f"BenchmarkLedgerExtendRows/{n}" for n in (32, 10000)),
    *(f"BenchmarkLedgerFoldHistory/rows={n}/history={h}" for n in (32, 10000) for h in (32, 1024)),
}
expected_tests = {
    "TestLedgerIndexedRemoveReinsertAndReplace",
    "TestLedgerIndexedWalkMatchesEachPrefix",
    "TestLedgerIndexedCheckpointKeepsFirstMatchingRowAndSeed",
}


def read(name):
    path = root / f"{name}.log"
    data = path.read_bytes()
    samples = {}
    snapshots = {}
    passed = Counter()
    for line in data.decode().splitlines():
        if match := re.match(r"^--- PASS: (TestLedgerIndexed\S+) ", line):
            passed[match[1]] += 1
        if match := re.search(r"ledger-fixture\s+(\S+)\s+([0-9a-f]{64})\s+(\d+)", line):
            key, digest, size = match.groups()
            snapshots.setdefault(key, []).append((digest, int(size)))
        if not line.startswith("BenchmarkLedger"):
            continue
        fields = line.split()
        if len(fields) < 4 or not fields[1].isdigit():
            continue
        key = re.sub(r"-\d+$", "", fields[0])
        if key not in expected or int(fields[1]) != 1 or len(fields[2:]) % 2:
            raise ValueError(f"unexpected benchmark population: {line}")
        metrics = {fields[i + 1]: float(fields[i]) for i in range(2, len(fields), 2)}
        if not {"ns/op", "B/op", "allocs/op", "rows", "snapshot-bytes"} <= metrics.keys():
            raise ValueError(f"missing measured benchmark metric: {line}")
        samples.setdefault(key, []).append(metrics)
    if samples.keys() != expected or snapshots.keys() != expected:
        raise ValueError(f"incomplete {name} population: {samples.keys()}, {snapshots.keys()}")
    if passed != dict.fromkeys(expected_tests, 3):
        raise ValueError(f"incomplete {name} semantic regressions: {passed}")
    if any(len(samples[k]) != 3 or len(snapshots[k]) != 3 or len(set(snapshots[k])) != 1 for k in expected):
        raise ValueError(f"incomplete or inconsistent {name} repetitions")
    return {"log_sha256": hashlib.sha256(data).hexdigest(), "semantic_passes": dict(passed), "samples": samples, "snapshots": snapshots}


baseline = read("baseline")
candidate = read("candidate")
rows = []
for key in sorted(expected):
    old = baseline["samples"][key]
    new = candidate["samples"][key]
    if baseline["snapshots"][key] != candidate["snapshots"][key]:
        raise ValueError(f"complete ledger output changed: {key}")
    if [(v["rows"], v["snapshot-bytes"]) for v in old] != [(v["rows"], v["snapshot-bytes"]) for v in new]:
        raise ValueError(f"fixture population changed: {key}")
    old_ns = statistics.median(v["ns/op"] for v in old)
    new_ns = statistics.median(v["ns/op"] for v in new)
    rows.append({"name": key, "snapshot_sha256": candidate["snapshots"][key][0][0], "baseline": old, "candidate": new, "baseline_median_ns": old_ns, "candidate_median_ns": new_ns, "candidate_to_baseline_time_ratio": new_ns / old_ns})

report = {
    "status": "COMPLETE_OUTPUT_PARITY_AND_MEASURED_COST_RECORDED",
    "baseline_head": (root / "baseline-head.txt").read_text().strip(),
    "candidate_head": (root / "candidate-head.txt").read_text().strip(),
    "baseline_log_sha256": baseline["log_sha256"],
    "candidate_log_sha256": candidate["log_sha256"],
    "baseline_semantic_passes": baseline["semantic_passes"],
    "candidate_semantic_passes": candidate["semantic_passes"],
    "rows": rows,
    "limits": ["Hosted fold and immutable-snapshot extension costs only", "Timing and allocation results are measured, not threshold-gated", "No actual PID49938 attribution or physical I/O or installed ledger-operation claim", "Warm snapshot cloning and final sorting remain proportional to selected ledger size"],
}
(root / "report.json").write_text(json.dumps(report, indent=2) + "\n")
print(json.dumps(report))
