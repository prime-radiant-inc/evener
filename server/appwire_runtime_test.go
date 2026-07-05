package server

import (
	"testing"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/appserver"
)

func TestAppCapabilities_SteerGatedOnActiveTurn(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		state      string
		processing bool
		reserved   bool
		stale      bool
		setSteer   bool
		wantSteer  bool
	}{
		{"processing with steerFunc", "active", true, false, false, true, true},
		{"reserved idle with steerFunc", "idle", false, true, false, true, true},
		{"stale projected active turn with steerFunc", "idle", false, false, true, true, false},
		{"idle with steerFunc", "idle", false, false, false, true, false},
		{"awaiting with steerFunc", "awaiting", false, false, false, true, false},
		{"closed with steerFunc", "closed", false, false, false, true, false},
		{"processing without steerFunc", "active", true, false, false, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			if tc.setSteer {
				s.SetSteerFunc(func(string) {})
			}
			if tc.reserved {
				s.appActiveTurnID = "turn_reserved"
				s.appReservedTurnID = "turn_reserved"
			}
			if tc.stale {
				s.appActiveTurnID = "turn_stale"
			}
			got := s.appCapabilities(tc.state, tc.processing)
			if got.Steer != tc.wantSteer {
				t.Fatalf("Steer = %v, want %v", got.Steer, tc.wantSteer)
			}
		})
	}
}

func TestAppDiagnosticsFromDetailedStatus_MCPStatusError(t *testing.T) {
	ds := DetailedStatus{
		MCP: []MCPServerInfo{{Name: "test-server", Tools: []string{"tool1"}, Status: "degraded", Error: "boom"}},
	}
	got := appDiagnosticsFromDetailedStatus(ds)
	if len(got.MCP) != 1 {
		t.Fatalf("MCP = %v, want 1", got.MCP)
	}
	m := got.MCP[0]
	if m.Name != "test-server" || len(m.Tools) != 1 || m.Tools[0] != "tool1" {
		t.Errorf("MCP[0] = %+v, want Name:test-server Tools:[tool1]", m)
	}
	if m.Status != "degraded" {
		t.Errorf("MCP[0].Status = %q, want degraded", m.Status)
	}
	if m.Error != "boom" {
		t.Errorf("MCP[0].Error = %q, want boom", m.Error)
	}
}

func TestAppTurnsFromNotificationsAccumulatesReasoningDeltas(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turnId":"turn_1"}`)}},
		{Notification: appwire.Notification{Method: "item/started", Params: []byte(`{"turnId":"turn_1","item":{"type":"reasoning","id":"item_reasoning_1","turnId":"turn_1","status":"inProgress"}}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":"Let me think"}`)}},
		{Notification: appwire.Notification{Method: "item/reasoning/summaryTextDelta", Params: []byte(`{"turnId":"turn_1","itemId":"item_reasoning_1","delta":" about this."}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turnId":"turn_1","turn":{"status":"completed"}}`)}},
	}
	turns := appTurnsFromNotifications(records)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	items := turns[0].Items
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d: %+v", len(items), items)
	}
	if items[0].Type != "reasoning" || items[0].Text != "Let me think about this." {
		t.Fatalf("reasoning item=%+v", items[0])
	}
}

// TestAppTurnsFromNotificationsCarriesTurnTiming verifies that replaying a
// turn/started carrying Turn.StartedAt and a turn/completed carrying
// Turn.CompletedAt/Turn.DurationMS reconstructs a Turn with those three
// timing fields set — today appTurnsFromNotifications only copies
// ItemsView/Status off the wire Turn and silently drops the timing fields.
func TestAppTurnsFromNotificationsCarriesTurnTiming(t *testing.T) {
	records := []appserver.SequencedNotification{
		{Notification: appwire.Notification{Method: "turn/started", Params: []byte(`{"turnId":"turn_1","turn":{"id":"turn_1","status":"inProgress","startedAt":1700000000}}`)}},
		{Notification: appwire.Notification{Method: "turn/completed", Params: []byte(`{"turnId":"turn_1","turn":{"id":"turn_1","status":"completed","completedAt":1700000042,"durationMs":4200}}`)}},
	}
	turns := appTurnsFromNotifications(records)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if turn.StartedAt == nil || *turn.StartedAt != 1700000000 {
		t.Fatalf("turn StartedAt=%v, want 1700000000", turn.StartedAt)
	}
	if turn.CompletedAt == nil || *turn.CompletedAt != 1700000042 {
		t.Fatalf("turn CompletedAt=%v, want 1700000042", turn.CompletedAt)
	}
	if turn.DurationMS == nil || *turn.DurationMS != 4200 {
		t.Fatalf("turn DurationMS=%v, want 4200", turn.DurationMS)
	}
}
