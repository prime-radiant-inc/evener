package agent

import (
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
)

func TestHumanNoteConcurrentSameValueNotifiesOnce(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response, err := s.SetHumanNote(fmt.Sprintf("save-%d", i), " shared\nvalue ")
			if err != nil {
				t.Error(err)
				return
			}
			if response.Note != "shared value" {
				t.Errorf("response = %+v", response)
			}
		}()
	}
	wg.Wait()
	snapshot := s.clientMutations.snapshot()
	if len(snapshot.Journal) != 8 || len(snapshot.PendingExecutions) != 1 || len(snapshot.SteeringOrder) != 1 {
		t.Fatalf("same-value saves = %d records, %v steering", len(snapshot.Journal), snapshot.SteeringOrder)
	}
	updates := 0
	for len(s.Events()) > 0 {
		if ev := <-s.Events(); ev.Kind == events.EventNotesUpdated {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("same-value updates = %d", updates)
	}
	reloadHumanNoteStore(t, s)
	if note, _ := s.notesSnapshot(); note != "shared value" {
		t.Fatalf("restored note = %q", note)
	}
	if len(s.clientMutations.snapshot().SteeringOrder) != 1 {
		t.Fatal("restore duplicated notification")
	}
}
