# Performance Profiling

Tools for measuring and optimizing evener's per-round framework overhead.

## Quick Start

```bash
# Run the benchmark (compares pre-perf and current builds)
./perf-bench/run.sh openai/gpt-5.4-mini
```

This builds two binaries (old vs current), runs each against the same task spec, and reports wall clock time, test results, API call counts, and LLM time breakdown.

## CLI Profiling Flags

The evener binary has built-in Go profiling support:

```bash
# CPU profile (go tool pprof)
evener --model openai/gpt-5.4-mini \
     --cpu-profile profile.prof \
     "your task here"

# Analyze:
go tool pprof -http=:8080 $(which evener) profile.prof

# Execution trace (go tool trace)
evener --model openai/gpt-5.4-mini \
     --trace trace.out \
     "your task here"

# Analyze:
go tool trace trace.out
```

## Live Profiling of `evener serve` and `evener hub`

`--cpu-profile` only covers a process from its start. To profile a daemon or
hub that has already been running for days, start it with
`EVENER_PPROF_ADDR` set to a loopback `host:port`. The process then serves
Go's `net/http/pprof` handlers on that address, on their own listener, never
on the hub's public one. Unset (the default) starts nothing. A host that is
not `127.0.0.1`, `[::1]`, or `localhost` is refused at startup.

```bash
EVENER_PPROF_ADDR=127.0.0.1:0 evener hub
# [hub] pprof listening on http://127.0.0.1:41873/debug/pprof/
```

Port `0` picks a free port, and the process logs the one it bound: the hub
to its stderr, each `evener serve` daemon to its stderr (for hub-spawned
daemons, that is the session's log under `<run_dir>/logs/`). Local daemons
the hub spawns inherit the hub's environment, so setting the variable on the
hub enables it for every daemon it launches. Use port `0` there: with a fixed
port the hub takes it, and each daemon logs a warning and runs without pprof.
Failing to bind never stops a process from starting; only a malformed or
non-loopback address does. To profile
daemons without the hub, set the variable in a launch configuration's `[env]`
table instead (see [Launch configuration](../evener-hub.md#launch-configuration)).

Grab profiles with `go tool pprof`, using the address from the log:

```bash
addr=127.0.0.1:41873
go tool pprof -http=:8080 "http://$addr/debug/pprof/heap"               # live heap
go tool pprof -http=:8080 "http://$addr/debug/pprof/profile?seconds=30" # 30s CPU sample
curl -s "http://$addr/debug/pprof/goroutine?debug=2" > goroutines.txt    # every goroutine's stack
```

`http://$addr/debug/pprof/` lists every profile the process offers.

## Round Timings

Every round of `processOneInput()` emits a `ROUND_TIMINGS` event with per-phase wall clock durations:

| Field | What it measures |
|-------|-----------------|
| `SystemPrompt` | Building the system prompt (cached components + string concat) |
| `ContextMgmt` | Context pressure check, compaction if triggered |
| `HistoryExpand` | Converting Turn history to LLM messages |
| `ToolDefs` | Selecting tool definitions for the request |
| `LLMCall` | The actual API call (dominates everything) |
| `ToolExec` | Executing tool calls (shell, file I/O, etc.) |
| `Persistence` | Transcript append + session meta save |
| `AfterAction` | Strategy post-processing |
| `LoopOverhead` | Loop detection, steering drain, task reminders |
| `TotalRound` | Wall clock for the entire round |

In `--verbose` mode, these appear as NDJSON events on stderr. For provider timing,
use the canonical API log independently of the semantic transcript.

## Synthetic Benchmark

```bash
# Run the micro-benchmark (mock LLM, measures pure framework overhead)
go test ./agent/ -bench BenchmarkRoundOverhead -benchtime 10x -run "^$"
```

This uses a mock LLM client with 10ms simulated latency and reports per-round overhead in microseconds. Useful for detecting framework regressions without real API calls.

## State paths and identifiers

Profiling artifacts that inspect persisted sessions should use the canonical
state layout:

```text
${XDG_STATE_HOME:-~/.local/state}/evener/projects/<project-id>/sessions/<session-id>.transcript.jsonl
```

`<project-id>` is a readable canonical-project ID with a 10-character base62
suffix. The main checkout and linked worktrees resolve to the same project
bucket; a distinct clone resolves to a distinct bucket. `<session-id>` is a
22-character UUIDv7 base62 payload. The clean break does not migrate or remove
inert old state; remove it manually when it is no longer needed.

## Analyzing Real Runs

### API log timing

Every provider attempt is logged per session to
`<state-dir>/sessions/<session-id>.api.jsonl` with latency. Settlement records do
not carry `latency_ms`, so select attempts explicitly:

```bash
# Total LLM time vs wall clock
python3 -c "
import json
records = [json.loads(line) for line in open('state/sessions/SESSION_ID.api.jsonl')]
calls = [record for record in records if record.get('kind') == 'api_attempt']
total_ms = sum(c['latency_ms'] for c in calls)
print(f'API calls: {len(calls)}')
print(f'LLM time:  {total_ms/1000:.1f}s')
"
```

### Tool execution time

Tool durations are recorded in transcript entries (`duration_ms` field on each tool result):

```bash
# Extract tool durations from transcript
python3 -c "
import json
from collections import defaultdict
times = defaultdict(lambda: {'n': 0, 'ms': 0})
for line in open('state/sessions/*.transcript.jsonl'):
    obj = json.loads(line)
    if obj.get('kind') != 'entry': continue
    turn = obj['turn']
    if turn.get('kind') != 'TOOL_RESULTS': continue
    for p in turn['message']['content']:
        tr = p.get('tool_result', {})
        name, dur = tr.get('name','?'), max(0, tr.get('duration_ms', 0))
        times[name]['n'] += 1
        times[name]['ms'] += dur
for name, v in sorted(times.items(), key=lambda x: -x[1]['ms']):
    print(f'{name:<20} {v[\"n\"]:>4}x  {v[\"ms\"]:>6}ms')
"
```

### Framework overhead formula

```
framework_time = wall_clock - llm_time - tool_execution_time
framework_per_round = framework_time / num_api_calls
```

With current optimizations, expect ~30ms/round of pure framework overhead.

## Benchmark Task

`perf-bench/task.md` defines a Python CLI todo app task that exercises ~15-20 tool rounds (file writes, shell commands, pytest runs). Good for A/B comparisons because:

- Deterministic spec (no ambiguity for the model)
- Multi-file output (storage.py, todo.py, test_todo.py)
- Built-in verification (pytest must pass)
- Exercises file I/O, shell execution, and iterative fix loops

## What We Optimized (March 2026)

| Change | Before | After |
|--------|--------|-------|
| `LoadProjectDocs` per round | `git rev-parse` subprocess every round | Cached at session init |
| `maybeAutoSave` | Full history JSON (MBs, `MarshalIndent`) every round | 500-byte meta JSON |
| Transcript fsync | `file.Sync()` per write (3-6/round) | Periodic (1s interval) |
| API log durability | exact attempts and settlements | Synchronous append/sync before retry, fallback, or return |
| Tool definitions | Rebuilt every round | Cached, two lists for MinResultRound gate |
| System prompt components | Rebuilt every round | Cached at init |
| History copies | 3x per round | 1x per round |

**Result**: Framework overhead went from ~467ms/round (19% of wall with gpt-5.4-mini) to ~32ms/round (1.5% of wall). With slower models like gpt-5.2, framework overhead is unmeasurable (<0.1%).
