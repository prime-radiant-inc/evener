package appitempaging

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestBuildFetchResponseReassemblesCanonicalItemEncoding(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 7, Item: 2}
	item := appwire.ThreadItem{
		Type:          "commandExecution",
		ID:            "tool-1",
		TranscriptKey: "tool-key",
		Position:      &position,
		Version:       4,
		ToolName:      "shell",
		ArgumentsJSON: `{"command":"printf é"}` + strings.Repeat("a", ManagerFetchRangeBytes),
		Output:        strings.Repeat("é", ManagerFetchRangeBytes),
		Raw:           json.RawMessage(`{"provider":"kept-once"}`),
	}
	want, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "window-view", ProjectionVersion: TranscriptItemProjectionVersion}
	cursor := FetchCursor{Position: position, ItemVersion: item.Version, Origin: FetchCursorOriginServingDaemon}

	var got []byte
	for {
		response, err := BuildFetchResponse(item, identity, cursor)
		if err != nil {
			t.Fatal(err)
		}
		chunk, err := base64.StdEncoding.DecodeString(response.Data)
		if err != nil {
			t.Fatal(err)
		}
		if response.Position != position || response.Version != item.Version || response.TotalLength != uint64(len(want)) || response.Offset != uint64(len(got)) {
			t.Fatalf("range metadata = %+v, bytes so far = %d", response, len(got))
		}
		got = append(got, chunk...)
		if response.FetchCursor == "" {
			break
		}
		cursor, err = DecodeFetchCursor(response.FetchCursor, identity)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled bytes differ from canonical ThreadItem JSON")
	}
	var decoded appwire.ThreadItem
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("ranges split canonical JSON irrecoverably: %v", err)
	}
}

func TestBuildFetchResponseRefusesChangedItemVersion(t *testing.T) {
	position := appwire.ThreadItemPosition{Entry: 2}
	item := appwire.ThreadItem{Position: &position, Version: 9, Text: "changed"}
	identity := CursorIdentity{ThreadRef: "local:thread", Incarnation: "window-view", ProjectionVersion: TranscriptItemProjectionVersion}
	_, err := BuildFetchResponse(item, identity, FetchCursor{Position: position, ItemVersion: 8, Origin: FetchCursorOriginSavedIndex})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("changed version error = %T %v", err, err)
	}
	data, ok := wireErr.Data.(appwire.TranscriptItemChangedErrorData)
	if !ok || data.EvenerErrorInfo != appwire.ErrorTranscriptItemChanged || data.NewVersion != item.Version {
		t.Fatalf("changed version data = %#v", wireErr.Data)
	}
}
