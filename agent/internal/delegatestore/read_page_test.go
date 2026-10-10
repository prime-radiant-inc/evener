package delegatestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestReadPageOversizedBatchAdvancesWithoutExceedingEventBudget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	var batch []Event
	for i := range 2001 {
		batch = append(batch, createdEventWithReferenceDescriptor(fmt.Sprintf("dlg_%04d", i)))
	}
	assigned, _, err := store.AppendBatch(make(State), batch)
	if err != nil {
		t.Fatal(err)
	}
	var cursor PageCursor
	var got []Event
	for range 30 {
		before := cursor.Journal.Offset
		pending := cursor.DecodedEvents
		page, complete, err := ReadPage(t.Context(), path, &cursor, 4<<20, 2000)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2000 || cursor.Journal.Offset-before > 4<<20 {
			t.Fatal("page exceeds event/raw-byte budget")
		}
		got = append(got, page...)
		if decoded := cursor.DecodedEvents - pending; decoded > 2000 || decoded != uint64(len(page)) {
			t.Fatalf("decoded=%d emitted=%d", decoded, len(page))
		}
		if complete {
			break
		}
		if cursor.Journal.Offset <= before && cursor.DecodedEvents <= pending {
			t.Fatal("pending batch did not advance")
		}
	}
	if !reflect.DeepEqual(got, assigned) {
		t.Fatalf("events=%d, want %d", len(got), len(assigned))
	}
	if _, err := Fold(got); err != nil {
		t.Fatal(err)
	}
	before := cursor
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ReadPage(ctx, path, &cursor, 4<<20, 2000); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(cursor, before) {
		t.Fatal("cancellation changed cursor")
	}
}

func TestFoldCreationInstantSurvivesRunUpdates(t *testing.T) {
	t.Parallel()
	created := createdEventWithReferenceDescriptor("dlg_creation")
	created.TS = time.Unix(100, 0).UTC()
	created.Seq = 1
	started := startedEvent("dlg_creation", 1, TriggerInitial)
	started.TS = time.Unix(200, 0).UTC()
	started.Seq = 2
	state, err := Fold([]Event{created, started})
	if err != nil {
		t.Fatal(err)
	}
	if !state["dlg_creation"].CreatedAt.Equal(created.TS) {
		t.Fatalf("creation=%v", state["dlg_creation"].CreatedAt)
	}
}

func TestReadPageRetainsDelegateBatchUntilTerminatorArrives(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	assigned, _, err := store.AppendBatch(make(State), []Event{createdEventWithReferenceDescriptor("dlg_tail")})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-1); err != nil {
		t.Fatal(err)
	}
	var cursor PageCursor
	if events, complete, err := ReadPage(t.Context(), path, &cursor, 4<<20, 2000); err != nil || complete || len(events) != 0 {
		t.Fatalf("events=%v complete=%v error=%v", events, complete, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	events, complete, err := ReadPage(t.Context(), path, &cursor, 4<<20, 2000)
	if err != nil || !complete || !reflect.DeepEqual(events, assigned) {
		t.Fatalf("events=%v complete=%v error=%v", events, complete, err)
	}
}

func TestReadPageDoesNotDecodeBeyondEventBudget(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "delegates.jsonl")
	header, err := json.Marshal(versionRecord{Version: CurrentVersion})
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(createdEventWithReferenceDescriptor("dlg_first"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(createdEventWithReferenceDescriptor("dlg_second"))
	if err != nil {
		t.Fatal(err)
	}
	raw := header
	raw = append(raw, '\n')
	raw = append(raw, []byte(`{"events":[`)...)
	raw = append(raw, first...)
	raw = append(raw, ',')
	raw = append(raw, second...)
	raw = append(raw, []byte(`,{"seq":"invalid only after budget"}]}
`)...)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor PageCursor
	events, complete, err := ReadPage(t.Context(), path, &cursor, 4<<20, 2)
	if err != nil || complete || len(events) != 2 {
		t.Fatalf("first page decoded past budget: events=%d complete=%v error=%v", len(events), complete, err)
	}
	before := cursor
	if _, _, err := ReadPage(t.Context(), path, &cursor, 4<<20, 2); err == nil {
		t.Fatal("later malformed event was not decoded on next page")
	}
	if !reflect.DeepEqual(cursor, before) {
		t.Fatal("failed decode consumed pending position")
	}
}
