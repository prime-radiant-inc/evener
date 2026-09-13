package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// preseedInput writes the delegate's opening entry before the run that
// executes it exists, so the id it mints belongs to a turn that has not
// started. Minting is not starting: every return after the mint can fail, and
// the runtime is then RETAINED without a launch
// (retainAdoptedWithoutLaunch), so a name adopted at mint time would stay on
// an idle child forever. activeTurnOwner prefers directTurnID, so the next
// thing that child publishes — an idle compaction fold, a recovery record —
// would be attributed to a turn that never ran.
func TestDelegatePreseedFailureLeavesNoExecutingTurnName(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	var childID string
	// Close the child's transcript writer just before the preseed appends, so
	// the preseed fails after the id has been minted. A closed writer takes
	// the append without reporting, so this lands on the strict readback —
	// the last of the returns that follow the mint, and the one furthest from
	// it.
	root.cfg.testOnly.delegateInitialInputAppend = func(child *Session) {
		childID = child.id
		child.mu.Lock()
		writer := child.transcript
		child.mu.Unlock()
		if writer == nil {
			t.Fatal("restored child transcript is unavailable")
		}
		if err := writer.Close(); err != nil {
			t.Fatalf("close child transcript: %v", err)
		}
	}

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, "preseed that cannot persist", 0)
	if outcome.result.Err == nil {
		t.Fatal("preseed with a closed transcript reported success")
	}
	if !strings.Contains(outcome.result.Err.Error(), "child input transcript") {
		t.Fatalf("preseed failure = %v, want a failure from the preseed itself", outcome.result.Err)
	}
	if childID == "" {
		t.Fatal("preseed never reported the child session")
	}
	sub := root.subagents.get(childID)
	if sub == nil {
		t.Fatal("the failed runtime was not retained; this test needs the retained-without-launch path")
	}
	sub.mu.Lock()
	running, driving := sub.running, sub.driving
	child := sub.sess
	sub.mu.Unlock()
	if running || driving {
		t.Fatalf("child is running=%v driving=%v; the failed preseed should have launched no run", running, driving)
	}
	if child == nil {
		t.Fatal("retained runtime has no session")
	}
	child.mu.Lock()
	directTurnID := child.directTurnID
	child.mu.Unlock()
	if directTurnID != "" {
		t.Fatalf("failed preseed left the executing-turn name %q on an idle child; activeTurnOwner would attribute its next record to a turn that never ran", directTurnID)
	}
	if owner := child.activeTurnOwner(); owner != "" {
		t.Fatalf("idle child after a failed preseed owns turn %q", owner)
	}
}

// The read-back is the preseed's proof that the entry it just wrote is on
// disk. Matching only the text cannot prove that: a write that silently did
// nothing leaves the NEWEST user input being some older entry, and delegates
// are routinely re-sent the same instruction, so identical text is ordinary
// rather than exotic. The run would then adopt an id no transcript entry
// carries, which is the divergence the whole identity chain exists to prevent.
func TestDelegatePreseedReadBackRequiresTheMintedTurnID(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	root, err := restoreDelegateResourceBootstrapSession(fixture.client, fixture.profile, fixture.workspace, fixture.meta, fixture.stateDir)
	if err != nil {
		t.Fatalf("restore root: %v", err)
	}
	defer root.Close()
	const input = "an instruction this delegate has been sent before"
	var (
		mu        sync.Mutex
		childID   string
		childPath string
	)
	root.cfg.testOnly.delegateInitialInputAppend = func(child *Session) {
		mu.Lock()
		childID, childPath = child.id, child.TranscriptPath()
		mu.Unlock()
	}
	// The read-back sees a transcript whose newest user input is an older
	// entry with the same text under a different id: exactly what an append
	// that silently did nothing leaves behind.
	previousOpen := openTranscriptFile
	openTranscriptFile = func(path string) (io.ReadCloser, error) {
		mu.Lock()
		target := childPath
		session := childID
		mu.Unlock()
		if target == "" || path != target {
			return previousOpen(path)
		}
		stale := schema.NewTurn(schema.TurnUserInput, llm.User(input))
		stale.StableTurnID = "turn_direct_from_an_earlier_send"
		line, marshalErr := json.Marshal(transcript.Entry{Kind: "entry", Seq: 1, Turn: stale})
		if marshalErr != nil {
			return nil, marshalErr
		}
		header, marshalErr := json.Marshal(transcript.Header{Kind: "header", FormatVersion: transcript.FormatVersion, SessionID: session})
		if marshalErr != nil {
			return nil, marshalErr
		}
		return io.NopCloser(bytes.NewReader(append(append(header, '\n'), append(line, '\n')...))), nil
	}
	t.Cleanup(func() { openTranscriptFile = previousOpen })

	outcome := (delegateRuntime{owner: root}).send(context.Background(), fixture.delegateID, input, 0)
	if outcome.result.Err == nil {
		t.Fatal("the read-back accepted an entry carrying a different turn id, so the run would adopt an id nothing on disk names")
	}
	if !strings.Contains(outcome.result.Err.Error(), "child input transcript") {
		t.Fatalf("preseed failure = %v, want a failure from the read-back itself", outcome.result.Err)
	}
	mu.Lock()
	id := childID
	mu.Unlock()
	if id == "" {
		t.Fatal("preseed never reported the child session")
	}
	sub := root.subagents.get(id)
	if sub == nil {
		t.Fatal("the failed runtime was not retained")
	}
	sub.mu.Lock()
	running, driving := sub.running, sub.driving
	sub.mu.Unlock()
	if running || driving {
		t.Fatalf("child is running=%v driving=%v; a read-back that could not prove its entry must start no run", running, driving)
	}
}
