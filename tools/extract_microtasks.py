"""Extract microtasks from failed eval trajectories for prompt replay testing.

Scans an eval run directory, categorizes failures, and extracts the conversation
state at the key decision point into microtask JSON files. Each microtask contains
the full conversation history in OpenAI Responses API `input` format, ready for
replay with different system prompts.

Usage:
    python3 extract_microtasks.py /tmp/h20-full/ --output tools/microtasks/
    python3 extract_microtasks.py /tmp/h20-full/ --category NEVER_SUBMITTED --output tools/microtasks/
    python3 extract_microtasks.py /tmp/h20-full/ --task build-cython-ext --output tools/microtasks/
    python3 extract_microtasks.py /tmp/h20-full/ --list  # just list failures
"""

import argparse
import json
import logging
import os
import sys
from datetime import datetime
from pathlib import Path

logger = logging.getLogger(__name__)

# Failure categories
TIMEOUT = "TIMEOUT"
NEVER_SUBMITTED = "NEVER_SUBMITTED"
EARLY_QUIT = "EARLY_QUIT"
SUBMITTED_WRONG = "SUBMITTED_WRONG"

TIMEOUT_THRESHOLD_S = 850  # 14m 10s — tasks have 15m wallclock
EARLY_QUIT_MAX_STEPS = 5


def _parse_duration(result_json):
    """Extract agent execution duration in seconds from result.json."""
    ae = result_json.get("agent_execution", {})
    started = ae.get("started_at")
    finished = ae.get("finished_at")
    if not started or not finished:
        return None
    try:
        start = datetime.fromisoformat(started.rstrip("Z"))
        end = datetime.fromisoformat(finished.rstrip("Z"))
        return (end - start).total_seconds()
    except (ValueError, TypeError):
        return None


def _categorize_failure(steps, duration):
    """Categorize a failed trial based on its trajectory and timing.

    Returns (category, description) tuple.
    """
    n_steps = len(steps)

    if duration is not None and duration > TIMEOUT_THRESHOLD_S:
        return TIMEOUT, f"Timed out at {duration:.0f}s with {n_steps} steps"

    if n_steps <= EARLY_QUIT_MAX_STEPS:
        return EARLY_QUIT, f"Quit after only {n_steps} steps"

    # Check if agent ever called a submission-like tool
    # (lace doesn't have 'communicate' — the agent just says "Done" as text)
    return NEVER_SUBMITTED, f"Completed {n_steps} steps without formal submission"


def _find_decision_point(steps, category):
    """Identify the key decision step where the agent went wrong.

    Returns (step_index, description) tuple. step_index is 0-based into the
    steps array and represents the last step to include in the microtask input.
    The replay should produce the NEXT action.
    """
    if category == EARLY_QUIT:
        # The first agent step — why did it give up immediately?
        for i, s in enumerate(steps):
            if s.get("source") == "agent":
                return i, "First agent action — agent quit immediately after this"
        # No agent steps at all — include everything up to the last non-agent step
        # so the model at least sees the task prompt
        last_non_agent = 0
        for i, s in enumerate(steps):
            if s.get("source") != "agent":
                last_non_agent = i
        return last_non_agent, "Agent never acted — showing full prompt context"

    if category == TIMEOUT:
        # Find the last non-repetitive agent step (before the loop)
        # Look for repeated tool calls with same function_name
        agent_steps = [(i, s) for i, s in enumerate(steps) if s.get("source") == "agent"]
        if len(agent_steps) >= 3:
            # Check if the last N steps are repetitive
            last_tools = []
            for _, s in agent_steps[-10:]:
                tools = [tc["function_name"] for tc in s.get("tool_calls", [])]
                last_tools.append(tuple(tools))

            # Find where repetition starts
            if len(last_tools) >= 3:
                for start_idx in range(len(last_tools) - 2):
                    pattern = last_tools[start_idx]
                    if all(t == pattern for t in last_tools[start_idx:]):
                        # Repetition starts at this point
                        abs_idx = agent_steps[-(len(last_tools) - start_idx)][0]
                        return abs_idx, "Start of repetitive loop — agent got stuck here"

        # Fallback: use the midpoint of agent steps
        mid = len(agent_steps) // 2
        return agent_steps[mid][0], "Midpoint of execution — agent eventually timed out"

    # NEVER_SUBMITTED: the last agent step with tool calls
    # The decision point is just before the agent's final "Done" message
    for i in range(len(steps) - 1, -1, -1):
        s = steps[i]
        if s.get("source") == "agent" and s.get("tool_calls"):
            return i, "Last tool call before agent declared done without submitting"

    # Fallback: second-to-last step
    return max(0, len(steps) - 2), "Near end of execution"


def _steps_to_responses_input(steps, up_to_index):
    """Convert ATIF steps to OpenAI Responses API input format.

    Converts steps[0..up_to_index] inclusive into the `input` array format
    used by the OpenAI Responses API.

    Returns list of input items.
    """
    items = []

    for step in steps[:up_to_index + 1]:
        source = step.get("source")
        message = step.get("message", "")
        tool_calls = step.get("tool_calls", [])
        observation = step.get("observation", {})

        if source == "system":
            if message:
                items.append({
                    "type": "message",
                    "role": "developer",
                    "content": message,
                })

        elif source == "user":
            if message:
                items.append({
                    "type": "message",
                    "role": "user",
                    "content": message,
                })

        elif source == "agent":
            # Agent message text (if any) goes as assistant message
            if message and message.strip():
                items.append({
                    "type": "message",
                    "role": "assistant",
                    "content": message,
                })

            # Each tool call becomes a function_call item
            for tc in tool_calls:
                args = tc.get("arguments", {})
                items.append({
                    "type": "function_call",
                    "name": tc.get("function_name", "unknown"),
                    "arguments": json.dumps(args) if isinstance(args, dict) else str(args),
                    "call_id": tc.get("tool_call_id", ""),
                })

            # Observations become function_call_output items
            for result in observation.get("results", []):
                items.append({
                    "type": "function_call_output",
                    "call_id": result.get("source_call_id", ""),
                    "output": result.get("content", ""),
                })

    return items


def _describe_next_action(steps, decision_index):
    """Describe what the agent actually did after the decision point."""
    if decision_index + 1 >= len(steps):
        return "Agent stopped (no more steps)"

    next_step = steps[decision_index + 1]
    source = next_step.get("source")
    message = next_step.get("message", "")[:200]
    tool_calls = next_step.get("tool_calls", [])

    if tool_calls:
        tc = tool_calls[0]
        args_str = json.dumps(tc.get("arguments", {}))[:150]
        return f"Called {tc['function_name']}({args_str})"
    elif message:
        return f"Said: {message}"
    else:
        return f"Step with source={source}, no tools or message"


def scan_eval_dir(eval_dir):
    """Scan an eval run directory and return info about each trial.

    Returns list of dicts with keys: trial_dir, task_name, trial_id, reward,
    duration, steps, trajectory_path, category, category_desc.
    """
    eval_dir = Path(eval_dir)
    trials = []

    for trial_name in sorted(os.listdir(eval_dir)):
        trial_path = eval_dir / trial_name
        if not trial_path.is_dir():
            continue

        # Parse task name and trial ID from directory name
        # Format: task-name__trialId
        parts = trial_name.rsplit("__", 1)
        if len(parts) != 2:
            logger.warning("Skipping %s: unexpected directory name format", trial_name)
            continue
        task_name, trial_id = parts

        # Read reward
        reward_path = trial_path / "verifier" / "reward.txt"
        if not reward_path.is_file():
            continue
        reward = reward_path.read_text().strip()
        if reward != "0":
            continue  # only care about failures

        # Read result.json for timing
        result_path = trial_path / "result.json"
        result_json = {}
        if result_path.is_file():
            try:
                result_json = json.loads(result_path.read_text())
            except json.JSONDecodeError:
                pass
        duration = _parse_duration(result_json)

        # Read trajectory
        trajectory_path = trial_path / "agent" / "trajectory.json"
        if not trajectory_path.is_file():
            logger.warning("No trajectory for %s", trial_name)
            continue
        try:
            trajectory = json.loads(trajectory_path.read_text())
        except json.JSONDecodeError:
            logger.warning("Bad trajectory JSON for %s", trial_name)
            continue

        steps = trajectory.get("steps", [])
        if not steps:
            continue

        category, category_desc = _categorize_failure(steps, duration)

        trials.append({
            "trial_dir": str(trial_path),
            "task_name": task_name,
            "trial_id": trial_id,
            "trial_name": trial_name,
            "reward": float(reward),
            "duration": duration,
            "n_steps": len(steps),
            "steps": steps,
            "trajectory": trajectory,
            "category": category,
            "category_desc": category_desc,
        })

    return trials


def extract_microtask(trial):
    """Extract a microtask from a failed trial.

    Returns a microtask dict ready for JSON serialization.
    """
    steps = trial["steps"]
    category = trial["category"]

    decision_idx, decision_desc = _find_decision_point(steps, category)
    input_items = _steps_to_responses_input(steps, decision_idx)
    actual_next = _describe_next_action(steps, decision_idx)

    # Get agent metadata from trajectory
    agent_info = trial["trajectory"].get("agent", {})
    model_name = agent_info.get("model_name", "")
    # Parse provider/model from "openai/gpt-5.2-codex" format
    if "/" in model_name:
        provider, model = model_name.split("/", 1)
    else:
        provider, model = "", model_name

    persona = agent_info.get("persona", "benchmark")

    microtask_id = f"{trial['task_name']}__{trial['trial_id']}__step{decision_idx + 1}"

    return {
        "id": microtask_id,
        "task_name": trial["task_name"],
        "trial_id": trial["trial_id"],
        "failure_category": category,
        "category_description": trial["category_desc"],
        "decision_step": decision_idx + 1,  # 1-indexed for display
        "total_steps": len(steps),
        "duration_seconds": trial["duration"],
        "input": input_items,
        "actual_next_action": actual_next,
        "decision_context": decision_desc,
        "original_persona": persona,
        "model": model,
        "provider": provider,
    }


def list_failures(trials):
    """Print a summary table of failures."""
    from collections import Counter

    by_category = Counter()
    by_task = {}

    for t in trials:
        by_category[t["category"]] += 1
        task = t["task_name"]
        if task not in by_task:
            by_task[task] = {"total": 0, "categories": Counter()}
        by_task[task]["total"] += 1
        by_task[task]["categories"][t["category"]] += 1

    print(f"\nTotal failures: {len(trials)}")
    print("\nBy category:")
    for cat, count in by_category.most_common():
        print(f"  {cat}: {count}")

    print(f"\nBy task ({len(by_task)} tasks):")
    for task in sorted(by_task.keys()):
        info = by_task[task]
        cats = ", ".join(f"{c}={n}" for c, n in info["categories"].most_common())
        print(f"  {task}: {info['total']} failures ({cats})")


def main():
    parser = argparse.ArgumentParser(
        description="Extract microtasks from failed eval trajectories"
    )
    parser.add_argument("eval_dir", help="Path to eval run directory")
    parser.add_argument("--output", "-o", default="tools/microtasks",
                        help="Output directory for microtask JSON files")
    parser.add_argument("--category", "-c", choices=[TIMEOUT, NEVER_SUBMITTED, EARLY_QUIT],
                        help="Filter to a specific failure category")
    parser.add_argument("--task", "-t", action="append",
                        help="Filter to specific task name(s) (repeatable)")
    parser.add_argument("--list", "-l", action="store_true",
                        help="Just list failures, don't extract")
    parser.add_argument("--verbose", "-v", action="store_true")

    args = parser.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(levelname)s: %(message)s",
    )

    trials = scan_eval_dir(args.eval_dir)
    logger.info("Found %d failed trials with trajectories", len(trials))

    # Apply filters
    if args.category:
        trials = [t for t in trials if t["category"] == args.category]
        logger.info("After category filter (%s): %d trials", args.category, len(trials))

    if args.task:
        task_set = set(args.task)
        trials = [t for t in trials if t["task_name"] in task_set]
        logger.info("After task filter: %d trials", len(trials))

    if args.list:
        list_failures(trials)
        return

    # Extract microtasks
    output_dir = Path(args.output)
    output_dir.mkdir(parents=True, exist_ok=True)

    extracted = 0
    for trial in trials:
        microtask = extract_microtask(trial)
        filename = f"{microtask['id']}.json"
        output_path = output_dir / filename

        with open(output_path, "w") as f:
            json.dump(microtask, f, indent=2)

        extracted += 1
        logger.debug("Wrote %s", output_path)

    logger.info("Extracted %d microtasks to %s", extracted, output_dir)

    # Print summary
    from collections import Counter
    cats = Counter(t["category"] for t in trials)
    print(f"\nExtracted {extracted} microtasks:")
    for cat, count in cats.most_common():
        print(f"  {cat}: {count}")


if __name__ == "__main__":
    main()
