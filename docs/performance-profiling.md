# Performance Profiling

Tools for measuring and optimizing serf's per-round framework overhead.

## Quick Start

```bash
# Run the benchmark (compares pre-perf and current builds)
./perf-bench/run.sh openai/gpt-5.4-mini
```

This builds two binaries (old vs current), runs each against the same task spec, and reports wall clock time, test results, API call counts, and LLM time breakdown.

## CLI Profiling Flags

The serf binary has built-in Go profiling support:

```bash
# CPU profile (go tool pprof)
serf --provider openai --model gpt-5.4-mini \
     --cpu-profile profile.prof \
     "your task here"

# Analyze:
go tool pprof -http=:8080 $(which serf) profile.prof

# Execution trace (go tool trace)
serf --provider openai --model gpt-5.4-mini \
     --trace trace.out \
     "your task here"

# Analyze:
go tool trace trace.out
```

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

In `--verbose` mode, these appear as NDJSON events on stderr. To extract from transcripts, use the API log timestamps to compute inter-call gaps.

## Synthetic Benchmark

```bash
# Run the micro-benchmark (mock LLM, measures pure framework overhead)
go test ./agent/ -bench BenchmarkRoundOverhead -benchtime 10x -run "^$"
```

This uses a mock LLM client with 10ms simulated latency and reports per-round overhead in microseconds. Useful for detecting framework regressions without real API calls.

## Analyzing Real Runs

### API log timing

Every LLM call is logged to `<state-dir>/api.jsonl` with latency:

```bash
# Total LLM time vs wall clock
python3 -c "
import json
calls = [json.loads(l) for l in open('state/api.jsonl')]
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
| API log fsync | `file.Sync()` per write | Periodic (2s interval) |
| Tool definitions | Rebuilt every round | Cached, two lists for MinResultRound gate |
| System prompt components | Rebuilt every round | Cached at init |
| History copies | 3x per round | 1x per round |

**Result**: Framework overhead went from ~467ms/round (19% of wall with gpt-5.4-mini) to ~32ms/round (1.5% of wall). With slower models like gpt-5.2, framework overhead is unmeasurable (<0.1%).
