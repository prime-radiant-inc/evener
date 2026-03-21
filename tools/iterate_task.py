#!/usr/bin/env python3
"""Single-task iteration tool for prompt experiments.

Wraps run_eval.py to provide a tight iterate-diagnose-fix loop for individual tasks.
Launches a single-task run, extracts a structured diagnostic report, and maintains
an iteration log.

Examples:
    # Run a task and get a report
    ./iterate_task.py run --task chess-best-move --note "baseline"

    # Run with a prompt overlay (appended to system prompt)
    ./iterate_task.py run --task chess-best-move --prompt experiments/v2.md --note "verify fix"

    # Re-examine a completed job
    ./iterate_task.py report --job fix-chess-best-move-iter1

    # View iteration history
    ./iterate_task.py log --task chess-best-move
"""

import argparse
import hashlib
import json
import os
import subprocess
import sys
import time

SCRIPT_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(SCRIPT_DIR)
LOG_DIR = os.path.join(SCRIPT_DIR, "iteration-logs")

sys.path.insert(0, SCRIPT_DIR)
from eval_lib import REMOTE, DEFAULT_JOBS_DIR, DEFAULT_MODEL


def ssh(cmd: str, check=True) -> str:
    r = subprocess.run(
        ["ssh", REMOTE, cmd], capture_output=True, text=True, timeout=30,
    )
    if check and r.returncode != 0:
        return ""
    return r.stdout.strip()


def find_trial_dir(job_name: str, task: str) -> str:
    """Find the trial directory for a task within a job."""
    out = ssh(f"ls -d {DEFAULT_JOBS_DIR}/{job_name}/{task}__* 2>/dev/null | head -1")
    return out


def poll_until_done(job_name: str, task: str, timeout_s: int = 1200) -> str:
    """Poll until reward.txt exists or timeout."""
    start = time.time()
    while time.time() - start < timeout_s:
        trial_dir = find_trial_dir(job_name, task)
        if trial_dir:
            reward = ssh(f"cat {trial_dir}/verifier/reward.txt {trial_dir}/reward.txt 2>/dev/null | head -1")
            if reward:
                return reward
            # Check if result.json has exception
            exc = ssh(f"python3 -c \"import json; r=json.load(open('{trial_dir}/result.json')); e=r.get('exception_info'); print(e['type'] if e else '')\" 2>/dev/null")
            if exc:
                return f"EXCEPTION:{exc}"
        # Check if harbor process is still running
        proc = ssh(f"pgrep -f 'job-name {job_name}' >/dev/null 2>&1 && echo running || echo done")
        if proc == "done" and not find_trial_dir(job_name, task):
            return "INFRASTRUCTURE_FAILURE"
        time.sleep(30)
        elapsed = int(time.time() - start)
        print(f"  ... waiting ({elapsed}s)", file=sys.stderr)
    return "TIMEOUT"


def extract_delegation(trial_dir: str) -> dict:
    """Parse transcript to extract delegation pattern and coordinator behavior."""
    raw = ssh(f"cat {trial_dir}/agent/agent-state/sessions/*.transcript.jsonl 2>/dev/null")
    if not raw:
        return {"spawns": [], "shell_cmds": 0, "file_writes": 0, "first_action": "unknown",
                "verify_after_implement": False, "fix_agents": 0, "coordinator_actions": []}

    spawns = []
    shell_cmds = 0
    file_writes = 0
    first_action = None
    saw_implementer = False
    shells_after_implementer = 0
    fix_agents = 0
    coordinator_actions = []

    for line in raw.splitlines():
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        if entry.get("kind") != "entry":
            continue
        turn = entry.get("turn", {})
        msg = turn.get("message", {})
        for block in msg.get("content", []):
            if not isinstance(block, dict) or block.get("kind") != "tool_call":
                continue
            tc = block["tool_call"]
            args = tc.get("arguments", {})
            if isinstance(args, str):
                try:
                    args = json.loads(args)
                except (json.JSONDecodeError, TypeError):
                    args = {}
            name = tc.get("name", "")

            if first_action is None and name not in ("task_list",):
                first_action = name

            if name == "spawn_agent":
                atype = args.get("agent_type", "default")
                spawns.append(atype)
                coordinator_actions.append(f"spawn:{atype}")
                if atype == "implementer" and saw_implementer:
                    fix_agents += 1
                if atype == "implementer":
                    saw_implementer = True
            elif name in ("write_file", "apply_patch"):
                file_writes += 1
                coordinator_actions.append(f"write:{name}")
            elif name == "exec_command":
                shell_cmds += 1
                cmd = args.get("command", "")[:80]
                coordinator_actions.append(f"shell:{cmd}")
                if saw_implementer:
                    shells_after_implementer += 1
            elif name == "communicate":
                coordinator_actions.append("communicate")

    return {
        "spawns": spawns,
        "shell_cmds": shell_cmds,
        "file_writes": file_writes,
        "first_action": first_action or "none",
        "verify_after_implement": shells_after_implementer > 0,
        "fix_agents": fix_agents,
        "coordinator_actions": coordinator_actions,
    }


def extract_verifier(trial_dir: str) -> dict:
    """Extract verifier test results."""
    raw = ssh(f"cat {trial_dir}/verifier/test-stdout.txt 2>/dev/null")
    if not raw:
        return {"passed": 0, "failed": 0, "total": 0, "failures": [], "raw_tail": ""}

    lines = raw.splitlines()
    passed = 0
    failed = 0
    failures = []

    for line in lines:
        if line.startswith("PASSED "):
            passed += 1
        elif line.startswith("FAILED "):
            failures.append(line.replace("FAILED ", "").strip())
            failed += 1

    # Get last 20 lines for context
    raw_tail = "\n".join(lines[-20:])

    return {
        "passed": passed,
        "failed": failed,
        "total": passed + failed,
        "failures": failures,
        "raw_tail": raw_tail,
    }


def extract_tokens(trial_dir: str) -> dict:
    """Extract token usage from result.json."""
    raw = ssh(f"cat {trial_dir}/result.json 2>/dev/null")
    if not raw:
        return {"prompt": 0, "completion": 0, "cached": 0}
    try:
        result = json.loads(raw)
        ar = result.get("agent_result", {})
        return {
            "prompt": ar.get("n_input_tokens", 0),
            "completion": ar.get("n_output_tokens", 0),
            "cached": ar.get("n_cache_tokens", 0),
        }
    except json.JSONDecodeError:
        return {"prompt": 0, "completion": 0, "cached": 0}


def extract_duration(trial_dir: str) -> int:
    """Extract agent execution duration in seconds."""
    raw = ssh(f"cat {trial_dir}/result.json 2>/dev/null")
    if not raw:
        return 0
    try:
        result = json.loads(raw)
        ae = result.get("agent_execution", {})
        start = ae.get("started_at", "")
        end = ae.get("finished_at", "")
        if start and end:
            from datetime import datetime
            fmt = "%Y-%m-%dT%H:%M:%S.%fZ"
            try:
                return int((datetime.strptime(end[:26] + "Z", fmt) - datetime.strptime(start[:26] + "Z", fmt)).total_seconds())
            except ValueError:
                return 0
    except json.JSONDecodeError:
        pass
    return 0


def print_report(task: str, job_name: str):
    """Print a structured diagnostic report."""
    trial_dir = find_trial_dir(job_name, task)
    if not trial_dir:
        print(f"No trial directory found for {task} in {job_name}")
        return

    reward = ssh(f"cat {trial_dir}/verifier/reward.txt {trial_dir}/reward.txt 2>/dev/null | head -1")
    result = "PASS" if reward in ("1", "1.0") else "FAIL"
    duration = extract_duration(trial_dir)
    deleg = extract_delegation(trial_dir)
    verifier = extract_verifier(trial_dir)
    tokens = extract_tokens(trial_dir)

    print(f"\n{'='*60}")
    print(f"Task: {task}")
    print(f"Result: {result} (reward: {reward or 'none'})")
    print(f"Duration: {duration // 60}m {duration % 60}s")
    print(f"Job: {job_name}")
    print(f"{'='*60}")

    print(f"\n--- Delegation ---")
    print(f"  Spawns: {' -> '.join(deleg['spawns']) or 'none'}")
    print(f"  First action: {deleg['first_action']}")
    print(f"  Coordinator shell cmds: {deleg['shell_cmds']}")
    print(f"  Coordinator file writes: {deleg['file_writes']}")
    print(f"  Fix agents spawned: {deleg['fix_agents']}")
    print(f"  Verified after implement: {deleg['verify_after_implement']}")

    delegated = any(s == "implementer" for s in deleg["spawns"])
    if not delegated:
        print(f"  *** DELEGATION FAILURE: no implementer spawned ***")
    if deleg["file_writes"] > 0:
        print(f"  *** COORDINATOR WROTE FILES DIRECTLY ({deleg['file_writes']}x) ***")

    print(f"\n--- Verifier ({verifier['passed']}/{verifier['total']}) ---")
    if verifier["failures"]:
        for f in verifier["failures"]:
            print(f"  FAIL: {f}")
    elif verifier["total"] == 0:
        print(f"  (no verifier output)")
    else:
        print(f"  All tests passed")

    if verifier["raw_tail"] and result == "FAIL":
        print(f"\n--- Verifier Detail ---")
        for line in verifier["raw_tail"].splitlines()[-15:]:
            print(f"  {line}")

    print(f"\n--- Tokens ---")
    print(f"  Prompt: {tokens.get('prompt') or 0:,}  Completion: {tokens.get('completion') or 0:,}  Cached: {tokens.get('cached') or 0:,}")

    print(f"\n--- Coordinator Timeline ---")
    for action in deleg["coordinator_actions"][:30]:
        print(f"  {action}")
    if len(deleg["coordinator_actions"]) > 30:
        print(f"  ... ({len(deleg['coordinator_actions']) - 30} more)")
    print()


def iteration_count(task: str) -> int:
    """Get the next iteration number for a task."""
    log_path = os.path.join(LOG_DIR, f"{task}.jsonl")
    if not os.path.exists(log_path):
        return 1
    with open(log_path) as f:
        return sum(1 for _ in f) + 1


def append_log(task: str, entry: dict):
    """Append an entry to the iteration log."""
    os.makedirs(LOG_DIR, exist_ok=True)
    log_path = os.path.join(LOG_DIR, f"{task}.jsonl")
    with open(log_path, "a") as f:
        f.write(json.dumps(entry) + "\n")


def cmd_run(args):
    """Run a single task and produce a report."""
    task = args.task
    model = args.model
    iteration = iteration_count(task)
    job_name = f"fix-{task}-iter{iteration}"

    print(f"=== Iteration {iteration}: {task} ===")
    if args.note:
        print(f"Note: {args.note}")

    # Hash prompt file if provided
    prompt_sha = ""
    if args.prompt:
        with open(args.prompt, "rb") as f:
            prompt_sha = hashlib.sha256(f.read()).hexdigest()[:12]
        print(f"Prompt: {args.prompt} ({prompt_sha})")

    # Build launch command
    launch_args = [
        sys.executable, os.path.join(SCRIPT_DIR, "run_eval.py"), "launch",
        "--task", task,
        "--reps", "1",
        "--model", model,
        "--job", job_name,
        "--force",
        "--allow-dirty",
    ]
    if args.prompt:
        launch_args.extend(["--ak", f"system_prompt_append={os.path.abspath(args.prompt)}"])
    if args.no_build:
        launch_args.append("--no-build")

    print(f"\nLaunching {job_name}...")
    r = subprocess.run(launch_args, cwd=REPO_ROOT)
    if r.returncode != 0:
        print("Launch failed!", file=sys.stderr)
        sys.exit(1)

    print(f"\nPolling for completion...")
    reward = poll_until_done(job_name, task, timeout_s=args.timeout)

    # Extract report
    print_report(task, job_name)

    # Log the iteration
    trial_dir = find_trial_dir(job_name, task)
    verifier = extract_verifier(trial_dir) if trial_dir else {}
    tokens = extract_tokens(trial_dir) if trial_dir else {}
    duration = extract_duration(trial_dir) if trial_dir else 0

    entry = {
        "iteration": iteration,
        "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "job_name": job_name,
        "prompt_file": args.prompt or "(default)",
        "prompt_sha256": prompt_sha,
        "note": args.note or "",
        "result": "PASS" if reward in ("1", "1.0") else "FAIL",
        "reward": reward,
        "duration_s": duration,
        "tests_passed": verifier.get("passed", 0),
        "tests_failed": verifier.get("failed", 0),
        "failure_summary": "; ".join(verifier.get("failures", [])[:3]),
        "tokens_prompt": tokens.get("prompt", 0),
        "tokens_completion": tokens.get("completion", 0),
        "model": model,
    }
    append_log(task, entry)
    print(f"Logged to {LOG_DIR}/{task}.jsonl")


def cmd_report(args):
    """Print report for an existing job."""
    # Infer task from job name if not provided
    task = args.task
    if not task:
        # Try to extract from job name like fix-TASK-iterN
        parts = args.job.split("-")
        if parts[0] == "fix" and parts[-1].startswith("iter"):
            task = "-".join(parts[1:-1])
    if not task:
        print("Cannot infer task from job name. Use --task.", file=sys.stderr)
        sys.exit(1)
    print_report(task, args.job)


def cmd_log(args):
    """Print iteration history for a task."""
    log_path = os.path.join(LOG_DIR, f"{args.task}.jsonl")
    if not os.path.exists(log_path):
        print(f"No iteration history for {args.task}")
        return

    entries = []
    with open(log_path) as f:
        for line in f:
            entries.append(json.loads(line))

    print(f"\n=== Iteration History: {args.task} ===\n")
    print(f" {'#':>2} | {'Result':6} | {'Tests':5} | {'Dur':>5} | {'Tokens':>8} | {'Note'}")
    print(f"----+--------+-------+-------+----------+------")
    for e in entries:
        n = e["iteration"]
        result = e["result"]
        tests = f"{e['tests_passed']}/{e['tests_passed'] + e['tests_failed']}"
        dur = f"{e['duration_s'] // 60}m{e['duration_s'] % 60:02d}"
        tok = f"{e['tokens_prompt']:,}"
        note = e["note"][:40]
        print(f" {n:>2} | {result:6} | {tests:5} | {dur:>5} | {tok:>8} | {note}")
    print()


def main():
    parser = argparse.ArgumentParser(description="Single-task prompt iteration tool")
    sub = parser.add_subparsers(dest="command", required=True)

    run_p = sub.add_parser("run", help="Run a task and get a diagnostic report")
    run_p.add_argument("--task", required=True, help="Task name")
    run_p.add_argument("--prompt", help="Prompt file to append to system prompt")
    run_p.add_argument("--model", default="openai/gpt-5.4", help="Model (default: gpt-5.4)")
    run_p.add_argument("--note", help="Description of what changed")
    run_p.add_argument("--no-build", action="store_true", help="Skip binary build")
    run_p.add_argument("--timeout", type=int, default=1200, help="Max wait seconds (default: 1200)")
    run_p.set_defaults(func=cmd_run)

    report_p = sub.add_parser("report", help="Print report for an existing job")
    report_p.add_argument("--job", required=True, help="Job name")
    report_p.add_argument("--task", help="Task name (inferred from job name if omitted)")
    report_p.set_defaults(func=cmd_report)

    log_p = sub.add_parser("log", help="Print iteration history for a task")
    log_p.add_argument("--task", required=True, help="Task name")
    log_p.set_defaults(func=cmd_log)

    args = parser.parse_args()
    args.func(args)


if __name__ == "__main__":
    main()
