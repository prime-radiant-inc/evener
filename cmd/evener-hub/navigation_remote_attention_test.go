package hub

import (
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// TestNavigationRemoteRowsCarryTheirQuestionAndApproval pins remote-host
// parity: a remote host's session reaches the controller's navigation with its
// question and its approval, as a local one does. The asking row carries
// ask_pending and sorts into NeedsYou's question band. The row blocked on an
// approval joins NeedsYou and keeps its real state, active: promotion changes
// membership, not state. A working remote row does neither, and the attention
// summary counts the two that need the user.
func TestNavigationRemoteRowsCarryTheirQuestionAndApproval(t *testing.T) {
	cache := &hubcore.RemoteThreadCache{}
	cache.Store([]appwire.Thread{
		{ID: "asking", Source: "devbox", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting}, Evener: appwire.EvenerThread{AskPending: true}},
		{ID: "approving", Source: "devbox", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}, Evener: appwire.EvenerThread{
			PendingEscalations: []appwire.SandboxEscalationRequested{
				{ThreadID: "approving", Ref: "devbox:approving", EscalationID: "esc_1", Tool: "write_file", Kind: "file", DeniedPath: "/home/me/sites/docs/index.md"},
			},
		}},
		{ID: "working", Source: "devbox", Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive}},
	})
	web := NewWebServer(hubcore.WebConfig{RemoteThreadCache: cache})

	captured, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	projection, err := buildNavigationProjection(captured.Inputs)
	if err != nil {
		t.Fatalf("buildNavigationProjection: %v", err)
	}

	needsYou := projection.NeedsYouPage(0, 50).Sessions
	if len(needsYou) != 2 || needsYou[0].Ref != "devbox:asking" || needsYou[1].Ref != "devbox:approving" {
		t.Fatalf("needs-you rows = %#v, want the asking row, then the row blocked on an approval", needsYou)
	}
	if !needsYou[0].AskPending {
		t.Fatalf("asking needs-you row = %#v, want ask_pending", needsYou[0])
	}
	if needsYou[1].State != "active" || needsYou[1].AskPending {
		t.Fatalf("approval needs-you row = %#v, want its real state active and no question", needsYou[1])
	}
	if attention := captured.Inputs.AttentionSummary; attention.NeedsYou != 2 || attention.Working != 1 {
		t.Fatalf("attention summary = %+v, want two needing you and one working", attention)
	}

	if asking := navigationProjectedSummary(t, projection, "devbox:asking"); !asking.AskPending {
		t.Fatalf("asking row = %#v, want ask_pending", asking)
	}
	for _, ref := range []string{"devbox:approving", "devbox:working"} {
		row := navigationProjectedSummary(t, projection, ref)
		if _, carried := navigationSummaryJSONFields(t, row)["ask_pending"]; carried {
			t.Fatalf("row %s carries ask_pending; want the key absent: %#v", ref, row)
		}
	}
}
