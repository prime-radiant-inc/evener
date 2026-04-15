# Experiment Dashboard Design

Extend `tools/dashboard/` into a browser-based experiment explorer with live monitoring, transcript drill-down, and run comparison.

## Decisions

- **Approach:** Experiment-first navigation (git-tracked JSON as top-level, harbor dirs for detail)
- **Deployment:** Local dev machine with SSH proxy to EC2 for live monitoring
- **Frontend:** Preact via CDN (htm tagged templates, no build step), reuse existing CSS
- **Live data:** S3 polling for scores + optional SSH tunnel to EC2 for container health
- **Priority:** Comparison and live monitoring are most important; all use cases matter

## Data Architecture

Three stores, one API layer:

### ExperimentStore (new: `experiment_store.py`)

Reads git-tracked metadata:
- `docs/experiments/runs/*.json` — 134+ run files with scores per task
- `docs/experiments/tasks/*.json` — per-task history scorecards
- `docs/experiments/scoreboard.json` — 89-task x runs matrix

Loaded into memory at startup. Filesystem watcher (`watchfiles`) triggers reload when files change (the researcher commits new results while the dashboard is running).

Provides:
- `list_experiments(filters)` — all runs, sortable by date/score/model
- `get_experiment(run_id)` — single run with full task scores
- `get_scoreboard()` — full 89-task matrix
- `get_task_history(task_name)` — score over time for one task

### RunStore (existing: `data.py`, unchanged)

Reads harbor job directories from local disk. Provides transcript loading, session tree building, trajectory parsing, artifact listing. No changes to its API surface.

When a user drills into a run that has harbor data available (local cache or S3), RunStore provides the detail layer: transcripts, trajectories, verifier output, timing.

### LiveStore (new: `live_store.py`)

Two data sources for in-flight waves:

**S3 polling:** Checks `s3://harbor-eval-results-526275945504/runs/{wave_id}/` every 30 seconds for new `result.json` files. Reuses the approach from `wave_scores.py` — list objects, parse reward values. Discovers active waves from `.serf-launches/*.json`.

**SSH proxy (optional):** Connects to running EC2 instances for real-time status — container health, task progress, system metrics. Uses `eval_lib.py`'s existing SSH helpers. Only activated when user clicks into a specific instance.

Emits updates via SSE (Server-Sent Events) to connected browsers.

### S3 client (new: `s3_client.py`)

Thin wrapper around `aws s3 sync` CLI calls. Downloads transcripts/artifacts on-demand to local cache (`~/.serf-evals/`) when user drills into a run not yet cached locally. No boto3 dependency — shells out to AWS CLI which is already installed and configured.

## Pages

### 1. Experiments (landing page)

All runs in a sortable, filterable table.

| Column | Source |
|--------|--------|
| Date | run JSON `date` field |
| Variant | run JSON `variant` field |
| Git SHA | run JSON `git_sha` (linked to commit) |
| Model | run JSON `model` |
| Mean Score | computed from `results` |
| Tasks Tested | count of `results` keys |
| Perfect (3/3) | count of tasks with score 1.0 |

Filters:
- Type: waves vs experiments (detected by run_id prefix pattern)
- Date range picker
- Model selector
- Score range

Click a row to drill into Run Detail.

### 2. Scoreboard

Interactive 89-task matrix. Rows = tasks, columns = recent N runs (configurable, default 10).

Cells colored by score: green (1.0), yellow (0.33-0.67), red (0.0). Hover shows rep breakdown.

Filters from `task-sets.md` categories:
- Discriminators (62) — the signal tasks
- Always-perfect (8) — safety net
- Never-passed (19) — excluded by default
- Regression set (9)
- Custom: failing, solved, untested

Click a cell to jump to Task Detail for that run. Click a task name to see Task History.

### 3. Live Monitor

Real-time view of running waves.

Layout:
- Top bar: wave ID, start time, elapsed, projected completion
- Running mean score, updating as results land
- Task grid (89 cells): completed-pass (green), completed-fail (red), in-progress (blue pulse), pending (gray)
- Per-task breakdown when clicked: rep status, reward value, timing

Data flow:
- Backend polls S3 every 30s
- SSE endpoint pushes task completion events to browser
- Browser updates grid cells and recalculates mean in real-time
- Optional: click an instance ID to SSH-proxy into its eval_dashboard for deep container monitoring

Auto-discovers active waves from `.serf-launches/` metadata files.

### 4. Compare

Side-by-side comparison of two runs.

Run selectors: two dropdowns pre-populated from experiment list, with most recent runs at top.

Categories (extending existing `/api/compare`):
- Improved: fail → pass (green)
- Regressed: pass → fail (red)
- Stable pass: pass → pass
- Stable fail: fail → fail
- Only in A / Only in B (different task sets)

Enhancements over existing:
- Per-task rep dots showing individual rep changes
- Bootstrap 95% CI on pass-rate difference (from `eval_stats.py`)
- McNemar's p-value for statistical significance
- Link each task row to its Task Detail

### 5. Run Detail (extends existing)

Adds experiment metadata header to existing run detail page:
- Variant description, git SHA (linked), date, model
- Mean score with delta from previous wave
- Failure category breakdown (timeout / wrong_answer / api_error / no_submit)

Task table enhanced with:
- Rep dots (pass/fail per rep) from git JSON
- Link to task history

When harbor data is available locally or via S3, shows full stats (rounds, tokens, wall time) per task. When only git JSON is available (no harbor data cached), shows scores only with a "load transcripts" button that triggers S3 download.

### 6. Task Detail & Transcripts (extends existing)

Existing trajectory timeline, session tree, verifier output, artifact browser — all unchanged.

New additions:
- **Task history sidebar:** Score sparkline showing this task's performance across all runs. Click a point to jump to that run's version.
- **Timing breakdown:** Wall time, total LLM latency, total tool execution time, time waiting for API responses. Computed from transcript API call entries.
- **On-demand S3 loading:** If transcripts aren't cached locally, "Load from S3" button fetches them.

## Frontend Architecture

### No build step

Preact + htm loaded via CDN ESM imports:
```
import { h, render } from 'https://esm.sh/preact'
import { useState, useEffect } from 'https://esm.sh/preact/hooks'
import htm from 'https://esm.sh/htm'
const html = htm.bind(h)
```

### File structure

```
tools/dashboard/static/
  index.html          — SPA shell (updated to load Preact entry)
  style.css           — existing design system (extended, not replaced)
  js/
    app.js            — router, nav bar, layout shell
    experiments.js    — experiment list with filters
    scoreboard.js     — interactive matrix
    live.js           — live monitor with SSE
    compare.js        — comparison view
    run-detail.js     — enhanced run detail
    task-detail.js    — enhanced task detail
    components/
      score-bar.js    — pass rate visualization
      rep-dots.js     — per-rep pass/fail indicators
      status-badge.js — pass/fail/running badges
      stat-card.js    — metric display card
      filters.js      — shared filter controls
```

### SSE for live updates

The `/api/live/waves/{wave_id}/stream` endpoint sends events:
```
event: task_complete
data: {"task": "chess-best-move", "rep": 2, "reward": 1.0, "mean": 0.667}

event: wave_progress
data: {"completed": 45, "total": 89, "mean": 0.542}
```

Browser connects via `EventSource`, updates Live Monitor grid reactively.

### Existing app.js

Stops being served as the SPA entry point. Remains in the repo for reference and rollback. The new `js/app.js` replaces it completely.

## Backend Changes

### New routes in server.py

```
GET /api/experiments                          — experiment list
GET /api/experiments/{run_id}                 — single experiment
GET /api/scoreboard                           — full task matrix
GET /api/scoreboard?filter=failing            — filtered matrix
GET /api/tasks/{task_name}/history            — task across all runs (replaces existing)
GET /api/live/waves                           — active waves
GET /api/live/waves/{wave_id}                 — wave status snapshot
GET /api/live/waves/{wave_id}/stream          — SSE event stream
GET /api/compare/{run_a}/{run_b}              — enhanced comparison
GET /api/runs/{job}/tasks/{task}/download      — trigger S3 download for transcripts
```

Existing routes (`/api/runs/*`, `/raw/*`, `/health`) unchanged.

### New dependencies

Add to `requirements.txt`:
```
watchfiles>=1.0        # filesystem monitoring for ExperimentStore reload
aiofiles>=24.0         # async file reading
```

No boto3. S3 access via `aws s3` CLI subprocess calls.

### Configuration

Environment variables:
- `DASHBOARD_DATA_DIR` — harbor job directory (existing, default `/data/agent-evals/runs`)
- `DASHBOARD_EXPERIMENTS_DIR` — git-tracked JSON directory (new, default `docs/experiments`)
- `DASHBOARD_CACHE_DIR` — local eval cache (new, default `~/.serf-evals`)
- `DASHBOARD_S3_BUCKET` — S3 bucket for results (new, default `harbor-eval-results-526275945504`)
- `DASHBOARD_LAUNCHES_DIR` — wave launch metadata (new, default `.serf-launches`)

CLI args mirror these for convenience.

## Testing

### New test files

- `test_experiment_store.py` — loading git JSON, filtering, scoreboard matrix, task history
- `test_live_store.py` — S3 polling (mocked subprocess), wave discovery, SSE event generation
- `test_s3_client.py` — download commands, cache management

### Existing tests unchanged

All existing tests in `test_server.py`, `test_data.py`, `test_stats.py`, `test_trajectory.py`, `test_markdown.py` continue to pass. The existing RunStore and routes are not modified.

### Fixtures

Extend `conftest.py` with:
- `experiment_dir` — temporary directory with sample run JSONs, task JSONs, scoreboard.json
- `launch_dir` — temporary `.serf-launches/` with wave metadata

## What stays untouched

These existing tools continue working exactly as before:
- `scoreboard.py` (CLI scoreboard)
- `wave_scores.py` (CLI live scores)
- `compare_runs.py` (CLI comparison)
- `interrogate_session.py` (session interrogation)
- `export_transcript.py` (HTML transcript export)
- `eval_dashboard.py` + `eval_dashboard.html` (on-box EC2 monitoring)
- `collect_results.py`, `eval_results.py` (result collection pipeline)
- All other tools in `tools/`

The researcher's active workflow is unaffected.
