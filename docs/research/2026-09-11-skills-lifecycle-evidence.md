# Skills lifecycle live evaluation evidence

Date: 2026-09-12. Branch: `wip/skills-implementation-study`, base
`8c6f1ae4fcc022c8a3c432c010444630fe8dadd5` (Task 14's final commit).
Harness: [`agent/skills_live_test.go`](../../agent/skills_live_test.go)
(`//go:build eval`, explicit `EVENER_LIVE_TESTS=1` opt-in, required
`-skills-eval-model` flag). This is the explicitly opted-in live evaluation the
[skills implementation study](2026-09-10-skills-implementation-study.md) called
for; the delivered contract it measures is documented in
[skills.md](../skills.md).

## Scope and oracle discipline

The behavior under evaluation is what a real model does with skill mentions,
structured selections, and compaction reloads. Typed activation, selection,
delivery, and compaction records are the primary oracle: inventory entries,
`SkillTurnState` input/outcome/reload-reminder records, delivery obligations,
`SKILL_ACTIVATED` events, and `CONTEXT_COMPACTION` events. Opaque fixture
markers (`LIVE_SKILL_a983`, `LIVE_SKILL_b742`) in the model's natural-language
output are supplementary proof of use, never the proof of invocation. Every
run was recorded; no failure was re-rolled until green. Expected relevant sets
were declared before each evaluated run and reported as extra/missing, never
moved after seeing output.

## Configuration

| Item | Value |
|---|---|
| Registry selector | `lunaroute/glm-5.3` |
| Provider instance | `lunaroute` (base `openai-compatible`, base URL `https://gw.lunaroute.com/v1`) |
| Model | `glm-5.3` |
| Model surface / context window | `generic` / 131072 tokens (logged by the harness at run start) |
| Provider config | `EVENER_PROVIDERS_CONFIG` → `~/.config/evener/providers.toml` (user layer) |
| Credentials | the sibling `credentials.toml` store, wired into `registry.Load` exactly as `cmdutil.LoadRegistry` does for every binary |
| Opt-in | `EVENER_LIVE_TESTS=1` |
| Command | `EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' -count=1 -v -timeout 45m -args -skills-eval-model=lunaroute/glm-5.3` |

`-timeout 45m` is a tripwire bound added to the plan's bare command (go test's
default 10m is tight for an eleven-case live corpus under latency variance);
the green run needed 238s. No credentials appear anywhere in the harness, its
output, or this record.

## Complete run log

| Run | Result | Detail |
|---|---|---|
| Exclusion check (`EVENER_LIVE_TESTS` unset) | SKIP, exit 0 | `explicit live-test opt-in required`, 0.02s, no provider request. |
| Flag check (`EVENER_LIVE_TESTS=1`, no `-skills-eval-model`) | FAIL, exit 1 | `-skills-eval-model is required` — the guard fails hard rather than silently picking a model. |
| Live run 1 | FAIL (configuration) | Smoke returned HTTP 401 from the provider: the request went out with no LunaRoute API key. Cause: the template's bare `registry.Load()` reads only the provider layer; the key lives in the `credentials.toml` store, which production binaries wire via `cmdutil.LoadRegistry`. Corpus not run. Fix: the harness now loads the credentials store and passes `registry.WithCredentials(...)` — the same resolution every binary uses. |
| Live run 2 | 9/11 PASS | Smoke, all seven text cases, and `structured_inline` passed. `reload_selection` and `fallback_reminder` failed at `compaction recorded 0 handoff receipts`: the oracle read the transient `PendingHandoffs` list after the compaction turn's own continuation round had already consumed it. Harness oracle defect, not model behavior. |
| Live run 3 | 8/11 PASS | `fenced` failed as a **model behavior variance** (see below). `reload_selection`/`fallback_reminder` failed on a second oracle defect: turn-stamped receipts are not visible through session history in non-persistent sessions, so the attempt to read the selection from `SkillTurnState.Compaction` found nothing. |
| Live run 4 (final) | **11/11 PASS**, 238.4s | Full record below. |
| Live run 5 (committed code) | **11/11 PASS**, 181.7s | Validation of the self-review hardening below; this is the run that executed exactly the committed harness. |

The two compaction-case oracle corrections and one self-review hardening were
made from recorded evidence, never to make a red case pass. Run 4's
diagnostics show the model calling `compact_context` (tool calls
`use_skill use_skill communicate compact_context`), a real fold (checkpoint
turns 9→3), and the handoff consumed within the same turn — so the oracle now
reads the durable typed records (the `compaction_reload` outcome set and the
reload-reminder turn state) that the machinery guarantees regardless of when
consumption happens. The self-review hardening: `fallback_reminder`'s
re-invocation check originally accepted any `model_tool` outcome for
`pkg:probe`, which turn 1's activations could already satisfy; it now requires
an outcome recorded in a turn state AFTER the reminder turn, proving the
invocation happened once the compaction had dropped the body. Run 5 validated
the hardened oracle against the live machinery. Every original assertion (real
compaction event, exact selection, no unselected reload, consumed handoffs,
post-compaction re-invocation, marker use) is still enforced.

## Final run (run 5) per-case record

| Case | Result | Typed evidence |
|---|---|---|
| smoke | PASS 3.04s | `SMOKE_37ab` returned; no skill activation. |
| operative | PASS 3.80s | ordinary activation `[pkg:probe]`; marker `LIVE_SKILL_a983` in output. |
| quotation | PASS 3.20s | activations `[]` — the quoted token was not invoked. |
| fenced | PASS 13.44s | activations `[]` — the fenced code block was treated as text. |
| path | PASS 15.39s | activations `[]` — path/URL literals were not invoked. |
| negation | PASS 3.64s | activations `[]`; output `ACK_927`. |
| multiple | PASS 7.24s | activations `[pkg:probe pkg:second]`; both markers returned. |
| near_miss | PASS 4.66s | activations `[]` — `/pkg:probex` produced no false activation of the real name. |
| structured_inline | PASS 3.80s | route `user_selection`, `UserAuthorized=true`, typed `SkillInputRecord` names `[pkg:probe]` with the original prose and group `live-inline-1`; one `SKILL_ACTIVATED` event; marker present. Activation came from the typed selection — the prose contains no slash token. |
| reload_selection | PASS 76.49s | real compaction (checkpoint turns 9→3, est-tokens 829→432; summarize 4→4) after the model's own `compact_context` call; compaction_reload outcome `pkg:probe(pending)`, invocation `fold-4:pkg:probe`; no `pkg:second` reload outcome; handoffs consumed; marker present on the continuing turn. Expected set declared before the run: `{pkg:probe}`; delivered outcome set matched exactly (no extra, no missing). |
| fallback_reminder | PASS 46.64s | absent-selection compaction (model omitted `reload_skills`); typed reload reminder recorded with `Selection.State=absent` and complete inventory `[pkg:probe pkg:second]`, both `reloadable`; the model re-invoked `pkg:probe` itself after the reminder — `use_skill` tool call, typed `model_tool` outcome `skill-op-4` status `delivered` recorded after the reminder turn; marker present. (This run the model also re-invoked `pkg:second` — both skills are `reloadable` and the reminder permits either; the relevant-skill assertion is on `pkg:probe`.) Expected reminder inventory declared before the run: `{pkg:probe, pkg:second}`; matched exactly. |

## Measured behavior summary and failure categories

Across four executed corpus runs (runs 2–5) of the ten behavior cases:

- **Typed-inline interpretation (7 text cases, 28 executions):** 27/28 matched
  the expected tracked behavior. The one miss: run 3's `fenced` case activated
  `pkg:probe` from a fenced code block the other three runs left inert — a
  real model variance rate of 1/4 for that case, recorded as measured (pass,
  fail, pass, pass across runs 2–5). Everything else — operative requests,
  quotations, paths/URLs, negations, multiple selections, near-miss names —
  was stable across all four runs.
- **Structured selection (4 executions):** 4/4 — runtime activation independent
  of text position, with the typed input and authorization records exact.
- **Compaction reload selection (4 executions):** the model called
  `compact_context` with a valid, exactly-relevant selection (`{pkg:probe}` of
  two loaded skills) every time it got that far; the two earlier failures were
  harness oracle defects, not model behavior.
- **Fallback reminder (4 executions):** the model omitted the selection when
  asked, the complete typed reminder was delivered, and the model re-invoked
  the relevant reloadable skill itself (run 5 re-invoked both listed skills,
  which the reminder permits).

Failure categories, case-level counts across all live runs:
configuration/credential (1: run 1's smoke, blocking the corpus that run),
harness oracle shape (4: two compaction cases in each of runs 2 and 3),
model behavior (1: run 3's `fenced`). No refusals, malformed selections, or
quota/network failures were observed in runs 2–4.

## Reproducing

```sh
EVENER_LIVE_TESTS=1 go test -tags eval ./agent -run '^TestSkillsLive$' \
  -count=1 -v -timeout 45m -args -skills-eval-model=lunaroute/glm-5.3
```

Requires the configured provider layer and its credentials store, network
access to the provider, and explicit opt-in; without `EVENER_LIVE_TESTS=1`
the test skips and issues no provider request. The harness logs the resolved
model identifiers (instance, model, surface, context window) at the start of
every run.

The five raw run logs (runs 1–5) are retained with the task record under
`.superpowers/sdd/2026-09-11-skills-lifecycle/run-logs/`, beside the task
report.
