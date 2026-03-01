# Eval Dashboard v2: Analysis Workbench

## Problem

The current dashboard is a transcript viewer. It presents raw data and forces the user
to analyze in their head — clicking through rounds one at a time, mentally
correlating tool calls across sessions, and tracking results from task to task.

Three user personas tested the dashboard against real benchmark data and identified
the same core failure: **the tool presents data but draws no conclusions**.

A failure analyst needs 3-4 clicks per round, per child session, to understand a
single task. A regression hunter cannot compare two runs at all. A prompt engineer
cannot see aggregate patterns without clicking through all 89 tasks.

## Design Principles

1. **General, not coupled to current problems.** The dashboard computes generic
   metrics from transcripts and API logs. It knows about rounds, tools, sessions,
   tokens, and timing. It does not hardcode knowledge of reviewers, delegation
   patterns, or specific failure modes — those emerge from the data.

2. **Server computes, frontend renders.** The server computes per-task metrics,
   per-run aggregates, and cross-run comparisons. The frontend renders tables,
   charts, and formatted text. No analysis logic in JavaScript.

3. **Two-state expand.** Every expandable element is either a one-liner or fully
   open. No nested "Show more" buttons inside expanded sections. Click once to
   see everything; click again to collapse.

4. **Raw files accessible everywhere.** Any view that shows derived data links to
   the raw source file. Links open rendered files in a new browser tab.

## Pages

### 1. Run List (landing page)

A table of discovered runs. Each row shows job name, task count, pass/fail counts,
and pass rate. Minimal change from current design.

### 2. Run Detail (primary analysis view)

The task list becomes a **data table** with computed metric columns. This page is
where 80% of analysis happens.

**Columns:**

| Column | Source | Purpose |
|--------|--------|---------|
| Task | directory name | identify |
| Result | reward.txt | pass/fail badge |
| Category | result.json + heuristics | timeout/wrong_answer/no_submit |
| Rounds | transcript trajectory | total rounds across all sessions |
| Wasted | transcript trajectory | rounds with no tools and no text (ERROR) |
| Tokens (in/out) | transcript usage fields | total input + output tokens |
| Sessions | transcript file count | session count (proxy for delegation) |
| Depth | session tree | max session tree depth |
| Submit @ | trajectory SUBMIT action | round number of first submission |
| Wall Time | result.json timestamps | seconds from start to finish |
| History | cross-run scan | dots showing pass/fail in other runs |

All columns sortable by clicking the header. The existing filter bar
(All/Pass/Fail/Timeout/Wrong Answer/No Submit) remains. A search box filters
task names.

**Raw file links:** A small icon next to the run name opens the manifest.json
(if present) in a new tab.

### 3. Task Detail (deep dive)

Redesigned around the failure analyst's workflow.

**Summary card** at top: key metrics (rounds, wasted, tokens, sessions, wall time),
the submitted value (extracted from the first SUBMIT tool call's arguments), and
the outcome badge. All visible without clicking.

**Verifier output:** Full-width, expanded by default, scrollable. Left-border
accent indicates pass (green) or fail (red).

**Agent stdout:** Full-width, collapsed by default.

**Trajectory:** Rounds default to collapsed (one-liner showing round number,
action, tool names with key args, token count). Clicking a round opens it fully —
all tool call arguments, all tool results, no truncation. A
**Toggle All** button opens or closes every round at once. A **search box** above
the timeline filters rounds by text content (matches tool names, args, results,
and assistant text).

Child sessions render inline at their spawn point (already implemented). Each
child session's label shows the model and depth.

**Raw file links:** Icons next to section headers link to transcript files,
api.jsonl, result.json, config.json, stdout. Each opens rendered in a new tab.

### 4. Run Comparison

Pick two runs from dropdowns. The server computes the diff.

**Summary line:** "Run B: 45/89 (+4 vs Run A). 7 improved, 3 regressed."

**Table:** One row per task. Columns: task name, Run A result, Run B result. Rows
colored green (improved), red (regressed), or neutral (same). Sort by delta
to surface regressions first.

Only tasks present in both runs appear. Tasks unique to one run appear in a
separate section below.

### 5. Task History (cross-run view for one task)

Shows every run where a given task appeared. One row per run, sorted by time
(newest first).

**Columns:** Job name, result badge, failure category, rounds, wasted, tokens,
wall time, link to full task detail.

**Verifier output** for each run in a two-state collapsible section (closed or
fully open).

This view answers: "Has configure-git-webserver ever passed? When did it start
failing? What changed?"

## Server: Stats Module

New file: `stats.py`. Computes metrics from transcripts and api.jsonl.

### Per-Task Metrics (from transcripts)

```python
@dataclass
class TaskStats:
    total_rounds: int           # across all sessions
    rounds_by_action: dict      # {"EXEC": 12, "EXPLORE": 5, "ERROR": 3, ...}
    wasted_rounds: int          # rounds where action == "ERROR"
    total_tokens_in: int        # summed from round usage fields
    total_tokens_out: int
    session_count: int
    max_depth: int              # deepest session tree depth
    first_submit_round: int     # round number of first SUBMIT in root session (0 if none)
    submitted_value: str        # first argument of first SUBMIT tool call
    wall_time_sec: float        # from result.json (finished_at - started_at)
    action_sequence: list       # ordered list of root session actions
```

### Per-Task API Metrics (from api.jsonl, if present)

```python
@dataclass
class APIStats:
    api_call_count: int
    total_latency_ms: int
    avg_latency_ms: float
    empty_response_count: int   # text_length=0 and tool_call_count=0
```

### Per-Run Aggregates (derived from per-task)

- Pass/fail/timeout/wrong_answer/no_submit counts
- Total rounds, tokens, wall time across all tasks
- Median and p90 wall time
- Distribution of action types across all tasks

### Cross-Run Task History

The server scans all runs for tasks with the same name. Returns an array of
`{job_name, passed, failure_category, total_rounds, wasted_rounds, tokens, wall_time}`
sorted by job directory modification time.

### Disk Caching

Stats cache to `<data_dir>/.cache/<job_name>/stats.json`. Cache key = hash of all
transcript and api.jsonl file modification times. If any file changes, the cache
invalidates and recomputes on next request.

## Server: Raw File Endpoint

New endpoint: `GET /raw/{file_path}`

Serves any file under the data directory. Behavior by extension:
- `.json`: pretty-printed with syntax highlighting (HTML wrapper)
- `.jsonl`: each line pretty-printed, separated by horizontal rules
- `.txt`, other: monospace `<pre>` block

Response is an HTML page (opens in a new tab). The endpoint rejects paths
that traverse outside the data directory.

## Server: Comparison Endpoint

New endpoint: `GET /api/compare?a={job_a}&b={job_b}`

Returns:
```json
{
    "run_a": {"job_name": "...", "passed": 41, "total": 89},
    "run_b": {"job_name": "...", "passed": 45, "total": 89},
    "improved": [{"task": "fix-vuln", "a": "fail", "b": "pass"}, ...],
    "regressed": [{"task": "crack-7z", "a": "pass", "b": "fail"}, ...],
    "stable_pass": [...],
    "stable_fail": [...],
    "only_a": [...],
    "only_b": [...]
}
```

## Frontend

**Tech:** Vanilla JS. No framework, no build step. The `h()` helper stays. Code
split into logical sections within `app.js` (one section per page). If the file
exceeds ~1500 lines, split into separate JS files per page.

**Key interaction rules:**
- Two-state expand everywhere. Collapsed = one-liner. Expanded = everything.
- Raw file links appear as small link icons (↗) next to section headers.
- All tables support column sorting via click-on-header.
- Search boxes filter in real time as you type.
- Comparison page uses two `<select>` dropdowns populated from the run list.

**Charts:** Inline SVG for simple visualizations (history dots, pass-rate bars).
No charting library.

## Data We Already Have But Don't Use

The design surfaces data that already exists on disk:

| Data | Source | Currently Used | v2 Uses |
|------|--------|----------------|---------|
| Round action types | transcript | shown per-round | aggregated as columns |
| Token usage per round | transcript | tiny inline label | summed as task metric |
| Session depth | transcript header | label on child | max depth column |
| Wall time | result.json | not shown | column + sort |
| API call count | api.jsonl | not shown | task metric |
| API latency | api.jsonl | not shown | task metric |
| Empty response count | api.jsonl | not shown | correlates with wasted rounds |
| Config (model, kwargs) | config.json | model shown | linked as raw file |
| Agent artifacts | agent/artifacts/ | not shown | linked as raw files |
| Verifier CTRF | verifier/ctrf.json | not shown | linked as raw file |

## What This Design Does NOT Include

- **Charts and graphs beyond simple SVG dots/bars.** Add histograms or
  scatter plots if needed later. YAGNI for now.
- **Real-time monitoring of in-progress runs.** This is a post-hoc analysis tool.
- **Database.** File-based data with disk caching is sufficient for single-user
  access to <100 tasks per run.
- **Agent-specific logic.** No hardcoded knowledge of serf's tools, reviewer gate,
  or delegation patterns. The tool classifies tools by pattern matching and
  computes generic metrics.
