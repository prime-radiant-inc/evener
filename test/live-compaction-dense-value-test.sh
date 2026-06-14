#!/usr/bin/env bash
# Dense-regime VALUE test for the "force a note before compaction" idea.
# Does a MANDATED, self-authored note beat a blind summary-only compaction when
# the agent must carry MANY facts through the compaction? (The 7-clean-fact task
# showed no difference; this stresses it with 15 facts + heavy bulk.)
set -u
. /tmp/serf-live.env
SERF=/tmp/serf-live
TRIALS=2
RESULTS=/tmp/live_eval_dense_results.txt
: > "$RESULTS"

FACTS='1. The API rate limit is 5000 requests per hour.
2. The production database is PostgreSQL 16.
3. The deploy token is DEPLOY-7Q2X.
4. The cache TTL is 300 seconds.
5. The primary AWS region is us-west-2.
6. The on-call handoff is every Monday.
7. The monthly error budget is 0.1%.
8. The JWT signing algorithm is RS256.
9. The message queue is Kafka topic orders-v3.
10. The canary rollout is 5% on Friday.
11. The index to add is idx_orders_account_status.
12. The breaking commit is f9e8d7c.
13. The feature flag is rollout_v2.
14. The grpc keepalive must be 10s.
15. The vault path is secret/data/api/prod.'

TOKENS=(5000 "PostgreSQL 16" DEPLOY-7Q2X 300 us-west-2 Monday "0.1%" RS256 "orders-v3" Friday idx_orders_account_status f9e8d7c rollout_v2 10s secret/data/api/prod)

QUESTIONS='1. API rate limit?
2. Production database and version?
3. Deploy token?
4. Cache TTL?
5. Primary AWS region?
6. On-call handoff day?
7. Monthly error budget?
8. JWT signing algorithm?
9. Message queue topic?
10. Canary rollout percent and day?
11. Index to add?
12. Breaking commit?
13. Feature flag?
14. gRPC keepalive interval?
15. Vault path?'

run_trial() {
  local arm="$1" trial="$2"
  local work; work=$(mktemp -d -t serf-live-work-XXXXX)
  # heavy filler so the facts get pushed well outside the preserved window and
  # the compaction has to compress hard
  for i in 1 2 3 4 5 6 7 8; do
    { echo "Filler file $i — irrelevant build/CI/log noise for context bulk."; \
      yes "lorem ipsum dolor sit amet consectetur adipiscing elit padding noise line $i $RANDOM" | head -200; } > "$work/filler$i.txt"
  done

  local compact_step
  if [ "$arm" = mandated ]; then
    compact_step='STOP. Your context is about to overflow and be compacted. You MUST call the compact tool RIGHT NOW. Put into note_to_self everything you will need to answer questions later; you decide what is important. Set compaction_instructions as you see fit.'
  else
    compact_step='Now call the compact tool with note_to_self set to the empty string "" and compaction_instructions "summarize the conversation so far". Do NOT record the facts in a note.'
  fi

  local prompt="You will do a multi-step task. Do the steps in order.
Step 1: Note these 15 project facts (there is NO file containing them — they are only here):
$FACTS
Step 2: Read filler1.txt through filler8.txt one at a time.
Step 3: $compact_step
Step 4: Now write a file answers.txt answering the following, one answer per line. There is no facts file to consult — answer ONLY from your current working context:
$QUESTIONS
Step 5: reply with the single word DONE."

  HOME="$HOMEISO" XDG_STATE_HOME="$ISO" "$SERF" --model openai/gpt-5.5 \
    --state-dir "$ISO/serf" --dir "$work" --max-rounds 24 "$prompt" \
    > "$work/run.log" 2>&1

  local ans="$work/answers.txt" kept=0 missing=""
  if [ -f "$ans" ]; then
    for tok in "${TOKENS[@]}"; do
      if grep -qiF -- "$tok" "$ans"; then kept=$((kept+1)); else missing="$missing $tok"; fi
    done
  else
    missing="(no answers.txt)"
  fi
  local tr; tr=$(ls -t "$ISO"/serf/sessions/*.transcript.jsonl 2>/dev/null | head -1)
  local note=0 comp="?"
  if [ -n "$tr" ]; then
    grep -q 'NOTE TO SELF' "$tr" && comp="note" || comp="empty"
    note=$(grep -o '\[NOTE TO SELF\].*\[END NOTE TO SELF\]' "$tr" 2>/dev/null | head -1 | wc -c)
  fi
  echo "ARM=$arm trial=$trial recall=$kept/15 compaction=$comp note_chars=$note | missing:$missing" | tee -a "$RESULTS"
}

echo "=== DENSE VALUE TEST: mandated self-note vs blind (15 facts) ===" | tee -a "$RESULTS"
for t in $(seq 1 $TRIALS); do run_trial mandated "$t"; done
for t in $(seq 1 $TRIALS); do run_trial blind "$t"; done

echo "=== AGGREGATE ===" | tee -a "$RESULTS"
awk -F'recall=' '/ARM=mandated/{split($2,a,"/"); ms+=a[1]; mn++} /ARM=blind/{split($2,a,"/"); bs+=a[1]; bn++} END{if(mn)printf "mandated-note mean recall: %.2f/15 (n=%d)\n", ms/mn, mn; if(bn)printf "blind mean recall: %.2f/15 (n=%d)\n", bs/bn, bn}' "$RESULTS" | tee -a "$RESULTS"
echo "DONE_DENSE"
