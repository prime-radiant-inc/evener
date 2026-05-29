# v5 Adversarial Review — Consolidated Findings

Date: 2026-05-29
Ticket: PRI-1880
Subject: `2026-05-29-provider-type-instance-model-design.md` (v5, commit d51d964b)

Two reviewers, **8 serious each (tie)**; edge to B for the fallback-guard
contradiction (the single most important finding). The headline: **the "drop
`WithModel` switching" decision is wrong — switching is load-bearing.** The good
news: the fix is consistent with the *spirit* of "drop in-profile switching"
(move switching OUT of the profile, UP to the session/selector) and the remaining
issues are wiring precision, not architecture. The design is converging.

## A. The switching reversal (the big one)

1. **[SERIOUS] Interactive `/model` cross-provider change has no selector path —
   it goes through `WithModel` on the live single-process daemon.** Trace: picker
   lists models across all providers (`web.go:2031-2073`) → `POST /model` /
   `thread/model/set` → `local_daemon.go:201-208` (no respawn) →
   `serve.go:292 SetModelFunc` → `Session.SetModel` (`session.go:1271`) →
   `s.profile.WithModel(model)` (`session.go:1277`). The daemon is bound to one
   profile; `WithModel` is the only in-process switch. v5's "interactive changes
   re-resolve through the top-level selector" path does not exist. *(Both.)*

2. **[SERIOUS] The cross-provider-fallback guard *relies on* switching.**
   `session.go:4140 if fbProfile.ID() != s.profile.ID()` trips only because
   `WithModel("anthropic/…")` returns `ID()=="anthropic"` today. Remove switching
   → `WithModel` never changes ID/tag → guard never fires → a cross-provider
   `model_fallbacks` entry is accepted and sent to the wrong adapter
   (`session.go:2705-2715`). v5 deletes the switch and depends on it in the same
   breath. *(B — the sharpest finding.)*

3. **[SERIOUS] Subagent model overrides use `WithModel` cross-provider.**
   `subagents.go:159,163 s.profile.WithModel(agent.Model)` where `agent.Model` is
   free-form frontmatter (`plugin_agents.go:68`) that can be `anthropic/claude…`
   while the parent runs openai — no fallback guard there. *(A.)*

4. **[SERIOUS] Phase-1a "zero behavior change" is impossible — tests encode the
   switch.** `profile_test.go:366 TestProviderProfile_WithModel_CrossProvider` and
   `:379` assert `WithModel` switches providers. Removing it fails them; deleting
   them is itself a behavior change. *(B.)*

**Corrected design (for v6):** keep switching, but move it out of the profile.
`WithModel` becomes within-instance only (model change / meta-provider upstream
namespace). Cross-instance switching becomes the **session/daemon re-resolving
through the instance-aware selector** (the daemon already loads the config, §4.11):
`SetModel`, subagent overrides, and `model_fallbacks` parse the first segment;
if it names a configured instance → selector builds that instance's profile; else
→ `WithModel` within-instance. The fallback guard compares the
selector-resolved behavior tags. This is faithful to "drop *in-profile*
switching" (it leaves `WithModel`) while keeping switching working, and the
selector lives in `cmdutil`/`agent` (no `llm` import cycle).

## B. Identity / error rewrite

5. **[SERIOUS] §4.3's central error-rewrite misses streamed errors.** `Stream()`
   returns `(stream, nil)` once the SSE opens; runtime errors are emitted from the
   adapter goroutine as `StreamEvent{Type: StreamEventError}` (`anthropic:775`,
   `openai:911,1175`, `openaicompat:515`, `google:559`) and surface at
   `session.go:3493-3497`, **bypassing** the `Stream()` return. The hardcoded
   `WrapContextError("anthropic"/…)` literals on the streaming path (the common
   runtime errors) survive a return-boundary rewrite. Fix: also stamp the provider
   where the session consumes stream errors (`session.go:3493`) or wrap
   `Events()`. *(Both.)*

6. **[SERIOUS/MODERATE] Removing `RewriteErrorProvider`'s empty-Provider no-op
   mislabels cancellations.** `errors.go:115-128` deliberately skips empty-Provider
   errors (`context.Canceled` via `NewAbortError`, `NoObjectGeneratedError`).
   §4.3 says remove the guard → "context canceled" becomes "work error: context
   canceled." Keep the guard; only stamp where Provider should be non-empty.
   *(B.)*

## C. Behavior-tag plumbing

7. **[SERIOUS] `req.BehaviorTag` enumeration grossly incomplete (13+ sites, not
   4).** `Provider: profile.ID()` request sites include `context_manager.go:947`,
   `fork_summarize.go:21`, `eval_probes.go:67,81`, `session_namer.go:56`, the
   `strategy_*.go` files, `tool_web_fetch.go:124`, `tool_web_search.go:20`,
   `diagnostics.go:96`; several call `client.Complete` directly without
   `applyModelRequestMetadata`. *(A.)* **Resolution:** don't thread
   `req.BehaviorTag` at all — the **profile carries the tag** (`BehaviorTag()`),
   session-level checks use `s.profile.BehaviorTag()`, and the **client derives
   the tag from `req.Provider` via the config's `NameToTag`** where llm-layer logic
   needs it (see #8). Drop the `req.BehaviorTag` field.

8. **[SERIOUS] Missed site: `classify.go:114-117` gates the OpenAI
   Responses→chat endpoint fallback on `Provider()=="openai"`.** After §4.3 makes
   errors carry the instance name, a renamed openai instance fails this and loses
   the endpoint fallback (`classify.go:88-91` → `session.go:4148`). Must key on
   behavior tag — stamp the tag onto errors at the client boundary (via
   `NameToTag`) so llm-layer logic can key on it. *(B.)*

9. **[SERIOUS] Picker/launch client-only sites have no config in the env-fallback
   path.** `launch_check.go:104/222`, `web.go:2038/2064`, `app_rpc.go:1518/1526`
   build clients via `NewFromEnv` with no `providerconfig.Config`, so no
   `NameToTag`. §4.11's helper returns only a `*llm.Client`. **Resolution:** the
   ephemeral env path must **synthesize a real `providerconfig.Config`** (instances
   named == tag) and go through `NewFromProviders`, so `NameToTag` exists in both
   paths; `WebConfig` carries the instance config. *(Both.)*

10. **[SERIOUS] `decidePrefixAction` needs BOTH instance name and tag.** Self-
    prefix *strip* compares the prefix to the **instance name** (a renamed
    anthropic instance "claude2" must strip `claude2/`); meta-provider *keep*
    compares to the **tag**. Resume reconstructs `instanceName/model`
    (`app_rpc.go:1726-1727`), so keying only on tag sends `instanceName/model` to
    the wire. *(B.)*

## D. Config / storage precision

11. **[SERIOUS] The config path is unpinned/contradictory.** Spec says
    `~/.serf/providers.toml` & `~/.serf/credentials.toml`, but the hub derives
    credentials as `filepath.Dir(stateDir)/credentials.toml` where `stateDir =
    DefaultStateDir = $XDG_STATE_HOME/serf` (`app_auth.go:57`,
    `storage.go:48-61`) — i.e. `~/.local/state/credentials.toml`, while
    `app_rpc.go:103` uses `hubStateRoot = ~/.serf`. **v6 must verify and pin the
    one canonical path** both hub and standalone read, and reconcile with the OAuth
    root (`$XDG_STATE_HOME/serf`). *(B; needs code re-verification.)*

12. **[SERIOUS] `adapterRecipe` must select quirks/baseURL/label by TYPE, not
    instance name.** `QuirksPreset` switches on literals (`openaicompat:49-77`);
    kimi/glm/openrouter call it with hardcoded presets (`kimi:45`, `glm:45`,
    `openrouter:52`). §4.4 threads only the instance name → a renamed kimi instance
    gets empty quirks (loses temp/top-p locking, finish mapping). Recipe needs
    `type` → {baseURL, quirks, label}. *(B.)*

13. **[SERIOUS/MODERATE] Existing `openai-compatible` adapter unaddressed.** A real
    registered `openai-compatible` adapter exists (`openaicompat:122 Name()`, env
    factory `:90-110`) with launch/env/display wiring, but `SelectProfile` has no
    case for it (already unselectable, `cmdutil.go:72`). Its name collides with the
    new behavior tag `openai-compatible`. v6 must fold `OPENAI_COMPATIBLE_*` env
    into a synthesized openai-type/chat-completions instance. *(A.)*

## E. Minor / refinements

14. **[MINOR] Resume helper returns a value, no error channel.**
    `resumeProviderFromProfileID`/`resumeRequestForConfig` (`app_rpc.go:1717-1735`)
    return a `ResumeRequest`; "errors clearly on vanished instance" (§4.5) needs a
    signature change the spec asserts as done. *(A.)*

15. **[MINOR] `openai_login.go` omitted from the §4.9 OAuth inventory.**
    `resolveOpenAIStateDir` (`:215-221`) writes `auth/openai.json` and needs an
    instance-name parameter (or is superseded by the device-code flow). *(A.)*

16. **[MINOR] §4.2 conflates `model_catalog.go:67` (lookup alias, DROP) with
    `:243 normalizeCatalogProvider` (ingest normalization, KEEP).** The 70 Gemini
    catalog entries are stored under `gemini`→`google` at ingest by `:243`; drop
    it and tag-`google`/`ListModels("google")` lookups miss them. Only `:67` and
    `client.go:236` are the alias to drop. *(Both.)*

## Verified-resolved (no score)

Both reviewers confirmed at the package-graph level: `llm` imports **zero**
`primeradiant.com/serf/*` packages (`go list -deps ./llm`), so the
`internal/providerconfig` leaf imported by both `llm` and `agent`/`cmdutil`
creates **no cycle** (finding A#1's *structural* fix is sound — the selector lives
in `cmdutil`). `BehaviorTag` as a plain string compiles fine. Catalog-by-tag works
for kimi/glm/openrouter/ollama (type==tag). The gemini-id diagnosis is correct.
Finish normalization genuinely needs no change (in-adapter static literals).
