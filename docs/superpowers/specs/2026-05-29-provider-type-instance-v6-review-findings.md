# v6 Adversarial Review — Consolidated Findings

Date: 2026-05-29
Ticket: PRI-1880
Subject: `2026-05-29-provider-type-instance-model-design.md` (v6, commit 31a45bfb)

A: 9 serious; B: 7 serious. **A wins on count; B's `preserveBaseOverrides`
regression is the single sharpest finding.** Most important: **the architecture
is validated** — switching-to-session is confirmed cycle-free and profile-swap-safe;
the leaf-package layering holds. The remaining issues are (1) a handful of real
refinements and (2) **yet more inventory misses** — the recurring,
asymptotic-but-never-zero completeness problem.

## Validated (no longer findings — both reviewers confirmed against code)

- **Switching-to-session is cycle-free.** `go list -deps ./llm` shows `llm`
  imports zero serf packages; `cmdutil` imports `agent` but `agent` does **not**
  import `cmdutil`; `agent` already imports `llm` for the catalog. So
  `agent.ResolveProfileFromConfig` + `cmdutil.SelectProfile` wrapping it is acyclic.
- **`SetModel` profile-swap is safe.** It already swaps `s.profile`, updates
  `contextMgr.SetProfile`, and rebuilds tool/prompt caches (`session.go:1288-1293`).
- Catalog ingest (`:243`, keep) vs lookup-alias (`:67`, drop) correctly coupled
  with the gemini-instance rename; `RewriteErrorProvider` empty-Provider no-op
  preserved; the 13 direct `client.Complete` sites are routing-only (no behavior
  key) — non-issues.

## Real refinements for v7

1. **[SERIOUS] The §4.2 table encodes a broken fallback guard.** The row swaps
   `fbProfile.ID()` → `.BehaviorTag()` at `session.go:4140` but leaves
   `fbProfile := s.profile.WithModel(fbModel)`. Since `WithModel` no longer
   switches (§4.6), `fbProfile.BehaviorTag()` always equals
   `s.profile.BehaviorTag()` → the guard **never fires** → a cross-tag
   `model_fallbacks` entry is accepted and **mis-routes at the execution site
   `session.go:2706`** (also `fbProfile := WithModel(...)`, omitted from the
   table). **Fix:** both the guard AND the execution resolve `fbModel` via
   `ResolveProfileFromConfig`, and the §4.2 table must say so. *(A1+A2, B4 — both.)*

2. **[SERIOUS] `ResolveProfileFromConfig` drops `preserveBaseOverrides`.** Today
   `WithModel`'s switch arm calls `preserveBaseOverrides(switched, p)`
   (`profile.go:540`) to carry `WithCommunicateOutputSchema`/`WithAllowedDecisions`
   across a switch; the comment (`:555-559`) explicitly warns that without it
   "`SetModel` and subagent model overrides would silently revert the communicate
   schema." v6's session-level resolver builds a fresh profile and never
   re-applies these. A daemon with `--output-schema`/`SERF_ALLOWED_DECISIONS` that
   does a cross-instance `/model` (or subagent/fallback) silently loses the
   override. **Fix:** the session re-applies its output-schema/allowed-decisions
   after `ResolveProfileFromConfig`. *(B1 — the standout.)*

3. **[SERIOUS] Env synthesis (§4.10) can't reproduce today's behavior without
   re-implementing the opaque factories.** `EnvAdapterFactory` (`env_registry.go:22`)
   returns a built adapter exposing no type/url/key. To synthesize a `Config` from
   env, you must independently re-read every env var AND replicate: the
   import-order default (anthropic when both keys set — `client.go:41-45`),
   `NonDefaultEligible` (ollama), **ollama always-on** (`ollama/adapter.go:186`),
   the **openrouter dual-registration** (one `OPENROUTER_API_KEY` →
   `openrouter` + `openrouter-anthropic`), and OAuth-first detection
   (`openai/adapter.go:80-121`). **Recommended fix:** *don't synthesize* — the
   env-fallback path keeps `NewFromEnv` + the existing `SelectProfile` resolution,
   with `NameToTag` = identity (env instances are named == their type == tag). The
   new `Config` resolver applies only when `providers.toml` exists. This dissolves
   the synthesis-reimplementation problem entirely. *(A5+A6+A7, B5+B6 — both,
   multiple angles.)*

4. **[SERIOUS] Stamp error identity at the `llm` stream layer, not just
   `session.go:3493`.** `llm.StreamGenerate` (`stream_generate.go:208`) drains the
   stream and captures `ev.Err` itself, outside the session (used by
   `llmcall`/`serfeval`/`StreamGenerateObject`). §4.3's session-only stamp misses
   it. **Fix:** stamp in the `llm.Client` stream wrapper (wrap `Events()`), which
   covers the session **and** `StreamGenerate` in one place. *(B3.)*

5. **[SERIOUS] `diagnostic.isProviderFailure` hardcodes the provider list (+ a JS
   mirror) — a new inventory miss, worsened by §4.3 restamping.**
   `internal/diagnostic/diagnostic.go:155-171` matches `provider+" error"` against
   `{openai,anthropic,google,gemini,openrouter,ollama,kimi,glm,minimax}`; after
   §4.3 stamps the **instance name**, `"work error (status=403)"` matches nothing
   → misclassified as a hub/serf failure (wrong user remediation). Duplicated in
   `cmd/serf-hub/assets/diagnostics.js:108-111`. **Fix:** classify on the
   structured `llm.Error` (which carries provider + the new behavior tag), not the
   message string; re-key the JS mirror. *(A4, B2 — both.)*

6. **[SERIOUS] `AuthRecord.Validate()` can't see the tag.** It's a method on the
   record (`storage.go:138-158`); "validate against behavior tag openai" (§4.9)
   needs the tag. **Fix:** drop the `Provider == "openai"` check entirely — the
   per-instance filename scopes the record, and OAuth is only offered for
   openai-tag instances at the service/UI layer, so the record validator doesn't
   need to re-check the provider. *(B8.)*

7. **[SERIOUS/latent] Provider-conditional tool registration isn't re-run on
   switch.** The gemini native `web_search` tool is registered in
   `registerCoreTools` (`session.go:4785`), which `SetModel`/`rebuildToolDefsCache`
   never re-runs. Switching to/from a google instance won't add/remove it. The
   switching-to-session move makes interactive switch-to-gemini prominent. **Fix:**
   `SetModel` re-evaluates provider-conditional tool registration. *(B latent
   flag.)*

8. **[SERIOUS] `hubStateRoot` resolver lives in `cmd/serf-hub` (`package main`),
   unreachable from standalone `serf`; a custom hub root diverges.** §4.10 says
   both read `$hubStateRoot/providers.toml`, but `DefaultHubStateRoot`
   (`config.go:51`) can't be imported by `cmd/serf`, and a `hub.toml`-customized
   root means a directly-invoked `serf run` (no `SERF_PROVIDERS_CONFIG`) reads
   `~/.serf` while the hub reads the custom root. **Fix:** move the path resolver
   to `internal/providerconfig`; document that hub-spawned daemons get
   `SERF_PROVIDERS_CONFIG` (so they match) and a custom-root hub requires the env
   for direct `serf run`. *(A9, B7 — both.)*

9. **[MINOR] `WithModel` same-instance rebuild needs the tag threaded.** The
   rebuild `NewOpenAICompatProfile(p.id, model, 0)` (`profile.go:568`) uses `id`
   for routing AND the catalog/Codex-gate, which now key on the tag. The
   constructor must take instance name + tag. *(A10.)*

10. **[MINOR] `anthropicProfile.WithModel` self-prefix strip needs the instance
    name** (like `decidePrefixAction`); §4.2 row `:649` mentions only the tag.
    *(B10.)*

11. **[MINOR] `adapterRecipe` for `minimax`/`openrouter-anthropic` wraps the
    *anthropic* adapter** (custom base URLs), not openaicompat+quirks; §4.4's
    "quirks preset from type" covers only kimi/glm/openrouter. Spell out the recipe
    = {which adapter, base URL, quirks, label} per type. *(B9.)*

12. **[MINOR] `cmd/serf-tui` provider-literal sites:** `model_display.go:7-13`
    `abbreviateModel` strips a hardcoded prefix list (called raw at
    `hub_model.go:4142`); `hub_status.go:248`/`hub_auth.go:12`/`hub_model.go:877`
    default OAuth provider to `"openai"`. Display/default-only, degrade gracefully,
    but not inventoried. *(A12, B8-minor.)*

13. **[MINOR] §4.2 "prompt sections" lists `:3873` + `:3972`, but only `:3972`
    selects the file** (`PromptData.Provider` at `:3873` feeds no section). Harmless
    but imprecise. *(A11.)*

14. **[MINOR] `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` has no home after the openai
    fold-in; OAuth-vs-inline-key precedence undefined** for an instance with both.
    Mostly accepted §2 non-goals; document. *(B11.)*

## The meta-pattern (why this matters)

Six rounds. The architecture has been stable and validated for two rounds. But
**every round still finds new inventory sites** that key on the provider string
(this round: `diagnostic.go` + its JS mirror, `StreamGenerate`, `abbreviateModel`,
the TUI OAuth defaults, the gemini `web_search` tool). These are precisely the
sites a **compiler + a renamed-instance integration test** ("create an `openai`
instance named `work`, run the full suite + a live session, assert nothing keys on
the literal `openai`") would surface **exhaustively in one pass**. Prose review
finds them one or two at a time and never reaches zero. The completeness problem is
asymptotic under review and decidable under build+test.
