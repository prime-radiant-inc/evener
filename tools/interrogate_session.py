#!/usr/bin/env python3
"""Interrogate a failed agent session by replaying its context and asking questions.

Downloads the transcript from S3, reconstructs the conversation, appends
interrogation questions, and sends to the same model. The model's self-report
reveals which instructions it noticed, how it prioritized them, and what
influenced its decisions.

Usage:
    # Interrogate with default questions
    python3 tools/interrogate_session.py \
        --run disc-3rep-v6 --rep 8 --task adaptive-rejection-sampler

    # Custom questions
    python3 tools/interrogate_session.py \
        --run disc-3rep-v6 --rep 8 --task adaptive-rejection-sampler \
        --question "Why did you choose to work directly instead of delegating?" \
        --question "What in the system prompt influenced your approach?"

    # Compare two reps (passing vs failing)
    python3 tools/interrogate_session.py \
        --run disc-3rep-v6 --rep 8 --task adaptive-rejection-sampler \
        --compare-run disc-3rep-v6 --compare-rep 64
"""

import argparse
import json
import os
import re
import subprocess
import sys
import tempfile

REGION = "us-west-1"
BUCKET = "harbor-eval-results-526275945504"


def s3_ls(prefix):
    r = subprocess.run(
        ["aws", "s3", "ls", f"s3://{BUCKET}/{prefix}", "--region", REGION, "--recursive"],
        capture_output=True, text=True,
    )
    return [line.split()[-1] for line in r.stdout.strip().split("\n") if line.strip()]


def s3_cat(path):
    r = subprocess.run(
        ["aws", "s3", "cp", f"s3://{BUCKET}/{path}", "-", "--region", REGION],
        capture_output=True, text=True,
    )
    return r.stdout


def find_coordinator_transcript(run_id, rep):
    """Find the coordinator transcript (no parent) for a given rep."""
    files = s3_ls(f"runs/{run_id}/rep-{rep}/")
    transcripts = [f for f in files if "transcript.jsonl" in f]

    for path in transcripts:
        content = s3_cat(path)
        if not content.strip():
            continue
        header = json.loads(content.split("\n")[0])
        parent = header.get("parent_session_id")
        if not parent:
            return path, content
    return None, None


def parse_transcript(content):
    """Parse transcript into header + conversation entries."""
    lines = content.strip().split("\n")
    header = json.loads(lines[0])
    entries = []
    for line in lines[1:]:
        data = json.loads(line)
        if data.get("kind") == "entry":
            entries.append(data)
    return header, entries


def summarize_tool_flow(entries, max_calls=15):
    """Extract tool call sequence from transcript entries."""
    calls = []
    for entry in entries:
        turn = entry.get("turn", {})
        msg = turn.get("message", {})
        content = msg.get("content", [])
        if not isinstance(content, list):
            continue
        for c in content:
            if c.get("kind") == "tool_call":
                tc = c.get("tool_call", {})
                name = tc.get("name", "?")
                args = tc.get("arguments", {})
                if name == "spawn_agent":
                    detail = f"type={args.get('agent_type', '?')}"
                elif name in ("exec_command", "shell"):
                    detail = args.get("command", "")[:50]
                elif name == "read_file":
                    detail = args.get("path", args.get("file_path", ""))[:50]
                elif name == "communicate":
                    detail = args.get("message", "")[:50]
                else:
                    detail = ""
                calls.append(f"{name}({detail})" if detail else name)
                if len(calls) >= max_calls:
                    return calls
    return calls


def get_verifier_output(run_id, rep):
    """Get the verifier test output for a rep."""
    files = s3_ls(f"runs/{run_id}/rep-{rep}/")
    for f in files:
        if "test-stdout.txt" in f:
            return s3_cat(f)
    return None


def interrogate(system_prompt, task_message, tool_flow, verifier_output, questions, model="gpt-5.4-mini"):
    """Send interrogation to the model."""
    from openai import OpenAI
    client = OpenAI()

    messages = [
        {"role": "system", "content": system_prompt},
        {"role": "user", "content": task_message},
        {"role": "assistant", "content": f"I executed the following tool calls: {', '.join(tool_flow[:10])}"},
    ]

    verifier_context = ""
    if verifier_output:
        # Extract just the test results
        lines = verifier_output.strip().split("\n")
        test_lines = [l for l in lines if "PASS" in l or "FAIL" in l or "assert" in l.lower()]
        verifier_context = f"\n\nVerifier results:\n" + "\n".join(test_lines[-10:])

    question_text = "\n".join(f"{i+1}. {q}" for i, q in enumerate(questions))

    messages.append({
        "role": "user",
        "content": f"""STOP. I'm the system operator reviewing your session. The verifier says your solution failed.{verifier_context}

Your tool call sequence was: {', '.join(tool_flow)}

I need honest answers about your decision-making:
{question_text}

Be specific about which instructions in your system prompt influenced each decision.""",
    })

    response = client.responses.create(
        model=model,
        input=messages,
        reasoning={"effort": "high"},
    )

    result = []
    for block in response.output:
        if hasattr(block, "content") and block.type == "message":
            for part in block.content:
                if hasattr(part, "text"):
                    result.append(part.text)
    return "\n".join(result)


DEFAULT_QUESTIONS = [
    "Did you delegate to an implementer, or work directly? Why?",
    "What was your first tool call and why did you choose it?",
    "If you delegated, what information did you include in the delegation? What did you leave out?",
    "Did you verify the result before submitting? How thoroughly?",
    "What in your system prompt most influenced your approach to this task?",
]


def main():
    parser = argparse.ArgumentParser(description="Interrogate a failed agent session")
    parser.add_argument("--run", required=True, help="Run ID")
    parser.add_argument("--rep", required=True, type=int, help="Rep number")
    parser.add_argument("--task", required=True, help="Task name (for display)")
    parser.add_argument("--question", action="append", help="Custom question (repeatable)")
    parser.add_argument("--compare-run", help="Run ID of passing rep to compare")
    parser.add_argument("--compare-rep", type=int, help="Rep number of passing rep")
    parser.add_argument("--model", default="gpt-5.4-mini", help="Model for interrogation")
    args = parser.parse_args()

    questions = args.question or DEFAULT_QUESTIONS

    print(f"=== Interrogating {args.task} (run={args.run}, rep={args.rep}) ===\n")

    # Download failing transcript
    print("Downloading failing transcript...")
    path, content = find_coordinator_transcript(args.run, args.rep)
    if not content:
        print(f"ERROR: No coordinator transcript found for rep {args.rep}")
        sys.exit(1)

    header, entries = parse_transcript(content)
    system_prompt = header.get("system_prompt", "")
    tool_flow = summarize_tool_flow(entries)

    # Get task message (first user input)
    task_message = ""
    for entry in entries:
        turn = entry.get("turn", {})
        if turn.get("kind") == "USER_INPUT":
            msg = turn.get("message", {})
            content = msg.get("content", [])
            if isinstance(content, list):
                for c in content:
                    if c.get("kind") == "text":
                        task_message = c["text"]
                        break
            break

    print(f"System prompt: {len(system_prompt)} chars")
    print(f"Tool flow ({len(tool_flow)} calls): {tool_flow[:8]}")

    # Get verifier output
    verifier = get_verifier_output(args.run, args.rep)
    if verifier:
        print(f"Verifier output: {len(verifier)} chars")

    # If comparing, also get the passing transcript
    if args.compare_run and args.compare_rep:
        print(f"\nDownloading passing transcript (run={args.compare_run}, rep={args.compare_rep})...")
        _, pass_content = find_coordinator_transcript(args.compare_run, args.compare_rep)
        if pass_content:
            _, pass_entries = parse_transcript(pass_content)
            pass_flow = summarize_tool_flow(pass_entries)
            print(f"Passing tool flow ({len(pass_flow)} calls): {pass_flow[:8]}")
            questions.append(
                f"A PASSING run of this same task used this tool sequence: {', '.join(pass_flow[:10])}. "
                f"Why did your approach differ? What would you do differently?"
            )

    # Interrogate
    print("\nSending interrogation...\n")
    response = interrogate(system_prompt, task_message, tool_flow, verifier, questions, args.model)

    print("=== MODEL RESPONSE ===\n")
    print(response)


if __name__ == "__main__":
    main()
