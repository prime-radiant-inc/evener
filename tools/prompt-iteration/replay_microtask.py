"""Replay microtasks against the OpenAI Responses API to test prompt changes.

Loads a microtask JSON (produced by extract_microtasks.py), optionally replaces
the system prompt with a different persona, then calls the OpenAI Responses API
to see what the model would do at that decision point. Compares the new action
with the original behavior.

This is a unit test for agent decisions — freeze the conversation state at a
specific turn, change the prompt, see if the agent's next action improves.

Usage:
    # Single microtask replay
    python3 replay_microtask.py tools/microtasks/build-cython-ext__DokDAoM__step15.json

    # With a different persona
    python3 replay_microtask.py tools/microtasks/build-cython-ext__DokDAoM__step15.json \
        --persona path/to/persona.md

    # Batch mode: replay all microtasks in a directory
    python3 replay_microtask.py tools/microtasks/ --batch

    # Filter batch by category
    python3 replay_microtask.py tools/microtasks/ --batch --category NEVER_SUBMITTED
"""

import argparse
import json
import logging
import os
import sys
from pathlib import Path

import openai

logger = logging.getLogger(__name__)


def _load_microtask(path):
    """Load a microtask JSON file."""
    with open(path) as f:
        return json.load(f)


def _load_persona(persona_path):
    """Load a persona markdown file."""
    with open(persona_path) as f:
        return f.read()


def _replace_system_prompt(input_items, new_prompt):
    """Replace the first developer message (system prompt) in the input array.

    Returns a new list with the replacement applied.
    """
    result = []
    replaced = False
    for item in input_items:
        if not replaced and item.get("type") == "message" and item.get("role") == "developer":
            result.append({
                "type": "message",
                "role": "developer",
                "content": new_prompt,
            })
            replaced = True
        else:
            result.append(item)
    return result


def _load_tool_definitions(tool_defs_path=None):
    """Load tool definitions for the Responses API.

    If a path is provided, loads from that file. Otherwise, returns a minimal
    set of tool definitions covering the tools lace uses in benchmark runs.
    """
    if tool_defs_path and Path(tool_defs_path).is_file():
        with open(tool_defs_path) as f:
            return json.load(f)

    # Minimal tool definitions inferred from lace benchmark usage.
    # These match the tools registered by lace's MCP tool registry.
    return [
        {
            "type": "function",
            "name": "bash",
            "description": "Run a shell command.",
            "parameters": {
                "type": "object",
                "properties": {
                    "command": {"type": "string", "description": "The shell command to run."},
                },
                "required": ["command"],
            },
        },
        {
            "type": "function",
            "name": "file_read",
            "description": "Read the contents of a file.",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Absolute path to the file."},
                    "offset": {"type": "integer", "description": "Line offset to start reading from."},
                    "limit": {"type": "integer", "description": "Maximum number of lines to read."},
                },
                "required": ["path"],
            },
        },
        {
            "type": "function",
            "name": "file_write",
            "description": "Write content to a file, creating it if it doesn't exist.",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Absolute path to the file."},
                    "content": {"type": "string", "description": "Content to write."},
                },
                "required": ["path", "content"],
            },
        },
        {
            "type": "function",
            "name": "file_edit",
            "description": "Edit a file by replacing old text with new text.",
            "parameters": {
                "type": "object",
                "properties": {
                    "path": {"type": "string", "description": "Absolute path to the file."},
                    "old_text": {"type": "string", "description": "Text to find and replace."},
                    "new_text": {"type": "string", "description": "Replacement text."},
                },
                "required": ["path", "old_text", "new_text"],
            },
        },
        {
            "type": "function",
            "name": "ripgrep_search",
            "description": "Search for a pattern in files using ripgrep.",
            "parameters": {
                "type": "object",
                "properties": {
                    "pattern": {"type": "string", "description": "Regex pattern to search for."},
                    "path": {"type": "string", "description": "Directory or file to search in."},
                    "glob": {"type": "string", "description": "File glob pattern to filter."},
                },
                "required": ["pattern"],
            },
        },
        {
            "type": "function",
            "name": "file_find",
            "description": "Find files matching a glob pattern.",
            "parameters": {
                "type": "object",
                "properties": {
                    "pattern": {"type": "string", "description": "Glob pattern to match."},
                    "path": {"type": "string", "description": "Directory to search in."},
                },
                "required": ["pattern"],
            },
        },
        {
            "type": "function",
            "name": "url_fetch",
            "description": "Fetch the content of a URL.",
            "parameters": {
                "type": "object",
                "properties": {
                    "url": {"type": "string", "description": "URL to fetch."},
                },
                "required": ["url"],
            },
        },
    ]


def replay_microtask(microtask, persona_text=None, tool_defs=None,
                     model_override=None, dry_run=False,
                     tool_choice=None):
    """Replay a microtask against the OpenAI Responses API.

    Args:
        microtask: Microtask dict (loaded from JSON).
        persona_text: Optional replacement system prompt text.
        tool_defs: Optional tool definitions list.
        model_override: Optional model name override.
        dry_run: If True, don't call the API — just print what would be sent.
        tool_choice: Optional tool_choice value ("auto", "required", "none",
            or a specific tool name).

    Returns:
        Dict with replay results: new_action, input_tokens, output_tokens, etc.
    """
    input_items = microtask["input"]

    # Replace system prompt if persona provided
    if persona_text:
        input_items = _replace_system_prompt(input_items, persona_text)

    model = model_override or microtask.get("model", "gpt-5.2-codex")
    tools = tool_defs or _load_tool_definitions()

    if dry_run:
        n_items = len(input_items)
        approx_chars = sum(len(json.dumps(item)) for item in input_items)
        return {
            "microtask_id": microtask["id"],
            "dry_run": True,
            "model": model,
            "n_input_items": n_items,
            "approx_input_chars": approx_chars,
            "n_tools": len(tools),
            "persona_replaced": persona_text is not None,
        }

    client = openai.OpenAI()

    # Build reasoning config
    reasoning = {"effort": "high", "summary": "auto"}

    kwargs = {
        "model": model,
        "input": input_items,
        "tools": tools,
        "reasoning": reasoning,
        "store": False,
    }
    if tool_choice:
        kwargs["tool_choice"] = tool_choice

    response = client.responses.create(**kwargs)

    # Extract the model's next action from the response
    new_actions = []
    response_text = ""
    reasoning_summaries = []

    for item in response.output:
        if item.type == "function_call":
            args_str = item.arguments[:150] if item.arguments else ""
            new_actions.append(f"Called {item.name}({args_str})")
        elif item.type == "message":
            for content in item.content:
                if hasattr(content, "text"):
                    response_text += content.text
        elif item.type == "reasoning":
            if item.summary:
                for s in item.summary:
                    if hasattr(s, "text"):
                        reasoning_summaries.append(s.text)

    if not new_actions and response_text:
        new_actions.append(f"Said: {response_text[:200]}")

    return {
        "microtask_id": microtask["id"],
        "model": model,
        "new_actions": new_actions,
        "response_text": response_text[:500] if response_text else "",
        "reasoning_summaries": reasoning_summaries,
        "original_action": microtask.get("actual_next_action", ""),
        "persona_replaced": persona_text is not None,
        "input_tokens": response.usage.input_tokens if response.usage else 0,
        "output_tokens": response.usage.output_tokens if response.usage else 0,
    }


def _format_result(result, microtask):
    """Format a replay result for display."""
    lines = []
    lines.append(f"=== {result['microtask_id']} ===")
    lines.append(f"Category: {microtask['failure_category']}")
    lines.append(f"Decision point: step {microtask['decision_step']}/{microtask['total_steps']}")
    lines.append(f"Model: {result['model']}")

    if result.get("dry_run"):
        lines.append(f"[DRY RUN] Would send {result['n_input_items']} input items "
                      f"(~{result['approx_input_chars']} chars) with {result['n_tools']} tools")
        return "\n".join(lines)

    lines.append("")
    lines.append(f"ORIGINAL action: {result['original_action']}")
    lines.append("")
    if result["new_actions"]:
        lines.append("NEW action(s):")
        for action in result["new_actions"]:
            lines.append(f"  {action}")
    elif result["response_text"]:
        lines.append(f"NEW response: {result['response_text'][:300]}")
    else:
        lines.append("NEW action: (empty response)")

    if result.get("reasoning_summaries"):
        lines.append("\nREASONING:")
        for summary in result["reasoning_summaries"]:
            for line in summary.split("\n"):
                lines.append(f"  {line}")

    if result.get("input_tokens"):
        lines.append(f"\nTokens: {result['input_tokens']} in / {result['output_tokens']} out")

    # Simple verdict
    original = result["original_action"].lower()
    new = " ".join(result["new_actions"]).lower() if result["new_actions"] else ""

    if "done" in original or "said:" in original:
        if new and "done" not in new and "said:" not in new:
            lines.append("\nVERDICT: CHANGED (agent now takes action instead of declaring done)")
        elif new and ("done" in new or "said:" in new):
            lines.append("\nVERDICT: SAME (agent still declares done)")
        else:
            lines.append("\nVERDICT: UNCLEAR")
    else:
        lines.append("\nVERDICT: DIFFERENT (original was a tool call too — compare manually)")

    return "\n".join(lines)


def main():
    parser = argparse.ArgumentParser(
        description="Replay microtasks against the OpenAI Responses API"
    )
    parser.add_argument("path", help="Microtask JSON file or directory (with --batch)")
    parser.add_argument("--persona", help="Path to replacement persona/system prompt file")
    parser.add_argument("--model", help="Override model name")
    parser.add_argument("--batch", action="store_true", help="Process all microtasks in directory")
    parser.add_argument("--category", help="Filter batch by failure category")
    parser.add_argument("--task", help="Filter batch by task name")
    parser.add_argument("--tool-defs", help="Path to tool definitions JSON file")
    parser.add_argument("--dry-run", action="store_true",
                        help="Don't call API, just show what would be sent")
    parser.add_argument("--limit", type=int, help="Maximum microtasks to process in batch")
    parser.add_argument("--tool-choice",
                        help="Tool choice mode: auto, required, none, or a tool name")
    parser.add_argument("--verbose", "-v", action="store_true")

    args = parser.parse_args()

    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(levelname)s: %(message)s",
    )

    # Load persona if provided
    persona_text = None
    if args.persona:
        persona_text = _load_persona(args.persona)
        logger.info("Loaded persona from %s (%d chars)", args.persona, len(persona_text))

    # Load tool definitions
    tool_defs = _load_tool_definitions(args.tool_defs)

    path = Path(args.path)

    if args.batch or path.is_dir():
        if not path.is_dir():
            logger.error("--batch requires a directory path")
            sys.exit(1)

        # Collect microtask files
        files = sorted(path.glob("*.json"))
        microtasks = []
        for f in files:
            mt = _load_microtask(f)
            if args.category and mt.get("failure_category") != args.category:
                continue
            if args.task and mt.get("task_name") != args.task:
                continue
            microtasks.append(mt)

        if args.limit:
            microtasks = microtasks[:args.limit]

        logger.info("Replaying %d microtasks", len(microtasks))

        results = []
        for mt in microtasks:
            try:
                result = replay_microtask(mt, persona_text, tool_defs,
                                          args.model, args.dry_run,
                                          args.tool_choice)
                results.append((result, mt))
                print(_format_result(result, mt))
                print()
            except Exception as e:
                logger.error("Failed to replay %s: %s", mt["id"], e)

        # Batch summary
        if results and not args.dry_run:
            changed = sum(1 for r, _ in results
                         if "CHANGED" in _format_result(r, _))
            same = sum(1 for r, _ in results
                      if "SAME" in _format_result(r, _))
            print(f"\n{'='*60}")
            print(f"BATCH SUMMARY: {len(results)} replayed, "
                  f"{changed} CHANGED, {same} SAME")

    else:
        # Single microtask
        mt = _load_microtask(path)
        result = replay_microtask(mt, persona_text, tool_defs,
                                  args.model, args.dry_run,
                                  args.tool_choice)
        print(_format_result(result, mt))


if __name__ == "__main__":
    main()
