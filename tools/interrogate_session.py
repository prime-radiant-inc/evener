#!/usr/bin/env python3
"""Interrogate an agent session by resuming it with serf and asking questions.

Uses serf's --resume-with to replay the FULL conversation history, then appends
interrogation questions. The model is placed back into the exact context of its
original session — same system prompt, same tool calls, same results.

Usage:
    # List sessions for a rep
    python3 tools/interrogate_session.py \
        --run v10-deleg-goldplate --rep 1 --task chess-best-move --list-sessions

    # Interrogate coordinator (default)
    python3 tools/interrogate_session.py \
        --run v10-deleg-goldplate --rep 3 --task chess-best-move \
        --question "Why did you not delegate?"

    # Interrogate a subagent by session ID (prefix match)
    python3 tools/interrogate_session.py \
        --run v10-deleg-goldplate --rep 1 --task chess-best-move \
        --session 01KMPF5M \
        --question "Why did you override the computational proof?"

    # Interrogate subagent by index (from --list-sessions output)
    python3 tools/interrogate_session.py \
        --run v10-deleg-goldplate --rep 1 --task chess-best-move \
        --session 2 \
        --question "Why did you override the computational proof?"
"""

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

REGION = "us-west-1"
BUCKET = "harbor-eval-results-526275945504"
RESULTS_DIR = os.path.expanduser("~/prime-radiant/harbor-runner/state/results")
SERF_BINARY = os.path.expanduser("~/prime-radiant/serf/serf-linux-amd64")


def _extract_task_name(dirname):
    """Extract task name from a 'taskname__hash' directory name."""
    return re.sub(r"__[A-Za-z0-9]+$", "", dirname)


def find_local_state_dir(run_id, rep, task=None):
    """Find the agent-state dir in locally downloaded results."""
    rep_dir = os.path.join(RESULTS_DIR, run_id, f"rep-{rep}")
    if not os.path.isdir(rep_dir):
        return None
    # Walk to find agent-state/sessions/, filtering by task name if provided.
    for root, dirs, files in os.walk(rep_dir):
        if os.path.basename(root) == "agent-state" and "sessions" in dirs:
            if task:
                # Match exact task name from taskname__hash directory
                parts = root.replace("\\", "/").split("/")
                task_dir_name = None
                for part in parts:
                    if _extract_task_name(part) == task and part != task:
                        task_dir_name = part
                        break
                if not task_dir_name:
                    continue
            return root
    return None


def download_results(run_id, rep, task=None):
    """Download results from S3 if not already local."""
    state_dir = find_local_state_dir(run_id, rep, task)
    if state_dir:
        return state_dir

    dest = os.path.join(RESULTS_DIR, run_id, f"rep-{rep}")
    s3_prefix = f"s3://{BUCKET}/runs/{run_id}/rep-{rep}/"

    # Download agent-state for the target task
    print(f"Downloading from S3 to {dest}...")
    cmd = [
        "aws", "s3", "sync", s3_prefix, dest,
        "--region", REGION,
        "--exclude", "*",
        "--include", f"*{task}*/agent-state/*" if task else "*agent-state/*",
    ]
    subprocess.run(cmd, check=True)

    # Also download verifier output for the target task
    if task:
        print(f"Downloading verifier output for {task}...")
        subprocess.run([
            "aws", "s3", "sync", s3_prefix, dest,
            "--region", REGION,
            "--exclude", "*",
            "--include", f"*{task}*/verifier/test-stdout.txt",
        ], check=True)

    return find_local_state_dir(run_id, rep, task)


def list_sessions(state_dir):
    """List all sessions in a state dir with metadata."""
    sessions_dir = os.path.join(state_dir, "sessions")
    sessions = []
    for f in sorted(os.listdir(sessions_dir)):
        if not f.endswith(".meta.json"):
            continue
        meta = json.load(open(os.path.join(sessions_dir, f)))
        session_id = meta["id"]

        # Check transcript for parent and role info
        transcript_path = os.path.join(sessions_dir, f"{session_id}.transcript.jsonl")
        parent = None
        turn_count = meta.get("turn_count", 0)
        task_preview = ""
        if os.path.exists(transcript_path):
            header = json.loads(open(transcript_path).readline())
            parent = header.get("parent_session_id")
            task_text = header.get("task", "")
            if task_text:
                task_preview = task_text[:80]

        # Infer role from meta config
        prompt = meta.get("config", {}).get("base_prompt_override", "")
        if not parent:
            role = "coordinator"
        elif "You are reviewing" in prompt:
            role = "reviewer"
        else:
            role = "implementer"

        sessions.append({
            "id": session_id,
            "role": role,
            "parent": parent,
            "model": meta.get("model", "?"),
            "turns": turn_count,
            "task_preview": task_preview,
        })
    return sessions


def resolve_session(sessions, selector):
    """Resolve a session selector to a session ID.

    Selector can be:
    - "coordinator" (default) — the session with no parent
    - A 1-based index from the list
    - A session ID prefix
    """
    if selector is None or selector == "coordinator":
        for s in sessions:
            if s["parent"] is None:
                return s
        print("ERROR: No coordinator session found")
        sys.exit(1)

    # Try as integer index (1-based)
    try:
        idx = int(selector)
        if 1 <= idx <= len(sessions):
            return sessions[idx - 1]
        print(f"ERROR: Session index {idx} out of range (1-{len(sessions)})")
        sys.exit(1)
    except ValueError:
        pass

    # Try as session ID prefix
    matches = [s for s in sessions if s["id"].startswith(selector)]
    if len(matches) == 1:
        return matches[0]
    if len(matches) > 1:
        print(f"ERROR: Ambiguous prefix '{selector}', matches: {[m['id'] for m in matches]}")
        sys.exit(1)
    print(f"ERROR: No session matching '{selector}'")
    sys.exit(1)


def get_verifier_output(run_id, rep, task=None):
    """Get verifier output from local results."""
    rep_dir = os.path.join(RESULTS_DIR, run_id, f"rep-{rep}")
    for root, dirs, files in os.walk(rep_dir):
        if task:
            # Match exact task name from the taskname__hash directory
            dir_name = os.path.basename(root)
            parent_name = os.path.basename(os.path.dirname(root))
            # test-stdout.txt lives at taskname__hash/verifier/test-stdout.txt
            # so when we're in the verifier dir, check the parent
            if dir_name == "verifier":
                if _extract_task_name(parent_name) != task:
                    continue
            else:
                continue
        for f in files:
            if f == "test-stdout.txt":
                return open(os.path.join(root, f)).read()
    return None


def fix_orphaned_tool_calls(state_dir, session_id):
    """Patch transcript if the last assistant message has unanswered tool calls.

    Sessions that timed out mid-tool-call have a pending function_call with no
    tool result. The OpenAI API rejects resume with "No tool output found for
    function call". We append synthetic tool results so the resume can proceed.

    Operates on the copy in the temp dir, not the original.
    """
    transcript_path = os.path.join(
        state_dir, "sessions", f"{session_id}.transcript.jsonl")
    if not os.path.exists(transcript_path):
        return

    with open(transcript_path) as f:
        lines = f.readlines()

    if not lines:
        return

    # Collect tool_call IDs from the last assistant turn and tool_result IDs
    # from any subsequent tool turns. Walk backward to find the last assistant
    # message that contains tool calls.
    last_assistant_idx = None
    pending_call_ids = []  # tool_call IDs from the last assistant message
    answered_call_ids = set()  # tool_call_ids from subsequent tool results

    for i in range(len(lines) - 1, -1, -1):
        entry = json.loads(lines[i])
        if "turn" not in entry or "message" not in entry["turn"]:
            continue
        msg = entry["turn"]["message"]
        role = msg.get("role")

        if role == "tool" and last_assistant_idx is None:
            # Tool result after the last assistant — collect its IDs
            for item in (msg.get("content") or []):
                if isinstance(item, dict) and item.get("kind") == "tool_result":
                    tid = item.get("tool_result", {}).get("tool_call_id")
                    if tid:
                        answered_call_ids.add(tid)

        elif role == "assistant":
            # Found the last assistant message — extract tool call IDs
            last_assistant_idx = i
            for item in (msg.get("content") or []):
                if isinstance(item, dict) and item.get("kind") == "tool_call":
                    tid = item.get("tool_call", {}).get("id")
                    name = item.get("tool_call", {}).get("name", "unknown")
                    if tid:
                        pending_call_ids.append((tid, name))
            break

        elif role == "user":
            # User message means no orphaned tool calls possible
            break

    if not pending_call_ids:
        return

    # Find unanswered calls
    unanswered = [(tid, name) for tid, name in pending_call_ids
                  if tid not in answered_call_ids]
    if not unanswered:
        return

    print(f"Fixing {len(unanswered)} orphaned tool call(s) in transcript...")
    # Append synthetic tool results
    with open(transcript_path, "a") as f:
        for tid, name in unanswered:
            seq = len(lines) + 1
            synthetic = {
                "kind": "tool_result",
                "seq": seq,
                "turn": {
                    "kind": "tool_result",
                    "message": {
                        "role": "tool",
                        "content": [{
                            "kind": "tool_result",
                            "tool_result": {
                                "tool_call_id": tid,
                                "name": name,
                                "content": json.dumps({
                                    "error": "Session timed out before this "
                                             "tool call completed."
                                }),
                                "is_error": True,
                            }
                        }]
                    }
                }
            }
            f.write(json.dumps(synthetic) + "\n")
            seq += 1


def build_question_message(questions, verifier_output):
    """Build the interrogation message."""
    parts = [
        "You have completed your work and it is now time for your post-mission "
        "debrief. Your solution was evaluated by a HIDDEN verifier that ran "
        "tests you could NOT see during execution. These tests were mounted "
        "AFTER your session ended — you never had access to them."
    ]

    if verifier_output:
        lines = verifier_output.strip().split("\n")
        test_lines = [l for l in lines
                      if "PASS" in l or "FAIL" in l or "assert" in l.lower()]
        if test_lines:
            parts.append(
                "\nHidden verifier results (you could NOT have run these):\n"
                + "\n".join(test_lines[-10:]))

    parts.append(
        "\nIMPORTANT: These tests were hidden from you. Do NOT suggest that "
        "you should have run them — you could not have. Focus on what you "
        "actually did, what you could have done differently with the "
        "information and tools available to you during your session, and what "
        "instruction changes would have changed your behavior."
        "\n\nAnswer each question thoroughly. For each answer, cite the "
        "specific instructions from your system prompt that influenced "
        "the decision. If two instructions conflicted, explain which "
        "won and why. Then call communicate with your full debrief.")
    for i, q in enumerate(questions):
        parts.append(f"{i+1}. {q}")
    return "\n".join(parts)


DEFAULT_QUESTIONS = [
    "What was your approach and why did you choose it?",
    "Which instructions in your system prompt most influenced your decisions?",
    "Were there any instructions you noticed but chose not to follow? Why?",
    "Given ONLY the information available to you during execution (not the "
    "hidden verifier results above), what would you have done differently?",
]


def main():
    parser = argparse.ArgumentParser(
        description="Interrogate an agent session by resuming it with serf")
    parser.add_argument("--run", required=True, help="Run ID")
    parser.add_argument("--rep", required=True, type=int, help="Rep number")
    parser.add_argument("--task", required=True, help="Task name (for display)")
    parser.add_argument("--session", default=None,
                        help="Session to interrogate: 'coordinator' (default), "
                             "1-based index, or session ID prefix")
    parser.add_argument("--list-sessions", action="store_true",
                        help="List available sessions and exit")
    parser.add_argument("--question", action="append",
                        help="Custom question (repeatable)")
    parser.add_argument("--model", default=None,
                        help="Model override (default: same as original session)")
    parser.add_argument("--serf", default=None,
                        help="Path to serf binary (default: local build)")
    args = parser.parse_args()

    # Find or download results
    state_dir = download_results(args.run, args.rep, args.task)
    if not state_dir:
        print(f"ERROR: Could not find agent-state for run={args.run} rep={args.rep} task={args.task}")
        sys.exit(1)

    sessions = list_sessions(state_dir)

    if args.list_sessions:
        print(f"Sessions for {args.task} (run={args.run}, rep={args.rep}):\n")
        for i, s in enumerate(sessions, 1):
            parent_info = f"parent={s['parent'][:8]}..." if s['parent'] else "ROOT"
            print(f"  {i}. {s['id']}  {s['role']:<14} {s['model']:<16} "
                  f"turns={s['turns']:<3} {parent_info}")
            if s["task_preview"]:
                print(f"     task: {s['task_preview']}")
        return

    target = resolve_session(sessions, args.session)
    questions = args.question or DEFAULT_QUESTIONS
    model = args.model or target["model"]

    print(f"=== Interrogating {args.task} (run={args.run}, rep={args.rep}) ===")
    print(f"Session: {target['id']} ({target['role']})")
    print(f"Model: {model}")
    print(f"Turns: {target['turns']}")
    print()

    # Build interrogation message
    verifier = get_verifier_output(args.run, args.rep, args.task)
    if verifier:
        print(f"Verifier output: {len(verifier)} chars")
    message = build_question_message(questions, verifier)

    # Find serf binary
    serf = args.serf
    if not serf:
        # Try local build first, then PATH
        local = os.path.expanduser("~/prime-radiant/serf/serf")
        if os.path.isfile(local) and os.access(local, os.X_OK):
            serf = local
        else:
            serf = "serf"

    # Copy state dir to temp location so we don't mutate the original
    # transcripts (serf writes new turns during resume).
    with tempfile.TemporaryDirectory(prefix="interrogate-") as tmp:
        tmp_state = os.path.join(tmp, "agent-state")
        shutil.copytree(state_dir, tmp_state)

        # Fix orphaned tool calls (timed-out sessions) before resume
        fix_orphaned_tool_calls(tmp_state, target["id"])

        # Resume the session with serf
        print(f"\nResuming session with serf...\n")
        cmd = [
            serf,
            "--resume-with", target["id"],
            "--state-dir", tmp_state,
            "--provider", "openai",
            "--model", model,
            "--max-rounds", "3",
            "--", message,
        ]

        print(f"=== MODEL RESPONSE ===\n")
        result = subprocess.run(cmd, capture_output=True, text=True,
                                env={**os.environ, "SERF_NON_INTERACTIVE": "1"})
        if result.stdout:
            print(result.stdout)
        if result.returncode != 0:
            print(f"\n[serf exited {result.returncode}]", file=sys.stderr)
            if result.stderr:
                print(result.stderr, file=sys.stderr)


if __name__ == "__main__":
    main()
