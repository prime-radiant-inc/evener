#!/usr/bin/env -S python3 -u
"""Wave launcher: fan-out eval tasks one-per-instance, backfill as slots free.

Called by run_eval.sh --wave. Not intended to be run directly.

Manages a work queue of (task, rep) pairs, launches them as individual EC2
instances via harbor-runner/launch.sh, polls for completion, and backfills
freed slots until all work is done.
"""

import argparse
import json
import os
import signal
import subprocess
import sys
import time
from dataclasses import dataclass, field

# vCPU counts for instance types we use
VCPU_MAP = {
    "c6i.large": 2,
    "c6i.xlarge": 4,
    "c6i.2xlarge": 8,
    "c6i.4xlarge": 16,
    "m6i.xlarge": 4,
    "m6i.2xlarge": 8,
    "m6i.4xlarge": 16,
}

POLL_INTERVAL = 60  # seconds between completion checks
LAUNCH_STAGGER = 2  # seconds between launches to avoid API throttle
STUCK_TIMEOUT = 3600  # 60 minutes — terminate and requeue
MAX_ATTEMPTS = 3
AWS_REGION = os.environ.get("AWS_REGION", "us-west-1")


@dataclass
class WorkItem:
    task: str
    rep: int
    status: str = "pending"  # pending, launched, done, failed
    instance_id: str | None = None
    launched_at: float | None = None
    attempts: int = 0


def parse_args():
    p = argparse.ArgumentParser(description="Wave launcher orchestrator")
    p.add_argument("--run-id", required=True)
    p.add_argument("--agent-dir", required=True)
    p.add_argument("--model", required=True)
    p.add_argument("--tasks", required=True, help="Comma-separated task names")
    p.add_argument("--reps", type=int, required=True)
    p.add_argument("--instance-type", default="c6i.xlarge")
    p.add_argument("--concurrency", type=int, default=1)
    p.add_argument("--max-vcpu", type=int, default=128)
    p.add_argument("--harbor-dir", required=True)
    return p.parse_args()


def build_work_queue(tasks: list[str], reps: int) -> list[WorkItem]:
    """Build queue ordered by rep then task — completes rep-1 first for early scores."""
    queue = []
    for rep in range(1, reps + 1):
        for task in tasks:
            queue.append(WorkItem(task=task, rep=rep))
    return queue


def launch_one(
    item: WorkItem,
    run_id: str,
    agent_dir: str,
    model: str,
    instance_type: str,
    concurrency: int,
    harbor_dir: str,
) -> str | None:
    """Launch a single instance for one task-rep. Returns instance ID or None on failure."""
    cmd = [
        os.path.join(harbor_dir, "launch.sh"),
        "--run-id", run_id,
        "--rep", str(item.rep),
        "--agent-dir", agent_dir,
        "--agent-import-path", "serf_agent:SerfAgent",
        "--model", model,
        "--task-names", item.task,
        "--concurrency", str(concurrency),
        "--instance-type", instance_type,
    ]
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=120, cwd=harbor_dir
        )
        if result.returncode != 0:
            # Check for capacity errors (retryable)
            if "InsufficientInstanceCapacity" in result.stderr:
                print(f"  Spot capacity unavailable for {item.task} rep-{item.rep}, will retry")
                return None
            print(f"  Launch failed for {item.task} rep-{item.rep}: {result.stderr.strip()}")
            return None

        # Parse instance ID from launch.sh output
        for line in result.stdout.splitlines():
            if "Instance:" in line:
                # Format: "  Instance: i-0abc123def (rep N)"
                parts = line.strip().split()
                idx = parts.index("Instance:")
                return parts[idx + 1]

        print(f"  Warning: no instance ID found in launch output for {item.task}")
        return None

    except subprocess.TimeoutExpired:
        print(f"  Launch timed out for {item.task} rep-{item.rep}")
        return None


def check_instance_states(instance_ids: list[str]) -> dict[str, str]:
    """Batch query EC2 for instance states. Returns {instance_id: state}."""
    if not instance_ids:
        return {}

    cmd = [
        "aws", "ec2", "describe-instances",
        "--instance-ids", *instance_ids,
        "--region", AWS_REGION,
        "--query", "Reservations[*].Instances[*].{Id:InstanceId,State:State.Name}",
        "--output", "json",
    ]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=30)
        if result.returncode != 0:
            print(f"  Warning: describe-instances failed: {result.stderr.strip()}")
            return {}

        data = json.loads(result.stdout)
        states = {}
        for reservation in data:
            for instance in reservation:
                states[instance["Id"]] = instance["State"]
        return states

    except (subprocess.TimeoutExpired, json.JSONDecodeError) as e:
        print(f"  Warning: instance state check failed: {e}")
        return {}


def terminate_instances(run_id: str, harbor_dir: str):
    """Terminate all instances for a run."""
    cmd = [os.path.join(harbor_dir, "terminate.sh"), "--run-id", run_id]
    subprocess.run(cmd, capture_output=True, timeout=30)


def format_elapsed(seconds: float) -> str:
    m, s = divmod(int(seconds), 60)
    h, m = divmod(m, 60)
    if h:
        return f"{h}h{m:02d}m"
    return f"{m}m{s:02d}s"


def main():
    args = parse_args()
    tasks = args.tasks.split(",")
    queue = build_work_queue(tasks, args.reps)
    total = len(queue)

    vcpu_per = VCPU_MAP.get(args.instance_type, 4)
    max_concurrent = args.max_vcpu // vcpu_per

    print(f"=== Wave launcher ===")
    print(f"Work items:     {total} ({len(tasks)} tasks x {args.reps} reps)")
    print(f"Max concurrent: {max_concurrent} instances ({vcpu_per} vCPU each, {args.max_vcpu} quota)")
    print(f"Poll interval:  {POLL_INTERVAL}s")
    print()

    # Track state
    pending = list(queue)  # items waiting to launch
    active: dict[str, WorkItem] = {}  # instance_id -> WorkItem
    done = 0
    failed = 0
    start_time = time.time()

    # SIGINT handler: print state and offer to terminate
    shutting_down = False

    def handle_signal(sig, frame):
        nonlocal shutting_down
        if shutting_down:
            print("\nForce quit.")
            sys.exit(1)
        shutting_down = True
        print(f"\nInterrupted. {len(active)} instances still running.")
        if active:
            print(f"Terminating all instances for {args.run_id}...")
            terminate_instances(args.run_id, args.harbor_dir)
        print(f"Final state: {done}/{total} done, {failed} failed, {len(pending)} never launched")
        sys.exit(1)

    signal.signal(signal.SIGINT, handle_signal)
    signal.signal(signal.SIGTERM, handle_signal)

    # --- Initial wave ---
    # First launch must complete (uploads agent tarball to S3).
    if pending:
        item = pending.pop(0)
        item.attempts += 1
        print(f"Launching {item.task} rep-{item.rep} (1/{total}, uploads tarball)...")
        instance_id = launch_one(
            item, args.run_id, args.agent_dir, args.model,
            args.instance_type, args.concurrency, args.harbor_dir,
        )
        if instance_id:
            item.status = "launched"
            item.instance_id = instance_id
            item.launched_at = time.time()
            active[instance_id] = item
        else:
            item.status = "pending"
            pending.insert(0, item)
            print("First launch failed — cannot continue without tarball upload.")
            sys.exit(1)

    # Launch rest of initial wave with stagger
    while len(active) < max_concurrent and pending:
        item = pending.pop(0)
        item.attempts += 1
        instance_id = launch_one(
            item, args.run_id, args.agent_dir, args.model,
            args.instance_type, args.concurrency, args.harbor_dir,
        )
        if instance_id:
            item.status = "launched"
            item.instance_id = instance_id
            item.launched_at = time.time()
            active[instance_id] = item
            launched_total = done + len(active)
            print(f"  Launched {item.task} rep-{item.rep} ({launched_total}/{total}) -> {instance_id}")
        else:
            # Requeue for retry
            item.status = "pending"
            pending.append(item)

        if pending and len(active) < max_concurrent:
            time.sleep(LAUNCH_STAGGER)

    print()
    print(f"Initial wave: {len(active)} instances launched, {len(pending)} queued")
    print()

    # --- Poll and backfill loop ---
    while active or pending:
        time.sleep(POLL_INTERVAL)

        # Check which instances have terminated
        states = check_instance_states(list(active.keys()))

        newly_done = []
        for iid, item in list(active.items()):
            state = states.get(iid)

            if state in ("terminated", "shutting-down"):
                newly_done.append(iid)
                item.status = "done"
                done += 1
                del active[iid]

            elif item.launched_at and (time.time() - item.launched_at) > STUCK_TIMEOUT:
                # Stuck — terminate and requeue
                print(f"  Timeout: {item.task} rep-{item.rep} ({iid}) running >{STUCK_TIMEOUT//60}m, terminating")
                subprocess.run(
                    ["aws", "ec2", "terminate-instances",
                     "--instance-ids", iid, "--region", AWS_REGION],
                    capture_output=True, timeout=15,
                )
                del active[iid]
                if item.attempts < MAX_ATTEMPTS:
                    item.status = "pending"
                    item.instance_id = None
                    item.launched_at = None
                    pending.append(item)
                else:
                    item.status = "failed"
                    failed += 1
                    print(f"  Failed permanently: {item.task} rep-{item.rep} after {MAX_ATTEMPTS} attempts")

        # Backfill freed slots
        launched_this_cycle = 0
        while len(active) < max_concurrent and pending:
            item = pending.pop(0)
            item.attempts += 1
            instance_id = launch_one(
                item, args.run_id, args.agent_dir, args.model,
                args.instance_type, args.concurrency, args.harbor_dir,
            )
            if instance_id:
                item.status = "launched"
                item.instance_id = instance_id
                item.launched_at = time.time()
                active[instance_id] = item
                launched_this_cycle += 1
            else:
                if item.attempts < MAX_ATTEMPTS:
                    item.status = "pending"
                    pending.append(item)
                else:
                    item.status = "failed"
                    failed += 1

            if pending and len(active) < max_concurrent:
                time.sleep(LAUNCH_STAGGER)

        # Progress report
        elapsed = format_elapsed(time.time() - start_time)

        # Per-rep progress
        rep_done = {}
        for item in queue:
            if item.rep not in rep_done:
                rep_done[item.rep] = 0
            if item.status == "done":
                rep_done[item.rep] += 1
        rep_str = "  ".join(f"rep-{r}: {c}/{len(tasks)}" for r, c in sorted(rep_done.items()))

        status_parts = [
            f"{done}/{total} done",
            f"{len(active)} running",
            f"{len(pending)} queued",
        ]
        if failed:
            status_parts.append(f"{failed} failed")
        if launched_this_cycle:
            status_parts.append(f"+{launched_this_cycle} launched")

        ts = time.strftime("%H:%M:%S")
        print(f"[{ts}] [{elapsed}] {' | '.join(status_parts)} | {rep_str}")

    # --- Summary ---
    elapsed = format_elapsed(time.time() - start_time)
    print()
    print(f"=== Wave complete in {elapsed} ===")
    print(f"Done: {done}/{total}")
    if failed:
        print(f"Failed: {failed}")
        for item in queue:
            if item.status == "failed":
                print(f"  {item.task} rep-{item.rep} ({item.attempts} attempts)")

    print()
    print("Next steps:")
    print(f"  ./tools/run_status.sh {args.run_id}")
    print(f"  ./tools/post_run.sh {args.run_id}")


if __name__ == "__main__":
    main()
