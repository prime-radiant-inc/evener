"""Parse transcripts into high-level trajectory timelines."""

import json


# Tool name -> action category mapping
_TOOL_CATEGORIES = {
    # EXPLORE
    "read_file": "EXPLORE",
    "glob": "EXPLORE",
    "grep": "EXPLORE",
    # EDIT
    "apply_patch": "EDIT",
    "edit_file": "EDIT",
    "write_file": "EDIT",
    # EXEC
    "shell": "EXEC",
    # SPAWN
    "spawn_agent": "SPAWN",
    # SUBMIT
    "communicate": "SUBMIT",
    "submit_result": "SUBMIT",
}

# Priority order for mixed-tool rounds (highest first)
_PRIORITY = ["SUBMIT", "SPAWN", "EDIT", "EXEC", "EXPLORE", "PLAN"]


def classify_round(tool_names, has_text):
    """Classify a round by its dominant tool type.

    Args:
        tool_names: list of tool names called in this round.
        has_text: whether the assistant produced text output.

    Returns:
        Action string: EXPLORE, EDIT, EXEC, SPAWN, SUBMIT, PLAN, or ERROR.
    """
    if not tool_names:
        return "PLAN" if has_text else "ERROR"

    categories = set()
    for name in tool_names:
        cat = _TOOL_CATEGORIES.get(name)
        if cat:
            categories.add(cat)

    if not categories:
        # Tools present but none recognized -- fall back to text check
        return "PLAN" if has_text else "ERROR"

    # Return highest-priority category
    for cat in _PRIORITY:
        if cat in categories:
            return cat

    return "PLAN" if has_text else "ERROR"


def build_trajectory(session):
    """Parse session entries into a list of round dicts.

    Each round = one ASSISTANT entry + its following TOOL_RESULTS.
    USER_INPUT and STEERING entries are skipped.

    Returns list of dicts with keys:
        round, action, summary, tool_calls, tool_results, text, usage, raw_entries
    """
    entries = session.get("entries", [])
    rounds = []
    round_num = 0
    i = 0

    while i < len(entries):
        entry = entries[i]
        turn = entry.get("turn", {})
        kind = turn.get("kind", "")

        # Skip non-ASSISTANT entries
        if kind != "ASSISTANT":
            i += 1
            continue

        round_num += 1
        raw_entries = [entry]
        assistant_msg = turn.get("message", {})
        usage = turn.get("usage", {})

        # Extract text and tool calls from assistant content
        text_parts = []
        tool_calls = []
        for item in assistant_msg.get("content", []):
            if item.get("kind") == "text":
                text_parts.append(item.get("text", ""))
            elif item.get("kind") == "tool_call":
                tc = item.get("tool_call", {})
                tool_calls.append(tc)

        text = "\n".join(text_parts)

        # Look for following TOOL_RESULTS
        tool_results = []
        if i + 1 < len(entries):
            next_entry = entries[i + 1]
            next_turn = next_entry.get("turn", {})
            if next_turn.get("kind") == "TOOL_RESULTS":
                raw_entries.append(next_entry)
                next_msg = next_turn.get("message", {})
                for item in next_msg.get("content", []):
                    if item.get("kind") == "tool_result":
                        tr = item.get("tool_result", {})
                        tool_results.append(tr)
                i += 1  # consume the TOOL_RESULTS entry

        tool_names = [tc.get("name", "") for tc in tool_calls]
        has_text = bool(text.strip())
        action = classify_round(tool_names, has_text)
        summary = _generate_summary(action, text, tool_calls)

        rounds.append({
            "round": round_num,
            "action": action,
            "summary": summary,
            "tool_calls": tool_calls,
            "tool_results": tool_results,
            "text": text,
            "usage": usage,
            "raw_entries": raw_entries,
        })

        i += 1

    return rounds


def _generate_summary(action, text, tool_calls):
    """Generate a one-line summary for a round based on its action type."""
    if action == "PLAN":
        return _summarize_plan(text)
    elif action == "SUBMIT":
        return _summarize_submit(tool_calls)
    elif action == "SPAWN":
        return _summarize_spawn(tool_calls)
    elif action == "EXEC":
        return _summarize_exec(tool_calls)
    elif action == "EDIT":
        return _summarize_edit(tool_calls)
    elif action == "EXPLORE":
        return _summarize_explore(tool_calls)
    elif action == "ERROR":
        return "(empty response)"
    return ""


def _summarize_plan(text):
    """First ~80 chars of text in quotes."""
    text = text.strip()
    if len(text) > 80:
        return f'"{text[:80]}..."'
    return f'"{text}"'


def _summarize_submit(tool_calls):
    """communicate("first 80 chars...")."""
    for tc in tool_calls:
        name = tc.get("name", "")
        if name in ("communicate", "submit_result"):
            args = _parse_args(tc)
            result_text = args.get("result", "")
            if len(result_text) > 80:
                result_text = result_text[:80] + "..."
            return f'{name}("{result_text}")'
    return "submit"


def _summarize_spawn(tool_calls):
    """agent_name: "first 60 chars of task..."."""
    for tc in tool_calls:
        if tc.get("name") == "spawn_agent":
            args = _parse_args(tc)
            agent = args.get("agent", "agent")
            task = args.get("task", "")
            if len(task) > 60:
                task = task[:60] + "..."
            return f'{agent}: "{task}"'
    return "spawn_agent"


def _summarize_exec(tool_calls):
    """Shell command(s) truncated."""
    cmds = []
    for tc in tool_calls:
        if tc.get("name") == "shell":
            args = _parse_args(tc)
            cmd = args.get("command", "")
            if len(cmd) > 80:
                cmd = cmd[:80] + "..."
            cmds.append(cmd)
    return "; ".join(cmds) if cmds else "shell"


def _summarize_edit(tool_calls):
    """File names from tool args."""
    files = []
    for tc in tool_calls:
        name = tc.get("name", "")
        if name in ("apply_patch", "edit_file", "write_file"):
            args = _parse_args(tc)
            filename = _extract_filename(name, args)
            if filename and filename not in files:
                files.append(filename)
    return ", ".join(files) if files else "edit"


def _summarize_explore(tool_calls):
    """File/pattern names from tool args."""
    parts = []
    for tc in tool_calls:
        name = tc.get("name", "")
        args = _parse_args(tc)
        if name == "read_file":
            path = args.get("path", args.get("file_path", ""))
            if path:
                parts.append(path)
        elif name == "glob":
            pattern = args.get("pattern", "")
            if pattern:
                parts.append(pattern)
        elif name == "grep":
            pattern = args.get("pattern", "")
            path = args.get("path", "")
            if pattern:
                parts.append(pattern)
            if path:
                parts.append(path)
    return ", ".join(parts) if parts else "explore"


def _parse_args(tool_call):
    """Parse tool call arguments (may be string or dict)."""
    args = tool_call.get("arguments", {})
    if isinstance(args, str):
        try:
            return json.loads(args)
        except (json.JSONDecodeError, TypeError):
            return {}
    return args if isinstance(args, dict) else {}


def _extract_filename(tool_name, args):
    """Extract filename from edit tool arguments."""
    if tool_name == "apply_patch":
        patch = args.get("patch", "")
        # Look for +++ b/filename pattern
        for line in patch.split("\n"):
            if line.startswith("+++ b/"):
                return line[6:].strip()
            if line.startswith("+++ "):
                return line[4:].strip()
        return ""
    elif tool_name == "edit_file":
        return args.get("path", args.get("file_path", ""))
    elif tool_name == "write_file":
        return args.get("path", args.get("file_path", ""))
    return ""
