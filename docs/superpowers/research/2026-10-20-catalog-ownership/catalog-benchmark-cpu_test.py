#!/usr/bin/env python3
"""Check the lookup/list CPU driver's acceptance of actual subprocess output."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

DRIVER = Path(__file__).with_name("catalog-benchmark-cpu.py")
BENCHMARK = "BenchmarkCatalogLookupAndList/Resolve"


class BenchmarkCPUDriverTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=os.environ.get("EVENER_SCRATCH_DIR"))
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.binary = self.root / "fixture"
        self.binary.write_text(
            f"#!/bin/sh\nprintf '%s\\n' '{BENCHMARK}-2 10 123 ns/op 8 B/op 1 allocs/op' 'PASS'\n"
        )
        self.binary.chmod(0o700)

    def run_driver(self):
        return subprocess.run(
            [sys.executable, str(DRIVER), "--before", str(self.binary),
             "--after", str(self.binary), "--before-revision", "a" * 40,
             "--after-revision", "b" * 40, "--benchmark", BENCHMARK,
             "--iterations", "10", "--pairs", "1", "--benchmark-source", __file__],
            capture_output=True, text=True, check=False,
        )

    def test_accepts_named_benchmark_with_exact_iteration_count(self):
        result = self.run_driver()
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        self.assertEqual(report["benchmark"], BENCHMARK)
        self.assertEqual(report["iterations"], 10)
        self.assertEqual([r["side"] for r in report["samples"]], ["before", "after"])
        for row in report["samples"]:
            self.assertGreaterEqual(row["user_cpu_seconds"] + row["system_cpu_seconds"], 0)

    def test_rejects_empty_wrong_or_incomplete_benchmark_output(self):
        for output in ["PASS", f"{BENCHMARK}-2 9 123 ns/op\\nPASS",
                       "BenchmarkOther-2 10 123 ns/op\\nPASS",
                       f"{BENCHMARK}-2 10 123 ns/op"]:
            with self.subTest(output=output):
                self.binary.write_text(f"#!/bin/sh\nprintf '{output}\\n'\n")
                result = self.run_driver()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
