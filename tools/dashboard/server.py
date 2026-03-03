"""Eval dashboard server.

Markdown by default. Send Accept: application/json for JSON.
"""

import html as html_mod
import json
import os
from pathlib import Path

from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, JSONResponse, HTMLResponse
from fastapi.staticfiles import StaticFiles

from data import RunStore
from stats import compute_run_stats, compute_task_stats, compute_task_history
from trajectory import build_trajectory
from markdown_render import render_run_list, render_run_detail, render_task_detail

app = FastAPI(title="Serf Eval Dashboard")

# Configure data dir from env or default.
_data_dir = os.environ.get("DASHBOARD_DATA_DIR", "/data/serf-evals/runs")
store = RunStore(_data_dir)
_cache_dir = os.path.join(_data_dir, ".cache")


def _wants_json(request: Request) -> bool:
    accept = request.headers.get("accept", "")
    return "application/json" in accept


def _md_response(content: str) -> PlainTextResponse:
    return PlainTextResponse(content, media_type="text/markdown; charset=utf-8")


@app.get("/health")
def health():
    return {"status": "ok"}


@app.get("/api/runs")
def list_runs(request: Request):
    runs = store.list_runs()
    if _wants_json(request):
        return JSONResponse(runs)
    return _md_response(render_run_list(runs))


@app.get("/api/runs/{job_name}")
def get_run(job_name: str, request: Request):
    run = store.get_run(job_name)
    if run is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\nRun `{job_name}` not found.\n")
    tasks = store.list_tasks(job_name)
    if _wants_json(request):
        return JSONResponse(run)
    return _md_response(render_run_detail(run, tasks))


@app.get("/api/runs/{job_name}/tasks")
def list_tasks(job_name: str, request: Request):
    run_stats = compute_run_stats(store, job_name, cache_dir=_cache_dir)
    if run_stats is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\nRun `{job_name}` not found.\n")
    if _wants_json(request):
        return JSONResponse(run_stats["tasks"])
    return _md_response(render_run_detail(store.get_run(job_name),
                                          store.list_tasks(job_name)))


@app.get("/api/runs/{job_name}/tasks/{task_name}")
def get_task(job_name: str, task_name: str, request: Request, trial: str = None):
    task = store.get_task(job_name, task_name, trial_hash=trial)
    if task is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\n`{task_name}` not found in `{job_name}`.\n")

    # Build trajectory from transcripts
    sessions = store.load_transcripts(task.get("transcript_files", []))
    tree = store.build_session_tree(sessions)

    trajectories = []
    for root_session in tree:
        trajectories.append({
            "session_id": root_session["session_id"],
            "model": root_session["model"],
            "depth": root_session["depth"],
            "trajectory": build_trajectory(root_session),
            "children": [
                {
                    "session_id": child["session_id"],
                    "parent_tool_call_id": child.get("parent_tool_call_id", ""),
                    "depth": child["depth"],
                    "model": child.get("model", ""),
                    "trajectory": build_trajectory(child),
                }
                for child in root_session.get("children", [])
            ],
        })

    # Extract system_prompt from the root (depth 0) session.
    system_prompt = ""
    for s in sessions:
        if s.get("depth", 0) == 0 and s.get("system_prompt"):
            system_prompt = s["system_prompt"]
            break
    task["system_prompt"] = system_prompt

    # Check for ATIF trajectory (used by non-serf agents like lace)
    task_dir = task.get("task_dir", "")
    if task_dir:
        atif_path = Path(task_dir) / "agent" / "trajectory.json"
        if atif_path.is_file():
            try:
                task["atif_trajectory"] = json.loads(atif_path.read_text())
            except (json.JSONDecodeError, OSError):
                pass

    if _wants_json(request):
        task["trajectory"] = trajectories
        task.pop("transcript_files", None)

        # Merge per-task stats (exclude action_sequence to avoid duplicating
        # trajectory data already present on the response).
        task_stats = compute_task_stats(store, job_name, task_name, trial_hash=trial)
        if task_stats:
            task.update({k: v for k, v in task_stats.items()
                         if k not in ("action_sequence",)})

        # Build raw file links (paths relative to data_dir for /raw/ endpoint)
        task_dir = task.get("task_dir", "")
        if task_dir:
            task_path = Path(task_dir)
            data_path = Path(store.data_dir).resolve()
            try:
                rel = task_path.resolve().relative_to(data_path)
                raw_files = {}
                for fname, key in [("result.json", "result"),
                                   ("config.json", "config")]:
                    if (task_path / fname).is_file():
                        raw_files[key] = str(rel / fname)
                stdout_file = task_path / "agent" / "command-0" / "stdout.txt"
                if stdout_file.is_file():
                    raw_files["stdout"] = str(rel / "agent" / "command-0" / "stdout.txt")
                cmd_file = task_path / "agent" / "command-0" / "command.txt"
                if cmd_file.is_file():
                    raw_files["command"] = str(rel / "agent" / "command-0" / "command.txt")
                test_stdout = task_path / "verifier" / "test-stdout.txt"
                if test_stdout.is_file():
                    raw_files["verifier"] = str(rel / "verifier" / "test-stdout.txt")
                state_dir = task_path / "agent" / "serf-state"
                if state_dir.is_dir():
                    for f in sorted(state_dir.glob("sessions/*.transcript.jsonl")):
                        raw_files.setdefault("transcripts", []).append(
                            str(rel / f.relative_to(task_path)))
                    api_log = state_dir / "api.jsonl"
                    if api_log.is_file():
                        raw_files["api_log"] = str(
                            rel / "agent" / "serf-state" / "api.jsonl")
                artifacts_dir = task_path / "agent" / "artifacts"
                if artifacts_dir.is_dir():
                    raw_files["artifacts_base"] = str(
                        rel / "agent" / "artifacts")
                task["raw_files"] = raw_files

                # All files in task directory with raw URLs
                all_files = store.list_all_files(task_path)
                for f in all_files:
                    f["raw_url"] = f"/raw/{rel}/{f['path']}"
                task["all_files"] = all_files
            except (ValueError, OSError):
                pass

        return JSONResponse(task)

    main_trajectory = trajectories[0]["trajectory"] if trajectories else []
    return _md_response(render_task_detail(
        task_name=task_name,
        job_name=job_name,
        reward=task.get("reward"),
        failure_category=task.get("failure_category", ""),
        trajectory=main_trajectory,
        verifier_output=task.get("test_output", ""),
    ))


@app.get("/api/runs/{job_name}/tasks/{task_name}/artifacts")
def list_artifacts(job_name: str, task_name: str):
    task = store.get_task(job_name, task_name)
    if task is None:
        return JSONResponse({"error": "not found"}, status_code=404)
    task_path = Path(task["task_dir"])
    artifacts = store.list_artifacts(task_path)
    # Add raw URLs for each file
    data_path = Path(store.data_dir).resolve()
    try:
        rel = task_path.resolve().relative_to(data_path)
        for a in artifacts:
            a["raw_url"] = f"/raw/{rel}/agent/artifacts/{a['path']}"
    except (ValueError, OSError):
        pass
    return JSONResponse(artifacts)


@app.get("/api/compare")
def compare_runs(request: Request, a: str = "", b: str = ""):
    tasks_a = store.list_tasks(a)
    if tasks_a is None:
        return JSONResponse({"error": f"run '{a}' not found"}, status_code=404)
    tasks_b = store.list_tasks(b)
    if tasks_b is None:
        return JSONResponse({"error": f"run '{b}' not found"}, status_code=404)

    map_a = {t["task_name"]: t for t in tasks_a}
    map_b = {t["task_name"]: t for t in tasks_b}
    all_tasks = sorted(set(map_a) | set(map_b))

    improved = []
    regressed = []
    stable_pass = []
    stable_fail = []
    pending = []
    only_a = []
    only_b = []

    def _task_label(t):
        """Map task dict to a compare label: pass/fail/running/queued."""
        status = t.get("status", "")
        if status in ("running", "queued"):
            return status
        return "pass" if t["passed"] else "fail"

    for task_name in all_tasks:
        in_a = task_name in map_a
        in_b = task_name in map_b
        if in_a and not in_b:
            only_a.append({"task": task_name,
                           "a": _task_label(map_a[task_name])})
            continue
        if in_b and not in_a:
            only_b.append({"task": task_name,
                           "b": _task_label(map_b[task_name])})
            continue

        ta, tb = map_a[task_name], map_b[task_name]
        entry = {"task": task_name,
                 "a": _task_label(ta),
                 "b": _task_label(tb)}

        # If either side is still running/queued, comparison is meaningless
        a_final = ta.get("status") in ("pass", "fail")
        b_final = tb.get("status") in ("pass", "fail")
        if not a_final or not b_final:
            pending.append(entry)
            continue

        a_pass = ta["passed"]
        b_pass = tb["passed"]
        if not a_pass and b_pass:
            improved.append(entry)
        elif a_pass and not b_pass:
            regressed.append(entry)
        elif a_pass and b_pass:
            stable_pass.append(entry)
        else:
            stable_fail.append(entry)

    passed_a = sum(1 for t in tasks_a if t["passed"])
    passed_b = sum(1 for t in tasks_b if t["passed"])

    return JSONResponse({
        "run_a": {"job_name": a, "passed": passed_a, "total": len(tasks_a)},
        "run_b": {"job_name": b, "passed": passed_b, "total": len(tasks_b)},
        "improved": improved,
        "regressed": regressed,
        "stable_pass": stable_pass,
        "stable_fail": stable_fail,
        "pending": pending,
        "only_a": only_a,
        "only_b": only_b,
    })


@app.get("/api/tasks/{task_name}/history")
def task_history(task_name: str):
    history = compute_task_history(store, task_name)
    return JSONResponse(history)


@app.get("/raw/{file_path:path}")
def raw_file(file_path: str):
    data_path = Path(store.data_dir).resolve()
    requested = (data_path / file_path).resolve()

    # Reject path traversal
    if not str(requested).startswith(str(data_path) + os.sep) and requested != data_path:
        return JSONResponse({"error": "forbidden"}, status_code=403)

    if not requested.is_file():
        return JSONResponse({"error": "not found"}, status_code=404)

    try:
        content = requested.read_text(errors="replace")
    except OSError:
        return JSONResponse({"error": "read error"}, status_code=500)

    suffix = requested.suffix.lower()
    if suffix == ".json":
        try:
            parsed = json.loads(content)
            escaped = html_mod.escape(json.dumps(parsed, indent=2))
        except (json.JSONDecodeError, ValueError):
            escaped = html_mod.escape(content)
        body = f"<pre>{escaped}</pre>"
    elif suffix == ".jsonl":
        parts = []
        for line in content.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                parsed = json.loads(line)
                escaped = html_mod.escape(json.dumps(parsed, indent=2))
            except (json.JSONDecodeError, ValueError):
                escaped = html_mod.escape(line)
            parts.append(f"<pre>{escaped}</pre>")
        body = "<hr>".join(parts)
    else:
        body = f"<pre>{html_mod.escape(content)}</pre>"

    return HTMLResponse(f"<!DOCTYPE html><html><body>{body}</body></html>")


@app.get("/")
def index():
    static_dir = os.path.join(os.path.dirname(__file__), "static")
    index_path = os.path.join(static_dir, "index.html")
    if os.path.isfile(index_path):
        return PlainTextResponse(open(index_path).read(), media_type="text/html")
    return PlainTextResponse("Dashboard not built yet.", media_type="text/html")


static_dir = os.path.join(os.path.dirname(__file__), "static")
if os.path.isdir(static_dir):
    app.mount("/static", StaticFiles(directory=static_dir), name="static")


if __name__ == "__main__":
    import argparse
    import sys
    import uvicorn

    # Ensure dashboard modules are importable.
    sys.path.insert(0, os.path.dirname(__file__))

    parser = argparse.ArgumentParser(description="Serf Eval Dashboard")
    parser.add_argument("--data-dir", default=None,
                        help="Directory to scan for eval runs")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()

    if args.data_dir:
        _data_dir = args.data_dir
        sys.modules[__name__].store = RunStore(args.data_dir)
        sys.modules[__name__]._cache_dir = os.path.join(args.data_dir, ".cache")

    uvicorn.run(app, host=args.host, port=args.port)
