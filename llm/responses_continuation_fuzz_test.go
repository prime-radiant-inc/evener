package llm

import (
	"strings"
	"testing"
)

// FuzzResponsesContinuationDecision drives the pure continuation decision and
// store-override logic — DecideResponsesContinuation,
// DecideResponsesContinuationForRequest, ResponsesContinuationSupportFor, and the
// ApplyResponsesContinuationStoreOverride/ClearResponsesContinuationStoreOverride
// round-trip — over an arbitrary mode, support entry, request, and policy. These
// gate whether serf uses server-side responses continuation; only fixed unit
// cases reached them (0% fuzz).
//
// Oracles:
//   - DecideResponsesContinuation returns ResponsesDelta IFF all three documented
//     conditions hold (mode auto, support enabled, positive max anchor age);
//     otherwise full history with a non-empty reason.
//   - DecideResponsesContinuationForRequest never strengthens the base decision:
//     it only downgrades a ResponsesDelta verdict (when a ConversationID is set).
//   - Apply-then-Clear restores the request's Store flag to its original value
//     (presence and value), the central revert invariant.
//   - ResponsesContinuationSupportFor on an absent family yields a disabled entry.
func FuzzResponsesContinuationDecision(f *testing.F) {
	f.Add(true, true, int64(3600), "", "public-openai-store", true, false)
	f.Add(false, true, int64(3600), "conv-1", "public-openai-store", false, true)
	f.Add(true, false, int64(0), "", "other", true, true)

	f.Fuzz(func(t *testing.T, auto, enabled bool, maxAge int64, convID, policy string, storeSet, storeVal bool) {
		mode := ResponsesContinuationOff
		if auto {
			mode = ResponsesContinuationAuto
		}
		support := ResponsesContinuationSupport{
			EndpointFamily:      ResponsesEndpointFamilyOpenAIPublic,
			Enabled:             enabled,
			MaxAnchorAgeSeconds: maxAge,
		}

		decision := DecideResponsesContinuation(mode, support)
		wantDelta := auto && enabled && maxAge > 0
		if (decision.HistoryMode == HistoryModeResponsesDelta) != wantDelta {
			t.Fatalf("DecideResponsesContinuation=%q, wantDelta=%v (auto=%v enabled=%v maxAge=%d)",
				decision.HistoryMode, wantDelta, auto, enabled, maxAge)
		}
		if !wantDelta {
			if decision.HistoryMode != HistoryModeFullHistory || decision.Reason == "" {
				t.Fatalf("non-delta decision malformed: %+v", decision)
			}
		}

		// Per-request decision never upgrades a non-delta base decision.
		req := Request{ConversationID: convID}
		perReq := DecideResponsesContinuationForRequest(mode, support, req)
		if decision.HistoryMode != HistoryModeResponsesDelta && perReq != decision {
			t.Fatalf("per-request changed a non-delta decision: %+v -> %+v", decision, perReq)
		}
		// The product trims the ConversationID before deciding, so only a
		// non-whitespace value forces the downgrade.
		if decision.HistoryMode == HistoryModeResponsesDelta && strings.TrimSpace(convID) != "" &&
			perReq.HistoryMode == HistoryModeResponsesDelta {
			t.Fatalf("ConversationID present but per-request still delta")
		}

		// Store-override apply/clear round-trip restores the original Store flag.
		base := Request{}
		if storeSet {
			s := storeVal
			base.Store = &s
		}
		applied, override := ApplyResponsesContinuationStoreOverride(base, policy)
		cleared := ClearResponsesContinuationStoreOverride(applied, override)
		if (cleared.Store == nil) != (base.Store == nil) {
			t.Fatalf("Store presence not restored: base=%v cleared=%v", base.Store, cleared.Store)
		}
		if base.Store != nil && *cleared.Store != *base.Store {
			t.Fatalf("Store value not restored: base=%v cleared=%v", *base.Store, *cleared.Store)
		}

		// Absent-family support is disabled.
		empty := ResponsesContinuationSupportFor(map[ResponsesEndpointFamily]ResponsesContinuationSupport{}, ResponsesEndpointFamilyOpenAIPublic)
		if empty.Enabled {
			t.Fatalf("absent family returned an enabled support entry")
		}
	})
}
