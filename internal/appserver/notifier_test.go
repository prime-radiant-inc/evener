package appserver

import (
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestNotifierAssignsSequence(t *testing.T) {
	notifier := NewNotifier(10)
	first := notifier.Record("th_1", appwire.NotifyThreadStatusChanged, map[string]string{"threadId": "th_1"})
	second := notifier.Record("th_1", appwire.NotifyAgentMessageDelta, appwire.AgentMessageDeltaParams{ThreadID: "th_1", Delta: "hi"})

	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("seqs=%d,%d", first.Seq, second.Seq)
	}
	if second.Notification.Method != appwire.NotifyAgentMessageDelta {
		t.Fatalf("method=%q", second.Notification.Method)
	}
}

func TestNotifierReplaysAfterCursor(t *testing.T) {
	notifier := NewNotifier(10)
	notifier.Record("th_1", appwire.NotifyThreadStatusChanged, nil)
	second := notifier.Record("th_1", appwire.NotifyAgentMessageDelta, nil)
	third := notifier.Record("th_2", appwire.NotifyThreadStatusChanged, nil)

	replayed := notifier.ReplayAfter(1, "th_1")
	if len(replayed) != 1 || replayed[0].Seq != second.Seq {
		t.Fatalf("replayed=%+v", replayed)
	}
	all := notifier.ReplayAfter(1, "")
	if len(all) != 2 || all[0].Seq != second.Seq || all[1].Seq != third.Seq {
		t.Fatalf("all=%+v", all)
	}
}
