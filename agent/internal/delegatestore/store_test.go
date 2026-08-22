package delegatestore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/task"
)

func TestCloneEventAttentionChangedIsolated(t *testing.T) {
	original := Event{
		Kind:             EventDelegateAttentionChanged,
		DelegateID:       "dlg_alpha",
		AttentionChanged: &DelegateAttentionChanged{NeedsAttention: true},
	}
	clone := cloneEvent(original)
	if clone.AttentionChanged == original.AttentionChanged {
		t.Fatal("clone aliased AttentionChanged payload")
	}
	clone.AttentionChanged.NeedsAttention = false
	if !original.AttentionChanged.NeedsAttention {
		t.Fatal("mutating clone changed original AttentionChanged payload")
	}
}

func TestStoreAppendBatchIsOneCrashAtomicLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions", "root", "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	callerState := make(State)
	created := createdEventWithReferenceDescriptor("dlg_alpha")
	assigned, accepted, err := store.AppendBatch(callerState, []Event{
		created,
		startedEvent("dlg_alpha", 1, TriggerInitial),
	})
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if len(assigned) != 2 || assigned[0].Seq != 1 || assigned[1].Seq != 2 {
		t.Fatalf("assigned events = %#v, want contiguous seq 1,2", assigned)
	}
	if len(callerState) != 0 {
		t.Fatalf("caller state mutated: %#v", callerState)
	}
	if aggregate := accepted["dlg_alpha"]; aggregate == nil || !aggregate.CurrentRunOpen || aggregate.Generation != 1 {
		t.Fatalf("accepted aggregate = %#v, want open generation 1", aggregate)
	}
	wantTaskTemplates := append([]task.TaskTemplate(nil), accepted["dlg_alpha"].Descriptor.TaskTemplates...)
	wantToolNameCeiling := append([]string(nil), accepted["dlg_alpha"].Descriptor.ToolNameCeiling...)
	created.Created.Descriptor.TaskTemplates[0].Prompt = "caller-mutated"
	created.Created.Descriptor.ToolNameCeiling[0] = "caller-mutated"
	if got := accepted["dlg_alpha"].Descriptor.TaskTemplates; !reflect.DeepEqual(got, wantTaskTemplates) {
		t.Fatalf("accepted task templates aliased caller mutation: got %#v want %#v", got, wantTaskTemplates)
	}
	if got := accepted["dlg_alpha"].Descriptor.ToolNameCeiling; !reflect.DeepEqual(got, wantToolNameCeiling) {
		t.Fatalf("accepted tool name ceiling aliased caller mutation: got %#v want %#v", got, wantToolNameCeiling)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	lines := bytes.Split(bytes.TrimSuffix(raw, []byte{'\n'}), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want version plus one batch\n%s", len(lines), raw)
	}
	var batch batchRecord
	if err := json.Unmarshal(lines[1], &batch); err != nil {
		t.Fatalf("decode batch line: %v", err)
	}
	if len(batch.Events) != 2 {
		t.Fatalf("batch events = %d, want 2", len(batch.Events))
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load reopened: %v", err)
	}
	replayed, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold reopened: %v", err)
	}
	if !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("reopened state differs:\n got %#v\nwant %#v", replayed, accepted)
	}
	aggregate := replayed["dlg_alpha"]
	if aggregate == nil || aggregate.Descriptor.SharedTaskStoreOwnerSessionID != "root_session" {
		t.Fatalf("reopened shared task store owner = %#v, want root_session", aggregate)
	}
	if !reflect.DeepEqual(aggregate.Descriptor.Config, created.Created.Descriptor.Config) {
		t.Fatalf("reopened descriptor config = %#v, want %#v", aggregate.Descriptor.Config, created.Created.Descriptor.Config)
	}
	if !reflect.DeepEqual(aggregate.Descriptor.TaskTemplates, wantTaskTemplates) {
		t.Fatalf("reopened task templates = %#v, want %#v", aggregate.Descriptor.TaskTemplates, wantTaskTemplates)
	}
	if !reflect.DeepEqual(aggregate.Descriptor.ToolNameCeiling, wantToolNameCeiling) {
		t.Fatalf("reopened tool name ceiling = %#v, want %#v", aggregate.Descriptor.ToolNameCeiling, wantToolNameCeiling)
	}
}

func TestStoreRoundTripsCanonicalPacketAndTypedExhaustion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, state, err := store.AppendBatch(make(State), []Event{
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerInitial),
	})
	if err != nil {
		t.Fatalf("Append create/start: %v", err)
	}
	resumable := false
	packet := TerminalPacket{
		Kind:     PacketTerminalError,
		Message:  json.RawMessage(`"max_turns exhausted at limit 23"`),
		Warnings: []string{"terminal warning"},
		Metadata: json.RawMessage(`{"exhaustion_budget":"max_turns","exhaustion_limit":23,"resumable":false}`),
	}
	assigned, accepted, err := store.AppendBatch(state, []Event{
		preparedEvent("dlg_alpha", 1, packet),
		{
			Kind:       EventDelegateRunFinished,
			DelegateID: "dlg_alpha",
			RunFinished: &RunFinished{
				Generation: 1,
				Outcome: Outcome{
					Status:           OutcomeExhausted,
					Reason:           "turn_budget_exhausted",
					EndedAt:          time.Unix(23, 0).UTC(),
					ExhaustionBudget: ExhaustionBudgetTurns,
					ExhaustionLimit:  23,
					Resumable:        &resumable,
				},
				Disposition: DispositionTerminalError,
				DeliveryID:  "dlg_alpha/delivery/1",
			},
		},
		{
			Kind:               EventDelegateResumabilityClosed,
			DelegateID:         "dlg_alpha",
			ResumabilityClosed: &ResumabilityClosed{Reason: "turn_budget_exhausted"},
		},
	})
	if err != nil {
		t.Fatalf("Append terminal batch: %v", err)
	}
	packet.Warnings[0] = "caller mutation"
	packet.Metadata[2] = 'x'
	resumable = true
	if got := assigned[1].RunFinished.Outcome.Resumable; got == nil || *got {
		t.Fatalf("assigned terminal event aliased caller resumability: %v", got)
	}
	if got := accepted["dlg_alpha"]; got.Phase != PhaseClosed || got.LatestOutcome == nil || got.LatestOutcome.Resumable == nil || *got.LatestOutcome.Resumable || got.PendingDeliveries[0].Packet.Warnings[0] != "terminal warning" || !json.Valid(got.PendingDeliveries[0].Packet.Metadata) {
		t.Fatalf("accepted terminal aggregate aliased caller data: %#v", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load reopened: %v", err)
	}
	replayed, err := Fold(events)
	if err != nil {
		t.Fatalf("Fold reopened: %v", err)
	}
	if !reflect.DeepEqual(replayed, accepted) {
		t.Fatalf("replayed terminal aggregate differs:\n got %#v\nwant %#v", replayed["dlg_alpha"], accepted["dlg_alpha"])
	}
}

func TestStoreInvalidBatchLeavesBytesSequenceAndStateUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, state, err := store.Append(make(State), createdEvent("dlg_alpha", ""))
	if err != nil {
		t.Fatalf("Append create: %v", err)
	}
	beforeBytes := mustReadFile(t, path)
	beforeState := stateJSON(t, state)
	beforeSeq := store.seq

	_, accepted, err := store.AppendBatch(state, []Event{
		startedEvent("dlg_alpha", 1, TriggerOwnerInput),
		finishedEvent("dlg_alpha", 2, OutcomeCompleted, DispositionReported, "dlg_alpha/delivery/2", nil),
	})
	if err == nil || !strings.Contains(err.Error(), "generation 2") {
		t.Fatalf("AppendBatch invalid error = %v, want generation rejection", err)
	}
	if accepted != nil {
		t.Fatalf("accepted state = %#v, want nil", accepted)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("bytes changed after rejected batch:\n got %q\nwant %q", got, beforeBytes)
	}
	if store.seq != beforeSeq {
		t.Fatalf("store seq = %d, want %d", store.seq, beforeSeq)
	}
	if got := stateJSON(t, state); got != beforeState {
		t.Fatalf("caller state changed:\n got %s\nwant %s", got, beforeState)
	}
}

func TestStoreAppendFailureLeavesBytesAndSequenceUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	beforeBytes := mustReadFile(t, path)
	beforeSeq := store.seq
	state := make(State)
	beforeState := stateJSON(t, state)

	store.ops.write = func(file *os.File, data []byte) (int, error) {
		n, writeErr := file.Write(data[:len(data)/2])
		if writeErr != nil {
			return n, writeErr
		}
		return n, errors.New("injected append failure")
	}
	_, accepted, err := store.AppendBatch(state, []Event{
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerInitial),
	})
	if err == nil || !strings.Contains(err.Error(), "injected append failure") {
		t.Fatalf("AppendBatch error = %v, want injected failure", err)
	}
	if accepted != nil {
		t.Fatalf("accepted state = %#v, want nil", accepted)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, beforeBytes) {
		t.Fatalf("bytes changed after failed write:\n got %q\nwant %q", got, beforeBytes)
	}
	if store.seq != beforeSeq {
		t.Fatalf("store seq = %d, want %d", store.seq, beforeSeq)
	}
	if got := stateJSON(t, state); got != beforeState {
		t.Fatalf("caller state changed:\n got %s\nwant %s", got, beforeState)
	}
}

func TestStoreSyncFailureRollsBackWholeBatch(t *testing.T) {
	t.Run("successful rollback keeps store usable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "delegates.jsonl")
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		beforeBytes := mustReadFile(t, path)
		beforeSeq := store.seq
		calls := 0
		store.ops.sync = func(file *os.File) error {
			calls++
			if calls == 1 {
				return errors.New("injected sync failure")
			}
			return file.Sync()
		}

		_, _, err = store.AppendBatch(make(State), []Event{
			createdEvent("dlg_alpha", ""),
			startedEvent("dlg_alpha", 1, TriggerInitial),
		})
		if err == nil || !strings.Contains(err.Error(), "injected sync failure") {
			t.Fatalf("AppendBatch error = %v, want injected sync failure", err)
		}
		if calls != 2 {
			t.Fatalf("sync calls = %d, want failed append sync plus rollback sync", calls)
		}
		if got := mustReadFile(t, path); !bytes.Equal(got, beforeBytes) {
			t.Fatalf("bytes changed after sync rollback:\n got %q\nwant %q", got, beforeBytes)
		}
		if store.seq != beforeSeq {
			t.Fatalf("store seq = %d, want %d", store.seq, beforeSeq)
		}
		store.ops.sync = func(file *os.File) error { return file.Sync() }
		if _, _, err := store.AppendBatch(make(State), []Event{createdEvent("dlg_alpha", "")}); err != nil {
			t.Fatalf("AppendBatch after successful rollback: %v", err)
		}
	})

	t.Run("failed rollback latches store unusable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "delegates.jsonl")
		store, err := Open(path)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		appendSyncErr := errors.New("injected append sync failure")
		rollbackSyncErr := errors.New("injected rollback sync failure")
		calls := 0
		store.ops.sync = func(*os.File) error {
			calls++
			if calls == 1 {
				return appendSyncErr
			}
			return rollbackSyncErr
		}

		_, _, err = store.AppendBatch(make(State), []Event{createdEvent("dlg_alpha", "")})
		if err == nil || !strings.Contains(err.Error(), "rollback") {
			t.Fatalf("AppendBatch error = %v, want rollback failure", err)
		}
		if !errors.Is(err, appendSyncErr) || !errors.Is(err, rollbackSyncErr) {
			t.Fatalf("AppendBatch error = %v, want both append and rollback causes", err)
		}
		store.ops.sync = func(file *os.File) error { return file.Sync() }
		_, _, err = store.AppendBatch(make(State), []Event{createdEvent("dlg_alpha", "")})
		if err == nil || !strings.Contains(err.Error(), "unusable") {
			t.Fatalf("AppendBatch after rollback failure = %v, want unusable latch", err)
		}
	})
}

func TestOpenRecoversOnlyUnterminatedTrailingBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	header := mustReadFile(t, path)
	batchBytes, err := json.Marshal(batchRecord{Events: sequence(
		createdEvent("dlg_alpha", ""),
		startedEvent("dlg_alpha", 1, TriggerInitial),
	)})
	if err != nil {
		t.Fatalf("marshal batch: %v", err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatalf("OpenFile append: %v", err)
	}
	if _, err := file.Write(batchBytes); err != nil {
		_ = file.Close()
		t.Fatalf("write unterminated batch: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close appended file: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open with trailing batch: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if got := mustReadFile(t, path); !bytes.Equal(got, header) {
		t.Fatalf("recovered bytes = %q, want header only %q", got, header)
	}
	events, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want unterminated create/start batch discarded", events)
	}
}

func TestOpenRecoversTornVersionHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	if err := os.WriteFile(path, []byte(`{"vers`), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open with torn header: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	raw := mustReadFile(t, path)
	if want := []byte("{\"version\":1}\n"); !bytes.Equal(raw, want) {
		t.Fatalf("recovered bytes = %q, want fresh header %q", raw, want)
	}
	events, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want empty store after torn-header recovery", events)
	}
	assigned, _, err := store.Append(make(State), createdEvent("dlg_alpha", ""))
	if err != nil {
		t.Fatalf("Append after recovery: %v", err)
	}
	if assigned.Seq != 1 {
		t.Fatalf("assigned seq = %d, want 1", assigned.Seq)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after recovery: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	events, err = reopened.Load()
	if err != nil {
		t.Fatalf("Load reopened: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 1 || events[0].DelegateID != "dlg_alpha" {
		t.Fatalf("reopened events = %#v, want the single appended create", events)
	}
}

func TestOpenRecoversTornFirstBatchAfterCompleteHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	header := []byte("{\"version\":1}\n")
	raw := append(append([]byte(nil), header...), `{"events":[{"kind`...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open with torn first batch: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if got := mustReadFile(t, path); !bytes.Equal(got, header) {
		t.Fatalf("recovered bytes = %q, want header only %q", got, header)
	}
	events, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("events = %#v, want torn first batch discarded", events)
	}
}

func TestOpenRejectsTerminatedMalformedHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	raw := []byte("{\"version\":}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "version header") {
		t.Fatalf("Open error = %v, want malformed terminated header rejection", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, raw) {
		t.Fatalf("malformed terminated header bytes changed:\n got %q\nwant %q", got, raw)
	}
}

func TestOpenRejectsTerminatedMalformedBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	raw := []byte("{\"version\":1}\n{\"events\":[}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "decode batch line") {
		t.Fatalf("Open error = %v, want malformed terminated batch", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, raw) {
		t.Fatalf("malformed terminated bytes changed:\n got %q\nwant %q", got, raw)
	}
}

func TestOpenRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	raw := []byte("{\"version\":2}\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, err := Open(path); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Fatalf("Open error = %v, want unknown version", err)
	}
	if got := mustReadFile(t, path); !bytes.Equal(got, raw) {
		t.Fatalf("unknown-version bytes changed:\n got %q\nwant %q", got, raw)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", path, err)
	}
	return raw
}
