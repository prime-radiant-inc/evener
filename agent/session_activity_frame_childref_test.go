package agent

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appprojector"
)

// The pushed evener/delegate/updated frame names the same child ref the read
// row keys on (#4258 item 5): a client matches the frame to its row by the
// (delegateId, childRef) identity the collection read already preserves, so a
// delegate id that names two rows can never swallow another child's update.
func TestDelegateFrameCarriesReadRowChildRef(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1, AgentsDocPath: filepath.Join(t.TempDir(), "no-personal-AGENTS.md")}))
	at := time.Unix(300, 0).UTC()
	id := "dlg_childref_parity"
	packet := delegatestore.TerminalPacket{Kind: delegatestore.PacketReported, Message: json.RawMessage(`"fixed"`)}
	seedStableReadonlyFinish(t, s, id, stableToolDescriptor(s, id, ""), at, delegateFinish{outcome: delegatestore.OutcomeCompleted, disposition: delegatestore.DispositionReported, endedAt: at.Add(time.Second), packet: &packet}, true)

	page, err := s.ListActivityDelegates(t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", s.ID()), Scope: appwire.SessionActivityScopeSubtree})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Delegates) != 1 {
		t.Fatalf("rows = %d, want 1", len(page.Delegates))
	}
	row := page.Delegates[0]
	if row.ChildRef != encodeRef("", "child-"+id) {
		t.Fatalf("read row childRef = %q, want the descriptor child's ref", row.ChildRef)
	}

	var snapshotRow delegateSnapshot
	for _, candidate := range s.delegateController.Snapshot().rows {
		if candidate.id == id {
			snapshotRow = candidate
		}
	}
	status := delegateFrameStatusInfo(at, s.ID(), snapshotRow)
	data := delegateUpdatedDataFromStatus(status)
	if data.ChildRef != encodeRef("", data.ChildSessionID) {
		t.Fatalf("frame child ref = %q, want encodeRef of child session %q", data.ChildRef, data.ChildSessionID)
	}
	if data.ChildRef != row.ChildRef {
		t.Fatalf("frame child ref = %q, read row childRef = %q", data.ChildRef, row.ChildRef)
	}
	// The roster row shares the read model's mapping, so the transcript's
	// delegate rows carry the same identity the activity frame names.
	if roster := status.AppwireInfo(); roster.ChildRef != row.ChildRef {
		t.Fatalf("roster child ref = %q, read row childRef = %q", roster.ChildRef, row.ChildRef)
	}
	projector := appprojector.NewAppEventProjector(s.ID(), encodeRef("", s.ID()))
	out := projector.Project(events.SessionEvent{Kind: events.EventDelegateUpdated, SessionID: s.ID(), Data: data})
	if len(out) != 1 {
		t.Fatalf("projector notifications = %+v, want 1", out)
	}
	params, ok := out[0].Params.(appwire.EvenerDelegateParams)
	if !ok {
		t.Fatalf("frame params = %T", out[0].Params)
	}
	if params.Delegate.ChildRef != row.ChildRef {
		t.Fatalf("projected frame child ref = %q, read row childRef = %q", params.Delegate.ChildRef, row.ChildRef)
	}
}
