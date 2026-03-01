"""Eval dashboard server.

Markdown by default. Send Accept: application/json for JSON.
"""

import os
from fastapi import FastAPI, Request
from fastapi.responses import PlainTextResponse, JSONResponse
from fastapi.staticfiles import StaticFiles

from data import RunStore
from trajectory import build_trajectory
from markdown_render import render_run_list, render_run_detail, render_task_detail

app = FastAPI(title="Serf Eval Dashboard")

# Configure data dir from env or default.
_data_dir = os.environ.get("DASHBOARD_DATA_DIR", "/data/serf-evals/runs")
store = RunStore(_data_dir)


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
    tasks = store.list_tasks(job_name)
    if tasks is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\nRun `{job_name}` not found.\n")
    if _wants_json(request):
        return JSONResponse(tasks)
    return _md_response(render_run_detail(store.get_run(job_name), tasks))


@app.get("/api/runs/{job_name}/tasks/{task_name}")
def get_task(job_name: str, task_name: str, request: Request):
    task = store.get_task(job_name, task_name)
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

    if _wants_json(request):
        task["trajectory"] = trajectories
        task.pop("transcript_files", None)
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
        import sys
        sys.modules[__name__].store = RunStore(args.data_dir)

    uvicorn.run(app, host=args.host, port=args.port)
