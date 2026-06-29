# API fuzzing + failure-to-regression toolkit — research & recommendation

**Status:** research / proposal. **Date:** 2026-06-28. **Author:** Bot (for Jesse).

This report assesses whether and how to build a toolkit that systematically fuzzes
serf's full API surface (unit + integration granularity) and automatically promotes any
discovered failure into a permanent regression test. It is grounded in a real read of
serf's code; load-bearing files and types are cited inline.

Scope note (added per Jesse): the end goal is **not** a serf-only tool. The intended
endpoint is a reusable **superpowers skill** — a portable methodology doc plus thin
tooling that any project can adopt — with serf as the first proving ground. The verdict
and architecture below are framed for that, separating a portable core from per-project
glue.

---

## 1. Executive answers to the three questions

**Q1 — Is this "a thing"? Yes, but it's two well-established things that are rarely
fused into one packaged loop.**
- (a) *Systematic API fuzzing* is mature and has real tools: Go's native coverage-guided
  fuzzer (`testing.F`, Go 1.18+), property-based testing (`pgregory.net/rapid`,
  `leanovate/gopter`, fast-check for JS), schema-driven REST fuzzers (Microsoft
  **RESTler**, **schemathesis**, WuppieFuzz), grammar fuzzers (the Fuzzing Book's
  approaches, Nautilus/Gramatron), and the continuous-fuzzing stack (OSS-Fuzz /
  ClusterFuzz / CIFuzz). Differential and metamorphic testing are established techniques
  for parser/engine families.
- (b) *Auto-promoting a discovered failure into a permanent regression test* is real but
  mostly lives **inside** the big fuzzers rather than as a reusable cross-language
  pattern. Go's `testing.F` already does it end-to-end for free (a crasher is written to
  `testdata/fuzz/FuzzX/` and re-runs as a normal test forever). ClusterFuzz/CIFuzz add
  crash dedup (stack-hash bucketing), corpus pruning, and regression bisection. The
  failure→test *automation as a portable discipline* (minimize → dedup → flake-guard →
  emit a named test → commit) is **not** a single off-the-shelf package — that synthesis
  is the novel, skill-worthy part.

**Q2 — Gold-standard rubric to adopt, or author our own skill? Adopt the disciplines,
author the skill.** There is no single certified rubric to "comply with." The credible
references are the **Go Fuzzing docs/best-practices**, the **property-based-testing**
discipline (QuickCheck lineage), the **Fuzzing Book** (grammar/API fuzzing chapters,
fuzzingbook.org), and **OWASP WSTG Appendix C — Fuzz Vectors** (a vector checklist, not a
methodology). Best practice for the *failure→regression loop specifically* is ad hoc
enough across ecosystems that a bespoke, opinionated **superpowers skill** is the right
artifact — it should *wrap* these disciplines, not reinvent them.

**Q3 — Build a toolkit, author a skill, or both? Both, phased, with the skill as the
endpoint.** Concretely: (1) harvest the near-free Go-native wins on serf first
(~150–300 LoC gets four real fuzz targets whose crashers auto-promote); (2) build the
genuinely new pieces serf needs — a **schema-driven generator** off serf's existing
machine-readable schemas and a **stateful appwire sequence fuzzer**; (3) build the
**auto-promotion harness** (the part Go gives free only for single-input targets);
(4) extract the portable methodology + thin scripts into a **superpowers skill** with a
small per-project adapter interface. Total serf-side build to a working, self-promoting
loop across all four surfaces: **~2,000–3,400 LoC**; the skill-extraction phase adds
**~600–1,000 LoC/prose**. Phase breakdown and seams in §7–§8.

The single most important grounded finding: **serf already exposes machine-readable
schemas for two of its four surfaces**, which makes schema-driven fuzzing unusually cheap
here — see §3.

---

## 2. Serf's four API surfaces (what I actually read)

| Surface | Entry seam (the choke point a fuzzer drives) | Untrusted input | Schema available? |
|---|---|---|---|
| **appwire JSON-RPC protocol** | `appserver.Router.Dispatch(ctx, appwire.Request)` and the generic `HandleTyped[P,R]` (`internal/appserver/router.go:37,49`); frame decode `appwire.Message.UnmarshalJSON` (`appwire/jsonrpc.go:113`) | WS text frames → request/notification JSON; per-method `params` | **Yes** — declarative catalog `appwire.Methods` (`appwire/protocol.go:85`), 42 methods with Go `Params`/`Result` types; 79 `*Params`/`*Response` structs in `appwire/types.go`; already reflected into `docs/appwire-protocol.md` with a drift guard |
| **Agent tool API** | `tool.Registry.ExecuteCall(ctx, env, llm.ToolCallData{Name, Arguments})` (`agent/internal/tool/registry.go:446`) | model-generated tool-call `Arguments json.RawMessage` (decoded to `map[string]any`) | **Yes** — every tool carries a hand-written JSON Schema in `Definition.Parameters` (`agent/internal/tool/definitions.go`), compiled via `santhosh-tekuri/jsonschema/v5` |
| **LLM provider adapters** | `llm.ParseSSE` (`llm/sse.go:82`) + per-provider stream decoders, e.g. `openai.Adapter.decodeResponsesStream` (`llm/providers/openai/responses.go:217`), `fromResponses(raw map[string]any)` (:967); anthropic `response.go`/`request.go` | upstream provider SSE/JSON bytes (untrusted-by-posture) | Partial — implicit in each adapter's event switch; no formal schema |
| **Hub HTTP API** | `WebServer.Handler()` route mux (`cmd/serf-hub/web.go:134`); per-handler `json.NewDecoder(r.Body).Decode(&T)` | HTTP bodies (`spawnRequest` etc., `web_types.go`), query params, path segments; same WS `/rpc` | Partial — `/api/spawn-schema` exists; bodies are Go structs, reflectable |

Test infrastructure I'd integrate with (surveyed):
- **No Go native fuzzing exists today** — `grep "func Fuzz"` / `testing.F` / `testdata/fuzz` are all empty across the tree. Greenfield, no conflicting convention.
- **No property-based libs** — no `rapid`/`gopter`/`testing/quick` in any `go.mod`/`go.sum`.
- `make test` → `scripts/run-module-tests.sh -count=1` over modules **`.` `agent` `llm` `auth`** (two-wave: root alone, then `agent llm auth` concurrent). `envvars` is in `go.work` but *not* in the gate. CI runs `make test-race` (`-short`). go.work means `go test ./...` does **not** span modules — fuzz targets are picked up per-module automatically.
- **jstest** (`cmd/serf-hub/jstest/`): ~100 plain-Node JSDOM scripts that eval the hub renderer bundle and assert on DOM. Run by `run-all.sh`; **not wired into Go tests, Makefile, or CI** (manual/agent-run).
- **92 scenario cards** (`test/scenarios/*.md`): English e2e cards executed by an agent (browser/tmux/Bash), guarded only by a stale-string lint (`scenario_docs_test.go`).
- Golden tests are hand-frozen (`agent/snapshot_golden_test.go`, `cmd/serf-tui/tui_samples_test.go`); no `-update`/`goldie`/`cupaloy` convention.

---

## 3. The grounded headline: serf is unusually fuzz-ready on two surfaces

Two facts change the cost calculus:

1. **The tool API has a single choke point with per-tool JSON Schemas already written.**
   `Registry.ExecuteCall` decodes *every* tool's args to `map[string]any` and runs
   `t.Schema.Validate(args)` against the per-tool compiled schema (`registry.go:461-475`).
   There are **no per-tool input structs** — one harness covers all 25 built-in tools by
   varying `Name`. And `definitions.go` is a literal corpus of JSON Schemas we can read at
   runtime to *generate* both valid-but-adversarial and schema-violating inputs. This is
   exactly what RESTler/schemathesis want from an OpenAPI spec — serf hands it to us for
   free.

2. **The appwire protocol has a single typed dispatch seam plus a declarative catalog.**
   `HandleTyped[P,R]` (`router.go:37`) is the *only* place params are unmarshalled, and
   `appwire.Methods` (`protocol.go:85`) enumerates every method with its Go `Params` zero
   value. The doc generator already reflects those types into JSON shapes (it produces
   `docs/appwire-protocol.md`), so the same reflection produces a generator. `Dispatch`
   is the integration seam; `Message.UnmarshalJSON` is the frame-level unit seam.

Known soft spots already visible from the read (good first oracles / seed targets):
- `Schema.Validate(args)` is **not** `recover()`-wrapped at runtime (only schema *compile*
  is, `registry.go:633`) — a pathological validated input is a candidate panic site.
- Loose field extraction after validation: `int(float64)` numeric narrowing on
  `max_runtime_ms`, `tail_lines`, etc., with **uneven** per-handler guards; `fmt.Sprint`
  on required strings; `args["x"].(bool)` type assertions.
- `additionalProperties: true` pass-throughs — `delegate.result_schema` and
  `communicate.output.data` accept arbitrary nested JSON; `result_schema` is then compiled
  as its own schema in `createDelegate` (a schema-compiling-attacker-controlled-schema
  path — prime fuzz target).
- Hook path divergence: `execTool` does `_ = json.Unmarshal(...)` (swallows errors) and
  `applyUpdatedToolInput` re-marshals merged maps **without re-validation**
  (`session_tools.go:433`) — a validated-path-bypass worth differential fuzzing.
- appwire `ID.UnmarshalJSON` rejects null but `Int64()`/`String()` swallow errors;
  `Message.UnmarshalJSON` does a multi-probe branch — classic frame-fuzz target.
- SSE parser (`llm/sse.go`) has two code paths (blocking vs timeout goroutine) over the
  same bytes — a **metamorphic** target (both must agree).

---

## 4. Per-surface fuzzing strategy

Legend for "failure→test": **(auto)** = Go writes the crasher to `testdata/fuzz/` and it
re-runs forever with zero extra code; **(harness)** = needs the auto-promotion harness in
§5 because the failing artifact is a *sequence* or a non-`testing.F` runner.

| # | Surface / target | Technique | Tool | Failure→test mechanism | Effort (LoC) |
|---|---|---|---|---|---|
| 1 | SSE frame parser (`llm.ParseSSE`) | coverage-guided byte fuzz; metamorphic (blocking vs timeout path agree) | Go `testing.F` | crasher auto-saved **(auto)** | 60–120 |
| 2 | appwire frame decode (`Message.UnmarshalJSON`, `ID`) | coverage-guided byte fuzz | Go `testing.F` | **(auto)** | 50–90 |
| 3 | appwire per-method param decode (`HandleTyped`) — *unit* | table-driven byte fuzz over `appwire.Methods`, decode each `Params` type | Go `testing.F` (one target, method-tagged corpus) | **(auto)** | 120–200 |
| 4 | Tool-arg decode+validate (`Registry.ExecuteCall`) — *unit* | byte fuzz of `Arguments` keyed by tool `Name`; oracle = no panic, validate-or-clean-error | Go `testing.F` over a real `registerCoreTools` registry | **(auto)** | 150–250 |
| 5 | Tool args **schema-aware** (valid-but-adversarial) | property-based generation **from** each tool's `Definition.Parameters` schema | `rapid` + a schema→generator (§3) | **(harness)** — capture minimized `map`, emit named test | 400–700 (incl. generator) |
| 6 | appwire **stateful sequence** (init→thread/start→turn/start→steer/interrupt/queue→clear…) — *integration* | model-based / stateful PBT against `Router.Dispatch`; model = session/turn/job state machine; oracle = legal transitions never panic / never wedge / status invariants hold | `rapid` state machine (`rapid.Run`/`StateMachine`) | **(harness)** — persist minimized op-sequence as a generated Go test | 500–900 |
| 7 | LLM adapter response decoding (per provider) | byte fuzz of provider SSE/JSON into `decodeResponsesStream`/`fromResponses`; **metamorphic** (reorder/split SSE frames ⇒ same accumulated `llm.Response`) | Go `testing.F` + small metamorphic harness | **(auto)** for crashers; **(harness)** for invariant violations | 250–450 |
| 8 | Hub HTTP handlers | schema-driven request fuzz at `httptest` level; auth off via empty `AuthToken`; oracle = never 5xx/panic, never path-escape | Go `testing.F`/`rapid` driving `WebServer.Handler()`; optionally **schemathesis/RESTler** against a generated OpenAPI (§6) | **(auto)** at `testing.F` level; **(harness)** for sequences | 300–600 |
| 9 | Web hub renderer (JS) | property-based DOM/protocol fuzz of the renderer + appwire client | **fast-check** (or jsfuzz) inside the jstest harness | **(harness)** — JS-side promoter emits a new `test-*.js` | 300–500 |

Honest limits per technique:
- **Go `testing.F` is one input per target** (one `[]byte` or a fixed typed tuple). It is
  perfect for #1–#4, #7 and good for #8, and its **auto-promotion is the whole win** for
  those. It cannot express stateful sequences (#5 generation, #6, sequence parts of #8).
- **`rapid` shrinks to a minimal failing case but does *not* persist a regression test** —
  it prints a reproducer/seed. So everything `rapid`-driven needs the §5 harness to become
  permanent. `rapid` also lacks coverage-guided feedback (it's generation-, not
  coverage-, guided), so for raw byte parsers `testing.F` is strictly better; use `rapid`
  where structure/sequence matters.
- **Differential testing across providers is weaker than it looks**: the adapters consume
  *provider-specific* wire formats, so there's no single input that's "the same" for
  OpenAI and Anthropic. The tractable version is **metamorphic** (within one provider:
  frame splitting/reordering/whitespace must not change the decoded result, and must never
  panic) plus a **shared-invariant** oracle (`llm.Response` well-formedness) applied to
  each adapter independently. Call it metamorphic, not differential.
- The **JS renderer** can't ride Go's corpus model at all; it needs its own promoter, and
  jstest isn't in CI today — so JS fuzzing is lower priority until jstest is gated.

---

## 5. The failure→regression automation (the part that needs real building)

What we get **free** from Go: for any `testing.F` target (#1–#4, #7, #8-unit), a failing
input is written to `testdata/fuzz/FuzzName/<hash>` and **re-runs as a deterministic unit
test on every `go test`** with no extra code. Diagnose → fix → the same file is the
regression. For maybe 60% of serf's fuzzable surface, "auto-promotion" is *already solved*
by the toolchain. The toolkit should **lean on this** and not reinvent it.

What needs building is the promoter for the **stateful / non-`testing.F`** failures
(`rapid` sequences, HTTP op-sequences, JS). A minimal, honest design — five stages, each a
small, testable unit (this is the reusable core of the eventual skill):

1. **Capture.** On a `rapid`/sequence failure, serialize the *minimized* failing artifact
   `rapid` already produced: the op list + seed + the concrete inputs. (rapid's automatic
   shrinking gives us minimization for free; for `testing.F`, `go test -fuzz` minimizes.)
2. **Oracle classification.** Tag the failure by oracle: `panic` (stack), `invariant`
   (which named invariant), `error-shape` (validate said clean but handler diverged),
   `wedge/timeout`, `http-5xx`, `path-escape`. The oracle catalog is per-surface but the
   *tagging contract* is portable.
3. **Dedup.** Bucket by a signature = `(oracle-tag, top-N normalized stack frames)` —
   the CERT-BFF / ClusterFuzz stack-hash approach (N≈3–5, project-path-normalized). One
   canonical case per bucket; skip if the bucket already has a committed regression test.
4. **Flake-guard (the critical discipline).** Re-run the minimized case **K times**
   (e.g. 5) with the **fixed seed** and, separately, confirm determinism. Promote **only**
   if it fails all K deterministically. A case that fails non-deterministically is
   *quarantined* (logged, not committed) — never promote a flake into the suite. This is
   the rule that keeps the gate trustworthy and is the hardest thing the big tools punt on.
5. **Emit + name.** Render a Go test from a template: a deterministic
   `TestRegression_<surface>_<oracleTag>_<shorthash>` that replays the captured op-sequence
   against the same seam (`Dispatch`/`ExecuteCall`/`Handler`) and asserts the **fixed**
   behavior (no panic / invariant holds / clean error). Drop it next to the surface's tests
   and (optionally, behind a flag) `git add` + commit with a provenance trailer. For
   `testing.F` surfaces, "emit" is a no-op — just keep the corpus file.

Abstraction seam (this is what makes it portable): a `Promoter` needs four project-supplied
hooks — **`Minimize`**, **`Signature`** (for dedup), **`Replay`** (rebuild + drive the
seam), and **`Emit`** (test template + path + naming + run command). Everything else
(flake-guard loop, bucket store, commit) is generic.

---

## 6. Schema-driven generation & the OpenAPI question

Because serf already has machine-readable schemas (§3), two routes:

- **Native route (recommended):** write a `schema→rapid.Generator` once (~150–250 LoC)
  that walks a JSON Schema (`type`, `properties`, `required`, `enum`, `additionalProperties`)
  and produces both schema-valid and schema-adjacent values. Feed it the tool
  `Definition.Parameters` maps and the reflected appwire `Params` types. This stays
  in-process, in Go, drives the real seams (`ExecuteCall`/`Dispatch`), and integrates with
  the §5 promoter. No external service, no spec-drift.
- **External route (optional, HTTP only):** generate an OpenAPI doc from the appwire
  catalog + hub routes and point **schemathesis** or **RESTler** at a running `serf-hub`.
  RESTler's producer-consumer dependency inference (create a thread → use its id) maps
  naturally onto serf's `threadId`/`turnId` flow and would find resource-ordering bugs the
  in-process fuzzer might miss. Cost: an OpenAPI generator (~200 LoC) + harness to boot a
  hub + ingest crashes back through the §5 promoter. Worth it **later**, not first — it
  duplicates coverage the native route already gets and adds a heavy dependency.

Verdict: build the native schema→generator; keep schemathesis/RESTler as an optional
Phase-4 add-on for the HTTP surface only.

---

## 7. Gold-standard rubric vs. bespoke skill — verdict

There is no certification to pass here. The honest landscape:

- **Go Fuzzing best practices** (go.dev/doc/security/fuzz): deterministic fast targets,
  seed with `f.Add`, crashers-as-regression. *Adopt verbatim* for #1–#4/#7/#8.
- **Property-based testing discipline** (QuickCheck → `rapid`/fast-check): properties +
  shrinking + state machines. *Adopt* for #5/#6/#9.
- **Fuzzing Book** (fuzzingbook.org): grammar-based and API fuzzing chapters are the
  textbook for #6's grammar/sequence generation. *Reference.*
- **OWASP WSTG Appendix C — Fuzz Vectors**: a *vector list* (injection/overflow/format
  strings), not a methodology. *Adopt as a seed-corpus checklist* for the HTTP surface.
- **OSS-Fuzz / ClusterFuzz / CIFuzz**: corpus pruning, stack-hash dedup, CI regression.
  *Borrow the dedup + corpus-pruning patterns* (§5.3) without adopting the infrastructure.

None of these packages the **failure→regression discipline as a portable, opinionated
procedure** spanning Go + JS + HTTP with an explicit *flake-guard-before-promote* rule.
That gap is exactly skill-shaped. **Verdict: author a bespoke superpowers skill that wraps
these disciplines; do not try to adopt a single external rubric.** The skill's unique value
is the *triage/promotion procedure* and the *when-to-reach-for-it / how-to-scope-a-target*
judgment, not the fuzzers themselves.

---

## 8. Generality: portable core vs. per-project glue (the superpowers skill)

The eventual skill is **methodology-first** with thin tooling. Split:

**Portable core (language/project-agnostic):**
- The **loop**: enumerate a surface → derive a generator from its schema → run against a
  seam → detect failure via an oracle → minimize → dedup → **flake-guard** → emit a named
  regression test → (commit). Identical everywhere.
- The **triage/promotion discipline** (§5.2–§5.5), especially the flake-guard-before-promote
  rule and stack-hash dedup.
- The **schema-driven generation** concept (point a generator at whatever machine-readable
  schema exists).
- The **oracle taxonomy**: panic / invariant / error-shape / wedge / 5xx / escape.

**Per-project adapter (the glue — four hooks, mirrors §5's seam):**
- **Schema source**: where structure comes from (serf: tool `Definition.Parameters` +
  reflected `appwire.Methods`; another project: OpenAPI, TS types, protobuf).
- **Seam / driver**: how to invoke one operation (`ExecuteCall`/`Dispatch`/`Handler`).
- **Oracle set**: what counts as a failure for this surface.
- **Emitter**: test template + naming + file location + the run/build command + corpus
  location (serf: a `*_test.go` next to the surface; `make test` per-module; `testdata/fuzz/`).

**Per-language fuzzer behind the common methodology** (inherently per-ecosystem):
- Go: `testing.F` (coverage-guided, free auto-promotion) + `rapid` (stateful/structured).
- JS: **fast-check** (property/model-based) or **jsfuzz** (coverage-guided) for the hub
  renderer; its own emitter writes `test-*.js`.
- Python/other adopters: **hypothesis** + **schemathesis**.
What generalizes: the loop, the triage/promotion discipline, schema-driven generation, the
oracle taxonomy. What's inherently per-language: the fuzzer engine, the corpus format,
and the test-emission syntax — all behind the four-hook adapter.

**Skill sketch (what to actually write):**
- *Name / trigger:* "fuzzing-an-api-surface" — reach for it when a surface parses untrusted
  or model-generated input (wire protocols, tool-arg JSON, upstream SSE, HTTP bodies), or
  after a parser/dispatch bug to lock it down.
- *Procedure:* (1) pick a surface + its seam; (2) find/derive the schema; (3) write the
  thinnest `testing.F` first and harvest free auto-promotion; (4) add oracles beyond "no
  panic"; (5) for stateful surfaces, model the state machine and use PBT; (6) wire the
  promoter with **flake-guard before promote**; (7) gate it in CI (seed corpus under
  `-short`, longer campaigns nightly).
- *Travelling tooling:* the `schema→generator` lib, the `Promoter` with its four-hook
  adapter interface + a Go and a JS emitter template, a seed-corpus checklist (OWASP fuzz
  vectors + project edge values), and a short "don't promote flakes / dedup by stack-hash"
  rubric.

---

## 9. Recommended build — phased, with LoC

Estimates assume a frontier LLM doing the work; LoC, not wall-clock (per Jesse).

- **Phase 0 — Free Go-native wins (serf).** Targets #1, #2, #3, #4. Add a `make fuzz`
  target and seed corpora; CI runs the seed corpus under `-short`. Delivers real crasher
  auto-promotion immediately. **~250–450 LoC.** *Highest value/LoC ratio — do this first.*
- **Phase 1 — Schema→generator + adversarial tool fuzz (#5).** The `schema→rapid.Generator`
  off `Definition.Parameters`, plus oracles beyond no-panic (validate/handler agreement).
  **~400–700 LoC.**
- **Phase 2 — Stateful appwire sequence fuzzer (#6).** `rapid` state machine over the
  session/turn/job model driving `Router.Dispatch`; invariant oracles (no wedge, status
  monotonicity). **~500–900 LoC.**
- **Phase 3 — Auto-promotion harness (§5).** Capture/minimize-passthrough, oracle tagging,
  stack-hash dedup, **flake-guard**, named-test emitter (Go). This is what turns Phases 1–2
  failures into permanent tests. **~400–700 LoC.**
- **Phase 4 — HTTP surface + provider metamorphic (#7, #8).** httptest-level fuzz +
  metamorphic SSE harness; *optional* OpenAPI-gen + schemathesis add-on. **~400–700 LoC**
  (+200 if OpenAPI route).
- **Phase 5 — Extract the superpowers skill.** Generalize Phases 0–3 behind the four-hook
  adapter; write the methodology doc; add a JS emitter + a fast-check renderer example
  (#9). **~600–1,000 LoC/prose** (skill doc ~300–500 lines + adapter/templates ~300–500).

Serf-side to a self-promoting loop across Go surfaces (Phases 0–3): **~1,550–2,750 LoC.**
With HTTP/provider (Phase 4): **~1,950–3,450 LoC.** Skill extraction (Phase 5): **+600–1,000.**

Recommended order: **Phase 0 → 3 → 1 → 2 → 4 → 5.** (Do the promoter early, right after the
free wins, so Phase-1/2 failures are permanent from day one rather than transient `rapid`
output.)

---

## 10. Risks & open questions

- **Flake-guard is load-bearing and serf has timing-sensitive seams.** The two-wave test
  runner exists *because* TUI/tmux tests are timing-sensitive; the SSE timeout path and
  job/watch machinery are concurrency-heavy. The promoter **must** quarantine
  non-deterministic failures or it will pollute the gate. This is the top risk; budget the
  flake-guard (§5.4) generously.
- **Stateful fuzzing needs a faithful model.** The appwire state machine (turn lifecycle,
  queue, interrupt, compaction) is the hard part of Phase 2; an inaccurate model yields
  false positives (illegal sequences that *should* error). Mitigate by deriving legal
  transitions from the existing scope/lifecycle docs and only asserting "never panic /
  never wedge / status invariants" before asserting richer behavior.
- **Oracle weakness for "valid-but-wrong".** "No panic" is a thin oracle; the real bugs
  (e.g. numeric-narrowing, validate/handler divergence, `additionalProperties:true`
  pass-throughs) need *semantic* oracles. The schema gives us "is this schema-valid"; the
  divergence oracle (validated-clean but handler-misbehaves) is where the §3 soft spots pay
  off — invest there.
- **JS surface is outside CI.** jstest isn't gated; Phase 5's JS promoter has lower ROI
  until jstest joins the gate. Note, don't block on it.
- **Provider fuzzing is metamorphic, not differential** — set expectations accordingly
  (§4); don't promise cross-provider differential bug-finding.
- **`envvars` module isn't in the test gate** — any fuzz target there wouldn't run under
  `make test`; either add it to `GO_MODULES` or skip the module.
- **Auto-commit of generated tests** needs a guardrail (provenance trailer, human-reviewable
  diff, opt-in flag) so the suite never silently absorbs a misclassified case.

---

## Appendix — prior art (real names & links)

Go-native fuzzing & best practices:
- Go Fuzzing (official): https://go.dev/doc/security/fuzz/
- First-Class Fuzzing design draft: https://go.googlesource.com/proposal/+/master/design/draft-fuzzing.md
- "What you need to know about fuzz testing and Go" (opensource.com): https://opensource.com/article/22/1/native-go-fuzz-testing
- Best practices on Go fuzzing (dev.to/kevwan): https://dev.to/kevwan/best-practices-for-go-fuzzing-in-go-118-4ic8

Property-based / stateful testing:
- `pgregory.net/rapid` (stateful, automatic shrinking): https://pgregory.net/rapid/ · https://github.com/flyingmutant/rapid
- rapid state-machine example: https://github.com/flyingmutant/rapid/blob/master/example_statemachine_test.go
- fast-check (JS, model-based + fuzzing): https://github.com/dubzzz/fast-check
- jsfuzz (coverage-guided JS): https://github.com/fuzzitdev/jsfuzz
- QuickREST (PBT generation from OpenAPI): https://arxiv.org/pdf/1912.09686

Schema/spec-driven REST fuzzing:
- RESTler — Stateful REST API Fuzzing (MSR): https://www.microsoft.com/en-us/research/publication/restler-stateful-rest-api-fuzzing/ · paper https://patricegodefroid.github.io/public_psfiles/icse2019.pdf · code https://github.com/microsoft/restler-fuzzer
- RAFT (RESTler in CI/CD): https://github.com/microsoft/rest-api-fuzz-testing
- WuppieFuzz — coverage-guided stateful REST: https://arxiv.org/pdf/2512.15554

Grammar/API fuzzing & textbook:
- The Fuzzing Book: https://www.fuzzingbook.org/ — API fuzzing chapter https://www.fuzzingbook.org/html/APIFuzzer.html — grammars https://www.fuzzingbook.org/html/Grammars.html

Differential / metamorphic:
- JEST (N+1 differential testing of JS engines): https://arxiv.org/pdf/2102.07498
- "Deriving Semantics-Aware Fuzzers from Web API Schemas": https://arxiv.org/pdf/2112.10328

Crash dedup / minimization / continuous fuzzing:
- ClusterFuzz: https://github.com/google/clusterfuzz · coverage-guided vs blackbox https://google.github.io/clusterfuzz/reference/coverage-guided-vs-blackbox/
- OSS-Fuzz ideal integration (CIFuzz, regression corpus): https://google.github.io/oss-fuzz/advanced-topics/ideal-integration/
- Igor — Crash Deduplication via Root-Cause Clustering: https://hexhive.epfl.ch/publications/files/21CCS.pdf
- Semantic Crash Bucketing: https://par.nsf.gov/servlets/purl/10081984

Rubric/standards:
- OWASP Web Security Testing Guide — Appendix C, Fuzz Vectors: https://github.com/OWASP/www-project-web-security-testing-guide/blob/master/latest/6-Appendix/C-Fuzzing.md
- SoK: Prudent Evaluation Practices for Fuzzing: https://arxiv.org/pdf/2405.10220
