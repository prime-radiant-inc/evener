# E2E Recursion Cards — live run
Branch: job-control-spec | Commit: 997d4cb7 | Model: openai/gpt-5.5
Hub: 127.0.0.1:9180 | Date: 2026-06-13

| Card | Verdict | Session(s) | Owner-scoped result | Notes |
|------|---------|-----------|---------------------|-------|
| 1 recursion-coordinator-fanout | PASS | root 01KV1BVWNWF9MW80906334JMYS / COORD-sess 01KV1BWPE16RD7V53D3B0DQK59 | PASS — no worker is the SUBJECT of any root-rail frame | maxSubagentDepth:2 took: reject says "(2)" |
| 2 recursion-deaf-coordinator-drivedown | PASS | root 01KV1CC027BFJ4TPKDPK9Y1Q2G / COORD-sess 01KV1CCDVPZTN7YDRC4CP3J919 | PASS — only COORD is a root-rail frame subject | maxSubagentDepth:2 took: grant of 1 ACCEPTED |
| 3 job-nested-visibility (amended) | PASS | root 01KV1CH8A38GY9XH15RSVVP15N / child 01KV1CHMPEV2N73XF99ZMFTXB1 | PASS — nested shell job NOT a root-rail frame subject | no recursion opt-in (default) |
| 4 job-stop-and-children (amended) | PASS | root 01KV1CQFSY4J4M94CHS4EMBRD2 / child 01KV1CV1MKNHGFTYFF443KEEDX | PASS — nested shell job NOT a root-rail frame subject | no recursion opt-in (default) |

---

## Card 1 — recursion-coordinator-fanout — PASS

- Root SID `01KV1BVWNWF9MW80906334JMYS`, tmpdir `/tmp/serf-e2e-recfan-dJzWa`, model gpt-5.5, launch_overrides.maxSubagentDepth=2.
- **Grant ceiling (step 2.1): PASS.** `delegation_allowance=2` REJECTED verbatim: `invalid_request: delegation_allowance must be less than your own allowance (2)`. The `(2)` proves maxSubagentDepth:2 opt-in took (root allowance = 2, not 1).
- **Grant succeeds + leaf gate (step 2.2): PASS.** allowance=1 delegate ACCEPTED -> COORD `job_01KV1BWP8XRB7K5V1SDMHX1XYM` (coord session `01KV1BWPE16RD7V53D3B0DQK59`). Coordinator (allowance 1) WAS offered the `delegate` tool; all 3 workers (allowance 0) were offered tools `[apply_patch,communicate,exec_command,grep_files,list_dir,read_file,task_list,web_fetch,write_file]` — `delegate` ABSENT. Hard leaf confirmed via the api_call tools array (not the prose system prompt).
- **Visibility (step 3): PASS.** job_list(include_descendants=true) = exactly 4 rows: COORD depth0 owner=root; 3 workers depth1 owner=coord-session (NOT root), parent=COORD, status=running, each exactly once.
  - workers: `job_01KV1BX2J9D7DP88HE1NXBYDGQ`, `job_01KV1BX5MZN0SMBEEFFVD25CPN`, `job_01KV1BX8MY8A4RZQC895FN9ETG`.
- **Drive-down (step 3/4): PASS.** Coordinator transcript shows POST-IDLE STEERING notification turns (turn[10], turn[13]) carrying ALL 3 worker completions after the coordinator already ended its spawn turn. Coordinator's OWN model woken, no-loss (3/3 delivered).
- **OWNER-SCOPED (step 4, HEADLINE): PASS.** Across all 3 root-rail `<job-notification>` frames (turn[13]=COORD, turn[35]=COORD2), the ONLY frame SUBJECTS (job_id= attribute) are COORD and COORD2 — the root's two OWN direct delegates. NO worker id is ever a frame subject. The 5 worker ids appear ONLY as substrings inside the coordinators' echoed OUTPUT EXCERPTS (their own `communicate` payloads), which is the delegate's own content — not a notification about the worker. Correct semantics keyed on the frame's job_id= subject (per agent/session_lifecycle.go injection/dedup), NOT naive substring.
- **Cascade stop (step 5): PASS (with a card-vs-reality note).** Before: COORD2 `job_01KV1C42FPTPJGDCB46D5MCYTX` depth0 + 2 workers (`job_01KV1C4FTDT9EQGNQE82KHRZV1`,`job_01KV1C4FM1YXXWK3FKBHKGHE75`) running depth1. After job_stop(COORD2, max_wait_ms 8000): BOTH workers `cancelled`/`stopped_by_parent` — cascade reached the subtree, no orphan. NOTE: the job_stop RESULT on COORD2 itself returned `status:"completed", reason:null` (not cancelled) because COORD2 — a fire-and-return coordinator — had ALREADY finished its own turn (shown `completed` in the before-list) by stop time; stop returns the actual terminal status when already terminal (job-control.md line 752). The cascade still felled the live workers. This is a card artifact (it assumed COORD2 still running at stop time), not a product cascade hole.
- **Durable substrate: PASS.** Root jobs.jsonl: 7 job_started + 7 job_finished (2 coords + 5 workers), all 5 worker ids present as forwarded one-hop copies. 7 job_notification_pending/delivered = the parent's drive signals (not rail frames), as expected.

## Card 2 — recursion-deaf-coordinator-drivedown — PASS

- Root SID `01KV1CC027BFJ4TPKDPK9Y1Q2G`, tmpdir `/tmp/serf-e2e-recdeaf-ceejD`, model gpt-5.5, launch_overrides.maxSubagentDepth=2.
- **maxSubagentDepth:2 opt-in took:** the grant of allowance=1 was ACCEPTED -> COORD `job_01KV1CCDP46MJQYEXY86TAGXHS` (coord session `01KV1CCDVPZTN7YDRC4CP3J919`). (Without the opt-in, root allowance=1 and the grant of 1 would be rejected; it was not.)
- **Deaf-coordinator drive-down (claim 1): PASS.** Coordinator transcript: turns[0-7] = spawn 2 workers + COORD_WORKERS communicate, then ENDED its turn. Workers (`job_01KV1CCSX6DE4RFYJ18YYGZ8Z1`, `job_01KV1CD5T7AMC1XRVE7SKVXS85`, 6s sleeps) finished while coordinator IDLE. Turn[8] = POST-IDLE STEERING notification for worker-1 completed; turn[13] = POST-IDLE notification for worker-2 completed. The coordinator's OWN model was driven/woken for its workers. (The root sent nothing during the ~15s window — genuine drive, not a same-turn poll.)
- **Owner-scoped (claim 2): PASS.** The ONLY root-rail notification frame SUBJECT is COORD (`job_01KV1CCDP46MJQYEXY86TAGXHS`). NO worker id is a frame subject. Worker ids + W1_DONE/W2_DONE appear ONLY inside COORD's echoed output excerpt (COORD's own payload), not as a notification about a worker. Root heard only about its own delegate.
- **No-loss/dedupe: PASS.** Each worker delivered EXACTLY ONCE to the coordinator (W1=1 frame, W2=1 frame). Root got exactly one COORD terminal frame.
- **Durable substrate: PASS.** Root jobs.jsonl: 3 job_started + 3 job_finished (COORD + 2 forwarded workers); both worker ids present as one-hop forwarded copies; 3 notification_pending/delivered = drive signals (not rail frames).

## Card 3 — job-nested-visibility (amended) — PASS

- Root SID `01KV1CH8A38GY9XH15RSVVP15N`, tmpdir `/tmp/serf-e2e-jnest-E21CO`, model gpt-5.5, NO recursion opt-in (default launch_overrides) — single delegate starting a nested SHELL job, default depth-1 allowance suffices.
- DELEGATE `job_01KV1CHMGC6DVX0YRASVFTZTKH` (child session `01KV1CHMPEV2N73XF99ZMFTXB1`); NESTED shell job `job_01KV1CHYPY85XB6Q3KY814610E`. Id equality holds (child's NESTED_JOB report == shell tool return == parent's list row) — no namespacing.
- **Arm (a) visibility: PASS.** Step1 (no include_nested) = 1 row, the DELEGATE only (terminal completed); nested job ABSENT. Step2 (include_nested true) adds exactly 1 row: nested job type=shell, status=running, parent_job_id=DELEGATE, owner_session_id=child (NOT root), visible_to_session_id=root. Nested OUTLIVES its delegate (delegate completed, nested running).
- **Arm (b) read: PASS.** job_read_output(nested) = status running, content "NEST_TOKEN_1\n" — read via the parent-visible id, no extra handle.
- **Arm (c) stop: PASS.** job_stop(nested, max_wait_ms 5000) = status "cancelled", reason "stopped_by_parent" — routed to the LIVE owner runtime and confirmed; NOT not_controllable (the cited rule, jobs_nested.go:76-78).
- **Arm (d) post-terminal read: PASS.** After both delegate and nested terminal: job_read_output(nested) = status "cancelled", content "NEST_TOKEN_1\n", and NOT NEST_TOKEN_2 (sleep cut short). Retained output still readable.
- **Owner-scoped (amendment): PASS.** The ONLY root-rail notification frame subject is the DELEGATE (`job_01KV1CHMGC...`, completed). The nested shell job's cancelled terminal is NOT a frame subject — owner-scoped to the child, parent retains on-demand visibility via include_nested. Nested id appears only inside the delegate's echoed output excerpt.
- **Durable forwarding: PASS.** Root jobs.jsonl: 2 job_started + 2 job_finished (delegate + forwarded nested). Nested job_started carries parent_job_id=DELEGATE, owner_session_id=child session.

## Card 4 — job-stop-and-children (amended) — PASS

- Root SID `01KV1CQFSY4J4M94CHS4EMBRD2`, tmpdir `/tmp/serf-e2e-jstop-7Qnw2`, model gpt-5.5, NO recursion opt-in.
- **Turn 1 arms (a)/(b): PASS.** Shell job `job_01KV1CRHDSYWW9SA9S9YR4WDD5`. job_stop(max_wait_ms 5000) result is TERMINAL in the result itself: status "cancelled", reason "stopped_by_parent" (no stop_pending — the wait worked). job_read_output after stop: status "cancelled", reason "stopped_by_parent", content "STOP_RETAIN_TOKEN\n" — output survived the stop.
- **Turn 2 arm (c) include_children cascade: PASS.** DELEGATE `job_01KV1CV1FE4W0KEWNAJATGX7XF` (child session `01KV1CV1MKNHGFTYFF443KEEDX`), nested shell `job_01KV1CVDCK5NX871JQSWZ80BZC`. Before-stop (gate): BOTH live — delegate running, nested shell running with parent=DELEGATE. job_stop(DELEGATE, include_children=true, max_wait_ms 5000) result: delegate "cancelled"/"stopped_by_parent". After-stop list: BOTH terminal — delegate cancelled/stopped_by_parent AND nested shell cancelled/stopped_by_parent (still listed, still parent=DELEGATE). No child left running = no recursion hole.
- **Owner-scoped (amendment): PASS.** Root-rail frame subjects = `job_01KV1CRHDSYWW9SA9S9YR4WDD5` (turn-1 shell, root-owned) and `job_01KV1CV1FE4W0KEWNAJATGX7XF` (DELEGATE, root's own) — both root's OWN jobs. The nested shell `job_01KV1CVDCK5NX...` is NOT a frame subject; its terminal did NOT land on the parent rail (owner-scoped to the child). Parent retains visibility via include_nested / jobs.jsonl.
- **Durable: PASS.** Root jobs.jsonl: 3 job_started + 3 job_finished (turn-1 shell + delegate + forwarded nested); all three job ids present.

---

## Headline summary

- All 4 cards PASS. The OWNER-SCOPED notification rule holds in every card: NO descendant-owned job (coordinator worker or nested shell) is ever the SUBJECT (job_id= attribute) of a notification frame on the ROOT's rail. The root is interrupted ONLY about its OWN direct delegates/jobs.
- Critical methodology note: a worker id appearing as a SUBSTRING inside an ancestor delegate's echoed OUTPUT EXCERPT is NOT a leak — that excerpt is the delegate's own payload (the coordinator chose to print its workers' ids via communicate). The owner-scoped check must be keyed on the frame's job_id= subject attribute (matching agent/session_lifecycle.go injection/dedup), not naive substring. A naive substring check produces FALSE leaks (it flagged all 5 workers in card 1 before correction).
- maxSubagentDepth:2 opt-in confirmed for cards 1-2: card 1 grant-ceiling reject reads "(2)" (proves root allowance=2); card 2 grant of 1 ACCEPTED.
- Drive-down confirmed live (cards 1-2): idle coordinators received post-idle notification turns carrying their workers' completions on the COORDINATOR's own transcript.
- Model: openai/gpt-5.5 throughout. Hub was NEVER killed/restarted/rebuilt. Only spawned sessions were shut down.
- One card-vs-reality artifact (card 1 step 5.4, NOT a product bug): job_stop(COORD2) returned status=completed because the fire-and-return coordinator had already finished its own turn by stop time; the cascade still felled its live workers.



## Orchestrator independent verification (owner-scoping, against primary artifacts)

Not trusting the runner's verdict, the owner-scoped absence was re-checked by
parsing the ROOT session transcripts for `<job-notification>` frame SUBJECTS
(the `job_id=` attribute, NOT substring matches — worker ids legitimately appear
as substrings inside a coordinator's echoed output payload) and mapping each
subject to its owner via `jobs.jsonl`:

- **Card 1 (coordinator-fanout):** root-rail frame subjects = exactly the two
  root-owned coordinator delegate jobs (`…SDMHX1XYM`, `…D5MCYTX`, both
  `owner_session_id = ROOT`). All 5 worker delegate jobs (owned by the two
  coordinator sessions) are ABSENT from the root rail. CLEAN.
- **Card 2 (deaf-coordinator-drivedown):** root-rail subject = only the
  coordinator (`…TAGXHS`). Neither worker is a frame subject. CLEAN.

Owner-scoped notification (Jesse's ruling — an agent is never interrupted about a
subagent's children) holds live, confirmed at the data level. Drive-down
confirmed: idle coordinators received post-idle notification turns carrying their
workers' completions exactly once (no-loss) on their OWN transcripts.
maxSubagentDepth:2 opt-in confirmed both directions. Stop-cascade confirmed.
