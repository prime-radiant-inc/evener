package apptranscript

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/llm"
)

// TestToolResultOutputImagesMatchesTheLiveDescriptorFieldForField is the drift
// guard between the two descriptions of one image: the live one the agent puts
// on TOOL_CALL_END (events.ToolResultOutputImage) and the reload one projected
// back out of the transcript (ToolResultOutputImages). A session that describes
// an image one way while streaming and another way after a reload has two
// sources of truth for the same bytes.
//
// It compares BY FIELD NAME over the wire struct rather than against a
// hand-written fixture, so a field added to appwire.OutputImage tomorrow fails
// here — named — instead of silently going unprojected (docs/testing.md).
func TestToolResultOutputImagesMatchesTheLiveDescriptorFieldForField(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'b', 'o', 'd', 'y'}
	result := &llm.ToolResultData{Name: "screenshot", ImageData: data, ImageMediaType: "image/webp"}

	live, ok := events.ToolResultOutputImage(result.Name, result.ImageData, result.ImageMediaType)
	if !ok {
		t.Fatal("events.ToolResultOutputImage reported no image for a result carrying bytes")
	}
	replayed := ToolResultOutputImages(result)
	if len(replayed) != 1 {
		t.Fatalf("ToolResultOutputImages=%+v, want exactly one descriptor", replayed)
	}

	liveValue := reflect.ValueOf(live)
	replayedValue := reflect.ValueOf(replayed[0])
	replayedType := replayedValue.Type()
	for i := range replayedType.NumField() {
		name := replayedType.Field(i).Name
		liveField := liveValue.FieldByName(name)
		if !liveField.IsValid() {
			t.Errorf("appwire.OutputImage field %s has no counterpart on events.OutputImage; the live descriptor cannot carry it", name)
			continue
		}
		if !reflect.DeepEqual(liveField.Interface(), replayedValue.Field(i).Interface()) {
			t.Errorf("field %s: live=%v, replayed=%v", name, liveField.Interface(), replayedValue.Field(i).Interface())
		}
	}
	for i := range liveValue.Type().NumField() {
		name := liveValue.Type().Field(i).Name
		if _, found := replayedType.FieldByName(name); !found {
			t.Errorf("events.OutputImage field %s is dropped by ToolResultOutputImages", name)
		}
	}
}

// TestToolResultOutputImagesAddressesBytesBySHAWithNoURL pins the two facts the
// serving side depends on: the descriptor names its content by sha256, and it
// leaves the fetch route to whichever server publishes the thread.
func TestToolResultOutputImagesAddressesBytesBySHAWithNoURL(t *testing.T) {
	data := []byte("some image bytes")
	sum := sha256.Sum256(data)
	got := ToolResultOutputImages(&llm.ToolResultData{Name: "read_file", ImageData: data})
	if len(got) != 1 {
		t.Fatalf("ToolResultOutputImages=%+v, want one descriptor", got)
	}
	want := appwire.OutputImage{
		Source:    "tool-result",
		Name:      "read_file",
		MediaType: "image/png",
		Size:      int64(len(data)),
		SHA:       hex.EncodeToString(sum[:]),
	}
	if got[0] != want {
		t.Fatalf("descriptor=%+v, want %+v", got[0], want)
	}
}

func TestToolResultOutputImagesIgnoresResultsWithoutBytes(t *testing.T) {
	if got := ToolResultOutputImages(nil); got != nil {
		t.Fatalf("nil result projected %+v, want nothing", got)
	}
	if got := ToolResultOutputImages(&llm.ToolResultData{Name: "shell", Content: "no image here"}); got != nil {
		t.Fatalf("byte-less result projected %+v, want nothing", got)
	}
}

// TestToolResultOutputImagesIgnoresADocument keeps a PDF out of the thumbnail
// strip. read_file routes a document through the same ImageResult the vision
// side-channel consumes, so the bytes reach a tool result exactly like an
// image's do — but a document has nothing an <img> can render, and describing
// one only buys a fetch whose bytes the browser discards.
func TestToolResultOutputImagesIgnoresADocument(t *testing.T) {
	got := ToolResultOutputImages(&llm.ToolResultData{
		Name: "read_file", ImageData: []byte("%PDF-1.7 ..."), ImageMediaType: "application/pdf",
	})
	if got != nil {
		t.Fatalf("document projected %+v, want nothing", got)
	}
}

// TestToolResultOutputImagesDescribesAnyImageMediaType covers the media types
// the sha route can serve but the file-backed mechanism's sniffing allowlist
// cannot (kata 1nr4 noted the BMP gap): these bytes come straight out of the
// transcript with their recorded type, so no allowlist applies.
func TestToolResultOutputImagesDescribesAnyImageMediaType(t *testing.T) {
	for _, mediaType := range []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp", "image/svg+xml"} {
		got := ToolResultOutputImages(&llm.ToolResultData{Name: "read_file", ImageData: []byte("bytes"), ImageMediaType: mediaType})
		if len(got) != 1 || got[0].MediaType != mediaType {
			t.Errorf("%s projected %+v, want one descriptor carrying that media type", mediaType, got)
		}
	}
}
