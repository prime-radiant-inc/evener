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
    "m6i.large": 2,
    "m6i.xlarge": 4,
    "m6i.2xlarge": 8,
    "m6i.4xlarge": 16,
    "r6i.large": 2,
    "r6i.xlarge": 4,
    "r6i.2xlarge": 8,
}

POLL_INTERVAL = 60  # seconds between completion checks
LAUNCH_STAGGER = 2  # seconds between launches to avoid API throttle
STUCK_TIMEOUT = 14400  # 4 hours — must exceed longest task timeout (up to 3h20m) + setup/cleanup
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
    p.add_argument("--agent-dir", default="")
    p.add_argument("--agent-name", default="",
                   help="Built-in Harbor agent name (e.g., terminus-2)")
    p.add_argument("--agent-import-path", default="serf_agent:SerfAgent")
    p.add_argument("--model", required=True)
    p.add_argument("--tasks", required=True, help="Comma-separated task names")
    p.add_argument("--reps", type=int, required=True)
    p.add_argument("--instance-type", default="c6i.xlarge")
    p.add_argument("--concurrency", type=int, default=1)
    p.add_argument("--max-vcpu", type=int, default=128)
    p.add_argument("--harbor-dir", required=True)
    p.add_argument("--agent-kwargs", default="",
                   help="Space-separated key=value pairs passed to SerfAgent "
                        "(e.g., 'system_prompt_as_user=true reasoning_effort=medium')")
    p.add_argument("--backfill", action="store_true",
                   help="Check S3 for existing results and skip completed task/rep combos")
    p.add_argument("--on-demand", action="store_true",
                   help="Launch on-demand instances instead of spot (uses separate vCPU quota)")
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
    agent_kwargs: str = "",
    agent_name: str = "",
    agent_import_path: str = "serf_agent:SerfAgent",
    on_demand: bool = False,
) -> str | None:
    """Launch a single instance for one task-rep.

    Returns instance ID on success, "transient" for retryable errors
    (capacity/quota), or None for permanent failures.
    """
    cmd = [
        os.path.join(harbor_dir, "launch.sh"),
        "--run-id", run_id,
        "--rep", str(item.rep),
        "--model", model,
        "--task-names", item.task,
        "--concurrency", str(concurrency),
        "--instance-type", instance_type,
    ]
    if on_demand:
        cmd.append("--on-demand")
    if agent_name:
        cmd.extend(["--agent-name", agent_name])
    else:
        cmd.extend(["--agent-dir", agent_dir,
                     "--agent-import-path", agent_import_path])
    if agent_kwargs:
        cmd.extend(["--agent-kwargs", agent_kwargs])
    try:
        result = subprocess.run(
            cmd, capture_output=True, text=True, timeout=120, cwd=harbor_dir
        )
        if result.returncode != 0:
            # Transient errors — retryable, don't count against attempts
            transient_errors = [
                "InsufficientInstanceCapacity",
                "MaxSpotInstanceCountExceeded",
                "SpotMaxPriceTooLow",
                "RequestLimitExceeded",
            ]
            if any(e in result.stderr for e in transient_errors):
                print(f"  Transient error for {item.task} rep-{item.rep}, will retry")
                return "transient"
            print(f"  Launch failed for {item.task} rep-{item.rep}: {result.stderr.strip()}")
            return None

        # Parse instance ID from launch.sh output
        # launch.sh prints both "Instance:   c6i.xlarge" (summary) and
        # "  Instance: i-0abc123def (rep N)" (actual ID). Match i- prefix.
        for line in result.stdout.splitlines():
            if "Instance:" in line:
                parts = line.strip().split()
                idx = parts.index("Instance:")
                candidate = parts[idx + 1]
                if candidate.startswith("i-"):
                    return candidate

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


def find_completed_in_s3(run_id: str) -> set[tuple[str, int]]:
    """Check S3 for task/rep combos that have verifier reward.txt.

    We check for reward.txt rather than result.json because harbor writes
    result.json even when the agent times out and the verifier never runs.
    Without reward.txt, the rep has no score and must be re-run.
    """
    bucket = os.environ.get("HARBOR_S3_BUCKET", "harbor-eval-results-526275945504")
    cmd = [
        "aws", "s3", "ls", "--recursive",
        f"s3://{bucket}/runs/{run_id}/",
        "--region", AWS_REGION,
    ]
    try:
        result = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
        if result.returncode != 0:
            print(f"  Warning: S3 listing failed: {result.stderr.strip()}")
            return set()

        completed = set()
        for line in result.stdout.splitlines():
            if "reward.txt" not in line:
                continue
            # Path: runs/RUN_ID/rep-N/JOB_NAME/TASK__HASH/verifier/reward.txt
            path = line.split()[-1]
            parts = path.split("/")
            # Find rep-N
            rep_part = next((p for p in parts if p.startswith("rep-")), None)
            if not rep_part:
                continue
            rep = int(rep_part.split("-")[1])
            # Task name is two dirs up from reward.txt (TASK__HASH/verifier/reward.txt)
            task_dir = parts[-3]  # e.g., "chess-best-move__aBcDeF1"
            task = task_dir.rsplit("__", 1)[0] if "__" in task_dir else task_dir
            completed.add((task, rep))
        return completed

    except subprocess.TimeoutExpired:
        print("  Warning: S3 listing timed out")
        return set()


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
    total_full = len(queue)

    if args.backfill:
        print(f"Backfill mode: checking S3 for existing results...")
        completed = find_completed_in_s3(args.run_id)
        before = len(queue)
        queue = [item for item in queue if (item.task, item.rep) not in completed]
        skipped = before - len(queue)
        print(f"  Found {len(completed)} completed, skipping {skipped}, {len(queue)} remaining")
        if not queue:
            print("Nothing to backfill — all task/rep combos have results.")
            return
        print()

    total = len(queue)

    vcpu_per = VCPU_MAP.get(args.instance_type, 4)
    max_concurrent = args.max_vcpu // vcpu_per

    print(f"=== Wave launcher ===")
    print(f"Work items:     {total} ({total_full} total, {total_full - total} already done)")
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
    # Retry transient errors (quota/capacity) up to 5 times with backoff —
    # "transient" returns are NOT real instance IDs and must not be stored
    # in active. Permanent failures (None) abort the launcher.
    if pending:
        item = pending.pop(0)
        item.attempts += 1
        label = "uploads tarball" if args.agent_dir else "first instance"
        print(f"Launching {item.task} rep-{item.rep} (1/{total}, {label})...")
        max_first_launch_retries = 5
        instance_id = None
        for first_retry in range(max_first_launch_retries):
            instance_id = launch_one(
                item, args.run_id, args.agent_dir, args.model,
                args.instance_type, args.concurrency, args.harbor_dir, args.agent_kwargs,
                args.agent_name, args.agent_import_path, args.on_demand,
            )
            if instance_id and instance_id != "transient":
                break
            if instance_id == "transient":
                wait_s = 15 * (first_retry + 1)
                print(f"  First-launch transient — waiting {wait_s}s before retry {first_retry + 2}/{max_first_launch_retries}")
                time.sleep(wait_s)
                continue
            # instance_id is None — permanent failure
            break
        if instance_id and instance_id != "transient":
            item.status = "launched"
            item.instance_id = instance_id
            item.launched_at = time.time()
            active[instance_id] = item
        else:
            item.status = "pending"
            pending.insert(0, item)
            if instance_id == "transient":
                print(f"First launch failed — {max_first_launch_retries} transient retries exhausted. Quota/capacity issue.")
            else:
                print("First launch failed — cannot continue without tarball upload.")
            sys.exit(1)

    # Launch rest of initial wave with stagger
    while len(active) < max_concurrent and pending:
        item = pending.pop(0)
        instance_id = launch_one(
            item, args.run_id, args.agent_dir, args.model,
            args.instance_type, args.concurrency, args.harbor_dir, args.agent_kwargs,
            args.agent_name, args.agent_import_path, args.on_demand,
        )
        if instance_id and instance_id != "transient":
            item.attempts += 1
            item.status = "launched"
            item.instance_id = instance_id
            item.launched_at = time.time()
            active[instance_id] = item
            launched_total = done + len(active)
            print(f"  Launched {item.task} rep-{item.rep} ({launched_total}/{total}) -> {instance_id}")
        elif instance_id == "transient":
            # Transient error — requeue without counting attempt
            item.status = "pending"
            pending.append(item)
            break  # Stop initial wave — quota likely full
        else:
            item.attempts += 1
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
            instance_id = launch_one(
                item, args.run_id, args.agent_dir, args.model,
                args.instance_type, args.concurrency, args.harbor_dir, args.agent_kwargs,
                args.agent_name, args.agent_import_path, args.on_demand,
            )
            if instance_id and instance_id != "transient":
                item.attempts += 1
                item.status = "launched"
                item.instance_id = instance_id
                item.launched_at = time.time()
                active[instance_id] = item
                launched_this_cycle += 1
            elif instance_id == "transient":
                # Transient error — requeue without counting attempt
                item.status = "pending"
                pending.append(item)
                break  # Stop backfilling — quota likely full, wait for next cycle
            else:
                item.attempts += 1
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
