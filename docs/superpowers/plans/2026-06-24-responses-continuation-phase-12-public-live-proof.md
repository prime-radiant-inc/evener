# Responses Continuation Phase 12 Public Live Proof Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add and, when credentials are available, run the opt-in public OpenAI production-path live proof required before enabling public OpenAI Responses continuation.

**Architecture:** Keep production registry defaults disabled while adding an explicit live test harness that drives the real `agent.Session` path through the real OpenAI Responses adapter with a test-only enabled registry. The harness records transcript/API/raw-body evidence for an anchor request, a `responses_delta` request, a full-history shadow size comparison, and invalid-anchor classification. The enablement commit is separate and is allowed only after a reviewed 12A proof artifact records concrete thresholds.

**Tech Stack:** Go tests, OpenAI provider adapter, `llm.APILogger` raw-body logging, existing session transcript reader, docs proof artifacts.

---

### Task 1: Add Public 12A Live Harness

**Files:**
- Create: `agent/session_openai_continuation_phase12_live_test.go`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md`

- [x] **Step 1: Write skipped-by-default live test**

Add `TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof` in package `agent`.

The test must skip unless:

```go
os.Getenv("SERF_OPENAI_RESPONSES_PHASE12_E2E") == "1"
```

It must also skip when `OPENAI_API_KEY` is empty.

- [x] **Step 2: Add harness setup**

Inside the test:

```go
stateDir := t.TempDir()

client := llm.NewClient()
adapter, err := openai.NewFromEnv(openai.Config{StateHome: stateDir})
client.Register(adapter)

apiLog, err := llm.NewAPILogger(filepath.Join(stateDir, "api.jsonl"))
apiLog.EnableRawLogging(filepath.Join(stateDir, "api-raw.jsonl"))
client.Use(apiLog)
t.Cleanup(func() { _ = apiLog.Close() })
```

Use a model from `SERF_OPENAI_RESPONSES_PHASE12_MODEL`, defaulting to the same public model used by discovery if unset.

The live command must set `SERF_LOG_RAW_HTTP=1` before `go test` starts because `llm.RawBodyEnabled()` is initialized at process startup.

- [x] **Step 3: Drive real Session anchor and delta**

Create a real session with:

```go
SessionConfig{
	StateDir:                    stateDir,
	OpenAIResponsesContinuation: "auto",
	testOnly: testConfig{
		responsesContinuationSupportRegistry: map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{
			llm.ResponsesEndpointFamilyOpenAIPublic: {
				EndpointFamily:        llm.ResponsesEndpointFamilyOpenAIPublic,
				StorageShapeProven:    true,
				ProductionPathProven:  true,
				Enabled:               true,
				MaxAnchorAgeSeconds:   3600,
				StorageShapeProofID:   "phase-0b-public",
				ProductionPathProofID: "phase-12a-public-live-harness",
			},
		},
	},
}
```

Run two `ProcessInput` calls. The first creates an anchor; the second must produce a transcript `api_call` with `HistoryMode=responses_delta`, `PreviousResponseIDHash` set, and `Request.FullHistoryInputTokensEstimate > 0`.

- [x] **Step 4: Record raw request body metrics**

Read `api-raw.jsonl`, find the full-history anchor request and the `responses_delta` request, and assert:

- delta raw request includes `previous_response_id`;
- delta raw request omits the first prompt marker;
- delta raw request is smaller than a full-history shadow body built for the same second prompt.

Log:

```go
t.Logf("phase12_public_live full_history_bytes=%d responses_delta_bytes=%d full_history_shadow_bytes=%d provider_input_tokens=%d full_history_shadow_tokens=%d", ...)
```

- [x] **Step 5: Record invalid-anchor behavior**

Use the same public adapter to send one direct invalid `PreviousResponseID` request. Assert the request fails and `openai.ClassifyResponsesError(err, true)` returns `llm.ResponsesErrorContinuationRejected`.

- [x] **Step 6: Run default skipped test**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof' -count=1 -v
```

Expected: PASS with skip when opt-in env or `OPENAI_API_KEY` is absent.

- [x] **Step 7: Commit**

```bash
git status --short
git add agent/session_openai_continuation_phase12_live_test.go docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md
git commit -m "test(agent): add public responses continuation phase 12 live harness"
```

### Task 2: Run 12A Public Live Proof

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-public.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md`

- [x] **Step 1: Run live proof command**

Run only with explicit credentials:

```bash
set -a
. ../../.env
set +a
SERF_LOG_RAW_HTTP=1 SERF_OPENAI_RESPONSES_PHASE12_E2E=1 GOCACHE=/tmp/serf-gocache go test ./agent -run 'TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof' -count=1 -v
```

- [x] **Step 2: Record proof artifact**

The proof must include:

- endpoint family: `openai_public`;
- model;
- accepted anchor response;
- accepted `responses_delta`;
- invalid-anchor rejection class;
- raw body sizes for delta and full-history shadow;
- observed provider input-token counts for delta and full-history shadow estimate;
- proposed `MaxAnchorAgeSeconds`;
- numeric rollout thresholds for eligible-hit-rate floor, prompt-cache hit-rate floor, storage-quota/error ceiling, provider-token/cost ceiling, and rate-limit ceiling.

- [x] **Step 3: Commit proof**

```bash
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-public.md docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md
git commit -m "docs: record responses continuation phase 12a public proof"
```

### Task 3: Enable Public Registry Entry

**Files:**
- Modify: `llm/responses_continuation.go`
- Modify: `llm/responses_continuation_test.go`
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12b-public.md`

- [x] **Step 1: Verify 12A artifact is complete**

Before code changes, inspect `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12a-public.md` and confirm it includes all numeric thresholds.

- [x] **Step 2: Write failing registry test**

Update `TestDefaultResponsesContinuationSupportRegistryDisabled` or add a new public-specific test asserting public OpenAI is enabled and Codex remains disabled:

```go
public := registry[ResponsesEndpointFamilyOpenAIPublic]
if !public.Enabled || !public.ProductionPathProven || public.MaxAnchorAgeSeconds <= 0 || public.ProductionPathProofID == "" {
	t.Fatalf("public support = %+v, want enabled with proof")
}
codex := registry[ResponsesEndpointFamilyOpenAICodex]
if codex.Enabled {
	t.Fatalf("codex support = %+v, want disabled", codex)
}
```

- [x] **Step 3: Flip only public OpenAI**

Change only the public registry row to:

```go
ResponsesEndpointFamilyOpenAIPublic: {
	EndpointFamily:        ResponsesEndpointFamilyOpenAIPublic,
	StorageShapeProven:    true,
	ProductionPathProven:  true,
	Enabled:               true,
	MaxAnchorAgeSeconds:   <proof value>,
	StorageShapeProofID:   "2026-06-24-responses-continuation-phase-0b",
	ProductionPathProofID: "2026-06-24-responses-continuation-phase-12a-public",
},
```

Do not change the Codex row.

- [x] **Step 4: Run deterministic registry/session tests**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./llm ./agent -run 'TestDefaultResponsesContinuationSupportRegistry|TestDecideResponsesContinuation|TestSession_OpenAIResponsesContinuationPhase9|TestSession_OpenAIResponsesContinuationPhase10' -count=1 -v
```

- [x] **Step 5: Record 12B proof and commit**

Create `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12b-public.md` referencing the exact 12A artifact and registry values, then commit:

```bash
git add llm/responses_continuation.go llm/responses_continuation_test.go docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12b-public.md
git commit -m "feat(llm): enable public responses continuation"
```

### Task 4: Blocked-State Proof If Credentials Are Missing

**Files:**
- Create: `docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12-blocked.md`
- Modify: `docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md`

- [ ] **Step 1: Record missing live credentials**

If `OPENAI_API_KEY` is absent, record that Task 2 and Task 3 are blocked and that production registry defaults remain disabled.

- [ ] **Step 2: Verify deterministic state**

Run:

```bash
GOCACHE=/tmp/serf-gocache go test ./agent ./llm -run 'TestSession_OpenAIResponsesContinuationPhase12PublicLiveProof|TestDefaultResponsesContinuationSupportRegistryDisabled' -count=1 -v
git status --short --branch
```

- [ ] **Step 3: Commit blocked proof**

```bash
git add docs/superpowers/proofs/2026-06-24-responses-continuation-phase-12-blocked.md docs/superpowers/plans/2026-06-24-responses-continuation-phase-12-public-live-proof.md
git commit -m "docs: record responses continuation phase 12 live-proof blocker"
```

---

## Self-Review

- Spec coverage: 12A-public harness, 12A proof artifact requirements, 12B-public guarded enablement, and blocked state are covered.
- Intentional limit: Codex backend remains disabled because Phase 0B observed `Unsupported parameter: previous_response_id`.
- Safety: no production registry entry may be enabled without a completed 12A artifact containing numeric thresholds.
