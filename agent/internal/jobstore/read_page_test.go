package jobstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadPageResumesAtNonzeroOffsetAndFoldsCompleteHistory(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	var data []byte
	source := []Event{{Seq: 1, Kind: EventJobStarted, JobID: "job_a", Type: JobShell, OwnerSessionID: "owner", Command: strings.Repeat("x", 511)}, {Seq: 2, Kind: EventJobFinished, JobID: "job_a", Status: StatusCompleted}}
	for _, event := range source {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		data = append(data, raw...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor PageCursor
	var got []Event
	for range 100 {
		before := cursor.Journal.Offset
		page, complete, err := ReadPage(t.Context(), path, &cursor, 37, 1)
		if err != nil {
			t.Fatal(err)
		}
		if cursor.Journal.Offset-before > 37 || len(page) > 1 {
			t.Fatal("page exceeded input/work limits")
		}
		got = append(got, page...)
		if complete {
			break
		}
		if cursor.Journal.Offset <= before {
			t.Fatal("cold continuation stalled")
		}
	}
	if !reflect.DeepEqual(got, source) {
		t.Fatalf("read events=%d, want %d", len(got), len(source))
	}
	if record := Fold(got)["job_a"]; record.Status != StatusCompleted || record.Command != source[0].Command {
		t.Fatalf("record=%+v", record)
	}
	before := cursor
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := ReadPage(ctx, path, &cursor, 37, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if !reflect.DeepEqual(cursor, before) {
		t.Fatal("cancellation changed cursor")
	}
}

func TestReadPageRetainsIncompleteTailUntilAppend(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "jobs.jsonl")
	prefix := []byte(`{"seq":1,"kind":"job_started","job_id":"job_tail"`)
	if err := os.WriteFile(path, prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	var cursor PageCursor
	if events, complete, err := ReadPage(t.Context(), path, &cursor, 4096, 20); err != nil || complete || len(events) != 0 {
		t.Fatalf("events=%v complete=%v error=%v", events, complete, err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString("}\n"); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	events, complete, err := ReadPage(t.Context(), path, &cursor, 4096, 20)
	if err != nil || !complete || len(events) != 1 || events[0].JobID != "job_tail" {
		t.Fatalf("events=%v complete=%v error=%v", events, complete, err)
	}
}
