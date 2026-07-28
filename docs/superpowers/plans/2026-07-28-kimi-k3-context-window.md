# Kimi K3 Context Window Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give Kimi K3 its documented 1,048,576-token context window without changing the 256K Kimi models.

**Architecture:** The embedded Serf override catalog remains the single source of model-specific Kimi metadata. The `kimi-anthropic` profile constructor and Web UI live-model enrichment consume that metadata, falling back only when a model or field is absent.

**Tech Stack:** Go, embedded JSON model catalog, deterministic Go tests.

## Global Constraints

- `k3` uses exactly 1,048,576 tokens.
- `k3-256k`, `kimi-for-coding`, and `kimi-for-coding-highspeed` use exactly 262,144 tokens.
- A positive live-provider context window overrides catalog metadata.
- Default tests make no network calls and require no provider credentials.
- Do not refresh the vendored LiteLLM catalog; upstream currently contains no K3 entry and the refresh has unrelated drift.

---

### Task 1: Model-specific Kimi context metadata

**Files:**
- Modify: `agent/provider/resolve_test.go`
- Modify: `llm/model_catalog_test.go`
- Modify: `cmd/serf-hub/app_models_test.go`
- Modify: `llm/data/serf_model_catalog_overrides.json`
- Modify: `agent/provider/profile.go`
- Modify: `cmd/serf-hub/web_spawn.go`

**Interfaces:**
- Consumes: `llm.EmbeddedModelCatalog().GetModelInfo(modelID)`
- Produces: `kimiAnthropicContextWindow(model string) int`
- Produces: live model entries whose missing `context_window` is filled from catalog metadata

- [ ] **Step 1: Write failing profile and catalog tests**

Add table-driven assertions that `ResolveProfileFromConfig` returns
1,048,576 for `kimi-anthropic-api/k3` and 262,144 for each 256K Kimi model.
Add embedded-catalog assertions with the same literal expected values.

- [ ] **Step 2: Write the failing live-model metadata test**

Use a scripted `llm.ProviderAdapter` whose `ListModels` returns `k3` without a
context window. Assert `fetchLiveModels` reports 1,048,576. Add a second entry
with an explicit positive context window and assert the live value survives.

- [ ] **Step 3: Run tests and verify RED**

Run:

```bash
go test ./agent/provider -run 'TestResolveProfileFromConfig_KimiAnthropic' -count=1
go test ./llm -run 'TestEmbeddedCatalog_Kimi' -count=1
go test ./cmd/serf-hub -run 'TestFetchLiveModels_KimiContextWindow' -count=1
```

Expected: behavioral assertion failures showing 262,144 or a missing
`context_window`, not compile errors.

- [ ] **Step 4: Materialize Kimi model metadata**

Add complete Serf-only catalog entries for `k3`, `k3-256k`, and
`kimi-for-coding-highspeed`; keep the existing `kimi-for-coding` entry at
262,144. Use Kimi's documented model IDs and context sizes.

- [ ] **Step 5: Resolve the Kimi Anthropic profile from the catalog**

In `newKimiAnthropicProfile`, trim the model ID, look up its exact embedded
catalog entry, and use its positive context window. Retain 262,144 when no
entry exists.

- [ ] **Step 6: Fill only missing live model context**

In `fetchLiveModels`, when a matching catalog entry exists and the live model
did not provide a positive `context_window`, copy the catalog's positive
context window into the response entry. Do not replace a live value.

- [ ] **Step 7: Run targeted tests and verify GREEN**

Run:

```bash
go test ./agent/provider ./llm ./cmd/serf-hub
```

Expected: all packages pass.

- [ ] **Step 8: Run the repository verification gate**

Run:

```bash
go test ./...
make lint
```

Expected: both commands exit zero.

- [ ] **Step 9: Commit**

Stage only the design, plan, tests, override, profile, and Web UI model files.
Commit with a detailed message describing the missing upstream metadata,
model-specific context resolution, live-value precedence, and verification.
