#!/usr/bin/env python3
"""Analyze api.jsonl and transcript API call files from serf runs.

Usage:
  tools/transcripts/api-log-analyze.py <api-log-file-or-dir> [options]

Options:
  --empty             Show only empty responses (no text, no tool calls)
  --summary           Per-session summary (calls, empties, tokens, avg latency)
  --cache-spikes      Show uncached input-token spikes
  --spike-threshold N Minimum uncached input tokens for --cache-spikes
  --raw               Print full raw response for matching entries
  --session ID        Filter to a specific session
  --errors            Show only entries with errors

When given a directory, recursively finds all api.jsonl and *.transcript.jsonl
files.

Default output: one line per API call showing session, round, model, latency,
tokens, finish reason, text length, tool call count.
"""
import argparse
import json
import os
import sys
from collections import defaultdict
from pathlib import Path


SOURCE_API_JSONL = "api_jsonl"
SOURCE_TRANSCRIPT = "transcript"


def find_log_files(path):
    """Find all supported API log files at the given path."""
    p = Path(path)
    if p.is_file():
        return [p]
    if p.is_dir():
        files = set(p.rglob("api.jsonl"))
        files.update(p.rglob("*.transcript.jsonl"))
        return sorted(files)
    print(f"error: {path} is not a file or directory", file=sys.stderr)
    sys.exit(1)


def is_transcript_file(path):
    return str(path).endswith(".transcript.jsonl")


def read_jsonl(path):
    with open(path) as fh:
        for lineno, line in enumerate(fh, 1):
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                print(f"warning: {path}:{lineno}: invalid JSON, skipping", file=sys.stderr)
                continue
            if not isinstance(entry, dict):
                print(f"warning: {path}:{lineno}: malformed JSON row, skipping", file=sys.stderr)
                continue
            yield lineno, entry


def read_entries(files):
    """Read all JSONL entries from the given files."""
    entries = []
    for f in files:
        if is_transcript_file(f):
            entries.extend(read_transcript_api_entries(f))
            continue

        for lineno, entry in read_jsonl(f):
            entry["_file"] = str(f)
            entry["_line"] = lineno
            entry["_source_kind"] = SOURCE_API_JSONL
            entries.append(entry)
    return dedupe_entries(entries)


def read_transcript_api_entries(path):
    """Read only api_call records from a transcript JSONL file."""
    entries = []
    header = {}
    for lineno, entry in read_jsonl(path):
        kind = entry.get("kind")
        if kind == "header":
            header = entry
            continue
        if kind != "api_call":
            continue

        entry["_file"] = str(path)
        entry["_line"] = lineno
        entry["_source_kind"] = SOURCE_TRANSCRIPT
        if header:
            if not entry.get("session_id") and header.get("session_id"):
                entry["session_id"] = header["session_id"]
            if not entry.get("profile_id") and header.get("profile_id"):
                entry["profile_id"] = header["profile_id"]
            req = entry.setdefault("request", {})
            if not req.get("model") and header.get("model"):
                req["model"] = header["model"]
        entries.append(entry)
    return entries


def usage_for(entry):
    resp = entry.get("response", {}) or {}
    return resp.get("usage", {}) or {}


def cache_read_tokens(usage):
    return usage.get("cache_read_tokens") or 0


def cache_write_tokens(usage):
    return usage.get("cache_write_tokens") or 0


def cache_write_1h_tokens(usage):
    return usage.get("cache_write_1h_tokens") or 0


def response_id(entry):
    resp = entry.get("response", {}) or {}
    raw = resp.get("raw", {}) or {}
    if not isinstance(raw, dict):
        raw = {}
    return resp.get("id") or raw.get("id")


def usage_signature(entry):
    usage = usage_for(entry)
    return (
        usage.get("input_tokens", 0),
        usage.get("output_tokens", 0),
        cache_read_tokens(usage),
        cache_write_tokens(usage),
        cache_write_1h_tokens(usage),
        usage.get("total_tokens", 0),
    )


def response_shape(entry):
    resp = entry.get("response", {}) or {}
    err = entry.get("error", "")
    return (
        response_id(entry),
        bool(err),
        err,
        resp.get("finish_reason"),
        resp.get("text_length", 0),
        resp.get("tool_call_count", 0),
    )


def dedupe_key(entry, index):
    req = entry.get("request", {}) or {}
    identity = (
        entry.get("session_id"),
        entry.get("round"),
        req.get("provider"),
        req.get("model"),
        response_id(entry),
    )
    key = (
        entry.get("session_id"),
        entry.get("round"),
        req.get("provider"),
        req.get("model"),
        usage_signature(entry),
        response_shape(entry),
    )
    if any(part not in (None, "") for part in identity):
        return ("api_call",) + key
    return ("path", entry.get("_file"), index)


def source_pair(entry):
    return (entry.get("_source_kind"), entry.get("_file"))


def is_cross_api_transcript_duplicate(left, right):
    if source_pair(left) == source_pair(right):
        return False
    return {left.get("_source_kind"), right.get("_source_kind")} == {
        SOURCE_API_JSONL,
        SOURCE_TRANSCRIPT,
    }


def context_score(entry):
    return sum(
        1
        for field in (
            "profile_id",
            "system_prompt",
            "source",
            "title",
            "hint",
            "seq",
        )
        if entry.get(field) not in (None, "")
    )


def dedupe_entries(entries):
    deduped = []
    seen = defaultdict(list)
    for index, entry in enumerate(entries):
        key = dedupe_key(entry, index)
        duplicate_index = next(
            (
                kept_index
                for kept_index in seen[key]
                if is_cross_api_transcript_duplicate(entry, deduped[kept_index])
            ),
            None,
        )
        if duplicate_index is None:
            seen[key].append(len(deduped))
            deduped.append(entry)
            continue
        if context_score(entry) > context_score(deduped[duplicate_index]):
            deduped[duplicate_index] = entry
    return deduped


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
        cache_tok = cache_read_tokens(usage)

        line = (
            f"{sid}  r{rnd:>3d}  {model:<30s}  {latency:>6d}ms  "
            f"in={in_tok:<6d} cache={cache_tok:<6d} out={out_tok:<5d}  {finish:<12s}  "
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
        "in_tokens": 0, "cache_read_tokens": 0,
        "cache_write_tokens": 0, "cache_write_1h_tokens": 0,
        "out_tokens": 0,
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

        usage = usage_for(entry)
        s["in_tokens"] += usage.get("input_tokens", 0)
        s["cache_read_tokens"] += cache_read_tokens(usage)
        s["cache_write_tokens"] += cache_write_tokens(usage)
        s["cache_write_1h_tokens"] += cache_write_1h_tokens(usage)
        s["out_tokens"] += usage.get("output_tokens", 0)

    print(f"{'Session':<14s}  {'Model':<30s}  {'Calls':>5s}  {'Empty':>5s}  "
          f"{'Errors':>6s}  {'InTok':>8s}  {'CacheRead':>9s}  {'Hit%':>6s}  "
          f"{'OutTok':>7s}  {'AvgMs':>6s}")
    print("-" * 125)

    for sid, s in sorted(sessions.items()):
        avg_ms = s["latency_sum"] // max(s["calls"], 1)
        prompt_tokens = (
            s["in_tokens"]
            + s["cache_read_tokens"]
            + s["cache_write_tokens"]
            + s["cache_write_1h_tokens"]
        )
        hit_pct = (s["cache_read_tokens"] / prompt_tokens * 100) if prompt_tokens else 0.0
        print(f"{sid[:14]:<14s}  {s['model']:<30s}  {s['calls']:>5d}  {s['empties']:>5d}  "
              f"{s['errors']:>6d}  {s['in_tokens']:>8d}  {s['cache_read_tokens']:>9d}  "
              f"{hit_pct:>5.1f}%  {s['out_tokens']:>7d}  {avg_ms:>6d}")


def format_cache_spike(entry):
    sid = entry.get("session_id", "-")
    rnd = entry.get("round", 0)
    req = entry.get("request", {})
    model = req.get("model", "?")
    usage = usage_for(entry)
    in_tok = usage.get("input_tokens", 0)
    cache_tok = cache_read_tokens(usage)
    file_name = entry.get("_file", "-")
    return (
        f"UNCACHED_SPIKE  session={sid}  r{rnd:>3d}  model={model}  "
        f"input={in_tok}  cache={cache_tok}  file={file_name}"
    )


def print_cache_spikes(entries, threshold):
    for entry in entries:
        usage = usage_for(entry)
        if usage.get("input_tokens", 0) >= threshold:
            print(format_cache_spike(entry))


def main():
    parser = argparse.ArgumentParser(
        description="Analyze serf API log files (api.jsonl and transcripts)",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=__doc__,
    )
    parser.add_argument("path", help="api.jsonl, transcript JSONL, or directory to search")
    parser.add_argument("--empty", action="store_true", help="Show only empty responses")
    parser.add_argument("--errors", action="store_true", help="Show only error entries")
    parser.add_argument("--summary", action="store_true", help="Per-session summary")
    parser.add_argument("--cache-spikes", action="store_true", help="Show uncached input-token spikes")
    parser.add_argument(
        "--spike-threshold",
        type=int,
        default=8000,
        help="Minimum uncached input tokens for --cache-spikes (default: 8000)",
    )
    parser.add_argument("--raw", action="store_true", help="Print full raw response")
    parser.add_argument("--session", help="Filter to a specific session ID")
    args = parser.parse_args()

    files = find_log_files(args.path)
    if not files:
        print("No api.jsonl or *.transcript.jsonl files found.", file=sys.stderr)
        sys.exit(1)

    if len(files) > 1:
        print(f"Found {len(files)} log files", file=sys.stderr)

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

    if args.cache_spikes:
        print_cache_spikes(entries, args.spike_threshold)
    elif args.summary:
        print_summary(entries)
    else:
        for entry in entries:
            print(format_entry(entry, show_raw=args.raw))

    if not entries:
        print("(no matching entries)", file=sys.stderr)


if __name__ == "__main__":
    try:
        main()
        sys.stdout.flush()
    except BrokenPipeError:
        devnull = os.open(os.devnull, os.O_WRONLY)
        os.dup2(devnull, sys.stdout.fileno())
        sys.exit(1)
