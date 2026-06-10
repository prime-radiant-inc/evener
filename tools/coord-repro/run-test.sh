#!/usr/bin/env bash
# Local coordinator delegation test for chess-best-move.
#
# Tests whether the coordinator delegates to an implementer or bypasses
# delegation by writing the answer directly. Does NOT test answer correctness.
#
# Usage:
#   OPENAI_API_KEY=... ./run-test.sh [label] [coordinator.md-path]
#
# The binary is built fresh from the current source tree. If coordinator.md-path
# is given, it replaces the bundled coordinator.md before building.
#
# Output: DELEGATE or BYPASS + details
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
LABEL="${1:-test}"
COORD_MD="${2:-}"
COORDINATOR_PATH="$REPO_ROOT/agent/bundled_plugins/coordinator-workflow/agents/coordinator.md"

WORKDIR="/tmp/coord-repro-work-${LABEL}"
STATEDIR="/tmp/coord-repro-state-${LABEL}"

# --- Setup workspace ---
rm -rf "$WORKDIR" "$STATEDIR"
mkdir -p "$WORKDIR"

# Generate chess board image
python3 "$SCRIPT_DIR/generate-board.py" "$WORKDIR/chess_board.png"

# --- Build binary with current (or overridden) coordinator.md ---
ORIG_COORD=""
if [ -n "$COORD_MD" ]; then
  ORIG_COORD=$(mktemp)
  cp "$COORDINATOR_PATH" "$ORIG_COORD"
  cp "$COORD_MD" "$COORDINATOR_PATH"
fi

cd "$REPO_ROOT"
go clean -cache 2>/dev/null || true
go build -o "/tmp/serf-coord-repro-${LABEL}" ./cmd/serf/ 2>&1

# Restore original coordinator.md if we swapped it
if [ -n "$ORIG_COORD" ]; then
  cp "$ORIG_COORD" "$COORDINATOR_PATH"
  rm -f "$ORIG_COORD"
fi

BINARY="/tmp/serf-coord-repro-${LABEL}"

# --- Task prompt (matches terminal-bench exactly, adjusted path) ---
TASK="The file chess_board.png has an image of a chess board. It is currently white to move. Write the best move for white to play to ${WORKDIR}/move.txt in the form [src][dst], for example, e2e4 or h1h8. If there are multiple winning moves, print them all, one per line."

# --- Run coordinator (low round limit — we only care about the delegation decision) ---
OPENAI_API_KEY="${OPENAI_API_KEY:?Set OPENAI_API_KEY}" \
  "$BINARY" \
  --provider openai --model gpt-5.4-mini --reasoning-effort none \
  --max-rounds 8 --no-project-prompts \
  --state-dir "$STATEDIR" --dir "$WORKDIR" \
  -- "$TASK" > "/tmp/coord-repro-log-${LABEL}.txt" 2>&1 || true

# --- Analyze transcript ---
SPAWNED="no"
WROTE_FILE="no"
USED_SHELL_WRITE="no"

python3 -c "
import json, glob, sys

spawned = False
wrote = False
shell_wrote = False

for f in sorted(glob.glob('${STATEDIR}/sessions/*.transcript.jsonl')):
    for line in open(f):
        entry = json.loads(line)
        if entry.get('kind') != 'entry':
            continue
        msg = entry.get('turn', {}).get('message', {})
        for part in msg.get('content', []):
            if not isinstance(part, dict) or part.get('kind') != 'tool_call':
                continue
            tc = part['tool_call']
            name = tc.get('name', '')
            args = tc.get('arguments', {})
            if isinstance(args, str):
                import json as j
                try:
                    args = j.loads(args)
                except:
                    args = {}
            if name == 'delegate':
                spawned = True
            elif name == 'write_file':
                wrote = True
            elif name in ('shell', 'exec_command'):
                cmd = args.get('command', '') or args.get('cmd', '')
                if any(op in cmd for op in ['>', 'tee ', 'cat <<', 'echo ', 'printf ']):
                    if 'move.txt' in cmd or 'move' in cmd.lower():
                        shell_wrote = True

print('delegate=' + str(spawned).lower())
print('write_file=' + str(wrote).lower())
print('shell_write=' + str(shell_wrote).lower())
" > "/tmp/coord-repro-result-${LABEL}.txt"

source "/tmp/coord-repro-result-${LABEL}.txt"

if [ "$delegate" = "true" ]; then
  echo "DELEGATE  label=$LABEL"
elif [ "$write_file" = "true" ] || [ "$shell_write" = "true" ]; then
  echo "BYPASS    label=$LABEL"
else
  echo "UNCLEAR   label=$LABEL  (check /tmp/coord-repro-log-${LABEL}.txt)"
fi

# Cleanup binary
rm -f "$BINARY"
