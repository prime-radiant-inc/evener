# Agent Model Selection Correctness Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make every explicit delegate or plugin-agent model selection resolve, validate, persist, run, and restore as the selected provider/model, while rejecting unverifiable or incapable selections before any child, job, worktree, transcript, metadata, or watch state is created.

**Architecture:** Add an agent-only model-selection preflight beside the existing mid-session switching helper: it resolves the configured instance, performs one bounded provider enumeration, canonicalizes the selected model, checks catalog visibility, tool-calling capability, and reasoning effort, and returns typed errors without changing session state. Delegate preparation accepts the frozen preflight result so it cannot resolve a second time after durable creation begins; restore validates the exact persisted profile without choosing a replacement. The prerequisite transcript/API-separation project owns attempt capture and grouping; this project consumes its canonical API-attempt lifecycle to verify that the existing configured fallback loop is the only fallback source and records requested/actual provenance.

**Tech Stack:** Go, `llm.Client`/`llm.ModelLister`, embedded `llm.ModelCatalog`, `agent/provider.Profile`, delegate jobstore JSONL, canonical private `llm/apilog` JSONL records, real-git worktree tests, scripted provider adapters.

## Global Constraints

This implementation does not:

- add a model-family preference or automatic escalation policy;
- modify Superpowers prompts, skills, or plans;
- probe availability with paid completions;
- silently fail open when explicit availability cannot be checked;
- add compatibility behavior for invalid historical model references;
- redesign the Web/TUI model picker or the existing switching protocol.

- Treat every requirement outside `docs/superpowers/specs/2026-07-15-agent-model-selection-correctness-design.md` as a defect. Stop and ask Jesse instead of expanding the implementation scope.

Additional repository constraint: before changing any test, re-read `docs/testing.md`. All default tests in this plan stay below the scripted provider/transport boundary and must remain independent of credentials, network access, quota, current model behavior, and ambient developer-machine state.

---

## Contract Precedence

The approved `docs/superpowers/specs/2026-07-15-agent-model-selection-correctness-design.md` intentionally differs from the older draft `docs/superpowers/specs/2026-07-12-model-switching-design.md` on enumeration failure: explicit agent selections fail with `model_availability_unverified`; the existing user-facing `Session.SetModel` path remains fail-open as specified by the older draft. Keep the policies separate. Do not change `thread/model/set`, `Session.SetModel`, `resolveModelSwitchTarget`, AppWire, Web, or TUI behavior in this project.

## Prerequisite

This is the fourth project in the approved execution order. All three earlier projects must be landed before starting:

| Order | Prerequisite plan | Existing contract this project preserves |
|---|---|---|
| 1 | `docs/superpowers/plans/2026-07-15-delegate-budget-truthfulness.md` | Typed lifetime/tool-round exhaustion, durable non-success `exhausted` jobs, partial evidence, and exact delegate resumability/result metadata. |
| 2 | `docs/superpowers/plans/2026-07-15-transcript-api-log-separation.md` | Semantic-only transcript v2, immediate durable canonical `api_attempt` records, separate append-only group settlement, attempt-group joins, and explicit API-log access. |
| 3 | `docs/superpowers/plans/2026-07-15-job-supervision-surface-cleanup.md` | Compact `job_status`, bounded `read_transcript(job:...)`, renamed transcript-read grant callbacks, ordered terminal/output/notification durability, and removal of model-facing `job_read_output`. |

Project 2 deletes `transcript.APICall`, `appendModelAPICallFunc`, `llm.AdapterAttemptRecord`, and the mixed transcript/API path; it establishes `apilog.APILogRecord`, `apilog.APIAttemptRecord`, `apilog.APIAttemptGroupSettlement`, `apilog.DecodeRecord`, and the parent `llm` attempt-group/sink/logger lifecycle used here. Do not recreate, wrap, or redesign any prerequisite infrastructure. In overlapping `subagents.go`, `job_delegate.go`, session/job result paths, and API lifecycle tests, preserve the landed budget, exhaustion, resumability, transcript-read grant, notification-ordering, and immediate-attempt-plus-settlement contracts. If any prerequisite contract or canonical interface is absent, stop and complete the earlier project rather than adding a temporary compatibility path.

## File Structure

| File | Responsibility |
|---|---|
| `agent/model_selection.go` | New typed error and side-effect-free validation for explicit/frozen agent model profiles. |
| `agent/model_selection_test.go` | Deterministic unit contract for resolution, canonicalization, availability, tools, effort clamp/validation, and safe alternatives. |
| `agent/session_config.go` | Carry the authoritative nonserialized configured-instance inventory and add the package-internal model-enumeration test seam. |
| `agent/session_init.go` | Reapply the runtime-only configured-instance inventory during restore. |
| `agent/testkit_test.go` | Give unrelated package-internal session tests a deterministic current-profile model listing; correctness tests override the seam. |
| `agent/subagents.go` | Select the effective delegate/plugin-agent ref once and prepare a child from that frozen selection. |
| `agent/job_delegate.go` | Run model preflight before IDs/worktrees and validate the exact frozen profile during restore. |
| `agent/job_delegate_model_selection_test.go` | Behavioral spawn tests, including no durable residue and cross-family request/tool isolation. |
| `agent/job_delegate_create_test.go` | Pin effective raw request plus canonical resolved profile in the existing durable descriptor test. |
| `agent/job_delegate_send_test.go` | Pin exact-profile restore and unavailable-profile refusal. |
| `agent/job_delegate_model_echo_test.go` | Keep explicit-selection echo coverage using an enumerable fake. |
| `agent/plugin_agents_integration_test.go` | Keep plugin-agent pinned model coverage using an enumerable fake and verify its configured ref is authoritative. |
| `agent/session_fallback_provenance_test.go` | Consume the prerequisite's canonical immediate attempt and group-settlement records to prove configured-only fallback order, requested/actual provider/model, grouping, and terminal-error preservation. |
| `agent/subagents_fuzz_test.go` | Keep the tagged subagent program deterministic by installing the same fake enumeration boundary. |
| `agent/fuzz_jdr_restore_lifecycle_test.go` | Keep the tagged delegate create/restore harness deterministic under strict explicit selection. |
| `cmdutil/load_client.go` | Derive sorted configured provider-instance names from `providercfg.Config`, without consulting the client adapter registry. |
| `cmdutil/load_client_test.go` | Pin trimming, deduplication, sorting, and config-only provenance of the instance-name helper. |
| `cmd/serf/run.go` | Populate the runtime inventory for fresh and restored one-shot sessions. |
| `cmd/serf/run_coverage_fuzz_test.go` | Capture fresh/restore configs and prove the provider inventory is forwarded. |
| `cmd/serf/serve.go` | Populate the runtime inventory for fresh and restored serve sessions. |
| `cmd/serf/serve_residual_fuzz_test.go` | Capture fresh/restore serve configs and prove the provider inventory is forwarded. |
| `tools/tool-fluency/cmd/serf-fluency/main.go` | Populate the inventory for the production-like live tool-fluency session constructor. |
| `tools/tool-fluency/cmd/serf-fluency/coverage_program_fuzz_test.go` | Capture the tool-fluency session config and prove config-derived instances are forwarded. |

### Task 1: Add the Agent-Only Typed Model Preflight

**Files:**
- Create: `agent/model_selection.go`
- Create: `agent/model_selection_test.go`
- Modify: `agent/session_config.go:206-225`
- Modify: `agent/session_init.go:308-365`
- Modify: `agent/testkit_test.go:62-103`
- Modify: `cmdutil/load_client.go`
- Modify: `cmdutil/load_client_test.go`
- Modify: `cmd/serf/run.go`
- Modify: `cmd/serf/run_coverage_fuzz_test.go`
- Modify: `cmd/serf/serve.go`
- Modify: `cmd/serf/serve_residual_fuzz_test.go`
- Modify: `tools/tool-fluency/cmd/serf-fluency/main.go`
- Modify: `tools/tool-fluency/cmd/serf-fluency/coverage_program_fuzz_test.go`

**Interfaces:**
- Consumes: `(*Session).resolveProfileForRef(base *provider.Profile, ref string) (*provider.Profile, bool, error)`, `(*llm.Client).ListModels(context.Context, string) ([]llm.ModelInfo, error)`, `llm.EmbeddedModelCatalog() *llm.ModelCatalog`, `llm.NormalizeReasoningEffort(string) string`, and `llm.ClampReasoningEffort(string, []string) string`.
- Produces:

```go
type ModelSelectionErrorCode string

const (
	ModelSelectionUnknownProvider          ModelSelectionErrorCode = "unknown_provider_instance"
	ModelSelectionUnknownModel             ModelSelectionErrorCode = "unknown_or_unavailable_model"
	ModelSelectionAvailabilityUnverified   ModelSelectionErrorCode = "model_availability_unverified"
	ModelSelectionMissingCapability        ModelSelectionErrorCode = "missing_required_capability"
	ModelSelectionInvalidReasoningEffort    ModelSelectionErrorCode = "invalid_reasoning_effort"
	ModelSelectionRestoredModelUnavailable ModelSelectionErrorCode = "restored_model_unavailable"
)

type ModelSelectionError struct {
	Code         ModelSelectionErrorCode
	Ref          string
	Provider     string
	Model        string
	Alternatives []string
	Cause        error
}

func (e *ModelSelectionError) Error() string
func (e *ModelSelectionError) Unwrap() error
func (s *Session) newModelSelectionError(code ModelSelectionErrorCode, ref string, profile *provider.Profile, alternatives []string, cause error) *ModelSelectionError
func (s *Session) configuredProviderInstances(base *provider.Profile) []string

type resolvedAgentModel struct {
	RequestedRef string
	Profile      *provider.Profile
}

func (s *Session) resolveExplicitAgentModel(ctx context.Context, base *provider.Profile, ref, reasoningEffort string) (resolvedAgentModel, error)
func (s *Session) validateFrozenAgentModel(ctx context.Context, profile *provider.Profile, reasoningEffort string) (*provider.Profile, error)
func (s *Session) validateEnumeratedAgentModel(ctx context.Context, requestedRef string, profile *provider.Profile, reasoningEffort string, allowAliases bool) (*provider.Profile, error)
```

- Authoritative runtime-only instance inventory and its config-derived constructor:

```go
// SessionConfig; never persisted in ConfigSnapshot or meta.json.
ConfiguredProviderInstances []string `json:"-"`

// RestoreSessionConfig; explicitly reapplied after configFromSnapshot.
ConfiguredProviderInstances []string

// cmdutil
func ConfiguredProviderInstanceNames(cfg providercfg.Config) []string
```

`ConfiguredProviderInstanceNames` returns sorted, deduplicated, trimmed non-empty `providercfg.Config.Instances[i].Name` values. `configuredProviderInstances(base)` returns only that inventory plus `base.ID()`, sorted and deduplicated. It must never call `s.client.ProviderNames()` or infer configured instances from registered adapters, fallbacks, environment variables, or a failed resolver.

- Test-only enumeration seam, never serialized and never set by production:

```go
// testConfig
listAgentModels func(context.Context, *provider.Profile) ([]llm.ModelInfo, error)
```

- `resolveExplicitAgentModel` is side-effect free. It returns the raw trimmed request separately from the canonical resolved profile. `validateFrozenAgentModel` requires an exact provider/model match and must not canonicalize to a replacement.

- Add this test helper in `agent/model_selection_test.go`; it installs the existing test resolver and counts every fake enumeration:

```go
func newModelSelectionSession(t *testing.T, models []llm.ModelInfo, listErr error) (*Session, *int) {
	t.Helper()
	s := newSession(t)
	s.resolveProfile = testResolver
	calls := 0
	s.cfg.testOnly.listAgentModels = func(_ context.Context, _ *provider.Profile) ([]llm.ModelInfo, error) {
		calls++
		return append([]llm.ModelInfo(nil), models...), listErr
	}
	return s, &calls
}
```

Production wiring uses the same config-derived value at every constructor:

```go
configuredInstances := cmdutil.ConfiguredProviderInstanceNames(provCfg)

// cmd/serf run and serve, fresh SessionConfig
ConfiguredProviderInstances: append([]string(nil), configuredInstances...),

// cmd/serf run and serve, RestoreSessionConfig
ConfiguredProviderInstances: append([]string(nil), configuredInstances...),

// tools/tool-fluency live sessCfg
ConfiguredProviderInstances: append([]string(nil), configuredInstances...),
```

In `RestoreSessionFromMetaWithConfig`, copy `restoreCfg.ConfiguredProviderInstances` into `cfg.ConfiguredProviderInstances` after `configFromSnapshot`; do not add it to `schema.ConfigSnapshot`. In `prepareSubagentRunWithModelSelection`, immediately after `subCfg := s.cfg`, clone the slice with `append([]string(nil), s.cfg.ConfiguredProviderInstances...)` so child mutation cannot alias parent runtime configuration.

- [ ] **Step 1: Write the failing configured-instance and typed-resolution tests**

First add `TestConfiguredProviderInstanceNames_UsesOnlyProviderConfig` in `cmdutil/load_client_test.go`: use configured instances `work`, `google`, duplicate `work`, and a blank name; require exactly `[]string{"google", "work"}`. This helper receives no `*llm.Client`, making adapter-registry leakage structurally impossible.

Extend `TestResolveExplicitAgentModel_UnknownInstanceListsConfiguredInstances` to set:

```go
s.cfg.ConfiguredProviderInstances = []string{"work", "google", "work", " "}
s.client.Register(&fakeAdapter{name: "client-only"})
```

Require alternatives `[]string{"google", "openai", "work"}`: `openai` is the current base, while the registered-but-unconfigured `client-only` adapter must be absent. Add `TestConfiguredProviderInstances_ChildAndRestoreClone`: mutate a freshly prepared child's inventory and prove the parent's slice is unchanged, then restore a top-level session from meta and prove `RestoreSessionConfig.ConfiguredProviderInstances` survives `configFromSnapshot` without appearing in `Session.Meta().Config`. Task 3 extends this contract through runtime-lost delegate reconstruction and a grandchild; the top-level restore check alone is not sufficient.

Add `TestRunPassesConfiguredProviderInstancesToFreshAndRestore` in `cmd/serf/run_coverage_fuzz_test.go` and `TestRunServePassesConfiguredProviderInstancesToFreshAndRestore` in `cmd/serf/serve_residual_fuzz_test.go`; use existing dependency injection to capture both `SessionConfig` and `RestoreSessionConfig` from a fake two-instance `providercfg.Config`, and require the same sorted two names on fresh and resume paths. Add `TestRunLiveProbePassesConfiguredProviderInstances` in `tools/tool-fluency/cmd/serf-fluency/coverage_program_fuzz_test.go`; capture `runnerNewSession` during a live probe and require the fake provider config's names. These tests check production construction, not UI or protocol output.

Add table-driven tests that assert `errors.As(err, &selectionErr)` and the exact `Code`, not just message fragments:

```go
func TestResolveExplicitAgentModel_TypedValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ref        string
		effort     string
		models     []llm.ModelInfo
		listErr    error
		wantCode   ModelSelectionErrorCode
		wantModel  string
	}{
		{name: "enumeration failure", ref: "anthropic/claude-opus-4-6", listErr: errors.New("catalog offline"), wantCode: ModelSelectionAvailabilityUnverified},
		{name: "unknown model", ref: "anthropic/not-real", models: []llm.ModelInfo{{ID: "claude-opus-4-6", SupportsTools: true}}, wantCode: ModelSelectionUnknownModel},
		{name: "no tool calling", ref: "anthropic/claude-text-only", models: []llm.ModelInfo{{ID: "claude-text-only"}}, wantCode: ModelSelectionMissingCapability},
		{name: "unknown effort", ref: "anthropic/claude-opus-4-6", effort: "turbo", models: []llm.ModelInfo{{ID: "claude-opus-4-6", SupportsTools: true, SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "high"}}}, wantCode: ModelSelectionInvalidReasoningEffort},
		{name: "effort on non-reasoning model", ref: "anthropic/claude-basic", effort: "high", models: []llm.ModelInfo{{ID: "claude-basic", SupportsTools: true}}, wantCode: ModelSelectionInvalidReasoningEffort},
		{name: "canonical alias", ref: "anthropic/claude-latest", models: []llm.ModelInfo{{ID: "claude-opus-4-6", Aliases: []string{"claude-latest"}, SupportsTools: true, SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "high"}}}, wantModel: "claude-opus-4-6"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, calls := newModelSelectionSession(t, tc.models, tc.listErr)
			got, err := s.resolveExplicitAgentModel(context.Background(), s.currentProfile(), tc.ref, tc.effort)
			if *calls != 1 {
				t.Fatalf("enumerations = %d, want 1", *calls)
			}
			if tc.wantCode != "" {
				var selectionErr *ModelSelectionError
				if !errors.As(err, &selectionErr) || selectionErr.Code != tc.wantCode {
					t.Fatalf("error = %v, want ModelSelectionError code %q", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveExplicitAgentModel: %v", err)
			}
			if got.RequestedRef != tc.ref || got.Profile.Model() != tc.wantModel {
				t.Fatalf("selection = raw %q resolved %s/%s", got.RequestedRef, got.Profile.ID(), got.Profile.Model())
			}
		})
	}
}
```

Add these dedicated tests for instance resolution, clamp behavior, frozen identity, and safe alternatives:

```go
func TestResolveExplicitAgentModel_UnknownInstanceListsConfiguredInstances(t *testing.T) {
	s, calls := newModelSelectionSession(t, nil, nil)
	s.cfg.ConfiguredProviderInstances = []string{"work", "google", "work", " "}
	s.client.Register(&fakeAdapter{name: "client-only"})
	_, err := s.resolveExplicitAgentModel(context.Background(), s.currentProfile(), "missing/gpt-5.3", "")
	var selectionErr *ModelSelectionError
	if !errors.As(err, &selectionErr) || selectionErr.Code != ModelSelectionUnknownProvider {
		t.Fatalf("error = %v, want unknown provider", err)
	}
	if *calls != 0 {
		t.Fatalf("enumerations = %d, want 0 for unresolved instance", *calls)
	}
	if !slices.Equal(selectionErr.Alternatives, []string{"google", "openai", "work"}) {
		t.Fatalf("alternatives = %v, want configured instance", selectionErr.Alternatives)
	}
}

func TestResolveExplicitAgentModel_ReasoningEffortUsesExistingClamp(t *testing.T) {
	s, _ := newModelSelectionSession(t, []llm.ModelInfo{{
		ID: "gpt-5.3", SupportsTools: true, SupportsReasoning: true,
		ReasoningEffortLevels: []string{"low", "high"},
	}}, nil)
	if _, err := s.resolveExplicitAgentModel(context.Background(), s.currentProfile(), "gpt-5.3", "medium"); err != nil {
		t.Fatalf("known effort that clamps to high was rejected: %v", err)
	}
}

func TestValidateFrozenAgentModel_RejectsAliasInsteadOfReplacingFrozenModel(t *testing.T) {
	s, _ := newModelSelectionSession(t, []llm.ModelInfo{{
		ID: "gpt-5.3", Aliases: []string{"gpt-review-latest"}, SupportsTools: true,
	}}, nil)
	_, err := s.validateFrozenAgentModel(context.Background(), NewOpenAIProfile("gpt-review-latest"), "")
	var selectionErr *ModelSelectionError
	if !errors.As(err, &selectionErr) || selectionErr.Code != ModelSelectionRestoredModelUnavailable {
		t.Fatalf("error = %v, want restored model unavailable", err)
	}
}

func TestModelSelectionError_AlternativesAreSortedToolCapableRefs(t *testing.T) {
	s, _ := newModelSelectionSession(t, []llm.ModelInfo{
		{ID: "zeta", SupportsTools: true},
		{ID: "text-only"},
		{ID: "alpha", SupportsTools: true},
	}, nil)
	_, err := s.resolveExplicitAgentModel(context.Background(), s.currentProfile(), "missing", "")
	var selectionErr *ModelSelectionError
	if !errors.As(err, &selectionErr) {
		t.Fatalf("error = %v, want ModelSelectionError", err)
	}
	want := []string{"openai/alpha", "openai/zeta"}
	if !slices.Equal(selectionErr.Alternatives, want) {
		t.Fatalf("alternatives = %v, want %v", selectionErr.Alternatives, want)
	}
}
```

The clamp test must request `medium` from levels `[]string{"low", "high"}` and succeed because `llm.ClampReasoningEffort` resolves it to `high`; a non-empty effort on a model with neither live nor configured reasoning support must return `invalid_reasoning_effort`.

- [ ] **Step 2: Run the focused tests to verify they fail**

Run:

```bash
go test ./agent -run 'Test(ResolveExplicitAgentModel|ValidateFrozenAgentModel|ModelSelectionError|ConfiguredProviderInstances)' -count=1
go test ./cmdutil ./cmd/serf ./tools/tool-fluency/cmd/serf-fluency -run 'Test.*ConfiguredProviderInstances' -count=1
```

Expected: FAIL with undefined `resolvedAgentModel`, `ModelSelectionError`, `resolveExplicitAgentModel`, `validateFrozenAgentModel`, the two nonserialized configured-instance fields, and `cmdutil.ConfiguredProviderInstanceNames`.

- [ ] **Step 3: Implement exact typed errors and one bounded enumeration**

Use one eight-second child context, unless the caller deadline is sooner. Do not call `Complete` or `Stream`:

```go
const agentModelEnumerationTimeout = 8 * time.Second

func (s *Session) listModelsForAgentSelection(ctx context.Context, profile *provider.Profile) ([]llm.ModelInfo, error) {
	if hook := s.cfg.testOnly.listAgentModels; hook != nil {
		return hook(ctx, profile)
	}
	if s.client == nil {
		return nil, errors.New("LLM client is unavailable")
	}
	return s.client.ListModels(ctx, profile.ID())
}
```

Add the `ConfiguredProviderInstances []string` field with tag `json:"-"` to `SessionConfig`, and add the same runtime-only field to `RestoreSessionConfig`. Implement `cmdutil.ConfiguredProviderInstanceNames` directly from `providercfg.Config.Instances`, then populate it in `cmd/serf/run.go`, `cmd/serf/serve.go`, and the tool-fluency live constructor as shown above. Restore must reapply a defensive copy after `configFromSnapshot`; child config cloning must also take a defensive copy. Do not persist this inventory and do not use `Client.ProviderNames`: registered adapters are executable transports, not proof that an instance is configured for this session.

Implement `configuredProviderInstances(base)` from a defensive copy of `s.cfg.ConfiguredProviderInstances` plus `base.ID()`. Use it only for `unknown_provider_instance` alternatives; model alternatives continue to come from the one enumerated provider list.

Resolution and validation order must be:

```go
func (s *Session) resolveExplicitAgentModel(ctx context.Context, base *provider.Profile, ref, effort string) (resolvedAgentModel, error) {
	raw := strings.TrimSpace(ref)
	wantsCrossProvider := base.CrossProviderRef(raw)
	requestedInstance, _, _ := strings.Cut(raw, "/")
	resolved, crossProvider, err := s.resolveProfileForRef(base, raw)
	if err != nil || resolved == nil || (wantsCrossProvider && (!crossProvider || resolved.ID() != requestedInstance)) {
		return resolvedAgentModel{}, s.newModelSelectionError(ModelSelectionUnknownProvider, raw, resolved, s.configuredProviderInstances(base), err)
	}
	if crossProvider {
		resolved = resolved.WithCommunicateOverridesFrom(base)
	}
	validated, err := s.validateEnumeratedAgentModel(ctx, raw, resolved, effort, true)
	if err != nil {
		return resolvedAgentModel{}, err
	}
	return resolvedAgentModel{RequestedRef: raw, Profile: validated}, nil
}
```

`validateEnumeratedAgentModel` must:

1. enumerate exactly once;
2. turn any enumeration error, including unsupported listing, timeout, authentication, or network failure, into `model_availability_unverified` for explicit selection;
3. match the requested model by exact live ID first, then a live alias, then a catalog alias whose canonical ID is present in the live list;
4. reject non-chat/media IDs with the existing `modelSwitchVisible` name filter;
5. require `SupportsTools` from the live row or its exact/alias-resolved embedded catalog row;
6. enrich the profile with `WithModel(canonical.ID).WithLiveModelInfo(canonical)`;
7. reject unknown effort vocabulary (`llm.ReasoningEffortRank(normalized) == 0`) and effort on a non-reasoning model; and
8. accept known effort vocabulary using the existing clamp, without persisting the clamped value in place of the requested effort.

For a frozen restore, use the same enumeration/capability check but require `model.ID == profile.Model()`; map any resolution, enumeration, membership, or capability failure to `restored_model_unavailable` with the original error as `Cause`. Never select an alternative.

Limit `Alternatives` to sorted, credential-free `provider/model` names from the returned list whose catalog/live capability proves tool support. The error formatter may show those names, but never environment variables, headers, URLs containing credentials, or raw provider configuration.

- [ ] **Step 4: Add the deterministic test boundary without weakening production**

Add `listAgentModels` to `testConfig`. In `newSession`, when the caller did not set it, inject a fake that returns only the profile being validated:

```go
if cfg.testOnly.listAgentModels == nil {
	cfg.testOnly.listAgentModels = func(_ context.Context, p *provider.Profile) ([]llm.ModelInfo, error) {
		return []llm.ModelInfo{{
			ID:                    p.Model(),
			Provider:              p.ID(),
			SupportsTools:          true,
			SupportsReasoning:      p.SupportsReasoning(),
			ReasoningEffortLevels: p.ReasoningEffortLevels(),
		}}, nil
	}
}
```

The new correctness tests must construct their own `Session` or explicitly replace this hook, so failures exercise the real validation logic rather than the permissive fixture. Do not add an exported production bypass.

- [ ] **Step 5: Run the focused tests to verify they pass**

Run:

```bash
go test ./agent -run 'Test(ResolveExplicitAgentModel|ValidateFrozenAgentModel|ModelSelectionError|ConfiguredProviderInstances)' -count=1
go test ./cmdutil ./cmd/serf ./tools/tool-fluency/cmd/serf-fluency -run 'Test.*ConfiguredProviderInstances' -count=1
```

Expected: PASS; every membership/capability case enumerates exactly once, an unresolved provider instance enumerates zero times, alternatives contain configured instances plus the current base only, and fresh/child/restore constructors retain the nonserialized inventory without slice aliasing.

- [ ] **Step 6: Commit the typed preflight**

```bash
git add agent/model_selection.go agent/model_selection_test.go agent/session_config.go agent/session_init.go agent/testkit_test.go agent/subagents.go cmdutil/load_client.go cmdutil/load_client_test.go cmd/serf/run.go cmd/serf/run_coverage_fuzz_test.go cmd/serf/serve.go cmd/serf/serve_residual_fuzz_test.go tools/tool-fluency/cmd/serf-fluency/main.go tools/tool-fluency/cmd/serf-fluency/coverage_program_fuzz_test.go
git commit -m "feat(agent): validate explicit agent model selections

Add typed, side-effect-free model selection errors and one bounded model-list
preflight for explicit agent and frozen restore profiles. Preserve the raw
request separately from the canonical profile, carry authoritative configured
instance names through fresh/child/restore construction, and keep provider
enumeration behind a deterministic test boundary."
```

### Task 2: Validate Before Delegate IDs, Jobs, Children, or Worktrees

**Files:**
- Modify: `agent/subagents.go:115-129,363-428,777-808`
- Modify: `agent/job_delegate.go:270-371`
- Create: `agent/job_delegate_model_selection_test.go`
- Modify: `agent/job_delegate_model_echo_test.go:90-110`
- Modify: `agent/plugin_agents_integration_test.go:169-260`
- Modify: `agent/subagents_fuzz_test.go:84-109,135-170`
- Modify: `agent/fuzz_jdr_restore_lifecycle_test.go:87-130`

**Interfaces:**
- Consumes: `resolvedAgentModel` and `(*Session).resolveExplicitAgentModel` from Task 1.
- Produces:

```go
type subagentModelSelection struct {
	Agent        *plugin.Agent
	RequestedRef string
	Profile      *provider.Profile
}

func (s *Session) selectSubagentModel(ctx context.Context, model, agentType, reasoningEffort string) (subagentModelSelection, error)
func (s *Session) prepareSubagentRunWithModelSelection(ctx context.Context, task, workingDir string, maxTurns int, agentType, reasoningEffort string, parentTasks []taskpkg.TaskTemplate, grantTools []string, selection subagentModelSelection) (*preparedSubagentRun, error)
```

- Existing `prepareSubagentRun(...)` remains as the internal wrapper for non-durable spawn callers: it calls `selectSubagentModel` once, then `prepareSubagentRunWithModelSelection`.
- `createDelegate` calls `selectSubagentModel` before `jobstore.NewDelegateID`, `jobstore.NewDelegateGeneration`, `jobstore.NewJobID`, and `createDelegateWorktree`, then passes the frozen selection into `prepareSubagentRunWithModelSelection`; it must never call the wrapper and re-enumerate.
- Restrict the `subagents.go` edit to model-selection extraction and the frozen selection parameter. Preserve Project 1's `SubagentExhausted`/typed budget classification and Project 3's transcript-read grant callbacks, job supervision fields, and notification behavior byte-for-byte.

- Add these helpers in `agent/job_delegate_model_selection_test.go` so residue checks include the job ledger, transcripts/meta, notifications, and live watch state:

```go
type delegateDurableSnapshot struct {
	JobEvents            int
	SessionFiles         []string
	WorktreePorcelain    string
	WorktreeMeta         []string
	PendingNotifications int
	ActiveWatches        int
	WatchHistory         int
	TerminalWatchFlushes int
}

func directoryNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("ReadDir(%s): %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	slices.Sort(names)
	return names
}

func snapshotDelegateDurableState(t *testing.T, r *wtDlgRepo) delegateDurableSnapshot {
	t.Helper()
	jm := r.s.jobManager
	jm.mu.Lock()
	activeWatches := len(jm.watches)
	watchHistory := len(jm.watchHistory)
	terminalFlushes := len(jm.terminalFlush)
	jm.mu.Unlock()
	return delegateDurableSnapshot{
		JobEvents:            len(loadJobStoreEvents(t, jm)),
		SessionFiles:         directoryNames(t, filepath.Join(r.stateDir, sessionsSubdir)),
		WorktreePorcelain:    wtGit(t, r.mainRoot, "worktree", "list", "--porcelain"),
		WorktreeMeta:         directoryNames(t, r.metaDir(t)),
		PendingNotifications: r.s.peekNotifications(),
		ActiveWatches:        activeWatches,
		WatchHistory:         watchHistory,
		TerminalWatchFlushes: terminalFlushes,
	}
}

func newModelSelectionWtRepo(t *testing.T, models []llm.ModelInfo, listErr error) (*wtDlgRepo, *fakeAdapter, *int) {
	t.Helper()
	adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateWithDefaultOutput("done") },
	}}
	client := llm.NewClient()
	client.Register(adapter)
	r := newWtDlgRepo(t, client)
	r.s.resolveProfile = testResolver
	calls := 0
	r.s.cfg.testOnly.listAgentModels = func(_ context.Context, _ *provider.Profile) ([]llm.ModelInfo, error) {
		calls++
		return append([]llm.ModelInfo(nil), models...), listErr
	}
	return r, adapter, &calls
}

func retainedDelegateChild(t *testing.T, s *Session, jobID string) *Session {
	t.Helper()
	rec := loadShellRecord(t, s.jobManager, jobID)
	if rec.DelegateRestore == nil {
		t.Fatalf("job %s has no delegate restore descriptor", jobID)
	}
	sub := s.subagents.get(rec.DelegateRestore.ChildSessionID)
	if sub == nil || sub.sess == nil {
		t.Fatalf("job %s has no retained child runtime", jobID)
	}
	return sub.sess
}

func lastAssistantTurn(t *testing.T, s *Session) schema.Turn {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.history) - 1; i >= 0; i-- {
		if s.history[i].Kind == schema.TurnAssistant {
			return s.history[i]
		}
	}
	t.Fatal("session history has no assistant turn")
	return schema.Turn{}
}
```

- [ ] **Step 1: Write failing behavioral tests for inheritance, explicit selection, and zero residue**

Use fake model listers and real Serf delegate plumbing. One table covers every typed preflight rejection and proves that each leaves all durable and notification/watch state unchanged:

```go
func TestCreateDelegate_InvalidExplicitModelsLeaveNoDurableResidue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ref      string
		models   []llm.ModelInfo
		listErr  error
		wantCode ModelSelectionErrorCode
	}{
		{name: "unknown instance", ref: "missing/gpt-5.3", wantCode: ModelSelectionUnknownProvider},
		{name: "unknown model", ref: "openai/not-real", models: []llm.ModelInfo{{ID: "gpt-5.2", SupportsTools: true}}, wantCode: ModelSelectionUnknownModel},
		{name: "missing tools", ref: "openai/text-only", models: []llm.ModelInfo{{ID: "text-only"}}, wantCode: ModelSelectionMissingCapability},
		{name: "availability unverified", ref: "openai/gpt-5.3", listErr: errors.New("catalog offline"), wantCode: ModelSelectionAvailabilityUnverified},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, adapter, _ := newModelSelectionWtRepo(t, tc.models, tc.listErr)
			before := snapshotDelegateDurableState(t, r)
			res := r.s.createDelegate(context.Background(), delegateArgs{
				Task: "do isolated work", Model: tc.ref, Isolation: "worktree",
			})
			var selectionErr *ModelSelectionError
			if !errors.As(res.Err, &selectionErr) || selectionErr.Code != tc.wantCode {
				t.Fatalf("error = %v, want ModelSelectionError code %q", res.Err, tc.wantCode)
			}
			if res.DelegateID != "" || res.JobID != "" || res.TranscriptRef != "" {
				t.Fatalf("rejected selection minted handles: %+v", res)
			}
			if got := adapter.Requests(); len(got) != 0 {
				t.Fatalf("rejected selection reached provider: %+v", got)
			}
			if after := snapshotDelegateDurableState(t, r); !reflect.DeepEqual(after, before) {
				t.Fatalf("durable state changed\nbefore: %+v\nafter:  %+v", before, after)
			}
		})
	}
}
```

Add the successful inheritance and canonical-alias cases:

```go
func TestCreateDelegate_OmittedModelInheritsWithoutEnumeration(t *testing.T) {
	r, adapter, calls := newModelSelectionWtRepo(t, nil, errors.New("must not enumerate"))
	res := r.s.createDelegate(context.Background(), delegateArgs{Task: "inherit", BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if *calls != 0 {
		t.Fatalf("enumerations = %d, want 0", *calls)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 || reqs[0].Provider != "openai" || reqs[0].Model != "gpt-5.2" {
		t.Fatalf("inherited requests = %+v", reqs)
	}
}

func TestCreateDelegate_ExplicitAliasUsesCanonicalModel(t *testing.T) {
	r, adapter, calls := newModelSelectionWtRepo(t, []llm.ModelInfo{{
		ID: "gpt-5.3", Aliases: []string{"gpt-review-latest"}, SupportsTools: true,
	}}, nil)
	res := r.s.createDelegate(context.Background(), delegateArgs{Task: "review", Model: "gpt-review-latest", BlockTimeoutMS: 5000})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if *calls != 1 {
		t.Fatalf("enumerations = %d, want 1", *calls)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 || reqs[0].Model != "gpt-5.3" || res.Model != "openai/gpt-5.3" {
		t.Fatalf("canonical request/result = requests %+v result %+v", reqs, res)
	}
}
```

The cross-family test starts with an OpenAI parent, selects `google/gemini-3-pro`, and proves both the selected child's request/tool state and the unchanged parent state:

```go
func TestCreateDelegate_ExplicitCrossFamilyUsesSelectedRequestAndTools(t *testing.T) {
	googleAdapter := &fakeAdapter{name: "google", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			resp := communicateWithDefaultOutput("google done")
			resp.Model = "gemini-3-pro-actual"
			return resp
		},
	}}
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	client.Register(googleAdapter)
	r := newWtDlgRepo(t, client)
	r.s.resolveProfile = testResolver
	r.s.cfg.OpenAIResponsesContinuation = "auto"
	r.s.cfg.testOnly.listAgentModels = func(_ context.Context, p *provider.Profile) ([]llm.ModelInfo, error) {
		if p.ID() != "google" {
			t.Fatalf("enumerated provider = %q, want google", p.ID())
		}
		return []llm.ModelInfo{{
			ID: "gemini-3-pro", Provider: "google", SupportsTools: true,
			SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "high"},
		}}, nil
	}
	parentProfile := r.s.currentProfile()
	parentProfileOptions := cloneMap(parentProfile.ProviderOptions())
	parentProfileTools := append([]llm.ToolDefinition(nil), parentProfile.ToolDefinitions()...)
	parentRegistryTools := append([]llm.ToolDefinition(nil), r.s.reg.Definitions()...)

	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task: "use google tools", Model: "google/gemini-3-pro", ReasoningEffort: "medium", BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	reqs := googleAdapter.Requests()
	if len(reqs) != 1 {
		t.Fatalf("google requests = %d, want 1", len(reqs))
	}
	req := reqs[0]
	if req.Provider != "google" || req.Model != "gemini-3-pro" {
		t.Fatalf("child request = %s/%s", req.Provider, req.Model)
	}
	if !requestHasTool(req, "web_search") {
		t.Fatal("google child did not receive its selected-profile web_search function tool")
	}
	if req.ReasoningEffort == nil || *req.ReasoningEffort != "high" {
		t.Fatalf("google child reasoning effort = %#v, want selected-model clamp to high", req.ReasoningEffort)
	}
	if req.HistoryMode != llm.HistoryModeFullHistory || req.PreviousResponseID != "" {
		t.Fatalf("google child inherited OpenAI continuation state: mode %q previous %q", req.HistoryMode, req.PreviousResponseID)
	}
	child := retainedDelegateChild(t, r.s, res.JobID)
	if child.currentProfile().BehaviorTag() != "google" {
		t.Fatalf("child behavior tag = %q", child.currentProfile().BehaviorTag())
	}
	turn := lastAssistantTurn(t, child)
	if turn.ResponseProvider != "google" || turn.ResponseRequestModel != "gemini-3-pro" || turn.ResponseModel != "gemini-3-pro-actual" {
		t.Fatalf("turn provenance = requested %s/%s actual %s", turn.ResponseProvider, turn.ResponseRequestModel, turn.ResponseModel)
	}
	if r.s.currentProfile() != parentProfile ||
		!reflect.DeepEqual(parentProfile.ProviderOptions(), parentProfileOptions) ||
		!reflect.DeepEqual(parentProfile.ToolDefinitions(), parentProfileTools) ||
		!reflect.DeepEqual(r.s.reg.Definitions(), parentRegistryTools) {
		t.Fatal("selected child profile mutated parent profile or registry state")
	}
}
```

Add the plugin-agent precedence test; the configured agent pin must be the raw request that is validated and persisted, while the ignored delegate argument must not leak into the child:

```go
func TestCreateDelegate_PluginAgentPinnedModelIsTheEffectiveExplicitSelection(t *testing.T) {
	r, adapter, calls := newModelSelectionWtRepo(t, []llm.ModelInfo{{
		ID: "gpt-5.3", Aliases: []string{"gpt-review-latest"}, SupportsTools: true,
	}}, nil)
	r.s.pluginAgents = map[string]plugin.Agent{
		"reviewer": {Name: "reviewer", PluginName: "test", Model: "gpt-review-latest"},
	}
	res := r.s.createDelegate(context.Background(), delegateArgs{
		Task: "review", AgentType: "reviewer", Model: "gpt-5.2", BlockTimeoutMS: 5000,
	})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v", res.Err)
	}
	if *calls != 1 {
		t.Fatalf("enumerations = %d, want 1", *calls)
	}
	reqs := adapter.Requests()
	if len(reqs) != 1 || reqs[0].Model != "gpt-5.3" {
		t.Fatalf("plugin-pinned requests = %+v", reqs)
	}
	desc := loadShellRecord(t, r.s.jobManager, res.JobID).DelegateRestore
	if desc == nil || desc.RequestedModel != "gpt-review-latest" || desc.ResolvedModel != "gpt-5.3" {
		t.Fatalf("plugin-pinned descriptor = %+v", desc)
	}
}
```

This is the selected-profile isolation/precedence contract; do not add family routing logic.

- [ ] **Step 2: Run the delegate model tests to verify they fail**

Run: `go test ./agent -run 'TestCreateDelegate_(InvalidExplicitModels|OmittedModel|ExplicitAlias|ExplicitCrossFamily|PluginAgentPinnedModel)' -count=1`

Expected: FAIL because the current path mints IDs/worktrees before resolving, accepts unverifiable/tool-less models, and resolves again inside `prepareSubagentRun`.

- [ ] **Step 3: Split selection from child construction**

Implement precedence exactly once:

```go
func (s *Session) selectSubagentModel(ctx context.Context, model, agentType, effort string) (subagentModelSelection, error) {
	base := s.currentProfile()
	selection := subagentModelSelection{Profile: base}
	if agentType = strings.TrimSpace(agentType); agentType != "" {
		a, ok := s.pluginAgents[agentType]
		if !ok {
			return subagentModelSelection{}, fmt.Errorf("unknown plugin agent type: %s", agentType)
		}
		selection.Agent = &a
	}

	effectiveRef := strings.TrimSpace(model)
	if selection.Agent != nil {
		agentModel := strings.TrimSpace(selection.Agent.Model)
		if agentModel != "" && agentModel != "inherit" {
			effectiveRef = agentModel
		}
	}
	if effectiveRef == "" {
		return selection, nil
	}
	resolved, err := s.resolveExplicitAgentModel(ctx, base, effectiveRef, effort)
	if err != nil {
		return subagentModelSelection{}, err
	}
	selection.RequestedRef = resolved.RequestedRef
	selection.Profile = resolved.Profile
	return selection, nil
}
```

Delete the model and plugin-agent resolution block at `subagents.go:394-426` from `prepareSubagentRunWithModelSelection`; use only `selection.Agent` and `selection.Profile`. Set `prepared.requestedModel = selection.RequestedRef`, not the possibly ignored delegate argument.

- [ ] **Step 4: Move the preflight ahead of all durable creation**

In `createDelegate`, run validation after pure argument/security/grant checks and before the first ID call:

```go
selection, err := s.selectSubagentModel(ctx, args.Model, args.AgentType, args.ReasoningEffort)
if err != nil {
	return delegateStartFailed(err)
}

delegateID := jobstore.NewDelegateID()
delegateGeneration := jobstore.NewDelegateGeneration()
jobID := jobstore.NewJobID()
```

After optional worktree creation, call:

```go
prepared, err := s.prepareSubagentRunWithModelSelection(
	ctx, task, workingDir, 0, args.AgentType, args.ReasoningEffort, nil, nil, selection,
)
```

Keep existing rollback code for failures that occur after worktree creation. Do not return minted IDs for model-selection failures because those failures now precede minting.

- [ ] **Step 5: Update existing explicit-model fixtures to enumerate deterministically**

In `job_delegate_model_echo_test.go`, make `TestCreateDelegate_ExplicitModelArgEchoesPin` explicitly enumerate its selected model:

```go
s := newDelegateTestSession(t, c)
s.cfg.testOnly.listAgentModels = func(context.Context, *provider.Profile) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{ID: "gpt-5.3", Provider: "openai", SupportsTools: true}}, nil
}
```

In `plugin_agents_integration_test.go`, install a counted enumeration immediately after `NewSession` in `TestSpawnAgent_PluginAgentType_Model`, then assert only the `override` row enumerated:

```go
listCalls := 0
sess.cfg.testOnly.listAgentModels = func(context.Context, *provider.Profile) ([]llm.ModelInfo, error) {
	listCalls++
	return []llm.ModelInfo{{ID: "gpt-4.1-nano", Provider: "openai", SupportsTools: true}}, nil
}
// After waitForRuntimeSubagent:
wantListCalls := 0
if tc.name == "override" {
	wantListCalls = 1
}
if listCalls != wantListCalls {
	t.Fatalf("model enumerations = %d, want %d", listCalls, wantListCalls)
}
```

The tagged fuzz harnesses construct raw `Session` values instead of using `newSession`. Add this deterministic lister to each harness's existing `testConfig`; it returns only the profile under validation, so generated explicit refs exercise orchestration rather than ambient provider availability:

```go
listAgentModels: func(_ context.Context, p *provider.Profile) ([]llm.ModelInfo, error) {
	return []llm.ModelInfo{{
		ID: p.Model(), Provider: p.ID(), SupportsTools: true,
		SupportsReasoning: p.SupportsReasoning(),
		ReasoningEffortLevels: p.ReasoningEffortLevels(),
	}}, nil
},
```

Do not add paid/live tests: scripted enumeration and the captured real child request cover the behavior.

- [ ] **Step 6: Run the focused delegate tests**

Run: `go test ./agent -run 'TestCreateDelegate_(InvalidExplicitModels|OmittedModel|ExplicitAlias|ExplicitCrossFamily|PluginAgentPinnedModel|ExplicitModelArgEchoesPin)' -count=1`

Expected: PASS; rejected selections return no delegate/job/transcript handles and leave every filesystem/job/worktree snapshot byte-for-byte unchanged.

- [ ] **Step 7: Commit the pre-creation delegate integration**

```bash
git add agent/subagents.go agent/job_delegate.go agent/job_delegate_model_selection_test.go agent/job_delegate_model_echo_test.go agent/plugin_agents_integration_test.go agent/subagents_fuzz_test.go agent/fuzz_jdr_restore_lifecycle_test.go
git commit -m "fix(agent): preflight delegate models before durable creation

Resolve the effective delegate or plugin-agent model exactly once before IDs,
jobs, children, transcripts, or worktrees are created. Pass the frozen profile
into child preparation and cover inheritance, canonical same-family selection,
cross-family request construction, and residue-free rejection."
```

### Task 3: Freeze Durable Provenance and Validate Exact Restore

**Files:**
- Modify: `agent/job_delegate.go:943-980,1021-1087,2097-2171`
- Modify: `agent/job_delegate_create_test.go:500-660`
- Modify: `agent/job_delegate_send_test.go:1244-1319,1381-1445,1481-1598`
- Modify: `agent/internal/jobstore/fold_test.go:540-625`
- Modify: `agent/job_delegate_model_selection_test.go`

**Interfaces:**
- Consumes: `subagentModelSelection.RequestedRef`, `resolvedAgentModel.Profile`, and `(*Session).validateFrozenAgentModel`.
- Produces the context-aware restore preflight while retaining the existing context-free wrapper for projection callers:

```go
func (s *Session) assessDelegateResumabilityContext(ctx context.Context, rec *jobstore.JobRecord, mode delegateResumabilityMode) delegateResumability
func notResumableSendErrorWithCause(reason string, cause error) error
```

- Add `Cause error` to the in-memory-only `delegateResumability`; do not persist it. `delegate_send` keeps the existing `target_not_resumable:profile_unavailable` prefix and wraps the typed cause so `errors.As` can recover `*ModelSelectionError`.
- Restrict `job_delegate.go` edits to pre-creation model validation, frozen restore validation, and typed-cause plumbing. Preserve Project 1's `exhausted` terminal/result/resumability mapping and Project 3's transcript-read authorization, terminal/output/notification ordering, and compact job-result surfaces.
- Produces no new persisted fields. Existing `jobstore.DelegateRestoreDescriptor` fields remain authoritative:

```go
RequestedModel    string `json:"requested_model,omitempty"`
ResolvedProfileID string `json:"resolved_profile_id,omitempty"`
ResolvedModel     string `json:"resolved_model,omitempty"`
ReasoningEffort   string `json:"reasoning_effort,omitempty"`
```

- Produced turn provenance remains `schema.Turn.ResponseProvider`, `ResponseRequestModel`, and `ResponseModel`, plus the prerequisite's `AttemptGroupID` join; canonical API-attempt provenance is verified in Task 4.

- [ ] **Step 1: Extend the descriptor test to fail on raw/canonical drift**

Change the existing durable descriptor test to request a live alias and enumerate its canonical model:

```go
Model: "gpt-review-latest"
```

with:

```go
[]llm.ModelInfo{{
	ID: "gpt-5.3", Aliases: []string{"gpt-review-latest"},
	SupportsTools: true, SupportsReasoning: true,
	ReasoningEffortLevels: []string{"low", "medium", "high"},
}}
```

Assert before the first child request:

```go
if desc.RequestedModel != "gpt-review-latest" ||
	desc.ResolvedProfileID != "openai" || desc.ResolvedModel != "gpt-5.3" ||
	desc.ReasoningEffort != "high" {
	t.Fatalf("descriptor provenance = raw %q resolved %s/%s effort %q", desc.RequestedModel, desc.ResolvedProfileID, desc.ResolvedModel, desc.ReasoningEffort)
}
```

The plugin-agent raw/effective/canonical descriptor case is already pinned by `TestCreateDelegate_PluginAgentPinnedModelIsTheEffectiveExplicitSelection` in Task 2; keep that assertion green while changing descriptor code.

- [ ] **Step 2: Write failing exact-restore tests**

Extend `TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile` by setting `rec.DelegateRestore.RequestedModel = "gpt-review-latest"` while keeping `ResolvedProfileID: "work"` and `ResolvedModel: "descriptor-model"`. Install exact availability before calling `sendDelegateMessage`:

```go
s.cfg.testOnly.listAgentModels = func(_ context.Context, p *provider.Profile) ([]llm.ModelInfo, error) {
	if p.ID() != "work" || p.Model() != "descriptor-model" {
		t.Fatalf("restore enumerated %s/%s, want frozen work/descriptor-model", p.ID(), p.Model())
	}
	return []llm.ModelInfo{{
		ID: "descriptor-model", Provider: "work", SupportsTools: true,
		SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "medium", "high"},
	}}, nil
}
```

Keep its existing assertions that only `work/descriptor-model` receives a request. Add assertions that the resumed descriptor still contains raw `gpt-review-latest`, exact resolved `work/descriptor-model`, and the original reasoning effort.

Add a rejection test that exercises both a verifiably missing frozen model and an enumeration failure:

```go
func TestDelegateRestore_FrozenProfileFailuresLeaveNoResidue(t *testing.T) {
	for _, tc := range []struct {
		name    string
		models  []llm.ModelInfo
		listErr error
	}{
		{name: "model unavailable", models: []llm.ModelInfo{{ID: "gpt-5.4", Provider: "work", SupportsTools: true}}},
		{name: "availability unverified", listErr: errors.New("catalog offline")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewClient()
			openAI := &fakeAdapter{name: "openai"}
			client.Register(openAI)
			s := newDelegateRestorePreflightSession(t, client)
			rec := seedStoppedDelegateRestoreRecord(t, s)
			markStoredDelegateResumable(t, s, rec)
			rec = loadShellRecord(t, s.jobManager, rec.JobID)
			rec.DelegateRestore.RequestedModel = "gpt-review-latest"
			rec.DelegateRestore.ResolvedProfileID = "work"
			rec.DelegateRestore.ResolvedModel = "gpt-5.3"
			replaceStoredDelegateRecord(t, s, rec)
			s.resolveProfile = func(ref string) (*provider.Profile, error) {
				if ref != "work/gpt-5.3" {
					return nil, fmt.Errorf("unexpected profile ref %s", ref)
				}
				return WithProviderID(NewOpenAIProfile("gpt-5.3"), "work"), nil
			}
			s.cfg.testOnly.listAgentModels = func(context.Context, *provider.Profile) ([]llm.ModelInfo, error) {
				return append([]llm.ModelInfo(nil), tc.models...), tc.listErr
			}
			beforeEvents := len(loadJobStoreEvents(t, s.jobManager))
			beforeProfile := s.currentProfile()
			res := s.sendDelegateMessage(context.Background(), sendMessageArgs{
				Target: rec.DelegateID, Message: "resume", OnIdle: "start",
			})
			if res.Err == nil || !strings.HasPrefix(res.Err.Error(), "target_not_resumable:profile_unavailable") {
				t.Fatalf("error = %v, want profile_unavailable", res.Err)
			}
			var selectionErr *ModelSelectionError
			if !errors.As(res.Err, &selectionErr) || selectionErr.Code != ModelSelectionRestoredModelUnavailable {
				t.Fatalf("typed cause = %v, want restored_model_unavailable", res.Err)
			}
			if len(loadJobStoreEvents(t, s.jobManager)) != beforeEvents ||
				s.subagents.get(rec.DelegateRestore.ChildSessionID) != nil ||
				s.currentProfile() != beforeProfile || len(openAI.Requests()) != 0 {
				t.Fatal("failed restore created a job/runtime/request or replaced the parent profile")
			}
		})
	}
}
```

Keep `TestCreateDelegate_ExplicitCrossFamilyUsesSelectedRequestAndTools` green; its retained-child assertion from Task 2 already proves produced-turn requested/actual model provenance without adding an exported history API.

- [ ] **Step 3: Run the provenance/restore tests to verify they fail**

Run: `go test ./agent -run '^(TestCreateDelegateDescriptor.*|TestDelegateRestore_.*|TestCreateDelegate_ProducedTurn.*|TestConfiguredProviderInstances_ReconstructedDelegateChildAndGrandchild|TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile|TestJobSendMessageReconstructsRestoredDelegateRuntimeFromDescriptor)$' -count=1`

Expected: FAIL because restore currently accepts a resolver-produced profile without availability/capability validation and the descriptor test still stores the pre-canonical request as the resolved model.

- [ ] **Step 4: Validate the frozen descriptor before restore side effects**

Keep descriptor fields authoritative and ignore `meta.Model` for profile selection. Keep configuration resolution separate from live enumeration so `job_list`/resumability projection does not perform network work; make the existing resolver return a typed frozen-identity error:

```go
func (s *Session) resolveDelegateRestoreProfileRef(base *provider.Profile, profileID, model string) (*provider.Profile, error) {
	ref := profileID + "/" + model
	var resolved *provider.Profile
	var err error
	if s.resolveProfile != nil {
		resolved, err = s.resolveProfile(ref)
	} else if profileID == base.ID() {
		resolved = base.WithModel(model)
	} else {
		err = fmt.Errorf("profile %q unavailable", ref)
	}
	if err != nil || resolved == nil {
		return nil, &ModelSelectionError{Code: ModelSelectionRestoredModelUnavailable, Ref: ref, Provider: profileID, Model: model, Cause: err}
	}
	if resolved.ID() != profileID || resolved.Model() != model {
		return nil, &ModelSelectionError{
			Code: ModelSelectionRestoredModelUnavailable, Ref: ref,
			Provider: profileID, Model: model,
			Cause: fmt.Errorf("resolver returned %s/%s", resolved.ID(), resolved.Model()),
		}
	}
	if resolved.ID() != base.ID() {
		resolved = resolved.WithCommunicateOverridesFrom(base)
	}
	return resolved, nil
}
```

Rename the existing assessment body from `assessDelegateResumability` to `assessDelegateResumabilityContext`, add `ctx context.Context` as its first non-receiver parameter, and retain this wrapper for projection/tests that do not carry a request context:

```go
func (s *Session) assessDelegateResumability(rec *jobstore.JobRecord, mode delegateResumabilityMode) delegateResumability {
	return s.assessDelegateResumabilityContext(context.Background(), rec, mode)
}
```

Inside the renamed body, replace the current configured-profile block exactly with:

```go
profile, err := s.resolveDelegateRestoreProfile(meta, desc)
if err == nil && mode == delegateResumabilityPreflight {
	profile, err = s.validateFrozenAgentModel(ctx, profile, desc.ReasoningEffort)
}
if err != nil {
	return delegateResumability{Reason: notResumableProfileUnavailable, Cause: err}
}
```

Add the cause-preserving renderer beside `notResumableSendError`:

```go
func notResumableSendErrorWithCause(reason string, cause error) error {
	base := notResumableSendError(reason)
	if cause == nil {
		return base
	}
	return fmt.Errorf("%s: %w", base.Error(), cause)
}
```

Change only the two `delegate_send` restore-preflight call sites to `assessDelegateResumabilityContext(ctx, rec, delegateResumabilityPreflight)` and pass `assessment.Cause` to `notResumableSendErrorWithCause`. Projection/finalization callers retain the wrapper and therefore do configuration-only validation. Live validation must happen before `beginReconstruction`, `restoreDelegateChildEnvironment`, worktree lock reacquisition, `RestoreSessionFromMetaWithConfig`, or a resumed job append.

In `restoreTerminalDelegateChildClaimed`, explicitly forward the runtime-only configured-instance inventory in the existing literal; it cannot come from `meta.Config`:

```go
restoreCfg := RestoreSessionConfig{
	StateDir:                    s.stateDir,
	ResolveProfile:              s.resolveProfile,
	ConfiguredProviderInstances: append([]string(nil), s.cfg.ConfiguredProviderInstances...),
	ModelFallbacks:              append([]string(nil), s.cfg.ModelFallbacks...),
	// Preserve the existing retry, clock, test, spawn, history, and side-effect fields.
}
```

Do not add the inventory to `schema.ConfigSnapshot` and do not reconstruct it from `s.client.ProviderNames()`.

Do not consult `RequestedModel` during restore and do not fall back to `meta.Model`; those are provenance and legacy session metadata, not replacement-selection inputs.

- [ ] **Step 5: Preserve descriptor provenance across resume**

Keep `resumedDelegateRestoreDescriptor` as a field-for-field copy of `RequestedModel`, `ResolvedProfileID`, `ResolvedModel`, and `ReasoningEffort`. In `delegateRestoreDescriptor`, read `prepared.requestedModel` for the raw effective ref and the child profile only for canonical resolved identity.

Retain the existing `delegateModelReportForDescriptor` terminal echo so restored results report `ResolvedProfileID/ResolvedModel`, not the parent's current profile.

- [ ] **Step 6: Update successful restore fixtures with explicit fake availability**

For the two custom `work/descriptor-model` success paths in `job_delegate_send_test.go`, add this hook to the parent session or the raw `RestoreSessionConfig.testOnly` used to rebuild it:

```go
listAgentModels: func(_ context.Context, p *provider.Profile) ([]llm.ModelInfo, error) {
	if p.ID() != "work" || p.Model() != "descriptor-model" {
		return nil, fmt.Errorf("unexpected restore profile %s/%s", p.ID(), p.Model())
	}
	return []llm.ModelInfo{{
		ID: "descriptor-model", Provider: "work", SupportsTools: true,
		SupportsReasoning: true, ReasoningEffortLevels: []string{"low", "medium", "high"},
	}}, nil
},
```

Keep the existing request assertions for provider/model, reasoning effort, frozen tools, result schema, and old-task exclusion. Do not weaken restore validation with a test-only skip.

Extend the runtime-only clone contract with `TestConfiguredProviderInstances_ReconstructedDelegateChildAndGrandchild` in `job_delegate_send_test.go`:

1. seed a stopped delegate restore record under a parent whose inventory is `[]string{"google", "openai", "work"}`;
2. force runtime loss and resume it through `sendDelegateMessage`, exercising `restoreTerminalDelegateChildClaimed` rather than direct top-level restore;
3. assert the reconstructed child has the same three instances, mutate its slice, and prove the parent slice is unchanged;
4. restore the child inventory, prepare an omitted-model grandchild from that reconstructed child, and assert the grandchild has the same inventory;
5. mutate the grandchild slice and prove both reconstructed child and root parent remain unchanged; and
6. register an extra client-only adapter and assert it never appears in any generation's configured alternatives.

Use the existing stopped-delegate/reconstruction helpers and deterministic lister. Do not persist new metadata or add a compatibility path.

- [ ] **Step 7: Run the focused restore/provenance tests**

Run: `go test ./agent ./agent/internal/jobstore -run '^(TestCreateDelegateDescriptor.*|TestDelegateRestore_.*|TestCreateDelegate_ProducedTurn.*|TestDelegateRestoreDescriptor.*|TestConfiguredProviderInstances_ReconstructedDelegateChildAndGrandchild|TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile|TestJobSendMessageReconstructsRestoredDelegateRuntimeFromDescriptor)$' -count=1`

Expected: PASS; raw request, canonical profile, reasoning effort, actual turn model, and resume identity remain distinct and correct.

- [ ] **Step 8: Commit durable provenance and restore validation**

```bash
git add agent/job_delegate.go agent/job_delegate_create_test.go agent/job_delegate_send_test.go agent/job_delegate_model_selection_test.go agent/internal/jobstore/fold_test.go
git commit -m "fix(agent): restore delegates on their frozen validated model

Persist the effective raw selection beside its canonical provider/model and
validate that exact frozen profile before reconstructing a delegate. Refuse
unavailable restores without choosing the parent, alias target, or any other
replacement, and pin per-turn requested/actual provenance."
```

### Task 4: Verify Configured Fallbacks in the Canonical API Log

**Files:**
- Create: `agent/session_fallback_provenance_test.go`

**Interfaces:**
- Consumes from the prerequisite plan: durable `apilog.APILogRecord`, `apilog.APIAttemptRecord`, `apilog.APIAttemptGroupSettlement`, and `apilog.DecodeRecord`; coordination `llm.APIAttemptMeta`, `llm.APIAttemptResult`, `llm.APIAttemptGroup`, `llm.BeginAPIAttempt`, `llm.NewSessionAPILogger`, and `(*llm.APILogger).Middleware`.
- Consumes existing policy: `SessionConfig.ModelFallbacks` and `modelFallbackEligible`.
- Produces only deterministic acceptance coverage. It does not add an attempt recorder, transcript record, fallback selector, or logging path.

Import the durable codec as:

```go
import apilog "primeradiant.com/serf/llm/apilog"
```

Add a fake provider that behaves like one transport boundary by opening and completing exactly one canonical attempt for each `Complete` call:

```go
type canonicalFallbackAdapter struct {
	respond func(llm.Request) (llm.Response, error)
}

func (a *canonicalFallbackAdapter) Name() string { return "openai" }

func (a *canonicalFallbackAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *canonicalFallbackAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	started := time.Now()
	attempt := llm.BeginAPIAttempt(ctx, llm.APIAttemptMeta{
		ProviderInstance: req.Provider,
		RequestModel:     req.Model,
		HistoryMode:      req.HistoryMode,
		EndpointFamily:   "test",
		Method:           http.MethodPost,
		Endpoint:         "https://canonical-fallback.invalid/v1/complete",
		Headers:          http.Header{"Content-Type": []string{"application/json"}},
		RequestBody:      []byte(req.Model),
		StartedAt:        started,
	})
	resp, err := a.respond(req)
	result := llm.APIAttemptResult{
		Response:     &resp,
		ResponseBody: []byte(resp.Model),
		Outcome:      apilog.AttemptSuccess,
		Err:          err,
		FinishedAt:   time.Now(),
	}
	if err != nil {
		result.Response = nil
		result.Outcome = apilog.AttemptProviderReject
		result.ErrorClass = "permanent"
	}
	attempt.Complete(result)
	return resp, err
}
```

Add helpers that attach the canonical logger and decode only its structured records:

```go
func newCanonicalFallbackSession(t *testing.T, fallbacks []string, respond func(llm.Request) (llm.Response, error)) (*Session, string) {
	t.Helper()
	stateDir := t.TempDir()
	logger, err := llm.NewSessionAPILogger(stateDir)
	if err != nil {
		t.Fatalf("NewSessionAPILogger: %v", err)
	}
	t.Cleanup(func() { _ = logger.Close() })
	client := llm.NewClient()
	client.Register(&canonicalFallbackAdapter{respond: respond})
	client.Use(logger.Middleware())
	policy := llm.RetryPolicy{MaxRetries: 0}
	s := newSession(t, withClient(client), withProfile(NewOpenAIProfile("primary")), withConfig(SessionConfig{
		StateDir: stateDir, MaxSubagentDepth: 1, NoProjectPrompts: true,
		ModelFallbacks: fallbacks, LLMRetryPolicy: &policy,
		testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, noSyncJobStore: true},
	}))
	return s, filepath.Join(stateDir, sessionsSubdir, s.ID()+".api.jsonl")
}

func readCanonicalFallbackRecords(t *testing.T, path string) ([]apilog.APIAttemptRecord, []apilog.APIAttemptGroupSettlement) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open canonical API log: %v", err)
	}
	defer f.Close()
	var attempts []apilog.APIAttemptRecord
	var settlements []apilog.APIAttemptGroupSettlement
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		rec, err := apilog.DecodeRecord(scanner.Bytes())
		if err != nil {
			t.Fatalf("decode canonical API attempt: %v", err)
		}
		switch rec := rec.(type) {
		case apilog.APIAttemptRecord:
			attempts = append(attempts, rec)
		case apilog.APIAttemptGroupSettlement:
			settlements = append(settlements, rec)
		default:
			t.Fatalf("unexpected canonical API record %T", rec)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan canonical API log: %v", err)
	}
	return attempts, settlements
}
```

- `api_attempt` records must already be appended and synced before another configured fallback begins. One `attempt_group_settlement` is appended only after the outer call settles; attempts are never delayed or rewritten to add finality/count.
- No new fallback source is introduced. The production loop continues to iterate only over the literal `s.cfg.ModelFallbacks` slice in order.

- [ ] **Step 1: Write the configured-fallback canonical-log tests**

The primary-failure/fallback-success test asserts the canonical record fields and the semantic turn join without reading any transcript API record:

```go
func TestFallbackProvenance_ConfiguredAttemptsShareCanonicalGroupAndExposeModels(t *testing.T) {
	perm := llm.ErrorFromHTTPStatus("openai", 403, "primary denied", nil, nil)
	var apiPath string
	s, apiPath := newCanonicalFallbackSession(t, []string{"fallback-b"}, func(req llm.Request) (llm.Response, error) {
		if req.Model == "primary" {
			return llm.Response{}, perm
		}
		attempts, settlements := readCanonicalFallbackRecords(t, apiPath)
		if len(attempts) != 1 || attempts[0].RequestModel != "primary" || len(settlements) != 0 {
			t.Fatalf("before fallback transport: attempts %+v settlements %+v; want durable primary and no settlement", attempts, settlements)
		}
		return llm.Response{Model: "fallback-b-actual", Message: llm.Assistant("done")}, nil
	})
	if _, err := s.ProcessInput(context.Background(), "run", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	attempts, settlements := readCanonicalFallbackRecords(t, apiPath)
	if len(attempts) != 2 {
		t.Fatalf("API attempts = %d, want primary + configured fallback", len(attempts))
	}
	if attempts[0].AttemptGroupID == "" || attempts[0].AttemptGroupID != attempts[1].AttemptGroupID ||
		attempts[0].AttemptIndex != 1 || attempts[1].AttemptIndex != 2 {
		t.Fatalf("attempt lifecycle = %+v", attempts)
	}
	if len(settlements) != 1 || settlements[0].AttemptGroupID != attempts[0].AttemptGroupID ||
		settlements[0].FinalAttemptID != attempts[1].AttemptID || settlements[0].FinalAttemptCount != 2 ||
		string(settlements[0].Outcome) != "success" {
		t.Fatalf("group settlement = %+v, attempts = %+v", settlements, attempts)
	}
	if attempts[0].ProviderInstance != "openai" || attempts[0].RequestModel != "primary" ||
		attempts[1].ProviderInstance != "openai" || attempts[1].RequestModel != "fallback-b" ||
		attempts[1].Response == nil || attempts[1].Response.Model != "fallback-b-actual" {
		t.Fatalf("requested/actual provenance = %+v", attempts)
	}
	turn := lastAssistantTurn(t, s)
	if turn.AttemptGroupID != attempts[0].AttemptGroupID || turn.ResponseRequestModel != "fallback-b" || turn.ResponseModel != "fallback-b-actual" {
		t.Fatalf("turn/API join = turn %+v attempts %+v", turn, attempts)
	}
}
```

Use one table for no fallback, literal order, and exhaustion:

```go
func TestFallbackProvenance_UsesOnlyLiteralConfiguredOrderAndPreservesTerminalError(t *testing.T) {
	for _, tc := range []struct {
		name      string
		fallbacks []string
		want      []string
	}{
		{name: "none", want: []string{"primary"}},
		{name: "configured", fallbacks: []string{"fallback-b", "fallback-c"}, want: []string{"primary", "fallback-b", "fallback-c"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, apiPath := newCanonicalFallbackSession(t, tc.fallbacks, func(req llm.Request) (llm.Response, error) {
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 403, req.Model+" denied", nil, nil)
			})
			_, err := s.ProcessInput(context.Background(), "run", nil)
			if err == nil || !strings.Contains(err.Error(), tc.want[len(tc.want)-1]+" denied") {
				t.Fatalf("terminal error = %v, want final configured model", err)
			}
			attempts, settlements := readCanonicalFallbackRecords(t, apiPath)
			got := make([]string, len(attempts))
			for i, attempt := range attempts {
				got[i] = attempt.RequestModel
				if attempt.AttemptIndex != i+1 {
					t.Fatalf("attempt %d lifecycle = %+v", i, attempt)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("attempted models = %v, want literal %v; records %+v", got, tc.want, attempts)
			}
			if len(settlements) != 1 || settlements[0].FinalAttemptID != attempts[len(attempts)-1].AttemptID ||
				settlements[0].FinalAttemptCount != len(tc.want) || string(settlements[0].Outcome) == "success" {
				t.Fatalf("failed group settlement = %+v, attempts %+v", settlements, attempts)
			}
		})
	}
}
```

The configured row proves that no family-, price-, latency-, or task-derived model appears. The final error remains `fallback-c denied` when both configured options fail.

- [ ] **Step 2: Run the canonical fallback tests**

Run: `go test ./agent -run 'TestFallbackProvenance_' -count=1`

Expected after the prerequisite: PASS with each canonical `api_attempt` durable before the next fallback, one final `attempt_group_settlement`, no transcript `api_call`, and no synthesized adapter record. If canonical types/lifecycle are missing, stop and complete the prerequisite; do not make this test compile against legacy logging.

- [ ] **Step 3: Run the existing fallback-policy tests unchanged**

Run: `go test ./agent -run 'TestFallbackChain_' -count=1`

Expected: PASS; retryable errors still use the retry loop rather than model fallback, endpoint/continuation fallback semantics remain unchanged, literal configured order remains unchanged, and exhaustion returns the final configured error.

- [ ] **Step 4: Commit only the canonical provenance coverage**

```bash
git add agent/session_fallback_provenance_test.go
git commit -m "test(agent): verify configured fallback API provenance

Prove primary and explicitly configured fallback transports append immediately
as distinct canonical API attempts, followed by one group settlement with
requested and actual model identity. Preserve literal fallback order and the
final configured error without adding another logger or fallback policy."
```

### Task 5: Run the Deterministic Acceptance Gate and Scope Audit

**Files:**
- Verify only: `agent/`, `agent/internal/jobstore/`, `cmdutil/`, `tools/tool-fluency/cmd/serf-fluency/`, existing model-switching tests in `server/`, `cmd/serf/`, `cmd/serf-hub/`, and `cmd/serf-tui/`

**Interfaces:**
- Consumes all prior task contracts.
- Produces no new code unless a failing assertion demonstrates a defect inside this plan's scope.

- [ ] **Step 1: Run the complete agent selection/restore/fallback gate**

Run:

```bash
go test ./agent ./agent/internal/jobstore -run '^(TestResolveExplicitAgentModel.*|TestValidateFrozenAgentModel.*|TestModelSelectionError.*|TestConfiguredProviderInstances_.*|TestCreateDelegate_.*Model.*|TestCreateDelegate_ExplicitCrossFamilyUsesSelectedRequestAndTools|TestDelegateRestore_.*|TestFallbackProvenance_.*|TestSendDelegateMessageRuntimeLostRestoreUsesDescriptorPreflightProfile|TestJobSendMessageReconstructsRestoredDelegateRuntimeFromDescriptor)$' -count=1
go test ./cmdutil ./cmd/serf ./tools/tool-fluency/cmd/serf-fluency -run 'Test.*ConfiguredProviderInstances' -count=1
```

Expected: PASS.

- [ ] **Step 2: Run existing turn-boundary switching tests unchanged**

Run:

```bash
go test ./agent ./server ./cmd/serf ./cmd/serf-hub ./cmd/serf-tui -run 'Test.*(SetModel|ModelSwitch|ThreadModelSet|ModelChanged)' -count=1
```

Expected: PASS. In particular, active-turn rejection/no-partial-mutation tests remain green and `Session.SetModel` retains its existing switching-specific availability policy. If this command fails because the new agent validator changed `thread/model/set` behavior, revert that coupling rather than changing the switching contract.

- [ ] **Step 3: Run the default deterministic suite**

Run:

```bash
go test ./... -count=1
go test -tags=serffuzz ./agent -run 'Fuzz(Safz_PrepareSubagentRun|DelegateCreationRestoreConfigProgram)' -count=1
```

Expected: PASS without provider credentials, network access, quota, or paid completions; tagged seed corpora use their explicit fake enumeration hooks.

- [ ] **Step 4: Run formatting and repository verification**

Run:

```bash
gofmt -w agent/model_selection.go agent/model_selection_test.go agent/subagents.go agent/job_delegate.go agent/job_delegate_model_selection_test.go agent/job_delegate_create_test.go agent/job_delegate_send_test.go agent/job_delegate_model_echo_test.go agent/plugin_agents_integration_test.go agent/session_fallback_provenance_test.go agent/session_config.go agent/session_init.go agent/testkit_test.go agent/subagents_fuzz_test.go agent/fuzz_jdr_restore_lifecycle_test.go cmdutil/load_client.go cmdutil/load_client_test.go cmd/serf/run.go cmd/serf/run_coverage_fuzz_test.go cmd/serf/serve.go cmd/serf/serve_residual_fuzz_test.go tools/tool-fluency/cmd/serf-fluency/main.go tools/tool-fluency/cmd/serf-fluency/coverage_program_fuzz_test.go
git diff --check
go test ./... -count=1
```

Expected: `gofmt` is clean, `git diff --check` prints nothing, and the suite passes.

- [ ] **Step 5: Audit the scope lock before finalizing**

Run:

```bash
git diff --name-only HEAD~4..HEAD
git diff HEAD~4..HEAD -- docs/superpowers/specs docs/superpowers/plans agent/internal/tool appwire cmd/serf-hub cmd/serf-tui
rg -n 'Complete\(|Stream\(' agent/model_selection.go
rg -n 'fallback|price|latency|risk|escalat' agent/model_selection.go agent/subagents.go
rg -n 'ProviderNames\(' agent/model_selection.go agent/subagents.go cmdutil/load_client.go cmd/serf/run.go cmd/serf/serve.go tools/tool-fluency/cmd/serf-fluency/main.go
rg -n 'ConfiguredProviderInstances' agent/session_config.go agent/session_init.go agent/subagents.go agent/job_delegate.go cmdutil/load_client.go cmd/serf/run.go cmd/serf/serve.go tools/tool-fluency/cmd/serf-fluency/main.go
rg -n 'AppendAPICall|transcript\.APICall|appendModelAPICallFunc|AdapterAttemptRecord' agent/session_fallback_provenance_test.go
```

Expected:

- no spec, Superpowers, AppWire, Web, TUI, or delegate-tool protocol file changed;
- overlapping edits in `agent/subagents.go` and `agent/job_delegate.go` preserve the prerequisite budget/exhaustion result metadata, partial-evidence resumability, compact job-status and bounded transcript-read surfaces, transcript-read grant callbacks, and terminal/output/notification ordering; review those hunks against the three prerequisite commits rather than reimplementing them here;
- `agent/model_selection.go` contains no completion or stream probe;
- configured-provider alternatives come only from `SessionConfig.ConfiguredProviderInstances` plus the current base instance; the `ProviderNames` search prints nothing in the new inventory/selection wiring;
- the configured-instance inventory is copied independently through top-level restore, ordinary child preparation, runtime-lost delegate reconstruction, and reconstructed-child-to-grandchild preparation, with no `ConfigSnapshot` persistence;
- model selection contains no family/price/latency/risk routing and no generated fallback;
- only the literal configured fallback loop in `session_model_call.go` can change the model during a provider attempt;
- fallback provenance tests consume `apilog.APIAttemptRecord` plus `apilog.APIAttemptGroupSettlement` from `<state-dir>/sessions/<session-id>.api.jsonl`, prove immediate attempt durability before fallback, and the legacy logging-symbol search prints nothing;
- no transcript/API logging implementation changed in this project; that ownership remains with the completed prerequisite plan;
- no compatibility branch accepts missing/invalid historical descriptor profiles; and
- no live test was added because scripted providers cover the required behavior.

- [ ] **Step 6: Record final verification in the last commit**

If formatting or verification required tracked corrections, commit only those corrections:

```bash
git add agent
git commit -m "test(agent): complete model selection correctness gate

Verify residue-free explicit-selection rejection, same- and cross-family child
construction, frozen restore identity, requested/actual provenance, configured
fallback attempts, and unchanged turn-boundary model switching."
```

If `git status --short` shows no corrections after Task 4, do not create an empty commit.
