# Test-runtime performance pass — report

Branch `perf-tests`, base `5c3d8d66e`. Commits landed: `6bd607b37`, `d3ce36088`.
Frontend: `cmd/serf-hub/frontend`. Machine: Apple M4, 10 cores, 16 GB, Node
v26.0.0, vitest 4.1.10, Biome 2.5.5, TypeScript ^6.0.3, Vite ^8.1.5, jsdom ^29.
Suite: **217 files / 3189 tests** (87 `.test.ts`, 130 `.test.tsx`).

## Headline

| metric | before | after | delta |
|---|---|---|---|
| full chain, solo (warm) | **29.13s** | **~24–25s** | **−~4s (~14%)** |
| full chain, 2-concurrent | **62–65s** (clean, low-load) | not cleanly re-measurable under fleet saturation; ≥ solo saving expected | — |
| vitest solo | 23.63s | ~21.5s | −~3.2s |
| tsc (warm, repeat runs) | 2.34s | ~0.75–1.0s | −~1.3–1.6s (×2 in chain) |

Two safe levers landed; two rejected with evidence; suite unchanged (217/3189,
3 consecutive green), no timeout/coverage/isolation weakening.

## Measurement caveat (important)

Baselines were taken early under low load (1-min load ~2–13). Partway through,
the agent fleet became very active and machine load climbed to **24–61 on 10
cores** and stayed there. Absolute wall-clock under that load is unusable
(same command varied 22–49s). Every conclusion below therefore rests on
**load-robust methods**: (a) vitest's own cumulative `environment`/`transform`
metrics, (b) **interleaved A/B** (before/after measured back-to-back so
common-mode load cancels), (c) **paired-difference and minimum** statistics
over many rounds. Where a number could not be isolated from fleet noise, it is
labelled as such rather than reported falsely.

## Baseline (low-load, clean)

Component solo medians (3 runs): tsc **2.34s**, vitest **23.63s**, lint **0.32s**
warm (biome self-reports ~0.2s; already trivial), build **2.55s** (re-runs tsc +
vite build). Full chain (tsc && vitest && lint && build): **29.13s**.

vitest internal breakdown (cumulative across workers): **environment 89–106s
(DOMINANT)**, tests 56–58s, import 25–27s, transform 6–7s, **setup 0ms**
(setupFiles is `[]` — nothing to audit). Under `isolate:true` (mandatory, not
touched) every file builds a fresh environment, so jsdom construction is pure
per-file overhead.

2-concurrent full chains (independent APFS clones, low load): slower 62–65s,
**stretch 2.14–2.22×** — solo vitest already saturates all 10 cores, so a second
suite roughly doubles worker demand and the two time-share with ~0 parallelism
gain.

Two slow outliers (legitimate DOM tests, untouched): `oauthDialogs.test.tsx`
~9.9s, `CredentialsSection.test.tsx` ~4.7s.

---

## Lever 1 — per-file environment routing (jsdom → node)  ✅ LANDED `6bd607b37`

**What.** 39 pure-logic `.test.ts` files carry `// @vitest-environment node`.
Chosen by a **transitive-import classifier** (`scratchpad/perf/classify.mjs`):
a file qualifies only if neither it nor any first-party module it transitively
imports (a) is a `.tsx`, or (b) references any browser-global token. The token
set includes the loud-fail globals AND the **node∩jsdom "silent-pass" set**
(`navigator`, `sessionStorage`, `CustomEvent`, `Event`, `EventTarget`) that
Node 26 exposes, so a routed file cannot silently depend on jsdom behaviour.
Result: **39 of 87** `.test.ts` route to node; **48 correctly keep jsdom**
(they reach `protocol/transport.ts`'s `window`, `stores/threads.ts`, or a `.tsx`
component). Notably `notifications/attention.test.ts` — cited as a "pure logic"
example — is correctly *rejected*: it transitively pulls in the transport layer.
No candidate uses `vi.mock`/`vi.hoisted`/`require`, so the import walk is complete.

**Isolation measurement (39-file subset, node vs jsdom, 3 runs each):**

| env | Duration | cumulative environment |
|---|---|---|
| node | ~0.80s | **~3ms** |
| jsdom | ~3.1s | **~18s** |

**Full-suite interleaved A/B (4 rounds, under load):**

| round | node dur | jsdom dur | node env | jsdom env |
|---|---|---|---|---|
| 1 | 21.00s | 25.76s | 73.6s | 106.9s |
| 2 | 21.58s | 23.23s | 76.7s | 93.1s |
| 3 | 25.12s | 29.04s | 99.9s | 121.0s |
| 4 | 21.45s | 23.85s | 75.7s | 98.2s |

node wins **every** round: median Duration 21.52s vs 24.80s (**−3.16s**),
cumulative environment ~−20s each round. Direction is unanimous across rounds
despite heavy external load.

**Safety experiment (mandatory — misclassification must fail LOUD):** forced the
DOM-heavy `App.test.tsx` to the node env → `ReferenceError: document is not
defined`, raised **at import time via a transitive dep** (`stores/prefs.ts`
`applyTheme`), "1 failed / Tests no tests", **vitest exit 1**. A throwaway
node-pragma DOM test likewise exits 1. So a misrouted file fails hard at the
gate; it cannot silently pass. Node-env surface probe (Node 26) recorded:
`document/window/localStorage/HTMLElement/Element/*Observer/matchMedia/
getComputedStyle/DOMParser` = undefined (loud fail); `navigator/sessionStorage/
CustomEvent` = present (excluded by the classifier).

**Verdict: safe win.** ~3.2s solo vitest, ~20s cumulative jsdom work removed;
larger relative benefit expected when contended (removes CPU-bound work when
cores are saturated). Suite green 217/3189, tsc/lint/build exit 0.

---

## Lever 2 — worker-pool cap  ❌ REJECTED (no clean landing)

Default pool `forks`, ~10 workers (= cores). Hypothesis: cap workers so N
concurrent suites stop thrashing.

**Solo cost of capping (`--maxWorkers`, node-routed HEAD, 2 runs each):**

| maxWorkers | solo Duration |
|---|---|
| 10 (default) | ~20.9s |
| 8 | ~22.2s |
| 6 | ~26.0s |
| 5 | ~28.4s |
| 4 | ~31.2s |

**2-concurrent vitest across caps:** at moderate load, `maxWorkers=5` (2×5 = 10
cores, a perfect fit) gave the best contended time (~43.9s vs ~49.8s at
default 10). But under the realistic heavy fleet load (25–40), the ranking
collapsed into noise — `5` best in one round, `10` best in the next,
contradictory — because at machine saturation the OS time-shares regardless of
per-suite cap.

**Why rejected.** The only cap that helps *clean* 2-concurrent (`5`) costs
**~33% solo** (28.4s vs 20.9s) and shows **no benefit under real fleet load**.
A cap that is solo-safe (`8`) shows no measurable contended gain. A single
static `maxWorkers` cannot adapt to the fleet's variable 1–6-agent concurrency:
optimal is ~10 for 1 suite, ~5 for 2, ~1–2 for 6. The correct fix for
cross-agent oversubscription is a **machine-level concurrency limiter** (a
shared token/semaphore across all agents' test runs), which is infrastructure,
not a per-suite test-config lever, and out of scope here. Landing any cap would
regress the common solo/low-concurrency case for an unproven contended gain.
(Note: capping is *safe* w.r.t. the forbidden list — isolation is unchanged —
it simply is not a net win.)

---

## Lever 3 — incremental tsc type-check  ✅ LANDED `d3ce36088`

**What.** `incremental: true` + `tsBuildInfoFile: ./.tsbuildinfo` in
`tsconfig.json`; artifact gitignored. Agents run the chain repeatedly, so after
the first (cold) run tsc reuses its build-info cache and re-checks only changed
files. The chain runs tsc twice (`typecheck` + inside `build`) sharing one
cache, so the build's tsc is warm even on a cold chain.

| tsc mode | time |
|---|---|
| non-incremental (baseline) | 2.34s |
| incremental, warm | **0.75–1.0s** (measured under heavy load 12–27, i.e. understated) |
| incremental, cold | writes cache; ≈ first-run cost, negligibly above baseline |

Saving ~1.3–1.6s per warm invocation, ~2.6s per warm chain (two tsc calls).

**Correctness experiment (mandatory — must still catch errors after a warm
run):** warm run → exit 0; injected `const x: number = "string"` → tsc reports
**`error TS2322`, exit non-zero**; reverted → exit 0. The cache does not mask
errors. Artifact is per-worktree and gitignored, so there is no cross-agent
sharing/race; the two in-chain tsc calls are sequential.

**Verdict: safe win.**

---

## Lever 4 — setupFiles / transform audit  (nothing to do)

`setupFiles: []` → **setup 0ms**, nothing to cut. Transform is 6–7s cumulative
(<1s wall); the only lever there is replacing/trimming `@vitejs/plugin-react`'s
transform, which risks changing how JSX/components compile — high risk, sub-second
reward. Not pursued (YAGNI + safety).

## Lever 5 — pool `forks` → `threads`  ❌ REJECTED (slower + introduced a flake)

Interleaved, 2 rounds: threads **slower** (36.6s vs 33.3s; 33.6s vs 25.9s) and
round 2 **failed** — `App.test.tsx > renders the dev widget gallery` (1 failed /
3188 passed). `App.test.tsx` mutates process globals (`globalThis.ResizeObserver`,
`globalThis.localStorage`) in `beforeAll`; under the shared-process threads pool
those leak across files in a worker. This is exactly the fragile module-singleton
isolation the brief flagged as "assume unsafe". `forks` is required — confirmed,
not landed.

---

## Final proof

**3 consecutive full-suite runs on HEAD** (`d3ce36088`), under load 16–22:
run 1/2/3 all **217 files / 3189 tests passed, exit 0**. No flake introduced.
(forks confirmed necessary; the only failure seen all session was the threads
experiment above and one timeout under ~5× CPU oversubscription at load 25–68,
both environmental — never on the committed forks config.)

**Solo full-chain before/after** — three load-robust methods converge:
- paired AFTER−BEFORE over 8 rounds: median **−3.63s** (AFTER faster in 5/8;
  spread ±10–25s is pure load-spike noise);
- minimum-of-observed (least-interfered): AFTER 23.40s vs BEFORE 27.28s → **−3.88s**;
- median-of-observed: AFTER 34.37s vs BEFORE 38.47s → **−4.10s**.
Against the clean low-load baseline of 29.13s this is a **~4s (~14%) solo
reduction** to ~24–25s warm.

**Contended before/after** — clean BEFORE was 62–65s. A clean AFTER could not be
isolated: during the measurement window the fleet drove load to 24–61 and my own
2-concurrent runs on top pushed it to 44–61, where results swung 60–110s (noise,
not signal). Both landed levers reduce *per-suite CPU work* (env ~−18s cumulative,
tsc ~−2.6s), which under core saturation converts to wall-time more directly than
solo, so the contended saving should be **≥** the solo ~4s; this is argued, not
cleanly measured, and is the one number the fleet load denied me.

### Load-induced mass failure — investigated, NOT a regression

During final verification a HEAD run once showed 1 failed / 3188 passed, and a
12-run baseline stress at **peak load (35–61)** showed **149 files failed / 68
passed on all 12 runs**. Root cause from the logs: **851× `ReferenceError:
document is not defined`** plus jsdom's internal `Symbol(Node prepared with
document state workarounds)` error — i.e. **jsdom failed to construct its
environment** under resource exhaustion, so every jsdom file threw "document
undefined". This is environmental, not test logic. Proof it is not mine and not
new:
- the same **baseline** (0 of my changes) failed 12/12 at load 35–61 but passes
  **3/3 at load ~15** — purely load-driven;
- **HEAD** passed 15/15 then 3/3 as load fell back to ~7–20;
- all 39 routed files are **synchronous and jsdom-free** (no `findBy`/`waitFor`/
  timers) so they cannot cause a timeout or jsdom-init failure.

Incidental benefit: because 39 files no longer request jsdom, HEAD has **less**
exposure to this failure mode than the all-jsdom baseline. The real remedy for
fleet flakiness under load is the same machine-level concurrency limit called
out under Lever 2 — reduce oversubscription, don't touch the tests.

## Commit range

`5c3d8d66e..d3ce36088` (2 commits): `6bd607b37` env-routing, `d3ce36088`
incremental-tsc. Reproduction scripts: `scratchpad/perf/` (`classify.mjs`,
`apply-pragma.mjs`, `measure.sh`, `contend*.sh`).
