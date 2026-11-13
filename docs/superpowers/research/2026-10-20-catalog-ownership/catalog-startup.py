#!/usr/bin/env python3
"""Compare Linux one-registry startup peak RSS and CPU using prebuilt probes.

Build catalog-startup.go against both source revisions before invoking this
script. Revision arguments label those builds; binary and probe hashes retain
artifact identity. Compilation and JSON validation are outside measured CPU.
The reported peak is the probe's self-reported executed-image VmHWM; wait4's
ru_maxrss survives exec and is floored by the launcher's image, so it is
retained only as a diagnostic (#4799).
"""

import argparse
import json
from pathlib import Path
import sys

from catalog_process import digest, paired_samples, revision, run_process


def measure(binary):
    stdout, metrics = run_process([str(binary)])
    checkpoint = json.loads(stdout)
    for key, expected in (("providers", 219), ("registry_loads", 1), ("gomaxprocs", 2)):
        if checkpoint.get(key) != expected:
            raise ValueError(f"probe {binary} returned unexpected {key}: {checkpoint.get(key)!r}")
    for key in ("heap_alloc_bytes", "total_alloc_bytes", "vm_hwm_kib"):
        if type(checkpoint.get(key)) is not int or checkpoint[key] <= 0:
            raise ValueError(f"probe {binary} did not report a positive {key}: {checkpoint.get(key)!r}")
    vm_hwm_kib = checkpoint["vm_hwm_kib"]
    return {
        "peak_rss_kib": vm_hwm_kib,
        "peak_rss_bytes": vm_hwm_kib * 1024,
        **metrics,
        "checkpoint": checkpoint,
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--before", required=True, type=Path, help="prebuilt baseline probe")
    parser.add_argument("--after", required=True, type=Path, help="prebuilt candidate probe")
    parser.add_argument("--before-revision", required=True, type=revision, help="Git SHA used to build --before")
    parser.add_argument("--after-revision", required=True, type=revision, help="Git SHA used to build --after")
    parser.add_argument("--pairs", default=3, type=int, help="paired fresh-process runs (default: 3)")
    parser.add_argument(
        "--parent-pad-mib", default=0, type=int,
        help="MiB of parent-only memory the driver holds while measuring, "
        "to prove the reported peak ignores the launcher's image (#4799)",
    )
    args = parser.parse_args()
    if sys.platform != "linux" or args.pairs < 1 or args.parent_pad_mib < 0:
        parser.error("requires Linux, at least one pair and a non-negative parent pad")
    binaries = {side: getattr(args, side).resolve(strict=True) for side in ("before", "after")}
    revisions = {side: getattr(args, f"{side}_revision") for side in binaries}
    parent_pad = bytearray(args.parent_pad_mib * 1024 * 1024)
    for offset in range(0, len(parent_pad), 4096):
        parent_pad[offset] = 1
    rows = paired_samples(binaries, revisions, args.pairs, measure)
    print(json.dumps({
        "platform": sys.platform,
        "parent_pad_mib": args.parent_pad_mib,
        "process_helper_sha256": digest(Path(__file__).with_name("catalog_process.py")),
        "probe_sha256": digest(Path(__file__).with_name("catalog-startup.go")),
        "driver_sha256": digest(Path(__file__)),
        "samples": rows,
    }, indent=2))


if __name__ == "__main__":
    try:
        main()
    except (OSError, ValueError, TypeError, AttributeError) as exc:
        print(f"catalog-startup: {exc}", file=sys.stderr)
        sys.exit(1)
