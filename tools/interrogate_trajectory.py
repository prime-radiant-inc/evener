"""Interrogate and replay agent trajectories against any Chat Completions model.

Two modes:
  interrogate: Feed the agent its own trajectory and ask "why did you do X?"
  replay:      Replay a decision point with a different system prompt

Works with ATIF-format trajectories (from lace) and any OpenAI-compatible
API (OpenRouter, OpenAI, etc.) via Chat Completions.

This is the fast inner loop for prompt optimization: instead of running a
full eval (~15min), replay a critical decision point (~5sec) or interrogate
a failed trajectory to understand what prompt language would change behavior.

Usage:
    # Interrogate: why did the agent use pyOpenSSL?
    python3 interrogate_trajectory.py interrogate trajectory.json \
        --question "Why did you use pyOpenSSL instead of the openssl CLI?"

    # Interrogate with a truncated context (only up to step N)
    python3 interrogate_trajectory.py interrogate trajectory.json \
        --question "What would you do next?" --up-to-step 5

    # Replay: would a different prompt change the decision at step 13?
    python3 interrogate_trajectory.py replay trajectory.json \
        --persona persona.md --at-step 13

    # Compare: run the same replay with two personas side by side
    python3 interrogate_trajectory.py compare trajectory.json \
        --persona-a iter-3.md --persona-b iter-4.md --at-step 13

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

# Use openai client with custom base_url for OpenRouter compatibility
try:
    import openai
except ImportError:
    print("pip install openai", file=sys.stderr)
    sys.exit(1)


DEFAULT_MODEL = "qwen/qwen3.5-flash-02-23"
DEFAULT_BASE_URL = "https://openrouter.ai/api/v1"


def _get_client():
    """Create an OpenAI client configured for OpenRouter."""
    api_key = os.environ.get("OPENROUTER_API_KEY") or os.environ.get("OPENAI_API_KEY")
    if not api_key:
        print("Set OPENROUTER_API_KEY or OPENAI_API_KEY", file=sys.stderr)
        sys.exit(1)

    base_url = os.environ.get("LLM_BASE_URL", DEFAULT_BASE_URL)
    return openai.OpenAI(api_key=api_key, base_url=base_url)


def _load_trajectory(path):
    """Load an ATIF trajectory JSON file."""
    with open(path) as f:
        return json.load(f)


def _atif_to_messages(trajectory, up_to_step=None):
    """Convert ATIF trajectory steps to Chat Completions messages.

    ATIF format:
      - step.source = "system" → system message
      - step.source = "user"  → user message
      - step.source = "agent" → assistant message (with optional tool_calls)
      - step.tool_calls[].result → tool response messages

    Returns a list of Chat Completions messages.
    """
    messages = []
    steps = trajectory["steps"]

    for step in steps:
        step_id = step["step_id"]
        if up_to_step is not None and step_id > up_to_step:
            break

        source = step.get("source", "")
        msg_text = step.get("message", "")
        tool_calls = step.get("tool_calls", [])

        if source == "system":
            messages.append({"role": "system", "content": msg_text})

        elif source == "user":
            messages.append({"role": "user", "content": msg_text})

        elif source == "agent":
            if tool_calls:
                # Build assistant message with tool_calls
                tc_list = []
                for i, tc in enumerate(tool_calls):
                    # ATIF uses tool_call_id/function_name; standard uses id/name
                    tc_id = tc.get("tool_call_id") or tc.get("id", f"call_{step_id}_{i}")
                    tc_name = tc.get("function_name") or tc.get("name", "unknown")
                    tc_list.append({
                        "id": tc_id,
                        "type": "function",
                        "function": {
                            "name": tc_name,
                            "arguments": json.dumps(tc.get("arguments", {})),
                        },
                    })

                assistant_msg = {"role": "assistant", "tool_calls": tc_list}
                if msg_text:
                    assistant_msg["content"] = msg_text
                messages.append(assistant_msg)

                # Add tool response messages
                for i, tc in enumerate(tool_calls):
                    tc_id = tc.get("tool_call_id") or tc.get("id", f"call_{step_id}_{i}")
                    result = tc.get("result", "")
                    # Truncate very long tool results to save tokens
                    if isinstance(result, str) and len(result) > 4000:
                        result = result[:3800] + f"\n... [truncated, {len(result)} chars total]"
                    messages.append({
                        "role": "tool",
                        "tool_call_id": tc_id,
                        "content": str(result),
                    })
            else:
                messages.append({"role": "assistant", "content": msg_text or ""})

    return messages


def _get_tool_definitions():
    """Minimal tool definitions for lace agent tools."""
    return [
        {"type": "function", "function": {"name": "bash", "parameters": {"type": "object", "properties": {"command": {"type": "string"}, "description": {"type": "string"}}, "required": ["command"]}}},
        {"type": "function", "function": {"name": "file_read", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "offset": {"type": "integer"}, "limit": {"type": "integer"}}, "required": ["path"]}}},
        {"type": "function", "function": {"name": "file_write", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}, "required": ["path", "content"]}}},
        {"type": "function", "function": {"name": "file_edit", "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "old_text": {"type": "string"}, "new_text": {"type": "string"}}, "required": ["path", "old_text", "new_text"]}}},
        {"type": "function", "function": {"name": "ripgrep_search", "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}, "path": {"type": "string"}, "glob": {"type": "string"}}, "required": ["pattern"]}}},
        {"type": "function", "function": {"name": "file_find", "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}, "path": {"type": "string"}}, "required": ["pattern"]}}},
        {"type": "function", "function": {"name": "done", "parameters": {"type": "object", "properties": {"reason": {"type": "string"}}, "required": ["reason"]}}},
    ]


def _replace_system_prompt(messages, new_prompt):
    """Replace the system message in a message list."""
    result = []
    replaced = False
    for msg in messages:
        if not replaced and msg["role"] == "system":
            result.append({"role": "system", "content": new_prompt})
            replaced = True
        else:
            result.append(msg)
    return result


def _summarize_step(step):
    """One-line summary of what happened at a step."""
    tc = step.get("tool_calls", [])
    if tc:
        calls = []
        for c in tc:
            name = c.get("function_name") or c.get("name", "?")
            args = json.dumps(c.get("arguments", {}))[:80]
            calls.append(f"{name}({args})")
        return "; ".join(calls)
    msg = step.get("message", "")
    return msg[:120] if msg else "(empty)"


def _atif_to_narrative(trajectory, up_to_step=None):
    """Convert ATIF trajectory to a narrative text for interrogation.

    Instead of using Chat Completions tool_call format (which requires matching
    tool definitions), we render the trajectory as readable text that the model
    can reason about.
    """
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
            # Include system prompt but truncated
            lines.append(f"[SYSTEM PROMPT — {len(msg_text)} chars, starting with:]")
            lines.append(msg_text[:500])
            if len(msg_text) > 500:
                lines.append("... [truncated]")

        elif source == "user":
            lines.append(f"\n[USER MESSAGE — Step {step_id}]")
            lines.append(msg_text)

        elif source == "agent":
            lines.append(f"\n[YOUR ACTION — Step {step_id}]")
            if msg_text:
                lines.append(f"Thinking: {msg_text[:300]}")
            for tc in tool_calls:
                name = tc.get("function_name") or tc.get("name", "?")
                args = tc.get("arguments", {})
                result = tc.get("result", "")
                # Show the call
                if name == "bash":
                    cmd = args.get("command", "")
                    lines.append(f"Called bash: {cmd[:200]}")
                elif name == "file_write":
                    content = args.get("content", "")
                    lines.append(f"Called file_write: {args.get('path', '?')}")
                    lines.append(f"  Content ({len(content)} chars): {content[:300]}")
                    if len(content) > 300:
                        lines.append("  ... [truncated]")
                elif name == "file_read":
                    lines.append(f"Called file_read: {args.get('path', '?')}")
                elif name == "file_edit":
                    lines.append(f"Called file_edit: {args.get('path', '?')}")
                elif name == "done":
                    lines.append(f"Called done: {args.get('reason', '')[:200]}")
                else:
                    lines.append(f"Called {name}: {json.dumps(args)[:200]}")

                # Show result (truncated)
                if result:
                    result_str = str(result)
                    if len(result_str) > 500:
                        result_str = result_str[:450] + f"\n... [{len(result_str)} chars total]"
                    lines.append(f"  Result: {result_str}")

    return "\n".join(lines)


def cmd_interrogate(args):
    """Interrogate mode: ask the model why it did something."""
    trajectory = _load_trajectory(args.trajectory)

    # For interrogation, render as narrative text (no tool_call format)
    narrative = _atif_to_narrative(trajectory, up_to_step=args.up_to_step)

    # Show what we're working with
    steps = trajectory["steps"]
    print(f"Trajectory: {len(steps)} steps, model: {trajectory['agent']['model_name']}")
    if args.up_to_step:
        print(f"Truncated to step {args.up_to_step}")
    print()

    # Show the steps for context
    if args.show_steps:
        for s in steps:
            if args.up_to_step and s["step_id"] > args.up_to_step:
                break
            print(f"  step {s['step_id']} [{s['source']}]: {_summarize_step(s)}")
        print()

    # Build messages: system context + narrative + question
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
        print(f"Tokens: {response.usage.prompt_tokens} in / {response.usage.completion_tokens} out")


def cmd_replay(args):
    """Replay mode: what would the model do at this step with a different prompt?"""
    trajectory = _load_trajectory(args.trajectory)

    # Get messages up to the decision point (exclusive)
    messages = _atif_to_messages(trajectory, up_to_step=args.at_step - 1)

    # Replace system prompt if persona provided
    if args.persona:
        persona_text = Path(args.persona).read_text()
        messages = _replace_system_prompt(messages, persona_text)
        print(f"Persona: {args.persona}")

    # Show original action at this step
    steps = trajectory["steps"]
    target_step = next((s for s in steps if s["step_id"] == args.at_step), None)
    if target_step:
        print(f"Original step {args.at_step}: {_summarize_step(target_step)}")
    print()

    if args.dry_run:
        print(f"Would send {len(messages)} messages to {args.model}")
        return

    client = _get_client()
    tools = _get_tool_definitions()

    print(f"Replaying step {args.at_step} with {args.model}...")

    kwargs = {
        "model": args.model,
        "messages": messages,
        "tools": tools,
        "max_tokens": 4000,
        "temperature": 0.3,
    }
    if args.tool_choice:
        kwargs["tool_choice"] = args.tool_choice

    response = client.chat.completions.create(**kwargs)

    choice = response.choices[0]
    if choice.message.tool_calls:
        print("NEW action(s):")
        for tc in choice.message.tool_calls:
            args_str = tc.function.arguments[:200]
            print(f"  {tc.function.name}({args_str})")
    if choice.message.content:
        print(f"NEW text: {choice.message.content[:500]}")

    print()
    if response.usage:
        print(f"Tokens: {response.usage.prompt_tokens} in / {response.usage.completion_tokens} out")


def cmd_compare(args):
    """Compare mode: replay the same decision point with two personas."""
    trajectory = _load_trajectory(args.trajectory)
    messages_base = _atif_to_messages(trajectory, up_to_step=args.at_step - 1)

    # Show original
    steps = trajectory["steps"]
    target_step = next((s for s in steps if s["step_id"] == args.at_step), None)
    if target_step:
        print(f"Original step {args.at_step}: {_summarize_step(target_step)}")
    print()

    if args.dry_run:
        print(f"Would send {len(messages_base)} messages x2 to {args.model}")
        return

    client = _get_client()
    tools = _get_tool_definitions()

    for label, persona_path in [("A", args.persona_a), ("B", args.persona_b)]:
        persona_text = Path(persona_path).read_text()
        messages = _replace_system_prompt(list(messages_base), persona_text)

        print(f"--- Persona {label}: {persona_path} ---")

        kwargs = {
            "model": args.model,
            "messages": messages,
            "tools": tools,
            "max_tokens": 4000,
            "temperature": 0.3,
        }

        response = client.chat.completions.create(**kwargs)
        choice = response.choices[0]

        if choice.message.tool_calls:
            for tc in choice.message.tool_calls:
                args_str = tc.function.arguments[:200]
                print(f"  {tc.function.name}({args_str})")
        if choice.message.content:
            print(f"  Text: {choice.message.content[:300]}")
        if response.usage:
            print(f"  Tokens: {response.usage.prompt_tokens} in / {response.usage.completion_tokens} out")
        print()


def cmd_nudge(args):
    """Nudge mode: replay from a step but inject a message first.

    This lets you test whether a specific intervention (e.g., "don't use
    pyOpenSSL") changes the model's behavior at a decision point.
    """
    trajectory = _load_trajectory(args.trajectory)

    # Get messages up to the decision point (exclusive)
    messages = _atif_to_messages(trajectory, up_to_step=args.at_step - 1)

    # Replace system prompt if persona provided
    if args.persona:
        persona_text = Path(args.persona).read_text()
        messages = _replace_system_prompt(messages, persona_text)

    # Inject the nudge as a user message right before the decision
    messages.append({
        "role": "user",
        "content": args.nudge,
    })

    # Show original action
    steps = trajectory["steps"]
    target_step = next((s for s in steps if s["step_id"] == args.at_step), None)
    if target_step:
        print(f"Original step {args.at_step}: {_summarize_step(target_step)}")
    print(f"Nudge: {args.nudge}")
    print()

    if args.dry_run:
        print(f"Would send {len(messages)} messages to {args.model}")
        return

    client = _get_client()
    tools = _get_tool_definitions()

    reps = args.reps or 1
    for i in range(reps):
        if reps > 1:
            print(f"--- Rep {i+1}/{reps} ---")

        kwargs = {
            "model": args.model,
            "messages": messages,
            "tools": tools,
            "max_tokens": 4000,
            "temperature": 0.7 if reps > 1 else 0.3,
        }

        response = client.chat.completions.create(**kwargs)
        choice = response.choices[0]

        if choice.message.tool_calls:
            for tc in choice.message.tool_calls:
                args_str = tc.function.arguments[:200]
                print(f"  {tc.function.name}({args_str})")
        if choice.message.content:
            print(f"  Text: {choice.message.content[:300]}")
        if response.usage:
            print(f"  Tokens: {response.usage.prompt_tokens} in / {response.usage.completion_tokens} out")
        print()


def cmd_steps(args):
    """Show step summaries from a trajectory (for picking decision points)."""
    trajectory = _load_trajectory(args.trajectory)
    steps = trajectory["steps"]
    print(f"Trajectory: {len(steps)} steps")
    print(f"Model: {trajectory['agent']['model_name']}")
    print(f"Persona: {trajectory['agent'].get('persona', '?')}")
    print()
    for s in steps:
        marker = ""
        tc = s.get("tool_calls", [])
        if tc:
            for c in tc:
                name = c.get("function_name") or c.get("name", "")
                args_str = json.dumps(c.get("arguments", {}))
                # Flag interesting decisions
                if "pip install" in args_str or "pip3 install" in args_str:
                    marker = " <<<< PIP INSTALL"
                elif "import " in args_str and "from " in args_str:
                    marker = " <<<< IMPORT"
                elif name == "done":
                    marker = " <<<< DONE"
        print(f"  step {s['step_id']:3d} [{s['source']:6s}]: {_summarize_step(s)}{marker}")


def main():
    parser = argparse.ArgumentParser(
        description="Interrogate and replay agent trajectories"
    )
    parser.add_argument("--model", default=DEFAULT_MODEL,
                        help=f"Model name (default: {DEFAULT_MODEL})")

    sub = parser.add_subparsers(dest="command", required=True)

    # interrogate
    p_int = sub.add_parser("interrogate", help="Ask the model about its own trajectory")
    p_int.add_argument("trajectory", help="Path to ATIF trajectory JSON")
    p_int.add_argument("--question", "-q", required=True,
                       help="Question to ask about the trajectory")
    p_int.add_argument("--up-to-step", type=int,
                       help="Only include steps up to this number")
    p_int.add_argument("--show-steps", action="store_true",
                       help="Print step summaries before asking")
    p_int.add_argument("--dry-run", action="store_true")

    # replay
    p_rep = sub.add_parser("replay", help="Replay a decision point with different prompt")
    p_rep.add_argument("trajectory", help="Path to ATIF trajectory JSON")
    p_rep.add_argument("--at-step", type=int, required=True,
                       help="Step number to replay (exclusive — replays what happens AT this step)")
    p_rep.add_argument("--persona", help="Path to replacement persona file")
    p_rep.add_argument("--tool-choice", help="Tool choice: auto, required, none")
    p_rep.add_argument("--dry-run", action="store_true")

    # compare
    p_cmp = sub.add_parser("compare", help="Compare two personas at a decision point")
    p_cmp.add_argument("trajectory", help="Path to ATIF trajectory JSON")
    p_cmp.add_argument("--at-step", type=int, required=True)
    p_cmp.add_argument("--persona-a", required=True)
    p_cmp.add_argument("--persona-b", required=True)
    p_cmp.add_argument("--dry-run", action="store_true")

    # nudge
    p_nudge = sub.add_parser("nudge", help="Replay with an injected message before the decision")
    p_nudge.add_argument("trajectory", help="Path to ATIF trajectory JSON")
    p_nudge.add_argument("--at-step", type=int, required=True)
    p_nudge.add_argument("--nudge", "-n", required=True,
                         help="Message to inject before the decision point")
    p_nudge.add_argument("--persona", help="Path to replacement persona file")
    p_nudge.add_argument("--reps", type=int, help="Number of repetitions (uses temp=0.7)")
    p_nudge.add_argument("--dry-run", action="store_true")

    # steps
    p_steps = sub.add_parser("steps", help="Show step summaries from a trajectory")
    p_steps.add_argument("trajectory", help="Path to ATIF trajectory JSON")

    args = parser.parse_args()

    if args.command == "interrogate":
        cmd_interrogate(args)
    elif args.command == "replay":
        cmd_replay(args)
    elif args.command == "compare":
        cmd_compare(args)
    elif args.command == "nudge":
        cmd_nudge(args)
    elif args.command == "steps":
        cmd_steps(args)


if __name__ == "__main__":
    main()
