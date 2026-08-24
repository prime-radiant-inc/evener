package events

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"
)

// TestCovEventKindMarkers covers event payload markers that are not exercised
// by the main event constructor table.
func TestCovEventKindMarkers(t *testing.T) {
	tests := []struct {
		name string
		data EventData
		want EventKind
	}{
		{"ReasoningSummaryDelta", ReasoningSummaryDeltaData{}, EventReasoningSummaryDelta},
		{"SessionNameChanged", SessionNameChangedData{}, EventSessionNameChanged},
		{"ModelChanged", ModelChangedData{}, EventModelChanged},
		{"ReasoningEffortChanged", ReasoningEffortChangedData{}, EventReasoningEffortChanged},
		{"TurnLimit", TurnLimitData{}, EventTurnLimit},
		{"LoopDetection", LoopDetectionData{}, EventLoopDetection},
		{"SandboxEscalationRequested", SandboxEscalationRequestedData{}, EventSandboxEscalationRequested},
		{"SandboxEscalationResolved", SandboxEscalationResolvedData{}, EventSandboxEscalationResolved},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.data.eventKind(); got != tc.want {
				t.Fatalf("eventKind() = %v, want %v", got, tc.want)
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
