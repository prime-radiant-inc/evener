"""Per-task metrics computed from transcripts."""

import json

from trajectory import build_trajectory


# Keys to look for the submitted value in SUBMIT tool call arguments,
# matching the order used by trajectory._summarize_by_args for SUBMIT.
_SUBMIT_VALUE_KEYS = ["result", "message", "output"]


def compute_task_stats(store, job_name, task_name):
    """Compute per-task metrics from transcripts.

    Returns a dict with keys:
        total_rounds, rounds_by_action, wasted_rounds,
        total_tokens_in, total_tokens_out, session_count,
        max_depth, first_submit_round, submitted_value, action_sequence

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

    return {
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
