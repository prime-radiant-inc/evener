package agent

import (
	"bufio"
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
)

// A delegate's opening input is written straight to the child's transcript at
// preseed time, before the run that executes it exists, so it never reaches
// acceptUserInput's naming. The entry and the USER_INPUT event the child later
// emits are the two halves the cold and live projections read: they must carry
// the SAME non-empty id, or the transcript projection falls back to the entry
// index while the live projector mints a turn_%d of its own and every item's
// transcript key changes across a reload. The environment entry preseed writes
// first is what makes the two disagree from the very first turn.
func TestDelegatePreseededInputCarriesOneTurnIdentity(t *testing.T) {
	root, fixture, entered, release := newBlockingColdDelegateRuntime(t)
	var (
		mu           sync.Mutex
		childPath    string
		childSession *Session
		userInputs   []events.UserInputData
	)
	// The reader is a goroutine draining the child's stream, so the run
	// reaching its model call says nothing about whether the reader has
	// consumed the USER_INPUT event that preceded it. Closing this once the
	// reader has recorded that event is the completion the assertions below
	// await; without it they read the slice on scheduling luck and see it
	// empty (CI, agent shard 4 at 73186a7).
	//
	// ConsumeEventsLossless would order this too, and is deliberately not used:
	// it makes the session daemon-served, which is the opposite of the unserved
	// preseed this case is about.
	userInputRecorded := make(chan struct{})
	var recordOnce sync.Once
	root.cfg.testOnly.delegateInitialInputAppend = func(child *Session) {
		mu.Lock()
		childPath = child.TranscriptPath()
		childSession = child
		mu.Unlock()
		go func() {
			for event := range child.Events() {
				if data, ok := event.Data.(events.UserInputData); ok && event.Kind == events.EventUserInput {
					mu.Lock()
					userInputs = append(userInputs, data)
					mu.Unlock()
					recordOnce.Do(func() { close(userInputRecorded) })
				}
			}
		}()
	}
	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "preseeded delegate input", 0)
	if outcome.result.Err != nil || outcome.result.Action != "started" {
		t.Fatalf("idle send = %#v", outcome.result)
	}
	<-entered
	defer close(release)
	// TRIPWIRE: scripted in-process adapter and an in-memory reader, no real
	// I/O; only fires if the preseeded turn stops emitting USER_INPUT, which
	// the assertions below are here to diagnose.
	awaitWithin(t, 10*time.Second, "the preseeded USER_INPUT event reaching the reader", func() {
		<-userInputRecorded
	})

	mu.Lock()
	path := childPath
	child := childSession
	emitted := append([]events.UserInputData(nil), userInputs...)
	mu.Unlock()
	if path == "" {
		t.Fatal("preseed never reported the child transcript")
	}
	entryID, sawEnvironmentFirst := preseededUserInputIdentity(t, path)
	if !sawEnvironmentFirst {
		t.Fatal("preseed wrote no environment entry before the input; the divergence this pins needs one")
	}
	if entryID == "" {
		t.Fatal("preseeded USER_INPUT entry has no stable turn id; cold projection falls back to its entry index")
	}
	if len(emitted) != 1 {
		t.Fatalf("child emitted %d USER_INPUT events, want exactly the preseeded one", len(emitted))
	}
	if emitted[0].StableTurnID != entryID {
		t.Fatalf("live USER_INPUT id = %q, persisted entry id = %q; the two projections would name the same turn differently", emitted[0].StableTurnID, entryID)
	}
	// The run is blocked inside its model call, so the turn is executing. The
	// two checks above read only what acceptUserInput passed to the event;
	// this one reads the session, which is what every OTHER record the run
	// publishes -- round timings, steering, a fold's compaction records --
	// gets its owner from. Without it, dropping acceptUserInput's adoption of
	// the preseeded id leaves this case green while all of those go ownerless.
	if child == nil {
		t.Fatal("preseed never reported the child session")
	}
	if owner := child.activeTurnOwner(); owner != entryID {
		t.Fatalf("executing delegate run owns turn %q, want the preseeded entry's id %q", owner, entryID)
	}
	if !selfMintedTurnID(entryID) {
		t.Fatalf("preseeded turn id = %q, want a self-minted name (%s...)", entryID, directTurnIDPrefix)
	}
}

// preseededUserInputIdentity reports the child's first USER_INPUT entry's
// stable turn id, and whether an ENVIRONMENT entry precedes it.
func preseededUserInputIdentity(t *testing.T, path string) (turnID string, environmentFirst bool) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open child transcript: %v", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	sawEnvironment := false
	for scanner.Scan() {
		entry, decodeErr := transcript.DecodeEntry(scanner.Bytes())
		if decodeErr != nil {
			continue
		}
		switch entry.Turn.Kind {
		case schema.TurnEnvironment:
			sawEnvironment = true
		case schema.TurnUserInput:
			return entry.Turn.StableTurnID, sawEnvironment
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan child transcript: %v", err)
	}
	t.Fatal("child transcript has no USER_INPUT entry")
	return "", false
}
