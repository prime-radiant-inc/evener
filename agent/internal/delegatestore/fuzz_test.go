//go:build serffuzz

package delegatestore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func FuzzFold(f *testing.F) {
	f.Add([]byte{0, 1, 2, 0, 1, 2, 7})
	f.Add([]byte{9, 3, 4, 5, 6})
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, program []byte) {
		if len(program) > 64 {
			program = program[:64]
		}
		events := buildFoldProgram(program)
		first, firstErr := Fold(events)
		second, secondErr := Fold(events)
		if fmt.Sprint(firstErr) != fmt.Sprint(secondErr) {
			t.Fatalf("Fold errors differ: first=%v second=%v", firstErr, secondErr)
		}
		if !bytes.Equal(mustMarshalFuzz(t, first), mustMarshalFuzz(t, second)) {
			t.Fatalf("Fold is nondeterministic:\nfirst=%s\nsecond=%s", mustMarshalFuzz(t, first), mustMarshalFuzz(t, second))
		}

		gap := cloneEvents(events)
		gap[len(gap)-1].Seq++
		if _, err := Fold(gap); err == nil {
			t.Fatal("Fold accepted a sequence gap")
		}
		mismatch := cloneEvent(events[0])
		mismatch.RunStarted = &RunStarted{Generation: 1, Trigger: TriggerInitial, StartedAt: time.Unix(1, 0).UTC()}
		if err := Apply(make(State), mismatch); err == nil {
			t.Fatal("Apply accepted a mismatched event payload")
		}
	})
}

func FuzzStoreReplay(f *testing.F) {
	f.Add([]byte("reported result"), false)
	f.Add([]byte("terminal failure"), true)
	f.Add([]byte{}, false)

	f.Fuzz(func(t *testing.T, message []byte, terminalError bool) {
		if len(message) > 256 {
			message = message[:256]
		}
		messageJSON, err := json.Marshal(string(message))
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		packet := TerminalPacket{Kind: PacketReported, Message: messageJSON, StructuredResult: json.RawMessage(`{"ok":true}`)}
		outcome := Outcome{Status: OutcomeCompleted, EndedAt: time.Unix(20, 0).UTC()}
		disposition := DispositionReported
		if terminalError {
			packet.Kind = PacketTerminalError
			packet.StructuredResult = nil
			outcome.Status = OutcomeFailed
			disposition = DispositionTerminalError
			if len(message) > 0 && message[0]&1 == 1 {
				resumable := true
				outcome.Status = OutcomeExhausted
				outcome.Reason = "tool_round_budget_exhausted"
				outcome.ExhaustionBudget = ExhaustionBudgetToolRounds
				outcome.ExhaustionLimit = int(message[0]) + 1
				outcome.Resumable = &resumable
				packet.Metadata = json.RawMessage(fmt.Sprintf(`{"exhaustion_budget":"%s","exhaustion_limit":%d,"resumable":true}`, outcome.ExhaustionBudget, outcome.ExhaustionLimit))
			}
		}

		path := filepath.Join(t.TempDir(), "delegates.jsonl")
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		_, state, err := store.AppendBatch(make(State), []Event{
			fuzzCreatedEvent("dlg_fuzz"),
			{
				Kind:       EventDelegateRunStarted,
				DelegateID: "dlg_fuzz",
				RunStarted: &RunStarted{Generation: 1, Trigger: TriggerInitial, StartedAt: time.Unix(10, 0).UTC()},
			},
		})
		if err != nil {
			_ = store.Close()
			t.Fatalf("Append create/start: %v", err)
		}
		_, accepted, err := store.AppendBatch(state, []Event{
			{
				Kind:             EventDelegateTerminalPrepared,
				DelegateID:       "dlg_fuzz",
				TerminalPrepared: &TerminalPrepared{Generation: 1, Packet: packet},
			},
			{
				Kind:       EventDelegateRunFinished,
				DelegateID: "dlg_fuzz",
				RunFinished: &RunFinished{
					Generation:  1,
					Outcome:     outcome,
					Disposition: disposition,
					DeliveryID:  "dlg_fuzz/delivery/1",
				},
			},
		})
		if err != nil {
			_ = store.Close()
			t.Fatalf("Append prepare/finish: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}

		reopened, err := Open(path)
		if err != nil {
			t.Fatalf("reopen: %v", err)
		}
		events, err := reopened.Load()
		if err != nil {
			_ = reopened.Close()
			t.Fatalf("Load: %v", err)
		}
		replayed, err := Fold(events)
		if err != nil {
			_ = reopened.Close()
			t.Fatalf("Fold reloaded: %v", err)
		}
		if err := reopened.Close(); err != nil {
			t.Fatalf("close reopened: %v", err)
		}
		if !bytes.Equal(mustMarshalFuzz(t, replayed), mustMarshalFuzz(t, accepted)) {
			t.Fatalf("reopen state differs:\nreplayed=%s\naccepted=%s", mustMarshalFuzz(t, replayed), mustMarshalFuzz(t, accepted))
		}
	})
}

func FuzzReadEvents(f *testing.F) {
	f.Add([]byte("{\"version\":1}\n"))
	f.Add([]byte("{\"version\":99}\n"))
	f.Add([]byte("{\"version\":1}\n{\"events\":["))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > 4096 {
			raw = raw[:4096]
		}
		path := filepath.Join(t.TempDir(), "delegates.jsonl")
		if err := os.WriteFile(path, raw, 0o400); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		fixed := time.Unix(1_000_000, 0).UTC()
		if err := os.Chtimes(path, fixed, fixed); err != nil {
			t.Fatalf("Chtimes: %v", err)
		}
		beforeBytes, beforeInfo := readFuzzSnapshot(t, path)

		first, firstErr := ReadEvents(path)
		second, secondErr := ReadEvents(path)
		if fmt.Sprint(firstErr) != fmt.Sprint(secondErr) {
			t.Fatalf("ReadEvents errors differ: first=%v second=%v", firstErr, secondErr)
		}
		if !bytes.Equal(mustMarshalFuzz(t, first), mustMarshalFuzz(t, second)) {
			t.Fatalf("ReadEvents results differ: first=%s second=%s", mustMarshalFuzz(t, first), mustMarshalFuzz(t, second))
		}
		afterBytes, afterInfo := readFuzzSnapshot(t, path)
		if !bytes.Equal(afterBytes, beforeBytes) {
			t.Fatalf("ReadEvents changed bytes:\nafter=%q\nbefore=%q", afterBytes, beforeBytes)
		}
		if afterInfo.Mode() != beforeInfo.Mode() || !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
			t.Fatalf("ReadEvents changed metadata: after=%v/%v before=%v/%v", afterInfo.Mode(), afterInfo.ModTime(), beforeInfo.Mode(), beforeInfo.ModTime())
		}
	})
}

func buildFoldProgram(program []byte) []Event {
	events := []Event{fuzzCreatedEvent("dlg_fuzz")}
	state, err := Fold(sequenceFuzzEvents(events))
	if err != nil {
		panic(err)
	}
	for _, operation := range program {
		aggregate := state["dlg_fuzz"]
		var candidate Event
		switch operation % 8 {
		case 0:
			candidate = Event{
				Kind:       EventDelegateRunStarted,
				DelegateID: "dlg_fuzz",
				RunStarted: &RunStarted{Generation: aggregate.Generation + 1, Trigger: TriggerOwnerInput, StartedAt: time.Unix(int64(len(events)+1), 0).UTC()},
			}
		case 1:
			candidate = Event{
				Kind:             EventDelegateTerminalPrepared,
				DelegateID:       "dlg_fuzz",
				TerminalPrepared: &TerminalPrepared{Generation: aggregate.Generation, Packet: TerminalPacket{Kind: PacketReported, Message: json.RawMessage(`"done"`)}},
			}
		case 2:
			candidate = Event{
				Kind:       EventDelegateRunFinished,
				DelegateID: "dlg_fuzz",
				RunFinished: &RunFinished{
					Generation:  aggregate.Generation,
					Outcome:     Outcome{Status: OutcomeCompleted, EndedAt: time.Unix(int64(len(events)+1), 0).UTC()},
					Disposition: DispositionReported,
					DeliveryID:  fmt.Sprintf("dlg_fuzz/delivery/%d", aggregate.Generation),
				},
			}
		case 3:
			candidate = Event{Kind: EventDelegateResumabilityClosed, DelegateID: "dlg_fuzz", ResumabilityClosed: &ResumabilityClosed{Reason: "fuzz_closed"}}
		case 4:
			candidate = Event{Kind: EventDelegateSubtreeStopRequested, DelegateID: "dlg_fuzz", SubtreeStopRequested: &SubtreeStopRequested{TargetDelegateID: "dlg_fuzz"}}
		case 5:
			candidate = Event{
				Kind:       EventDelegateRunFinished,
				DelegateID: "dlg_fuzz",
				RunFinished: &RunFinished{
					Generation:  aggregate.Generation,
					Outcome:     Outcome{Status: OutcomeStopped, EndedAt: time.Unix(int64(len(events)+1), 0).UTC()},
					Disposition: DispositionTerminalError,
					DeliveryID:  fmt.Sprintf("dlg_fuzz/delivery/%d", aggregate.Generation),
					Packet:      &TerminalPacket{Kind: PacketTerminalError, Message: json.RawMessage(`"stopped"`)},
				},
			}
		case 6:
			candidate = Event{Kind: EventDelegateSubtreeStopCompleted, DelegateID: "dlg_fuzz", SubtreeStopCompleted: &SubtreeStopCompleted{RequestSeq: aggregate.PendingStopSeq}}
		case 7:
			deliveryID := ""
			if len(aggregate.PendingDeliveries) > 0 {
				deliveryID = aggregate.PendingDeliveries[0].DeliveryID
			}
			candidate = Event{Kind: EventDelegateDeliveryAcknowledged, DelegateID: "dlg_fuzz", DeliveryAcknowledged: &DeliveryAcknowledged{DeliveryID: deliveryID}}
		}
		candidate.Seq = uint64(len(events) + 1)
		next, cloneErr := cloneState(state)
		if cloneErr != nil {
			panic(cloneErr)
		}
		if Apply(next, candidate) != nil {
			continue
		}
		state = next
		events = append(events, candidate)
	}
	return sequenceFuzzEvents(events)
}

func fuzzCreatedEvent(id string) Event {
	return Event{
		Kind:       EventDelegateCreated,
		DelegateID: id,
		Created: &DelegateCreated{Descriptor: Descriptor{
			ChildSessionID:  "session_" + id,
			TranscriptRef:   "transcript:" + id,
			OwnerSessionID:  "root",
			Task:            "fuzz task",
			AgentType:       "worker",
			ToolNameCeiling: []string{"communicate"},
			Resumable:       true,
		}},
	}
}

func sequenceFuzzEvents(events []Event) []Event {
	events = cloneEvents(events)
	for i := range events {
		events[i].Seq = uint64(i + 1)
	}
	return events
}

func mustMarshalFuzz(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	return encoded
}

func readFuzzSnapshot(t *testing.T, path string) ([]byte, os.FileInfo) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	return raw, info
}
