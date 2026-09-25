package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// Two client steers in one turn become two history steering items. Their
// identity is the server's: each id derives from its transcript key
// (item_steering_<entryIndex>, the 1-based entry index: its key's ordinal
// plus one, which is also its position's entry), each carries its steer request's
// clientMutationId, and both belong to the running execution turn, never to
// the steer mutation's own receipt id. The live stream a client reduces and a
// fresh read agree.
func TestTwoSteersInOneTurnAreHistoryItemsWithServerIdentity(t *testing.T) {
	sess, script, _, stateDir := newScriptedSession(t)
	ps := bridgeParitySession(t, sess, stateDir)
	t.Cleanup(func() { ps.close(t) })
	srv := ps.srv
	srv.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Steer: sess.AcceptClientMutationSteer})

	var receipts []string
	steer := func(clientMutationID, text string) {
		response, err := srv.handleAppTurnSteer(context.Background(), appwire.TurnSteerParams{
			ThreadID: sess.ID(), ClientMutationID: clientMutationID, ExpectedInstanceID: sess.ID(),
			Input: []appwire.InputItem{{Type: "text", Text: text}},
		})
		if err != nil {
			t.Errorf("steer %s: %v", clientMutationID, err)
		}
		receipts = append(receipts, response.Receipt.TurnID)
	}
	// Each steer lands while a round runs and is delivered as the next
	// round's STEERING entry.
	script.script(
		func(llm.Request) (llm.Response, error) {
			steer("steer-one", "look at the tests first")
			return parityCommunicate("comm-1", "Looking.", false), nil
		},
		func(llm.Request) (llm.Response, error) {
			steer("steer-two", "and then the docs")
			return parityCommunicate("comm-2", "Reading the docs too.", false), nil
		},
		step(parityCommunicate("comm-3", "Done with both.", true)),
	)
	if _, err := sess.ProcessInput(context.Background(), "review the change", nil); err != nil {
		t.Fatal(err)
	}
	ps.await(t, events.EventSessionEnd, nil)

	for label, turns := range map[string][]appwire.Turn{
		"live": ps.liveTurns(t),
		"read": readWholeThread(t, srv, sess.ID()).Thread.Turns,
	} {
		var steering []appwire.ThreadItem
		userTurn := ""
		for _, turn := range turns {
			for _, item := range turn.Items {
				switch item.Type {
				case "userMessage":
					userTurn = item.TurnID
				case "steering":
					steering = append(steering, item)
				}
			}
		}
		if len(steering) != 2 {
			t.Fatalf("%s: %d steering items, want 2: %+v", label, len(steering), steering)
		}
		for i, want := range []string{"steer-one", "steer-two"} {
			item := steering[i]
			if item.ClientMutationID != want {
				t.Errorf("%s: steering item %d clientMutationId = %q, want %q", label, i, item.ClientMutationID, want)
			}
			entryIndex := keyOrdinal(t, item.TranscriptKey) + 1
			if wantID := fmt.Sprintf("item_steering_%d", entryIndex); item.ID != wantID {
				t.Errorf("%s: steering item %s id = %q, want %q from its key", label, item.TranscriptKey, item.ID, wantID)
			}
			if item.Position == nil || item.Position.Entry != uint64(entryIndex) {
				t.Errorf("%s: steering item %s position = %+v, want entry %d", label, item.TranscriptKey, item.Position, entryIndex)
			}
			if userTurn == "" || item.TurnID != userTurn {
				t.Errorf("%s: steering item %d in turn %q, want the running turn %q", label, i, item.TurnID, userTurn)
			}
			if item.TurnID == receipts[i] {
				t.Errorf("%s: steering item %d takes its steer's receipt id %q as its turn", label, i, receipts[i])
			}
		}
		if steering[0].ID == steering[1].ID {
			t.Errorf("%s: both steers share the id %q", label, steering[0].ID)
		}
	}
}

// keyOrdinal is the entry ordinal of an item key,
// apptranscript-item-v2:<turnID>:<ordinal>:<part>.
func keyOrdinal(t *testing.T, key string) int {
	t.Helper()
	fields := strings.Split(key, ":")
	if len(fields) != 4 || fields[0] != "apptranscript-item-v2" {
		t.Fatalf("item key %q is not apptranscript-item-v2:<turnID>:<ordinal>:<part>", key)
	}
	ordinal, err := strconv.Atoi(fields[2])
	if err != nil {
		t.Fatalf("item key %q ordinal: %v", key, err)
	}
	return ordinal
}

// Every thread's read carries its own running turn: the root's is the
// execution SetProcessingTurn published; a delegate's, which nothing calls
// SetProcessingTurn for, is its overlay's running turn from
// EXECUTION_STARTED, cleared by that execution's EXECUTION_ENDED.
func TestEveryThreadReadCarriesItsOwnRunningTurn(t *testing.T) {
	root := newServedTranscript(t, NewServer(ServerConfig{}), "root")
	srv := root.srv
	childPath := writeDelegateTranscript(t, "child", "child work")
	srv.SetDescendantTranscriptPathFunc(func(string) string { return childPath })
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.SessionStartData{}))

	activeTurns := func() (rootTurn, childTurn string) {
		t.Helper()
		for ref, got := range map[string]*string{"local:root": &rootTurn, "local:child": &childTurn} {
			read, err := srv.appThreadReadSnapshotChecked(appwire.ThreadReadParams{Ref: ref, IncludeTurns: true})
			if err != nil {
				t.Fatalf("read %s: %v", ref, err)
			}
			*got = read.Thread.Evener.ActiveTurnID
		}
		list, err := srv.handleAppThreadList(context.Background(), appwire.ThreadListParams{})
		if err != nil {
			t.Fatal(err)
		}
		for _, thread := range list.Data {
			want := map[string]string{"root": rootTurn, "child": childTurn}[thread.ID]
			if thread.Evener.ActiveTurnID != want {
				t.Errorf("thread/list row %s activeTurnId = %q, its read says %q", thread.ID, thread.Evener.ActiveTurnID, want)
			}
		}
		return rootTurn, childTurn
	}

	if rootTurn, childTurn := activeTurns(); rootTurn != "" || childTurn != "" {
		t.Fatalf("idle threads read running turns root %q, child %q", rootTurn, childTurn)
	}

	startExecution(srv, "root", "t_root")
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.ExecutionStartedData{TurnID: "t_child"}))
	if rootTurn, childTurn := activeTurns(); rootTurn != "t_root" || childTurn != "t_child" {
		t.Fatalf("running turns root %q, child %q, want t_root and t_child", rootTurn, childTurn)
	}
	if overlay := srv.appHistoryForID("child").overlay.RunningTurnID(); overlay != "t_child" {
		t.Fatalf("the delegate's overlay runs %q, want t_child", overlay)
	}

	// An end for an execution the delegate is no longer running changes
	// nothing.
	srv.RecordDescendantAppEvent("root", threadEvent("child", events.ExecutionEndedData{TurnID: "t_stale", Status: "completed"}))
	if _, childTurn := activeTurns(); childTurn != "t_child" {
		t.Fatalf("after a stale execution end the delegate runs %q, want t_child", childTurn)
	}

	srv.RecordDescendantAppEvent("root", threadEvent("child", events.ExecutionEndedData{TurnID: "t_child", Status: "completed"}))
	if rootTurn, childTurn := activeTurns(); rootTurn != "t_root" || childTurn != "" {
		t.Fatalf("after the delegate's EXECUTION_ENDED: root %q, child %q, want t_root and none", rootTurn, childTurn)
	}

	srv.SetProcessing(false)
	if rootTurn, _ := activeTurns(); rootTurn != "" {
		t.Fatalf("after the root's processing ended it runs %q, want none", rootTurn)
	}
}
