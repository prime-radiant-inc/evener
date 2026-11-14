"""Shared Linux subprocess accounting for the offline catalog measurement drivers."""

import argparse
import hashlib
import os
import re
import subprocess
import sys
import tempfile


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def revision(value):
    if not re.fullmatch(r"[0-9a-f]{40}", value):
        raise argparse.ArgumentTypeError("revision must be a full Git commit SHA")
    return value


def run_process(argv):
    """Return bounded stdout and the exited child's wait4 counters, never parent CPU."""
    if sys.platform != "linux":
        raise ValueError("measurement requires Linux ru_maxrss units")
    env = dict(os.environ, GOMAXPROCS="2")
    scratch = os.environ.get("EVENER_SCRATCH_DIR")
    with tempfile.TemporaryFile(dir=scratch) as out, tempfile.TemporaryFile(dir=scratch) as err:
        process = subprocess.Popen(argv, stdout=out, stderr=err, env=env)
        try:
            _, status, usage = os.wait4(process.pid, 0)
        except BaseException:
            process.kill()
            process.wait()
            raise
        process.returncode = os.waitstatus_to_exitcode(status)
        out.seek(0)
        err.seek(0)
        stdout, stderr = out.read(65537), err.read(65537)
    if process.returncode != 0:
        raise ValueError(f"{argv[0]} exited {process.returncode}: {stderr.decode(errors='replace')}")
    if stderr or len(stdout) > 65536:
        raise ValueError(f"{argv[0]} emitted diagnostics or oversized output")
    if usage.ru_maxrss <= 0:
        raise ValueError(f"{argv[0]} has no rusage result")
    return stdout, {
        # getrusage counters survive exec, so wait4's ru_maxrss is floored by
        # the launcher's own image (#4799): it is retained only as a
        # diagnostic. The honest executed-image peak is the child's VmHWM,
        # which the measured probe self-reports and its driver validates.
        "rusage_maxrss_kib": usage.ru_maxrss,
        "rusage_maxrss_bytes": usage.ru_maxrss * 1024,
        "user_cpu_seconds": usage.ru_utime,
        "system_cpu_seconds": usage.ru_stime,
    }


def paired_samples(binaries, revisions, pairs, measure):
    """Alternate the first side across pairs and reject changes to measured binaries."""
    identities = {side: digest(path) for side, path in binaries.items()}
    rows = []
    for pair in range(1, pairs + 1):
        order = ("before", "after") if pair % 2 else ("after", "before")
        for side in order:
            rows.append({
                "pair": pair, "side": side,
                "revision": revisions[side], "binary_sha256": identities[side],
                **measure(binaries[side]),
            })
    if any(digest(path) != identities[side] for side, path in binaries.items()):
        raise ValueError("a measurement binary changed during the comparison")
    return rows
