# LLM Provider Architecture

How serf turns a `provider/model` string into an API call, and the two
identities that hold the system together. This documents the **current**
implementation (as of 2026-05, after the PRI-1880 *Phase 1a* behavior-tag
separation). Companion doc:
[`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md)
(credentials, OAuth, and how the hub spawns sessions).

## The mental model to keep

A provider has **two distinct identities**, and the whole design turns on not
confusing them:

```
instance NAME  =  profile.ID()  =  req.Provider  =  adapter registration name   → ROUTING + IDENTITY
behavior TAG   =  profile.BehaviorTag()  =  providerconfig.BehaviorTag(type,style) → ALL provider-conditional BEHAVIOR
```

- The **name** routes and identifies: a request carries `req.Provider`, the
  client looks up `client.providers[name]` to pick the adapter, and responses /
  error labels report the name (which instance answered).
- The **tag** drives behavior: every place that used to branch on the provider
  string (`== "openai"`, `switch provider`) now keys on `BehaviorTag()`. The tag
  is derived from a provider *type* + an `apiStyle` by one pure function,
  `providerconfig.BehaviorTag` (`internal/providerconfig/providerconfig.go:34`).

**In Phase 1a these still coincide at runtime** — every default instance is named
after its type, so `name == type == tag` and nothing changed observably. But the
*code* now keys behavior on the tag, not the name, so a renamed or custom
instance (where `name != type`, coming in Phase 1b) will behave like its
underlying provider while routing/identifying under its own name. **If you change
how providers are identified, keep this split: route on the name, branch on the
tag.**

> Pre-1a this was a single string equal "in four places at once." Phase 1a split
> *behavior* (the tag) from *name* (routing/identity) and moved provider switching
> out of the profile and up to the session (below). The old audits and the v1–v6
> spec revisions under `docs/superpowers/specs/` describe the journey.

## Request lifecycle

```
model string "openai/gpt-5"
  │  cmdutil.ParseModelRef          cmdutil/cmdutil.go   → {Provider:"openai", Model:"gpt-5"}
  ▼
cmdutil.SelectProfile(provider, model)            (wraps agent profile construction)
  │  builds a ProviderProfile; rejects unknown provider names
  ▼
ProviderProfile  (agent/profile.go)   profile.ID()=="openai"  profile.BehaviorTag()=="openai"
  │  carries: instance name (id), behavior tag, context window, tool surface,
  │           quirks, ProviderOptions key, CheapModel
  ▼
agent.Session   sets req.Provider = s.profile.ID()   (the NAME)
  │  provider-conditional behavior branches on s.profile.BehaviorTag()  (the TAG)
  ▼
llm.Client.Complete / Stream            llm/client.go
  │  prov = normalizeProviderName(req.Provider)   (now just lower/trim)
  │  adapter = c.providers[prov]                  (else "unknown provider")
  │  resp.Provider + error labels stamped back to req.Provider (the NAME), centrally
  ▼
ProviderAdapter.Complete/Stream         llm/providers/<x>/adapter.go
  ▼
vendor HTTP API
```

Models are addressed `provider/model`. `ParseModelRef` splits on the **first**
`/`, so the model half may contain slashes (`openrouter/anthropic/claude-…`,
`ollama/llama3:8b`) — see meta-providers below.

## The behavior tag (`internal/providerconfig`)

`internal/providerconfig` is a **leaf package** (imports nothing from `llm`/
`agent`/`cmdutil`, so all of them can import it without a cycle). It owns the
shared vocabulary:

- `BehaviorTag(typ, style string) string` (`:34`) — the one definition of "what
  behavior does this provider have." It returns the type for every type **except
  `openai`**, which splits by `apiStyle`: `openai`+`chat-completions` →
  `openai-compatible`; everything else (incl. `openai`+`responses`) → the type.
- `NameToTag(cfg) map[string]string` (`:42`) — instance name → tag, for the
  config-driven case (Phase 1b).
- `Config`/`InstanceConfig` types and `DefaultStateRoot()` (`:52`, the `~/.serf`
  resolver) — the scaffolding Phase 1b's `providers.toml` loader will populate.

The set of behavior tags is exactly the old set of distinct provider behaviors:
`openai`, `openai-compatible`, `anthropic`, `google`, `openrouter`,
`openrouter-anthropic`, `kimi`, `glm`, `minimax`, `ollama`.

## Provider profiles (`agent/profile.go`)

A **profile** is the provider-shaped half of a session. It now carries **both**
an `id` (the instance name) and a `behaviorTag`. Constructors stamp the tag via
`providerconfig.BehaviorTag`:

| Constructor | id (Phase 1a) | behaviorTag | `profile.go` |
|---|---|---|---|
| `NewOpenAIProfile` | `openai` | `openai` | tag at :596 |
| `NewAnthropicProfile` | `anthropic` | `anthropic` | :686 |
| `NewGeminiProfile` | **`google`** | `google` | id+tag at :707-708 |
| `NewMiniMaxProfile` | `minimax` | `minimax` | :742 |
| `NewOpenRouterAnthropicProfile` | `openrouter-anthropic` | `openrouter-anthropic` | :904 |
| `NewOpenAICompatProfile(id, …)` | caller-supplied (`kimi`/`glm`/`openrouter`/`ollama`) | = id | :1027 |

`BehaviorTag()` is on the `ProviderProfile` interface (`:302`).
`WithProviderID(profile, name)` (in `profile_overrides.go`) renames an instance —
it overrides the `id` while **preserving the tag** (this is how a `name != type`
instance is constructed; used in tests today, by the Phase-1b config path later).

**Gemini is now canonical `google`.** `NewGeminiProfile` sets `id == "google"`
(not `"gemini"`); the `gemini`→`google` rewrites in routing and catalog lookup
were removed. `cmdutil.SelectProfile` still accepts `gemini` *and* `google` as
input aliases (so `--model gemini/…` works), both yielding an id-`google`
profile; `gemini` survives only as an input alias and as the `ProviderOptions`
key the Google adapter reads (it reads both `"google"` and `"gemini"`), and as
the catalog **ingest** normalization (`model_catalog.go normalizeCatalogProvider`).

Profile methods that branch on provider behavior now switch on **`p.behaviorTag`**
(not `p.id`):
- `CheapModel()` (`:355`).
- `decidePrefixAction(behaviorTag, instanceName, prefix)` (`:411`) — the
  **meta-provider** logic: a self-prefix (`prefix == instanceName`) is stripped;
  meta-provider upstream namespaces (openrouter/openrouter-anthropic/minimax, by
  tag) are kept; a cross-provider prefix yields `prefixActionSwitch`, which the
  *session* (not the profile) acts on (below).
- `rebuildOnSameProviderChange(behaviorTag)` (`:529`), the catalog lookups
  (`resolveOpenAICompatCatalogModel`/`suppressBareCatalogLookup`, keyed on tag),
  and the `behaviorTag == "openrouter"` MiniMax-reasoning gate.
- `WithModel(model)` (`:506`, anthropic variant) re-targets the model **within
  the current instance only** — it strips/keeps prefixes and rebuilds for the
  catalog, but **no longer switches providers** (the switch arm was removed).

`ProviderOptions` is a `map[string]any` keyed by the behavior tag's contract
string; each profile writes its options under that key and the matching adapter
reads the same key. This is a type-level contract, unaffected by the instance
name.

## Switching providers happens at the session, not the profile

Pre-1a, `WithModel` rebuilt a fresh profile via a hardcoded vendor switch. Now
the **session** owns cross-provider/instance switching, via a resolver injected
at construction (cycle-free: `cmd/serf` builds the closure; `agent` just holds
the field):

- `SessionConfig.ResolveProfile func(ref) (ProviderProfile, error)`
  (`session.go:201`), wired in `cmd/serf/serve.go` + `run.go` to
  `cmdutil.SelectProfile`.
- `Session.resolveProfileForRef(ref)` (`session.go:1283`): if `decidePrefixAction`
  classifies the ref as a cross-provider switch **and** a resolver is set → call
  the resolver and swap `s.profile` (preserving communicate/allowed-decisions
  overrides via `preserveBaseOverrides`, and re-running provider-conditional tool
  registration via `reapplyProviderSpecificTools`, `:1307`); otherwise →
  `profile.WithModel(ref)` (within-instance).
- `SetModel`, subagent model overrides, and `model_fallbacks` all route through
  this. The cross-provider-fallback guard compares `BehaviorTag()` (a fallback to
  a *different* tag errors; same-tag is allowed).

## Adapters and the registry (`llm/`)

The `ProviderAdapter` interface is small (`llm/client.go`): `Name()`,
`Complete`, `Stream`. Adapters self-register from env — each package's `init()`
calls `RegisterEnvAdapterFactory` (`llm/env_registry.go`); `llm.NewFromEnv` runs
every factory and registers the ones whose env vars are present. `Client.Register`
keys the adapter by `adapter.Name()`; the **default provider** is the first
registered adapter that is not `NonDefaultEligible` (ollama opts out). Registration
order is package **import order**, so the env-driven default is effectively
alphabetical (with both `ANTHROPIC_API_KEY` and `OPENAI_API_KEY` set, the default
is *anthropic*).

**Identity is stamped centrally** (Phase 1a). `llm.Client.Complete`/`Stream` set
`resp.Provider = req.Provider` and rewrite errors with the instance name +
behavior tag — `RewriteErrorProvider(err, prov)` + `StampErrorBehaviorTag(err,
tag)` (`client.go:116-117`), and a `providerStampStream` wrapper does the same for
streamed `StreamEventError`/`StreamEventFinish` events (covering both the session
consumer and `llm.StreamGenerate`). The empty-provider no-op is preserved
(`context.Canceled` stays unlabeled). So adapters' hardcoded `resp.Provider`/error
literals no longer leak — the response and errors report **the instance name**.
`Client.nameToTag` (`:20`, settable via `SetNameToTag`) maps instance name → tag
for llm-layer logic; it is nil in Phase 1a (env path), so `behaviorTagFor`
(`:260`) falls back to the provider name (identity), which is correct while
name==tag.

`normalizeProviderName` is now just lowercase + trim (the `gemini`→`google`
rewrite is gone — the Gemini profile's id is `google`).

### Wire protocols (they differ — this matters)

| Tag | Adapter pkg | Endpoint | Notes |
|---|---|---|---|
| `openai` | `openai` | `/v1/responses`, or `/backend-api/codex/responses` at `chatgpt.com` for OAuth | OAuth → Codex backend; API key → standard API |
| `openai-compatible` | `openaicompat` | `/chat/completions` | vLLM/LiteLLM/Ollama-style; **not** the Responses API |
| `anthropic` | `anthropic` | `/v1/messages` | base URL overridable |
| `google` | `google` | Gemini API | its own protocol |
| `kimi`, `glm`, `openrouter` | thin wrappers → `openaicompat` | `/chat/completions` | own base URL + `QuirksPreset(...)` (by type) |
| `minimax`, `openrouter-anthropic` | thin wrappers → `anthropic` | `/v1/messages` | own base URL |
| `ollama` | `ollama` → `openaicompat` | `/v1/chat/completions` | NonDefaultEligible |

Because `openai` (Responses) and `openai-compatible` (Chat Completions) are
**different protocols**, you can't reach a vLLM box by pointing the `openai`
adapter at it; that's what the openaicompat adapter is for. (Finish-reason
normalization lives *inside* each adapter, called with the adapter's own static
literal — it keys on the wire protocol, not on `req.Provider`, so it needed no
change.)

## What keys on what (the map to consult before touching identity)

**Routing + identity (the NAME — `req.Provider`/`profile.ID()`):**
- `req.Provider = s.profile.ID()` at every LLM call site (main loop, vision,
  fallbacks, and the side-channel calls in `session_namer.go`,
  `fork_summarize.go`, `context_manager.go`, `tool_web_*`, `strategy_*`,
  `eval_probes.go`, `ListModels`), all resolving through `client.providers[...]`.
- `cmdutil.SelectProfile` — rejects unknown names; the hub's launch gate runs it
  in a subprocess (companion doc).
- `resp.Provider` + error labels — stamped centrally to the instance name.
- Resume: session meta persists `ProfileID`; the hub now **passes it through**
  (`resumeRequestForConfig`, `cmd/serf-hub/app_rpc.go`) and errors on empty —
  the old hardcoded allowlist was removed (downstream `SelectProfile` validates).

**Behavior (the TAG — `profile.BehaviorTag()`):**
- 24h prompt cache: `session.go:1450` `BehaviorTag() == "openai"`.
- Gemini native web_search registration: `session.go:4881` `BehaviorTag() ==
  "google"` (and re-applied on switch by `reapplyProviderSpecificTools`).
- Cross-provider fallback guard: compares `BehaviorTag()`.
- Profile model-handling: `CheapModel`, `decidePrefixAction`,
  `rebuildOnSameProviderChange`, catalog lookups, the openrouter gate — all on
  the tag.
- Prompt sections: `SectionResolver.provider = s.profile.BehaviorTag()`
  (`session.go`) → filenames like `tools.provider-openai_append.md`.
- OpenAI Responses→chat endpoint-fallback signal: `classify.go:118` on the
  error's `BehaviorTag()` (falls back to `Provider()` when the tag is empty).
- Provider-failure diagnostics classify on the structured `llm.Error` (provider +
  tag), not a hardcoded name list (`internal/diagnostic`, `diagnostics.js`).

## Phase 1a done / Phase 1b next

**Phase 1a (done, behavior-preserving):** the name/tag split above; switching
moved to the session; central identity stamping; Gemini canonicalized to
`google`; `internal/providerconfig` leaf. Verified by a renamed-instance
integration backstop (`agent/provider_instance_integration_test.go`) + a
full-tree sweep.

**Deferred to Phase 1b** (need real custom instances + a config to matter and be
testable): the `providers.toml` loader + `NewFromProviders`; per-instance OAuth;
the `openai` `apiStyle` recipe + custom `anthropic` base-URL instances; wiring
`SetNameToTag` from the config; and the **picker/launch behavior filters** that
still key on the literal provider name —
`cmd/serf/launch_check.go` (openrouter / openrouter-anthropic), `cmd/serf-hub/
web.go`, `app_rpc.go launchProviderAllowsUnreportedModels`, and the env-keyed
`cmdutil.queryModelContextWindow`. These are no-ops while name==type and are
re-keyed in 1b. The design lives in
[`docs/superpowers/specs/2026-05-29-provider-type-instance-model-design.md`](superpowers/specs/2026-05-29-provider-type-instance-model-design.md).

## Adding or changing a provider

- **A new vendor with an existing wire protocol:** a thin wrapper package (see
  `kimi`/`minimax`) — `Name()`, base URL, `QuirksPreset` (by type),
  `RegisterEnvAdapterFactory`, a `SelectProfile` case + a profile constructor that
  stamps the right `behaviorTag`, plus the credential maps (companion doc).
- **A new wire protocol:** a full adapter package + a profile constructor.
- **Touching identity/routing:** keep the split — route on the name, branch on
  the tag; add new behavior switches on `BehaviorTag()`, never on `profile.ID()`.

## See also

- [`llm-provider-config-and-launch.md`](llm-provider-config-and-launch.md) —
  credentials store, provider env-var reference, OpenAI OAuth, and the hub →
  `launch-check` → `serf serve` process model.
- [`ollama.md`](ollama.md) — running serf against a local Ollama server.
- [`superpowers/specs/2026-05-29-provider-type-instance-model-design.md`](superpowers/specs/2026-05-29-provider-type-instance-model-design.md)
  — the type/instance design (Phase 1a implemented; 1b/2 pending).
- Historical point-in-time audits live under `docs/audit-logs/` (Feb 2026; paths
  there predate the `internal/llm` → `llm/` move).
