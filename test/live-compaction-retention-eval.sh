#!/usr/bin/env bash
# Live end-to-end choice/retention eval for the self-compaction note.
# Feature arm: agent calls compact and CHOOSES what to note.
# Baseline arm: agent calls compact with an empty note (summary-only compaction).
# Task: read 7 facts + filler (so facts fall outside preserved-recent), compact,
# then answer 7 questions from post-compaction memory. Score = facts recalled / 7.
set -u
. /tmp/serf-live.env
SERF=/tmp/serf-live
TRIALS=2
RESULTS=/tmp/live_eval_results.txt
: > "$RESULTS"

FACTS='The API rate limit is 5000 requests per hour.
The production database is PostgreSQL 16.
The deploy token is DEPLOY-7Q2X.
The cache TTL is 300 seconds.
The primary AWS region is us-west-2.
The on-call handoff is every Monday.
The monthly error budget is 0.1%.'

# scoring tokens (one distinctive token per fact)
TOKENS=(5000 "PostgreSQL 16" DEPLOY-7Q2X 300 us-west-2 Monday "0.1%")

QUESTIONS='1. What is the API rate limit?
2. What is the production database and version?
3. What is the deploy token?
4. What is the cache TTL?
5. What is the primary AWS region?
6. What day is the on-call handoff?
7. What is the monthly error budget?'

run_trial() {
  local arm="$1" trial="$2"
  local work; work=$(mktemp -d -t serf-live-work-XXXXX)
  # Facts live in the PROMPT, not a re-readable file, so the only way to recall
  # them after compaction is whatever survived (the note, or the summary).
  for i in 1 2 3 4 5 6; do
    { echo "Filler file $i. Irrelevant background notes for context bulk."; \
      yes "lorem ipsum dolor sit amet consectetur adipiscing elit padding line $i" | head -60; } > "$work/filler$i.txt"
  done

  local compact_step
  if [ "$arm" = feature ]; then
    compact_step='Now call the compact tool to compact your context. Use note_to_self to record whatever YOU judge you will need for the final step; choose the content yourself. Set compaction_instructions as you see fit.'
  else
    compact_step='Now call the compact tool with note_to_self set to the empty string "" and compaction_instructions "summarize the conversation so far". Do NOT record the facts in a note.'
  fi

  local prompt="You will do a multi-step task. Do the steps in order.
Step 1: Note these 7 project facts (there is NO file containing them — they are only here in this message):
$FACTS
Step 2: Read filler1.txt, filler2.txt, filler3.txt, filler4.txt, filler5.txt, filler6.txt one at a time.
Step 3: $compact_step
Step 4: Now write a file answers.txt that answers, one answer per line, the following. There is no facts file to consult — answer ONLY from your current working context:
$QUESTIONS
Step 5: reply with the single word DONE."

  # Use the same env as the working smoke: OAuth token + config live under $ISO/$HOMEISO.
  HOME="$HOMEISO" XDG_STATE_HOME="$ISO" "$SERF" --model openai/gpt-5.5 \
    --state-dir "$ISO/serf" --dir "$work" --max-rounds 20 "$prompt" \
    > "$work/run.log" 2>&1

  # score
  local ans="$work/answers.txt" kept=0
  local tokfound=""
  if [ -f "$ans" ]; then
    for tok in "${TOKENS[@]}"; do
      if grep -qiF -- "$tok" "$ans"; then kept=$((kept+1)); tokfound="$tokfound +$tok"; else tokfound="$tokfound -$tok"; fi
    done
  else
    tokfound="(no answers.txt produced)"
  fi
  # did it re-read facts.md after compacting? (fairness check)
  # trials share $ISO/serf; this trial's transcript is the most-recent one.
  local tr; tr=$(ls -t "$ISO"/serf/sessions/*.transcript.jsonl 2>/dev/null | head -1)
  local compacted="no" reread="?" notelen=0
  if [ -n "$tr" ]; then
    grep -q 'NOTE TO SELF' "$tr" && compacted="yes-note" || { grep -q '"compact"' "$tr" && compacted="yes-empty"; }
    reread=$(grep -c 'facts.md' "$tr" 2>/dev/null)
    notelen=$(grep -o '\[NOTE TO SELF\].*\[END NOTE TO SELF\]' "$tr" 2>/dev/null | head -1 | wc -c)
  fi
  echo "ARM=$arm trial=$trial recall=$kept/7 compaction=$compacted facts.md_mentions=$reread note_chars=$notelen | $tokfound" | tee -a "$RESULTS"
}

echo "=== LIVE EVAL: feature (agent-chosen note) vs baseline (empty note) ===" | tee -a "$RESULTS"
for t in $(seq 1 $TRIALS); do run_trial feature "$t"; done
for t in $(seq 1 $TRIALS); do run_trial baseline "$t"; done

echo "=== AGGREGATE ===" | tee -a "$RESULTS"
awk -F'recall=' '/ARM=feature/{split($2,a,"/"); fs+=a[1]; fn++} /ARM=baseline/{split($2,a,"/"); bs+=a[1]; bn++} END{if(fn)printf "feature mean recall: %.2f/7 (n=%d)\n", fs/fn, fn; if(bn)printf "baseline mean recall: %.2f/7 (n=%d)\n", bs/bn, bn}' "$RESULTS" | tee -a "$RESULTS"
echo "DONE_LIVE_EVAL"
