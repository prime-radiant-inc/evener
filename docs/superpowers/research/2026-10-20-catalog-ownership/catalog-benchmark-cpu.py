#!/usr/bin/env python3
"""Measure CPU for a fixed-count catalog benchmark in prebuilt Go test binaries.

This includes test-process startup, registry setup and warmup in both sides. It
is a fixture CPU comparison, not isolated per-call CPU or a daemon estimate.
The same benchmark source must be compiled into both binaries before measuring.
"""

import argparse
import json
from pathlib import Path
import re
import sys

from catalog_process import digest, paired_samples, revision, run_process


def measure(binary, benchmark, iterations):
    stdout, metrics = run_process([
        str(binary), "-test.run=^$", f"-test.bench=^{re.escape(benchmark)}$",
        f"-test.benchtime={iterations}x", "-test.count=1", "-test.benchmem",
    ])
    output = stdout.decode()
    lines = [line.split() for line in output.splitlines() if line.startswith("Benchmark")]
    if (len(lines) != 1 or len(lines[0]) < 4
            or lines[0][0].rsplit("-", 1)[0] != benchmark
            or int(lines[0][1]) != iterations or lines[0][3] != "ns/op"
            or output.splitlines().count("PASS") != 1):
        raise ValueError(f"{binary} did not complete exactly the requested benchmark work")
    return {**metrics, "output": output}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", type=Path, required=True, help="baseline Go test binary")
    parser.add_argument("--after", type=Path, required=True, help="candidate Go test binary")
    parser.add_argument("--before-revision", type=revision, required=True)
    parser.add_argument("--after-revision", type=revision, required=True)
    parser.add_argument("--benchmark-source", type=Path, required=True, help="identical benchmark file used for both builds")
    parser.add_argument("--benchmark", required=True, help="exact full benchmark/subbenchmark name")
    parser.add_argument("--iterations", type=int, default=10000, help="timed benchmark iterations (default: 10000)")
    parser.add_argument("--pairs", type=int, default=3)
    args = parser.parse_args()
    if args.pairs < 1 or args.iterations < 1:
        parser.error("requires positive pairs and iterations")
    binaries = {side: getattr(args, side).resolve(strict=True) for side in ("before", "after")}
    revisions = {side: getattr(args, f"{side}_revision") for side in binaries}
    rows = paired_samples(binaries, revisions, args.pairs,
                          lambda binary: measure(binary, args.benchmark, args.iterations))
    print(json.dumps({
        "benchmark": args.benchmark, "iterations": args.iterations,
        "benchmark_source_sha256": digest(args.benchmark_source),
        "driver_sha256": digest(Path(__file__)),
        "process_helper_sha256": digest(Path(__file__).with_name("catalog_process.py")),
        "samples": rows,
    }, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError) as exc:
        print(f"catalog-benchmark-cpu: {exc}", file=sys.stderr)
        sys.exit(1)
