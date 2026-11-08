package hub

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestRosterPendingSetPairChangesDeliverOnce(t *testing.T) {
	t.Parallel()
	b := &attentionBroker{}
	ident := func(string) (rosterIdentity, bool) {
		return rosterIdentity{Ref: "local:worker", InstanceID: "instance-token"}, true
	}
	entry := appwire.AttentionEntry{ID: "worker", Level: "needs_you", AskPending: true, AskBootGeneration: "boot-one", AskRevision: 1, PendingQuestion: &appwire.PendingQuestion{Question: "q-token", Count: 1}}
	b.Publish(map[string]appwire.AttentionEntry{"worker": entry}, ident)
	sub, err := b.Subscribe(appwire.ManagerView{Ref: "local:manager"})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	<-sub.Updates
	for _, change := range []struct {
		boot     string
		revision uint64
		count    int
	}{{"boot-one", 2, 2}, {"boot-two", 2, 2}} {
		entry.AskBootGeneration, entry.AskRevision = change.boot, change.revision
		entry.PendingQuestion = &appwire.PendingQuestion{Question: "q-token", Count: change.count}
		b.Publish(map[string]appwire.AttentionEntry{"worker": entry}, ident)
		select {
		case got := <-sub.Updates:
			if len(got.Events) != 1 || got.Events[0].Kind != "needs_you" || got.Events[0].AskRevision != change.revision || got.Events[0].AskBootGeneration != change.boot || got.Events[0].PendingQuestion.Count != change.count {
				t.Fatalf("pair transition=%+v", got)
			}
		default:
			t.Fatalf("pair change (%s,%d) did not deliver", change.boot, change.revision)
		}
		entry.PublishedAt++
		b.Publish(map[string]appwire.AttentionEntry{"worker": entry}, ident)
		select {
		case extra := <-sub.Updates:
			t.Fatalf("stable set repeated: %+v", extra)
		default:
		}
	}
}
