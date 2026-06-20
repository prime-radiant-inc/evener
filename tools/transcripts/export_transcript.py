#!/usr/bin/env python3
"""Export serf agent transcripts as formatted HTML.

Downloads all session transcripts (coordinator + subagents) for a given
run/rep/task and renders them as a navigable HTML page with session
hierarchy, tool calls, text output, and token usage.

Usage:
    python3 tools/transcripts/export_transcript.py \
        --run wave-121bc79-20260330-0549 --rep 1 --task cobol-modernization

    python3 tools/transcripts/export_transcript.py \
        --run wave-121bc79-20260330-0549 --rep 1 --task cobol-modernization \
        --output transcript.html
"""

import argparse
import html
import json
import os
import re
import subprocess
import sys

REGION = "us-west-1"
BUCKET = "harbor-eval-results-526275945504"
RESULTS_DIR = os.path.expanduser("~/prime-radiant/harbor-runner/state/results")

# Collapsible threshold — content longer than this gets wrapped in <details>
# but is NEVER truncated. All content is always present in the HTML.
COLLAPSE_THRESHOLD = 800


def _extract_task_name(dirname):
    """Extract task name from a 'taskname__hash' directory name."""
    return re.sub(r"__[A-Za-z0-9]+$", "", dirname)


def find_local_state_dir(run_id, rep, task=None):
    """Find the agent-state dir in locally downloaded results."""
    rep_dir = os.path.join(RESULTS_DIR, run_id, f"rep-{rep}")
    if not os.path.isdir(rep_dir):
        return None
    for root, dirs, _files in os.walk(rep_dir):
        if os.path.basename(root) == "agent-state" and "sessions" in dirs:
            if task:
                parts = root.replace("\\", "/").split("/")
                found = False
                for part in parts:
                    if _extract_task_name(part) == task and part != task:
                        found = True
                        break
                if not found:
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

    print(f"Downloading from S3 to {dest}...", file=sys.stderr)
    cmd = [
        "aws", "s3", "sync", s3_prefix, dest,
        "--region", REGION,
        "--exclude", "*",
        "--include", f"*{task}*/agent-state/*" if task else "*agent-state/*",
    ]
    subprocess.run(cmd, check=True)

    return find_local_state_dir(run_id, rep, task)


def load_sessions(state_dir):
    """Load all session transcripts and metadata from a state dir.

    Returns a dict of session_id -> {meta, header, entries, api_calls} where
    entries and api_calls are interleaved in sequence order.
    """
    sessions_dir = os.path.join(state_dir, "sessions")
    sessions = {}

    for f in sorted(os.listdir(sessions_dir)):
        if not f.endswith(".meta.json"):
            continue
        meta = json.load(open(os.path.join(sessions_dir, f)))
        session_id = meta["id"]

        transcript_path = os.path.join(sessions_dir, f"{session_id}.transcript.jsonl")
        if not os.path.exists(transcript_path):
            continue

        header = None
        timeline = []  # all entries and api_calls in order

        with open(transcript_path) as tf:
            for line in tf:
                obj = json.loads(line)
                if obj["kind"] == "header":
                    header = obj
                else:
                    timeline.append(obj)

        if not header:
            continue

        # Infer role
        parent = header.get("parent_session_id")
        prompt = meta.get("config", {}).get("base_prompt_override", "")
        system_prompt = header.get("system_prompt", "")
        if not parent:
            role = "coordinator"
        elif "You are reviewing" in prompt or "reviewer" in system_prompt.lower()[:500]:
            role = "reviewer"
        elif "explorer" in system_prompt.lower()[:500]:
            role = "explorer"
        else:
            role = "implementer"

        # Compute token totals and timing from api_calls
        total_input = 0
        total_output = 0
        total_reasoning = 0
        total_cache_read = 0
        total_latency_ms = 0
        num_rounds = 0
        first_ts = None
        last_ts = None
        for item in timeline:
            if item["kind"] == "api_call":
                num_rounds = max(num_rounds, item.get("round", 0) + 1)
                usage = item.get("response", {}).get("usage", {})
                total_input += usage.get("input_tokens", 0)
                total_output += usage.get("output_tokens", 0)
                total_reasoning += usage.get("reasoning_tokens", 0)
                total_cache_read += usage.get("cache_read_tokens", 0)
                total_latency_ms += item.get("latency_ms", 0)
                ts = item.get("ts")
                if ts:
                    if first_ts is None or ts < first_ts:
                        first_ts = ts
                    if last_ts is None or ts > last_ts:
                        last_ts = ts

        # Wall clock: from first API call to last API call + its latency
        wall_clock_s = None
        if first_ts and last_ts:
            from datetime import datetime
            try:
                t0 = datetime.fromisoformat(first_ts.replace("Z", "+00:00"))
                t1 = datetime.fromisoformat(last_ts.replace("Z", "+00:00"))
                wall_clock_s = (t1 - t0).total_seconds()
                # Add the last call's latency
                wall_clock_s += (item.get("latency_ms", 0) / 1000.0) if item.get("kind") == "api_call" else 0
            except (ValueError, TypeError):
                pass

        sessions[session_id] = {
            "meta": meta,
            "header": header,
            "timeline": timeline,
            "role": role,
            "parent": parent,
            "task": header.get("task", ""),
            "model": meta.get("model", header.get("model", "?")),
            "turn_count": meta.get("turn_count", 0),
            "first_ts": first_ts,
            "last_ts": last_ts,
            "wall_clock_s": wall_clock_s,
            "usage": {
                "input_tokens": total_input,
                "output_tokens": total_output,
                "reasoning_tokens": total_reasoning,
                "cache_read_tokens": total_cache_read,
                "total_tokens": total_input + total_output,
                "total_latency_ms": total_latency_ms,
                "num_rounds": num_rounds,
            },
        }

    return sessions


def build_session_tree(sessions):
    """Build a tree of session IDs rooted at the coordinator.

    Returns (root_id, children_map) where children_map[id] = [child_ids]
    in the order the children were spawned.
    """
    children = {}
    root = None
    for sid, s in sessions.items():
        children.setdefault(sid, [])
        if s["parent"] is None:
            root = sid
        else:
            children.setdefault(s["parent"], []).append(sid)
    return root, children


def _esc(text):
    """HTML-escape text."""
    return html.escape(str(text))


def _fmt_tokens(n):
    """Format token count with commas."""
    return f"{n:,}"


def _collapsible(text, summary_label="content"):
    """Wrap long text in a collapsible <details> block. Never truncates."""
    if len(text) <= COLLAPSE_THRESHOLD:
        return f'<pre>{_esc(text)}</pre>'
    return (
        f'<details><summary>{summary_label} ({len(text)} chars)</summary>'
        f'<pre>{_esc(text)}</pre></details>'
    )


def _parse_tool_call_from_entry(content_item):
    """Extract tool name and arguments from a tool_call content item."""
    tc = content_item.get("tool_call", {})
    name = tc.get("name", "unknown")
    args_raw = tc.get("arguments", "")
    if isinstance(args_raw, str):
        try:
            args = json.loads(args_raw)
        except (json.JSONDecodeError, ValueError):
            args = args_raw
    else:
        args = args_raw
    call_id = tc.get("id", "")
    return name, args, call_id


def _parse_tool_result_from_entry(content_item):
    """Extract tool name, result content, and error status from a tool_result."""
    tr = content_item.get("tool_result", {})
    name = tr.get("name", "unknown")
    content = tr.get("content", "")
    is_error = tr.get("is_error", False)
    call_id = tr.get("tool_call_id", "")
    return name, content, is_error, call_id


def _render_tool_args(name, args):
    """Render tool arguments as formatted HTML. Never truncates."""
    if isinstance(args, dict):
        # Special handling for delegate - show key params prominently.
        if name == "delegate":
            parts = []
            for key in ("agent_type", "model", "reasoning_effort", "max_turns", "blocking"):
                if key in args:
                    parts.append(f'<span class="arg-key">{_esc(key)}</span>: {_esc(str(args[key]))}')
            if "task" in args:
                task_text = args["task"]
                parts.append(f'<span class="arg-key">task</span>: <span class="delegation-text">{_collapsible(task_text, "delegation task")}</span>')
            return "<br>".join(parts)

        formatted = json.dumps(args, indent=2)
        return _collapsible(formatted, "arguments")
    else:
        text = str(args)
        return _collapsible(text, "arguments")


def _render_tool_result(name, content, is_error):
    """Render a tool result as HTML with collapsible long content."""
    if isinstance(content, str):
        # Try to parse JSON for nicer formatting
        try:
            parsed = json.loads(content)
            content_text = json.dumps(parsed, indent=2)
        except (json.JSONDecodeError, ValueError):
            content_text = content
    else:
        content_text = json.dumps(content, indent=2) if content else "(empty)"

    error_class = " error" if is_error else ""
    return f'<div class="tool-result{error_class}">{_collapsible(content_text, "result")}</div>'


def render_session_html(session_id, session, sessions, depth=0):
    """Render a single session's transcript as HTML."""
    role = session["role"]
    model = session["model"]
    usage = session["usage"]
    task_text = session.get("task", "")
    timeline = session["timeline"]

    parts = []

    # Timing info
    first_ts = session.get("first_ts", "")
    last_ts = session.get("last_ts", "")
    wall_s = session.get("wall_clock_s")
    wall_str = f"{wall_s:.0f}s" if wall_s is not None else "?"
    ts_str = first_ts[:19].replace("T", " ") if first_ts else "?"
    latency_s = usage.get("total_latency_ms", 0) / 1000.0

    # Session header
    parts.append(f'<div class="session session-{role}" style="margin-left: {depth * 24}px">')
    parts.append(f'<div class="session-header" onclick="this.parentElement.querySelector(\'.session-body\').classList.toggle(\'collapsed\')">')
    parts.append(f'<span class="role-badge role-{role}">{_esc(role)}</span>')
    parts.append(f'<span class="session-id">{_esc(session_id[:12])}</span>')
    parts.append(f'<span class="session-model">{_esc(model)}</span>')
    parts.append(f'<span class="session-stats">{usage["num_rounds"]} rounds | '
                 f'{wall_str} wall / {latency_s:.0f}s API | '
                 f'{_fmt_tokens(usage["total_tokens"])} tokens '
                 f'({_fmt_tokens(usage["input_tokens"])} in / '
                 f'{_fmt_tokens(usage["output_tokens"])} out'
                 f'{" / " + _fmt_tokens(usage["reasoning_tokens"]) + " reasoning" if usage["reasoning_tokens"] else ""})'
                 f' | started {ts_str}</span>')
    parts.append(f'<span class="collapse-indicator">[click to toggle]</span>')
    parts.append('</div>')

    # Task delegation text (for subagents)
    if task_text and role != "coordinator":
        parts.append(f'<div class="task-delegation">')
        parts.append(f'<div class="task-label">Delegated task:</div>')
        parts.append(_collapsible(task_text, "delegation task"))
        parts.append('</div>')

    # Session body with timeline
    parts.append('<div class="session-body">')

    current_round = -1
    for item in timeline:
        if item["kind"] == "api_call":
            rnd = item.get("round", 0)
            if rnd != current_round:
                if current_round >= 0:
                    parts.append('</div>')  # close previous round
                current_round = rnd
                latency = item.get("latency_ms", 0)
                ts = item.get("ts", "")
                ts_display = ts[:19].replace("T", " ") if ts else ""
                resp = item.get("response", {})
                finish = resp.get("finish_reason", "")
                parts.append(f'<div class="round">')
                parts.append(f'<div class="round-header">')
                parts.append(f'Round {rnd}')
                parts.append(f'<span class="round-meta">{ts_display} | {latency}ms | {finish}</span>')
                parts.append('</div>')

            # Show text output from the raw response
            raw = item.get("response", {}).get("raw", {})
            for output_item in raw.get("output", []):
                if output_item.get("type") == "message":
                    for c in output_item.get("content", []):
                        if c.get("type") == "output_text" and c.get("text"):
                            text = c["text"]
                            # Check if this is the coordinator's plan (round 0)
                            if rnd == 0 and role == "coordinator" and "plan" in text.lower()[:50]:
                                parts.append(f'<div class="plan-block">')
                                parts.append(f'<div class="plan-label">Coordinator Plan</div>')
                                if len(text) > 500:
                                    preview = text[:500]
                                    parts.append(f'<pre class="plan-text">{_esc(preview)}</pre>')
                                    parts.append(f'<details><summary>Show full plan ({len(text)} chars)</summary>')
                                    parts.append(f'<pre class="plan-text">{_esc(text)}</pre>')
                                    parts.append('</details>')
                                else:
                                    parts.append(f'<pre class="plan-text">{_esc(text)}</pre>')
                                parts.append('</div>')
                            else:
                                parts.append(f'<div class="text-output"><pre>{_esc(text)}</pre></div>')

        elif item["kind"] == "entry":
            turn = item.get("turn", {})
            turn_kind = turn.get("kind", "")
            msg = turn.get("message", {})
            content = msg.get("content", [])

            if not isinstance(content, list):
                continue

            for c in content:
                if not isinstance(c, dict):
                    continue

                if c.get("kind") == "text" and turn_kind == "USER_INPUT":
                    text = c.get("text", "")
                    if text:
                        parts.append(f'<div class="user-input">')
                        parts.append(f'<div class="input-label">User Input</div>')
                        parts.append(_collapsible(text, "text output"))
                        parts.append('</div>')

                elif c.get("kind") == "tool_call":
                    name, args, call_id = _parse_tool_call_from_entry(c)
                    tool_class = "tool-spawn" if name == "delegate" else \
                                 "tool-communicate" if name == "communicate" else ""
                    parts.append(f'<div class="tool-call {tool_class}">')
                    parts.append(f'<span class="tool-name">{_esc(name)}</span>')
                    parts.append(_render_tool_args(name, args))
                    parts.append('</div>')

                elif c.get("kind") == "tool_result":
                    name, result_content, is_error, call_id = _parse_tool_result_from_entry(c)
                    parts.append(f'<div class="tool-result-block">')
                    parts.append(f'<span class="result-label">{"ERROR" if is_error else "Result"} from {_esc(name)}:</span>')
                    parts.append(_render_tool_result(name, result_content, is_error))
                    parts.append('</div>')

    # Close last round if open
    if current_round >= 0:
        parts.append('</div>')

    parts.append('</div>')  # session-body
    parts.append('</div>')  # session

    return "\n".join(parts)


def render_html(sessions, root_id, children_map, run_id, rep, task):
    """Render the complete HTML page."""

    # Compute aggregate stats
    total_tokens = sum(s["usage"]["total_tokens"] for s in sessions.values())
    total_input = sum(s["usage"]["input_tokens"] for s in sessions.values())
    total_output = sum(s["usage"]["output_tokens"] for s in sessions.values())
    total_reasoning = sum(s["usage"]["reasoning_tokens"] for s in sessions.values())
    total_rounds = sum(s["usage"]["num_rounds"] for s in sessions.values())

    # Build session HTML recursively
    def render_tree(sid, depth=0):
        parts = []
        session = sessions.get(sid)
        if session:
            parts.append(render_session_html(sid, session, sessions, depth))
        for child_id in children_map.get(sid, []):
            parts.append(render_tree(child_id, depth + 1))
        return "\n".join(parts)

    sessions_html = render_tree(root_id) if root_id else "<p>No sessions found.</p>"

    # Session index
    index_rows = []
    def build_index(sid, depth=0):
        s = sessions.get(sid)
        if not s:
            return
        indent = "&nbsp;" * (depth * 4)
        role = s["role"]
        u = s["usage"]
        wall = s.get("wall_clock_s")
        wall_str = f"{wall:.0f}s" if wall is not None else "—"
        t0 = s.get("first_ts", "")
        t0_display = t0[:19].replace("T", " ") if t0 else "—"
        index_rows.append(
            f'<tr class="index-{role}">'
            f'<td>{indent}<span class="role-badge role-{role}">{_esc(role)}</span></td>'
            f'<td class="mono">{_esc(sid[:12])}</td>'
            f'<td>{_esc(s["model"])}</td>'
            f'<td class="right">{u["num_rounds"]}</td>'
            f'<td class="right">{wall_str}</td>'
            f'<td class="right">{t0_display}</td>'
            f'<td class="right">{_fmt_tokens(u["input_tokens"])}</td>'
            f'<td class="right">{_fmt_tokens(u["output_tokens"])}</td>'
            f'<td class="right">{_fmt_tokens(u["reasoning_tokens"])}</td>'
            f'<td class="right">{_fmt_tokens(u["total_tokens"])}</td>'
            f'</tr>'
        )
        for child_id in children_map.get(sid, []):
            build_index(child_id, depth + 1)

    if root_id:
        build_index(root_id)
    index_html = "\n".join(index_rows)

    # Overall wall clock: from earliest first_ts to latest last_ts across all sessions
    all_first = [s["first_ts"] for s in sessions.values() if s.get("first_ts")]
    all_last = [s["last_ts"] for s in sessions.values() if s.get("last_ts")]
    overall_wall_str = "?"
    overall_start_str = "?"
    overall_end_str = "?"
    if all_first and all_last:
        from datetime import datetime
        try:
            t0 = min(datetime.fromisoformat(t.replace("Z", "+00:00")) for t in all_first)
            t1 = max(datetime.fromisoformat(t.replace("Z", "+00:00")) for t in all_last)
            overall_s = (t1 - t0).total_seconds()
            mins, secs = divmod(int(overall_s), 60)
            overall_wall_str = f"{mins}m {secs}s" if mins else f"{secs}s"
            overall_start_str = t0.strftime("%H:%M:%S")
            overall_end_str = t1.strftime("%H:%M:%S")
        except (ValueError, TypeError):
            pass

    total_api_latency_ms = sum(s["usage"].get("total_latency_ms", 0) for s in sessions.values())
    total_api_s = total_api_latency_ms / 1000.0

    return f"""<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Transcript: {_esc(task)} | {_esc(run_id)} rep {rep}</title>
<style>
  * {{ margin: 0; padding: 0; box-sizing: border-box; }}
  body {{
    font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif;
    background: #0d1117; color: #c9d1d9; padding: 24px;
    line-height: 1.5;
  }}
  h1 {{ font-size: 20px; color: #f0f6fc; margin-bottom: 4px; }}
  .subtitle {{ font-size: 13px; color: #8b949e; margin-bottom: 20px; }}

  /* Stats bar */
  .stats-bar {{
    display: flex; gap: 12px; margin-bottom: 24px; flex-wrap: wrap;
  }}
  .stat {{
    background: #161b22; border: 1px solid #30363d; border-radius: 8px;
    padding: 10px 14px; min-width: 110px;
  }}
  .stat .label {{ font-size: 11px; color: #8b949e; text-transform: uppercase; letter-spacing: 0.5px; }}
  .stat .value {{ font-size: 20px; font-weight: 600; color: #f0f6fc; }}

  /* Session index table */
  .index-table {{
    width: 100%; border-collapse: collapse; margin-bottom: 28px;
    font-size: 13px;
  }}
  .index-table th {{
    background: #161b22; border: 1px solid #30363d; padding: 6px 10px;
    text-align: left; font-weight: 600; color: #f0f6fc;
  }}
  .index-table td {{
    border: 1px solid #21262d; padding: 5px 10px;
  }}
  .index-table .right {{ text-align: right; }}
  .index-table .mono {{ font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px; }}
  .index-coordinator td {{ background: #0d1a2e; }}
  .index-implementer td {{ background: #0d1f15; }}
  .index-reviewer td {{ background: #1f1a0d; }}
  .index-explorer td {{ background: #1a0d1f; }}

  /* Role badges */
  .role-badge {{
    display: inline-block; padding: 2px 8px; border-radius: 4px;
    font-size: 11px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.5px;
  }}
  .role-coordinator {{ background: #1f3a5f; color: #58a6ff; }}
  .role-implementer {{ background: #1a3a2a; color: #3fb950; }}
  .role-reviewer {{ background: #3a2a1a; color: #d29922; }}
  .role-explorer {{ background: #2a1a3a; color: #bc8cff; }}

  /* Session blocks */
  .session {{
    border: 1px solid #30363d; border-radius: 8px; margin-bottom: 16px;
    overflow: hidden;
  }}
  .session-coordinator {{ border-left: 3px solid #58a6ff; }}
  .session-implementer {{ border-left: 3px solid #3fb950; }}
  .session-reviewer {{ border-left: 3px solid #d29922; }}
  .session-explorer {{ border-left: 3px solid #bc8cff; }}

  .session-header {{
    background: #161b22; padding: 10px 16px; cursor: pointer;
    display: flex; align-items: center; gap: 12px; flex-wrap: wrap;
    user-select: none;
  }}
  .session-header:hover {{ background: #1c2128; }}
  .session-id {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px; color: #8b949e;
  }}
  .session-model {{ font-size: 12px; color: #8b949e; }}
  .session-stats {{ font-size: 12px; color: #8b949e; margin-left: auto; }}
  .collapse-indicator {{ font-size: 11px; color: #484f58; }}

  .session-body {{ padding: 12px 16px; }}
  .session-body.collapsed {{ display: none; }}

  /* Task delegation */
  .task-delegation {{
    background: #161b22; border: 1px solid #30363d; border-radius: 6px;
    padding: 10px 14px; margin: 8px 16px 4px 16px;
  }}
  .task-label {{
    font-size: 11px; font-weight: 600; color: #8b949e;
    text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 6px;
  }}
  .task-text {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px;
    color: #c9d1d9; white-space: pre-wrap; word-break: break-word;
    line-height: 1.4;
  }}

  /* Round blocks */
  .round {{
    border-left: 2px solid #21262d; margin: 10px 0; padding-left: 14px;
  }}
  .round-header {{
    font-size: 12px; font-weight: 600; color: #8b949e; margin-bottom: 6px;
  }}
  .round-meta {{ font-weight: 400; color: #484f58; margin-left: 8px; }}

  /* Plan block */
  .plan-block {{
    background: #1a2d50; border: 1px solid #2a4a7f; border-radius: 6px;
    padding: 12px 16px; margin: 8px 0;
  }}
  .plan-label {{
    font-size: 13px; font-weight: 700; color: #58a6ff; margin-bottom: 8px;
    text-transform: uppercase; letter-spacing: 0.5px;
  }}
  .plan-text {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px;
    color: #e6edf3; white-space: pre-wrap; word-break: break-word;
    line-height: 1.5;
  }}

  /* Text output */
  .text-output {{
    margin: 6px 0;
  }}
  .text-output pre {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px;
    color: #c9d1d9; white-space: pre-wrap; word-break: break-word;
    line-height: 1.4; background: #161b22; padding: 8px 12px;
    border-radius: 4px;
  }}

  /* User input */
  .user-input {{
    background: #1a1625; border: 1px solid #30363d; border-radius: 6px;
    padding: 10px 14px; margin: 8px 0;
  }}
  .input-label {{
    font-size: 11px; font-weight: 600; color: #bc8cff;
    text-transform: uppercase; letter-spacing: 0.5px; margin-bottom: 6px;
  }}
  .user-input pre {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 12px;
    color: #c9d1d9; white-space: pre-wrap; word-break: break-word;
    line-height: 1.4;
  }}

  /* Tool calls */
  .tool-call {{
    background: #161b22; border: 1px solid #21262d; border-radius: 6px;
    padding: 8px 12px; margin: 4px 0;
  }}
  .tool-spawn {{
    border-color: #1f3a5f; background: #0d1a2e;
  }}
  .tool-communicate {{
    border-color: #1a3a2a; background: #0d1f15;
  }}
  .tool-name {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 13px;
    font-weight: 700; color: #79c0ff;
  }}
  .tool-spawn .tool-name {{ color: #58a6ff; }}
  .tool-communicate .tool-name {{ color: #3fb950; }}
  .tool-args {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 11px;
    color: #8b949e; white-space: pre-wrap; word-break: break-word;
    margin-top: 4px; line-height: 1.3;
  }}
  .arg-key {{ color: #d2a8ff; font-weight: 600; }}
  .delegation-text {{ color: #c9d1d9; }}

  /* Tool results */
  .tool-result-block {{
    margin: 4px 0;
  }}
  .result-label {{
    font-size: 11px; color: #8b949e; font-weight: 600;
  }}
  .tool-result {{
    background: #0d1117; border: 1px solid #21262d; border-radius: 4px;
    padding: 6px 10px; margin-top: 2px;
  }}
  .tool-result.error {{
    border-color: #f8514966; background: #1a0000;
  }}
  .tool-result pre, .result-preview {{
    font-family: 'SF Mono', 'Consolas', monospace; font-size: 11px;
    color: #8b949e; white-space: pre-wrap; word-break: break-word;
    line-height: 1.3;
  }}
  .tool-result.error pre {{ color: #f85149; }}

  /* Collapsible details */
  details {{
    margin-top: 4px;
  }}
  details summary {{
    font-size: 11px; color: #58a6ff; cursor: pointer;
    font-family: 'SF Mono', 'Consolas', monospace;
  }}
  details summary:hover {{ color: #79c0ff; }}
</style>
</head>
<body>

<h1>Transcript: {_esc(task)}</h1>
<div class="subtitle">{_esc(run_id)} &middot; rep {rep} &middot; {len(sessions)} session(s)</div>

<div class="stats-bar">
  <div class="stat"><div class="label">Wall Clock</div><div class="value">{overall_wall_str}</div></div>
  <div class="stat"><div class="label">API Time</div><div class="value">{total_api_s:.0f}s</div></div>
  <div class="stat"><div class="label">Start / End</div><div class="value" style="font-size:14px">{overall_start_str} &rarr; {overall_end_str}</div></div>
  <div class="stat"><div class="label">Sessions</div><div class="value">{len(sessions)}</div></div>
  <div class="stat"><div class="label">Total Rounds</div><div class="value">{total_rounds}</div></div>
  <div class="stat"><div class="label">Total Tokens</div><div class="value">{_fmt_tokens(total_tokens)}</div></div>
  <div class="stat"><div class="label">Reasoning</div><div class="value">{_fmt_tokens(total_reasoning)}</div></div>
</div>

<h2 style="font-size: 15px; color: #f0f6fc; margin-bottom: 10px;">Session Index</h2>
<table class="index-table">
<tr>
  <th>Role</th><th>Session ID</th><th>Model</th>
  <th>Rounds</th><th>Wall</th><th>Started</th><th>Input</th><th>Output</th><th>Reasoning</th><th>Total</th>
</tr>
{index_html}
</table>

<h2 style="font-size: 15px; color: #f0f6fc; margin-bottom: 10px;">Transcript</h2>
{sessions_html}

</body>
</html>"""


def main():
    parser = argparse.ArgumentParser(
        description="Export serf agent transcripts as formatted HTML")
    parser.add_argument("--run", required=True, help="Run ID (wave name or run identifier)")
    parser.add_argument("--rep", required=True, type=int, help="Rep number")
    parser.add_argument("--task", required=True, help="Task name")
    parser.add_argument("--output", "-o", default=None,
                        help="Output file (default: stdout)")
    args = parser.parse_args()

    state_dir = download_results(args.run, args.rep, args.task)
    if not state_dir:
        print(f"ERROR: Could not find agent-state for run={args.run} "
              f"rep={args.rep} task={args.task}", file=sys.stderr)
        sys.exit(1)

    print(f"Loading sessions from {state_dir}...", file=sys.stderr)
    sessions = load_sessions(state_dir)
    if not sessions:
        print("ERROR: No sessions found", file=sys.stderr)
        sys.exit(1)

    root_id, children_map = build_session_tree(sessions)
    if not root_id:
        print("WARNING: No root (coordinator) session found, using first session",
              file=sys.stderr)
        root_id = next(iter(sessions))

    print(f"Rendering {len(sessions)} session(s)...", file=sys.stderr)
    html_output = render_html(sessions, root_id, children_map,
                              args.run, args.rep, args.task)

    if args.output:
        with open(args.output, "w") as f:
            f.write(html_output)
        print(f"Written to {args.output}", file=sys.stderr)
    else:
        print(html_output)


if __name__ == "__main__":
    main()
