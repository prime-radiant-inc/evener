# serf-doctor-forensics: the read-only forensic inspector produces the *right* numbers where naive grep produces the wrong ones

**What this covers**: the `serf-doctor` binary (`cmd/serf-doctor` over the
`agent/doctor` package) end-to-end against a real on-disk state tree — the data
plane that produced the *wrong numbers* during the observer-provenance work.
Proves the six corrections the tool exists for:

1. **`watches` collapses `watch_send_pending` coalescing** — distinct settled
   deliveries, not the raw pending-line count `grep -c` reports.
2. **`watches --self-loops` returns only watches whose runaway fuse fired**,
   read from the recorded breaker telemetry (`runaway_drops`,
   `max_self_influence_depth` — `agent/doctor/watches.go:46-49`), never
   re-derived from the provenance `Chain` and never from the always-present
   `WatchKeys` stamp (`ContainsWatch` is vacuously true on any recorded
   delivery — `agent/provenance.ContainsWatch`). Bounded self-influence is
   normal under the inform+breaker policy, so it is not a finding. See Step 3.
3. **`transcript --count` separates structural calls from textual mentions** —
   the `delegate_send` "5 mentions / 0 calls" trap.
4. **`locate` resolves the per-session `jobs.jsonl` SUBDIR** (`sessions/<sid>/jobs.jsonl`),
   not a flat `<sid>.jobs.jsonl` beside the transcript.
5. **`jobs` folds the log into per-job state** — status *and* the reason that
   produced it, exit code, output bytes, timings — off settled disk, where the
   2026-07-31 outbox diagnosis could only get them from a live daemon's
   `/status`. See Step 5.
6. **`watches` joins each row with its target job's state** — a watch that
   never fired reads as broken delivery machinery until the row shows the
   target job stopped with zero output and so could never match its condition
   (`agent/doctor/watches.go:33-41,206-221`). See Step 6.

Unit coverage: `agent/doctor/*_test.go`, `cmd/serf-doctor/main_test.go`. This
card proves it against a built binary on a real state-dir shape.

## Pre-state

- Build the binary: `make build-doctor` (or `go build -o /tmp/serf-doctor ./cmd/serf-doctor`).
- A scratch state dir with one session whose `jobs.jsonl` exercises coalescing,
  a dropped delivery, an `evicted` terminal, and a self-loop `Chain`. Build it
  deterministically (the binary's selector `--state-dir <root>` targets it; the
  root is itself the bucket, so sessions sit directly under `sessions/`):

  ```bash
  # SID must be a REAL session id: serf-doctor validates the selector before
  # reading anything (identifier.ValidateSessionID -> 22-char base62 UUIDv7,
  # identifier/uuid.go:12,64-73), so a readable fake like 01SCNDOCTOR... is
  # rejected with `invalid session id` and no step below runs. This literal is
  # a generated, validated id; keep it or generate another.
  #
  # Every watch_registered line below carries generation + owner_session_id +
  # visible_session_id + target + config_hash. FoldWatches SKIPS a registration
  # missing any one of them (agent/internal/jobstore/fold.go:234-237) and says
  # nothing about it — the deliveries still fold from the watch_send events, so
  # a dropped registration costs you the `target=`/`owner=` lines and the whole
  # target-job join in Step 6 while the delivery counts look perfectly fine.
  SCR=$(mktemp -d); SESS="$SCR/sessions"; SID=033z4xc9zDkqiOXWEe1X4l
  mkdir -p "$SESS/$SID"
  printf '{"kind":"header","session_id":"%s"}\n' "$SID" > "$SESS/$SID.transcript.jsonl"
  printf '{"id":"%s"}'                          "$SID" > "$SESS/$SID.meta.json"
  cat > "$SESS/$SID/jobs.jsonl" <<'EOF'
  {"kind":"watch_registered","seq":1,"watch_id":"w1","watch":{"generation":"g1","owner_session_id":"033z4xc9zDkqiOXWEe1X4l","visible_session_id":"033z4xc9zDkqiOXWEe1X4l","target":"job:j1","send_to":"obs","condition":"output_match","config_hash":"h1"}}
  {"kind":"watch_send_pending","seq":2,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":1}}
  {"kind":"watch_send_pending","seq":3,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":2}}
  {"kind":"watch_send_pending","seq":4,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":3}}
  {"kind":"watch_send_delivered","seq":5,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":3,"coalesced_count":3}}
  {"kind":"watch_send_pending","seq":6,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j2"},"delivery_id":"d2","update_seq":1}}
  {"kind":"watch_send_dropped","seq":7,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j2"},"delivery_id":"d2","update_seq":1,"diagnostic_reason":"send_to gone"}}
  {"kind":"watch_send_pending","seq":8,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j3"},"delivery_id":"d3","update_seq":1}}
  {"kind":"watch_send_evicted","seq":9,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j3"},"delivery_id":"d3","update_seq":1}}
  {"kind":"watch_registered","seq":10,"watch_id":"w2","watch":{"generation":"g2","owner_session_id":"033z4xc9zDkqiOXWEe1X4l","visible_session_id":"033z4xc9zDkqiOXWEe1X4l","target":"job:j9","config_hash":"h2"}}
  {"kind":"watch_send_delivered","seq":11,"watch_id":"w2","watch_send":{"key":{"watch_id":"w2"},"delivery_id":"dl","provenance":{"watch_keys":[{"watch_id":"w2","watch_generation":"g2"}],"chain":[{"kind":"watch","watch_id":"w2","delivery_id":"dprior"},{"kind":"watch","watch_id":"w2","delivery_id":"dl"}]}}}
  EOF
  ```

## Steps

1. **`locate` resolves the SUBDIR jobs path.**
   ```bash
   /tmp/serf-doctor locate "$SID" --state-dir "$SCR" --json
   ```
   ASSERT `jobs_path` ends with `sessions/033z4xc9zDkqiOXWEe1X4l/jobs.jsonl`
   (the subdir, NOT a flat `…AAAA.jobs.jsonl`), and `transcript_path` ends with
   `sessions/033z4xc9zDkqiOXWEe1X4l.transcript.jsonl`.

2. **`watches` collapses coalescing — the headline correction.**
   ```bash
   /tmp/serf-doctor watches "$SID" --state-dir "$SCR"
   ```
   ASSERT for `w1`: `5 pending lines` collapse to `3 distinct` deliveries (the
   `j1` key alone is 3 pending → 1 delivered), the line contains `coalescing
   collapsed`, and the three terminals appear as `1 delivered, 1 dropped, 1
   evicted` across `w1`'s three keys (`distinct_deliveries == 3`). Contrast:
   `grep -c watch_send_pending "$SESS/$SID/jobs.jsonl"` == `5`, which is NOT the
   delivery count. The dropped delivery shows `reason=send_to gone`.

3. **`watches --self-loops` returns only fired fuses (breaker telemetry).**
   Under the inform+breaker policy the doctor no longer re-derives self-loop
   verdicts from the Chain — it reads the RECORDED telemetry: per-delivery
   `self_influence_depth` stamps, the per-watch `max_self_influence_depth`,
   and `runaway_drops` (dropped sends whose `diagnostic_reason == "runaway"`).
   Append a runaway drop for `w2` so the fuse registers:
   ```bash
   cat >> "$SESS/$SID/jobs.jsonl" <<'EOF'
  {"kind":"watch_send_dropped","seq":12,"watch_id":"w2","watch_send":{"key":{"watch_id":"w2"},"delivery_id":"dr","diagnostic_reason":"runaway","self_influence_depth":8}}
  EOF
   /tmp/serf-doctor watches "$SID" --state-dir "$SCR" --self-loops --json
   ```
   ASSERT exactly one watch (`w2`) is returned with `runaway_drops == 1` and
   `max_self_influence_depth == 8`. ASSERT `w1` is NOT in the output (no
   runaway drops — `--self-loops` filters on fired fuses, not on carrying its
   own key). The rendered (non-JSON) form shows `breaker: FIRED` for `w2`. An
   empty result serializes as `"watches": []`.

4. **`transcript --count` distinguishes calls from mentions.** Append an
   assistant turn that *names* `delegate_send` without calling it, plus a real
   `read_file` call, then:
   ```bash
   /tmp/serf-doctor transcript "$SID" --count delegate_send --state-dir "$SCR"
   ```
   ASSERT `delegate_send: 0 calls` with a non-zero "textual mention(s)" note —
   the structural invocation count is 0 even though the name appears in text.

5. **`jobs` folds the log into per-job state.** The fixture so far is all
   watches and no jobs; append two job records plus two watches on them, for
   this step and the next:
   ```bash
   cat >> "$SESS/$SID/jobs.jsonl" <<'EOF'
  {"kind":"job_started","seq":13,"job_id":"job_033z4xc9zDkqiOXWEe1X4m","type":"shell","command":"make test","started_at":"2026-07-31T18:00:00Z"}
  {"kind":"job_finished","seq":14,"job_id":"job_033z4xc9zDkqiOXWEe1X4m","status":"completed","exit_code":0,"ended_at":"2026-07-31T18:01:00Z","output_bytes":4096}
  {"kind":"job_started","seq":15,"job_id":"job_033z4xc9zDkqiOXWEe1X4n","type":"shell","command":"npm run dev","started_at":"2026-07-31T18:00:00Z"}
  {"kind":"job_finished","seq":16,"job_id":"job_033z4xc9zDkqiOXWEe1X4n","status":"stopped","reason":"run_timeout","exit_code":-1,"ended_at":"2026-07-31T18:02:00Z","output_bytes":0}
  {"kind":"watch_registered","seq":17,"watch_id":"w3","watch":{"generation":"g3","owner_session_id":"033z4xc9zDkqiOXWEe1X4l","visible_session_id":"033z4xc9zDkqiOXWEe1X4l","target":"job_033z4xc9zDkqiOXWEe1X4n","send_to":"caller","condition":"output_match:ready","config_hash":"h3"}}
  {"kind":"watch_cleared","seq":18,"watch_id":"w3","watch":{"generation":"g3","end_reason":"auto_removed_terminal"}}
  {"kind":"watch_registered","seq":19,"watch_id":"w4","watch":{"generation":"g4","owner_session_id":"033z4xc9zDkqiOXWEe1X4l","visible_session_id":"033z4xc9zDkqiOXWEe1X4l","target":"job_033z4xc9zDkqiOXWEe1X4o","send_to":"caller","condition":"output_match:ready","config_hash":"h4"}}
  EOF
   /tmp/serf-doctor jobs "$SID" --state-dir "$SCR"
   ```
   ASSERT two blocks, in the log's append order: `job
   job_033z4xc9zDkqiOXWEe1X4m  (completed)` with `exit=0  output_bytes=4096`,
   and `job job_033z4xc9zDkqiOXWEe1X4n  (stopped: run_timeout)` with `exit=-1
   output_bytes=0`. The parenthetical is `status: reason`, so the *reason* a
   job stopped travels with the status — `grep -c job_finished` gives 2 and
   tells you neither. ASSERT `--job job_033z4xc9zDkqiOXWEe1X4n` renders that
   job alone, and that `--job job_nope` prints `job job_nope not found in this
   session` and exits 0 — NOT `no jobs recorded`, which would wrongly say the
   session ran nothing (`agent/doctor/jobs.go:209-214`).

6. **`watches` joins each row with its target job's state.**
   ```bash
   /tmp/serf-doctor watches "$SID" --state-dir "$SCR" --watch w3
   /tmp/serf-doctor watches "$SID" --state-dir "$SCR" --watch w4
   ```
   ASSERT `w3` — the watch on the job the run timeout stopped — renders
   `(ended: auto_removed_terminal)`, `deliveries: 0 distinct`, and the joined
   line `target job: status=stopped  reason=run_timeout  exit=-1
   output_bytes=0  ended=2026-07-31T18:02:00Z`. Zero deliveries plus a target
   that produced no output is "the condition could never match", not "delivery
   is broken" — the two readings the join exists to separate. ASSERT `w4`,
   whose target job id appears in no record, renders `target job: not recorded
   in this session's jobs.jsonl` rather than nothing at all. In `--json`, those
   are `target_job` (a full job view) and `target_job_missing: true`; `w1` and
   `w2` carry neither, because a target that is not a `job_` id is a session
   watch, not a missing job.

7. **Real-data spot check (non-deterministic, optional).** Point the binary at
   the live state home and confirm it reads real provenance:
   ```bash
   /tmp/serf-doctor watches <a-real-SID-with-watch_send-events>
   ```
   ASSERT the printed `distinct_deliveries` is ≤ `grep -c watch_send_pending` on
   the same `jobs.jsonl` (coalescing only ever collapses), and that the
   `breaker:` line reports `runaway_drops`/`max_self_influence_depth` from the
   recorded stamps rather than anything walked out of the `Chain`.

## Falsifiable failure modes

- If `watches` reported `5` (or `8`) distinct deliveries on the fixture, it would
  be counting pending lines — the original bug.
- If `--self-loops` returned `w1`, it would be (wrongly) using `WatchKeys` /
  `ContainsWatch` as the verdict.
- If `locate`'s `jobs_path` were `sessions/<sid>.jobs.jsonl`, it would have the
  §8 path wrong and `watches` would read an empty/absent file.
- If `jobs` reported the stopped job as just `stopped` with no `run_timeout`,
  the reason would have been dropped from the fold and the row would no longer
  say *why* the job ended.
- If `w3`'s row carried no `target job:` line, the join regressed and the row
  would again read as broken delivery machinery. If `w4`'s said nothing rather
  than `not recorded`, an absent target would be indistinguishable from a
  session watch — the guess the `TargetJobMissing` flag exists to refuse.
