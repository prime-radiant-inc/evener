#!/usr/bin/env python3
"""Parse Codex JSONL output and write a result JSON compatible with serfeval format."""
import json
import sys
import os

def parse_codex_jsonl(jsonl_path, task_file, diff_text, duration, output_path):
    total_input = 0
    total_output = 0
    turn_count = 0
    last_message = ""
    errors = []

    with open(jsonl_path) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except json.JSONDecodeError:
                continue

            etype = event.get("type", "")

            if etype == "turn.completed":
                turn_count += 1
                usage = event.get("usage", {})
                total_input += usage.get("input_tokens", 0)
                total_output += usage.get("output_tokens", 0)

            if etype == "item.completed":
                item = event.get("item", {})
                if item.get("type") == "agent_message":
                    last_message = item.get("text", "")

            if etype == "error":
                errors.append(event.get("message", ""))

            if etype == "turn.failed":
                err = event.get("error", {})
                errors.append(err.get("message", ""))

    with open(task_file) as f:
        task_text = f.read()

    result = {
        "strategy": "codex",
        "model": "gpt-5.3-codex",
        "task": task_text,
        "completed": turn_count > 0 and len(errors) == 0,
        "turn_count": turn_count,
        "total_input_tokens": total_input,
        "total_output_tokens": total_output,
        "total_tokens": total_input + total_output,
        "duration_seconds": duration,
        "result": last_message,
        "diff": diff_text,
        "retention_score": None,
        "retention_breakdown": None,
    }
    if errors:
        result["errors"] = errors

    with open(output_path, "w") as f:
        json.dump(result, f, indent=2)

    print(f"  tokens={result['total_tokens']} turns={turn_count} duration={duration}s completed={result['completed']}")


if __name__ == "__main__":
    jsonl_path = sys.argv[1]
    task_file = sys.argv[2]
    diff_file = sys.argv[3]  # path to file containing diff text
    duration = int(sys.argv[4])
    output_path = sys.argv[5]

    if os.path.exists(diff_file):
        with open(diff_file) as f:
            diff_text = f.read()
    else:
        diff_text = ""

    parse_codex_jsonl(jsonl_path, task_file, diff_text, duration, output_path)
