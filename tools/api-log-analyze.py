#!/usr/bin/env python3
"""Analyze api.jsonl files from serf runs.

Usage:
  tools/api-log-analyze.py <api-log-file-or-dir> [options]

Options:
  --empty       Show only empty responses (no text, no tool calls)
  --summary     Per-session summary (calls, empties, tokens, avg latency)
  --raw         Print full raw response for matching entries
  --session ID  Filter to a specific session
  --errors      Show only entries with errors

When given a directory, recursively finds all api.jsonl files.

Default output: one line per API call showing session, round, model, latency,
tokens, finish reason, text length, tool call count.
"""
import argparse
import json
import os
import sys
from collections import defaultdict
from pathlib import Path


def find_log_files(path):
    """Find all api.jsonl files at the given path."""
    p = Path(path)
    if p.is_file():
        return [p]
    if p.is_dir():
        return sorted(p.rglob("api.jsonl"))
    print(f"error: {path} is not a file or directory", file=sys.stderr)
    sys.exit(1)


def read_entries(files):
    """Read all JSONL entries from the given files."""
    entries = []
    for f in files:
        with open(f) as fh:
            for lineno, line in enumerate(fh, 1):
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                    entry["_file"] = str(f)
                    entries.append(entry)
                except json.JSONDecodeError:
                    print(f"warning: {f}:{lineno}: invalid JSON, skipping", file=sys.stderr)
    return entries


def is_empty(entry):
    """Check if a response is empty (no text, no tool calls)."""
    resp = entry.get("response")
    if resp is None:
        return entry.get("error", "") != ""  # errors are "empty" in a different way
    return resp.get("text_length", 0) == 0 and resp.get("tool_call_count", 0) == 0


def format_entry(entry, show_raw=False):
    """Format a single entry as a one-line summary."""
    sid = entry.get("session_id", "-")[:12]
    rnd = entry.get("round", 0)
    req = entry.get("request", {})
    model = req.get("model", "?")
    latency = entry.get("latency_ms", 0)
    err = entry.get("error", "")

    if err:
        line = f"{sid}  r{rnd:>3d}  {model:<30s}  {latency:>6d}ms  ERROR: {err[:80]}"
    else:
        resp = entry.get("response", {})
        usage = resp.get("usage", {})
        finish = resp.get("finish_reason", "?")
        txt_len = resp.get("text_length", 0)
        tc_count = resp.get("tool_call_count", 0)
        in_tok = usage.get("input_tokens", 0)
        out_tok = usage.get("output_tokens", 0)

        line = (
            f"{sid}  r{rnd:>3d}  {model:<30s}  {latency:>6d}ms  "
            f"in={in_tok:<6d} out={out_tok:<5d}  {finish:<12s}  "
            f"text={txt_len:<5d} tools={tc_count}"
        )

    if show_raw:
        resp = entry.get("response", {})
        raw = resp.get("raw", {})
        if raw:
            line += "\n  raw: " + json.dumps(raw, indent=2).replace("\n", "\n  ")

    return line


def print_summary(entries):
    """Print per-session summary."""
    sessions = defaultdict(lambda: {
        "calls": 0, "empties": 0, "errors": 0,
        "in_tokens": 0, "out_tokens": 0,
        "latency_sum": 0, "model": "?",
    })

    for entry in entries:
        sid = entry.get("session_id", "(no session)")
        s = sessions[sid]
        s["calls"] += 1
        s["latency_sum"] += entry.get("latency_ms", 0)
        s["model"] = entry.get("request", {}).get("model", "?")

        if entry.get("error"):
            s["errors"] += 1
        elif is_empty(entry):
            s["empties"] += 1

        resp = entry.get("response", {})
        usage = resp.get("usage", {})
        s["in_tokens"] += usage.get("input_tokens", 0)
        s["out_tokens"] += usage.get("output_tokens", 0)

    print(f"{'Session':<14s}  {'Model':<30s}  {'Calls':>5s}  {'Empty':>5s}  "
          f"{'Errors':>6s}  {'InTok':>8s}  {'OutTok':>7s}  {'AvgMs':>6s}")
    print("-" * 105)

    for sid, s in sorted(sessions.items()):
        avg_ms = s["latency_sum"] // max(s["calls"], 1)
        print(f"{sid[:14]:<14s}  {s['model']:<30s}  {s['calls']:>5d}  {s['empties']:>5d}  "
              f"{s['errors']:>6d}  {s['in_tokens']:>8d}  {s['out_tokens']:>7d}  {avg_ms:>6d}")


def main():
    parser = argparse.ArgumentParser(
        description="Analyze serf API log files (api.jsonl)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("path", help="api.jsonl file or directory to search")
    parser.add_argument("--empty", action="store_true", help="Show only empty responses")
    parser.add_argument("--errors", action="store_true", help="Show only error entries")
    parser.add_argument("--summary", action="store_true", help="Per-session summary")
    parser.add_argument("--raw", action="store_true", help="Print full raw response")
    parser.add_argument("--session", help="Filter to a specific session ID")
    args = parser.parse_args()

    files = find_log_files(args.path)
    if not files:
        print("No api.jsonl files found.", file=sys.stderr)
        sys.exit(1)

    if len(files) > 1:
        print(f"Found {len(files)} api.jsonl files", file=sys.stderr)

    entries = read_entries(files)
    if not entries:
        print("No entries found.", file=sys.stderr)
        sys.exit(1)

    # Apply filters.
    if args.session:
        entries = [e for e in entries if args.session in e.get("session_id", "")]
    if args.empty:
        entries = [e for e in entries if is_empty(e)]
    if args.errors:
        entries = [e for e in entries if e.get("error")]

    if args.summary:
        print_summary(entries)
    else:
        for entry in entries:
            print(format_entry(entry, show_raw=args.raw))

    if not entries:
        print("(no matching entries)", file=sys.stderr)


if __name__ == "__main__":
    main()
