"""Parse transcripts into high-level trajectory timelines.

Tool classification uses pattern matching on tool names, not hardcoded maps.
This works for any agent — serf, codex, claude, or custom agents.
"""

import json
import re


# Patterns to match tool names to categories.
# Order matters: first match wins. Each entry is (regex_pattern, category).
_TOOL_PATTERNS = [
    # SUBMIT: communication/result tools
    (re.compile(r"communicate|submit|done$|report_result|finish", re.I), "SUBMIT"),
    # REVIEW: approve/reject tools
    (re.compile(r"approve|reject|review", re.I), "REVIEW"),
    # SPAWN: agent delegation
    (re.compile(r"spawn|agent|delegate", re.I), "SPAWN"),
    # TASK: task management (before EXPLORE so task_list doesn't match "list")
    (re.compile(r"task_list|todo|plan_tool", re.I), "TASK"),
    # EXEC: shell/command execution
    (re.compile(r"exec|command|shell|run_|bash|terminal", re.I), "EXEC"),
    # EDIT: file modification
    (re.compile(r"write|edit|patch|create_file|update_file|insert|replace", re.I), "EDIT"),
    # EXPLORE: file reading/searching
    (re.compile(r"read|list|glob|grep|search|find|dir$|ls$|cat$|head$|tail$", re.I), "EXPLORE"),
]

# Priority order for mixed-tool rounds (highest first)
_PRIORITY = ["SUBMIT", "REVIEW", "SPAWN", "EDIT", "EXEC", "EXPLORE", "TASK", "PLAN"]


def classify_tool(name):
    """Classify a single tool name into a category.

    Returns a category string or None if no pattern matches.
    """
    for pattern, category in _TOOL_PATTERNS:
        if pattern.search(name):
            return category
    return None


def classify_round(tool_names, has_text):
    """Classify a round by its dominant tool type.

    Args:
        tool_names: list of tool names called in this round.
        has_text: whether the assistant produced text output.

    Returns:
        Action string: EXPLORE, EDIT, EXEC, SPAWN, SUBMIT, REVIEW,
        TASK, PLAN, TOOL (unrecognized tool), or ERROR (truly empty).
    """
    if not tool_names:
        return "PLAN" if has_text else "ERROR"

    categories = set()
    has_unrecognized = False
    for name in tool_names:
        cat = classify_tool(name)
        if cat:
            categories.add(cat)
        else:
            has_unrecognized = True

    if not categories:
        # Tools present but none matched any pattern.
        # This is NOT an error — the agent called a tool we don't categorize.
        return "TOOL"

    # Return highest-priority category
    for cat in _PRIORITY:
        if cat in categories:
            return cat

    # Fallback: has categories but none in priority list (shouldn't happen)
    return "TOOL"


def build_trajectory(session):
    """Parse session entries into a list of round dicts.

    Each round = one ASSISTANT entry + its following TOOL_RESULTS.

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

        if kind in ("STEERING", "USER_INPUT"):
            i += 1
            continue

        # Skip other non-ASSISTANT entries
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

        tool_names = []
        nonfinal_communicates = []
        for tc in tool_calls:
            name = tc.get("name", "")
            kind = _communicate_kind(tc)
            if kind and kind != "final":
                nonfinal_communicates.append(tc)
                continue
            tool_names.append(name)

        has_text = bool(text.strip()) or bool(nonfinal_communicates)
        action = classify_round(tool_names, has_text)
        summary = _generate_summary(action, text, tool_calls)

        duration_ms = sum(tr.get("duration_ms", 0) for tr in tool_results)

        rounds.append({
            "round": round_num,
            "action": action,
            "summary": summary,
            "tool_calls": tool_calls,
            "tool_results": tool_results,
            "text": text,
            "usage": usage,
            "duration_ms": duration_ms,
            "raw_entries": raw_entries,
        })

        i += 1

    return rounds


def _generate_summary(action, text, tool_calls):
    """Generate a one-line summary for a round based on its action type.

    Summarization is generic — it extracts information from tool arguments
    by looking for common keys, not by matching specific tool names.
    """
    if action == "PLAN":
        if not text.strip():
            communicate_summary = _summarize_communicate(tool_calls, final_only=False, nonfinal_only=True)
            if communicate_summary:
                return communicate_summary
        return _summarize_plan(text)
    elif action == "SUBMIT":
        communicate_summary = _summarize_communicate(tool_calls, final_only=True)
        if communicate_summary:
            return communicate_summary
        return _summarize_by_args(tool_calls, ["result", "message", "output"], "submit")
    elif action == "SPAWN":
        return _summarize_spawn(tool_calls)
    elif action == "EXEC":
        return _summarize_by_args(tool_calls, ["command", "cmd", "script"], "exec")
    elif action == "EDIT":
        return _summarize_edit(tool_calls)
    elif action == "EXPLORE":
        return _summarize_explore(tool_calls)
    elif action == "REVIEW":
        return _summarize_by_args(tool_calls, ["reason", "message", "feedback"], "review")
    elif action == "TASK":
        return _summarize_by_args(tool_calls, ["description", "task", "name"], "task")
    elif action == "TOOL":
        return _summarize_unknown_tools(tool_calls)
    elif action == "ERROR":
        return "(empty response)"
    return ""


def _summarize_plan(text):
    """First ~80 chars of text in quotes."""
    text = text.strip()
    if len(text) > 80:
        return f'"{text[:80]}..."'
    return f'"{text}"'


def _summarize_by_args(tool_calls, arg_keys, fallback_label):
    """Generic summarizer: find first matching arg key, show tool_name("value")."""
    for tc in tool_calls:
        name = tc.get("name", fallback_label)
        args = _parse_args(tc)
        for key in arg_keys:
            value = args.get(key, "")
            if value:
                value = _format_summary_value(value)
                if len(value) > 80:
                    value = value[:80] + "..."
                return f'{name}("{value}")'
    # No matching arg keys found — show tool name(s)
    names = [tc.get("name", "") for tc in tool_calls if tc.get("name")]
    return ", ".join(names) if names else fallback_label


def _summarize_spawn(tool_calls):
    """agent_name: "first 60 chars of task..."."""
    for tc in tool_calls:
        cat = classify_tool(tc.get("name", ""))
        if cat == "SPAWN":
            args = _parse_args(tc)
            agent = args.get("agent", args.get("name", "agent"))
            task = args.get("task", args.get("prompt", args.get("instruction", "")))
            if len(task) > 60:
                task = task[:60] + "..."
            return f'{agent}: "{task}"'
    return "spawn"


def _summarize_edit(tool_calls):
    """Extract file paths from any edit tool's arguments."""
    files = []
    for tc in tool_calls:
        cat = classify_tool(tc.get("name", ""))
        if cat == "EDIT":
            args = _parse_args(tc)
            filename = _extract_filename_generic(tc.get("name", ""), args)
            if filename and filename not in files:
                files.append(filename)
    return ", ".join(files) if files else "edit"


def _summarize_explore(tool_calls):
    """Extract file/pattern names from any explore tool's arguments."""
    parts = []
    for tc in tool_calls:
        args = _parse_args(tc)
        # Try common arg names for paths and patterns
        for key in ("path", "file_path", "file", "directory", "dir"):
            val = args.get(key, "")
            if val and val not in parts:
                parts.append(val)
        for key in ("pattern", "query", "regex", "glob"):
            val = args.get(key, "")
            if val and val not in parts:
                parts.append(val)
    return ", ".join(parts[:4]) if parts else "explore"


def _summarize_unknown_tools(tool_calls):
    """For unrecognized tools, show tool_name(first_interesting_arg)."""
    for tc in tool_calls:
        name = tc.get("name", "tool")
        args = _parse_args(tc)
        # Try to find something interesting in the args
        for key in ("command", "path", "file_path", "pattern", "task",
                     "result", "message", "query", "url"):
            val = args.get(key, "")
            if val:
                val = _format_summary_value(val)
                if len(val) > 60:
                    val = val[:60] + "..."
                return f'{name}("{val}")'
        # Just show the tool name
        return name
    return "tool"


def _summarize_communicate(tool_calls, final_only=False, nonfinal_only=False):
    """Summarize communicate-style tool calls with kind-aware labels."""
    for tc in tool_calls:
        kind = _communicate_kind(tc)
        if not kind:
            continue
        if final_only and kind != "final":
            continue
        if nonfinal_only and kind == "final":
            continue

        name = tc.get("name", "communicate")
        args = _parse_args(tc)
        message = _communicate_message(args)
        label = name
        if name == "communicate":
            label = f"{name}:{kind}"
        if message:
            if len(message) > 80:
                message = message[:80] + "..."
            return f'{label}("{message}")'
        return label
    return ""


def _communicate_kind(tool_call):
    """Return communicate kind for communicate-style tools, else empty string."""
    name = tool_call.get("name", "")
    cat = classify_tool(name)
    if cat != "SUBMIT":
        return ""

    if name != "communicate":
        return "final"

    args = _parse_args(tool_call)
    kind = args.get("kind", "")
    if isinstance(kind, str):
        kind = kind.strip().lower()
    else:
        kind = ""

    # Legacy communicate payloads had no explicit kind and were implicitly final.
    return kind or "final"


def _communicate_message(args):
    """Extract the user-visible message from communicate-style args."""
    for key in ("message", "result"):
        value = args.get(key, "")
        if isinstance(value, str) and value.strip():
            return value.strip()

    output = args.get("output")
    if isinstance(output, dict):
        for key in ("message", "result"):
            value = output.get(key, "")
            if isinstance(value, str) and value.strip():
                return value.strip()
        if output:
            return _format_summary_value(output)
    if isinstance(output, str) and output.strip():
        return output.strip()

    return ""


def _format_summary_value(value):
    """Render summary values, preferring nested output.message when present."""
    if isinstance(value, dict):
        for key in ("message", "result"):
            nested = value.get(key, "")
            if isinstance(nested, str) and nested.strip():
                return nested.strip()
        try:
            return json.dumps(value, sort_keys=True)
        except (TypeError, ValueError):
            return str(value)
    return str(value)


def _parse_args(tool_call):
    """Parse tool call arguments (may be string or dict)."""
    args = tool_call.get("arguments", {})
    if isinstance(args, str):
        try:
            return json.loads(args)
        except (json.JSONDecodeError, TypeError):
            return {}
    return args if isinstance(args, dict) else {}


def _extract_filename_generic(tool_name, args):
    """Extract filename from edit tool arguments, trying common patterns."""
    # Try direct path keys first
    for key in ("path", "file_path", "file", "filename"):
        val = args.get(key, "")
        if val:
            return val

    # For patch-style tools, parse the diff header
    patch = args.get("patch", args.get("diff", ""))
    if patch:
        for line in patch.split("\n"):
            if line.startswith("+++ b/"):
                return line[6:].strip()
            if line.startswith("+++ "):
                return line[4:].strip()

    return ""
