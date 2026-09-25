package appprojector

import (
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
)

// A persisted NOTICE entry displays exactly as the live announcement for the
// same event: history and live must not disagree about a notice's kind,
// description, text or detail.
func TestNewFormatNoticeMatchesTheLiveAnnouncement(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  events.SessionEvent
		notice schema.NoticeInfo
	}{
		{
			name:   "tool repair",
			event:  events.SessionEvent{Kind: events.EventToolCallRepaired, Data: events.ToolCallRepairedData{ToolName: "communicate", CallID: "c1", Changes: []string{"alias:message:msg→message", "drop_unknown:artifacts:dropped artifacts"}}},
			notice: schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: "communicate", CallID: "c1", Changes: []string{"alias:message:msg→message", "drop_unknown:artifacts:dropped artifacts"}}},
		},
		{
			name:   "tool repair without changes",
			event:  events.SessionEvent{Kind: events.EventToolCallRepaired, Data: events.ToolCallRepairedData{}},
			notice: schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{}},
		},
		{
			name:   "goal blocked",
			event:  events.SessionEvent{Kind: events.EventGoalEnded, Data: events.GoalEndedData{Status: "blocked", Reason: "needs a key", Iterations: 3}},
			notice: schema.NoticeInfo{Kind: schema.NoticeGoalEnded, GoalEnded: &schema.GoalEndedNotice{Status: "blocked", Reason: "needs a key", Iterations: 3}},
		},
		{
			name:   "turn limit",
			event:  events.SessionEvent{Kind: events.EventTurnLimit, Data: events.TurnLimitData{MaxTurns: 3, MaxToolRoundsPerInput: 7}},
			notice: schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxTurns: 3, MaxToolRoundsPerInput: 7}},
		},
		{
			name:   "skill activated",
			event:  events.SessionEvent{Kind: events.EventSkillActivated, Data: events.SkillActivatedData{Name: "brainstorming"}},
			notice: schema.NoticeInfo{Kind: schema.NoticeSkillActivated, SkillActivated: &schema.SkillActivatedNotice{Name: "brainstorming"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.event.SessionID = "th_1"
			turn := notificationTurn(t, NewAppEventProjector("th_1", "local:th_1").Project(tc.event), appwire.NotifyTurnCompleted)
			if len(turn.Items) != 1 {
				t.Fatalf("live turn = %+v, want one item", turn)
			}
			live := turn.Items[0]

			notice := tc.notice
			persisted, ok := apptranscript.NoticeItem("t_1", 0, schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnNotice, Notice: &notice})
			if !ok {
				t.Fatal("NoticeItem projected nothing")
			}
			if persisted.Type != live.Type || persisted.EventKind != live.EventKind || persisted.Description != live.Description ||
				persisted.Text != live.Text || string(persisted.Raw) != string(live.Raw) || persisted.Status != live.Status {
				t.Fatalf("persisted notice differs from live:\n got %+v\nwant %+v", persisted, live)
			}
		})
	}
}
