#!/usr/bin/env python3
"""Run with python3 catalog-startup_test.py; no provider access or Go build needed."""

import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

DRIVER = Path(__file__).with_name("catalog-startup.py")


class StartupDriverTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir=os.environ.get("EVENER_SCRATCH_DIR"))
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.before = self.fixture("before")
        self.after = self.fixture("after")

    def checkpoint(self, **overrides):
        return {
            "providers": 219, "registry_loads": 1, "gomaxprocs": 2,
            "heap_alloc_bytes": 1024, "total_alloc_bytes": 4096,
            **overrides,
        }

    def fixture(self, name, *, providers=219, loads=1, gomaxprocs=2, exit_code=0, vm_hwm_kib=32256):
        path = self.root / name
        checkpoint = self.checkpoint(providers=providers, registry_loads=loads, gomaxprocs=gomaxprocs)
        if vm_hwm_kib is not None:
            checkpoint["vm_hwm_kib"] = vm_hwm_kib
        payload = json.dumps(checkpoint)
        path.write_text(f"#!/bin/sh\nprintf '%s\\n' '{payload}'\nexit {exit_code}\n")
        path.chmod(0o700)
        return path

    def vm_hwm_fixture(self, name):
        """A checkpoint child that reports its own executed-image VmHWM."""
        path = self.root / name
        fmt = json.dumps(self.checkpoint())[:-1] + ',"vm_hwm_kib":%s}'
        path.write_text(
            "#!/bin/sh\n"
            "vm_hwm=$(awk '/^VmHWM:/ {print $2}' /proc/self/status)\n"
            f"printf '{fmt}\\n' \"$vm_hwm\"\n"
        )
        path.chmod(0o700)
        return path

    def run_driver(self, extra=(), pairs="3"):
        return subprocess.run(
            [sys.executable, str(DRIVER), "--before", str(self.before),
             "--after", str(self.after), "--pairs", pairs,
             "--before-revision", "a" * 40, "--after-revision", "b" * 40, *extra],
            capture_output=True, text=True, check=False,
        )

    def test_records_alternating_pairs_and_resource_units(self):
        result = self.run_driver()
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        rows = report["samples"]
        self.assertEqual([(r["pair"], r["side"]) for r in rows], [
            (1, "before"), (1, "after"), (2, "after"),
            (2, "before"), (3, "before"), (3, "after"),
        ])
        for row in rows:
            self.assertEqual(row["revision"], ("a" if row["side"] == "before" else "b") * 40)
            self.assertEqual(len(row["binary_sha256"]), 64)
            self.assertEqual(row["peak_rss_kib"], row["checkpoint"]["vm_hwm_kib"])
            self.assertEqual(row["peak_rss_bytes"], row["peak_rss_kib"] * 1024)
            self.assertGreater(row["rusage_maxrss_kib"], 0)
            self.assertGreaterEqual(row["user_cpu_seconds"], 0)
            self.assertGreaterEqual(row["system_cpu_seconds"], 0)
            self.assertEqual(row["checkpoint"]["providers"], 219)

    def test_rejects_probe_without_a_positive_executed_image_peak(self):
        for vm_hwm_kib in [None, True, 0, -1, 12.5, "32256"]:
            with self.subTest(vm_hwm_kib=vm_hwm_kib):
                self.after = self.fixture("bad", vm_hwm_kib=vm_hwm_kib)
                result = self.run_driver()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_parent_memory_does_not_floor_the_reported_peak(self):
        # getrusage counters survive exec, so wait4's ru_maxrss is floored by
        # the launcher's own image; the executed image's VmHWM is the honest
        # peak (#4799). Identical children run from the same driver with and
        # without 40 MiB of parent-only memory: the reported peak must not
        # move, and only the retained rusage diagnostic shows the floor.
        # The rusage-jump assertion below assumes the kernel carries the
        # parent's pre-exec peak into the child's post-exec ru_maxrss (fork
        # copy-on-write semantics); that floor — the very defect #4799
        # documents — is present on the Linux 6.8 host that recorded the
        # shipped samples even where CPython chooses posix_spawn.
        self.before = self.vm_hwm_fixture("before")
        self.after = self.vm_hwm_fixture("after")
        small = self.report_peaks(self.run_driver())
        big = self.report_peaks(self.run_driver(["--parent-pad-mib", "40"]))
        for side in ("before", "after"):
            for small_row, big_row in zip(small[side], big[side]):
                self.assertLessEqual(
                    abs(big_row["peak_rss_kib"] - small_row["peak_rss_kib"]), 2048,
                    f"{side} pair {small_row['pair']}: parent-only memory moved the reported peak",
                )
                self.assertGreaterEqual(
                    big_row["rusage_maxrss_kib"] - small_row["rusage_maxrss_kib"], 35 * 1024,
                    f"{side} pair {small_row['pair']}: the rusage diagnostic lost the inherited-image floor",
                )

    def report_peaks(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)
        report = json.loads(result.stdout)
        return {
            side: [row for row in report["samples"] if row["side"] == side]
            for side in ("before", "after")
        }

    def test_rejects_failed_child_even_with_valid_output(self):
        self.after = self.fixture("failed", exit_code=7)
        result = self.run_driver()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

    def test_rejects_wrong_workload(self):
        for field, value in [("providers", 218), ("loads", 2), ("gomaxprocs", 1)]:
            with self.subTest(field=field):
                self.after = self.fixture("wrong", **{field: value})
                result = self.run_driver()
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.stdout, "")

    def test_rejects_malformed_output(self):
        self.after.write_text("#!/bin/sh\nprintf 'invalid-json\\n'\n")
        result = self.run_driver()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")

    def test_rejects_empty_comparison(self):
        result = self.run_driver(pairs="0")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(result.stdout, "")


if __name__ == "__main__":
    unittest.main()
