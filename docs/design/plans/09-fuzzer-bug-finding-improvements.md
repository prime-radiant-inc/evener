# Phase 8 — beyond coverage: dramatically improve the fuzzers' bug-finding

> **Update (2026-06-29): EXECUTED — merged to main.** All four levers below shipped:
> 8.1 internal invariants, 8.2 differential (golden · cross-provider · two-path ·
> stream-vs-non-stream), 8.3 stateful (jobstore · compaction · hub-multi-session),
> 8.4 structure-aware generators (appwire · transcript · all four provider SSE
> streams) — finding **2 real decoder bugs** (anthropic streaming finish-reason,
> gemini usage-on-finish-chunk). The supporting tail (8.5 dictionaries / OSS-Fuzz
> seeds; continuous-fuzzing infra) was deliberately deferred as lower-value. This
> is the original plan, preserved as written; for current usage see
> [`docs/fuzzing.md`](../../fuzzing.md).

**Status:** plan, ready to fan out. **Premise:** the 10× deeper campaign found *nothing
new*. That is the **oracle ceiling, not the time ceiling** — more hours and more targets
won't help. Almost every real bug we found came from the few *semantic* oracles
(metamorphic stream divergence, tool-call-order nondeterminism, the reload dups) and the
sandbox containment tripwires (the `serf/instance/remove` path-traversal). Oracle census
today: 34 "never panic", 34 round-trip, 26 fixed-point, 23 invariant, 14 metamorphic,
~9 path-escape, 3 wedge, 2 monotonic, **0 differential**, and only **3 stateful** targets
vs 72 single-input. The four levers below change *what we can detect*, not how long we look.

**Build order:** 8.1 first (its infra amplifies all 75 existing targets), then 8.2/8.3/8.4
in parallel (independent). 8.5 is supporting. Every lane: TDD, keep the gate green, drive
the REAL seam, route stateful failures through `fuzz/promoter`, commit phased.

---

## 8.1 — Make the program self-checking: internal invariant assertions (HIGHEST ROI)

**Idea.** How SQLite/Postgres fuzzing actually finds *logic* bugs: the production code is
densely instrumented with cheap internal invariant checks that the fuzzer trips *at the
point the logic goes wrong*, not when it surfaces externally. Our oracles are all
*external* (round-trip/no-panic). Internal invariants instantly upgrade **all 75 existing
targets** from crash-finders into logic-bug-finders.

**8.1.0 — the assertion mechanism (prerequisite, single agent, build FIRST).**
A tiny package (propose `internal/invariant`, serf-wide importable) exposing
`invariant.Hold(cond bool, format string, args ...any)`:
- **Zero-cost in production.** Gate the body behind a build tag (`//go:build serffuzz`) so
  a normal build compiles it to an empty inlinable no-op — verify with a disassembly/bench
  that the production path carries no overhead and no behavior change. (A runtime bool is
  the fallback if the build-tag split proves too invasive; build tag is preferred.)
- **Loud under fuzz.** When built `-tags serffuzz`, a violated invariant panics with the
  message + the offending value, so the existing no-panic oracle catches it for free.
- Wire `-tags serffuzz` into the fuzz Makefile targets (`make fuzz`, `run-fuzz.sh`,
  `fuzz-coverage`) so every target runs with invariants live; the production build and the
  non-fuzz test gate stay tag-free (unchanged).
- Acceptance: a deliberately-violated invariant is caught by a fuzz target; `go build ./...`
  (no tag) is byte-identical to before; `make test` unaffected.

**8.1.1+ — invariant lanes (fan out, one subsystem per lane, after 8.1.0).** Each lane adds
domain-true invariants at that subsystem's hot logic and proves the relevant existing fuzz
target trips a *deliberately-broken* one (red→green discipline). Candidate lanes + example
invariants (the lane verifies each invariant is actually TRUE before asserting it — a wrong
invariant is a false-positive crash):
- **appprojector / apptranscript:** every emitted `ThreadItem` has a non-empty `turnID` and
  a known `Type`; item IDs within a turn are unique; reload projection ⊆ documented diffs.
- **jobstore fold/applyEvent:** status only advances (terminal is sticky); no event applied
  twice; a delegate's child set is consistent across reducers.
- **session lifecycle:** `history` never shrinks except across a compaction turn;
  `modelResponses` monotonic; no orphaned tool-call without its result post-repair.
- **llm decoders:** a decoded tool call always has a non-empty name; usage counts are
  non-negative; an assembled message's parts are well-typed.
- **appwire codec / dispatch:** a decoded frame's `Kind()` matches the populated field;
  a response/error frame's id round-trips.

**Risk.** Invariants must be true (verify against the code, not assumed) and zero-cost in
prod (build tag). This is the highest-impact item; do it carefully and first.

---

## 8.2 — Differential oracles (the entirely-missing class)

**Idea.** The strongest fuzzing finds bugs by *disagreement between two things that must
agree*. We have none. Three sub-levers, fan out independently:

**8.2a — golden/snapshot regression-differential (lowest effort, high value).** Commit the
decoded output of each decode target's corpus as a canonical golden; a change that silently
alters a decoder's output flags in CI. Catches behavior drift a refactor introduces with no
new crash. Build a small `goldendiff` helper + a `make fuzz-goldens` (regen) / gate step
(compare). Apply to the codec/decoder targets first.

**8.2b — cross-provider differential.** Generate one *canonical logical response* (text +
tool calls + reasoning + usage), encode it into each provider's wire format, decode via each
real adapter, and assert the accumulated `llm.Response` is equivalent modulo provider-specific
fields (the allow-list). Catches adapter-specific drift our per-provider metamorphic can't.
Needs a logical-response generator + per-provider encoders (the inverse of the decoders) —
the substantial piece; scope to the 4 real decoders.

**8.2c — two-path differential audit.** We already do SSE blocking-vs-timeout. Sweep for
other dual-path code (any place two code paths compute the same thing — e.g. live
projection vs reload projection, which 8.1-T4 already covers metamorphically) and add the
differential where a second path exists.

Acceptance: a deliberate decoder output change is caught by 8.2a; a crafted cross-provider
divergence by 8.2b.

---

## 8.3 — More stateful / sequence models (where the real bugs hide)

**Idea.** Only 3 stateful targets; every non-crash bug we found was a *state* bug. Build
`rapid` state machines (mirror the lifecycle/Phase-2 pattern: declarative op table → thin
machine → invariants weakest-first → failures through `fuzz/promoter`) for serf's other
stateful subsystems. Fan out, one per lane:
- **jobstore as a state machine:** legal event sequences (start→…→terminal, delegate
  spawn/finalize, watch/grant) → fold → invariants over the *sequence* (not just one log).
- **context-manager / compaction:** message accumulation → compact → history invariants
  (the shrink-exception, needle retention, no-lost-turn) — the surface behind serf's
  reload/compaction history.
- **hub multi-session / multi-source:** the appserver/hub state across several sessions and
  sources (list/start/clear/steer interleavings) — beyond the single-session lifecycle.

Acceptance: each finds ≥1 issue or proves clean to a stated depth; deterministic under
`-race` with zero promoter quarantines.

---

## 8.4 — Structure-aware generators (make every exec count)

**Idea.** Most execs today are garbage that dies at the first `json.Unmarshal`; a tiny
fraction reach deep logic — which is *why* 10× more time found nothing (10× more shallow
inputs). We built `fuzz/schemagen` + `fuzz/typegen` (byte-fed structured generation) but
only the registry uses them. Extend structure-aware generation, via the existing `Source`
abstraction, to the high-value single-input targets so coverage-guided fuzzing explores the
*structured* space. Fan out, one format per lane:
- **provider SSE / response streams:** a generator emitting valid-but-adversarial event
  *sequences* (text/tool/reasoning/usage/incomplete) for the metamorphic + Complete targets.
- **transcripts:** valid transcript JSONL covering every turn kind, feeding the
  transcript/replay + reload targets.
- **appwire frames:** generalize the typegen reflect path to whole frames (request/response/
  error/notification), feeding `FuzzMessageDecode`.

Each lane: drive the target's byte input through a byte-`Source`-fed structured generator;
prove materially higher focus coverage + that it reaches inputs the raw-byte target couldn't.

---

## 8.5 — Supporting (lower priority, after the above)

- **Format dictionaries + known-bad seeds.** Seed corpora with the magic tokens/edge values
  each parser cares about, and import upstream OSS-Fuzz crashers for the libraries we use
  (encoding/json, BurntSushi/toml, yaml, the SSE shape) so we inherit their known edges.
- **Continuous fuzzing service.** An always-on OSS-Fuzz-style runner (corpus accumulation,
  regression bisection, dedup) — the industrial "look harder" answer. Deliberately last:
  the diagnosis says the ceiling is *what we can see*, not how long we look, so the oracle
  levers (8.1–8.4) come first.

---

## Execution waves
- **Wave 1:** 8.1.0 (assertion mechanism — single focused build; gate-critical: prove
  zero-cost-in-prod + tag wiring) → then fan out the 8.1 invariant lanes (per subsystem).
- **Wave 2 (parallel, independent):** 8.2a golden-diff + 8.2b cross-provider · 8.3 stateful
  lanes · 8.4 structure-aware lanes. Disjoint by subsystem/module.
- **Wave 3:** 8.5 supporting.

Every wave: parent runs the full gate (`make fuzz`/`test`/`lint`/`fuzz-gap-check` +
`-race` on rapid targets) with `-tags serffuzz` on the fuzz path, and confirms the
production build is unchanged, before moving on.
