#!/usr/bin/env python3
"""Analyze correlations between communicate tool usage and empty API responses.

Reads all api.jsonl files from a run directory and checks:
1. Whether reviewer rejection of communicate triggers empty responses
2. Whether bare-text steering triggers empty responses
3. Parent vs subagent empty rates at the same message counts
4. Coordinator tool call density breakdown

Usage:
  python3 analyze-empty-correlations.py /tmp/full-mk3/full-mk3
"""
import json
import sys
from collections import defaultdict
from pathlib import Path


def load_all_entries(run_dir):
    """Load all api.jsonl entries from every task directory."""
    entries_by_session = defaultdict(list)
    total_files = 0
    total_entries = 0
    for api_file in sorted(Path(run_dir).rglob("api.jsonl")):
        total_files += 1
        with open(api_file) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                    entry["_file"] = str(api_file)
                    sid = entry.get("session_id", "unknown")
                    entries_by_session[sid].append(entry)
                    total_entries += 1
                except json.JSONDecodeError:
                    pass

    # Sort each session's entries by round number
    for sid in entries_by_session:
        entries_by_session[sid].sort(key=lambda e: e.get("round", 0))

    print(f"Loaded {total_entries} entries from {total_files} api.jsonl files across {len(entries_by_session)} sessions")
    return entries_by_session


def is_empty(entry):
    """Check if a response is empty (no text, no tool calls)."""
    resp = entry.get("response")
    if resp is None:
        return False  # error entries handled separately
    return resp.get("text_length", 0) == 0 and resp.get("tool_call_count", 0) == 0


def is_parent_session(entry):
    """A parent/coordinator session has communicate in its tool names."""
    tools = entry.get("request", {}).get("tool_names", [])
    return "communicate" in tools


def has_communicate_call(entry):
    """Check if the response contains a communicate tool call."""
    resp = entry.get("response", {})
    raw = resp.get("raw", {})
    output = raw.get("output", [])
    if not isinstance(output, list):
        return False
    for item in output:
        if isinstance(item, dict) and item.get("type") == "function_call" and item.get("name") == "communicate":
            return True
    return False


def has_spawn_agent_call(entry):
    """Check if the response contains a spawn_agent tool call."""
    resp = entry.get("response", {})
    raw = resp.get("raw", {})
    output = raw.get("output", [])
    if not isinstance(output, list):
        return False
    for item in output:
        if isinstance(item, dict) and item.get("type") == "function_call" and item.get("name") == "spawn_agent":
            return True
    return False


def get_tool_names_from_response(entry):
    """Get all tool call names from a response."""
    resp = entry.get("response", {})
    raw = resp.get("raw", {})
    output = raw.get("output", [])
    names = []
    if not isinstance(output, list):
        return names
    for item in output:
        if isinstance(item, dict) and item.get("type") == "function_call":
            names.append(item.get("name", "unknown"))
    return names


def is_bare_text(entry):
    """Check if response is bare text (text_length > 0, tool_call_count = 0)."""
    resp = entry.get("response")
    if resp is None:
        return False
    return resp.get("text_length", 0) > 0 and resp.get("tool_call_count", 0) == 0


def prev_was_communicate_rejection(entries, idx):
    """Check if entry at idx-1 was a communicate call, and entry at idx has
    evidence of rejection in the input (increased message count with rejection feedback).

    Since we can't directly see the tool result in the NEXT request's raw input
    (the Responses API tracks conversation server-side), we infer rejection by:
    - The previous entry had a communicate call
    - The current entry exists (meaning the session continued after communicate)
    - If the session continued after communicate, it MUST have been rejected
      (accepted communicate ends the session)
    """
    if idx <= 0:
        return False
    prev = entries[idx - 1]
    return has_communicate_call(prev)


def prev_was_bare_text(entries, idx):
    """Check if entry at idx-1 was bare text (steering would follow)."""
    if idx <= 0:
        return False
    prev = entries[idx - 1]
    return is_bare_text(prev)


def analysis_1_communicate_rejection(entries_by_session):
    """For every call following a communicate call (= rejection since session continued),
    check if the response is empty. Compare to empty rate after other tool results."""
    print("\n" + "=" * 80)
    print("ANALYSIS 1: Communicate Rejection -> Empty Response Correlation")
    print("=" * 80)
    print()
    print("Logic: If an entry follows a communicate call AND the session continued,")
    print("the reviewer MUST have rejected it. We compare empty rates after rejection")
    print("vs after other tool results.")
    print()

    after_rejection_total = 0
    after_rejection_empty = 0
    after_other_total = 0
    after_other_empty = 0
    after_rejection_details = []

    for sid, entries in entries_by_session.items():
        # Only look at parent sessions
        if not entries or not is_parent_session(entries[0]):
            continue
        for i in range(1, len(entries)):
            curr = entries[i]
            if curr.get("error"):
                continue  # skip error entries

            if prev_was_communicate_rejection(entries, i):
                after_rejection_total += 1
                if is_empty(curr):
                    after_rejection_empty += 1
                    after_rejection_details.append({
                        "session": sid[:16],
                        "round": curr.get("round"),
                        "msgs": curr.get("request", {}).get("message_count"),
                        "file": curr.get("_file", "").split("/full-mk3/")[1][:40] if "/full-mk3/" in curr.get("_file", "") else curr.get("_file", "")[:40],
                    })
            else:
                after_other_total += 1
                if is_empty(curr):
                    after_other_empty += 1

    rej_rate = (after_rejection_empty / after_rejection_total * 100) if after_rejection_total > 0 else 0
    other_rate = (after_other_empty / after_other_total * 100) if after_other_total > 0 else 0

    print(f"After communicate rejection: {after_rejection_empty}/{after_rejection_total} empty ({rej_rate:.1f}%)")
    print(f"After other tool results:    {after_other_empty}/{after_other_total} empty ({other_rate:.1f}%)")
    if rej_rate > 0 and other_rate > 0:
        print(f"Relative risk: {rej_rate/other_rate:.1f}x")
    print()

    if after_rejection_details:
        print("Empty responses after rejection:")
        for d in after_rejection_details[:20]:
            print(f"  session={d['session']}  round={d['round']}  msgs={d['msgs']}  task={d['file']}")
    print()


def analysis_2_bare_text_steering(entries_by_session):
    """After bare text responses (where steering is injected), check if the NEXT
    response is empty at a higher rate."""
    print("\n" + "=" * 80)
    print("ANALYSIS 2: Bare Text Steering -> Empty Response Correlation")
    print("=" * 80)
    print()
    print("Logic: When the model emits bare text (text>0, tools=0), the agent loop")
    print("injects steering saying 'use the communicate tool'. We check if the next")
    print("API call after such steering produces an empty response.")
    print()

    after_bare_text_total = 0
    after_bare_text_empty = 0
    after_other_total = 0
    after_other_empty = 0
    bare_text_then_empty_details = []

    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        for i in range(1, len(entries)):
            curr = entries[i]
            if curr.get("error"):
                continue

            if prev_was_bare_text(entries, i):
                after_bare_text_total += 1
                if is_empty(curr):
                    after_bare_text_empty += 1
                    bare_text_then_empty_details.append({
                        "session": sid[:16],
                        "round": curr.get("round"),
                        "msgs": curr.get("request", {}).get("message_count"),
                        "file": curr.get("_file", "").split("/full-mk3/")[1][:40] if "/full-mk3/" in curr.get("_file", "") else curr.get("_file", "")[:40],
                    })
            else:
                # Count non-bare-text-following entries (excluding first)
                after_other_total += 1
                if is_empty(curr):
                    after_other_empty += 1

    bt_rate = (after_bare_text_empty / after_bare_text_total * 100) if after_bare_text_total > 0 else 0
    other_rate = (after_other_empty / after_other_total * 100) if after_other_total > 0 else 0

    print(f"After bare text (steering injected): {after_bare_text_empty}/{after_bare_text_total} empty ({bt_rate:.1f}%)")
    print(f"After other responses:               {after_other_empty}/{after_other_total} empty ({other_rate:.1f}%)")
    if bt_rate > 0 and other_rate > 0:
        print(f"Relative risk: {bt_rate/other_rate:.1f}x")
    print()

    if bare_text_then_empty_details:
        print("Empty responses after bare-text steering:")
        for d in bare_text_then_empty_details[:20]:
            print(f"  session={d['session']}  round={d['round']}  msgs={d['msgs']}  task={d['file']}")

    # Also: what happens after bare text? Break down into empty, communicate, other tool, more bare text
    print()
    print("What follows bare text responses (parent sessions only):")
    follows = defaultdict(int)
    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        for i in range(1, len(entries)):
            if prev_was_bare_text(entries, i):
                curr = entries[i]
                if curr.get("error"):
                    follows["error"] += 1
                elif is_empty(curr):
                    follows["empty"] += 1
                elif has_communicate_call(curr):
                    follows["communicate_call"] += 1
                elif is_bare_text(curr):
                    follows["more_bare_text"] += 1
                else:
                    follows["other_tool_call"] += 1
    total = sum(follows.values())
    for k, v in sorted(follows.items(), key=lambda x: -x[1]):
        pct = v / total * 100 if total > 0 else 0
        print(f"  {k:25s}: {v:4d} ({pct:.1f}%)")
    print()


def analysis_3_empty_rate_by_message_count(entries_by_session):
    """Compare parent vs subagent empty rates at the same message counts."""
    print("\n" + "=" * 80)
    print("ANALYSIS 3: Empty Rate by Message Count (Parent vs Subagent)")
    print("=" * 80)
    print()

    parent_by_msgcount = defaultdict(lambda: {"total": 0, "empty": 0})
    subagent_by_msgcount = defaultdict(lambda: {"total": 0, "empty": 0})

    parent_sessions = set()
    subagent_sessions = set()

    for sid, entries in entries_by_session.items():
        if not entries:
            continue
        is_parent = is_parent_session(entries[0])
        for entry in entries:
            if entry.get("error"):
                continue
            mc = entry.get("request", {}).get("message_count", 0)
            if is_parent:
                parent_by_msgcount[mc]["total"] += 1
                if is_empty(entry):
                    parent_by_msgcount[mc]["empty"] += 1
                parent_sessions.add(sid)
            else:
                subagent_by_msgcount[mc]["total"] += 1
                if is_empty(entry):
                    subagent_by_msgcount[mc]["empty"] += 1
                subagent_sessions.add(sid)

    print(f"Parent sessions: {len(parent_sessions)}")
    print(f"Subagent sessions: {len(subagent_sessions)}")
    print()

    # Overall rates
    parent_total = sum(v["total"] for v in parent_by_msgcount.values())
    parent_empty = sum(v["empty"] for v in parent_by_msgcount.values())
    sub_total = sum(v["total"] for v in subagent_by_msgcount.values())
    sub_empty = sum(v["empty"] for v in subagent_by_msgcount.values())
    print(f"Overall parent empty rate:   {parent_empty}/{parent_total} ({parent_empty/parent_total*100:.1f}%)" if parent_total else "No parent entries")
    print(f"Overall subagent empty rate: {sub_empty}/{sub_total} ({sub_empty/sub_total*100:.1f}%)" if sub_total else "No subagent entries")
    print()

    # Find overlapping message counts with reasonable sample sizes
    all_mcs = sorted(set(parent_by_msgcount.keys()) | set(subagent_by_msgcount.keys()))
    print(f"{'MsgCount':>8s}  {'Parent':>15s}  {'ParentRate':>10s}  {'Subagent':>15s}  {'SubRate':>10s}")
    print("-" * 65)

    # Bucket into ranges for cleaner display
    buckets = [(1, 5), (6, 10), (11, 15), (16, 20), (21, 30), (31, 50), (51, 100), (101, 200), (201, 500)]
    for lo, hi in buckets:
        p_total = sum(parent_by_msgcount[mc]["total"] for mc in range(lo, hi + 1))
        p_empty = sum(parent_by_msgcount[mc]["empty"] for mc in range(lo, hi + 1))
        s_total = sum(subagent_by_msgcount[mc]["total"] for mc in range(lo, hi + 1))
        s_empty = sum(subagent_by_msgcount[mc]["empty"] for mc in range(lo, hi + 1))
        if p_total == 0 and s_total == 0:
            continue
        p_rate = f"{p_empty/p_total*100:.1f}%" if p_total > 0 else "n/a"
        s_rate = f"{s_empty/s_total*100:.1f}%" if s_total > 0 else "n/a"
        label = f"{lo}-{hi}"
        print(f"{label:>8s}  {p_empty:>5d}/{p_total:<7d}   {p_rate:>10s}  {s_empty:>5d}/{s_total:<7d}   {s_rate:>10s}")
    print()

    # Also show per-round position within a session
    print("Empty rate by round position within session (parent only):")
    parent_by_round = defaultdict(lambda: {"total": 0, "empty": 0})
    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        for entry in entries:
            if entry.get("error"):
                continue
            rnd = entry.get("round", 0)
            parent_by_round[rnd]["total"] += 1
            if is_empty(entry):
                parent_by_round[rnd]["empty"] += 1

    round_buckets = [(1, 2), (3, 5), (6, 10), (11, 15), (16, 20), (21, 30), (31, 50), (51, 100)]
    print(f"{'Round':>8s}  {'Empty/Total':>15s}  {'Rate':>10s}")
    print("-" * 38)
    for lo, hi in round_buckets:
        t = sum(parent_by_round[r]["total"] for r in range(lo, hi + 1))
        e = sum(parent_by_round[r]["empty"] for r in range(lo, hi + 1))
        if t == 0:
            continue
        print(f"{lo:>3d}-{hi:<3d}   {e:>5d}/{t:<7d}   {e/t*100:>9.1f}%")
    print()


def analysis_4_tool_call_density(entries_by_session):
    """Breakdown of what the coordinator does each round."""
    print("\n" + "=" * 80)
    print("ANALYSIS 4: Coordinator Tool Call Density Breakdown")
    print("=" * 80)
    print()

    categories = defaultdict(int)
    tool_name_counts = defaultdict(int)
    total_parent_entries = 0

    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        for entry in entries:
            if entry.get("error"):
                categories["error"] += 1
                total_parent_entries += 1
                continue

            total_parent_entries += 1
            resp = entry.get("response", {})
            text_len = resp.get("text_length", 0)
            tool_count = resp.get("tool_call_count", 0)

            if text_len == 0 and tool_count == 0:
                categories["empty"] += 1
            elif text_len > 0 and tool_count == 0:
                categories["bare_text"] += 1
            elif tool_count > 0:
                # Categorize by what tools were called
                tool_names = get_tool_names_from_response(entry)
                if not tool_names:
                    # Fallback: can't parse raw output
                    categories["tool_call_unknown"] += 1
                else:
                    for tn in tool_names:
                        tool_name_counts[tn] += 1
                    if "communicate" in tool_names:
                        if len(tool_names) == 1:
                            categories["communicate_only"] += 1
                        else:
                            categories["communicate_plus_other"] += 1
                    elif "spawn_agent" in tool_names:
                        categories["spawn_agent"] += 1
                    elif "resume_agent" in tool_names or "wait" in tool_names:
                        categories["agent_management"] += 1
                    else:
                        categories["other_tool_call"] += 1

    print(f"Total parent coordinator API calls: {total_parent_entries}")
    print()
    print("Response category breakdown:")
    print(f"{'Category':30s}  {'Count':>6s}  {'Pct':>7s}")
    print("-" * 48)
    for cat, count in sorted(categories.items(), key=lambda x: -x[1]):
        pct = count / total_parent_entries * 100 if total_parent_entries > 0 else 0
        print(f"  {cat:28s}  {count:>6d}  {pct:>6.1f}%")
    print()

    print("Tool call frequency (all coordinator tool calls):")
    print(f"{'Tool Name':30s}  {'Count':>6s}  {'Pct':>7s}")
    print("-" * 48)
    total_tool_calls = sum(tool_name_counts.values())
    for tn, count in sorted(tool_name_counts.items(), key=lambda x: -x[1]):
        pct = count / total_tool_calls * 100 if total_tool_calls > 0 else 0
        print(f"  {tn:28s}  {count:>6d}  {pct:>6.1f}%")
    print()

    # Communicate attempts vs successful exits
    communicate_calls = categories.get("communicate_only", 0) + categories.get("communicate_plus_other", 0)
    print(f"Communicate calls: {communicate_calls}")
    print(f"Spawn agent calls: {categories.get('spawn_agent', 0)}")
    print(f"Empty responses: {categories.get('empty', 0)}")
    print(f"Bare text responses: {categories.get('bare_text', 0)}")
    print()

    # How many sessions have communicate calls?
    sessions_with_communicate = 0
    sessions_total_parent = 0
    communicate_per_session = []
    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        sessions_total_parent += 1
        comm_count = sum(1 for e in entries if has_communicate_call(e))
        if comm_count > 0:
            sessions_with_communicate += 1
            communicate_per_session.append(comm_count)

    print(f"Parent sessions with >=1 communicate call: {sessions_with_communicate}/{sessions_total_parent}")
    if communicate_per_session:
        avg = sum(communicate_per_session) / len(communicate_per_session)
        print(f"Average communicate calls per session (when >0): {avg:.1f}")
        dist = defaultdict(int)
        for c in communicate_per_session:
            dist[c] += 1
        print("Distribution of communicate calls per session:")
        for k in sorted(dist.keys()):
            print(f"  {k} calls: {dist[k]} sessions")
    print()


def analysis_5_empty_sequences(entries_by_session):
    """Bonus: analyze consecutive empty response sequences."""
    print("\n" + "=" * 80)
    print("ANALYSIS 5 (BONUS): Consecutive Empty Response Sequences")
    print("=" * 80)
    print()

    sequence_lengths = []
    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        current_streak = 0
        for entry in entries:
            if entry.get("error"):
                if current_streak > 0:
                    sequence_lengths.append(current_streak)
                    current_streak = 0
                continue
            if is_empty(entry):
                current_streak += 1
            else:
                if current_streak > 0:
                    sequence_lengths.append(current_streak)
                    current_streak = 0
        if current_streak > 0:
            sequence_lengths.append(current_streak)

    if not sequence_lengths:
        print("No consecutive empty sequences found in parent sessions.")
        return

    print(f"Total empty sequences: {len(sequence_lengths)}")
    dist = defaultdict(int)
    for sl in sequence_lengths:
        dist[sl] += 1
    print("Sequence length distribution:")
    for k in sorted(dist.keys()):
        print(f"  {k} consecutive empties: {dist[k]} occurrences")
    print(f"Max consecutive empties: {max(sequence_lengths)}")
    print(f"Average sequence length: {sum(sequence_lengths)/len(sequence_lengths):.1f}")
    print()

    # What precedes empty sequences?
    print("What immediately precedes the FIRST empty in each sequence (parent sessions):")
    predecessors = defaultdict(int)
    for sid, entries in entries_by_session.items():
        if not entries or not is_parent_session(entries[0]):
            continue
        for i, entry in enumerate(entries):
            if not is_empty(entry) or entry.get("error"):
                continue
            # Is this the START of an empty sequence?
            if i > 0 and is_empty(entries[i - 1]) and not entries[i - 1].get("error"):
                continue  # not the start
            # It's the start. What preceded it?
            if i == 0:
                predecessors["(first entry)"] += 1
            else:
                prev = entries[i - 1]
                if prev.get("error"):
                    predecessors["error"] += 1
                elif has_communicate_call(prev):
                    predecessors["communicate_call"] += 1
                elif is_bare_text(prev):
                    predecessors["bare_text"] += 1
                elif has_spawn_agent_call(prev):
                    predecessors["spawn_agent"] += 1
                else:
                    tool_names = get_tool_names_from_response(prev)
                    if tool_names:
                        predecessors[f"tool:{tool_names[0]}"] += 1
                    else:
                        predecessors["other"] += 1

    total = sum(predecessors.values())
    for k, v in sorted(predecessors.items(), key=lambda x: -x[1]):
        pct = v / total * 100 if total > 0 else 0
        print(f"  {k:30s}: {v:4d} ({pct:.1f}%)")
    print()


def main():
    if len(sys.argv) < 2:
        print(f"Usage: {sys.argv[0]} <run-directory>", file=sys.stderr)
        sys.exit(1)

    run_dir = sys.argv[1]
    entries_by_session = load_all_entries(run_dir)

    analysis_1_communicate_rejection(entries_by_session)
    analysis_2_bare_text_steering(entries_by_session)
    analysis_3_empty_rate_by_message_count(entries_by_session)
    analysis_4_tool_call_density(entries_by_session)
    analysis_5_empty_sequences(entries_by_session)


if __name__ == "__main__":
    main()
