package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The session badge reads "question waiting" from detail.AskPending, which was
// snapshot-only: after the user answered, the TUI kept saying it until its next
// read. The flag now rides thread/status/changed (#1613), so the frame that goes
// with the resumed turn clears it — the same way the capability set beside it is
// applied inline rather than waited for.
func TestHubModelStatusFrameClearsTheWaitingQuestion(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:        "local:th_1",
		SessionID:  "sess_1",
		State:      appwire.ThreadStatusAwaiting,
		AskPending: true,
	}
	if lines := strings.Join(m.sessionHeaderLines(), "\n"); !strings.Contains(strings.ToLower(lines), "question waiting") {
		t.Fatalf("precondition: the header does not show the waiting question:\n%s", lines)
	}

	answered := false
	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID:   "th_1",
			Ref:        "local:th_1",
			Status:     appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			AskPending: &answered,
		}).Notification,
	})
	got := updated.(hubModel)
	if got.detail.AskPending {
		t.Fatal("detail.AskPending=true after the frame that says the question was answered")
	}
}

// Absent means "no update", never "no question waiting": an older daemon omits
// the field, and clearing on absence would stop the TUI showing a question the
// read legitimately gave it.
func TestHubModelStatusFrameWithoutAskPendingLeavesItAlone(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:        "local:th_1",
		SessionID:  "sess_1",
		State:      appwire.ThreadStatusAwaiting,
		AskPending: true,
	}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID: "th_1",
			Ref:      "local:th_1",
			Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		}).Notification,
	})
	got := updated.(hubModel)
	if !got.detail.AskPending {
		t.Fatal("detail.AskPending=false after a frame that carried no askPending, want the read's own value kept")
	}
}

// A turn that ended on needs_response rests idle, then its daemon announces
// awaiting on a status frame of its own once the quiet period passes, with no
// turn around it. The TUI takes the new state from that frame alone, and no
// question appears.
func TestHubModelStatusFrameOutsideATurnMovesIdleToAwaiting(t *testing.T) {
	m := newHubModel(nil, "")
	m.mode = hubModeSession
	m.detail = hubSessionDetail{
		Ref:       "local:th_1",
		SessionID: "sess_1",
		State:     appwire.ThreadStatusIdle,
	}

	updated, _ := m.Update(hubNotificationMsg{
		ok: true,
		notification: *appwire.NotificationMessage(appwire.NotifyThreadStatusChanged, appwire.ThreadStatusChangedParams{
			ThreadID: "th_1",
			Ref:      "local:th_1",
			Status:   appwire.ThreadStatus{Type: appwire.ThreadStatusAwaiting},
		}).Notification,
	})
	got := updated.(hubModel)
	if got.detail.State != appwire.ThreadStatusAwaiting {
		t.Fatalf("detail.State=%q after the settled status frame, want awaiting", got.detail.State)
	}
	if got.detail.AskPending {
		t.Fatal("detail.AskPending=true, but no question was asked")
	}
}
