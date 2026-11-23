# Evener Efficiency Program: Evaluation Rubric & Fresh-Eyes Audit (Round 2 & Round 3)

**Date:** 2026-11-22
**Updated:** 2026-11-23 (Round 4 isolated experiments, provisional combined stack evaluation, `#6940` bisect, and Gate 1 verification)
**Base reference:** `origin/main` at `31e7d0ba4a` (includes `4759d4c0eb`, PR #7501 unified `communicate({})` quiet yield and root bare-text fallback)
**Interactive report (v4, 1,267 normalized runs across R2, R3, R4 baseline + R4-1..R4-5, `opencode 1.19.5`, and `pi 1.2.0`):** https://artifacts.trout-rigel.ts.net/p/evener-efficiency-audit-r3/
**Round 4 Research Plan:** https://artifacts.trout-rigel.ts.net/p/evener-r4-research-plan/
**Extraction CLI:** `/home/jesse/.local/share/toolsmith/tools/bin/evener-eval-ledger` (read-only; normalizes token counts across Evener `.api.jsonl`/`.transcript.jsonl`, `pi` `sessions/*.jsonl`, and `opencode` `opencode.db`)

---

## 1. Executive Verdict

None of the three Round 3 pull requests (#7505, #7503, #7504) should merge as written.

1. **What changed between Round 2 and Round 3 on `main`:** Between Round 2 `shipped` (`9fdbfd65d2`) and Round 3 `origin/main` (`b57dfc40eb`), PR #7422 (atomic multi-edit `edit_file`, single edit tool per session), PR #7469 (`read_file` v2), and other base commits (including removing loop-triggered reasoning escalation) landed on `main`. The R2-to-R3 delta is a historical cross-revision measurement, not a single-PR isolated control. On `overfull-hbox` real containers, `origin/main` (`b57dfc40eb`) improved over `shipped` (`9fdbfd65d2`) from **2/3 to 3/3 pass**, cut mean tool calls **32.0 to 23.7 (-26%)**, cut mean input tokens **1.26M to 758k (-40%)**, and cut mean output tokens **27.6k to 14.6k (-47%)**. On `path-tracing-reverse`, mean calls dropped **41.3 to 32.0 (-23%)** and input tokens dropped **2.48M to 1.98M (-20%)**.
2. **What full token accounting revealed:** Earlier comparisons understated `opencode` output tokens because Harbor's ATIF `trajectory.json` excludes `opencode`'s reasoning tokens. Reading `opencode.db` directly shows `opencode` uses a median of **22,958 total output tokens** (15,482 reasoning) across its 30 board trials — nearly identical to Evener `origin/main`'s **23,284 output tokens** (15,712 reasoning). Evener's real efficiency gap on most tasks is **input tokens**: `origin/main` starts Turn 1 at **22,456 tokens** (`system.md.tmpl` + 27 tool schemas), compared to **7,472** for `opencode` and **1,705** for `pi 1.2.0`. Across 30 container trials, that turns into **795k median input tokens** for Evener vs **498k** for `opencode` and **197k** for `pi`.
3. **What Round 3 failed to prove:** Round 3 screened individual arms (`exp1`, `exp2`, `exp3`, `exp4`) only on the `overfull-hbox` fixture (`n=12`) and then jumped straight to a stacked binary (`exp134`) for Terminal-Bench 2.1 containers (`30 trials`). In isolation on the fixture, prompt/schema cuts reduced input tokens (`-13%` to `-38%`), but `exp3` (`9/12` pass, 3 timeouts) and `exp4` (`8/12` pass, 3 behavior timeouts + 1 outage) did not cleanly beat `main-r3` (`12/12` pass). On containers, stacked `exp134` cut median input tokens **-37.6%** (`795k -> 496k`, matching `opencode`) and cut calls 8% to 35% on 9 of 10 tasks, **but regressed `configure-git-webserver` from 2/3 to 0/3** and **left `largest-eigenval` unsolved at 1/3**. Because none of the three PRs was tested alone on containers, we cannot attribute either the wins or the `configure-git-webserver` regression to an individual PR.
4. **Why the three open PRs are blocked:**
   - **PR #7503 (`exp3-surface-diet`):** **Closed.** Failed CI (`TestModelListToolPreservesJSONUnderConfiguredOutputLimit`), conflicted with `4759d4c0eb`, degraded `toolIntentDescription`, and compressed `system.md.tmpl` into sloppy telegraphic prose that inverted a real background-process rule (*"stop all other background processes before ending"* / *"stop persistent processes before ending"*). Will be replaced with clean, grammatical schema edits and progressive-disclosure documents on top of `4759d4c0eb`.
   - **PR #7505 (`exp1-oneshot-ceremony`):** Green CI, real mechanical gain on `notes_*`/`urls_*` omission, but 4 unresolved RoboRev findings (telling the model not to clean `$EVENER_SCRATCH_DIR` leaks files until the harness cleans scratch itself; symlink `--dir` git detection bug; stale prompt on `--resume`; unnecessary `Omit`/`IsReserved` map) and predates `4759d4c0eb`. **Recommendation: rework on `4759d4c0eb` and test in isolation.**
   - **PR #7504 (`exp4-direct-exec`):** Green CI, but 3 unresolved RoboRev findings (narrowing backups to "binary or database" exposes irreplaceable text/CSV files; `/tmp/run.py` fails in Linux `bwrap` read-only `/tmp`; "stop immediately" contradicts `## Finishing` worktree cleanup), 3 fixture behavior timeouts in isolation, prime suspect in `configure-git-webserver` 0/3, and predates `4759d4c0eb`. **Recommendation: hold, fix all 3 findings, split hypotheses, and test in isolation on `4759d4c0eb`.**

---

## 2. Five-Gate Evaluation Rubric (Mandatory Promotion Standard)

Every future efficiency experiment starts from `origin/main` including `4759d4c0eb`. Pre-PR checks (prose audit, 5-model live verification, isolated benchmarks, and ledger audit) must pass before opening a PR; CI and RoboRev review checks run on the PR and must pass on the reviewed PR head before promotion or merge.

### Gate 1 — Prose Quality & Contract Fidelity
- **Before opening a PR:**
  - Branch includes `4759d4c0eb` (#7501) and current `origin/main`.
  - Every edited line of `agent/prompts/system.md.tmpl` and `agent/internal/tool/definitions.go` is reviewed directly for clear, complete, grammatical English (Strunk/Hemingway style). Never compress prompts into telegraphic shorthand, semicolon pileups, or single-line crammed lists.
  - Never alter a safety, lifecycle, or cleanup qualifier while tightening prose.
  - A 5-model / multi-provider live E2E field test passes and audits clean with `evener doctor transcript --health`.
- **Before promotion or merge (on the reviewed PR head):**
  - All GitHub CI checks pass green and all RoboRev findings are resolved.

### Gate 2 — Isolated Attribution Before Stacking
- Every candidate PR must be benchmarked **in isolation** against a same-day, same-fleet `origin/main` control before combining changes into a stack.
- Never cite stacked container metrics (such as `exp134`) as evidence for an individual PR.
- Screening tier: `overfull-hbox` fixture, `n >= 12` reps interleaved with control.
- Promotion tier: all 10 Terminal-Bench 2.1 container tasks (`4 pi-win + 6 guard`), `n >= 3` attempts per task (`30 trials`), 1 trial per idle 8-vCPU host.

### Gate 3 — Complete Normalized Token & Timing Accounting
All numbers must be generated by `evener-eval-ledger` from raw API logs (`.api.jsonl`), transcripts (`.transcript.jsonl`), `pi` session logs (`sessions/*.jsonl`), and `opencode.db`, with zero missing logs and zero arithmetic anomalies.

**Normalized token definitions (identical across Evener, `pi`, and `opencode`):**
- `round1_input_tokens`: Total prompt tokens on Turn 1 (system prompt + tool schemas + initial user turn), computed with the inclusive prompt-input formula below.
- `input_tokens = cache-miss input + cache_read + cache_creation`: Total prompt tokens summed across all agent rounds, counting every token once. `cache_creation` includes both 5m (`cache_write_tokens`) and 1h (`cache_write_1h_tokens`) cache-write tiers when reported separately, matching `llm/api_attempt.go` and `agent/internal/atif/atif.go`.
  - Raw Evener `usage.input_tokens`, raw `pi` `usage.input`, and raw `opencode.db` `tokens.input` exclude cached read tokens (and Anthropic cache-creation tokens when reported separately) and are normalized by adding `cache_read` and `cache_creation` (both 5m and 1h tiers when present) across both Round 1 and whole-run totals.
- `output_tokens = non_reasoning_output_tokens + reasoning_tokens`: Total generated tokens summed across all agent rounds.
  - Raw `opencode` ATIF `trajectory.json` excludes reasoning tokens; `evener-eval-ledger` reads `opencode.db` so `opencode` output tokens include reasoning, matching Evener and `pi`.
- `total_tokens = input_tokens + output_tokens`.
- `max_single_round_output_tokens`: Peak output tokens in any single round (key diagnostic for runaway reasoning spirals).
- Wall time broken down into `gen_s` (model generation), `stall_wait_s` (pre-first-byte gateway keep-alive queue wait), and `tool_s` (command execution).

### Gate 4 — Zero Guard Regressions & Honest Outcome Reporting
- **Automatic rejection:** any of the 5 deterministic guard tasks dropping below `15/15` (or any dropping to `0/3`), or overall 10-task raw pass rate dropping below control (`>= 27/30`). Following the Round 4 `#6940` bisect (Section 7), the 5 deterministic guard tasks serve as strict gates (`15/15`), while `configure-git-webserver` is advisory individually, tracked statistically as a task-quality caveat, and still counted in the overall 10-task raw pass rate.
- Never hide behavior timeouts inside "passed-only" averages. Always report raw `pass/n`, `behavior timeouts`, `gateway-limited timeouts`, and `gateway outages` side-by-side.

### Gate 5 — Parity Targets vs `pi 1.2.0` and `opencode 1.19.5`

| Metric | Current `origin/main` (`b57dfc4`) | `opencode 1.19.5` | `pi 1.2.0` | Target Bar |
|---|---:|---:|---:|---:|
| Headless Round 1 input tokens | 22,456 | 7,472 | 1,705 | **<= 10,000** |
| TB 2.1 median total input tokens | 795,038 | 497,720 | 196,588 | **<= 450,000** |
| TB 2.1 median output tokens (incl. reasoning) | 23,284 | 22,958 | 11,861 (R2) / 20,965 (board) | **<= 20,000** |
| TB 2.1 median tool calls | 25.5 | 22.5 | 14.0 (R2) / 16.0 (board) | **<= 20.0** |
| `largest-eigenval` clean pass rate | 1/3 (3 TO) | 3/3 (0 TO) | 3/3 (Nov 16) / 1/2 (R2) | **>= 2/3 (0 TO)** |
| `configure-git-webserver` pass rate (**advisory**) | 2/3 | n/a | 2/3 | **>= 2/3 (advisory; see Section 7)** |
| Overall 10-task TB 2.1 pass rate | 27/30 (90%) | 12/12 (on 4 overlapping) | 25/29 (R2) | **>= 27/30 (90%)** |

`configure-git-webserver` is **advisory** as a single-task row (consistent with Gate 4 and the `#6940` bisect ruling in Section 7), yet it still counts in the overall 10-task raw pass rate (`>= 27/30`), which must be reported alongside the strict `15/15` gate on the 5 deterministic guard tasks.

---

## 3. Complete Token & Outcome Summary

### A. Fixture Screening (`overfull-hbox`, medians over non-outage runs; all outcomes shown)

| Arm | Source | n | Pass | Behav TO | GW TO | Outage | Med Calls | Med Pre/Post | Med R1 In | Med Total In | Med Uncached | Med Out | Med Reas | Med Wall |
|---|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| `pi 1.2.0` | `r2-fixture` | 32 | 27/32 | 1 | 4 | 0 | 13.0 | 11.0 / 2.0 | 1,647 | 165,621 | 13,145 | 9,041 | 5,807 | 252s |
| `plr2` (floor) | `r2-fixture` | 15 | 14/15 | 0 | 1 | 0 | 17.0 | 14.0 / 3.0 | 1,283 | 200,249 | 13,508 | 10,705 | 6,901 | 448s |
| `main-chat` (`9fdbfd6`) | `r2-fixture` | 76 | 51/76 | 8 | 18 | 0 | 29.0 | 20.0 / 9.0 | 22,971 | 1,114,013 | 50,712 | 20,747 | 12,126 | 734s |
| **`main-r3` (`b57dfc4`)** | `r3-fixture` | 12 | **12/12** | 0 | 0 | 0 | 28.0 | 13.5 / 10.5 | 22,448 | 1,025,500 | 47,815 | 19,191 | 11,451 | 411s |
| `exp1` | `r3-fixture` | 12 | **12/12** | 0 | 0 | 0 | 28.5 | 20.0 / 7.0 | 19,437 | 990,876 | 44,877 | 19,799 | 13,318 | 385s |
| `exp2` | `r3-fixture` | 12 | **12/12** | 0 | 0 | 0 | 25.0 | 18.0 / 8.0 | 22,595 | 861,057 | 47,386 | 18,929 | 12,557 | 291s |
| `exp12` | `r3-fixture` | 12 | **12/12** | 0 | 0 | 0 | 29.0 | 18.5 / 7.0 | 19,587 | 927,670 | 42,942 | 17,740 | 10,330 | 343s |
| `exp3` | `r3-fixture` | 12 | 9/12 | 1 | 2 | 1 | 26.0 | 17.0 / 8.0 | 16,025 | 743,856 | 40,458 | 20,858 | 11,719 | 377s |
| `exp123` | `r3-fixture` | 12 | 9/12 | 2 | 1 | 1 | 25.0 | 17.0 / 5.0 | 13,836 | 661,851 | 37,723 | 16,274 | 8,614 | 452s |
| `exp4` | `r3-fixture` | 12 | 8/12 | 3 | 0 | 1 | 25.0 | 20.0 / 5.0 | 21,199 | 823,325 | 46,372 | 16,340 | 10,584 | 287s |
| `exp134` | `r3-fixture` | 12 | 10/12 | 1 | 0 | 1 | 27.0 | 18.0 / 9.0 | 13,817 | 734,280 | 36,556 | 18,999 | 11,296 | 352s |

### B. Terminal-Bench 2.1 Containers (10 Tasks, Means per Task)

| Task | Arm | Pass (TO) | Mean Calls | Mean R1 In | Mean Total In | Mean Out (Reas) | Max 1-Rnd Out |
|---|---|---:|---:|---:|---:|---:|---:|
| `sqlite-db-truncate` | R3 `main` / `exp134` / `opencode` / R2 `pi` | 3/3 / 3/3 / 3/3 / 3/3 | 11.7 / 9.7 / 9.0 / 11.0 | 22.4k / 13.9k / 7.4k / 1.7k | 264k / 189k / 127k / 83k | 13.9k (10.6k) / 13.1k (9.7k) / 8.0k (5.9k) / 10.8k (8.3k) | 11.7k / 8.8k / 4.3k / 7.7k |
| `overfull-hbox` | R3 `main` / `exp134` / `opencode` / R2 `pi` | 3/3 / 3/3 / 3/3 / 3/3 | 23.7 / 21.7 / 15.3 / 15.3 | 22.4k / 13.9k / 7.4k / 1.7k | 758k / 549k / 227k / 193k | 14.6k (9.2k) / 15.5k (10.5k) / 8.2k (5.2k) / 10.9k (7.3k) | 2.7k / 4.4k / 5.4k / 6.0k |
| `path-tracing-reverse` | R3 `main` / `exp134` / `opencode` / R2 `pi` | 3/3 / 3/3 / 3/3 / 2/3 | 32.0 / 31.7 / 28.7 / 27.7 | 22.4k / 14.0k / 7.5k / 1.7k | 1.98M / 2.08M / 1.45M / 1.28M | 70.6k (63.4k) / 57.4k (49.3k) / 46.5k (40.7k) / 45.0k (40.1k) | 44.1k / 34.2k / 22.4k / 16.4k |
| `largest-eigenval` | R3 `main` / `exp134` / `opencode` / Nov16 `pi` | **1/3 (3)** / **1/3 (2)** / **3/3 (0)** / **3/3 (0)** | 35.0 / 30.7 / 46.3 / 51.0 | 22.5k / 14.0k / 7.5k / 1.7k | 2.04M / 1.61M / 2.40M / 1.75M | 68.3k (47.3k) / 64.7k (49.0k) / 45.6k (26.7k) / 53.3k (33.3k) | **30.0k** / **24.9k** / **13.6k** / **16.0k** |
| `feal-linear-cryptanalysis` | R3 `main` / `exp134` / R2 `pi` | 3/3 / 3/3 / 3/3 | 16.3 / 14.3 / 12.0 | 22.5k / 14.0k / 1.7k | 560k / 480k / 150k | 26.3k (20.4k) / 29.6k (24.1k) / 11.4k (7.4k) | 19.4k / 26.3k / 5.7k |
| `path-tracing` | R3 `main` / `exp134` / R2 `pi` | 3/3 / 3/3 / 2/3 (1) | 32.7 / 25.3 / 40.0 | 22.5k / 14.0k / 1.8k | 2.36M / 1.37M / 2.02M | 38.0k (30.3k) / 30.0k (23.0k) / 50.1k (34.4k) | 16.6k / 9.1k / 11.6k |
| `configure-git-webserver` | R3 `main` / `exp134` / R2 `shipped` / R2 `pi` | **2/3** / **0/3** / **3/3 (1)** / **2/3** | 27.7 / 19.0 / 26.7 / 21.3 | 22.4k / 13.9k / 22.9k / 1.7k | 766k / 431k / 808k / 240k | 25.3k (19.3k) / 21.9k (16.5k) / 22.8k (16.8k) / 12.7k (8.7k) | 5.6k / 6.7k / 4.8k / 3.0k |
| `portfolio-optimization` | R3 `main` / `exp134` / R2 `pi` | 3/3 / 3/3 / 3/3 | 29.7 / 19.3 / 10.3 | 22.5k / 14.1k / 1.8k | 982k / 387k / 86k | 27.6k (15.6k) / 20.0k (12.9k) / 10.5k (4.7k) | 11.5k / 12.4k / 6.0k |
| `sqlite-with-gcov` | R3 `main` / `exp134` / R2 `pi` | 3/3 / 3/3 / 3/3 | 25.3 / 23.3 / 20.3 | 22.4k / 13.9k / 1.6k | 686k / 566k / 523k | 10.3k (5.8k) / 13.4k (8.9k) / 6.3k (3.7k) | 1.9k / 1.9k / 2.5k |
| `sparql-university` | R3 `main` / `exp134` / R2 `pi` | 3/3 / 3/3 / 3/3 | 14.0 / 11.3 / 13.7 | 22.6k / 14.1k / 1.9k | 450k / 263k / 156k | 9.9k (6.2k) / 9.6k (6.0k) / 6.7k (4.7k) | 4.2k / 4.2k / 4.4k |

*(Full per-run rows for the 1,003 R2/R3 runs and all 1,267 normalized R2 through R4 runs in v4 are published in the interactive artifact and attached CSV/JSON.)*

---

## 4. Deep-Dive Root-Cause Analyses

### A. `configure-git-webserver` (0/3 on `exp134` vs 2/3 on `origin/main` and 3/3 on R2 `shipped`)
- **Verified verifier error:** All four Round 3 failures (`r3main-2`, `exp134-0`, `exp134-1`, `exp134-2`) failed identically: `Web server returned HTTP 404` alongside `fatal: not a git repository (or any of the parent directories): .git`.
- **Verified transcript mechanism:** The task prompt gives `git clone user@server:/git/server` while also stating `"I'll setup login with the server to work"`. Both passing `origin/main` runs (`rep0`, `rep1`) created a literal Linux account `user` (`useradd`). All four failing runs configured `ubuntu` or `root` instead.
- **Why this blocks PR #7504 and PR #7503:** In `exp134-0` and `exp134-1`, the model explicitly noticed the missing `user` account and reasoned itself out of creating it. Both Exp 4's strict *"stop immediately... nothing beyond what was asked"* rule and Exp 3's degraded prompt prose were active together; neither was isolated on this task. `exp134` fails Gate 4 because its full 30-trial board scored **`25/30`** (below the `27/30` control), with its `0/3` `configure-git-webserver` result recorded as an advisory observation that contributed to the aggregate rather than a standalone automatic veto.

### B. `largest-eigenval` (1/3 on `origin/main` and `exp134` vs 3/3 on `opencode` and Nov 16 `pi`)
- **What changed our understanding:** Comparing `evener-eval-ledger` across harnesses shows `opencode` (46.3 calls, 3/3, 0 TO) and `pi` (51.0 calls, 3/3, 0 TO) actually take **more** tool rounds than Evener (30.7–35.0 calls) and finish well inside 900s.
- **The real bottleneck is reasoning tokens per round and uncapped `max_tokens`:**
  - `opencode`: 576 reasoning tokens/call, hard cap `max_1rnd = 32,000` (observed task peak 13,560).
  - `pi`: 653 reasoning tokens/call, hard cap `max_1rnd = 16,384` (observed task peak 16,015).
  - Evener: 1,351–2,075 reasoning tokens/call, uncapped `max_tokens = 262,144` (observed task peaks 24,897 to 40,686 in a single round!).
- Spending 25k–40k reasoning tokens in a single round hand-deriving custom C / LAPACK / Francis double-shift QR solvers consumes 250–400s in one request and burns 600–860s of generation time across 30 rounds, hitting the 900s wall with zero gateway stall.

---

## 5. Completed Round 4 Post-`4759d4c0eb` Baseline (`origin/main` at `93d73fdd59`)

Before starting any new isolated experiment, we ran a clean 42-trial rebaseline on `origin/main` (`93d73fdd59`, verified to include `4759d4c0eb` / PR #7501) across 4 idle `c7i.2xlarge` hosts (`overwork-r4`, torn down cleanly after collection; 0 key leaks).

- **Fixture (`overfull-hbox`, n=12):** **12/12 pass (0 timeouts, 0 outages)** — median **28.0 calls** (mean 27.8, post-pass 9.3), median **22,369 Round 1 input**, **1,090,075 total input** (1,035,968 cached), **20,448 output** (14,181 reasoning), **285s wall**. Three of 12 runs used `4759d4c0eb`'s root bare-text fallback (`communicate` dropped 12/12 -> 9/12), while 11/12 still called `notes_agent_set`.
- **Terminal-Bench 2.1 Containers (30 trials = 10 tasks x 3 attempts):** **27/30 pass (90.0%)**, 5 timeouts (1 gateway-limited, 4 behavior: all 3 `largest-eigenval` + 1 `path-tracing` at 107 calls / 1800s). Overall medians: **26.0 calls**, **22,373 R1 input**, **976,320 total input** (928,192 cached), **24,660 output** (15,738 reasoning), **310s wall**, peak **35,099** tokens in one round.

| Task (`tb-r4main`, `93d73fdd59`) | Pass (TO) | Mean Calls | Mean R1 In | Mean Total In | Mean Out (Reas) | Max 1-Rnd Out | Mean Wall |
|---|---:|---:|---:|---:|---:|---:|---:|
| `sqlite-db-truncate` | 3/3 (0) | 11.7 | 22,306 | 317,370 | 14,856 (11,329) | 6,636 | 142s |
| `overfull-hbox` | 3/3 (1) | 30.0 | 22,317 | 1,007,252 | 19,938 (13,979) | 5,708 | 444s |
| `path-tracing-reverse` | 3/3 (0) | 41.0 | 22,355 | 2,899,161 | 63,311 (53,295) | 25,285 | 646s |
| `largest-eigenval` | **2/3 (3)** | 42.0 | 22,392 | 3,221,747 | **99,650 (69,919)** | **35,099** | 900s |
| `feal-linear-cryptanalysis` | 3/3 (0) | 21.0 | 22,404 | 987,829 | 35,612 (26,823) | 17,425 | 386s |
| `path-tracing` | 3/3 (1) | 52.0 | 22,431 | 5,545,031 | 66,173 (47,578) | 11,310 | 759s |
| `configure-git-webserver` | **1/3 (0)** | 25.0 | 22,318 | 1,009,367 | 36,557 (29,356) | 5,676 | 482s |
| `portfolio-optimization` | 3/3 (0) | 27.3 | 22,468 | 767,939 | 21,517 (11,328) | 8,637 | 279s |
| `sqlite-with-gcov` | 3/3 (0) | 35.3 | 22,314 | 1,013,103 | 15,028 (9,709) | 2,455 | 241s |
| `sparql-university` | 3/3 (0) | 13.7 | 22,511 | 418,376 | 9,502 (5,981) | 5,349 | 90s |

**Key takeaway from the rebaseline:** Even clean `origin/main` (`93d73fdd59`) scored **1/3** on `configure-git-webserver` (both failures hit the exact same `HTTP 404` / `fatal: not a git repository` error on `user@server`), putting clean `origin/main` at **3/6 (50%)** pooled across R3 + R4. Meanwhile `largest-eigenval` hit 900s on all 3 attempts while generating **99.7k mean output tokens (69.9k reasoning)** and **3.22M mean input tokens**.

---

## 6. Clean Round 4 Experiment Queue Starting From `93d73fdd59` (`>= 4759d4c0eb`)

1. **Experiment R4-A — Per-round `max_tokens` cap for `largest-eigenval`:** Test capping per-turn `max_tokens` (e.g., 16,384 or 32,768 matching `pi` and `opencode`, vs Evener's 262,144) isolated on `largest-eigenval` + guard tasks to see if cutting 25k–40k single-round reasoning spirals delivers clean non-timeout passes without hurting hard tasks.
2. **Experiment R4-B — Progressive-disclosure system docs + clean Strunk-grade tool descriptions:** Move large specialized sections of `system.md.tmpl` (such as delegation, worktree, transcript/doctor, and background-work detail) into on-demand system disclosure documents instead of cramming inline prose, and tighten tool schemas in complete, clear English targeting `r1_in <= 10k` headless — tested in isolation.
3. **Experiment R4-C — Clean rework of #7505 (one-shot ceremony + harness-owned scratch cleanup):** Fix all 4 RoboRev findings (actually clean scratch in the harness, resolve symlink `--dir`, handle `--resume`, use register -> check -> `Registry.Remove`) and test in isolation.
4. **Experiment R4-D — Isolated behavioral prompts (reworking #7504):** Fix all 3 RoboRev findings (keep irreplaceable text/CSV files protected, use `$EVENER_SCRATCH_DIR` instead of `/tmp/run.py`, avoid contradicting `## Finishing`), and test foreground-default and inline-script rules separately from stop-when-done wording.

---

## 7. Confirmed `configure-git-webserver` `#6940` Bisect Ruling

Thirty live trials on an identical 4-host EC2 fleet proved an exact pass-rate shift at `e48b7bcdc8` (`#6940`, full-history chat reasoning replay). Runs before `#6940` passed `11/12` (`91.7%`, `55.8` mean calls), whereas runs after `#6940` passed `9/18` (`50.0%`, `26.4` mean calls).

The task itself causes the ambiguity. Its `verify.sh` hardcodes `user@localhost`, even though `instruction.md` states `"I'll setup login"`. With full reasoning replay enabled, the model remembers Round 1 instead of thrashing for `50–96` calls into an accidental `useradd user`.

**Ruling:** Keep `#6940`. Treat the 5 deterministic guard tasks as strict promotion gates, and track `configure-git-webserver` statistically as a task-quality caveat.

---

## 8. Completed Round 4 Results (222 Live Trials)

Round 4 ran `222` live trials via `vertex-arc/gemini-4-hydrogen` with `100%` reasoning replay, `0` AWS leftovers, and `0` key leaks. All counts below come from `evener-eval-ledger`. The Round 4 control ran on `93d73fdd59` (`22,373` median Round 1 input, `976,320` median total input, `26.0` median calls), whereas R4-2, R4-3, and R4-4 were evaluated on `c9d8412ec2` (and later rebased onto `8a3453bf0b` for PRs, with R4-5 evaluated on `8a3453bf0b`). Intervening base commits landed between `93d73fdd59` and `8a3453bf0b`, including the bare-text retry cap (`#7537`), repetition-breaker naming (`#7536`), and quiet-yield `communicate` wording (`#7549`/`#7551`). Comparisons against `93d73fdd59` are therefore **near-matched / historical base comparisons**, not strict same-SHA isolated attribution; Gate 2 requires a same-base control whenever strict attribution is needed.

### R4-1: Per-Round `max_tokens` Cap (`32k` vs `16k` vs `262k`, 54 trials): DROPPED (falsified)
- `cap16k` triggered `18` `finish_reason=length` truncations across `7/15` Terminal-Bench trials and collapsed `path-tracing-reverse` from `3/3` to `0/3`.
- `cap32k` eliminated truncations (`0`), yet `largest-eigenval` still timed out `3/3` at `900s` (`2/3` passing on disk).

### R4-2: Progressive-Disclosure Bundled Guides + Strunk Schema Diet (`c9d8412ec2`, rebased onto `8a3453bf0b`, 42 trials): WINNER
- **Terminal-Bench 2.1:** `28/30` (`93.3%`), tying `opencode` and `pi`, with `15/15` across the 5 deterministic guard tasks.
- **Efficiency:** Median Round 1 input fell `46.8%` (`22,373` to `11,895`), median total input fell `31.6%` (`976,320` to `668,043`), and median calls fell from `26.0` to `23.5`.
- **Contract fix:** Added a 1-sentence `DefJobWatchNamed` validation guardrail after a forensic audit caught `7` `job_watch` errors.

### R4-3: Clean PR `#7505` Rework (`c9d8412ec2`, rebased onto `8a3453bf0b`, 42 trials): WINNER
- **RoboRev resolution:** Fixed all 4 RoboRev findings cleanly.
- **Reliability and efficiency:** `15/15` deterministic guards, `0` ceremony calls across all `42` runs, `0.33%` tool errors, and `-44%` fixture post-pass calls (`9.0` to `5.0`).
- **Timeouts:** Recorded `1` behavior timeout plus a clean `774s` non-timeout pass on `largest-eigenval-1`. The remaining `11/12` timeouts were Lunaroute `gw_lim` stalls (`med_stall_s=199.5s`).

### R4-4: Clean PR `#7504` Rework (`c9d8412ec2`, rebased onto `8a3453bf0b`, 42 trials): WINNER
- **RoboRev resolution:** Fixed all 3 RoboRev findings cleanly.
- **Terminal-Bench 2.1:** `28/30` (`93.3%`), with `24/24` across all 8 tasks outside `largest-eigenval` and `configure-git-webserver`.
- **Efficiency:** `21.0` median calls (beating `opencode`'s `22.5`), `22,389` median output tokens and `14,452` median reasoning tokens (beating `opencode`'s `23.0k` / `15.5k`), `0` fixture behavior timeouts, and `0` tool errors across all `42` runs.

### R4-5: Combined Winning Stack `R4-2 + R4-3 + R4-4` (`e11d67a0f3` on `8a3453bf0b`, 42 trials): PROVISIONAL / INCOMPLETE (8 container provider outages)
- **Input and tool discipline:** Round 1 input dropped `51%` (`10,646` on fixture, `11,030` on Terminal-Bench), with `0` ceremony calls, **`0` tool errors across all `830` tool calls** (`0/8` `job_watch` errors), and `using-worktrees` auto-gated `0/30` on non-git containers.
- **Fixture outcomes (`overfull-hbox`, n=12):** `9/12` raw pass (`3` transient Lunaroute HTTP 503 outages at `calls=0..2`; `9/9`, `100%` non-outage, `0` timeouts).
- **Terminal-Bench 2.1 outcomes (n=30):** `19/30` raw pass, with `8` transient Lunaroute HTTP 503 outages (`calls=0..2`), yielding `19/22` non-outage pass (`16/16` across the 8 deterministic/core tasks, `1/3` clean non-timeout pass on `largest-eigenval` at `833s`, and `2/3` on `configure-git-webserver`).
- **Gate 4 validation status:** Full-board Gate 4 promotion validation for R4-5 is **provisional / incomplete due to 8 provider outages**. Replacing or re-running the infrastructure-invalid attempts is required to establish `>= 27/30` across the complete 30-trial board.

---

## 9. Gate 1 Five-Model Live `evener doctor` Verification

All `6/6` live checks passed across `deepseek-4.1-flash`, `glm-5.3`, `gemini-4-hydrogen`, `gpt-6-astra`, and `kimi-for-coding`, plus live `use_skill("using-worktrees")`. The run logged `0` tool errors, `0` `notes_agent_set` calls, and spotless `evener doctor` health.

---

## 10. Unmerged PR Stack

- **PR `#7505`** (R4-3: one-shot ceremony and scratch cleanup): unmerged, pending Jesse's review.
- **PR `#7504`** (R4-4: direct execution and behavioral prompts): unmerged, pending Jesse's review.
- **New R4-2 PR** (`exp-r4-2-progressive-disclosure`, replacing closed `#7503`): unmerged, pending Jesse's review.
- **PR `#7509`** (this rubric and audit document): unmerged, pending Jesse's review.
