package hub

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

func TestScopeWatchValidation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		request appwire.SessionWatchRequest
		valid   bool
	}{
		{appwire.SessionWatchRequest{Scope: "top_level"}, true},
		{appwire.SessionWatchRequest{Scope: "top_level", Ref: "local:target"}, false},
		{appwire.SessionWatchRequest{Scope: "top_level", Completion: true}, false},
		{appwire.SessionWatchRequest{Scope: "top_level", Events: []string{"status"}}, false},
		{appwire.SessionWatchRequest{Scope: "project"}, false},
		{appwire.SessionWatchRequest{Ref: "local:target", Events: []string{"status"}}, true},
	} {
		if got := validSessionWatch(tc.request) == nil; got != tc.valid {
			t.Errorf("validSessionWatch(%+v) = %t, want %t", tc.request, got, tc.valid)
		}
	}
}

func TestRosterGlobalInitialAndDeltas(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	entries := make(map[string]appwire.AttentionEntry)
	for i := range 300 {
		id := fmt.Sprintf("worker-%03d", i)
		entries[id] = appwire.AttentionEntry{ID: id, Level: "working"}
	}
	entries["self"] = appwire.AttentionEntry{ID: "self", Level: "needs_you"}
	identity := func(id string) (rosterIdentity, bool) {
		if id == "self" {
			return rosterIdentity{Ref: "local:stable"}, true
		}
		return rosterIdentity{Ref: "remote:" + id}, true
	}
	b.Publish(entries, identity)
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:stable", Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	initial := <-sub.Updates
	if !initial.Initial || len(initial.Events) != 300 {
		t.Fatalf("full initial population = %+v", initial)
	}
	for _, event := range initial.Events {
		if event.Ref != "remote:"+event.ID || event.Kind != "idle" {
			t.Fatalf("initial event = %+v", event)
		}
	}
	entries["worker-000"] = appwire.AttentionEntry{ID: "worker-000", Level: "needs_you", NeedsResponse: true}
	b.Publish(entries, identity)
	delta := <-sub.Updates
	if len(delta.Events) != 1 || delta.Events[0].Kind != "needs_you" || !delta.Events[0].NeedsResponse {
		t.Fatalf("delta = %+v", delta)
	}
	entries["worker-000"] = appwire.AttentionEntry{ID: "worker-000", Level: "needs_you", AskPending: true}
	b.Publish(entries, identity)
	delta = <-sub.Updates
	if len(delta.Events) != 1 || delta.Events[0].Kind != "needs_you" || !delta.Events[0].AskPending {
		t.Fatalf("same-level flag delta = %+v", delta)
	}
}

func TestRosterSelfAfterClear(t *testing.T) {
	t.Parallel()
	snapshot := navigationSnapshot{live: []hubcore.LiveEntry{{Entry: rendezvous.Entry{WorkspaceRef: "local:stable"}, SessionID: "changed-session"}}}
	b := &attentionBroker{}
	b.Publish(map[string]appwire.AttentionEntry{"changed-session": {ID: "changed-session", Level: "needs_you"}}, rosterIdentities(snapshot))
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:stable", InstanceID: "changed-session", Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if update := <-sub.Updates; len(update.Events) != 0 {
		t.Fatalf("own cleared session reached roster: %+v", update.Events)
	}
}

func TestRosterIdentitiesUseWorkspaceRef(t *testing.T) {
	t.Parallel()
	snapshot := navigationSnapshot{live: []hubcore.LiveEntry{
		{Entry: rendezvous.Entry{WorkspaceRef: "local:saved", SessionID: "cleared", ThreadID: "saved"}, SessionID: "cleared"},
		{Entry: rendezvous.Entry{SourceID: "remote", ThreadID: "thread"}, SessionID: "remote:thread"},
	}}
	identity := rosterIdentities(snapshot)
	if got, ok := identity("cleared"); !ok || got.Ref != "local:saved" {
		t.Fatalf("cleared workspace = %+v, %t", got, ok)
	}
	if got, ok := identity("remote:thread"); !ok || got.Ref != "remote:thread" {
		t.Fatalf("remote workspace = %+v, %t", got, ok)
	}
}

func TestRosterWorkerClear(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	publish := func(sessionID string) {
		snapshot := navigationSnapshot{live: []hubcore.LiveEntry{{
			Entry: rendezvous.Entry{WorkspaceRef: "local:worker", SessionID: sessionID}, SessionID: sessionID,
		}}}
		b.Publish(map[string]appwire.AttentionEntry{sessionID: {ID: sessionID, Level: "idle"}}, rosterIdentities(snapshot))
	}
	publish("before-clear")
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:manager", Manager: true})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	initial := <-sub.Updates
	if !initial.Initial || len(initial.Events) != 1 || initial.Events[0].ID != "before-clear" {
		t.Fatalf("before Clear snapshot=%+v", initial)
	}
	// Draining the initial before the synchronous Publish is the barrier:
	// the replacement must be a delta, not an overwritten initial snapshot.
	publish("after-clear")
	select {
	case update := <-sub.Updates:
		if update.Initial || update.Resync || len(update.Events) != 1 || update.Events[0].Ref != "local:worker" || update.Events[0].ID != "after-clear" || update.Events[0].Kind != "new" {
			t.Fatalf("worker Clear delta=%+v, want one live replacement", update)
		}
	default:
		t.Fatal("worker Clear replacement was not published")
	}
}

func TestRosterDisappearanceConfirmation(t *testing.T) {
	t.Parallel()
	for _, returns := range []bool{true, false} {
		t.Run(fmt.Sprintf("replacement=%t", returns), func(t *testing.T) {
			b := &attentionBroker{}
			identity := func(string) (rosterIdentity, bool) { return rosterIdentity{Ref: "local:worker"}, true }
			b.Publish(map[string]appwire.AttentionEntry{"before-clear": {ID: "before-clear", Level: "idle"}}, identity)
			sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:manager", Manager: true})
			if err != nil {
				t.Fatal(err)
			}
			defer sub.Cancel()
			<-sub.Updates
			// Each synchronous Publish is a completed tick, so the assertions
			// cannot race the broker or depend on a guessed quiet interval.
			b.Publish(nil, identity)
			select {
			case update := <-sub.Updates:
				t.Fatalf("first missing tick delivered %+v", update)
			default:
			}
			if len(sub.baseline) != 1 || sub.baseline["before-clear"].Ref != "local:worker" {
				t.Fatalf("first missing tick lost baseline: %+v", sub.baseline)
			}
			var next map[string]appwire.AttentionEntry
			wantKind := "disappeared"
			if returns {
				next = map[string]appwire.AttentionEntry{"after-clear": {ID: "after-clear", Level: "idle"}}
				wantKind = "new"
			}
			b.Publish(next, identity)
			select {
			case update := <-sub.Updates:
				if update.Revision != 3 || len(update.Events) != 1 || update.Events[0].Ref != "local:worker" || update.Events[0].Kind != wantKind {
					t.Fatalf("confirmation tick=%+v, want one %s", update, wantKind)
				}
			default:
				t.Fatal("confirmation tick was not delivered")
			}
			if returns && (len(sub.baseline) != 1 || sub.baseline["after-clear"].Ref != "local:worker") || !returns && len(sub.baseline) != 0 {
				t.Fatalf("confirmation baseline=%+v", sub.baseline)
			}
			b.Publish(next, identity)
			select {
			case update := <-sub.Updates:
				t.Fatalf("settled confirmation repeated %+v", update)
			default:
			}
		})
	}
}

func TestRosterWatchContextRemovesSubscriber(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	ctx, cancel := context.WithCancel(t.Context())
	updates, err := watchRoster(ctx, b, appwire.ManagerView{Ref: "local:caller", Manager: true})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	b.mu.Lock()
	registered := len(b.subscribers)
	b.mu.Unlock()
	if registered != 1 {
		cancel()
		t.Fatalf("registered subscriptions = %d", registered)
	}
	cancel()
	deadline, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		select {
		case _, open := <-updates:
			if !open {
				b.mu.Lock()
				remaining := len(b.subscribers)
				b.mu.Unlock()
				if remaining != 0 {
					t.Fatalf("subscriptions after cancel = %d", remaining)
				}
				return
			}
		case <-deadline.Done():
			t.Fatal("roster watch did not close after cancel")
		}
	}
}

func TestRosterQuietChurnAndDeltaCap(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	identity := func(id string) (rosterIdentity, bool) { return rosterIdentity{Ref: "remote:" + id}, true }
	b.Publish(map[string]appwire.AttentionEntry{"one": {ID: "one", Level: "working"}}, identity)
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:caller"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	<-sub.Updates
	b.Publish(map[string]appwire.AttentionEntry{"one": {ID: "one", Level: "idle"}}, identity)
	select {
	case update := <-sub.Updates:
		t.Fatalf("working/idle churn emitted %+v", update)
	default:
	}
	entries := map[string]appwire.AttentionEntry{"one": {ID: "one", Level: "idle"}}
	for i := range 300 {
		id := fmt.Sprintf("worker-%03d", i)
		entries[id] = appwire.AttentionEntry{ID: id, Level: "working"}
	}
	b.Publish(entries, identity)
	update := <-sub.Updates
	if !update.Resync || len(update.Events) != 0 {
		t.Fatalf("over-limit delta = %+v", update)
	}
	b.Publish(entries, identity)
	select {
	case extra := <-sub.Updates:
		t.Fatalf("resync baseline repeated: %+v", extra)
	default:
	}
}

func TestRosterNewErrorAndDisappeared(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	identity := func(string) (rosterIdentity, bool) { return rosterIdentity{Ref: "local:worker"}, true }
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:caller"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	<-sub.Updates
	entry := appwire.AttentionEntry{ID: "worker", Level: "working"}
	b.Publish(map[string]appwire.AttentionEntry{"worker": entry}, identity)
	if update := <-sub.Updates; len(update.Events) != 1 || update.Events[0].Kind != "new" {
		t.Fatalf("new delta = %+v", update)
	}
	entry.Level = "error"
	b.Publish(map[string]appwire.AttentionEntry{"worker": entry}, identity)
	if update := <-sub.Updates; len(update.Events) != 1 || update.Events[0].Kind != "error" {
		t.Fatalf("error delta = %+v", update)
	}
	b.Publish(nil, identity)
	select {
	case update := <-sub.Updates:
		t.Fatalf("unconfirmed disappearance = %+v", update)
	default:
	}
	b.Publish(nil, identity)
	if update := <-sub.Updates; len(update.Events) != 1 || update.Events[0].Kind != "disappeared" || update.Events[0].Ref != "local:worker" {
		t.Fatalf("disappeared delta = %+v", update)
	}
}

func TestRosterSlowSubscriberKeepsLatest(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	identity := func(string) (rosterIdentity, bool) { return rosterIdentity{Ref: "remote:worker"}, true }
	b.Publish(map[string]appwire.AttentionEntry{"worker": {ID: "worker", Level: "idle"}}, identity)
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:caller"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	for i := range 4 {
		b.Publish(map[string]appwire.AttentionEntry{"worker": {ID: "worker", Level: "needs_you", Title: fmt.Sprint(i), AskPending: i%2 == 0}}, identity)
	}
	update := <-sub.Updates
	if !update.Initial || len(update.Events) != 1 || update.Events[0].Title != "3" || update.Events[0].AskPending {
		t.Fatalf("slow subscriber's initial snapshot = %+v", update)
	}
	select {
	case extra := <-sub.Updates:
		t.Fatalf("old update remained queued: %+v", extra)
	default:
	}
	b.Publish(map[string]appwire.AttentionEntry{"worker": {ID: "worker", Level: "error"}}, identity)
	update = <-sub.Updates
	if update.Resync || len(update.Events) != 1 || update.Events[0].Kind != "error" {
		t.Fatalf("delta after refreshed baseline = %+v", update)
	}
}

func TestRosterConcurrentDrain(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:caller"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	stop, drained := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(drained)
		for {
			select {
			case <-stop:
				return
			case <-sub.Updates:
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		identity := func(string) (rosterIdentity, bool) { return rosterIdentity{Ref: "local:worker"}, true }
		for i := range 100000 {
			level := "needs_you"
			if i%2 == 0 {
				level = "error"
			}
			b.Publish(map[string]appwire.AttentionEntry{"worker": {ID: "worker", Level: level, Title: fmt.Sprint(i)}}, identity)
		}
	}()
	select {
	case <-done:
		close(stop)
		<-drained
	case <-time.After(5 * time.Second):
		close(stop)
		stack := make([]byte, 65536)
		n := runtime.Stack(stack, true)
		t.Fatalf("Publish blocked while subscriber drained channel: %s", stack[:n])
	}
}
