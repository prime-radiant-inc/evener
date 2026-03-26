#!/usr/bin/env bash
# Quick local repro for implementer behavior on mteb-retrieve.
# Runs the implementer DIRECTLY (--agent implementer), skipping coordinator.
# Feeds an AWS-style delegation message to test implementer prompt compliance.
#
# Usage:
#   OPENAI_API_KEY=... ./run-test.sh /path/to/serf-binary [label]
#
# Output: PASS/FAIL + whether the implementer read the model README
set -euo pipefail

BINARY="${1:?Usage: $0 /path/to/serf-binary [label]}"
LABEL="${2:-test}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

WORKDIR="/tmp/impl-repro-work-${LABEL}"
STATEDIR="/tmp/impl-repro-state-${LABEL}"
EXPECTED="MTEB: Massive Text Embedding Benchmark"

rm -rf "$WORKDIR" "$STATEDIR"
mkdir -p "$WORKDIR"
cp "$SCRIPT_DIR/data.txt" "$WORKDIR/"

# AWS-style delegation message — no hints about reading docs.
TASK="You need to solve an artifact-generation task in ${WORKDIR}. Requirements:
- Query string: 'terminal-bench'
- Corpus: each line of ${WORKDIR}/data.txt is a separate document; there are 30 lines total.
- Use embedding model BAAI/bge-small-zh-v1.5 at revision 7999e1d3359715c523056ef9478215996d62a620.
- sentence-transformers is installed.
- Find the document with the 5th highest cosine similarity to the query among the documents.
- Write the exact resulting line, with newline, to ${WORKDIR}/result.txt.

Constraints:
- Do the actual computation using the specified embedding model/revision; do not approximate manually.
- Self-verify: show the ranked similarities for at least the top 10, confirm the chosen 5th document.
- Clean up any temporary files; leave only deliverable files."

OPENAI_API_KEY="${OPENAI_API_KEY:?Set OPENAI_API_KEY}" \
  "$BINARY" \
  --provider openai --model gpt-5.4 --reasoning-effort medium \
  --max-rounds 50 --no-project-prompts \
  --agent implementer \
  --state-dir "$STATEDIR" --dir "$WORKDIR" \
  -- "$TASK" > "/tmp/impl-repro-log-${LABEL}.txt" 2>&1

RESULT=""
[ -f "$WORKDIR/result.txt" ] && RESULT=$(cat "$WORKDIR/result.txt" | tr -d '\n')

READ_README="no"
python3 -c "
import json, glob
for f in glob.glob('$STATEDIR/sessions/*.transcript.jsonl'):
    for line in open(f):
        entry = json.loads(line)
        if entry.get('kind') != 'entry': continue
        msg = entry.get('turn',{}).get('message',{})
        for part in msg.get('content',[]):
            if isinstance(part,dict) and part.get('kind')=='tool_call':
                tc = part['tool_call']
                if tc.get('name') == 'read_file':
                    args = tc.get('arguments',{})
                    if isinstance(args,str): import json as j; args=j.loads(args)
                    if 'README' in args.get('file_path','').upper():
                        print('yes'); exit()
print('no')
" 2>/dev/null | grep -q "yes" && READ_README="yes"

if [ "$RESULT" = "$EXPECTED" ]; then
  echo "PASS  readme=$READ_README  label=$LABEL"
else
  echo "FAIL  readme=$READ_README  label=$LABEL  got='${RESULT:0:50}'"
fi
