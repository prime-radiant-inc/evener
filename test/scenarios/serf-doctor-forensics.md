# serf-doctor-forensics: the read-only forensic inspector produces the *right* numbers where naive grep produces the wrong ones

**What this covers**: the `serf-doctor` binary (`cmd/serf-doctor` over the
`agent/doctor` package) end-to-end against a real on-disk state tree — the data
plane that produced the *wrong numbers* during the observer-provenance work.
Proves the four corrections the tool exists for:

1. **`watches` collapses `watch_send_pending` coalescing** — distinct settled
   deliveries, not the raw pending-line count `grep -c` reports.
2. **`watches` self-loop verdict reads the provenance `Chain`**, not the
   always-present `WatchKeys` stamp (`ContainsWatch` is vacuously true on any
   recorded delivery — `agent/provenance.ContainsWatch`).
3. **`transcript --count` separates structural calls from textual mentions** —
   the `delegate_send` "5 mentions / 0 calls" trap.
4. **`locate` resolves the per-session `jobs.jsonl` SUBDIR** (`sessions/<sid>/jobs.jsonl`),
   not a flat `<sid>.jobs.jsonl` beside the transcript.

Unit coverage: `agent/doctor/*_test.go`, `cmd/serf-doctor/main_test.go`. This
card proves it against a built binary on a real state-dir shape.

## Pre-state

- Build the binary: `make build-doctor` (or `go build -o /tmp/serf-doctor ./cmd/serf-doctor`).
- A scratch state dir with one session whose `jobs.jsonl` exercises coalescing,
  a dropped delivery, an `evicted` terminal, and a self-loop `Chain`. Build it
  deterministically (the binary's selector `--state-dir <root>` targets it; the
  root is itself the bucket, so sessions sit directly under `sessions/`):

  ```bash
  SCR=$(mktemp -d); SESS="$SCR/sessions"; SID=01SCNDOCTORAAAAAAAAAAAAAAAA
  mkdir -p "$SESS/$SID"
  printf '{"kind":"header","session_id":"%s"}\n' "$SID" > "$SESS/$SID.transcript.jsonl"
  printf '{"id":"%s"}'                          "$SID" > "$SESS/$SID.meta.json"
  cat > "$SESS/$SID/jobs.jsonl" <<'EOF'
  {"kind":"watch_registered","seq":1,"watch_id":"w1","watch":{"generation":"g1","target":"job:j1","send_to":"obs","condition":"output_match"}}
  {"kind":"watch_send_pending","seq":2,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":1}}
  {"kind":"watch_send_pending","seq":3,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":2}}
  {"kind":"watch_send_pending","seq":4,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":3}}
  {"kind":"watch_send_delivered","seq":5,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j1"},"delivery_id":"d1","update_seq":3,"coalesced_count":3}}
  {"kind":"watch_send_pending","seq":6,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j2"},"delivery_id":"d2","update_seq":1}}
  {"kind":"watch_send_dropped","seq":7,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j2"},"delivery_id":"d2","update_seq":1,"diagnostic_reason":"send_to gone"}}
  {"kind":"watch_send_pending","seq":8,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j3"},"delivery_id":"d3","update_seq":1}}
  {"kind":"watch_send_evicted","seq":9,"watch_id":"w1","watch_send":{"key":{"watch_id":"w1","watch_target":"job:j3"},"delivery_id":"d3","update_seq":1}}
  {"kind":"watch_registered","seq":10,"watch_id":"w2","watch":{"generation":"g2","target":"job:j9"}}
  {"kind":"watch_send_delivered","seq":11,"watch_id":"w2","watch_send":{"key":{"watch_id":"w2"},"delivery_id":"dl","provenance":{"watch_keys":[{"watch_id":"w2","watch_generation":"g2"}],"chain":[{"kind":"watch","watch_id":"w2","delivery_id":"dprior"},{"kind":"watch","watch_id":"w2","delivery_id":"dl"}]}}}
  EOF
  ```

## Steps

1. **`locate` resolves the SUBDIR jobs path.**
   ```bash
   /tmp/serf-doctor locate "$SID" --state-dir "$SCR" --json
   ```
   ASSERT `jobs_path` ends with `sessions/01SCNDOCTORAAAAAAAAAAAAAAAA/jobs.jsonl`
   (the subdir, NOT a flat `…AAAA.jobs.jsonl`), and `transcript_path` ends with
   `sessions/01SCNDOCTORAAAAAAAAAAAAAAAA.transcript.jsonl`.

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

5. **Real-data spot check (non-deterministic, optional).** Point the binary at
   the live state home and confirm it reads real provenance:
   ```bash
   /tmp/serf-doctor watches <a-real-SID-with-watch_send-events>
   ```
   ASSERT the printed `distinct_deliveries` is ≤ `grep -c watch_send_pending` on
   the same `jobs.jsonl` (coalescing only ever collapses), and that any
   `self-loop` verdict is read from the `Chain`.

## Falsifiable failure modes

- If `watches` reported `5` (or `8`) distinct deliveries on the fixture, it would
  be counting pending lines — the original bug.
- If `--self-loops` returned `w1`, it would be (wrongly) using `WatchKeys` /
  `ContainsWatch` as the verdict.
- If `locate`'s `jobs_path` were `sessions/<sid>.jobs.jsonl`, it would have the
  §8 path wrong and `watches` would read an empty/absent file.
