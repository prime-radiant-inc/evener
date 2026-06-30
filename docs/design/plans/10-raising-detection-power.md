# Phase 9 — Raising detection power

> **Status (2026-06-30): W1–W3 DONE + merged; W4 cross-subsystem invariant done,
> namer-determinism deferred.** W1 oracle mutation-audit engine shipped (6
> mutations, 5 oracle classes). W2 found+fixed **two real Anthropic 400 bugs**
> (provider-option forced `tool_choice` under thinking; `max_tokens` ≤ budget);
> other-provider contracts deferred rather than invented. W3 added the
> structure-aware live-vs-reload differential (~85× deeper, no divergence). W4
> added a sound cross-subsystem invariant (job terminal-state finality under the
> full interleaving — Oracle 7, validated over 8000+`-race` checks); the namer/
> events-channel determinism refactor was investigated and **deferred with
> rationale**: enabling it requires turning on `StateDir` (autosave + session
> logging + persistence) inside the carefully-tuned deterministic lifecycle
> model, a destabilizing change for low-yield coverage that the
> detection-not-coverage diagnosis does not justify unsupervised.
>
> **Status (2026-06-29): PROPOSED, not started.** Point-in-time design + plan.
> Successor to `09-fuzzer-bug-finding-improvements.md` (Phase 8, complete). Scopes
> the next round of fuzzing work after the toolkit's breadth (Phases 0–7), four
> oracle levers (Phase 8), and corpus/continuous infra (Phase 8.5) all landed.
> Effort is in lines-of-code assuming a frontier LLM does the work, not wall time.

## Why this phase exists

Phase 8 settled the central diagnosis empirically: **the ceiling is detection,
not search.** A 10×-deeper campaign found nothing new, and *both* real decoder
bugs (anthropic finish-reason, gemini usage) came from a stronger **oracle** —
the cross-provider differential — not from more fuzztime. The structure-aware
generators, which only widen the search, found zero bugs.

So this phase invests in **oracle strength and provability**, not search:

1. **Prove the oracles we already have actually fire** (W1). We assert ~80 targets
   "detect bugs," but have only ad-hoc-verified a handful. A blind oracle reads
   exactly like a correct program: green.
2. **Add oracles where a known bug class has none** (W2). The request-construction
   side — where real prod-400 bugs have been found — currently asserts only
   "no panic."
3. **Extend the proven winner** (W3): the differential oracle, to the two-path
   projection (live vs reload) where user-visible bugs keep recurring.
4. **Finish the stateful layer's remaining depth** (W4) — now incremental, because
   the interleaved lifecycle model already exists.

Non-goals are stated at the end; they are the things Phase 8 proved don't pay.

---

## W1 — Oracle mutation-audit (do first)

**Goal.** A standing, automated proof that every fuzz oracle reddens on the bug
class it claims to catch. This is the rigor capstone: it measures detection
*sensitivity*, the quantity the diagnosis names as the bottleneck, and it turns
each bug we've already fixed into a permanent "the detector still works" check.

**Mechanism (recommended: patch-based, SUT stays pristine).** A mutation is a
known fault — typically the inverse of a fix we shipped — expressed as a small
git patch, **paired with a corpus seed that reaches the fault** (for a
fuzzer-found bug, the regression seed the toolchain already saved; otherwise a
seed shipped with the mutation). The audit applies each patch in a *throwaway git
worktree at HEAD*, runs the corresponding target's seed corpus under
`-tags serffuzz`, and asserts the run **fails** (the oracle caught the fault). A
clean HEAD must pass (sanity). On authoring, each mutation is confirmed to flip
its seed clean→failing, which proves both that the seed reaches the fault and
that the oracle fires — without that pairing, a seed-only run could leave a
genuinely sensitive oracle reading "blind" simply because no committed seed
exercises the mutated path.

Keeping the fault out of the production tree matters for the codebase's
cleanliness bar, and patch *rot* becomes a **loud** failure, not a silent skip:
if `git apply` no longer lands (the SUT was refactored), the audit errors with
"mutation `<id>` no longer applies — re-derive it," which is exactly the signal
we want. Re-deriving is cheap: it is the inverse of a known fix.

> Alternative considered: build-tag fault injection via a `mutate` package
> mirroring `invariant/` (a `mutate.Active(id)` no-op under `!serfmutate`,
> env-selected under `-tags serfmutate`). Cleaner against refactors, but it
> scatters `if mutate.Active(...) { buggy } else { correct }` scaffolding through
> production SUT files. Prefer patches; fall back to this only for a fault that
> cannot be expressed as a stable patch.

**Artifacts.**
- `fuzz/mutations/manifest.tsv` — one row per mutation:
  `id <TAB> target(module:FuzzName) <TAB> patchfile <TAB> description`.
- `fuzz/mutations/<id>.patch` — the fault.
- `scripts/fuzz-oracle-audit.sh` — the harness (mirrors `fuzz-triage.sh`/
  `fuzz-bisect.sh` conventions: env seams `SERF_FUZZ_RUNNER`, throwaway worktree,
  honest reporting). For each mutation: worktree-at-HEAD → `git apply` (loud on
  failure) → `go test -tags serffuzz -run '^<FuzzName>$' <pkg>` → assert non-zero
  → clean up. Then a **gap report**: every native target in `run-fuzz.sh --list`
  with no mutation is flagged "unaudited oracle" (informational at first; a soft
  gate later, like `fuzz-gap-check`).
- `scripts/fuzz-oracle-audit-selftest.sh` — deterministic, real-git: a throwaway
  module whose target's oracle catches one injected fault and is blind to
  another; assert the audit reports caught/blind correctly (the
  `fuzz-bisect-selftest.sh` pattern).
- `make fuzz-oracle-audit` + `make fuzz-oracle-audit-selftest`.

**Seed mutation set (one per shipped bug + a few oracle-specific).** Each is a
one-to-few-line patch reintroducing a real or plausible regression:
- `anthropic-finish-toplevel` → read `stop_reason` at top level not under
  `delta` (the `2bbd…`/Phase-8 differential find). Target:
  `llm:FuzzAnthropicStreamMetamorphic` / the difftest target.
- `gemini-usage-early-return` → return before parsing `usageMetadata` on the
  finish chunk (`6403…`). Target: `llm:FuzzGeminiStreamStructured`.
- `toolcall-order-maprange` → range the `map[int]*toolCallState` directly instead
  of `slices.Sorted(maps.Keys(...))` (`646d…`). Target:
  `llm:FuzzOpenAIChatCompletionsMetamorphic`.
- `instance-name-no-base` → drop `filepath.Base` from `AuthFilePath` (the
  path-traversal `.json`-delete fix). Target: the serf/instance dispatch target.
- `jobstore-terminal-unsticky` → let a terminal job event be overwritten (the
  `applyEvent` terminal-sticky invariant). Target: `agent:TestJobstoreSeqFuzz`.
- `frontmatter-determinism-naive` → revert the NaN-aware equal to
  `reflect.DeepEqual` (this session's oracle fix). Target:
  `agent:FuzzFrontmatterParse` — note: this mutation lives in the *test* oracle,
  so it validates the audit can also guard oracle code, not only SUT code.

**Definition of done.** Every mutation reddens its target; clean HEAD is green;
the gap report lists the unaudited targets; self-test passes; gate green.

**Effort.** ~200–300 LoC (harness + self-test) + ~8–12 small patches + manifest.

**Risks.** Patch rot (mitigated: loud failure). Two ways an audit can falsely
read "oracle blind," both addressed by the authoring-time clean→failing
confirmation above: an *equivalent* mutation (changes no real output), and a
mutation whose fault **no committed seed reaches** under a seed-only run (the
oracle is fine; the input never gets there). The seed-pairing requirement closes
both — if the mutation does not flip its seed on authoring, it is rejected until a
reaching seed (or a bounded `-fuzz` step for that one target) is added.

**Recursion.** W2's and W3's new oracles each get a mutation here. W1 thereby
becomes the standing meta-gate for the whole toolkit's detection power.

---

## W2 — Request-contract invariants (concrete, high prod value)

**Goal.** Guard the provider **request-construction** side with the same rigor as
the decode side. Today `buildRequestBody` for each provider is fuzzed only for
no-panic + JSON-marshalable + a couple of preserved keys
(`llm/providers/*/requestbuild_fuzz_test.go`). Yet the real, prod-impacting bugs
here are **contract violations** that surface only as a provider 400 in E2E:
- anthropic forcing `tool_choice` (`any`/`tool`) while thinking is enabled →
  400. Guard today: `llm/providers/anthropic/request.go:154-160` (downgrades to
  `auto`).
- anthropic `max_tokens ≤ thinking budget` → 400. Guard today:
  `request.go:175-183`, with `budget := llm.ReasoningBudget(effort)` at
  `request.go:137`.

These guards exist but nothing *proves* they hold under adversarial input, and a
refactor could silently break either.

**Mechanism.** Two complementary layers, both reusing the zero-cost-in-prod
`invariant` package (`invariant.Hold`, `Enabled` — see existing sites
`llm/providers/anthropic/adapter.go:559`, `google/adapter.go:496`,
`openai/adapter.go:1003`):

1. **Inline post-condition invariants** in each `buildRequestBody`, asserting the
   contract *after* the guard runs:
   - anthropic (`request.go`): `invariant.Hold` that when the thinking budget
     > 0, the built `tool_choice` is never a forced kind, and built `max_tokens`
     > thinking budget.
   - general (all providers): required keys present; no mutually-exclusive fields
     co-set; `max_tokens`, when present, > 0; a named `tool_choice` references a
     declared tool.
2. **Strengthened fuzz oracles** in `requestbuild_fuzz_test.go`: parse the output
   map and assert the same contracts directly (so the contract is checked even in
   a non-serffuzz run, and the invariant is checked under serffuzz). The fuzzer
   already varies `llm.Request` (`llm/types.go:241` — `ReasoningEffort`,
   `ToolChoice`, `MaxTokens`, `Tools`, `ResponseFormat`); widen the generated
   space to drive every guard arm.

**Per-provider contract inventory (starting set; derive the rest from each
provider's API docs + the guards already in code).**
- anthropic: thinking⇄tool_choice, thinking⇄max_tokens, system-as-top-level,
  tool_choice names a declared tool.
- openai responses/chat: `response_format` json_schema well-formed;
  reasoning-effort only on reasoning models; `max_output_tokens`/`max_tokens`
  positivity; tool_choice references a tool.
- google: `contents` non-empty; `generationConfig` budget ≤ max; role alternation.
- openaicompat: the union, modulated by `ProviderQuirks`
  (`llm/providers/openaicompat/request.go:12`).

**Definition of done.** Each contract is an inline invariant *and* a fuzz-oracle
assertion; the existing E2E-found 400s are covered as named contracts with a
mutation in W1; gate green; the strengthened targets find no new bug (or one is
fixed TDD red→green if they do).

**Effort.** ~150–300 LoC across providers (anthropic richest), plus the widened
generators. Independent of W1; can land in parallel.

**Risk.** Over-tight invariants that reject a legitimate request shape — each
contract must be grounded in the provider's actual API, not guessed; where the
contract is uncertain, encode it as a fuzz-oracle assertion first (easy to relax)
before promoting to an inline invariant.

---

## W3 — Extend the differential oracle (the proven winner)

**Goal.** The differential oracle found both Phase-8 bugs; apply it where
user-visible bugs keep recurring: the **two projection paths**. serf projects a
turn two ways — live (`internal/appprojector`) and on reload
(`internal/apptranscript` + the hub replay `cmd/serf-hub` …
`app_threadread.go#replayTurnToAgentTurn`). Divergence between them is a
*recurring* real-bug source: thinking traces vanishing on reload, web_search not
re-projecting, communicate-echo duplicated on reload — each shipped as its own
fix. A live-vs-reload differential makes that whole class a standing oracle.

**Mechanism.** There is already a `FuzzHubReplayLiveVsReload`
(`app_threadread.go#replayTurnToAgentTurn`). Audit its **content-type
completeness** and extend the structured generator to emit every content type the
live path can produce — thinking, web_search, audio/document attachments,
tool-call + tool-result, communicate echo — then assert the live projection and
the reload projection of the *same* synthetic transcript are equal up to the
documented, intentional differences. Where they differ for an undocumented
reason, that is the finding.

Secondary (optional, lower yield): a **cross-provider request-build differential**
— one canonical `llm.Request`, each provider's `buildRequestBody`, assert the
shared-semantic fields agree (same message count, same tool names, consistent
max-tokens semantics). Shapes differ per provider, so this is a
normalize-then-compare, not a byte diff; modest value, list as stretch.

**Definition of done.** The live-vs-reload differential covers every content type
in the live projector's output; a coverage note records any deliberately-excluded
type; new oracle gets a W1 mutation; gate green.

**Effort.** ~150–250 LoC (generator extension + the differential harness; the
target already exists).

**Risk.** Encoding the *intended* live/reload differences precisely — get them
wrong and the oracle is either noisy (false positives) or blind (over-broad
allowance). Anchor each allowed difference to the code that creates it.

---

## W4 — Finish the stateful layer's remaining depth (smaller than it looks)

**Honest scoping.** The interleaved core model already exists and is broad:
`agent/lifecycle_seqfuzz_test.go` (`TestLifecycleSeqFuzz`) drives ProcessInput,
interrupt, steer, enqueue, follow-up, goal set/clear, advance-clock, **delegate**,
**background shell**, **background delegate**, observe, and close, *and* forced
compaction (`kindCompact`) — all under the injectable clock
(`agent/internal/clock`, `agenttest.FakeClock` with `Advance`/`BlockUntil`). The
per-subsystem models (jobstore I1–I7, compaction I1–I7, hub-multisession INV1–3,
router) cover their own invariants deeply. So this is **not** a from-scratch
unified model; it is targeted depth on a layer that is already good.

What is actually left:

1. **Close the two determinism traps** the lifecycle model currently works around,
   so deeper interleavings are reproducible:
   - the buffered events channel `make(chan events.SessionEvent, 256)`
     (`agent/session_init.go:130,358`) — the 256 buffer makes emission/receive
     scheduling nondeterministic. Give the fuzzer a synchronous-drain seam or
     route draining through the fake clock's quiescence handshake.
   - the background **namer goroutine** (`agent/session_namer.go:186`
     `launchInitialPromptNamer`, joined via `sendersWG.Wait()` in `Close()` at
     `session_lifecycle.go:174,219`) — route its scheduling through the injectable
     clock so a stateful run is deterministic with the namer *enabled* (today it
     is avoided).
2. **Cross-subsystem invariants** the isolated models cannot see: assert
   subsystem-deep properties *inside* the interleaved context — e.g. compaction
   during an active delegate must not drop the delegate's pending watch-sends; a
   job reaching terminal during compaction preserves notification monotonicity
   across the compaction boundary. Share the jobstore/compaction invariant checks
   into the lifecycle model's `checkInvariants`.
3. **Stretch:** live-subprocess multi-source for `TestHubMultiSessionSeqFuzz`
   (scoped out in Phase 8 for needing real subprocess sources).

**Definition of done.** The lifecycle model runs deterministically with the namer
enabled and a synchronous event drain; at least two cross-subsystem invariants are
added and survive a long `-race` run; gate green.

**Effort.** Determinism work is a real refactor touching production scheduling
(~200–400 LoC + risk); the cross-subsystem invariants are smaller. Largest risk of
the four — do it **last**.

**Risk.** The determinism refactor touches production code paths (event emission,
namer lifecycle); a mistake degrades real runtime behavior. Gate on the full
`-race` suite and E2E feel-tests, as Phase 6's launch-time-watch revert taught.

---

## Sequencing

1. **W1 first** — it is bounded, independent, and proves the detection layer
   before we invest in more of it. Every later workstream's oracles plug into it.
2. **W2 next** — concrete, independent, highest prod-bug value (the 400 class).
3. **W3** — extends the proven winner to the recurring reload-bug class.
4. **W4 last** — the only real refactor; smallest marginal value now that the
   stateful layer is already broad; highest blast radius.

W1–W3 are mutually independent and could be parallel lanes (disjoint modules:
W1 = scripts + `fuzz/`, W2 = `llm/providers/*`, W3 = `cmd/serf-hub` + `appwire`),
following the established fan-out discipline. W4 is sequential and serial.

Each workstream is its own gate-green `--no-ff` merge. New oracles in W2/W3/W4
each land with a W1 mutation proving they redden.

## Non-goals (Phase 8 proved these don't pay)

- More fuzztime / deeper search — the ceiling is detection, not depth.
- More single-input decode targets — the surface is covered (gap gate is green).
- More structure-aware generators — they widen search, found zero bugs.
- Driving reflect-decode targets to 100% coverage — unreachable-at-floor by
  construction; the ratchet already lifts the drivable ones.
