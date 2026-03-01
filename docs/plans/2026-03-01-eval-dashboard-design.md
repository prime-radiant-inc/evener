# Eval Dashboard Design

## Problem

Investigating eval failures requires SSH, throwaway scripts, and mental juggling between
transcripts, verifier output, and API logs. The existing transcript viewer is a static HTML
generator with a dark cyber aesthetic. It shows raw transcript entries linearly — no
hierarchy, no trajectory overview, no cross-run comparison.

We need a live dashboard that makes eval results immediately comprehensible to both humans
and agents.

## Architecture

A single FastAPI process on magic-kingdom serves both the API and the frontend. No database,
no build step, no npm. The server reads eval data directly from disk — harbor's output
directories for running evals, the structured archive for completed ones.

```
tools/dashboard/
  server.py          # FastAPI app, routes, content negotiation
  data.py            # Discover runs, read transcripts/summaries/rewards
  trajectory.py      # Parse transcripts into high-level trajectory
  markdown.py        # Render any view as markdown
  static/
    index.html       # SPA shell
    app.js           # Client-side routing and rendering
    style.css        # Styles
```

**Content negotiation.** Every endpoint returns markdown by default. The frontend sends
`Accept: application/json` and gets JSON. An agent or a human with curl gets readable
markdown without any flags.

**Data sources.** Configurable via CLI flags:
- `--runs-dir /data/serf-evals/runs/` — completed, archived runs
- `--live-dir /tmp/` — in-progress harbor jobs (pattern: `full-*/`)

No archiving step required to view results. The dashboard reads harbor's native directory
structure.

**Deploy:** `pip install fastapi uvicorn && uvicorn server:app --host 0.0.0.0 --port 8080`

## Pages

### `/` — Dashboard

All runs, newest first. Running evals pinned to top with live pass/fail counts.

Table columns: run name, model, git SHA, pass rate (bar), status (running/complete), age.

### `/runs/{run_id}` — Run Detail

Header with manifest info: model, git SHA, branch, build time.

Stat cards: pass rate + Wilson CI, failure category breakdown (bar chart).

Task grid: each task as a row — name, result badge, failure category, session count.
Filterable by result and failure category.

### `/runs/{run_id}/tasks/{task}` — Task Detail

The redesigned transcript viewer. Two panels:

**Left: Trajectory view.** The transcript parsed into a high-level timeline:

```
Round 1   EXPLORE   ls, cat instruction.md
Round 2   PLAN      "I'll implement the extension..."
Round 3   SPAWN     test-engineer → 12 rounds, 3 files
Round 4   EXEC      pytest → 2/5 passed
Round 5   EDIT      Fixed setup.py:42
Round 6   EXEC      pytest → 5/5 passed
Round 7   SUBMIT    communicate("Done...")
          REVIEW    Reviewer → 6 rounds → REJECTED
                    "Missing import, Cython not compiled"
Round 8   EDIT      Added import, fixed build
Round 9   SUBMIT    communicate("Fixed...")
          REVIEW    Reviewer → 4 rounds → APPROVED
```

Each round classified by its dominant action:
- `EXPLORE` — read_file, glob, grep
- `EXEC` — shell commands
- `EDIT` — apply_patch, edit_file, write_file
- `SPAWN` — spawn_agent (child summary inlined)
- `SUBMIT` — communicate/submit_result
- `REVIEW` — reviewer subagent (verdict + reason surfaced)
- `PLAN` — assistant text without tool calls
- `ERROR` — API or tool errors

Click any round to expand the full raw entries (tool arguments, results, usage).

Subagent sessions nest inline at their spawn point — not as separate flat sections.
The reviewer appears indented under the SUBMIT that triggered it. The tree structure
is visible, not hidden.

**Right: Context panel.** Verifier test output, task instruction, failure category.
Visible alongside the transcript so you never juggle between files.

### `/compare` — Cross-Run Comparison

Select two or more runs. Table shows each task with result per run, delta
(improvement/regression/stable), and summary statistics (bootstrap CI, McNemar's p-value).

Click a task to see trajectories side-by-side as parallel columns.

### `/tasks/{task}` — Task History

One task across all runs and reps. Shows how this task performs over time and across
configurations. Trajectories from different runs displayed as parallel timelines — you
see strategy differences at a glance.

## Visual Design

Warm neutral aesthetic. Linear/Vercel territory.

- Background: `#F8F8F6` (warm off-white)
- Cards: `#FFFFFF`, border `1px solid rgba(0,0,0,0.08)`, subtle shadow
- Text: `#1A1A1A` primary, `#6B6B6B` secondary, `#A0A0A0` tertiary
- Code/transcripts: `'SF Mono', 'Cascadia Code', 'JetBrains Mono', monospace`
- Body: system font stack (`-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif`)
- Pass: `#18A34A`, Fail: `#DC2626`, Timeout: `#CA8A04`, No Submit: `#9CA3AF`
- Spacing: 8px grid. 14px body text. Generous whitespace.

No gradients, no glows, no pill badges. Status shown by small colored dots or subtle
left-border accents. Transcript entries use indentation and whitespace — not colored
backgrounds — to show structure. The trajectory timeline uses a vertical line with dots
(like git log) to show progression.

## Agent-Facing Markdown API

Every URL returns markdown by default. Examples:

`GET /` returns a summary table of all runs with pass rates.

`GET /runs/{id}/tasks/{task}` returns the trajectory, verifier output, and reviewer
verdict as structured markdown — readable in a context window, parseable by an agent
investigating failures.

This replaces SSH + throwaway scripts for failure investigation.

## Future: Multi-Trial Support

Eval runs will have multiple reps per task. The dashboard shows per-task pass rates
(majority/strict/any) and lets you compare trajectories across reps — same task,
same build, different outcomes. This reveals nondeterminism: "Rep 1 passed because
it tried approach X, Rep 2 failed because it tried approach Y."

Cross-harness comparison (serf vs codex on the same task) uses the same trajectory
side-by-side view, filtered by adapter.
