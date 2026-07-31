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
