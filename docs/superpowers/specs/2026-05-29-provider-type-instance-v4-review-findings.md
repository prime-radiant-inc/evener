# v4 Adversarial Review — Consolidated Findings

Date: 2026-05-29
Ticket: PRI-1880
Subject: `2026-05-29-provider-type-instance-model-design.md` (v4, commit 47497bcb)

Two independent reviewers. **A: 8 serious; B: 10 serious — B wins** (more uniques:
the Gemini-id routing break, `launchProviderAllowsUnreportedModels`, the OpenRouter
dual-route migration, the §4.9-vs-§4.11 OAuth keying contradiction). 5 points to B.

**Convergence signal:** both reviewers verified that **all 12 v3 findings are
genuinely resolved** in v4 (system-prompt section coupling, main-request site,
resume-by-lookup, `normalizeProviderName` trim/lower, per-instance OAuth threading,
the every-spawn launch-check, the OAuth-is-global correction). The remaining issues
are mostly **new** (introduced by v4's behavior-tag/selector/storage mechanisms) or
a few **more** missed coupling sites — and they cluster into three themes.

## Theme A — package layering for tag/selector/config (architectural)

1. **[SERIOUS] Selector injection has an import cycle.** `ProviderProfile` is an
   `agent` type (`agent/profile.go:40`); `llm` does **not** import `agent` (would
   cycle). §4.6/§4.11 put the `selectInstance(name,model)(ProviderProfile,error)`
   selector in `llm.NewFromProviders` — `llm` cannot return `agent.ProviderProfile`
   or call `agent.New*Profile`. The selector must live in `agent`/`cmdutil` (where
   `SelectProfile` + the constructors are); `NewFromProviders` builds only the
   `*llm.Client` (adapters). The instance config must be threaded into **both**
   layers. *(Both reviewers — the headline v4 error.)*

2. **[SERIOUS] Behavior tag is unreachable at client-only sites.** The picker /
   launch-check / launch-validation sites operate on `client.ProviderNames()`
   (instance names); `ProviderAdapter` exposes only `Name()/Complete/Stream` and
   `*llm.Client` has no tag/type accessor. §4.2 says "re-key on behavior tag" for
   `web.go:2038/2064`, `launch_check.go:104/222`, `app_rpc.go:1526` — but the tag
   isn't in scope there. Fix: thread the instance config's name→tag map to those
   sites (they all have the config available), not via the client. *(B serious; A
   minor.)*

**Resolution direction (mechanical, can spec without a decision):** put the
instance-config type in a **leaf package** both `llm` and `agent`/`cmdutil` import
without cycle; `llm` builds adapters from it; `cmdutil`/`agent` build profiles +
the selector from it; client-only sites consult its name→tag map; `cmd` wires both.

## Theme B — model-string prefix resolution (needs a decision)

3. **[SERIOUS] Meta-provider namespace collision.** §4.6 "switch when the prefix
   names a configured instance; keep when it's an upstream namespace" breaks because
   the **default migrated instance names** (`anthropic`, `google`, `openai`,
   `minimax`) **are exactly** OpenRouter's upstream namespaces. So on an
   `openrouter` session, `WithModel("anthropic/claude…")` — today `prefixActionKeep`
   (`profile.go:405-409`) — would now **switch to the `anthropic` instance**,
   defeating OpenRouter routing. Today's code avoids this because the switchable set
   is a hardcoded closed set (`ollama/kimi/glm/openrouter/openrouter-anthropic`,
   `profile.go:406`). *(Both.)*

4. **[SERIOUS] Catalog lookup keyed on `id` (→ instance name).**
   `resolveOpenAICompatCatalogModel` (`profile.go:954-966`) looks up
   `id + "/" + model` and `suppressBareCatalogLookup(id)` (`:929`, `id=="ollama"`).
   With `id`=instance name, a renamed kimi/glm/openrouter instance misses its
   catalog entry (→ 128K default / wrong effort levels), and a renamed ollama
   instance loses bare-lookup suppression (re-introducing the silent
   context-truncation bug the code exists to prevent). §4.2 caught `:930`/`:1007`
   but not the catalog-prefix lookups at `:955/964`; these need the **type/tag**,
   not the instance name. *(Both.)*

**The deep observation:** cross-provider fallback is **already disallowed**
(`session.go:4140` errors if `fbProfile.ID() != profile.ID()`), so `WithModel`'s
cross-provider *switch* path is largely vestigial. Dropping/constraining it would
dissolve #1 (no selector to inject) and #3 (no instance-name prefix ambiguity).
**Decision needed (see report).**

## Theme C — migration / storage ownership (needs a decision)

5. **[SERIOUS] Migration is non-deterministic: env is per-process, not global.**
   §4.10 claims "every source is global → whichever process migrates produces the
   same file." False: hub (launchd/systemd) and standalone `serf` (shell) are
   separate binaries with different env (`OPENAI_API_KEY` etc.). flock+atomic
   prevents a torn file but not divergent content. *(Both.)*

6. **[SERIOUS] No usable cross-process lock exists.** The only flock
   (`cmd/serf-hub/flock.go`) is `package main` (unimportable by `cmd/serf`) **and**
   `LOCK_NB` (loser errors, doesn't wait-and-read). §4.10/§6 "the loser sees the
   winner's file" needs a new shared **blocking** primitive that doesn't exist.
   *(Both.)*

7. **[SERIOUS] OpenRouter migration drops a route: one env var → two providers.**
   `OPENROUTER_API_KEY` registers **both** `openrouter` and `openrouter-anthropic`
   adapters (`openrouter/adapter.go:40`, `openrouter_anthropic/adapter.go:60`).
   §4.10's 1:1 env→instance mapping emits only `openrouter`, losing the
   Anthropic-Messages route — contradicting §5 "resolves the same models." *(B.)*

**Resolution direction (needs a decision):** make migration **hub-owned** (single
writer; the hub has `credentials.toml` and is the management surface). Standalone
`serf` **reads** `providers.toml` but does not migrate — dissolving #5 and #6. Open
question: what does standalone serf do if `providers.toml` is absent **and** the hub
has never run? **Decision needed (see report).**

## Residual inventory misses (fold into v5)

8. **[SERIOUS] `NewGeminiProfile` hardcodes `id:"gemini"`** (`profile.go:699`).
   Deleting the `gemini`→`google` alias (§4.2) while the constructor still stamps
   `"gemini"` → `req.Provider="gemini"` → `c.providers["gemini"]` miss → unknown
   provider on **every** Gemini request. Also `CheapModel case "gemini"` (`:349`)
   and `WithModel case "google","gemini"` (`:527,643`): re-keying `switch p.id`→
   `switch behaviorTag` without changing the case value from `"gemini"` to
   `"google"` (the tag) silently breaks. *(B; A flagged the CheapModel half.)*

9. **[SERIOUS] Error-label identity is ~20 hardcoded literals across 5 adapters**
   (`WrapContextError`/`ErrorFromHTTPStatus`/`NewStreamError`/
   `NewUnsupportedToolChoiceError` in openai/anthropic/google/openaicompat). §4.3
   names only `NewStreamError`/`RewriteErrorProvider`. "Rewrite centrally" needs new
   client-layer wiring (`RewriteErrorProvider` no-ops on empty Provider and is
   ollama-only today). Note: `resp.Provider` itself has almost no production reader
   (only `llmcall` + the dead guard; the API log uses `req.Provider`), so the real
   identity surface is the error label, which §4.3 under-scopes. *(A.)*

10. **[SERIOUS] `launchProviderAllowsUnreportedModels` hardcodes
    `"openrouter-anthropic"`** (`app_rpc.go:1526`, called `:1518` with the instance
    name). A renamed openrouter-anthropic instance has its models rejected. Same
    class as the picker skips but a different site in neither inventory. *(B.)*

11. **[SERIOUS] Auth surface larger than stated + an internal contradiction.**
    Five type→env/mode maps, not four (the 5th: `cmdutil.go:266 providerEnvConfig`).
    Uninventoried branches: `serf-tui/auth.go:26/27/80`, `spawn.go:466
    validateProviderCredentials` (own `openai`/`openai-compatible` branches +
    `LoadAuth`). And §4.9 (OAuth file = `auth/<instanceName>.json`) **contradicts**
    §4.11 ("OAuth record **by behavior tag**") — per-instance filename vs shared
    per-tag file can't both hold. *(A + B.)*

## Minor

12. **[MINOR] `resp.Provider` "single chokepoint" imprecise** — `:3511` is in
    `consumeModelStream`, not `callModel`; two edits, not one. (Resolves v3 #16
    regardless; folds into #9 above — error labels are the real surface.)
13. **[MINOR] Mixed-case instance names break routing** — `normalizeProviderName`
    lowercases lookups (kept) but §4.4 registers under verbatim `Name()`; "Work"
    registers, "work" looked up → miss. Need a case-fold-on-create rule in §6.
14. **[MINOR] `internal/diagnostic/diagnostic.go:155-166`** hardcodes the provider
    list for bare-string error classification; custom-instance labels misclassify
    (structured fast-path mitigates).

## Verified-resolved v3 findings (no score)

Both reviewers confirmed: v3 #1 (main-request `session.go:2662`), #2 (system-prompt
`section_resolver.go:79-84`/`session.go:3972`), #3 (resume allowlist deletable;
`WebConfig` can carry the set), #16 (`resp.Provider` both paths), #17a/b
(per-instance OAuth threading + `AuthRecord.Validate` relax), #18
(`normalizeProviderName` trim/lower), the #9 OAuth-is-global correction, and #10
(every-spawn launch-check enumerated). Finish-norm genuinely needs no change.
