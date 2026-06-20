#!/usr/bin/env python3
"""Interrogate an agent session by resuming it with serf and asking questions.

Uses serf's --resume-with to replay the FULL conversation history, then appends
interrogation questions. The model is placed back into the exact context of its
original session — same system prompt, same tool calls, same results.

Usage:
    # List sessions for a rep
    python3 tools/transcripts/interrogate_session.py \
        --run v10-deleg-goldplate --rep 1 --task chess-best-move --list-sessions

    # Interrogate coordinator (default)
    python3 tools/transcripts/interrogate_session.py \
        --run v10-deleg-goldplate --rep 3 --task chess-best-move \
        --question "Why did you not delegate?"

    # Interrogate a subagent by session ID (prefix match)
    python3 tools/transcripts/interrogate_session.py \
        --run v10-deleg-goldplate --rep 1 --task chess-best-move \
        --session 01KMPF5M \
        --question "Why did you override the computational proof?"

    # Interrogate subagent by index (from --list-sessions output)
    python3 tools/transcripts/interrogate_session.py \
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
from datetime import datetime, timezone

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


def download_results(run_id, rep, task=None, include_verifier_output=False):
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

    # Verifier output is opt-in because it contains the hidden PASS/FAIL
    # ground truth. Leaking it into a debrief prompt corrupts any
    # self-awareness probing.
    if task and include_verifier_output:
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
            "profile_id": meta.get("profile_id", ""),
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

    # Find the max seq across existing entries so appended synthetic
    # results don't collide.
    max_seq = 0
    for line in lines:
        try:
            entry = json.loads(line)
        except json.JSONDecodeError:
            continue
        s = entry.get("seq", 0)
        if isinstance(s, int) and s > max_seq:
            max_seq = s

    ts = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000000Z")

    # Append synthetic TOOL_RESULTS entries. Structure must match the
    # rest of the transcript: top-level kind="entry", turn.kind="TOOL_RESULTS"
    # (uppercase, plural), with timestamp and usage fields.
    with open(transcript_path, "a") as f:
        for tid, name in unanswered:
            max_seq += 1
            synthetic = {
                "kind": "entry",
                "seq": max_seq,
                "turn": {
                    "kind": "TOOL_RESULTS",
                    "message": {
                        "role": "tool",
                        "content": [{
                            "kind": "tool_result",
                            "tool_result": {
                                "tool_call_id": tid,
                                "name": name,
                                "content": "[Session ended with orphaned "
                                           "tool call; no result recorded.]",
                                "is_error": True,
                            }
                        }]
                    },
                    "timestamp": ts,
                    "usage": {
                        "input_tokens": 0,
                        "output_tokens": 0,
                        "total_tokens": 0,
                    }
                }
            }
            f.write(json.dumps(synthetic) + "\n")


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


def _extract_wave_sha(run_id: str) -> str | None:
    """Extract git SHA from wave ID or launch metadata."""
    # Try .serf-launches/RUN_ID.json first
    launch_file = os.path.expanduser(f"~/prime-radiant/serf/.serf-launches/{run_id}.json")
    if os.path.isfile(launch_file):
        try:
            with open(launch_file) as f:
                meta = json.load(f)
            return meta.get("git_sha")
        except (json.JSONDecodeError, KeyError):
            pass

    # Fall back to parsing wave-SHA-DATE format
    m = re.match(r"wave-([0-9a-f]{7,})-", run_id)
    if m:
        return m.group(1)

    return None


def _check_binary_matches_wave(serf_path: str, wave_sha: str, run_id: str):
    """Warn if the serf binary doesn't match the wave's git SHA."""
    # Check current git HEAD
    try:
        head = subprocess.run(
            ["git", "rev-parse", "--short", "HEAD"],
            capture_output=True, text=True, cwd=os.path.expanduser("~/prime-radiant/serf")
        ).stdout.strip()
    except Exception:
        head = None

    # Check binary modification time vs wave launch time
    try:
        binary_mtime = os.path.getmtime(serf_path)
    except Exception:
        binary_mtime = 0

    sha_match = head and wave_sha and head.startswith(wave_sha[:7])

    if os.environ.get("SKIP_SHA_CHECK"):
        return

    if not sha_match:
        print(f"\n{'='*70}", file=sys.stderr)
        print(f"WARNING: Binary/wave SHA mismatch!", file=sys.stderr)
        print(f"  Wave {run_id} was built from SHA: {wave_sha}", file=sys.stderr)
        print(f"  Current git HEAD: {head or 'unknown'}", file=sys.stderr)
        print(f"  Binary: {serf_path}", file=sys.stderr)
        print(f"", file=sys.stderr)
        print(f"  The serf binary injects its OWN system prompt into the resumed", file=sys.stderr)
        print(f"  session. If the binary is from a different SHA, the model sees", file=sys.stderr)
        print(f"  the WRONG prompts and produces false root cause attributions.", file=sys.stderr)
        print(f"", file=sys.stderr)
        print(f"  Fix: git checkout {wave_sha} && go build -o serf ./cmd/serf/", file=sys.stderr)
        print(f"  Then re-run this interrogation.", file=sys.stderr)
        print(f"{'='*70}\n", file=sys.stderr)

        resp = input("Continue anyway? [y/N] ") if sys.stdin.isatty() else "n"
        if resp.lower() != "y":
            print("Aborted. Rebuild the binary first.", file=sys.stderr)
            sys.exit(1)


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
    parser.add_argument("--provider", default=None,
                        help="Provider override (default: auto-detect from session profile_id, "
                             "fall back to openai if unknown)")
    parser.add_argument("--serf", default=None,
                        help="Path to serf binary (default: local build)")
    parser.add_argument("--force", action="store_true",
                        help="Skip binary/wave SHA mismatch check (use when binary is manually verified)")
    parser.add_argument("--include-verifier-output", action="store_true",
                        help="Include the hidden verifier's test-stdout.txt in the debrief prompt. "
                             "OFF by default because leaking ground-truth PASS/FAIL corrupts "
                             "self-awareness probing.")
    args = parser.parse_args()

    # Find or download results
    state_dir = download_results(
        args.run, args.rep, args.task,
        include_verifier_output=args.include_verifier_output)
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

    # Resolve provider: --provider flag wins, then session metadata profile_id,
    # else default to openai (historical behavior) with a warning.
    provider = args.provider or target.get("profile_id") or ""
    if not provider:
        print("[WARN] Session profile_id missing from metadata; defaulting to --provider openai. "
              "Pass --provider explicitly if this is wrong.", file=sys.stderr)
        provider = "openai"

    print(f"=== Interrogating {args.task} (run={args.run}, rep={args.rep}) ===")
    print(f"Session: {target['id']} ({target['role']})")
    print(f"Model: {model}")
    print(f"Turns: {target['turns']}")
    print()

    # Build interrogation message. Verifier output is opt-in — when
    # disabled, we skip reading test-stdout.txt entirely so there is no
    # path by which PASS/FAIL leaks into the prompt.
    verifier = None
    if args.include_verifier_output:
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

    # CRITICAL: Check that the serf binary matches the wave's git SHA.
    # A stale binary injects the WRONG system prompt into the resumed session,
    # producing false root cause attributions. See prompt-lessons.md.
    wave_sha = _extract_wave_sha(args.run)
    if wave_sha and not args.force:
        _check_binary_matches_wave(serf, wave_sha, args.run)

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
            "--provider", provider,
            "--model", model,
            "--max-rounds", "3",
            "--", message,
        ]

        print(f"=== MODEL RESPONSE ===\n")
        try:
            result = subprocess.run(cmd, capture_output=True, text=True,
                                    timeout=120,
                                    env={**os.environ, "SERF_NON_INTERACTIVE": "1"})
        except subprocess.TimeoutExpired:
            print("\n[serf timed out after 120s — session may have orphaned spawned agents]",
                  file=sys.stderr)
            return
        if result.stdout:
            print(result.stdout)
        if result.returncode != 0:
            print(f"\n[serf exited {result.returncode}]", file=sys.stderr)
            if result.stderr:
                print(result.stderr, file=sys.stderr)


if __name__ == "__main__":
    main()
