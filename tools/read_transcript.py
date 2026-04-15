#!/usr/bin/env python3
"""Read and analyze serf eval transcripts from the command line."""

import argparse
import json
import os
import sys
import textwrap
from pathlib import Path

EVALS_ROOT = Path.home() / ".serf-evals" / "tasks"


def rep_dir(args):
    return EVALS_ROOT / args.task / args.run / f"rep-{args.rep}"


def sessions_dir(args):
    return rep_dir(args) / "sessions"


def load_session_files(args):
    """Return sorted list of (session_id, transcript_path, meta_path) tuples."""
    sdir = sessions_dir(args)
    if not sdir.is_dir():
        sys.exit(f"Sessions directory not found: {sdir}")

    transcripts = sorted(sdir.glob("*.transcript.jsonl"))
    results = []
    for t in transcripts:
        sid = t.name.split(".")[0]
        meta = sdir / f"{sid}.meta.json"
        results.append((sid, t, meta))
    return results


def resolve_session(args, sessions):
    """Resolve --session flag to a (session_id, transcript_path, meta_path) tuple."""
    if args.session is None:
        return sessions[0] if sessions else None

    # Try as integer index first
    try:
        idx = int(args.session)
        if 0 <= idx < len(sessions):
            return sessions[idx]
        sys.exit(f"Session index {idx} out of range (0-{len(sessions)-1})")
    except ValueError:
        pass

    # Try as session ID prefix
    prefix = args.session.upper()
    matches = [s for s in sessions if s[0].startswith(prefix)]
    if len(matches) == 1:
        return matches[0]
    elif len(matches) == 0:
        sys.exit(f"No session matching prefix '{args.session}'")
    else:
        sys.exit(
            f"Ambiguous prefix '{args.session}', matches: "
            + ", ".join(m[0] for m in matches)
        )


def load_transcript(path):
    """Load all entries from a transcript JSONL file."""
    entries = []
    with open(path) as f:
        for line in f:
            line = line.strip()
            if line:
                entries.append(json.loads(line))
    return entries


def load_meta(path):
    """Load session metadata."""
    if path.exists():
        with open(path) as f:
            return json.load(f)
    return None


def truncate(text, max_len=120):
    """Truncate text to max_len, adding ellipsis if needed."""
    if not text:
        return ""
    text = text.replace("\n", " ").strip()
    if len(text) <= max_len:
        return text
    return text[: max_len - 3] + "..."


def format_args_summary(arguments):
    """Create a brief summary of tool call arguments."""
    if not arguments:
        return ""
    parts = []
    for key, val in arguments.items():
        if isinstance(val, str):
            parts.append(f"{key}={truncate(val, 60)}")
        elif isinstance(val, bool):
            if val:
                parts.append(key)
        elif isinstance(val, (int, float)):
            parts.append(f"{key}={val}")
        else:
            parts.append(f"{key}=...")
    return ", ".join(parts)


# ─── Commands ────────────────────────────────────────────────────────────


def cmd_list_sessions(args):
    sessions = load_session_files(args)
    if not sessions:
        print("No sessions found.")
        return

    print(f"Sessions for {args.task} / {args.run} / rep-{args.rep}:")
    print()
    for i, (sid, tpath, mpath) in enumerate(sessions):
        meta = load_meta(mpath)
        model = meta.get("model", "?") if meta else "?"
        turns = meta.get("turn_count", "?") if meta else "?"
        has_override = bool(
            meta.get("config", {}).get("base_prompt_override") if meta else False
        )
        role = "subagent" if has_override else "coordinator"
        print(f"  [{i}] {sid}  model={model}  turns={turns}  role={role}")


def cmd_tool_calls(args):
    sessions = load_session_files(args)
    sid, tpath, mpath = resolve_session(args, sessions)
    entries = load_transcript(tpath)

    print(f"Tool calls for session {sid}:")
    print()

    # Collect tool calls and their results
    pending_calls = {}  # tool_call_id -> (round, name, args_summary)
    round_num = 0

    for entry in entries:
        if entry.get("kind") == "api_call":
            round_num = entry.get("round", round_num)
            continue

        if entry.get("kind") != "entry":
            continue

        turn = entry.get("turn", {})
        message = turn.get("message", {})
        role = message.get("role", "")
        content = message.get("content", [])

        if role == "assistant":
            for item in content:
                if item.get("kind") == "tool_call":
                    tc = item.get("tool_call", {})
                    call_id = tc.get("id", "")
                    name = tc.get("name", "?")
                    args_summary = format_args_summary(tc.get("arguments", {}))
                    pending_calls[call_id] = (round_num, name, args_summary)

        elif role == "tool":
            for item in content:
                if item.get("kind") == "tool_result":
                    tr = item.get("tool_result", {})
                    call_id = tr.get("tool_call_id", "")
                    result_name = tr.get("name", "?")
                    result_content = tr.get("content", "")
                    is_error = tr.get("is_error", False)

                    if call_id in pending_calls:
                        rnd, name, args_summary = pending_calls.pop(call_id)
                    else:
                        rnd = round_num
                        name = result_name

                    err_marker = " ERROR" if is_error else ""
                    result_summary = truncate(str(result_content), 100)

                    print(f"  R{rnd:02d}  {name}{err_marker}")
                    if args_summary:
                        print(f"        args: {args_summary}")
                    if result_summary:
                        print(f"        => {result_summary}")
                    print()


def cmd_system_prompt(args):
    sessions = load_session_files(args)
    sid, tpath, mpath = resolve_session(args, sessions)
    entries = load_transcript(tpath)

    for entry in entries:
        if entry.get("kind") == "header":
            prompt = entry.get("system_prompt", "")
            if prompt:
                print(prompt)
                return

    # Fall back to meta.json base_prompt_override
    meta = load_meta(mpath)
    if meta:
        override = meta.get("config", {}).get("base_prompt_override", "")
        if override:
            print(override)
            return

    print("No system prompt found.")


def cmd_delegation(args):
    sessions = load_session_files(args)
    if not sessions:
        print("No sessions found.")
        return

    # Look in the coordinator session (first session) for spawn_agent calls
    _sid, tpath, _mpath = sessions[0]
    entries = load_transcript(tpath)

    delegation_num = 0
    for entry in entries:
        if entry.get("kind") != "entry":
            continue

        turn = entry.get("turn", {})
        message = turn.get("message", {})
        role = message.get("role", "")
        content = message.get("content", [])

        if role != "assistant":
            continue

        for item in content:
            if item.get("kind") != "tool_call":
                continue
            tc = item.get("tool_call", {})
            if tc.get("name") != "spawn_agent":
                continue

            delegation_num += 1
            agent_args = tc.get("arguments", {})
            agent_type = agent_args.get("agent_type", "?")
            task_text = agent_args.get("task", "")
            model = agent_args.get("model", "")
            max_turns = agent_args.get("max_turns", "")

            print(f"── Delegation #{delegation_num}: {agent_type} ──")
            if model:
                print(f"  model: {model}")
            if max_turns:
                print(f"  max_turns: {max_turns}")
            print()
            # Print task text with wrapping
            for line in task_text.split("\n"):
                wrapped = textwrap.fill(line, width=90, initial_indent="  ", subsequent_indent="  ")
                print(wrapped)
            print()

    if delegation_num == 0:
        print("No spawn_agent calls found in coordinator session.")


def cmd_verifier(args):
    path = rep_dir(args) / "verifier-stdout.txt"
    if not path.exists():
        sys.exit(f"Verifier output not found: {path}")
    print(path.read_text())


def cmd_score(args):
    path = rep_dir(args) / "reward.txt"
    if not path.exists():
        sys.exit(f"Reward file not found: {path}")
    score = path.read_text().strip()
    print(f"Score: {score}")


def cmd_full(args):
    sessions = load_session_files(args)
    sid, tpath, mpath = resolve_session(args, sessions)
    entries = load_transcript(tpath)

    limit = args.limit
    turn_count = 0

    print(f"Full transcript for session {sid}:")
    print()

    for entry in entries:
        if entry.get("kind") == "header":
            model = entry.get("model", "?")
            print(f"── HEADER: model={model}, session={entry.get('session_id', '?')} ──")
            print()
            continue

        if entry.get("kind") == "api_call":
            # Show round marker
            rnd = entry.get("round", "?")
            latency = entry.get("latency_ms", "?")
            print(f"── API CALL: round={rnd}, latency={latency}ms ──")
            print()
            continue

        if entry.get("kind") != "entry":
            continue

        turn = entry.get("turn", {})
        message = turn.get("message", {})
        role = message.get("role", "")
        content = message.get("content", [])
        usage = turn.get("usage", {})

        turn_count += 1
        if limit and turn_count > limit:
            print(f"  ... (limited to {limit} turns, {len(entries)} total entries)")
            break

        tokens_in = usage.get("input_tokens", 0)
        tokens_out = usage.get("output_tokens", 0)
        token_info = ""
        if tokens_in or tokens_out:
            token_info = f"  [{tokens_in}in/{tokens_out}out]"

        print(f"── {role.upper()}{token_info} ──")

        for item in content:
            kind = item.get("kind", "")

            if kind == "text":
                text = item.get("text", "")
                if text.strip():
                    for line in text.split("\n"):
                        print(f"  {line}")

            elif kind == "tool_call":
                tc = item.get("tool_call", {})
                name = tc.get("name", "?")
                args_summary = format_args_summary(tc.get("arguments", {}))
                print(f"  [CALL] {name}")
                if args_summary:
                    print(f"         {args_summary}")

            elif kind == "tool_result":
                tr = item.get("tool_result", {})
                name = tr.get("name", "?")
                is_error = tr.get("is_error", False)
                result = str(tr.get("content", ""))
                err_tag = " ERROR" if is_error else ""
                print(f"  [RESULT{err_tag}] {name}")
                # Show result content, but limit long output
                lines = result.split("\n")
                for line in lines[:20]:
                    print(f"    {line}")
                if len(lines) > 20:
                    print(f"    ... ({len(lines)} lines total)")

        print()


# ─── CLI ─────────────────────────────────────────────────────────────────


def main():
    parser = argparse.ArgumentParser(
        description="Read and analyze serf eval transcripts.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=textwrap.dedent("""\
        Examples:
          %(prog)s --run RUN --rep 1 --task chess-best-move --list-sessions
          %(prog)s --run RUN --rep 1 --task chess-best-move --tool-calls
          %(prog)s --run RUN --rep 1 --task chess-best-move --session 1 --tool-calls
          %(prog)s --run RUN --rep 1 --task chess-best-move --delegation
          %(prog)s --run RUN --rep 1 --task chess-best-move --session 0 --full --limit 20
        """),
    )

    parser.add_argument("--run", required=True, help="Run ID")
    parser.add_argument("--rep", required=True, type=int, help="Rep number")
    parser.add_argument("--task", required=True, help="Task name")
    parser.add_argument(
        "--session",
        default=None,
        help="Session index (0,1,...) or session ID prefix. Defaults to first session.",
    )
    parser.add_argument("--limit", type=int, default=None, help="Limit number of turns (for --full)")

    cmds = parser.add_mutually_exclusive_group(required=True)
    cmds.add_argument("--list-sessions", action="store_true", help="List sessions")
    cmds.add_argument("--tool-calls", action="store_true", help="Show tool calls")
    cmds.add_argument("--system-prompt", action="store_true", help="Show system prompt")
    cmds.add_argument("--delegation", action="store_true", help="Show delegation messages")
    cmds.add_argument("--verifier", action="store_true", help="Show verifier output")
    cmds.add_argument("--score", action="store_true", help="Show score")
    cmds.add_argument("--full", action="store_true", help="Full transcript dump")

    args = parser.parse_args()

    # Validate the rep directory exists
    rd = rep_dir(args)
    if not rd.is_dir():
        sys.exit(f"Rep directory not found: {rd}")

    if args.list_sessions:
        cmd_list_sessions(args)
    elif args.tool_calls:
        cmd_tool_calls(args)
    elif args.system_prompt:
        cmd_system_prompt(args)
    elif args.delegation:
        cmd_delegation(args)
    elif args.verifier:
        cmd_verifier(args)
    elif args.score:
        cmd_score(args)
    elif args.full:
        cmd_full(args)


if __name__ == "__main__":
    main()
