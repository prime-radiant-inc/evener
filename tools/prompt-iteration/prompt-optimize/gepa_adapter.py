"""GEPA adapter for optimizing Qwen benchmark personas.

Wraps the harbor eval infrastructure to enable optimize_anything to evolve
benchmark persona prompts against terminal-bench tasks.

Usage:
    export OPENROUTER_API_KEY=<key>
    python3 tools/prompt-optimize/gepa_adapter.py

    # Or with custom config:
    python3 tools/prompt-optimize/gepa_adapter.py \
        --seed-persona tools/prompt-optimize/personas/iter-7-component-isolation.md \
        --max-evals 50 \
        --reflection-model openai/gpt-5.1 \
        --tasks openssl-selfsigned-cert,count-dataset-tokens,kv-store-grpc,build-cython-ext
"""

import argparse
import json
import os
import subprocess
import sys
import tempfile
import time
from pathlib import Path

# Unbuffered output
sys.stdout.reconfigure(line_buffering=True)
sys.stderr.reconfigure(line_buffering=True)

try:
    import gepa.optimize_anything as oa
    from gepa.optimize_anything import optimize_anything, GEPAConfig, EngineConfig
except ImportError:
    print("pip install gepa  (or run from ~/git/gepa)", file=sys.stderr)
    sys.exit(1)


# Remote eval infrastructure
REMOTE = os.environ.get("EVAL_REMOTE", "jesse@magic-kingdom")
LACE_DIR = "/home/jesse/git/terminal-bench/runs/lace-qwen-chainfix"
PERSONA_DEST = f"{LACE_DIR}/lace/packages/agent/config/agent-personas/benchmark-opt.md"
ENV_FILE = "/home/jesse/git/terminal-bench/.env"
MODEL = "openrouter/qwen/qwen3.5-flash-02-23"

# Mix of pass/fail tasks, all ≤10 minutes
DEFAULT_TASKS = [
    "kv-store-grpc",              # 1min, passed with iter-7
    "db-wal-recovery",            # 3min, failed
    "gcode-to-text",              # 3min, failed
    "count-dataset-tokens",       # 5min, nondeterministic
    "openssl-selfsigned-cert",    # 10min, reliably passes (regression canary)
    "build-cython-ext",           # 9min, reliably passes (regression canary)
    "largest-eigenval",           # 9min, passed with iter-7
    "regex-log",                  # ~10min, failed
]


def _ssh_cmd(cmd, timeout=120):
    """Run a command on magic-kingdom via SSH."""
    result = subprocess.run(
        ["ssh", REMOTE, "bash", "-s"],
        input=cmd,
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    return result.stdout.strip(), result.stderr.strip(), result.returncode


def _deploy_persona(persona_text):
    """Write persona text to a temp file and scp to magic-kingdom."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".md", delete=False) as f:
        f.write(persona_text)
        tmp_path = f.name

    try:
        result = subprocess.run(
            ["scp", tmp_path, f"{REMOTE}:{PERSONA_DEST}"],
            capture_output=True,
            text=True,
            timeout=30,
        )
        if result.returncode != 0:
            raise RuntimeError(f"scp failed: {result.stderr}")
    finally:
        os.unlink(tmp_path)


def _run_task(job_name, task):
    """Run a single task via harbor, blocking until completion."""
    launch_cmd = f"""
export PATH="$HOME/.local/bin:$PATH"
cd {LACE_DIR}
set -a; source {ENV_FILE}; set +a

harbor run \
    --dataset "terminal-bench@2.0" \
    --agent-import-path lace_agent:LaceAgent \
    --job-name {job_name} \
    --model {MODEL} \
    --ak persona=benchmark-opt \
    --agent-setup-timeout-multiplier 3.0 \
    -t {task} 2>&1
"""
    # Harbor blocks until task completes — use long timeout
    # 1800s (30 min) to handle Docker resource contention
    stdout, stderr, rc = _ssh_cmd(launch_cmd, timeout=1800)
    if rc != 0 and "already exists" in stdout + stderr:
        _ssh_cmd(f"sudo rm -rf {LACE_DIR}/jobs/{job_name}")
        stdout, stderr, rc = _ssh_cmd(launch_cmd, timeout=1800)

    return stdout, stderr, rc


def _wait_for_result(job_name, task, timeout_seconds=900):
    """Poll for task completion."""
    start = time.time()
    while time.time() - start < timeout_seconds:
        check_cmd = f"""
cd {LACE_DIR}/jobs/{job_name} 2>/dev/null || exit 1
for d in {task}__*/; do
    [ -d "$d" ] || continue
    if [ -f "$d/result.json" ]; then
        python3 -c "import json; r=json.load(open('${{d}}result.json')); print(json.dumps({{'reward': r.get('verifier_result',{{}}).get('rewards',{{}}).get('reward','?')}}))"
        exit 0
    fi
done
echo '{{"status": "running"}}'
"""
        stdout, _, _ = _ssh_cmd(check_cmd)
        try:
            data = json.loads(stdout)
            if "reward" in data:
                return float(data["reward"])
        except (json.JSONDecodeError, ValueError):
            pass
        time.sleep(15)

    return 0.0  # Timeout = fail


def _get_trajectory_narrative(job_name, task):
    """Get a narrative summary of the agent's trajectory for ASI.

    Copies events.jsonl locally, then uses lace_interrogate.py's narrative mode.
    """
    # Find and copy events.jsonl
    find_cmd = (
        f"find {LACE_DIR}/jobs/{job_name}/{task}__*/agent/agent-state/agent-sessions/ "
        f"-name events.jsonl 2>/dev/null | head -1"
    )
    events_path, _, _ = _ssh_cmd(find_cmd)
    if not events_path:
        return "No events found"

    # Copy to local temp
    with tempfile.NamedTemporaryFile(suffix=".jsonl", delete=False) as f:
        local_path = f.name

    try:
        subprocess.run(
            ["scp", f"{REMOTE}:{events_path}", local_path],
            capture_output=True, timeout=30,
        )
        # Use our existing narrative converter
        script_dir = Path(__file__).parent.parent
        sys.path.insert(0, str(script_dir))
        from lace_interrogate import _parse_events, _events_to_narrative

        events = _parse_events(local_path)
        narrative = _events_to_narrative(events)
        return narrative[:5000]
    except Exception as e:
        return f"Failed to get trajectory: {e}"
    finally:
        os.unlink(local_path)


def evaluate_persona(candidate, example):
    """GEPA evaluator: deploy persona, run task, return score + ASI."""
    persona_text = candidate["persona"]
    task = example["task"]
    job_name = f"gepa-{task}-{int(time.time())}"

    print(f"  [eval] {task} job={job_name}", flush=True)
    oa.log(f"Task: {task}")
    oa.log(f"Job: {job_name}")

    try:
        # Deploy persona
        _deploy_persona(persona_text)

        # Run task (blocks until harbor completes)
        stdout, stderr, rc = _run_task(job_name, task)

        # Get reward from result.json
        reward = _check_reward(job_name, task)
    except subprocess.TimeoutExpired:
        print(f"  [eval] {task} TIMEOUT", flush=True)
        oa.log("Task timed out (harbor or Docker overloaded)")
        reward = 0.0
    except Exception as e:
        print(f"  [eval] {task} ERROR: {e}", flush=True)
        oa.log(f"Error: {e}")
        reward = 0.0

    print(f"  [eval] {task} reward={reward}", flush=True)
    oa.log(f"Reward: {reward}")

    # Get trajectory for ASI on failures
    if reward < 1.0:
        try:
            trajectory = _get_trajectory_narrative(job_name, task)
            oa.log(f"Trajectory:\n{trajectory}")
        except Exception:
            oa.log("Failed to get trajectory")

    return reward


def _check_reward(job_name, task):
    """Check the reward for a completed task."""
    cmd = f"""
cd {LACE_DIR}/jobs/{job_name} 2>/dev/null || exit 1
for d in {task}__*/; do
    [ -d "$d" ] || continue
    if [ -f "$d/result.json" ]; then
        python3 -c "import json; r=json.load(open('${{d}}result.json')); print(r.get('verifier_result',{{}}).get('rewards',{{}}).get('reward', 0.0))"
        exit 0
    fi
done
echo "0.0"
"""
    stdout, _, _ = _ssh_cmd(cmd)
    try:
        return float(stdout.strip())
    except ValueError:
        return 0.0


def main():
    parser = argparse.ArgumentParser(description="GEPA prompt optimization for Qwen benchmark")
    parser.add_argument("--seed-persona", type=str,
                        default="tools/prompt-optimize/personas/iter-7-component-isolation.md",
                        help="Path to seed persona .md file")
    parser.add_argument("--max-evals", type=int, default=30,
                        help="Maximum evaluator calls (default: 30)")
    parser.add_argument("--reflection-model", type=str, default="openai/gpt-5.1",
                        help="Model for GEPA reflection (default: gpt-5.1)")
    parser.add_argument("--tasks", type=str,
                        default=",".join(DEFAULT_TASKS),
                        help="Comma-separated task names")
    parser.add_argument("--run-dir", type=str, default="tools/prompt-optimize/gepa-runs",
                        help="Directory for GEPA logs and artifacts")
    args = parser.parse_args()

    # Load seed persona
    seed_text = Path(args.seed_persona).read_text()
    seed_candidate = {"persona": seed_text}

    # Build dataset (one example per task)
    tasks = args.tasks.split(",")
    dataset = [{"task": t.strip()} for t in tasks]

    print(f"Seed persona: {args.seed_persona} ({len(seed_text)} chars)", flush=True)
    print(f"Tasks: {tasks}", flush=True)
    print(f"Max evals: {args.max_evals}", flush=True)
    print(f"Reflection model: {args.reflection_model}", flush=True)
    print(f"Run dir: {args.run_dir}", flush=True)
    print(flush=True)

    try:
        from gepa.optimize_anything import ReflectionConfig
    except ImportError:
        ReflectionConfig = None

    config_kwargs = {
        "engine": EngineConfig(
            max_metric_calls=args.max_evals,
            parallel=False,  # Tasks are sequential (share one machine)
            run_dir=args.run_dir,
            capture_stdio=True,
        ),
    }
    if ReflectionConfig:
        config_kwargs["reflection"] = ReflectionConfig(
            reflection_lm=args.reflection_model,
        )

    config = GEPAConfig(**config_kwargs)

    result = optimize_anything(
        seed_candidate=seed_candidate,
        evaluator=evaluate_persona,
        dataset=dataset,
        objective=(
            "Optimize the agent persona prompt to improve outcomes across all user-specified "
            "tasks. The prompt must generalize — do NOT add task-specific guidance, examples, "
            "or workarounds for individual tasks. Changes should be general principles that "
            "improve the agent's decision-making process. Adding text dilutes existing working "
            "instructions, so prefer editing existing sections over adding new ones."
        ),
        config=config,
    )

    # Output results
    print("\n=== GEPA Optimization Complete ===")
    print(f"Total metric calls: {result.total_metric_calls}")
    print(f"Number of candidates: {result.num_candidates}")
    best_scores = result.val_aggregate_scores
    if best_scores:
        print(f"Best aggregate score: {max(best_scores)}")
    print(f"Best candidate persona length: {len(result.best_candidate['persona'])} chars")

    # Save best persona
    output_path = Path(args.run_dir) / "best_persona.md"
    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_text(result.best_candidate["persona"])
    print(f"Saved to: {output_path}")


if __name__ == "__main__":
    main()
