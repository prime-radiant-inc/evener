package task

import (
	"sync"
	"testing"
)

// TestTaskStoreMutationPublicationSerializesSharedProducers models a root and
// child session that share one store. The root is parked after its mutation and
// summary capture but before publication; the child then reaches the shared
// publication serializer. Commit order and emitted-summary order must remain
// identical, so an older root summary can never land after the child's newer
// summary.
func TestTaskStoreMutationPublicationSerializesSharedProducers(t *testing.T) {
	store := newTestStore(t)
	rootCommitted := make(chan struct{})
	childWaiting := make(chan struct{})
	releaseRoot := make(chan struct{})
	type publication struct {
		epoch    uint64
		revision uint64
		summary  ListSummary
	}
	emitted := make(chan publication, 2)

	var waitingOnce sync.Once
	beforeMutationPublicationWaitHook = func() {
		waitingOnce.Do(func() { close(childWaiting) })
	}
	t.Cleanup(func() { beforeMutationPublicationWaitHook = nil })

	rootDone := make(chan error, 1)
	go func() {
		rootDone <- store.MutateAndPublish(func(epoch, revision uint64) error {
			if _, err := store.Append([]TaskInput{{Description: "root mutation", Prompt: "root"}}); err != nil {
				return err
			}
			summary := Summarize(store.View())
			close(rootCommitted)
			<-releaseRoot
			emitted <- publication{epoch: epoch, revision: revision, summary: summary}
			return nil
		})
	}()
	<-rootCommitted

	childDone := make(chan error, 1)
	go func() {
		childDone <- store.MutateAndPublish(func(epoch, revision uint64) error {
			if _, err := store.Append([]TaskInput{{Description: "child mutation", Prompt: "child"}}); err != nil {
				return err
			}
			emitted <- publication{epoch: epoch, revision: revision, summary: Summarize(store.View())}
			return nil
		})
	}()

	<-childWaiting
	close(releaseRoot)
	if err := <-rootDone; err != nil {
		t.Fatalf("root publication: %v", err)
	}
	if err := <-childDone; err != nil {
		t.Fatalf("child publication: %v", err)
	}

	first, second := <-emitted, <-emitted
	if first.summary.Total != 1 || second.summary.Total != 2 {
		t.Fatalf("emitted totals = [%d, %d], want commit order [1, 2]", first.summary.Total, second.summary.Total)
	}
	if first.revision != 1 || second.revision != 2 {
		t.Fatalf("publication revisions = [%d, %d], want [1, 2]", first.revision, second.revision)
	}
	if first.epoch == 0 || second.epoch != first.epoch {
		t.Fatalf("publication epochs = [%d, %d], want same nonzero store epoch", first.epoch, second.epoch)
	}
	if final := Summarize(store.View()); final.Total != 2 {
		t.Fatalf("final total = %d, want 2", final.Total)
	}
}

func TestTaskStorePublicationEpochDistinguishesIncarnations(t *testing.T) {
	first := newTestStore(t)
	second := newTestStore(t)
	var firstEpoch, secondEpoch uint64
	if err := first.MutateAndPublish(func(epoch, _ uint64) error {
		firstEpoch = epoch
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := second.MutateAndPublish(func(epoch, _ uint64) error {
		secondEpoch = epoch
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if firstEpoch == 0 || secondEpoch <= firstEpoch {
		t.Fatalf("store epochs = [%d, %d], want process-monotonic nonzero incarnations", firstEpoch, secondEpoch)
	}
}
