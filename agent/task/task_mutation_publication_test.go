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
	emitted := make(chan ListSummary, 2)

	var waitingOnce sync.Once
	store.beforeMutationPublicationWait = func() {
		waitingOnce.Do(func() { close(childWaiting) })
	}

	rootDone := make(chan error, 1)
	go func() {
		rootDone <- store.MutateAndPublish(func() error {
			if _, err := store.Append([]TaskInput{{Description: "root mutation", Prompt: "root"}}); err != nil {
				return err
			}
			summary := Summarize(store.View())
			close(rootCommitted)
			<-releaseRoot
			emitted <- summary
			return nil
		})
	}()
	<-rootCommitted

	childDone := make(chan error, 1)
	go func() {
		childDone <- store.MutateAndPublish(func() error {
			if _, err := store.Append([]TaskInput{{Description: "child mutation", Prompt: "child"}}); err != nil {
				return err
			}
			emitted <- Summarize(store.View())
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
	if first.Total != 1 || second.Total != 2 {
		t.Fatalf("emitted totals = [%d, %d], want commit order [1, 2]", first.Total, second.Total)
	}
	if final := Summarize(store.View()); final.Total != 2 {
		t.Fatalf("final total = %d, want 2", final.Total)
	}
}
