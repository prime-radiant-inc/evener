"""Per-task metrics computed from transcripts."""

import json
from datetime import datetime
from pathlib import Path

from trajectory import build_trajectory


# Keys to look for the submitted value in SUBMIT tool call arguments,
# matching the order used by trajectory._summarize_by_args for SUBMIT.
_SUBMIT_VALUE_KEYS = ["result", "message", "output"]


def compute_task_stats(store, job_name, task_name):
    """Compute per-task metrics from transcripts, result.json, and api.jsonl.

    Returns a dict with keys:
        total_rounds, rounds_by_action, wasted_rounds,
        total_tokens_in, total_tokens_out, session_count,
        max_depth, first_submit_round, submitted_value, action_sequence,
        wall_time_sec, api_call_count, total_latency_ms, avg_latency_ms,
        empty_response_count

    Returns None if the task is not found.
    """
    task = store.get_task(job_name, task_name)
    if task is None:
        return None

    transcript_files = task.get("transcript_files", [])
    sessions = store.load_transcripts(transcript_files)
    roots = store.build_session_tree(sessions)

    total_rounds = 0
    rounds_by_action = {}
    wasted_rounds = 0
    total_tokens_in = 0
    total_tokens_out = 0
    first_submit_round = 0
    submitted_value = ""
    max_depth = 0

    # Walk all sessions to accumulate metrics
    all_sessions = _flatten_tree(roots)
    for session in all_sessions:
        depth = session.get("depth", 0)
        if depth > max_depth:
            max_depth = depth

        trajectory = build_trajectory(session)
        for rnd in trajectory:
            total_rounds += 1
            action = rnd["action"]
            rounds_by_action[action] = rounds_by_action.get(action, 0) + 1

            if action == "ERROR":
                wasted_rounds += 1

            usage = rnd.get("usage", {})
            total_tokens_in += usage.get("input_tokens", 0)
            total_tokens_out += usage.get("output_tokens", 0)

            if action == "SUBMIT" and first_submit_round == 0:
                first_submit_round = rnd["round"]
                submitted_value = _extract_submitted_value(rnd)

    # Action sequence: root sessions only
    action_sequence = []
    for root in roots:
        trajectory = build_trajectory(root)
        for rnd in trajectory:
            action_sequence.append(rnd["action"])

    # Wall time and API metrics from task directory
    task_dir = task.get("task_dir")
    wall_time = _compute_wall_time(task_dir) if task_dir else None
    api_stats = _compute_api_stats(task_dir) if task_dir else {}

    result = {
        "total_rounds": total_rounds,
        "rounds_by_action": rounds_by_action,
        "wasted_rounds": wasted_rounds,
        "total_tokens_in": total_tokens_in,
        "total_tokens_out": total_tokens_out,
        "session_count": len(sessions),
        "max_depth": max_depth,
        "first_submit_round": first_submit_round,
        "submitted_value": submitted_value,
        "action_sequence": action_sequence,
        "wall_time_sec": wall_time,
    }
    result.update(api_stats)
    return result


def _compute_wall_time(task_dir_path):
    """Compute wall time in seconds from result.json timestamps.

    Returns float seconds or None if timestamps are missing or unparseable.
    """
    result_file = Path(task_dir_path) / "result.json"
    if not result_file.is_file():
        return None
    try:
        data = json.loads(result_file.read_text())
    except (json.JSONDecodeError, OSError):
        return None

    started = data.get("started_at")
    finished = data.get("finished_at")
    if not started or not finished:
        return None

    try:
        t_start = datetime.fromisoformat(started)
        t_end = datetime.fromisoformat(finished)
        return (t_end - t_start).total_seconds()
    except (ValueError, TypeError):
        return None


def _compute_api_stats(task_dir_path):
    """Compute API metrics from api.jsonl.

    Returns a dict with api_call_count, total_latency_ms, avg_latency_ms,
    and empty_response_count. All values are None if api.jsonl doesn't exist.
    """
    api_file = Path(task_dir_path) / "agent" / "serf-state" / "api.jsonl"
    if not api_file.is_file():
        return {
            "api_call_count": None,
            "total_latency_ms": None,
            "avg_latency_ms": None,
            "empty_response_count": None,
        }

    entries = []
    try:
        for line in api_file.read_text().splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                entries.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    except OSError:
        return {
            "api_call_count": None,
            "total_latency_ms": None,
            "avg_latency_ms": None,
            "empty_response_count": None,
        }

    count = len(entries)
    total_latency = sum(e.get("latency_ms", 0) for e in entries)
    avg_latency = total_latency / count if count > 0 else 0.0
    empty_count = sum(
        1 for e in entries
        if e.get("response", {}).get("text_length", -1) == 0
        and e.get("response", {}).get("tool_call_count", -1) == 0
    )

    return {
        "api_call_count": count,
        "total_latency_ms": total_latency,
        "avg_latency_ms": avg_latency,
        "empty_response_count": empty_count,
    }


def _flatten_tree(roots):
    """Flatten a session tree into a list via depth-first traversal."""
    result = []
    stack = list(roots)
    while stack:
        node = stack.pop()
        result.append(node)
        # Push children in reverse so they come out in order
        for child in reversed(node.get("children", [])):
            stack.append(child)
    return result


def _extract_submitted_value(rnd):
    """Extract the submitted value from a SUBMIT round's first matching tool call."""
    for tc in rnd.get("tool_calls", []):
        args = tc.get("arguments", {})
        if isinstance(args, str):
            try:
                args = json.loads(args)
            except (json.JSONDecodeError, TypeError):
                args = {}
        if not isinstance(args, dict):
            continue
        for key in _SUBMIT_VALUE_KEYS:
            val = args.get(key, "")
            if val:
                return str(val)
    return ""
