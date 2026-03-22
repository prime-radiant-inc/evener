#!/usr/bin/env python3
"""Generate a mini website from benchmark transcript logs.

Usage:
    python3 transcript-viewer.py <job_dir> [--output <dir>] [--filter failed|passed|all]

Produces an index.html with collapsible task views, each showing all
sessions (including subagent transcripts) with formatted tool calls,
assistant text, and results.
"""

import argparse
import glob
import html
import json
import os
import sys
from datetime import datetime
from pathlib import Path


def parse_args():
    p = argparse.ArgumentParser(description="Benchmark transcript viewer")
    p.add_argument("job_dir", help="Path to harbor job directory")
    p.add_argument("--output", "-o", default=None, help="Output directory (default: <job_dir>/viewer)")
    p.add_argument("--filter", choices=["failed", "passed", "all"], default="failed",
                   help="Which tasks to include (default: failed)")
    return p.parse_args()


def load_task_info(task_dir):
    """Load task metadata from result.json and reward.txt."""
    info = {"name": os.path.basename(task_dir).rsplit("__", 1)[0], "dir": task_dir}

    reward_file = os.path.join(task_dir, "verifier", "reward.txt")
    if os.path.exists(reward_file):
        info["reward"] = open(reward_file).read().strip()
    else:
        info["reward"] = "?"

    result_file = os.path.join(task_dir, "result.json")
    if os.path.exists(result_file):
        try:
            r = json.load(open(result_file))
            info["exception"] = r.get("exception_info", {})
            info["agent_execution"] = r.get("agent_execution", {})
            info["config"] = r.get("config", {})
        except Exception:
            pass

    test_out = os.path.join(task_dir, "verifier", "test-stdout.txt")
    if os.path.exists(test_out):
        info["test_output"] = open(test_out).read()
    else:
        info["test_output"] = ""

    # Agent stdout
    stdout = os.path.join(task_dir, "agent", "command-0", "stdout.txt")
    if os.path.exists(stdout):
        info["agent_stdout"] = open(stdout).read()
    else:
        info["agent_stdout"] = ""

    return info


def load_sessions(task_dir):
    """Load all session transcripts, sorted by creation time."""
    sessions = []
    session_dir = os.path.join(task_dir, "agent", "serf-state", "sessions")
    if not os.path.isdir(session_dir):
        return sessions

    for jf in sorted(glob.glob(os.path.join(session_dir, "*.json"))):
        sid = os.path.basename(jf).replace(".json", "")
        transcript_file = jf.replace(".json", ".transcript.jsonl")

        meta = {}
        try:
            meta = json.load(open(jf))
        except Exception:
            pass

        entries = []
        header = {}
        if os.path.exists(transcript_file):
            for line in open(transcript_file):
                line = line.strip()
                if not line:
                    continue
                try:
                    obj = json.loads(line)
                    if obj.get("kind") == "header":
                        header = obj
                    elif obj.get("kind") == "entry":
                        entries.append(obj)
                except Exception:
                    pass

        sessions.append({
            "id": sid,
            "meta": meta,
            "header": header,
            "entries": entries,
        })

    # Sort by created_at from header
    def sort_key(s):
        return s["header"].get("created_at", s["id"])
    sessions.sort(key=sort_key)
    return sessions


def escape(text):
    return html.escape(str(text))


def truncate(text, limit=500):
    if len(text) <= limit:
        return text
    return text[:limit] + f"... ({len(text)} chars total)"


def format_tool_args(args_str, limit=300):
    """Pretty-format tool arguments."""
    if isinstance(args_str, dict):
        args_str = json.dumps(args_str, indent=2)
    try:
        obj = json.loads(args_str)
        formatted = json.dumps(obj, indent=2)
        if len(formatted) > limit:
            # Show truncated but allow expand
            return formatted
        return formatted
    except Exception:
        return str(args_str)


def render_entry(entry):
    """Render a single transcript entry to HTML."""
    turn = entry.get("turn", {})
    kind = turn.get("kind", "")
    msg = turn.get("message", {})
    seq = entry.get("seq", "?")
    usage = turn.get("usage", {})

    parts = []

    if kind == "USER_INPUT":
        text = ""
        for c in msg.get("content", []):
            if c.get("kind") == "text":
                text = c.get("text", "")
        parts.append(f'<div class="entry user-input">'
                     f'<span class="seq">#{seq}</span> '
                     f'<span class="badge badge-user">USER</span> '
                     f'<pre class="content">{escape(text)}</pre>'
                     f'</div>')

    elif kind == "ASSISTANT":
        for c in msg.get("content", []):
            if c.get("kind") == "text":
                text = c.get("text", "").strip()
                if text:
                    parts.append(
                        f'<div class="entry assistant-text">'
                        f'<span class="seq">#{seq}</span> '
                        f'<span class="badge badge-assistant">ASSISTANT</span> '
                        f'<pre class="content">{escape(text)}</pre>'
                        f'</div>')
            elif c.get("kind") == "tool_call":
                tc = c["tool_call"]
                name = tc.get("name", "?")
                args = format_tool_args(tc.get("arguments", ""))
                badge_class = "badge-tool"
                if name == "submit_result":
                    badge_class = "badge-submit"
                elif name in ("approve", "reject"):
                    badge_class = "badge-verdict"
                elif name == "spawn_agent":
                    badge_class = "badge-spawn"

                args_html = escape(args)
                args_id = f"args-{seq}-{tc.get('id', '')[:8]}"
                collapsed = len(args) > 300
                if collapsed:
                    parts.append(
                        f'<div class="entry tool-call">'
                        f'<span class="seq">#{seq}</span> '
                        f'<span class="badge {badge_class}">{escape(name)}</span> '
                        f'<button class="toggle-btn" onclick="toggle(\'{args_id}\')">show args</button>'
                        f'<pre class="content collapsible" id="{args_id}" style="display:none">{args_html}</pre>'
                        f'</div>')
                else:
                    parts.append(
                        f'<div class="entry tool-call">'
                        f'<span class="seq">#{seq}</span> '
                        f'<span class="badge {badge_class}">{escape(name)}</span> '
                        f'<pre class="content">{args_html}</pre>'
                        f'</div>')

        # Usage line
        if usage and usage.get("input_tokens"):
            usage_str = f"in={usage.get('input_tokens',0)} out={usage.get('output_tokens',0)}"
            cache = (usage.get("raw") or {}).get("input_tokens_details", {}).get("cached_tokens")
            if cache:
                usage_str += f" cache={cache}"
            reasoning = (usage.get("raw") or {}).get("output_tokens_details", {}).get("reasoning_tokens")
            if reasoning:
                usage_str += f" reasoning={reasoning}"
            parts.append(f'<div class="usage">{usage_str}</div>')

    elif kind == "TOOL_RESULTS":
        for c in msg.get("content", []):
            if c.get("kind") == "tool_result":
                tr = c["tool_result"]
                name = tr.get("name", "?")
                content = tr.get("content", "")
                is_error = tr.get("is_error", False)
                err_class = " error" if is_error else ""

                content_html = escape(content)
                result_id = f"result-{seq}-{tr.get('tool_call_id', '')[:8]}"
                collapsed = len(content) > 400
                if collapsed:
                    preview = escape(truncate(content, 200))
                    parts.append(
                        f'<div class="entry tool-result{err_class}">'
                        f'<span class="seq">#{seq}</span> '
                        f'<span class="badge badge-result">{"ERROR" if is_error else "RESULT"}: {escape(name)}</span> '
                        f'<pre class="content preview">{preview}</pre>'
                        f'<button class="toggle-btn" onclick="toggle(\'{result_id}\')">show full</button>'
                        f'<pre class="content collapsible" id="{result_id}" style="display:none">{content_html}</pre>'
                        f'</div>')
                else:
                    parts.append(
                        f'<div class="entry tool-result{err_class}">'
                        f'<span class="seq">#{seq}</span> '
                        f'<span class="badge badge-result">{"ERROR" if is_error else "RESULT"}: {escape(name)}</span> '
                        f'<pre class="content">{content_html}</pre>'
                        f'</div>')

    return "\n".join(parts)


def render_session(session, index):
    """Render a full session to HTML."""
    header = session["header"]
    entries = session["entries"]
    sid = session["id"]
    depth = header.get("depth", "?")
    task = header.get("task", "")
    model = header.get("model", "?")
    parent_id = header.get("parent_session_id", "")

    label = "Main Agent" if depth in (0, "?") and not parent_id else f"Subagent (depth={depth})"
    if parent_id:
        label += f" &larr; {parent_id[:12]}"

    # Detect reviewer vs test-writer vs other subagent
    if task and "review" in task.lower()[:50]:
        label = f"Reviewer (depth={depth})"
    elif task and ("test" in task.lower()[:30] or "spec:" in task.lower()[:30]):
        label = f"Test-Writer (depth={depth})"

    session_id = f"session-{sid[:12]}-{index}"

    task_preview = escape(truncate(task, 200)) if task else "(no task text)"

    entry_html = "\n".join(render_entry(e) for e in entries)

    return f"""
    <div class="session">
        <div class="session-header" onclick="toggle('{session_id}')">
            <span class="session-label">{label}</span>
            <span class="session-model">{escape(model)}</span>
            <span class="session-turns">{len(entries)} entries</span>
            <span class="session-id">{sid[:16]}</span>
        </div>
        <div class="session-task"><strong>Task:</strong> {task_preview}</div>
        <div class="session-body" id="{session_id}">
            {entry_html}
        </div>
    </div>
    """


def render_task(task_info, sessions):
    """Render a full task page section."""
    name = task_info["name"]
    reward = task_info["reward"]
    exc = task_info.get("exception") or {}
    exc_type = exc.get("exception_type", "")
    exc_msg = exc.get("exception_message", "")[:120] if exc.get("exception_message") else ""

    # Classify failure
    agent_stdout = task_info.get("agent_stdout", "")
    submitted = "[submit_result]" in agent_stdout
    if exc_type == "AgentTimeoutError":
        fail_class = "timeout"
        fail_label = "TIMEOUT"
    elif submitted:
        fail_class = "wrong-answer"
        fail_label = "WRONG ANSWER"
    elif "[error]" in agent_stdout:
        fail_class = "api-error"
        fail_label = "ERROR"
    else:
        fail_class = "no-submit"
        fail_label = "NO SUBMIT"

    reward_class = "reward-pass" if reward == "1" else "reward-fail"

    # Test output
    test_output = task_info.get("test_output", "")
    test_html = ""
    if test_output:
        test_id = f"test-{name}"
        test_html = f"""
        <div class="test-output">
            <button class="toggle-btn" onclick="toggle('{test_id}')">Verifier Output</button>
            <pre class="collapsible" id="{test_id}" style="display:none">{escape(test_output[-3000:])}</pre>
        </div>
        """

    sessions_html = "\n".join(render_session(s, i) for i, s in enumerate(sessions))

    task_body_id = f"task-{name}"

    return f"""
    <div class="task {fail_class}" id="anchor-{name}">
        <div class="task-header" onclick="toggle('{task_body_id}')">
            <span class="task-name">{escape(name)}</span>
            <span class="badge {reward_class}">reward={escape(reward)}</span>
            <span class="badge badge-{fail_class}">{fail_label}</span>
            <span class="task-sessions">{len(sessions)} session(s)</span>
            {f'<span class="exc-info">{escape(exc_type)}</span>' if exc_type else ''}
        </div>
        <div class="task-body" id="{task_body_id}" style="display:none">
            {test_html}
            {sessions_html}
        </div>
    </div>
    """


CSS = """
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, monospace;
       background: #0d1117; color: #c9d1d9; padding: 20px; max-width: 1400px; margin: 0 auto; }
h1 { color: #58a6ff; margin-bottom: 8px; }
.summary { color: #8b949e; margin-bottom: 20px; font-size: 14px; }
.summary strong { color: #c9d1d9; }
.controls { margin-bottom: 16px; display: flex; gap: 8px; flex-wrap: wrap; }
.controls button { background: #21262d; color: #c9d1d9; border: 1px solid #30363d;
                   padding: 6px 12px; border-radius: 6px; cursor: pointer; font-size: 13px; }
.controls button:hover { background: #30363d; }
.controls button.active { background: #388bfd33; border-color: #388bfd; color: #58a6ff; }

.task { border: 1px solid #30363d; border-radius: 8px; margin-bottom: 8px; overflow: hidden; }
.task-header { padding: 12px 16px; cursor: pointer; display: flex; align-items: center;
               gap: 10px; flex-wrap: wrap; background: #161b22; }
.task-header:hover { background: #1c2128; }
.task-name { font-weight: 600; font-size: 15px; color: #e6edf3; min-width: 200px; }
.task-sessions { color: #8b949e; font-size: 12px; }
.exc-info { color: #f85149; font-size: 12px; }
.task-body { padding: 0 16px 16px; }

.badge { padding: 2px 8px; border-radius: 12px; font-size: 11px; font-weight: 600; display: inline-block; }
.reward-pass { background: #238636; color: #fff; }
.reward-fail { background: #da3633; color: #fff; }
.badge-timeout { background: #9e6a03; color: #fff; }
.badge-wrong-answer { background: #8957e5; color: #fff; }
.badge-no-submit { background: #6e7681; color: #fff; }
.badge-api-error { background: #da3633; color: #fff; }

.session { border: 1px solid #21262d; border-radius: 6px; margin: 12px 0; }
.session-header { padding: 10px 14px; background: #0d1117; cursor: pointer;
                  display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.session-header:hover { background: #161b22; }
.session-label { font-weight: 600; color: #79c0ff; font-size: 13px; }
.session-model { color: #8b949e; font-size: 12px; }
.session-turns { color: #8b949e; font-size: 12px; }
.session-id { color: #484f58; font-size: 11px; font-family: monospace; }
.session-task { padding: 6px 14px; font-size: 12px; color: #8b949e; border-bottom: 1px solid #21262d; }
.session-body { padding: 8px 14px; }

.entry { margin: 6px 0; padding: 6px 10px; border-radius: 4px; font-size: 13px; }
.seq { color: #484f58; font-size: 11px; font-family: monospace; margin-right: 4px; }

.user-input { background: #0d2a4a; border-left: 3px solid #58a6ff; }
.assistant-text { background: #1a1e24; border-left: 3px solid #3fb950; }
.tool-call { background: #161b22; border-left: 3px solid #d29922; }
.tool-result { background: #161b22; border-left: 3px solid #8b949e; }
.tool-result.error { border-left-color: #f85149; background: #1c0d0d; }

.badge-user { background: #1f6feb; color: #fff; }
.badge-assistant { background: #238636; color: #fff; }
.badge-tool { background: #9e6a03; color: #fff; }
.badge-result { background: #30363d; color: #c9d1d9; }
.badge-submit { background: #8957e5; color: #fff; }
.badge-verdict { background: #da3633; color: #fff; }
.badge-spawn { background: #1f6feb; color: #fff; }

.content { white-space: pre-wrap; word-break: break-word; font-family: 'SF Mono', Monaco,
           'Cascadia Code', monospace; font-size: 12px; margin-top: 4px; color: #c9d1d9;
           max-height: 600px; overflow-y: auto; }
.preview { color: #8b949e; }
.usage { font-size: 11px; color: #484f58; padding: 2px 10px; }

.test-output { margin: 12px 0; }
.test-output pre { background: #0d1117; padding: 12px; border-radius: 6px; font-size: 12px;
                   max-height: 400px; overflow-y: auto; }

.toggle-btn { background: #21262d; color: #8b949e; border: 1px solid #30363d; padding: 2px 8px;
              border-radius: 4px; cursor: pointer; font-size: 11px; }
.toggle-btn:hover { background: #30363d; color: #c9d1d9; }

.collapsible { transition: none; }

.toc { background: #161b22; border: 1px solid #30363d; border-radius: 8px; padding: 16px; margin-bottom: 20px; }
.toc h2 { color: #58a6ff; margin-bottom: 8px; font-size: 14px; }
.toc-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 4px; }
.toc a { color: #c9d1d9; text-decoration: none; font-size: 13px; padding: 4px 8px; border-radius: 4px; display: flex; gap: 6px; align-items: center; }
.toc a:hover { background: #21262d; }
.toc .fail-type { font-size: 10px; padding: 1px 6px; border-radius: 8px; }
"""

JS = """
function toggle(id) {
    var el = document.getElementById(id);
    if (el) {
        el.style.display = el.style.display === 'none' ? 'block' : 'none';
    }
}
function expandAll() {
    document.querySelectorAll('.task-body, .session-body, .collapsible').forEach(function(el) {
        el.style.display = 'block';
    });
}
function collapseAll() {
    document.querySelectorAll('.task-body, .collapsible').forEach(function(el) {
        el.style.display = 'none';
    });
    // Keep session bodies visible when task is open
}
function filterTasks(type) {
    document.querySelectorAll('.task').forEach(function(el) {
        if (type === 'all') {
            el.style.display = '';
        } else {
            el.style.display = el.classList.contains(type) ? '' : 'none';
        }
    });
    document.querySelectorAll('.controls button[data-filter]').forEach(function(btn) {
        btn.classList.toggle('active', btn.dataset.filter === type);
    });
}
"""


def generate_site(job_dir, output_dir, task_filter):
    job_name = os.path.basename(os.path.normpath(job_dir))

    # Find all task dirs
    task_dirs = sorted(glob.glob(os.path.join(job_dir, "*__*")))

    tasks = []
    for td in task_dirs:
        info = load_task_info(td)
        if task_filter == "failed" and info["reward"] == "1":
            continue
        if task_filter == "passed" and info["reward"] != "1":
            continue
        sessions = load_sessions(td)
        tasks.append((info, sessions))

    # Count categories
    n_timeout = sum(1 for info, _ in tasks if (info.get("exception") or {}).get("exception_type") == "AgentTimeoutError")
    n_submitted = sum(1 for info, _ in tasks if "[submit_result]" in info.get("agent_stdout", "") and (info.get("exception") or {}).get("exception_type") != "AgentTimeoutError")
    n_no_submit = len(tasks) - n_timeout - n_submitted

    # Build TOC
    toc_items = []
    for info, sessions in tasks:
        name = info["name"]
        exc_type = (info.get("exception") or {}).get("exception_type", "")
        agent_stdout = info.get("agent_stdout", "")
        if exc_type == "AgentTimeoutError":
            ft = '<span class="fail-type badge-timeout">TIMEOUT</span>'
        elif "[submit_result]" in agent_stdout:
            ft = '<span class="fail-type badge-wrong-answer">WRONG</span>'
        elif "[error]" in agent_stdout:
            ft = '<span class="fail-type badge-api-error">ERROR</span>'
        else:
            ft = '<span class="fail-type badge-no-submit">NO SUBMIT</span>'
        reward_badge = "reward-pass" if info["reward"] == "1" else "reward-fail"
        toc_items.append(f'<a href="#anchor-{name}">{ft} {escape(name)} <span class="badge {reward_badge}" style="font-size:10px">{info["reward"]}</span></a>')

    toc_html = f"""
    <div class="toc">
        <h2>{len(tasks)} tasks ({task_filter})</h2>
        <div class="toc-grid">{"".join(toc_items)}</div>
    </div>
    """

    # Build task sections
    task_sections = "\n".join(render_task(info, sessions) for info, sessions in tasks)

    page = f"""<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{escape(job_name)} - Transcript Viewer</title>
    <style>{CSS}</style>
</head>
<body>
    <h1>{escape(job_name)}</h1>
    <div class="summary">
        <strong>{len(task_dirs)} total tasks</strong> &middot;
        Showing: {len(tasks)} ({task_filter}) &middot;
        Timeout: {n_timeout} &middot; Wrong Answer: {n_submitted} &middot; No Submit/Other: {n_no_submit}
    </div>
    <div class="controls">
        <button onclick="expandAll()">Expand All</button>
        <button onclick="collapseAll()">Collapse All</button>
        <button data-filter="all" onclick="filterTasks('all')" class="active">All</button>
        <button data-filter="timeout" onclick="filterTasks('timeout')">Timeout ({n_timeout})</button>
        <button data-filter="wrong-answer" onclick="filterTasks('wrong-answer')">Wrong Answer ({n_submitted})</button>
        <button data-filter="no-submit" onclick="filterTasks('no-submit')">No Submit ({n_no_submit})</button>
    </div>
    {toc_html}
    {task_sections}
    <script>{JS}</script>
</body>
</html>"""

    os.makedirs(output_dir, exist_ok=True)
    out_file = os.path.join(output_dir, "index.html")
    with open(out_file, "w") as f:
        f.write(page)
    print(f"Generated {out_file} ({len(page)} bytes, {len(tasks)} tasks)")
    return out_file


def main():
    args = parse_args()
    output_dir = args.output or os.path.join(args.job_dir, "viewer")
    generate_site(args.job_dir, output_dir, args.filter)


if __name__ == "__main__":
    main()
