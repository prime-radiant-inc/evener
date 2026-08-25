package events

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

// TestCovNewEventPayloads covers public event payloads not exercised by the
// main event constructor table.
func TestCovNewEventPayloads(t *testing.T) {
	tests := []struct {
		name string
		data EventData
		want EventKind
	}{
		{"SessionNameChanged", SessionNameChangedData{Name: "new name", Source: "user"}, EventSessionNameChanged},
		{"ModelChanged", ModelChangedData{OldProvider: "old-provider", OldModel: "old-model", NewProvider: "new-provider", NewModel: "new-model", ReasoningEffortLevels: []string{"low", "high"}, SupportsReasoning: true, MarkerText: "model changed"}, EventModelChanged},
		{"ReasoningEffortChanged", ReasoningEffortChangedData{ReasoningEffort: "high"}, EventReasoningEffortChanged},
		{"SandboxEscalationRequested", SandboxEscalationRequestedData{EscalationID: "esc_1", Mode: "workspace-write", Tool: "write_file", Kind: "write", DeniedPath: "/outside/file", Command: "write", OutputSoFar: "partial", PartiallyRan: true}, EventSandboxEscalationRequested},
		{"SandboxEscalationResolved", SandboxEscalationResolvedData{EscalationID: "esc_1"}, EventSandboxEscalationResolved},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := New(tc.data)
			if event.Kind != tc.want {
				t.Fatalf("New(%T).Kind = %v, want %v", tc.data, event.Kind, tc.want)
			}
			if !reflect.DeepEqual(event.Data, tc.data) {
				t.Fatalf("New(%T).Data = %#v, want payload preserved as %#v", tc.data, event.Data, tc.data)
			}
		})
	}
}

func TestCovToolResultOutputImage(t *testing.T) {
	for _, tc := range []struct {
		name      string
		data      []byte
		mediaType string
	}{
		{name: "nil data", data: nil, mediaType: "image/png"},
		{name: "empty data", data: []byte{}, mediaType: "image/png"},
		{name: "non-image media type", data: []byte("data"), mediaType: "application/pdf"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, ok := ToolResultOutputImage("fixture", tc.data, tc.mediaType); ok || got != (OutputImage{}) {
				t.Fatalf("ToolResultOutputImage() = (%+v, %v), want zero image and false", got, ok)
			}
		})
	}

	data := []byte("png-data")
	sum := sha256.Sum256(data)
	want := OutputImage{
		Source:    OutputImageSourceToolResult,
		Name:      "photo.png",
		MediaType: "image/png",
		Size:      int64(len(data)),
		SHA:       hex.EncodeToString(sum[:]),
	}
	got, ok := ToolResultOutputImage("photo.png", data, "image/png")
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolResultOutputImage() = (%+v, %v), want (%+v, true)", got, ok, want)
	}

	jpegData := []byte("jpeg-data")
	jpegSum := sha256.Sum256(jpegData)
	wantDefault := OutputImage{
		Source:    OutputImageSourceToolResult,
		Name:      "photo.jpg",
		MediaType: "image/png",
		Size:      int64(len(jpegData)),
		SHA:       hex.EncodeToString(jpegSum[:]),
	}
	got, ok = ToolResultOutputImage("photo.jpg", jpegData, "")
	if !ok || !reflect.DeepEqual(got, wantDefault) {
		t.Fatalf("ToolResultOutputImage(default media type) = (%+v, %v), want (%+v, true)", got, ok, wantDefault)
	}
}
