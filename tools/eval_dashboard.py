#!/usr/bin/env python3
"""Serf Eval Dashboard — mission control for benchmark runs.

Usage:
    python3 eval_dashboard.py [--port PORT] [--jobs-root DIR]

Serves a live web dashboard for monitoring serf eval runs on magic-kingdom.
Zero external dependencies — uses only Python stdlib.
"""

import argparse
import glob
import http.server
import json
import os
import subprocess
import time
from pathlib import Path
from urllib.parse import parse_qs, urlparse

# --- Configuration ---

RUNS_DIR = os.path.expanduser("~/git/terminal-bench/runs")
JOBS_ROOT = "/tmp"
ARCHIVE_ROOT = "/data/serf-evals"


# --- System info ---

def get_system_info():
    """Machine health: load, memory, disk, containers."""
    load = os.getloadavg()
    cores = os.cpu_count() or 1

    mem = {}
    try:
        with open("/proc/meminfo") as f:
            for line in f:
                parts = line.split()
                key = parts[0].rstrip(":")
                if key in ("MemTotal", "MemAvailable"):
                    mem[key] = int(parts[1]) * 1024
    except Exception:
        pass

    containers = 0
    try:
        result = subprocess.run(
            ["docker", "ps", "-q"], capture_output=True, text=True, timeout=5
        )
        if result.stdout.strip():
            containers = len(result.stdout.strip().split("\n"))
    except Exception:
        pass

    disk = {}
    try:
        result = subprocess.run(
            ["df", "-B1", "/tmp"], capture_output=True, text=True, timeout=5
        )
        lines = result.stdout.strip().split("\n")
        if len(lines) >= 2:
            parts = lines[1].split()
            disk["total"] = int(parts[1])
            disk["used"] = int(parts[2])
            disk["available"] = int(parts[3])
    except Exception:
        pass

    return {
        "load": [round(l, 2) for l in load],
        "cores": cores,
        "mem_total_gb": round(mem.get("MemTotal", 0) / (1024**3), 1),
        "mem_available_gb": round(mem.get("MemAvailable", 0) / (1024**3), 1),
        "containers": containers,
        "disk_total_gb": round(disk.get("total", 0) / (1024**3), 1),
        "disk_available_gb": round(disk.get("available", 0) / (1024**3), 1),
    }


# --- Run discovery ---

def discover_runs():
    """Find all eval runs (active in /tmp, manifests in runs dir)."""
    runs = {}

    # Manifests from run_eval.py
    if os.path.isdir(RUNS_DIR):
        for name in os.listdir(RUNS_DIR):
            manifest_path = os.path.join(RUNS_DIR, name, "manifest.json")
            if os.path.isfile(manifest_path):
                try:
                    with open(manifest_path) as f:
                        manifest = json.load(f)
                except Exception:
                    manifest = {}

                job_dir = os.path.join(JOBS_ROOT, name, name)
                active = os.path.isdir(job_dir)

                runs[name] = {
                    "job_name": name,
                    "manifest": manifest,
                    "active": active,
                    "job_dir": job_dir if active else None,
                }

    # Also scan /tmp for jobs without manifests
    try:
        for name in os.listdir(JOBS_ROOT):
            if name in runs:
                continue
            job_dir = os.path.join(JOBS_ROOT, name, name)
            if not os.path.isdir(job_dir):
                continue
            has_tasks = any(
                "__" in e
                for e in os.listdir(job_dir)
                if os.path.isdir(os.path.join(job_dir, e))
            )
            if has_tasks:
                runs[name] = {
                    "job_name": name,
                    "manifest": {},
                    "active": True,
                    "job_dir": job_dir,
                }
    except Exception:
        pass

    return runs


# --- Task status ---

def get_tasks(job_dir):
    """Per-task status for a job."""
    tasks = []
    if not job_dir or not os.path.isdir(job_dir):
        return tasks

    for entry in sorted(os.listdir(job_dir)):
        task_path = os.path.join(job_dir, entry)
        if not os.path.isdir(task_path) or "__" not in entry:
            continue

        parts = entry.rsplit("__", 1)
        task_name = parts[0]
        task_hash = parts[1] if len(parts) > 1 else ""

        # Reward — harbor puts it at verifier/reward.txt
        reward = None
        for rpath in [
            os.path.join(task_path, "verifier", "reward.txt"),
            os.path.join(task_path, "reward.txt"),
        ]:
            if os.path.isfile(rpath):
                try:
                    reward = float(open(rpath).read().strip())
                    break
                except Exception:
                    pass

        # Fallback: result.json verifier_result
        if reward is None:
            result_json = os.path.join(task_path, "result.json")
            if os.path.isfile(result_json):
                try:
                    with open(result_json) as f:
                        rdata = json.load(f)
                    vr = rdata.get("verifier_result", {})
                    rewards = vr.get("rewards", {})
                    if "reward" in rewards:
                        reward = float(rewards["reward"])
                except Exception:
                    pass

        # Status
        if reward is not None:
            status = "pass" if reward > 0 else "fail"
        else:
            status = "running"

        # Session and API call counts
        state_dir = os.path.join(task_path, "agent", "serf-state")
        sessions_dir = os.path.join(state_dir, "sessions")

        session_count = 0
        if os.path.isdir(sessions_dir):
            session_count = len(
                glob.glob(os.path.join(sessions_dir, "*.transcript.jsonl"))
            )

        api_calls = 0
        total_tokens = 0
        api_log = os.path.join(state_dir, "api.jsonl")
        if os.path.isfile(api_log):
            try:
                with open(api_log) as f:
                    for line in f:
                        api_calls += 1
                        try:
                            entry_data = json.loads(line)
                            resp = entry_data.get("response", {})
                            usage = resp.get("usage", {})
                            total_tokens += usage.get("total_tokens", 0)
                        except Exception:
                            pass
            except Exception:
                pass

        # Result metadata (for error/timeout detection)
        result_json = os.path.join(task_path, "result.json")
        error_info = None
        if os.path.isfile(result_json):
            try:
                with open(result_json) as f:
                    rdata = json.load(f)
                exc = rdata.get("exception")
                if exc:
                    error_info = exc[:200]
                    if "Timeout" in exc:
                        status = "timeout"
                    elif reward is None:
                        status = "error"
            except Exception:
                pass

        tasks.append({
            "dir_name": entry,
            "name": task_name,
            "hash": task_hash,
            "reward": reward,
            "status": status,
            "sessions": session_count,
            "api_calls": api_calls,
            "total_tokens": total_tokens,
            "error": error_info,
        })

    return tasks


# --- Transcript ---

def get_transcript(task_path):
    """All session transcripts for a task."""
    state_dir = os.path.join(task_path, "agent", "serf-state")
    sessions_dir = os.path.join(state_dir, "sessions")

    if not os.path.isdir(sessions_dir):
        return {"sessions": []}

    sessions = []
    for tfile in sorted(
        glob.glob(os.path.join(sessions_dir, "*.transcript.jsonl"))
    ):
        session = {"file": os.path.basename(tfile), "header": None, "entries": []}
        try:
            with open(tfile) as f:
                for line in f:
                    line = line.strip()
                    if not line:
                        continue
                    try:
                        obj = json.loads(line)
                        if obj.get("kind") == "header":
                            session["header"] = obj
                        elif obj.get("kind") == "entry":
                            # Truncate very long tool results to keep payload manageable
                            turn = obj.get("turn", {})
                            msg = turn.get("message", {})
                            for part in msg.get("content", []):
                                if part.get("kind") == "tool_result":
                                    tr = part.get("tool_result", {})
                                    content = tr.get("content", "")
                                    if isinstance(content, str) and len(content) > 8000:
                                        tr["content"] = (
                                            content[:4000]
                                            + f"\n\n... [{len(content) - 8000} chars truncated] ...\n\n"
                                            + content[-4000:]
                                        )
                                elif part.get("kind") == "text":
                                    text = part.get("text", "")
                                    if isinstance(text, str) and len(text) > 8000:
                                        part["text"] = (
                                            text[:4000]
                                            + f"\n\n... [{len(text) - 8000} chars truncated] ...\n\n"
                                            + text[-4000:]
                                        )
                            session["entries"].append(obj)
                    except json.JSONDecodeError:
                        continue
        except Exception:
            pass
        sessions.append(session)

    return {"sessions": sessions}


# --- API log ---

def get_apilog(task_path):
    """API call log entries for a task."""
    api_log = os.path.join(task_path, "agent", "serf-state", "api.jsonl")
    entries = []
    if not os.path.isfile(api_log):
        return entries

    try:
        with open(api_log) as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    entry = json.loads(line)
                    # Strip the bulky raw response for the list view
                    # (full raw available via separate endpoint if needed)
                    resp = entry.get("response")
                    if resp and "raw" in resp:
                        raw = resp["raw"]
                        resp["raw_keys"] = list(raw.keys()) if isinstance(raw, dict) else []
                        resp["raw_size"] = len(json.dumps(raw))
                        del resp["raw"]
                    entries.append(entry)
                except json.JSONDecodeError:
                    continue
    except Exception:
        pass
    return entries


# --- Verifier output ---

def get_verifier_output(task_path):
    """Verifier test output."""
    for subpath in [
        "verifier/test-stdout.txt",
        "verifier/stdout.txt",
        "verifier/test-output.txt",
    ]:
        path = os.path.join(task_path, subpath)
        if os.path.isfile(path):
            try:
                content = open(path).read()
                if len(content) > 50000:
                    content = content[:25000] + "\n\n... truncated ...\n\n" + content[-25000:]
                return content
            except Exception:
                pass
    return None


# --- Agent stdout ---

def get_agent_stdout(task_path):
    """Agent stdout/stderr log."""
    for subpath in [
        "agent/stdout.txt",
        "agent/serf-state/session.log.jsonl",
    ]:
        path = os.path.join(task_path, subpath)
        if os.path.isfile(path):
            try:
                content = open(path).read()
                if len(content) > 50000:
                    content = content[-50000:]
                return content
            except Exception:
                pass
    return None


# --- Harbor log ---

def get_harbor_log(job_name, lines=500):
    """Tail of harbor log."""
    log_path = os.path.join(JOBS_ROOT, f"{job_name}.log")
    if not os.path.isfile(log_path):
        return ""
    try:
        result = subprocess.run(
            ["tail", f"-{lines}", log_path],
            capture_output=True, text=True, timeout=5,
        )
        return result.stdout
    except Exception:
        return ""


# --- HTTP Handler ---

class DashboardHandler(http.server.BaseHTTPRequestHandler):
    def log_message(self, format, *args):
        pass  # suppress request logging

    def do_GET(self):
        path = urlparse(self.path).path.rstrip("/") or "/"

        if path == "/":
            self.serve_file("eval_dashboard.html", "text/html")
        elif path == "/api/system":
            self.json_response(get_system_info())
        elif path == "/api/runs":
            runs = discover_runs()
            # Attach summary status to each run
            for run in runs.values():
                if run.get("job_dir"):
                    tasks = get_tasks(run["job_dir"])
                    run["summary"] = {
                        "total": len(tasks),
                        "pass": sum(1 for t in tasks if t["status"] == "pass"),
                        "fail": sum(1 for t in tasks if t["status"] == "fail"),
                        "running": sum(1 for t in tasks if t["status"] == "running"),
                        "timeout": sum(1 for t in tasks if t["status"] == "timeout"),
                        "error": sum(1 for t in tasks if t["status"] == "error"),
                    }
                else:
                    run["summary"] = None
            self.json_response(list(runs.values()))
        elif path.startswith("/api/runs/"):
            self.route_run(path)
        else:
            self.send_error(404)

    def route_run(self, path):
        parts = path.split("/")
        if len(parts) < 4:
            self.send_error(404)
            return

        job_name = parts[3]
        runs = discover_runs()
        if job_name not in runs:
            self.send_error(404, f"Run not found: {job_name}")
            return

        run = runs[job_name]
        job_dir = run.get("job_dir")
        rest = parts[4:]

        if not rest:
            self.json_response(run)

        elif rest[0] == "manifest":
            self.json_response(run.get("manifest", {}))

        elif rest[0] == "tasks" and len(rest) == 1:
            self.json_response(get_tasks(job_dir))

        elif rest[0] == "tasks" and len(rest) >= 3:
            dir_name = rest[1]
            action = rest[2]
            task_path = os.path.join(job_dir, dir_name) if job_dir else ""

            if not os.path.isdir(task_path):
                self.send_error(404, f"Task not found: {dir_name}")
                return

            if action == "transcript":
                self.json_response(get_transcript(task_path))
            elif action == "apilog":
                self.json_response(get_apilog(task_path))
            elif action == "verifier":
                self.json_response({"output": get_verifier_output(task_path)})
            elif action == "agent-log":
                self.json_response({"output": get_agent_stdout(task_path)})
            else:
                self.send_error(404)

        elif rest[0] == "log":
            qs = parse_qs(urlparse(self.path).query)
            lines = int(qs.get("lines", ["500"])[0])
            self.json_response({"log": get_harbor_log(job_name, lines)})

        else:
            self.send_error(404)

    def serve_file(self, filename, content_type):
        script_dir = os.path.dirname(os.path.abspath(__file__))
        filepath = os.path.join(script_dir, filename)
        if not os.path.isfile(filepath):
            self.send_error(404, f"Not found: {filename}")
            return
        with open(filepath, "rb") as f:
            content = f.read()
        self.send_response(200)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", len(content))
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(content)

    def json_response(self, data):
        body = json.dumps(data, default=str).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", len(body))
        self.send_header("Access-Control-Allow-Origin", "*")
        self.send_header("Cache-Control", "no-cache")
        self.end_headers()
        self.wfile.write(body)


def main():
    parser = argparse.ArgumentParser(description="Serf Eval Dashboard")
    parser.add_argument("--port", type=int, default=8080, help="Port to serve on")
    parser.add_argument("--jobs-root", default="/tmp", help="Root directory for job data")
    args = parser.parse_args()

    global JOBS_ROOT
    JOBS_ROOT = args.jobs_root

    server = http.server.HTTPServer(("0.0.0.0", args.port), DashboardHandler)
    print(f"Serf Eval Dashboard: http://0.0.0.0:{args.port}/")
    print(f"  Jobs root: {JOBS_ROOT}")
    print(f"  Runs dir:  {RUNS_DIR}")
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        print("\nShutdown.")
        server.server_close()


if __name__ == "__main__":
    main()
