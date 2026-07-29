# Provider-Local Plugin Agent Model Fallback Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make plugin-agent model declarations advisory to the active provider, falling back to the explicit delegate model and then the parent model without ever sending an unavailable host-runtime alias such as `sonnet` to Kimi.

**Architecture:** Add curated aliases to the embedded model catalog, then introduce one side-effect-free subagent model selector that resolves plugin metadata against the current provider's live model list. Direct spawns and durable delegates consume the selector's frozen profile and provenance; plugin preferences never invoke the cross-provider resolver, while an explicit delegate fallback retains the existing resolver semantics.

**Tech Stack:** Go, `llm.ModelCatalog`, `llm.Client.ListModels`, `agent/provider.Profile`, plugin agents, delegate jobstore restore descriptors, scripted provider and worktree test seams.

## Global Constraints

- The approved contract is `docs/superpowers/specs/2026-07-29-plugin-agent-model-fallback-design.md`.
- The selection order is exactly: available plugin model, explicit delegate model, parent model.
- Plugin metadata never switches providers. Explicit delegate model refs retain the existing within-provider and cross-provider resolver behavior.
- The current provider instance's live model list is authoritative for plugin-model availability. Enumeration failure or unsupported enumeration makes the plugin preference unavailable.
- Known aliases resolve through the embedded catalog. Unknown exact model IDs remain eligible when the active provider advertises them.
- Availability matching is exact after the existing trim and case normalization. Do not use dated-family fallback for live membership.
- Run plugin membership selection once before delegate IDs, worktrees, job records, child sessions, transcripts, or watches.
- Preserve the existing best-effort child-session live metadata refresh; “one selection” does not mean only one `ListModels` call over the whole child-construction lifecycle.
- Persist the winning request source in `RequestedModel` and the frozen concrete profile in `ResolvedProfileID` and `ResolvedModel`. Restore must not re-read plugin metadata.
- Emit a non-fatal `EventWarning` through `emitDiagnosticWarning`; do not run the `Notification` hook.
- Default tests stay deterministic and offline. Use scripted adapters at the provider boundary and scripted git for control-flow assertions.
- Make the smallest coherent change. Do not import the strict explicit-model validation, capability policy, or provider-inventory design from the canceled `docs/superpowers/plans/2026-07-15-agent-model-selection-correctness.md`.
- Before changing tests during execution, re-read `docs/testing.md`.

---

## File Structure

| File | Responsibility |
|---|---|
| `llm/data/serf_model_catalog_overrides.json` | Curated `sonnet`, `opus`, and `haiku` alias targets. |
| `llm/model_catalog.go` | Detect a unique alias target without silently accepting ambiguous aliases. |
| `llm/model_catalog_embedded.go` | Apply aliases only to exact/materialized catalog entries, not dated-family descendants. |
| `llm/model_catalog_test.go` | Catalog alias, ambiguity, production mapping, and non-inheritance regressions. |
| `agent/subagent_model_selection.go` | Provider-local plugin resolution, fallback precedence, provenance, and warning construction. |
| `agent/subagent_model_selection_test.go` | Deterministic selector contract against enumerable and unenumerable fake providers. |
| `agent/subagents.go` | Prepare children from a frozen model selection and wire the direct-spawn path. |
| `agent/plugin_agents_integration_test.go` | Direct-spawn request-model and warning behavior. |
| `agent/subagents_fuzz_test.go` | Keep tagged preparation fuzzing deterministic with an enumerable scripted adapter. |
| `agent/job_delegate.go` | Run selection before durable side effects and attach the frozen result. |
| `agent/job_delegate_model_selection_test.go` | Durable descriptor, result echo, warning, and pre-side-effect failure behavior. |
| `agent/job_delegate_isolation_test.go` | Preserve rollback coverage after unknown agent types move into preflight. |
| `docs/subagent-runtime-contracts.md` | Document plugin-agent model precedence and diagnostics. |
| `docs/llm-providers.md` | Distinguish provider-local plugin metadata from explicit delegate model routing. |

## Selection Interfaces

Tasks 2 through 4 use these exact package-private types and methods:

```go
type subagentModelSelection struct {
	agent          *plugin.Agent
	profile        *provider.Profile
	requestedModel string
	warning        *events.WarningData
}

type pluginAgentModelResolution struct {
	profile *provider.Profile
	reason  string
}

func (s *Session) selectSubagentModel(
	ctx context.Context,
	explicitModel string,
	agentType string,
) (subagentModelSelection, error)

func (s *Session) resolvePluginAgentModel(
	ctx context.Context,
	base *provider.Profile,
	requested string,
) pluginAgentModelResolution

func resolvePluginAgentCatalogRef(
	base *provider.Profile,
	catalog *llm.ModelCatalog,
	requested string,
) (candidate string, reason string)

func (s *Session) prepareSubagentRunWithModelSelection(
	ctx context.Context,
	task string,
	workingDir string,
	maxTurns int,
	agentType string,
	reasoningEffort string,
	parentTasks []taskpkg.TaskTemplate,
	grantTools []string,
	selection subagentModelSelection,
) (*preparedSubagentRun, error)
```

`pluginAgentModelResolution.reason` is empty on success and otherwise one of
`cross-provider`, `unavailable`, `unverified`, or `ambiguous`. These values are
diagnostic classifications, not public error codes.

### Task 1: Add Curated Catalog Aliases Without Family Leakage

**Files:**
- Modify: `llm/data/serf_model_catalog_overrides.json`
- Modify: `llm/model_catalog.go:61-72`
- Modify: `llm/model_catalog_embedded.go:61-199`
- Test: `llm/model_catalog_test.go:158-205`

**Interfaces:**
- Consumes: `ModelCatalog.GetModelInfo(string) *ModelInfo` and the existing exact-ID-first index.
- Produces:

```go
// ResolveAlias returns the unique catalog model declaring alias. Exact model
// IDs are not aliases. If multiple model IDs declare alias, it returns nil,
// true so callers fail closed instead of depending on catalog order.
func (c *ModelCatalog) ResolveAlias(alias string) (target *ModelInfo, ambiguous bool)
```

- [ ] **Step 1: Write RED tests for unique and ambiguous alias resolution**

Extend `TestModelCatalog_AliasLookup` with an alias shadowed by a real ID, then
add this focused test:

```go
func TestModelCatalog_ResolveAliasRejectsAmbiguity(t *testing.T) {
	cat := &ModelCatalog{Models: []ModelInfo{
		{ID: "model-a", Provider: "openai", Aliases: []string{"fast"}},
		{ID: "model-b", Provider: "openai", Aliases: []string{"fast"}},
	}}

	target, ambiguous := cat.ResolveAlias("fast")
	if target != nil || !ambiguous {
		t.Fatalf("ResolveAlias(fast) = (%v, %t), want (nil, true)", target, ambiguous)
	}
}
```

Also assert a unique alias returns its concrete model and that a literal model
ID returns `(nil, false)`.

- [ ] **Step 2: Write RED tests for production aliases and exact-only overlay behavior**

Add:

```go
func TestEmbeddedModelCatalog_ClaudeAliases(t *testing.T) {
	cat := EmbeddedModelCatalog()
	for alias, want := range map[string]string{
		"sonnet": "claude-sonnet-4-6",
		"opus":   "claude-opus-4-7",
		"haiku":  "claude-haiku-4-5",
	} {
		got, ambiguous := cat.ResolveAlias(alias)
		if ambiguous || got == nil || got.ID != want {
			t.Errorf("ResolveAlias(%q) = (%v, %t), want %q", alias, got, ambiguous, want)
		}
	}
}
```

Add `TestApplyOverrides_AliasesDoNotInheritToDatedSnapshots` with a synthetic
catalog containing `claude-sonnet-4-6` and
`claude-sonnet-4-6-20260205`. Apply:

```json
{"claude-sonnet-4-6":{"aliases":["sonnet"]}}
```

Require the bare entry to contain `sonnet`, the dated entry to have no aliases,
and `ResolveAlias("sonnet")` to return the bare entry.

- [ ] **Step 3: Run the focused tests and verify the real RED**

Run:

```bash
go test ./llm -run 'Test(ModelCatalog_ResolveAliasRejectsAmbiguity|EmbeddedModelCatalog_ClaudeAliases|ApplyOverrides_AliasesDoNotInheritToDatedSnapshots)$'
```

Expected: compilation fails because `ResolveAlias` is undefined. After adding
`ResolveAlias` in Step 4, rerun this command and require the production-alias
and exact-only-overlay assertions to remain RED until Steps 5 and 6.

- [ ] **Step 4: Implement unique alias resolution**

Implement `ResolveAlias` by trimming the input, returning `(nil, false)` when it
equals any real model ID, then scanning `ModelInfo.Aliases`. Return a copy of the
only matching `ModelInfo`; return `(nil, true)` when two distinct model IDs
declare the same alias. Match the existing catalog's case-sensitive alias
semantics.

- [ ] **Step 5: Prevent family overlays from copying aliases**

Change:

```go
func applyOverlayFields(m *ModelInfo, ov map[string]any, includeAliases bool)
```

In `applyOverrides`, pass `includeAliases=true` for an exact override match and
for a materialized entry. Pass `false` when a dated model inherited the bare
family override. Keep all non-alias overlay fields inheritable. Guard the
existing alias assignment with `if includeAliases`.

- [ ] **Step 6: Add the three curated production aliases**

In `serf_model_catalog_overrides.json`:

- add `"aliases": ["sonnet"]` to `claude-sonnet-4-6`;
- add an overlay-only entry for `claude-opus-4-7` with
  `"aliases": ["opus"]`;
- add an overlay-only entry for `claude-haiku-4-5` with
  `"aliases": ["haiku"]`.

Do not add a Codex alias and do not change model capability metadata.

- [ ] **Step 7: Run the catalog tests**

Run:

```bash
go test ./llm -run 'Test(ModelCatalog|EmbeddedModelCatalog|ApplyOverrides)'
```

Expected: PASS.

- [ ] **Step 8: Commit the catalog unit**

```bash
git status --short
git add llm/data/serf_model_catalog_overrides.json llm/model_catalog.go llm/model_catalog_embedded.go llm/model_catalog_test.go
git commit -m "feat: add provider model aliases

Add curated Claude host aliases to the embedded model catalog, expose
ambiguity-aware alias resolution, and keep aliases off dated family entries so
selection remains deterministic."
```

### Task 2: Implement the Side-Effect-Free Selection Contract

**Files:**
- Create: `agent/subagent_model_selection.go`
- Create: `agent/subagent_model_selection_test.go`
- Reuse: `agent/live_model_metadata.go:10-48`
- Reuse: `agent/session.go:711-726`
- Reuse: `agent/session_set_model_test.go:165-189`
- Reuse: `agent/profile_testhelpers_test.go:53-55`

**Interfaces:**
- Consumes: Task 1's `(*ModelCatalog).ResolveAlias`, `liveModelInfoFor`,
  `liveModelMetadataTimeout`, `(*provider.Profile).CrossProviderRef`,
  `WithModel`, `WithLiveModelInfo`, and `resolveProfileForRef`.
- Produces: the exact types and methods in **Selection Interfaces** above.

- [ ] **Step 1: Write RED tests for precedence and plugin availability**

Create table-driven selector tests covering these concrete contracts:

```go
{
	name:              "available exact plugin model wins",
	parentModel:       "gpt-5.2",
	pluginModel:       "gpt-5.3",
	explicitModel:     "anthropic/boom",
	liveModels:        []llm.ModelInfo{{ID: "gpt-5.2"}, {ID: "gpt-5.3"}},
	wantRequestedModel:"gpt-5.3",
	wantProfileID:     "openai",
	wantModel:         "gpt-5.3",
	wantWarning:       false,
	wantResolverCalls: 0,
},
{
	name:              "unavailable plugin model uses explicit fallback",
	parentModel:       "gpt-5.2",
	pluginModel:       "gpt-4.1-nano",
	explicitModel:     "gpt-5.3",
	liveModels:        []llm.ModelInfo{{ID: "gpt-5.2"}},
	wantRequestedModel:"gpt-5.3",
	wantProfileID:     "openai",
	wantModel:         "gpt-5.3",
	wantWarning:       true,
	wantReason:        "unavailable",
},
{
	name:              "unenumerable plugin model inherits parent",
	parentModel:       "gpt-5.2",
	pluginModel:       "gpt-5.3",
	listErr:           errors.New("models endpoint disabled"),
	wantRequestedModel:"",
	wantProfileID:     "openai",
	wantModel:         "gpt-5.2",
	wantWarning:       true,
	wantReason:        "unverified",
},
```

The first case deliberately makes the explicit fallback invalid. It proves the
fallback is not evaluated when the plugin preference wins.

- [ ] **Step 2: Write RED tests for aliases, custom IDs, and provider locality**

Add separate tests because their profile shapes matter:

- A renamed native Anthropic instance with ID `work`, behavior tag `anthropic`,
  plugin model `sonnet`, and live model `claude-sonnet-4-6` resolves to
  `work/claude-sonnet-4-6`, preserving the current instance.
- A Kimi-Anthropic profile on model `k3` with plugin model `sonnet` classifies it
  as `cross-provider`, does not call `ListModels`, and uses explicit fallback
  `k3`.
- A provider-qualified plugin ref such as `anthropic/claude-sonnet-4-6` on Kimi
  falls through as `cross-provider` even when `ResolveProfile` could resolve an
  Anthropic instance; assert the resolver is not called for the plugin ref.
- An OpenRouter-Anthropic profile with plugin model `sonnet` keeps the
  established `anthropic/claude-sonnet-4-6` namespace and succeeds only when
  that exact wire ID is advertised.
- An unknown exact custom ID succeeds when that exact ID is advertised.
- A dated exact ID does not succeed merely because its undated family is
  advertised.
- A synthetic ambiguous alias is classified `ambiguous`. Exercise the catalog
  translation helper `resolvePluginAgentCatalogRef` with a synthetic
  `ModelCatalog` rather than mutating the embedded singleton.
- A plugin model identical to the parent's concrete model succeeds without a
  membership enumeration.
- Plugin model `inherit` plus explicit model `gpt-5.3` selects `gpt-5.3`
  without a warning.
- Plugin model `inherit` plus no explicit model inherits the parent without a
  warning.

For every successful live match, assert the selected profile carries the live
model's advertised ID and metadata via `WithLiveModelInfo`.

- [ ] **Step 3: Run the selector tests and verify RED**

Run:

```bash
go test ./agent -run 'Test(SelectSubagentModel|ResolvePluginAgentModel)'
```

Expected: compilation fails because the selector types and methods do not
exist.

- [ ] **Step 4: Implement plugin-agent lookup and fallback selection**

Implement `selectSubagentModel` in this order:

1. retain the existing delegation-allowance guard so a forbidden direct spawn
   fails before model enumeration;
2. trim and resolve `agentType`; unknown non-empty types return the existing
   `unknown plugin agent type` error;
3. snapshot `base := s.currentProfile()`;
4. if the agent model is non-empty and not `inherit`, call
   `resolvePluginAgentModel`;
5. on plugin success, return immediately with the original trimmed plugin ref
   in `requestedModel` and do not inspect the explicit model;
6. otherwise resolve the trimmed explicit model with `resolveProfileForRef`;
7. preserve communicate overrides when that explicit ref switches providers;
8. otherwise inherit `base` with an empty `requestedModel`.

When a non-inherit plugin preference falls through, return:

```go
selected := subagentModelSelection{
	agent:          agent,
	profile:        fallbackProfile,
	requestedModel: explicitModel,
}
warning := events.WarningData{
	Source:     "plugin",
	Title:      "plugin agent model unavailable",
	PluginName: agent.PluginName,
	Message: fmt.Sprintf(
		"plugin %q agent %q requested model %q, but it is %s on active provider %q; using %q",
		agent.PluginName,
		agentType,
		pluginModel,
		resolution.reason,
		base.ID(),
		fallbackProfile.ID()+"/"+fallbackProfile.Model(),
	),
}
selected.warning = &warning
return selected, nil
```

Return the warning data; do not emit it from the selector.

- [ ] **Step 5: Implement provider-local alias translation and membership**

In `resolvePluginAgentModel`:

1. call `resolvePluginAgentCatalogRef` to perform Steps 2 through 6 below;
2. reject `base.CrossProviderRef(requested)` as `cross-provider`;
3. check exact catalog identity before treating the ref as an alias;
4. resolve a unique alias through Task 1's method and reject ambiguity;
5. when the alias target provider equals `base.BehaviorTag()` using
   `strings.EqualFold`, use the bare
   canonical ID so renamed native instances remain local;
6. otherwise qualify the canonical ID as `provider/model`, rejecting it when
   `base.CrossProviderRef` says that would switch; this preserves meta-provider
   upstream namespaces;
7. build the candidate with `base.WithModel`;
8. if the candidate profile already has the base provider ID and concrete
   model, return `base` without enumeration;
9. call `s.client.ListModels` once with
   `context.WithTimeout(ctx, liveModelMetadataTimeout)`;
10. classify list errors as `unverified`, an absent exact match as
   `unavailable`, and a match as success;
11. return `candidate.WithLiveModelInfo(advertisedInfo)`.

Use `liveModelInfoFor(models, candidate.Model())`; do not use
`LookupModelInfo`, `familyModelID`, or a provider completion.

`resolvePluginAgentCatalogRef` returns an unknown non-alias ref unchanged so
live enumeration can accept custom provider model IDs. It must not call the
session resolver or inspect configured providers.

- [ ] **Step 6: Run selector and race tests**

Run:

```bash
go test ./agent -run 'Test(SelectSubagentModel|ResolvePluginAgentModel)'
go test -race ./agent -run 'TestSelectSubagentModel'
```

Expected: PASS.

- [ ] **Step 7: Commit the selector unit**

```bash
git status --short
git add agent/subagent_model_selection.go agent/subagent_model_selection_test.go
git commit -m "feat: select provider-local plugin agent models

Resolve plugin model preferences against the current provider's advertised
models, keep plugin metadata from switching providers, and freeze the actual
fallback source and diagnostic without mutating session state."
```

### Task 3: Wire Direct Spawns to the Frozen Selection

**Files:**
- Modify: `agent/subagents.go:360-423,774-799`
- Modify: `agent/plugin_agents_integration_test.go:170-280`
- Modify: `agent/subagents_fuzz_test.go:20-145,333-430`

**Interfaces:**
- Consumes: Task 2's `subagentModelSelection` and `selectSubagentModel`.
- Produces: `prepareSubagentRunWithModelSelection` with the exact signature in
  **Selection Interfaces**. The existing `prepareSubagentRun` signature remains
  as a thin internal/test wrapper.

- [ ] **Step 1: Write the direct-spawn RED regressions**

Update `TestSpawnAgent_PluginAgentType_Model` so the override case registers a
`fakeEnumerableAdapter` advertising the parent and plugin models. Keep the
inherit case unchanged.

Add:

```go
func TestSpawnAgent_UnavailablePluginModelUsesExplicitFallback(t *testing.T)
```

Construct a Kimi-Anthropic parent on `k3`, register a
`fakeEnumerableAdapter{name: "kimi-anthropic-api"}` advertising only `k3`, and
register a plugin agent declaring `sonnet`. Call `spawnAgent` with explicit
model `k3`, wait for completion, and require:

- the child request provider is `kimi-anthropic-api`;
- the child request model is `k3`;
- no request uses `sonnet`;
- exactly one buffered warning contains the plugin name, agent type, `sonnet`,
  `cross-provider`, and `kimi-anthropic-api`.

Build the renamed Kimi profile with
`WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api")` so the
test covers the exact provider identity from the incident.

Add a second integration test with an Anthropic parent advertising
`claude-sonnet-4-6`; require `sonnet` to win over an explicit fallback and the
wire request to use `claude-sonnet-4-6`.

- [ ] **Step 2: Run the direct-spawn tests and verify behavioral RED**

Run:

```bash
go test ./agent -run 'TestSpawnAgent_(PluginAgentType_Model|UnavailablePluginModelUsesExplicitFallback|AvailablePluginAliasWins)'
```

Expected: the Kimi case sends `sonnet` or fails at the provider boundary, and
the alias case does not canonicalize.

- [ ] **Step 3: Split preparation from selection**

Move plugin lookup and all model-resolution code out of the body that creates
the child. `prepareSubagentRunWithModelSelection` must use only:

```go
agent := selection.agent
subProfile := selection.profile
```

Set:

```go
requestedModel: selection.requestedModel,
```

Keep `prepareSubagentRun` as:

```go
selection, err := s.selectSubagentModel(ctx, model, agentType)
if err != nil {
	return nil, err
}
return s.prepareSubagentRunWithModelSelection(
	ctx, task, workingDir, maxTurns, agentType, reasoningEffort,
	parentTasks, grantTools, selection,
)
```

This wrapper does not emit the warning because preparation-only tests do not
launch a delegation.

- [ ] **Step 4: Make `spawnAgent` select and warn before child construction**

In `spawnAgent`, call `selectSubagentModel`, then:

```go
if selection.warning != nil {
	s.emitDiagnosticWarning(*selection.warning)
}
```

Only then call `prepareSubagentRunWithModelSelection`. Retain the existing tree
slot, launch, cleanup, and response behavior unchanged.

- [ ] **Step 5: Keep the tagged preparation fuzzer deterministic**

In `subagents_fuzz_test.go`, add:

```go
type safzEnumerableAdapter struct {
	*agenttest.ScriptedAdapter
	models []llm.ModelInfo
}

func (a *safzEnumerableAdapter) ListModels(context.Context) ([]llm.ModelInfo, error) {
	return append([]llm.ModelInfo(nil), a.models...), nil
}
```

Register this wrapper for both the parent and child clients with advertised
models `gpt-5`, `gpt-5.2`, and `gpt-5.3`. Update the file header: preparation
may perform one scripted model-list read for plugin membership but still makes
no completion and launches no goroutine. Keep the existing determinism digest;
its `requestedModel` field now checks the actual winning source.

- [ ] **Step 6: Run direct-spawn, preparation, fuzz-compile, and race tests**

Run:

```bash
go test ./agent -run 'Test(SpawnAgent|PrepareSubagentRun)'
go test -tags=serffuzz ./agent -run '^$'
go test -race ./agent -run 'TestSpawnAgent_(UnavailablePluginModelUsesExplicitFallback|AvailablePluginAliasWins)'
```

Expected: PASS.

- [ ] **Step 7: Commit the direct-spawn unit**

```bash
git status --short
git add agent/subagents.go agent/plugin_agents_integration_test.go agent/subagents_fuzz_test.go
git commit -m "fix: apply plugin model fallback to direct spawns

Prepare subagents from one frozen model selection, emit provider-local fallback
diagnostics on real launches, and preserve deterministic fuzz coverage of the
winning request source."
```

### Task 4: Preflight Durable Delegates, Persist the Winner, and Document the Contract

**Files:**
- Modify: `agent/job_delegate.go:277-370,2135-2168`
- Create: `agent/job_delegate_model_selection_test.go`
- Modify: `agent/job_delegate_isolation_test.go:295-340`
- Modify: `docs/subagent-runtime-contracts.md:77-104`
- Modify: `docs/llm-providers.md:574-594`

**Interfaces:**
- Consumes: Task 2's selector and Task 3's
  `prepareSubagentRunWithModelSelection`.
- Produces: durable descriptors whose `RequestedModel` reflects the winning
  source and whose resolved fields freeze the concrete child profile.

- [ ] **Step 1: Write RED durable fallback and provenance tests**

Create `TestCreateDelegate_UnavailablePluginModelPersistsExplicitFallback`:

- parent profile: Kimi-Anthropic instance `kimi-anthropic-api`, model `k3`;
- live models: only `k3`;
- plugin agent model: `sonnet`;
- explicit delegate model: `k3`;
- child response: `communicateWithDefaultOutput("done")`.

Build the profile with
`WithProviderID(newKimiAnthropicProfile("k3"), "kimi-anthropic-api")`.

Require:

```go
desc.RequestedModel == "k3"
desc.ResolvedProfileID == "kimi-anthropic-api"
desc.ResolvedModel == "k3"
result.Model == "kimi-anthropic-api/k3"
```

Also require the child request to use `k3`, and require one diagnostic warning
for the rejected plugin model.

Create `TestCreateDelegate_UnavailablePluginModelPersistsParentFallback` with no
explicit model. Require empty `RequestedModel` and the parent's concrete
provider/model in both descriptor and result.

Create `TestCreateDelegate_AvailablePluginAliasPersistsAliasAndConcreteModel`.
On an Anthropic parent advertising `claude-sonnet-4-6`, require:

```go
desc.RequestedModel == "sonnet"
desc.ResolvedModel == "claude-sonnet-4-6"
result.Model == "anthropic/claude-sonnet-4-6"
```

- [ ] **Step 2: Write the RED pre-side-effect failure test**

Using `newScriptedWtDlgRepo`, register a plugin agent declaring `sonnet` on its
default OpenAI parent and set `ResolveProfile` to return an error for explicit ref
`anthropic/boom`. Call `createDelegate` with:

```go
delegateArgs{
	Task:      "must fail before durable state",
	AgentType: "test-plugin:reviewer",
	Model:     "anthropic/boom",
	Isolation: "worktree",
	WatchParent: true,
}
```

Capture `len(r.git.calls)` before the call. Require:

- `res.Err` wraps or contains the explicit resolver error;
- `res.DelegateID`, `res.StartedJobID`, and `res.TranscriptRef` are empty;
- the scripted git call count is unchanged;
- `LoadDelegates()` and the delegate job list remain empty;
- `s.subagents` contains no child and `s.jobManager.watches` remains empty;
- no child model request was made.

This proves the explicit delegate fallback is validated only after the plugin
preference falls through, and before IDs or isolation work.

Name this test
`TestCreateDelegate_PluginFallbackFailureHasNoSideEffects`.

- [ ] **Step 3: Run the durable tests and verify behavioral RED**

Run:

```bash
go test ./agent -run 'TestCreateDelegate_(UnavailablePluginModel|AvailablePluginAlias|PluginFallbackFailure)'
```

Expected: descriptors record the rejected plugin or the wrong concrete model,
or the failure returns minted IDs and invokes scripted git.

- [ ] **Step 4: Move durable selection ahead of IDs and worktrees**

In `createDelegate`, after argument, sandbox, job-manager, allowance, and
context validation but before `NewDelegateID`, call:

```go
selection, err := s.selectSubagentModel(ctx, args.Model, args.AgentType)
if err != nil {
	return delegateStartFailed(err)
}
if selection.warning != nil {
	s.emitDiagnosticWarning(*selection.warning)
}
```

After setting ID-derived context and any worktree/sandbox context, call
`prepareSubagentRunWithModelSelection` with the frozen `selection`. Do not call
the selector again.

The child `NewSession` path may still perform its existing best-effort live
metadata refresh. That refresh enriches the already selected concrete profile;
it is not a second membership decision.

- [ ] **Step 5: Preserve rollback coverage after agent lookup moves earlier**

The existing
`TestDelegateIsolation_SpawnFailureAfterWorktreeCreateRollsBackLane` uses an
unknown agent type to force failure after worktree creation. That error now
correctly occurs before IDs and git.

Replace its failure trigger by saturating `r.s.treeCounter` with
`reserve(slotKindJob)` before `createDelegate`, and release those reservations
in test cleanup. This lets argument validation and model preflight succeed,
creates the worktree, then makes child preparation return
`errTreeAtCapacity`. Rename the test to
`TestDelegateIsolation_TreeAtCapacityAfterWorktreeCreateRollsBackLane`.
Keep its lane and sidecar rollback assertions. This continues to cover the
post-worktree `prepareSubagentRunWithModelSelection` rollback branch without
reintroducing late plugin lookup.

- [ ] **Step 6: Pin restore to the frozen descriptor**

Add
`TestSendDelegateMessage_RestoreIgnoresChangedPluginModel` beside the durable
provenance tests. Use `newDelegateRestorePreflightSession`,
`seedStoppedDelegateRestoreRecord`, `markStoredDelegateResumable`,
`loadShellRecord`, and `replaceStoredDelegateRecord`, following
`TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile`.

Set the descriptor's `AgentType` to `test-plugin:reviewer`,
`RequestedModel` to `sonnet`, and frozen resolved fields to
`work/descriptor-model`. Register that agent type in `s.pluginAgents` with a
different unavailable model before calling `sendDelegateMessage`. Configure
`s.resolveProfile` to accept only `work/descriptor-model`.

Require the restored child profile and first restored request to use
`work/descriptor-model`, and require `warningMessages(s)` after the send to
contain no plugin-model fallback warning.

No production restore code change is expected. If this test fails, repair the
restore path to consume only the frozen descriptor; do not rerun
`selectSubagentModel`.

- [ ] **Step 7: Prove direct and durable paths select the same profile**

Add `TestPluginAgentModelSelection_DirectAndDurableParity`. On one enumerable
Kimi parent with plugin model `sonnet` and explicit fallback `k3`, launch once
through `spawnAgent` and once through `createDelegate`. Record both child
requests and require the same provider `kimi-anthropic-api` and model `k3`.
Require the durable descriptor's concrete profile to match those requests.

- [ ] **Step 8: Update runtime and provider documentation**

In `docs/subagent-runtime-contracts.md`, document:

- plugin `model` is provider-local advisory metadata;
- precedence is available plugin model, explicit delegate model, parent model;
- `sonnet`, `opus`, and `haiku` are catalog aliases;
- exact custom IDs work only when advertised by the current provider;
- unadvertised or unverifiable plugin models fall through with a diagnostic.

In `docs/llm-providers.md`, replace the statement that all subagent model
overrides route through cross-provider resolution. State that explicit delegate
model arguments still use `resolveProfileForRef`, while plugin-agent model
metadata is checked only against the current provider instance and never
switches providers.

- [ ] **Step 9: Run durable, restore, parity, and isolation tests**

Run:

```bash
go test ./agent -run 'Test(CreateDelegate_.*Plugin|PluginAgentModelSelection_DirectAndDurableParity|DelegateIsolation_TreeAtCapacityAfterWorktreeCreateRollsBackLane|.*Restore.*Plugin)'
go test -race ./agent -run 'TestCreateDelegate_(UnavailablePluginModelPersistsExplicitFallback|AvailablePluginAliasPersistsAliasAndConcreteModel)'
```

Expected: PASS.

- [ ] **Step 10: Run the complete verification**

Run:

```bash
gofmt -w agent/subagent_model_selection.go agent/subagent_model_selection_test.go agent/subagents.go agent/plugin_agents_integration_test.go agent/subagents_fuzz_test.go agent/job_delegate.go agent/job_delegate_model_selection_test.go agent/job_delegate_isolation_test.go llm/model_catalog.go llm/model_catalog_embedded.go llm/model_catalog_test.go
go test ./llm ./agent
go test -tags=serffuzz ./agent -run '^$'
go test ./...
git diff --check
git status --short
```

Expected: all tests pass, fuzz-tagged tests compile, the diff has no whitespace
errors, and status contains only the intended Task 4 files before commit.

- [ ] **Step 11: Commit the durable integration and documentation**

```bash
git status --short
git add agent/job_delegate.go agent/job_delegate_model_selection_test.go agent/job_delegate_isolation_test.go docs/subagent-runtime-contracts.md docs/llm-providers.md
git commit -m "fix: preflight durable plugin model fallback

Select plugin-agent models before delegate IDs and worktrees, persist the actual
winning request and concrete profile, keep restore frozen, and document the
provider-local fallback contract."
```

- [ ] **Step 12: Review the final change range**

Run:

```bash
git log --oneline --decorate -8
git diff --stat df374e48d..HEAD
git diff --check df374e48d..HEAD
git status --short
```

Review the complete diff against
`docs/superpowers/specs/2026-07-29-plugin-agent-model-fallback-design.md`.
Confirm there is no tool-choice change, provider-family routing table, live
completion probe, restore-time plugin reevaluation, Codex alias, or change to
normal `Session.SetModel` behavior.
