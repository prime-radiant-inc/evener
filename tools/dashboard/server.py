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
from sse_starlette.sse import EventSourceResponse

import re

from data import RunStore
from experiment_store import ExperimentStore
from harbor_store import HarborStore
from live_store import LiveStore
from s3_client import S3Client
from stats import compute_run_stats, compute_task_stats, compute_task_history
from trajectory import build_trajectory
from markdown_render import render_run_list, render_run_detail, render_task_detail

app = FastAPI(title="Serf Eval Dashboard")

# Configure data dir from env or default.
_data_dir = os.environ.get("DASHBOARD_DATA_DIR", "/data/agent-evals/runs")
store = RunStore(_data_dir)
_cache_dir = os.path.join(_data_dir, ".cache")

# Experiment metadata store (optional).
_experiments_dir = os.environ.get("DASHBOARD_EXPERIMENTS_DIR", "")
experiment_store = ExperimentStore(_experiments_dir) if _experiments_dir else None

# Live run monitoring (optional) — reads harbor-runner state files.
_harbor_state_dir = os.environ.get("DASHBOARD_HARBOR_STATE_DIR", "")
live_store = LiveStore(_harbor_state_dir) if _harbor_state_dir else None
# Live cache dir - prefer persistent storage, updated at startup if available
_live_cache_dir = "/tmp/serf-live-cache"

# Completed harbor runs (optional) — reads state/results/ trees so the
# dashboard can surface runs that haven't been post_run.sh'd yet.
# (s3_client is set below; HarborStore is re-initialized at main
# startup to wire in S3 if a bucket is configured.)
harbor_store = HarborStore(_harbor_state_dir) if _harbor_state_dir else None

# On-demand S3 transcript fetching (optional).
_s3_bucket = os.environ.get("DASHBOARD_S3_BUCKET", "")
_s3_region = os.environ.get("DASHBOARD_S3_REGION", "us-west-1")
s3_client = S3Client(_s3_bucket, region=_s3_region) if _s3_bucket else None

# Match harbor job_name format: {wave}_rep{N}. Captures (wave, rep_int).
_JOB_WAVE_REP_RE = re.compile(r"^(.+)_rep(\d+)$")


def _sync_task_from_s3(
    job_name: str, task_name: str, rep_override: int | None = None,
) -> str | None:
    """Fetch a task's files from S3 into the data dir.

    Resolves (wave, rep) from job_name:
      - {wave}_rep{N}: use that rep (ignores rep_override)
      - {wave}: bare wave name — use rep_override if given, else rep 1

    Returns the effective job_name to use for the retry lookup (which
    always has the _rep{N} suffix), or None if sync failed or S3 is
    not configured.
    """
    if s3_client is None:
        return None
    m = _JOB_WAVE_REP_RE.match(job_name)
    if m:
        wave = m.group(1)
        rep = int(m.group(2))
    else:
        # Bare wave name — use rep_override if given, else default to 1
        wave = job_name
        rep = rep_override if rep_override is not None else 1
    result = s3_client.sync_task(
        wave=wave, rep=rep, task_name=task_name,
        cache_base=Path(store.data_dir),
    )
    if result is None:
        return None
    return f"{wave}_rep{rep}"


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
def get_task(
    job_name: str, task_name: str, request: Request,
    trial: str = None, rep: int = None,
):
    # When a rep is explicitly requested for a bare wave name, look up
    # the _rep{N} job directly (it may already be cached).
    lookup_job = job_name
    if rep is not None and not _JOB_WAVE_REP_RE.match(job_name):
        lookup_job = f"{job_name}_rep{rep}"

    task = store.get_task(lookup_job, task_name, trial_hash=trial)
    if task is None:
        # Try fetching from S3 on cache miss. Sync syncs into
        # {wave}_rep{N}/, so retry with that job_name.
        effective_job = _sync_task_from_s3(
            job_name, task_name, rep_override=rep,
        )
        if effective_job is not None:
            task = store.get_task(effective_job, task_name, trial_hash=trial)
    if task is None:
        if _wants_json(request):
            return JSONResponse({"error": "not found"}, status_code=404)
        return _md_response(f"# Not Found\n\n`{task_name}` not found in `{job_name}`.\n")

    # Build trajectory from transcripts
    sessions = store.load_transcripts(task.get("transcript_files", []))
    tree = store.build_session_tree(sessions)

    def _initial_prompt(session):
        """First USER_INPUT turn's text, or empty string."""
        for entry in session.get("entries", []):
            if entry.get("kind") != "entry":
                continue
            turn = entry.get("turn", {})
            if turn.get("kind") != "USER_INPUT":
                continue
            for part in turn.get("message", {}).get("content", []):
                if part.get("kind") == "text" and part.get("text"):
                    return part["text"]
        return ""

    # Regex for STEERING task headers:
    # [Task #1: Inventory | reasoning: low]
    _STEERING_RE = re.compile(
        r"^\[Task #(\d+):\s*([^\|\]]+?)(?:\s*\|\s*reasoning:\s*(\w+))?\s*\]\n?"
    )

    def _initial_task_list(session):
        """Extract tasks from STEERING messages seen in the session.

        Serf's coordinator injects each of its built-in tasks as a
        STEERING turn whose text opens with
            [Task #N: <title> | reasoning: <effort>]
            <prompt text...>
        Parsing these gives us id/description/prompt/reasoning_effort
        for every task in the agent's frontmatter, which is otherwise
        invisible in the tool_call/tool_result stream.

        Returns a list of dicts (deduped by id, first occurrence wins).
        """
        tasks = {}
        for entry in session.get("entries", []):
            if entry.get("kind") != "entry":
                continue
            turn = entry.get("turn", {})
            if turn.get("kind") != "STEERING":
                continue
            for part in turn.get("message", {}).get("content", []):
                if part.get("kind") != "text":
                    continue
                text = part.get("text", "") or ""
                m = _STEERING_RE.match(text)
                if not m:
                    continue
                task_id = int(m.group(1))
                if task_id in tasks:
                    continue
                title = m.group(2).strip()
                effort = (m.group(3) or "").strip()
                # Everything after the header line is the prompt.
                prompt = text[m.end():].strip()
                # Strip trailing continuation guidance the coordinator
                # appends (varies per task) so the prompt stays focused
                # on the task itself.
                for sep in ("\n\nWhen you have completed this task",
                            "\n\nYou responded with bare text"):
                    idx = prompt.find(sep)
                    if idx >= 0:
                        prompt = prompt[:idx].strip()
                entry = {
                    "id": task_id,
                    "description": title,
                    "prompt": prompt,
                }
                if effort:
                    entry["reasoning_effort"] = effort
                tasks[task_id] = entry
        if not tasks:
            return []
        return [tasks[k] for k in sorted(tasks)]

    trajectories = []
    for root_session in tree:
        trajectories.append({
            "session_id": root_session["session_id"],
            "model": root_session["model"],
            "depth": root_session["depth"],
            "system_prompt": root_session.get("system_prompt", ""),
            "initial_prompt": _initial_prompt(root_session),
            "initial_task_list": _initial_task_list(root_session),
            "trajectory": build_trajectory(root_session),
            "children": [
                {
                    "session_id": child["session_id"],
                    "parent_tool_call_id": child.get("parent_tool_call_id", ""),
                    "depth": child["depth"],
                    "model": child.get("model", ""),
                    "system_prompt": child.get("system_prompt", ""),
                    "initial_prompt": _initial_prompt(child),
                    "initial_task_list": _initial_task_list(child),
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


# --- Experiment metadata routes -------------------------------------------

def _enrich_run(run: dict) -> dict:
    """Add task_count / mean_score / perfect_count computed fields."""
    enriched = dict(run)
    scores = [t["score"] for t in run.get("results", {}).values()]
    enriched["task_count"] = len(scores)
    enriched["mean_score"] = sum(scores) / len(scores) if scores else 0.0
    enriched["perfect_count"] = sum(1 for s in scores if s == 1.0)
    # Preserve needs_s3_fetch flag for frontend progressive loading
    if run.get("needs_s3_fetch"):
        enriched["needs_s3_fetch"] = True
    return enriched


def _merged_experiments(run_type: str = None) -> list:
    """Experiment store + harbor state, deduped by run_id.

    Committed runs (experiment_store) win on conflict — they're the
    post-processed, canonical versions.
    """
    runs = {}
    if harbor_store is not None:
        for r in harbor_store.list_runs():
            runs[r["run_id"]] = _enrich_run({**r, "source": "harbor"})
    if experiment_store is not None:
        for r in experiment_store.list_experiments(run_type=None):
            r = dict(r)
            r.setdefault("source", "committed")
            runs[r["run_id"]] = r
    # Apply type filter
    result = list(runs.values())
    if run_type == "wave":
        result = [r for r in result if r["run_id"].startswith("wave-")]
    elif run_type == "experiment":
        result = [r for r in result if not r["run_id"].startswith("wave-")]
    # Sort by full timestamp from run_id, falling back to date
    def sort_key(r):
        run_id = r.get("run_id", "")
        m = re.search(r"(\d{8})[-_](\d{4,6})$", run_id)
        if m:
            time = m.group(2).ljust(6, '0')  # Pad HHMM to HHMM00
            return f"{m.group(1)}-{time}"
        date = r.get("date", "")
        return date.replace("-", "") + "-000000" if date else "00000000-000000"
    result.sort(key=sort_key, reverse=True)
    return result


@app.get("/api/experiments")
def list_experiments(type: str = None):
    if experiment_store is None and harbor_store is None:
        return JSONResponse({"error": "experiment store not configured"},
                            status_code=501)
    return JSONResponse(_merged_experiments(run_type=type))


@app.get("/api/experiments/scores")
def batch_scores(ids: str = ""):
    """Fetch scores for multiple runs in one request.

    Query param `ids` is a comma-separated list of run_ids.
    Returns {run_id: {task_count, mean_score, perfect_count}} for each.
    Uses fast method (reward.txt only, parallel) for S3 runs.
    """
    if harbor_store is None:
        return JSONResponse({"error": "harbor store not configured"},
                            status_code=501)
    run_ids = [x.strip() for x in ids.split(",") if x.strip()]
    results = {}
    for run_id in run_ids:
        scores = harbor_store.get_run_scores_fast(run_id)
        if scores is not None:
            results[run_id] = scores
    return JSONResponse(results)


@app.post("/api/experiments/sync")
def sync_runs(ids: str = ""):
    """Sync full run data from S3 to local cache.

    Query param `ids` is a comma-separated list of run_ids.
    Returns {run_id: success_bool} for each.
    """
    if harbor_store is None:
        return JSONResponse({"error": "harbor store not configured"},
                            status_code=501)
    if harbor_store.sync_cache_dir is None:
        return JSONResponse({"error": "sync cache not configured"},
                            status_code=501)

    run_ids = [x.strip() for x in ids.split(",") if x.strip()]
    results = {}
    for run_id in run_ids:
        success = harbor_store.sync_run(run_id)
        results[run_id] = success
    return JSONResponse(results)


@app.get("/api/experiments/tasks")
def list_experiment_tasks(model: str = None, harness: str = None):
    """Return all tasks with their performance summaries across runs.

    Optional filters:
    - model: filter to runs with this model
    - harness: filter to runs with this harness
    """
    runs = _merged_experiments()

    # Collect available filter values
    all_models = set()
    all_harnesses = set()

    # Aggregate per-task stats across all runs
    task_stats = {}  # task_name → {runs: [...], pass_count, total_count}
    for run in runs:
        run_model = run.get("model", "unknown")
        run_harness = run.get("harness", "serf")
        all_models.add(run_model)
        all_harnesses.add(run_harness)

        # Apply filters
        if model and run_model != model:
            continue
        if harness and run_harness != harness:
            continue

        results = run.get("results", {})
        for task_name, task_data in results.items():
            if task_name not in task_stats:
                task_stats[task_name] = {"runs": [], "pass_count": 0, "total_count": 0}
            stats = task_stats[task_name]
            score = task_data.get("score", 0)
            stats["runs"].append({
                "run_id": run["run_id"],
                "date": run.get("date", ""),
                "git_sha": run.get("git_sha", ""),
                "model": run_model,
                "harness": run_harness,
                "score": score,
            })
            stats["total_count"] += 1
            if score >= 1.0:
                stats["pass_count"] += 1

    # Build response with summary stats
    tasks = []
    for task_name, stats in sorted(task_stats.items()):
        # Sort runs by date descending
        runs_sorted = sorted(stats["runs"], key=lambda r: r["date"], reverse=True)
        # Recent scores for sparkline (last 20 runs)
        recent_scores = [r["score"] for r in runs_sorted[:20]]
        tasks.append({
            "task": task_name,
            "run_count": stats["total_count"],
            "pass_count": stats["pass_count"],
            "pass_rate": stats["pass_count"] / stats["total_count"] if stats["total_count"] > 0 else 0,
            "latest_score": runs_sorted[0]["score"] if runs_sorted else 0,
            "latest_date": runs_sorted[0]["date"] if runs_sorted else "",
            "recent_scores": recent_scores,
        })

    return JSONResponse({
        "tasks": tasks,
        "filters": {
            "models": sorted(all_models),
            "harnesses": sorted(all_harnesses),
        },
    })


@app.get("/api/experiments/tasks/{task_name}/history")
def experiment_task_history(task_name: str, model: str = None, harness: str = None):
    """Return history of a specific task across all runs (merged sources)."""
    runs = _merged_experiments()

    history = []
    for run in runs:
        # Apply filters
        if model and run.get("model", "unknown") != model:
            continue
        if harness and run.get("harness", "serf") != harness:
            continue

        results = run.get("results", {})
        if task_name not in results:
            continue

        task_data = results[task_name]
        history.append({
            "run_id": run["run_id"],
            "date": run.get("date", ""),
            "git_sha": run.get("git_sha", ""),
            "model": run.get("model", "unknown"),
            "harness": run.get("harness", "serf"),
            "score": task_data.get("score", 0),
            "reps": task_data.get("reps", []),
        })

    # Sort by date descending
    history.sort(key=lambda r: r["date"], reverse=True)
    return JSONResponse(history)


@app.get("/api/experiments/{run_id}")
def get_experiment(run_id: str):
    if experiment_store is None and harbor_store is None:
        return JSONResponse({"error": "experiment store not configured"},
                            status_code=501)
    # Committed runs win over harbor-synthesized ones
    run = None
    if experiment_store is not None:
        run = experiment_store.get_experiment(run_id)
    if run is None and harbor_store is not None:
        run = harbor_store.get_run(run_id)
    if run is None:
        return JSONResponse({"error": "not found"}, status_code=404)
    return JSONResponse(run)


@app.get("/api/scoreboard")
def get_scoreboard(filter: str = None):
    if experiment_store is None:
        return JSONResponse({"error": "experiment store not configured"},
                            status_code=501)
    return JSONResponse(experiment_store.get_scoreboard(filter=filter))


# --- Live run routes ------------------------------------------------------

@app.get("/api/live/runs")
def list_live_runs():
    if live_store is None:
        return JSONResponse({"error": "live store not configured"},
                            status_code=501)
    return JSONResponse(live_store.list_runs())


@app.get("/api/live/runs/{run_id}")
def get_live_run(run_id: str):
    if live_store is None:
        return JSONResponse({"error": "live store not configured"},
                            status_code=501)
    enriched = live_store.run_with_live_state(run_id)
    if enriched is None:
        return JSONResponse({"error": "not found"}, status_code=404)
    os.makedirs(_live_cache_dir, exist_ok=True)
    enriched = live_store.enrich_with_results(enriched, _live_cache_dir)
    return JSONResponse(enriched)


@app.get("/api/live/runs/{run_id}/stream")
async def stream_live_run(run_id: str):
    if live_store is None:
        return JSONResponse({"error": "live store not configured"},
                            status_code=501)

    import asyncio

    async def event_generator():
        os.makedirs(_live_cache_dir, exist_ok=True)
        while True:
            enriched = live_store.run_with_live_state(run_id)
            if enriched is None:
                yield {
                    "event": "error",
                    "data": json.dumps({"error": "run not found"}),
                }
                return
            enriched = live_store.enrich_with_results(
                enriched, _live_cache_dir,
            )
            yield {
                "event": "state_update",
                "data": json.dumps(enriched),
            }
            await asyncio.sleep(15)

    return EventSourceResponse(event_generator())


# --- Raw file serving -----------------------------------------------------

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


@app.middleware("http")
async def _no_cache_for_static(request: Request, call_next):
    """Disable browser cache on /static/ so JS module edits take effect
    without hard-reloads. Small SPA — the bandwidth cost is negligible
    and the dev iteration cost of stale modules is high."""
    response = await call_next(request)
    if request.url.path.startswith("/static/"):
        response.headers["Cache-Control"] = "no-cache, must-revalidate"
    return response


if __name__ == "__main__":
    import argparse
    import sys
    import uvicorn

    # Ensure dashboard modules are importable.
    sys.path.insert(0, os.path.dirname(__file__))

    parser = argparse.ArgumentParser(description="Serf Eval Dashboard")
    parser.add_argument("--data-dir", default=None,
                        help="Directory to scan for eval runs")
    parser.add_argument("--experiments-dir", default=None,
                        help="Directory with experiment metadata (runs/, tasks/, scoreboard.json)")
    parser.add_argument("--harbor-state-dir", default=None,
                        help="Harbor-runner state directory with run .env files")
    parser.add_argument("--s3-bucket", default=None,
                        help="S3 bucket for on-demand transcript fetching "
                             "(default: harbor-eval-results-526275945504)")
    parser.add_argument("--s3-region", default=None,
                        help="AWS region for the S3 bucket (default: us-west-1)")
    parser.add_argument("--sync-cache-dir", default=None,
                        help="Persistent directory for synced S3 run data")
    parser.add_argument("--host", default="0.0.0.0")
    parser.add_argument("--port", type=int, default=8080)
    args = parser.parse_args()

    if args.data_dir:
        _data_dir = args.data_dir
        sys.modules[__name__].store = RunStore(args.data_dir)
        sys.modules[__name__]._cache_dir = os.path.join(args.data_dir, ".cache")

    # Repo root for auto-detection of directories.
    repo_root = Path(__file__).resolve().parent.parent.parent

    # Resolve experiments directory: explicit flag > env var > auto-detect.
    exp_dir = args.experiments_dir
    if not exp_dir:
        candidate = repo_root / "docs" / "experiments"
        if candidate.is_dir():
            exp_dir = str(candidate)
    if exp_dir:
        sys.modules[__name__].experiment_store = ExperimentStore(exp_dir)

    # Resolve harbor state directory: explicit flag > env var > auto-detect.
    harbor_state_dir = args.harbor_state_dir or _harbor_state_dir
    if not harbor_state_dir:
        candidate = repo_root.parent / "harbor-runner" / "state" / "runs"
        if candidate.is_dir():
            harbor_state_dir = str(candidate)
    # Resolve S3 bucket first so HarborStore can use it.
    s3_bucket = (args.s3_bucket or _s3_bucket
                 or "harbor-eval-results-526275945504")
    s3_region = args.s3_region or _s3_region
    if s3_bucket:
        sys.modules[__name__].s3_client = S3Client(s3_bucket, region=s3_region)

    # Resolve sync cache directory
    sync_cache_dir = args.sync_cache_dir
    if not sync_cache_dir:
        # Default to persistent storage if available
        persistent = Path("/Volumes/Local Archives/serf-s3-cache")
        if persistent.is_dir():
            sync_cache_dir = str(persistent)

    # Live cache dir - use sync cache parent or /tmp fallback
    if sync_cache_dir:
        live_cache = Path(sync_cache_dir).parent / "serf-live-cache"
    else:
        live_cache = Path("/tmp/serf-live-cache")
    live_cache.mkdir(parents=True, exist_ok=True)
    sys.modules[__name__]._live_cache_dir = str(live_cache)

    if harbor_state_dir:
        sys.modules[__name__].live_store = LiveStore(harbor_state_dir)
        sys.modules[__name__].harbor_store = HarborStore(
            harbor_state_dir,
            s3_client=sys.modules[__name__].s3_client,
            sync_cache_dir=sync_cache_dir,
        )

    uvicorn.run(app, host=args.host, port=args.port)
