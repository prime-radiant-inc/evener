package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultResponsesContinuationSupportRegistryPublicEnabledCodexDisabled(t *testing.T) {
	registry := DefaultResponsesContinuationSupportRegistry()

	public, ok := registry[ResponsesEndpointFamilyOpenAIPublic]
	if !ok {
		t.Fatalf("registry missing endpoint family %q", ResponsesEndpointFamilyOpenAIPublic)
	}
	if public.EndpointFamily != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("public EndpointFamily = %q, want %q", public.EndpointFamily, ResponsesEndpointFamilyOpenAIPublic)
	}
	if !public.Enabled || !public.StorageShapeProven || !public.ProductionPathProven {
		t.Fatalf("public support = %+v, want enabled with proven storage and production path", public)
	}
	if public.MaxAnchorAgeSeconds != 3600 {
		t.Fatalf("public MaxAnchorAgeSeconds = %d, want 3600", public.MaxAnchorAgeSeconds)
	}
	if public.StorageShapeProofID != "2026-06-24-responses-continuation-phase-0b" {
		t.Fatalf("public StorageShapeProofID = %q", public.StorageShapeProofID)
	}
	if public.ProductionPathProofID != "2026-06-24-responses-continuation-phase-12a-public" {
		t.Fatalf("public ProductionPathProofID = %q", public.ProductionPathProofID)
	}

	codex, ok := registry[ResponsesEndpointFamilyOpenAICodex]
	if !ok {
		t.Fatalf("registry missing endpoint family %q", ResponsesEndpointFamilyOpenAICodex)
	}
	if codex.EndpointFamily != ResponsesEndpointFamilyOpenAICodex {
		t.Fatalf("codex EndpointFamily = %q, want %q", codex.EndpointFamily, ResponsesEndpointFamilyOpenAICodex)
	}
	if codex.Enabled {
		t.Fatalf("codex support = %+v, want disabled", codex)
	}
	if codex.StorageShapeProven || codex.ProductionPathProven || codex.MaxAnchorAgeSeconds != 0 || codex.StorageShapeProofID != "" || codex.ProductionPathProofID != "" {
		t.Fatalf("codex support = %+v, want unproven disabled defaults", codex)
	}
}

func TestResponsesContinuationSupportForUnknownFamilyDisabled(t *testing.T) {
	support := ResponsesContinuationSupportFor(
		DefaultResponsesContinuationSupportRegistry(),
		ResponsesEndpointFamily("unknown_endpoint"),
	)

	if support.EndpointFamily != ResponsesEndpointFamily("unknown_endpoint") {
		t.Fatalf("EndpointFamily = %q, want requested family", support.EndpointFamily)
	}
	if support.Enabled {
		t.Fatal("unknown endpoint family must be disabled")
	}
}

func TestDecideResponsesContinuationRequiresAutoEnabledAndAnchorAge(t *testing.T) {
	enabled := enabledResponsesContinuationSupport()

	tests := []struct {
		name    string
		mode    ResponsesContinuationMode
		support ResponsesContinuationSupport
		want    ResponsesContinuationDecision
	}{
		{
			name:    "off ignores enabled support",
			mode:    ResponsesContinuationOff,
			support: enabled,
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_off",
			},
		},
		{
			name: "auto with default disabled codex support uses full history",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupportFor(
				DefaultResponsesContinuationSupportRegistry(),
				ResponsesEndpointFamilyOpenAICodex,
			),
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_endpoint_not_enabled",
			},
		},
		{
			name: "auto with default enabled public support allows responses delta",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupportFor(
				DefaultResponsesContinuationSupportRegistry(),
				ResponsesEndpointFamilyOpenAIPublic,
			),
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeResponsesDelta,
				Reason:      "continuation_enabled",
			},
		},
		{
			name: "auto with enabled support but no anchor age uses full history",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupport{
				EndpointFamily:       ResponsesEndpointFamilyOpenAIPublic,
				StorageShapeProven:   true,
				ProductionPathProven: true,
				Enabled:              true,
			},
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_anchor_age_unbounded",
			},
		},
		{
			name:    "auto with proven enabled support allows responses delta",
			mode:    ResponsesContinuationAuto,
			support: enabled,
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeResponsesDelta,
				Reason:      "continuation_enabled",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideResponsesContinuation(tc.mode, tc.support)
			if got != tc.want {
				t.Fatalf("decision = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestDecideResponsesContinuationForRequestDisablesExplicitConversationID(t *testing.T) {
	enabled := enabledResponsesContinuationSupport()

	got := DecideResponsesContinuationForRequest(ResponsesContinuationAuto, enabled, Request{
		Model:          "gpt-5.2",
		ConversationID: " conv_public ",
	})

	want := ResponsesContinuationDecision{
		HistoryMode: HistoryModeFullHistory,
		Reason:      "continuation_conversation_id_present",
	}
	if got != want {
		t.Fatalf("decision = %+v, want %+v", got, want)
	}
}

func TestDecideResponsesContinuationForRequestAllowsNoConversationID(t *testing.T) {
	enabled := enabledResponsesContinuationSupport()

	got := DecideResponsesContinuationForRequest(ResponsesContinuationAuto, enabled, Request{
		Model: "gpt-5.2",
	})

	want := ResponsesContinuationDecision{
		HistoryMode: HistoryModeResponsesDelta,
		Reason:      "continuation_enabled",
	}
	if got != want {
		t.Fatalf("decision = %+v, want %+v", got, want)
	}
}

func TestResponsesContinuationPlanInputDoesNotExposeRawScopeFields(t *testing.T) {
	inputType := reflect.TypeOf(ResponsesContinuationPlanInput{})
	for i := 0; i < inputType.NumField(); i++ {
		name := inputType.Field(i).Name
		for _, sensitive := range []string{"APIKey", "Bearer", "Token", "Raw"} {
			if strings.Contains(name, sensitive) {
				t.Fatalf("planner input field %s exposes raw/sensitive scope data", name)
			}
		}
		for _, identifier := range []string{"OrgID", "ProjectID"} {
			if strings.Contains(name, identifier) && !strings.HasSuffix(name, "Hash") {
				t.Fatalf("planner input field %s exposes raw %s instead of a hash", name, identifier)
			}
		}
	}
}

func TestPlanResponsesContinuationCopiesSanitizedScopeOnly(t *testing.T) {
	input := ResponsesContinuationPlanInput{
		EndpointFamily: ResponsesEndpointFamilyOpenAIPublic,
		AuthScopeIdentity: AuthScopeIdentity{
			Version:        "cont-scope-v1",
			AuthSource:     "api_key",
			CredentialHash: "cont-scope-v1:credential:abc",
		},
		OrgIDHash:     " cont-scope-v1:org_id:def ",
		ProjectIDHash: " cont-scope-v1:project_id:ghi ",
		Request: Request{
			Provider: "openai",
			Model:    "gpt-5.4",
		},
	}

	plan := PlanResponsesContinuation(input)
	if plan.EndpointFamily != ResponsesEndpointFamilyOpenAIPublic {
		t.Fatalf("EndpointFamily = %q, want %q", plan.EndpointFamily, ResponsesEndpointFamilyOpenAIPublic)
	}
	if plan.AuthScopeIdentity != input.AuthScopeIdentity {
		t.Fatalf("AuthScopeIdentity = %+v, want %+v", plan.AuthScopeIdentity, input.AuthScopeIdentity)
	}
	if plan.OrgIDHash != "cont-scope-v1:org_id:def" {
		t.Fatalf("OrgIDHash = %q", plan.OrgIDHash)
	}
	if plan.ProjectIDHash != "cont-scope-v1:project_id:ghi" {
		t.Fatalf("ProjectIDHash = %q", plan.ProjectIDHash)
	}
	if plan.RequestFingerprint != "" || plan.StorageScopeFingerprint != "" || plan.StoragePolicyLabel != "" || plan.ContinuationStorageAllowed {
		t.Fatalf("later-phase planner fields must remain zero, got %+v", plan)
	}
}

func TestContinuationStorageScopeDoesNotExposeRawScopeFields(t *testing.T) {
	typ := reflect.TypeOf(ContinuationStorageScope{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		for _, sensitive := range []string{"APIKey", "Bearer", "Token", "Raw"} {
			if strings.Contains(name, sensitive) {
				t.Fatalf("storage scope field %s exposes raw/sensitive scope data", name)
			}
		}
		for _, identifier := range []string{"OrgID", "ProjectID", "Account", "Workspace", "Credential", "ConversationID"} {
			if strings.Contains(name, identifier) && !strings.HasSuffix(name, "Hash") {
				t.Fatalf("storage scope field %s exposes raw %s instead of a hash", name, identifier)
			}
		}
	}
}

func TestContinuationStoreOverrideSetsAndClearsNilStore(t *testing.T) {
	base := Request{Model: "gpt-5.4"}

	owned, override := ApplyResponsesContinuationStoreOverride(base, ResponsesStoragePolicyPublicOpenAIStore)
	if base.Store != nil {
		t.Fatalf("base request mutated: Store = %v", *base.Store)
	}
	if owned.Store == nil || !*owned.Store {
		t.Fatalf("owned Store = %v, want true", owned.Store)
	}
	if !override.StoreSetByContinuation || override.OriginalStore != nil {
		t.Fatalf("override = %+v, want continuation-owned nil original", override)
	}
	if override.StoragePolicy != ResponsesStoragePolicyPublicOpenAIStore {
		t.Fatalf("StoragePolicy = %q", override.StoragePolicy)
	}

	cleared := ClearResponsesContinuationStoreOverride(owned, override)
	if cleared.Store != nil {
		t.Fatalf("cleared Store = %v, want nil", *cleared.Store)
	}
}

func TestContinuationStoreOverrideRestoresExplicitFalse(t *testing.T) {
	explicitFalse := false
	base := Request{Model: "gpt-5.4", Store: &explicitFalse}

	owned, override := ApplyResponsesContinuationStoreOverride(base, ResponsesStoragePolicyPublicOpenAIStore)
	if base.Store == nil || *base.Store {
		t.Fatalf("base Store mutated: %v", base.Store)
	}
	if owned.Store == nil || !*owned.Store {
		t.Fatalf("owned Store = %v, want true", owned.Store)
	}
	if !override.StoreSetByContinuation || override.OriginalStore == nil || *override.OriginalStore {
		t.Fatalf("override = %+v, want original false", override)
	}

	cleared := ClearResponsesContinuationStoreOverride(owned, override)
	if cleared.Store == nil || *cleared.Store {
		t.Fatalf("cleared Store = %v, want explicit false", cleared.Store)
	}
}

func TestContinuationStoreOverridePreservesExplicitTrue(t *testing.T) {
	explicitTrue := true
	base := Request{Model: "gpt-5.4", Store: &explicitTrue}

	owned, override := ApplyResponsesContinuationStoreOverride(base, ResponsesStoragePolicyPublicOpenAIStore)
	if owned.Store == nil || !*owned.Store {
		t.Fatalf("owned Store = %v, want explicit true", owned.Store)
	}
	if override.StoreSetByContinuation {
		t.Fatalf("override = %+v, want no continuation-owned store field", override)
	}

	cleared := ClearResponsesContinuationStoreOverride(owned, override)
	if cleared.Store == nil || !*cleared.Store {
		t.Fatalf("cleared Store = %v, want explicit true", cleared.Store)
	}
}

func TestContinuationStoreOverrideClonesProviderOptions(t *testing.T) {
	base := Request{
		Model: "gpt-5.4",
		ProviderOptions: map[string]any{
			"openai": map[string]any{
				"metadata": map[string]any{"trace": "base"},
			},
		},
	}

	owned, _ := ApplyResponsesContinuationStoreOverride(base, ResponsesStoragePolicyPublicOpenAIStore)
	ownedOpenAI := owned.ProviderOptions["openai"].(map[string]any)
	ownedMetadata := ownedOpenAI["metadata"].(map[string]any)
	ownedMetadata["trace"] = "owned"

	baseOpenAI := base.ProviderOptions["openai"].(map[string]any)
	baseMetadata := baseOpenAI["metadata"].(map[string]any)
	if baseMetadata["trace"] != "base" {
		t.Fatalf("base provider options mutated: %+v", base.ProviderOptions)
	}
}

func TestContinuationStoreOverrideIgnoresNonStoragePolicy(t *testing.T) {
	base := Request{Model: "gpt-5.4"}

	owned, override := ApplyResponsesContinuationStoreOverride(base, ResponsesStoragePolicyPublicOpenAINoStore)
	if owned.Store != nil {
		t.Fatalf("owned Store = %v, want nil for no-store policy", *owned.Store)
	}
	if override.StoreSetByContinuation {
		t.Fatalf("override = %+v, want no continuation-owned store field", override)
	}
	if override.StoragePolicy != ResponsesStoragePolicyPublicOpenAINoStore {
		t.Fatalf("StoragePolicy = %q", override.StoragePolicy)
	}
}

func enabledResponsesContinuationSupport() ResponsesContinuationSupport {
	return ResponsesContinuationSupport{
		EndpointFamily:        ResponsesEndpointFamilyOpenAIPublic,
		StorageShapeProven:    true,
		ProductionPathProven:  true,
		Enabled:               true,
		MaxAnchorAgeSeconds:   3600,
		StorageShapeProofID:   "phase-0b-public",
		ProductionPathProofID: "phase-12a-public",
	}
}
