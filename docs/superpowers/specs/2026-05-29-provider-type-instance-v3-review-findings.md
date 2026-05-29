# v3 Adversarial Review — Consolidated Findings

Date: 2026-05-29
Ticket: PRI-1880
Subject of review: `2026-05-29-provider-type-instance-model-design.md` (v3, commit 71f48d36)

Two independent adversarial reviewers (competition framing). Reviewer A: 11
serious; Reviewer B: 9 serious. Below is the **deduplicated, verified** set. Every
`file:line` was cited by a reviewer who opened the file. **Note:** the reviewers
ran on this branch, which is behind `main` and lacks `docs/llm-providers.md` /
`docs/llm-provider-config-and-launch.md` (merged to main separately) — a branch
artifact, not a spec defect; rebase before implementing.

## I. Coupling sites the §4.2b "complete inventory" missed

1. **[SERIOUS] §4.2a mis-cites the main-request site → would silently disable
   OpenAI 24h prompt caching.** §4.2a says stamp `req.ProviderType` at
   `session.go:1460` + `:2709`. But `1460` is inside `describeImage` (vision
   side-channel, fn header `:1416`). The **main agentic request** is built in
   `processOneInput` at `session.go:2662`; there's a third at `:3873`. The
   prompt-cache gate (`:1382`) runs via `applyModelRequestMetadata(&req)` at
   `:2685` on the **2662** request. Following §4.2a literally leaves
   `ProviderType` empty on the main request → re-keyed cache check never fires.
   *(Both reviewers.)*

2. **[SERIOUS] System-prompt provider coupling entirely missed.**
   `PromptData.Provider = s.profile.ID()` (`session.go:3873`),
   `SectionResolver.provider = s.profile.ID()` (`:3972`); section filenames are
   `fmt.Sprintf("%s.provider-%s", name, r.provider)` (`section_resolver.go:79-84`),
   and a real load-bearing section exists:
   `agent/prompts/sections/tools.provider-openai_append.md`. Rename an
   `openai`-type instance to `work` → lookup `tools.provider-work_append` → not
   found → OpenAI-specific prompt guidance silently dropped. Falsifies Phase 1a's
   "renamed instance behaves identically" claim. *(Reviewer B — unique.)*

3. **[SERIOUS] Resume/fork breaks for custom/renamed instances.**
   `resumeProviderFromProfileID` (`cmd/serf-hub/app_rpc.go:1735-1742`) is a
   hardcoded 10-name allowlist fed the persisted `Meta.ProfileID` (= instance
   name post-redesign); any custom name hits `default → ""`, leaving
   `req.Provider` empty on re-launch (`:1723-1726`). Absent from §4.2b. *(Both.)*

4. **[SERIOUS] `gemini`→`google` alias lives in 3 places, not 1.** §4.4/§8.7 delete
   only `client.go:236`. Also: `model_catalog.go:67` (`if p == "gemini" { p =
   "google" }`) and `model_catalog.go:243-249 normalizeCatalogProvider`. The
   catalog stores models under `Provider: "google"` (type), not instance name, so
   `ListModels` for a renamed instance returns nothing. *(Both.)*

5. **[SERIOUS] §4.2d overstates `RewriteErrorProvider`.** It's called **only** by
   ollama (`ollama/adapter.go:52,58,117`). Every other adapter hardcodes the type
   in errors: `openai/adapter.go:495,1179 NewStreamError("openai", …)`, anthropic/
   google/openaicompat likewise. Renamed-instance error labels still show the
   type; achieving §4.2d's claimed diagnostic means re-keying ~15 literals the
   inventory omits. *(Reviewer A — unique.)*

6. **[SERIOUS] Launch-check + web model picker branch on provider literals not in
   the inventory, and one of them *hides* models (behavior, not display).**
   `launch_check.go:104` (skip `openrouter-anthropic`), `:222` (`openrouter`
   tools-only filter); `web.go:2038,2064` (`web.go:2064` removes non-tool models
   for `openrouter`). All iterate `client.ProviderNames()` (= instance names).
   §4.11 frames picker work as display-only; `web.go:2064` is behavior. *(Both.)*

7. **[SERIOUS] Launch-gating/credential surface is 4 hardcoded type-maps + ~9
   `provider=="openai"` branches, not "one branch."** `credentials/store.go:38
   providerEnvVars`, `:60 providerAuthModes`; `launchconfig/env.go:30
   providerEnvVar`; `app_auth.go:133 credentialAuthModes`; openai guards at
   `app_auth.go:108,151,191,249,292,430,461`, `serf-tui/auth.go:80`,
   `storage.go:142`. §4.7 says "replacing its `provider == "openai"` branch"
   (singular). *(Reviewer A; overlaps B's OAuth finding.)*

8. **[MINOR] `queryModelContextWindow` is a request-time env read, type-keyed.**
   `cmdutil.go:69` → `providerEnvConfig` (`:266-274`) reads `KIMI_API_KEY`/
   `KIMI_BASE_URL` etc. via `os.Getenv` at selection time. Orphaned by "env never
   consulted again" and keyed on type names that won't match custom instances.
   *(Reviewer B.)*

## II. No-back-compat migration design holes

9. **[SERIOUS, PARTIALLY CORRECTED] Credential storage is fragmented across hub
   vs standalone serf** — but the reviewers' "OAuth is per-workspace" sub-claim is
   **wrong** (verified post-review).
   - **Confirmed real:** standalone `serf` builds its client only from env
     (`serve.go:39`/`run.go:127` → `llm.NewFromEnv`; **no** `credentials` import
     under `cmd/serf/`). The hub reads `~/.serf/credentials.toml` (`main.go`,
     `HubStateRoot = ~/.serf`). So API keys have two unrelated sources.
   - **Corrected:** OAuth `openai.json` is **machine-global**, not per-workspace.
     `serf openai login` writes `resolveOpenAIStateDir` which **ignores workDir**
     and returns `DefaultStateDir()` = `$XDG_STATE_HOME/serf` (`openai_login.go:
     216-220`); the adapter reads it via `DefaultStateDirWithStateHome(cfg.
     StateHome)` where `cfg.StateHome ← env.StateHome ← XDG_STATE_HOME`
     (`openai/adapter.go:80`, `env_registry.go:52`). The per-project
     `RuntimeDir(origin,wd)` is the **transcript** `StateDir` (`WithStateDir` sets
     `cfg.StateDir`, `env_registry.go:16-18`), which the OAuth path does **not**
     use. So there is **one** global `~/.local/state/serf/auth/openai.json`, read
     by both standalone and hub-spawned daemons.
   - **Net for v4:** the migration reads three *global* sources (env +
     `~/.serf/credentials.toml` + one global `openai.json`); the real fix is to
     have **standalone serf also read the shared provider store**, not the
     per-workspace OAuth scare. *(Reviewers flagged the fragmentation; the
     per-workspace specifics were wrong.)*

10. **[SERIOUS] §4.9 "`NewFromEnv` is no longer a runtime path" undercounts live
    consumers.** Callers: `cmd/serf/run.go:127` (the **standalone CLI**),
    `cmd/serf-hub/web.go:2024` (live picker `fetchLiveModels`),
    `launch_check.go:94,159`, `serve.go:36`, `cmd/llmcall/main.go:220`,
    `cmd/serfeval/main.go:196`, plus `llm/generate.go:158 DefaultClient()`. Also:
    `validateSerfLaunchContract` runs `launch-check --model` (no `--models`) on
    **every spawn** (`spawn.go:558-564`, called `:141`) → `launch_check.go:159
    NewFromEnv`; §4.3 only re-plumbs the `--models` path. If `NewFromEnv` stays
    there, `providers.toml`-only instances aren't registered →
    `providerConfigured` false (`launch_check.go:163-171`) → validation silently
    skipped. *(Both.)*

11. **[SERIOUS] No cross-process lock for first-run migration.** The only lock is
    the hub singleton flock (`cmd/serf-hub/flock.go`, `~/.serf/hub.lock`); it
    doesn't cover `providers.toml`. Hub + standalone serf (or two serf
    invocations) can each see absence and migrate+write concurrently → clobber or
    torn read. §6 only addresses corrupt/absent, not concurrent. *(Reviewer B.)*

12. **[SERIOUS] Migration can emit a duplicate instance name the loader rejects.**
    §4.8 migrates `OPENAI_API_KEY` → an `openai` instance **and** the
    `OPENAI_COMPATIBLE_*` slot → "an `openai` instance with
    `apiStyle=chat-completions`." The compat env factory fires whenever
    `OPENAI_COMPATIBLE_BASE_URL` is set (`openaicompat/adapter.go:88-99`). Two
    instances both named `openai` violate §6's uniqueness rule → invalid
    `providers.toml`. *(Reviewer B.)*

13. **[MINOR] `OPENAI_COMPATIBLE_PROVIDER_QUIRKS` dropped by migration.**
    `openaicompat/adapter.go:110-111` applies `QuirksPreset`. §4.8 migrates base
    URL/key/apiStyle but not quirks (§2 defers per-instance quirks) → migrated
    instance not behavior-equivalent, contradicting §5 acceptance. *(Reviewer B.)*

14. **[MINOR] `ToEnv` still injects env into the spawned daemon.**
    `launchconfig/env.go:80-95` still writes the credential env var + passes
    parent env. Post-migration the daemon reads only `providers.toml`, so env/
    per-launch key rotation silently has no effect — unflagged behavior change.
    *(Reviewer A.)*

## III. Internal contradictions / unbuildable-as-written

15. **[SERIOUS] `WithModel`'s cross-provider "switch" path is incoherent.**
    The switch calls `NewOpenAIProfile`/`NewAnthropicProfile`/`NewGeminiProfile`/
    `NewMiniMaxProfile`, which **hardcode** `id: "openai"/"anthropic"/"gemini"/
    "minimax"` (`profile.go:579,678,699,732,893`), so it can't "preserve both
    id=instance name and type" (§4.2b). The disambiguation switches on the
    **parsed prefix** (`decidePrefixAction` inner switch, `profile.go:405-408`),
    not `p.id` — so §4.2b's "change `p.id` → `p.providerType`" does nothing for it.
    §4.4 says the prefix resolves "only as a configured instance name" while the
    constructors switch on type. §4.2b calls this a mechanical swap; §9 admits
    it's "the single hardest sub-problem." Genuine contradiction. *(Both.)*

16. **[SERIOUS] `resp.Provider` fix (§4.2c) only covers streaming.** The one-line
    fix at `session.go:3511` is in the streaming arm. The **non-streaming**
    `s.client.Complete` path (`session.go:3353-3358`) and the vision side-channel
    (`:1492`) return the adapter's `Response` verbatim, with `Provider` hardcoded
    to the type (`openai/adapter.go:1049,1934`, etc.). §7's test ("instance `work`
    reports `resp.Provider == "work"`") can't pass for those paths. *(Reviewer A.)*

17. **[SERIOUS] Per-instance OAuth is unbuildable from `NewFromProviders(config)`
    as specified.** (a) The adapter resolves OAuth **at construction** by state
    dir (`openai/adapter.go:79-110` → `AuthFilePath(stateDir)`); the §4.1 recipe
    `adapterRecipe(instanceName, cfg)` carries neither state dir; §4.10's "the
    session reads it via its state dir" is wrong about *where* OAuth resolves.
    (b) `AuthRecord.Validate()` hardcodes `r.Provider != "openai"` rejection
    (`storage.go:142-143`), called by `LoadAuth` (`:77`) — an `auth/<name>.json`
    for any other instance fails to load. §4.7 re-keys the path but not the record
    or validator. *(Both — complementary.)*

18. **[MINOR] `normalizeProviderName` has 4 call sites doing trim/lower.**
    `client.go:91,122,207,224`. "Removed" (§4.2b) should be "replaced with
    trim/lower," or case handling regresses at all four lookups. *(Reviewer B.)*

## What v3 got right (verified by both, not findings)

Routing keys on `profile.ID()` = `req.Provider` (`session.go:2662`,
`client.go:90`). Finish-reason normalization **is** already type-correct (in-adapter
static literals: `google:514`, `openaicompat:359`, `anthropic:642`) — §8.3 holds.
`ProviderOptions` keys are a type-level recipe/adapter contract (`profile.go:590`
vs `openai/adapter.go:296`; google reads both `"google"`/`"gemini"`) — §8.4 holds.
The `agent/profile.go` id-branch citations (344, 396, 498, 519/533/562, 649, 930,
1007) and the hardcoded-`resp.Provider` adapter citations are accurate.

## Competition result

Reviewer A: 11 serious (all verified real, none padded; uniques: #16 non-streaming
`resp.Provider`, #10 full `NewFromEnv` consumer list, #5 `RewriteErrorProvider`,
#7 the 4-map credential surface). Reviewer B: 9 serious (uniques: #2 system-prompt
coupling, #12 duplicate-name migration, #17b `AuthRecord.Validate`, #10 the
"every-spawn" sharpening). **A wins on count (11 vs 9) and on unique serious finds
(4 vs 2)** — but B's #2 (system-prompt section coupling) and #12 (duplicate-name
migration) are among the highest-value findings overall. 5 points to A.
