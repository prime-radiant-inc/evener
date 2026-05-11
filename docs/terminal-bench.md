# Terminal-Bench & Harbor Reference

## Overview
- terminal-bench is a benchmark suite of 89 coding tasks run in Docker containers
- Harbor is the orchestrator CLI that manages container lifecycle, agent execution, and verification
- Each task has: instructions, a Docker environment, a hidden verifier, and a 900s timeout
- Agents run inside containers with tools (shell, file read/write, etc.)

## Infrastructure
- **flower-garden**: Linux server at 192.168.118.101 (jesse@), 8 cores, 30GB RAM, Ubuntu + Docker
- **Benchmark repo**: `~/git/terminal-bench/` on flower-garden
- **Serf adapter**: `~/git/terminal-bench/serf_agent.py` — Python adapter that bridges harbor to serf binary
- **Install template**: `~/git/terminal-bench/install-serf.sh.j2` — Jinja2 template for apt packages, python symlink, serf binary copy into container
- **Serf binary**: `~/git/terminal-bench/serf-linux-amd64` — uploaded via scp from local build
- **Task cache**: `~/.cache/harbor/tasks/<hash>/<task-name>/` — task definitions, environments, verifiers
- **Verifier source**: `~/.cache/harbor/tasks/<hash>/<task-name>/tests/` — the hidden test scripts/files

## Harbor CLI

Harbor is the eval orchestrator. Install with `uv tool install harbor` (PyPI package: `harbor`).
On flower-garden it's at `~/.local/bin/harbor` (installed via `uv tool install`).

### Basic run command
```bash
cd ~/git/terminal-bench
harbor run \
  --agent-import-path 'serf_agent:SerfAgent' \
  -m openai/gpt-5.2-codex \
  --ak max_rounds=100 \
  -d 'terminal-bench@2.0' \
  -t <task-name> \
  -o /tmp/serf-runN \
  -n 4 \
  --delete \
  --debug
```

### Key flags
- `--agent-import-path 'serf_agent:SerfAgent'` — Python module:class for the agent adapter
- `-m openai/gpt-5.2-codex` — model name (passed through to serf)
- `--ak max_rounds=100` — agent kwargs (passed to SerfAgent constructor)
- `-d 'terminal-bench@2.0'` — dataset name and version
- `-t <task-name>` — specific task(s) to run; omit for ALL 89 tasks; can repeat `-t` for multiple
- `-o /tmp/output-dir` — output directory for results
- `-n 4` — parallelism (number of concurrent containers). Use 4 for flower-garden (8 cores, 30GB RAM). Use 1 for sequential debugging.
- `--delete` — delete Docker containers after completion (saves disk)
- `--debug` — verbose logging

### Output structure
```
/tmp/serf-runN/
  2026-02-23__04-19-54/          # timestamped run directory
    config.json                   # run configuration
    result.json                   # aggregate results
    job.log                       # harbor log
    <task>__<hash>/              # per-task directory
      result.json                # task result with reward
      agent/
        command-0                # agent command output
        install.sh               # install script used
        setup/                   # setup artifacts
        serf-state/
          sessions/
            <session-id>.json              # session metadata
            <session-id>.transcript.jsonl  # full transcript
```

### Result format
```json
{
  "verifier_result": {
    "rewards": {
      "reward": 1.0
    }
  },
  "exception_info": null,
  "agent_execution": {
    "started_at": "...",
    "finished_at": "..."
  }
}
```
- reward 1.0 = pass, 0.0 = fail
- exception_info may contain "AgentTimeoutError" for 900s timeouts

## Transcript format (.transcript.jsonl)
Each line is a JSON object:
- Header: `{"kind": "header", "session_id": "...", "model": "...", "working_dir": "/app"}`
- Entries: `{"kind": "entry", "seq": N, "turn": {...}}`
  - turn.kind: "USER_INPUT", "ASSISTANT", "TOOL_RESULT", "STEERING"
  - turn.message.content: array of parts
    - `{"kind": "text", "text": "..."}` — text content
    - `{"kind": "tool_call", "tool_call": {"name": "...", "arguments": {...}}}` — tool invocation
    - `{"kind": "tool_result", "tool_result": {"name": "...", "content": "..."}}` — tool output

### Parsing transcripts remotely
```bash
ssh jesse@192.168.118.101 'python3 << '"'"'PYEOF'"'"'
import json, glob
for path in sorted(glob.glob("/tmp/serf-runN/2026-*/<task>*/agent/serf-state/sessions/*.transcript.jsonl")):
    for line in open(path):
        e = json.loads(line)
        if e.get("kind") != "entry": continue
        seq = e["seq"]
        turn = e.get("turn", {})
        tkind = turn.get("kind", "")
        msg = turn.get("message", {})
        content = msg.get("content", [])
        if not isinstance(content, list): continue
        for part in content:
            if not isinstance(part, dict): continue
            pk = part.get("kind", "")
            if pk == "tool_call":
                tc = part.get("tool_call", {})
                name = tc["name"]
                if name == "communicate":
                    args = tc.get("arguments", {})
                    print("[%d] COMMUNICATE(%s): %s" % (seq, args.get("action",""), args.get("message","")[:400]))
                else:
                    a = json.dumps(tc.get("arguments", ""))[:200]
                    print("[%d] %s: %s" % (seq, name, a))
PYEOF'
```

**IMPORTANT**: When writing Python in SSH heredocs, avoid f-strings with nested braces/quotes.
Use `%` formatting or `.format()` instead. The quoting pattern `'python3 << '"'"'PYEOF'"'"'`
works for embedding Python in bash over SSH.

## Workflow: Iterating on prompts

### 1. Edit prompt locally
Edit `agent/prompts/base.md` in the serf repo.

### 2. Build and test locally (quick iteration)
```bash
go build -o /tmp/serf-mac ./cmd/serf/
export $(cat .env | xargs)
/tmp/serf-mac --model openai/gpt-5.2-codex --max-rounds 8 \
  --state-dir /tmp/test-state -- 'task instructions here'
```
- Use low --max-rounds (5-15) for quick attitude checks
- Can't test tasks that need /app/ or Docker-specific resources
- Good for: checking if agent attempts vs gives up, plan quality, test quality

### 3. Resume to interrogate decisions
```bash
/tmp/serf-mac --model openai/gpt-5.2-codex --max-rounds 3 \
  --state-dir /tmp/test-state \
  --resume-with <SESSION_ID> \
  -- 'Why did you choose X instead of Y?'
```
- `--resume <id>` continues the session (agent keeps working)
- `--resume-with <id>` starts new conversation with old context (for asking questions)

### 4. Build Linux binary and upload
```bash
GOOS=linux GOARCH=amd64 go build -o serf-linux-amd64 ./cmd/serf/
scp -o StrictHostKeyChecking=no serf-linux-amd64 jesse@192.168.118.101:~/git/terminal-bench/serf-linux-amd64
```

### 5. Run on flower-garden

**CRITICAL**: The harbor process must have API keys in its environment. The `.env` file
at `~/git/terminal-bench/.env` has `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, and `GEMINI_API_KEY`.
You MUST source it before launching harbor, otherwise serf inside the container gets no API
key and fails with "no LLM providers configured via environment variables".

```bash
ssh -o StrictHostKeyChecking=no jesse@192.168.118.101 '
  export $(cat ~/git/terminal-bench/.env | xargs)
  cd ~/git/terminal-bench
  nohup ~/.local/bin/harbor run [flags] > /tmp/serf-runN.log 2>&1 &
  echo "PID=$!"
'
```

**Note on quoting**: Use single quotes for the outer SSH command so `$!` and `$()` expand
on the remote host, not locally. Double-quote the outer command causes `$!` to expand
to nothing locally.

### 6. Monitor progress
```bash
# Quick status
ssh jesse@192.168.118.101 "for d in /tmp/serf-runN/2026-*/*/; do
  task=\$(basename \$d | sed 's/__.*//');
  if [ -f \$d/result.json ]; then
    reward=\$(python3 -c \"import json; print(json.load(open('\$d/result.json')).get('verifier_result',{}).get('rewards',{}).get('reward','?'))\");
    [ \"\$reward\" = '1.0' ] && echo \"PASS \$task\" || echo \"FAIL \$task\";
  else echo \".... \$task\"; fi;
done"

# Check running containers
ssh jesse@192.168.118.101 "docker ps --format '{{.Names}}' | grep -v calibre | grep -v torrent | sort"

# Check harbor process
ssh jesse@192.168.118.101 "ps aux | grep harbor | grep -v grep"
```

### 7. Kill a run
```bash
ssh jesse@192.168.118.101 "
  kill <HARBOR_PID>
  sleep 2
  ps aux | grep harbor | grep -v grep | awk '{print \$2}' | xargs -r kill
  sleep 3
  docker ps -q --filter 'name=<task>' | xargs -r docker stop
"
```

## Reading verifier source
Verifiers are at `~/.cache/harbor/tasks/<hash>/<task>/tests/` on flower-garden.
Common patterns:
- `verify.sh` — bash script that runs pytest or custom checks
- `test_*.py` — pytest files with the actual assertions
- Some use `expect` scripts for interactive testing (e.g., SSH with passwords)
- Verifiers often install their own dependencies (curl, uv, pytest)

To find the hash for a task:
```bash
ssh jesse@192.168.118.101 "ls ~/.cache/harbor/tasks/"
# Then: ls ~/.cache/harbor/tasks/<hash>/<task-name>/tests/
```

## Running 3x full suite
Script at `/tmp/run-full-3x.sh` on flower-garden runs 3 sequential full-suite runs:
```bash
nohup /tmp/run-full-3x.sh > /tmp/full-3x.log 2>&1 &
```
Output goes to `/tmp/serf-full-{1,2,3}/`. Each run takes ~4-5 hours at `-n 4`.

## Key gotchas
- **API keys MUST be in harbor's environment**: Source `~/git/terminal-bench/.env` before launching harbor. Without this, serf inside the container has no API key and immediately fails with "no LLM providers configured". The serf adapter reads `OPENAI_API_KEY` from `os.environ` and passes it into the container. Always use `export $(cat ~/git/terminal-bench/.env | xargs)` before `harbor run`.
- **Harbor is `~/.local/bin/harbor`, NOT `terminal-bench`**: The `terminal-bench` package (PyPI) is a different project that installs a competing CLI. If you see a `terminal-bench` binary in a venv, that's the wrong tool. Harbor is installed via `uv tool install harbor` and lives at `~/.local/bin/harbor`.
- **Harbor stdout is buffered under nohup**: the log file may be empty for a while. Check `docker ps` or transcript files instead.
- **Python f-string syntax errors in SSH**: commas and colons in f-string braces cause SyntaxError on remote Python. Use % formatting.
- **SSH host key changes**: flower-garden's key may change. Always use `-o StrictHostKeyChecking=no`.
- **Docker disk space**: large tasks (mteb ~8.6GB) can fill disk. Use `--delete` to clean up containers.
- **900s agent timeout**: harbor kills the agent after 900s regardless of --max-rounds. AgentTimeoutError in results.
- **Nondeterminism**: ~18 of 89 tasks are flaky — they pass in some runs and fail in others. Need 3+ runs for reliable numbers.
- **flower-garden has other services**: calibre, torrent, plex, etc. Filter them out when checking docker ps.
- **Parallelism tradeoffs**: `-n 4` is good for flower-garden. Higher may cause resource contention on CPU/memory-heavy tasks.

## Scoring methodology
- **Single run score**: passes/89 from one run
- **Cumulative best**: union of all passes across all runs (optimistic upper bound)
- **3-run average**: mean of 3 independent full runs (realistic expected score)
- Tasks are binary: 1.0 (all verifier tests pass) or 0.0 (any failure)

## Failure categories
From auditing multiple runs, failures fall into these buckets:
- **TIMEOUT**: Agent killed at 900s. Often heavy compute tasks (caffe-cifar-10, train-fasttext)
- **GAVE UP**: Agent concluded task was impossible and stopped early. Target with prompt changes.
- **WRONG APPROACH**: Agent built the wrong thing (e.g., local git instead of SSH)
- **CLOSE/PARTIAL**: Agent got most tests passing but missed 1-2. Often nondeterministic.
- **DOMAIN HARD**: Task requires specialized knowledge the model lacks
- **MODEL ERROR**: Content filter, API error, or rate limit killed the session
