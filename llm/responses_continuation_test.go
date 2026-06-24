package llm

import "testing"

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
