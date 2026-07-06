package main

import (
	"testing"

	"primeradiant.com/serf/cmd/serf-tui/internal/transcript"
)

// TestHubModelSessionComposerAwaitingQuestionKeyedOnPendingSet is a
// regression test for the "question waiting" chip outliving the question it
// announces. Under attention-status-model v5, a session re-arms
// State=="awaiting" after ANY clean output-producing turn — including the
// reply that resolves an ask_user question — so AwaitingQuestion must key on
// pendingAskQuestions (the same transcript scan question_overlay.go's
// toggleAskOverlay uses), not on the raw wire state. m.detail.State is pinned
// to "awaiting" in every case below so the assertions actually discriminate
// pending-set-keying from state-keying; if AwaitingQuestion keyed on state
// alone, all three cases would report true.
func TestHubModelSessionComposerAwaitingQuestionKeyedOnPendingSet(t *testing.T) {
	for _, tt := range []struct {
		name     string
		messages []transcript.ChatMessage
		want     bool
	}{
		{
			name:     "unresolved ask_user is pending",
			messages: []transcript.ChatMessage{askUserToolMsg("call_1", oneQuestionArgsJSON, true, "")},
			want:     true,
		},
		{
			name: "ask_user answered by a following user reply is not pending",
			messages: []transcript.ChatMessage{
				askUserToolMsg("call_1", oneQuestionArgsJSON, true, ""),
				{Kind: transcript.MsgUser, Text: "[answers]\n1. [DB choice] -> \"Postgres\""},
			},
			want: false,
		},
		{
			name:     "a generic awaiting rest with no ask_user at all is not pending",
			messages: []transcript.ChatMessage{{Kind: transcript.MsgAssistant, Text: "Ready for the next task."}},
			want:     false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := newSessionHubModel(nil)
			m.detail.State = "awaiting"
			m.session.messages = tt.messages

			if got := m.sessionComposerPanel().AwaitingQuestion; got != tt.want {
				t.Fatalf("AwaitingQuestion = %v, want %v (messages=%#v)", got, tt.want, tt.messages)
			}
		})
	}
}

func awaitingSendModel() hubModel {
	m := hubModel{}
	m.detail.State = "awaiting"
	m.detail.Capabilities.Queue = true
	m.detail.Capabilities.Send = true
	m.session.processing = false
	return m
}

func TestSessionComposerMode_RestedAwaitingShowsSend(t *testing.T) {
	m := awaitingSendModel()
	if got := m.sessionComposerMode(); got != hubComposerModeSend {
		t.Fatalf("rested awaiting composer mode = %v, want hubComposerModeSend (plain Send, no queue)", got)
	}
}

func TestSessionComposerMode_ActiveStillQueues(t *testing.T) {
	m := awaitingSendModel()
	m.detail.State = "active"
	m.session.processing = true
	if got := m.sessionComposerMode(); got != hubComposerModeQueue {
		t.Fatalf("active composer mode = %v, want hubComposerModeQueue (unchanged)", got)
	}
}
