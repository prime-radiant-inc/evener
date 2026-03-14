"""Interrogate a lace agent session about its decisions.

Reads events.jsonl from a lace session, reconstructs the conversation as
Chat Completions messages, appends a question, and calls the LLM to get
the agent's self-reflection on its decisions.

Works with:
  - Direct events.jsonl paths
  - Session dirs (containing events.jsonl)
  - Trial dirs from eval runs (auto-discovers sessions)
  - ATIF trajectory.json files (delegates to narrative format)

Usage:
    # From an events.jsonl on magic-kingdom
    python3 lace_interrogate.py interrogate \
        /data/agent-evals/runs/lace-gate-v4-full/regex-log__abc123/agent/agent-state/agent-sessions/sess_xxx/events.jsonl \
        -q "Why did you use Perl instead of Python for testing?"

    # From a trial directory (picks largest session)
    python3 lace_interrogate.py interrogate \
        /data/agent-evals/runs/lace-gate-v4-full/regex-log__abc123 \
        -q "What went wrong?"

    # List sessions in a trial
    python3 lace_interrogate.py sessions \
        /data/agent-evals/runs/lace-gate-v4-full/regex-log__abc123

    # Show event summary for a session
    python3 lace_interrogate.py events \
        /data/agent-evals/runs/lace-gate-v4-full/regex-log__abc123/agent/agent-state/agent-sessions/sess_xxx

Environment:
    OPENROUTER_API_KEY  API key for OpenRouter (or any OpenAI-compatible endpoint)
    OPENAI_API_KEY      Fallback if OPENROUTER_API_KEY not set
    LLM_BASE_URL        Override base URL (default: https://openrouter.ai/api/v1)
"""

import argparse
import json
import os
import sys
from pathlib import Path


try:
    import openai
except ImportError:
    print("pip install openai", file=sys.stderr)
    sys.exit(1)


DEFAULT_MODEL = "anthropic/claude-sonnet-4"
DEFAULT_BASE_URL = "https://openrouter.ai/api/v1"


def _get_client():
    """Create an OpenAI client configured for OpenRouter."""
    api_key = os.environ.get("OPENROUTER_API_KEY") or os.environ.get("OPENAI_API_KEY")
    if not api_key:
        print("Set OPENROUTER_API_KEY or OPENAI_API_KEY", file=sys.stderr)
        sys.exit(1)
    base_url = os.environ.get("LLM_BASE_URL", DEFAULT_BASE_URL)
    return openai.OpenAI(api_key=api_key, base_url=base_url)


def _resolve_events_path(path_str):
    """Resolve a path to an events.jsonl file.

    Accepts:
      - Direct path to events.jsonl
      - Session directory (containing events.jsonl)
      - Trial directory (agent/agent-state/agent-sessions/sess_*/events.jsonl)
      - ATIF trajectory.json

    Returns (events_path, is_atif) where is_atif=True for trajectory.json.
    """
    p = Path(path_str)

    # Direct file — accept any .jsonl as events, any .json as trajectory
    if p.is_file():
        if p.suffix == ".jsonl":
            return p, False
        if p.suffix == ".json":
            return p, True

    # Session directory
    events = p / "events.jsonl"
    if events.exists():
        return events, False

    # Trial directory — find the largest session (most events = main session)
    sessions_dir = p / "agent" / "agent-state" / "agent-sessions"
    if sessions_dir.is_dir():
        best = None
        best_size = -1
        for sess_dir in sessions_dir.iterdir():
            ef = sess_dir / "events.jsonl"
            if ef.exists():
                size = ef.stat().st_size
                if size > best_size:
                    best = ef
                    best_size = size
        if best:
            print(f"Auto-selected session: {best.parent.name} ({best_size} bytes)",
                  file=sys.stderr)
            return best, False

    # Maybe it's a trajectory.json alongside
    traj = p / "agent" / "trajectory.json"
    if traj.exists():
        return traj, True

    print(f"Cannot find events.jsonl or trajectory.json from: {path_str}", file=sys.stderr)
    sys.exit(1)


def _parse_events(events_path):
    """Parse events.jsonl into a list of event dicts."""
    events = []
    with open(events_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return events


def _events_to_messages(events):
    """Convert lace events to Chat Completions messages.

    Event types:
      - context_injected → system message
      - prompt → user message
      - message → assistant message (text content)
      - tool_use → assistant tool_call + tool result
      - context_compacted → reset messages to summary
      - turn_start, turn_end → skip
    """
    messages = []
    # Accumulate tool_use events between messages to batch them
    pending_tool_calls = []

    def _flush_tool_calls():
        """Flush accumulated tool calls as an assistant message + tool results."""
        nonlocal pending_tool_calls
        if not pending_tool_calls:
            return

        tc_list = []
        tool_results = []
        for tc in pending_tool_calls:
            data = tc.get("data", {})
            tc_id = data.get("toolCallId", f"call_{tc.get('eventSeq', 0)}")
            tc_name = data.get("name", "unknown")
            tc_input = data.get("input", {})

            tc_list.append({
                "id": tc_id,
                "type": "function",
                "function": {
                    "name": tc_name,
                    "arguments": json.dumps(tc_input),
                },
            })

            # Extract result
            result_data = data.get("result", {})
            result_content = result_data.get("content", [])
            result_text = ""
            for block in result_content:
                if isinstance(block, dict) and block.get("type") == "text":
                    result_text += block.get("text", "")

            # Truncate long results
            if len(result_text) > 4000:
                result_text = result_text[:3800] + f"\n... [truncated, {len(result_text)} chars]"

            tool_results.append({
                "role": "tool",
                "tool_call_id": tc_id,
                "content": result_text or "(no output)",
            })

        messages.append({"role": "assistant", "tool_calls": tc_list})
        messages.extend(tool_results)
        pending_tool_calls = []

    for event in events:
        etype = event.get("type", "")
        data = event.get("data", {})

        if etype == "context_injected":
            _flush_tool_calls()
            content_blocks = data.get("content", [])
            text = ""
            for block in content_blocks:
                if isinstance(block, dict) and block.get("type") == "text":
                    text += block.get("text", "")
            if text.strip():
                messages.append({"role": "system", "content": text})

        elif etype == "context_compacted":
            _flush_tool_calls()
            # Reset messages — compaction replaces history
            messages.clear()
            summary = data.get("summary", "")
            if summary.strip():
                messages.append({"role": "system", "content": summary})
            preserved = data.get("preserved", [])
            for msg in preserved:
                if isinstance(msg, dict) and msg.get("role") in ("user", "assistant", "system"):
                    messages.append(msg)

        elif etype == "prompt":
            _flush_tool_calls()
            content_blocks = data.get("content", [])
            text = ""
            for block in content_blocks:
                if isinstance(block, dict) and block.get("type") == "text":
                    text += block.get("text", "")
            if text.strip():
                messages.append({"role": "user", "content": text})

        elif etype == "message":
            _flush_tool_calls()
            content = data.get("content", "")
            if isinstance(content, list):
                text = ""
                for block in content:
                    if isinstance(block, dict) and block.get("type") == "text":
                        text += block.get("text", "")
                content = text
            if content and content.strip():
                messages.append({"role": "assistant", "content": content})

        elif etype == "tool_use":
            pending_tool_calls.append(event)

        # Skip turn_start, turn_end, etc.

    _flush_tool_calls()
    return messages


def _events_to_narrative(events, up_to_seq=None):
    """Convert events to a readable narrative for interrogation.

    Like interrogate_trajectory.py's _atif_to_narrative but for events.jsonl.
    """
    lines = []

    for event in events:
        seq = event.get("eventSeq", 0)
        if up_to_seq is not None and seq > up_to_seq:
            break

        etype = event.get("type", "")
        data = event.get("data", {})

        if etype == "context_injected":
            content_blocks = data.get("content", [])
            text = ""
            for block in content_blocks:
                if isinstance(block, dict) and block.get("type") == "text":
                    text += block.get("text", "")
            lines.append(f"[SYSTEM PROMPT — {len(text)} chars, starting with:]")
            lines.append(text[:500])
            if len(text) > 500:
                lines.append("... [truncated]")

        elif etype == "prompt":
            content_blocks = data.get("content", [])
            text = ""
            for block in content_blocks:
                if isinstance(block, dict) and block.get("type") == "text":
                    text += block.get("text", "")
            lines.append(f"\n[USER MESSAGE — Event {seq}]")
            lines.append(text)

        elif etype == "message":
            content = data.get("content", "")
            if isinstance(content, list):
                text_parts = []
                for block in content:
                    if isinstance(block, dict) and block.get("type") == "text":
                        text_parts.append(block.get("text", ""))
                content = "".join(text_parts)
            if content and content.strip():
                lines.append(f"\n[ASSISTANT THINKING — Event {seq}]")
                lines.append(content[:500])
                if len(content) > 500:
                    lines.append("... [truncated]")

        elif etype == "tool_use":
            tc_name = data.get("name", "?")
            tc_input = data.get("input", {})
            result_data = data.get("result", {})
            result_content = result_data.get("content", [])
            result_text = ""
            for block in result_content:
                if isinstance(block, dict) and block.get("type") == "text":
                    result_text += block.get("text", "")

            lines.append(f"\n[TOOL CALL — Event {seq}]")
            if tc_name == "bash":
                cmd = tc_input.get("command", "")
                lines.append(f"bash: {cmd[:300]}")
            elif tc_name == "file_write":
                content = tc_input.get("content", "")
                lines.append(f"file_write: {tc_input.get('path', '?')}")
                lines.append(f"  Content ({len(content)} chars): {content[:300]}")
                if len(content) > 300:
                    lines.append("  ... [truncated]")
            elif tc_name == "file_read":
                lines.append(f"file_read: {tc_input.get('path', '?')}")
            elif tc_name == "file_edit":
                lines.append(f"file_edit: {tc_input.get('path', '?')}")
            elif tc_name == "done":
                lines.append(f"done: {tc_input.get('reason', '')[:200]}")
            else:
                lines.append(f"{tc_name}: {json.dumps(tc_input)[:300]}")

            if result_text:
                if len(result_text) > 500:
                    result_text = result_text[:450] + f"\n... [{len(result_text)} chars total]"
                lines.append(f"  Result: {result_text}")

        elif etype == "turn_end":
            stop_reason = data.get("stopReason", "")
            lines.append(f"\n[TURN END — {stop_reason}]")

    return "\n".join(lines)


def _load_atif_narrative(trajectory_path, up_to_step=None):
    """Load ATIF trajectory and convert to narrative."""
    with open(trajectory_path) as f:
        trajectory = json.load(f)

    # Reuse the narrative format from interrogate_trajectory.py
    lines = []
    steps = trajectory["steps"]
    for step in steps:
        step_id = step["step_id"]
        if up_to_step is not None and step_id > up_to_step:
            break
        source = step.get("source", "")
        msg_text = step.get("message", "")
        tool_calls = step.get("tool_calls", [])

        if source == "system":
            lines.append(f"[SYSTEM PROMPT — {len(msg_text)} chars, starting with:]")
            lines.append(msg_text[:500])
            if len(msg_text) > 500:
                lines.append("... [truncated]")
        elif source == "user":
            lines.append(f"\n[USER MESSAGE — Step {step_id}]")
            lines.append(msg_text)
        elif source == "agent":
            if msg_text:
                lines.append(f"\n[ASSISTANT — Step {step_id}]")
                lines.append(msg_text[:500])
            for tc in tool_calls:
                name = tc.get("function_name") or tc.get("name", "?")
                args = tc.get("arguments", {})
                result = tc.get("result", "")
                lines.append(f"\n[TOOL CALL — Step {step_id}]")
                if name == "bash":
                    lines.append(f"bash: {args.get('command', '')[:300]}")
                elif name == "file_write":
                    content = args.get("content", "")
                    lines.append(f"file_write: {args.get('path', '?')}")
                    lines.append(f"  Content ({len(content)} chars): {content[:300]}")
                elif name == "done":
                    lines.append(f"done: {args.get('reason', '')[:200]}")
                else:
                    lines.append(f"{name}: {json.dumps(args)[:300]}")
                if result:
                    r = str(result)
                    if len(r) > 500:
                        r = r[:450] + f"\n... [{len(r)} chars total]"
                    lines.append(f"  Result: {r}")

    return "\n".join(lines)


def _summarize_event(event):
    """One-line summary of an event."""
    etype = event.get("type", "?")
    data = event.get("data", {})
    seq = event.get("eventSeq", "?")

    if etype == "context_injected":
        content = data.get("content", [])
        text_len = sum(len(b.get("text", "")) for b in content if isinstance(b, dict))
        return f"[{seq:3}] context_injected ({text_len} chars)"

    if etype == "prompt":
        content = data.get("content", [])
        text = ""
        for b in content:
            if isinstance(b, dict) and b.get("type") == "text":
                text += b.get("text", "")
        return f"[{seq:3}] prompt: {text[:100]}"

    if etype == "message":
        content = data.get("content", "")
        if isinstance(content, list):
            parts = [b.get("text", "") for b in content if isinstance(b, dict)]
            content = "".join(parts)
        return f"[{seq:3}] message: {str(content)[:80]}"

    if etype == "tool_use":
        name = data.get("name", "?")
        inp = data.get("input", {})
        if name == "bash":
            cmd = inp.get("command", "")[:80]
            return f"[{seq:3}] tool_use: bash({cmd})"
        if name == "file_write":
            return f"[{seq:3}] tool_use: file_write({inp.get('path', '?')})"
        if name == "file_read":
            return f"[{seq:3}] tool_use: file_read({inp.get('path', '?')})"
        if name == "file_edit":
            return f"[{seq:3}] tool_use: file_edit({inp.get('path', '?')})"
        if name == "done":
            return f"[{seq:3}] tool_use: done({inp.get('reason', '')[:60]})"
        return f"[{seq:3}] tool_use: {name}({json.dumps(inp)[:80]})"

    if etype == "turn_start":
        return f"[{seq:3}] turn_start"
    if etype == "turn_end":
        return f"[{seq:3}] turn_end ({data.get('stopReason', '?')})"
    if etype == "context_compacted":
        return f"[{seq:3}] context_compacted"

    return f"[{seq:3}] {etype}"


def cmd_interrogate(args):
    """Ask the model about decisions in a session."""
    path, is_atif = _resolve_events_path(args.path)

    if is_atif:
        narrative = _load_atif_narrative(path, up_to_step=args.up_to_event)
    else:
        events = _parse_events(path)
        narrative = _events_to_narrative(events, up_to_seq=args.up_to_event)

    # Count events/steps for display
    if is_atif:
        with open(path) as f:
            traj = json.load(f)
        n_steps = len(traj["steps"])
        print(f"Trajectory: {n_steps} steps (ATIF format)")
    else:
        print(f"Session: {path.parent.name}")
        print(f"Events: {len(events)}")

    if args.up_to_event:
        print(f"Truncated to event/step {args.up_to_event}")
    print()

    if args.show_events:
        if not is_atif:
            for e in events:
                if args.up_to_event and e.get("eventSeq", 0) > args.up_to_event:
                    break
                print(f"  {_summarize_event(e)}")
            print()

    messages = [
        {
            "role": "system",
            "content": (
                "You are reviewing a transcript of your own actions as an "
                "autonomous coding agent. Answer questions about your reasoning "
                "honestly and specifically. Do not be defensive or make excuses. "
                "Focus on what actually drove your decisions."
            ),
        },
        {
            "role": "user",
            "content": (
                "Here is a transcript of actions you took while working on a task:\n\n"
                f"{narrative}\n\n"
                "---\n\n"
                f"{args.question}"
            ),
        },
    ]

    if args.dry_run:
        print(f"Would send {len(messages)} messages to {args.model}")
        print(f"Narrative length: {len(narrative)} chars")
        print(f"Question: {args.question}")
        return

    client = _get_client()
    print(f"Asking {args.model}...")
    print(f"Q: {args.question}")
    print()

    response = client.chat.completions.create(
        model=args.model,
        messages=messages,
        max_tokens=2000,
        temperature=0.3,
    )

    answer = response.choices[0].message.content
    print(f"A: {answer}")
    print()
    if response.usage:
        print(f"Tokens: {response.usage.prompt_tokens} in / "
              f"{response.usage.completion_tokens} out")


def cmd_resume(args):
    """Resume a session: load the full conversation and append a question.

    Unlike interrogate (which renders a narrative), this reconstructs the
    actual message history including tool calls, so the model sees its own
    conversation verbatim. More expensive but more faithful.
    """
    path, is_atif = _resolve_events_path(args.path)

    if is_atif:
        print("resume mode requires events.jsonl, not ATIF trajectory.json",
              file=sys.stderr)
        sys.exit(1)

    events = _parse_events(path)

    # Truncate events if --up-to-event specified
    if args.up_to_event is not None:
        events = [e for e in events if e.get("eventSeq", 0) <= args.up_to_event]

    messages = _events_to_messages(events)

    # Replace system prompt if --persona specified
    if args.persona:
        persona_text = Path(args.persona).read_text()
        # Find and replace the first system message
        for i, m in enumerate(messages):
            if m.get("role") == "system":
                messages[i] = {"role": "system", "content": persona_text}
                break

    print(f"Session: {path.parent.name}")
    print(f"Events: {len(events)}")
    if args.up_to_event is not None:
        print(f"Truncated to event {args.up_to_event}")
    if args.persona:
        print(f"Persona: {args.persona}")
    print(f"Messages reconstructed: {len(messages)}")
    print()

    # Append the question as a new user message
    messages.append({"role": "user", "content": args.question})

    # We need tool definitions for the model to understand tool_call messages
    tools = [
        {"type": "function", "function": {"name": "bash", "parameters": {"type": "object", "properties": {"command": {"type": "string"}, "description": {"type": "string"}, "timeout": {"type": "integer"}}, "required": ["command"]}}},
        {"type": "function", "function": {"name": "file_read", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "offset": {"type": "integer"}, "limit": {"type": "integer"}}, "required": ["path"]}}},
        {"type": "function", "function": {"name": "file_write", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}, "required": ["path", "content"]}}},
        {"type": "function", "function": {"name": "file_edit", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "old_text": {"type": "string"}, "new_text": {"type": "string"}}, "required": ["path", "old_text", "new_text"]}}},
        {"type": "function", "function": {"name": "ripgrep_search", "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}, "path": {"type": "string"}}, "required": ["pattern"]}}},
        {"type": "function", "function": {"name": "file_find", "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}, "path": {"type": "string"}}, "required": ["pattern"]}}},
        {"type": "function", "function": {"name": "url_fetch", "parameters": {"type": "object", "properties": {"url": {"type": "string"}}, "required": ["url"]}}},
        {"type": "function", "function": {"name": "done", "parameters": {"type": "object", "properties": {"reason": {"type": "string"}}, "required": ["reason"]}}},
    ]

    if args.dry_run:
        # Show message role distribution
        roles = {}
        for m in messages:
            r = m["role"]
            roles[r] = roles.get(r, 0) + 1
        print(f"Would send {len(messages)} messages to {args.model}")
        print(f"Message breakdown: {roles}")
        print(f"Question: {args.question}")
        return

    client = _get_client()
    reps = getattr(args, "reps", 1) or 1

    for rep in range(reps):
        if reps > 1:
            print(f"=== Rep {rep + 1}/{reps} ===")

        print(f"Asking {args.model} (full conversation replay)...")
        print(f"Q: {args.question}")
        print()

        response = client.chat.completions.create(
            model=args.model,
            messages=messages,
            tools=tools,
            tool_choice="auto",  # Let model decide — tool calls show what it would do next
            max_tokens=4000,
            temperature=0.7 if reps > 1 else 0.3,  # Higher temp for diversity across reps
        )

        choice = response.choices[0]
        # Show text response
        if choice.message.content:
            print(f"A: {choice.message.content}")
        # Show tool calls (what the model would do next)
        if choice.message.tool_calls:
            print("Tool calls:")
            for tc in choice.message.tool_calls:
                args_str = tc.function.arguments[:300]
                print(f"  {tc.function.name}({args_str})")
        print()
        if response.usage:
            print(f"Tokens: {response.usage.prompt_tokens} in / "
                  f"{response.usage.completion_tokens} out")
        if reps > 1:
            print()


def cmd_events(args):
    """Show event summary for a session."""
    path, is_atif = _resolve_events_path(args.path)

    if is_atif:
        print("events command requires events.jsonl, not trajectory.json",
              file=sys.stderr)
        sys.exit(1)

    events = _parse_events(path)
    print(f"Session: {path.parent.name}")
    print(f"Events: {len(events)}")
    print()

    for e in events:
        print(f"  {_summarize_event(e)}")


def cmd_sessions(args):
    """List sessions in a trial directory."""
    p = Path(args.path)
    sessions_dir = p / "agent" / "agent-state" / "agent-sessions"

    if not sessions_dir.is_dir():
        # Maybe it's already a sessions dir
        sessions_dir = p
        if not sessions_dir.is_dir():
            print(f"Not a directory: {sessions_dir}", file=sys.stderr)
            sys.exit(1)

    for sess_dir in sorted(sessions_dir.iterdir()):
        ef = sess_dir / "events.jsonl"
        if not ef.exists():
            continue
        events = _parse_events(ef)
        n_events = len(events)
        size = ef.stat().st_size

        # Count tool calls
        tool_calls = [e for e in events if e.get("type") == "tool_use"]
        turns = [e for e in events if e.get("type") == "turn_end"]

        # Get first prompt text (truncated)
        first_prompt = ""
        for e in events:
            if e.get("type") == "prompt":
                content = e.get("data", {}).get("content", [])
                for b in content:
                    if isinstance(b, dict) and b.get("type") == "text":
                        first_prompt = b.get("text", "")[:80]
                        break
                break

        print(f"  {sess_dir.name}")
        print(f"    Events: {n_events}, Tool calls: {len(tool_calls)}, "
              f"Turns: {len(turns)}, Size: {size} bytes")
        if first_prompt:
            print(f"    Prompt: {first_prompt}")
        print()


def main():
    parser = argparse.ArgumentParser(
        description="Interrogate lace agent sessions about their decisions"
    )
    parser.add_argument("--model", default=DEFAULT_MODEL,
                        help=f"Model name (default: {DEFAULT_MODEL})")

    sub = parser.add_subparsers(dest="command", required=True)

    # interrogate — narrative-based, cheaper
    p_int = sub.add_parser("interrogate",
                           help="Ask about decisions (narrative mode, cheaper)")
    p_int.add_argument("path",
                       help="Path to events.jsonl, session dir, trial dir, or trajectory.json")
    p_int.add_argument("--question", "-q", required=True,
                       help="Question to ask about the session")
    p_int.add_argument("--up-to-event", type=int,
                       help="Only include events up to this sequence number")
    p_int.add_argument("--show-events", action="store_true",
                       help="Print event summaries before asking")
    p_int.add_argument("--dry-run", action="store_true")

    # resume — full conversation replay, more faithful but expensive
    p_res = sub.add_parser("resume",
                           help="Ask about decisions (full replay mode, faithful but expensive)")
    p_res.add_argument("path",
                       help="Path to events.jsonl or session dir")
    p_res.add_argument("--question", "-q", required=True,
                       help="Question to ask")
    p_res.add_argument("--up-to-event", type=int,
                       help="Only include events up to this sequence number")
    p_res.add_argument("--persona", type=str,
                       help="Path to persona .md file to replace system prompt")
    p_res.add_argument("--reps", type=int, default=1,
                       help="Number of times to replay (default: 1)")
    p_res.add_argument("--dry-run", action="store_true")

    # events — show event summary
    p_ev = sub.add_parser("events", help="Show event summary for a session")
    p_ev.add_argument("path", help="Path to events.jsonl or session dir")

    # sessions — list sessions in a trial
    p_sess = sub.add_parser("sessions", help="List sessions in a trial directory")
    p_sess.add_argument("path", help="Path to trial directory")

    args = parser.parse_args()

    if args.command == "interrogate":
        cmd_interrogate(args)
    elif args.command == "resume":
        cmd_resume(args)
    elif args.command == "events":
        cmd_events(args)
    elif args.command == "sessions":
        cmd_sessions(args)


if __name__ == "__main__":
    main()
