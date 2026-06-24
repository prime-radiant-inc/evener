package llm

import (
	"reflect"
	"strings"
	"testing"
)

func TestDefaultResponsesContinuationSupportRegistryDisabled(t *testing.T) {
	registry := DefaultResponsesContinuationSupportRegistry()

	for _, family := range []ResponsesEndpointFamily{
		ResponsesEndpointFamilyOpenAIPublic,
		ResponsesEndpointFamilyOpenAICodex,
	} {
		support, ok := registry[family]
		if !ok {
			t.Fatalf("registry missing endpoint family %q", family)
		}
		if support.EndpointFamily != family {
			t.Fatalf("support.EndpointFamily = %q, want %q", support.EndpointFamily, family)
		}
		if support.Enabled {
			t.Fatalf("%s Enabled = true, want false", family)
		}
		if support.StorageShapeProven {
			t.Fatalf("%s StorageShapeProven = true, want false", family)
		}
		if support.ProductionPathProven {
			t.Fatalf("%s ProductionPathProven = true, want false", family)
		}
		if support.MaxAnchorAgeSeconds != 0 {
			t.Fatalf("%s MaxAnchorAgeSeconds = %d, want 0", family, support.MaxAnchorAgeSeconds)
		}
		if support.StorageShapeProofID != "" {
			t.Fatalf("%s StorageShapeProofID = %q, want empty", family, support.StorageShapeProofID)
		}
		if support.ProductionPathProofID != "" {
			t.Fatalf("%s ProductionPathProofID = %q, want empty", family, support.ProductionPathProofID)
		}
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
			name: "auto with default disabled public support uses full history",
			mode: ResponsesContinuationAuto,
			support: ResponsesContinuationSupportFor(
				DefaultResponsesContinuationSupportRegistry(),
				ResponsesEndpointFamilyOpenAIPublic,
			),
			want: ResponsesContinuationDecision{
				HistoryMode: HistoryModeFullHistory,
				Reason:      "continuation_endpoint_not_enabled",
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
