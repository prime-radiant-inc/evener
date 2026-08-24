package events

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// TestCovEventKindMarkers covers all eventKind methods on event payload
// structs (eventdata.go lines 36-88). These are the uncovered 0.0% markers.
func TestCovEventKindMarkers(t *testing.T) {
	tests := []struct {
		name string
		data EventData
		want EventKind
	}{
		// Lines 43-59: the eventKind markers that were at 0%.
		{"ReasoningSummaryDelta", ReasoningSummaryDeltaData{}, EventReasoningSummaryDelta},
		{"SessionNameChanged", SessionNameChangedData{}, EventSessionNameChanged},
		{"ModelChanged", ModelChangedData{}, EventModelChanged},
		{"ReasoningEffortChanged", ReasoningEffortChangedData{}, EventReasoningEffortChanged},
		{"TurnLimit", TurnLimitData{}, EventTurnLimit},
		{"LoopDetection", LoopDetectionData{}, EventLoopDetection},
		// Lines 83-88: sandbox escalation markers.
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

// TestCovNew covers the New constructor (eventdata.go lines 25-31).
func TestCovNew(t *testing.T) {
	ev := New(WarningData{Message: "test"})
	if ev.Kind != EventWarning {
		t.Fatalf("Kind = %v, want %v", ev.Kind, EventWarning)
	}
	if ev.Timestamp.IsZero() {
		t.Fatal("Timestamp should be set")
	}
	if _, ok := ev.Data.(WarningData); !ok {
		t.Fatalf("Data type = %T, want WarningData", ev.Data)
	}
}

// TestCovToolResultOutputImage covers ToolResultOutputImage (payloads.go lines 191-208).
func TestCovToolResultOutputImage(t *testing.T) {
	// Empty data → false.
	if _, ok := ToolResultOutputImage("test.png", nil, "image/png"); ok {
		t.Fatal("empty data should return false")
	}
	if _, ok := ToolResultOutputImage("test.png", []byte{}, "image/png"); ok {
		t.Fatal("empty bytes should return false")
	}

	// Non-image media type → false.
	if _, ok := ToolResultOutputImage("doc.pdf", []byte("data"), "application/pdf"); ok {
		t.Fatal("non-image media type should return false")
	}

	// Image with explicit media type.
	img, ok := ToolResultOutputImage("photo.png", []byte("png-data"), "image/png")
	if !ok {
		t.Fatal("image/png should return true")
	}
	if img.Name != "photo.png" || img.MediaType != "image/png" || img.Size != 8 || img.Source != OutputImageSourceToolResult {
		t.Fatalf("img = %+v", img)
	}
	if img.SHA == "" {
		t.Fatal("SHA should be set")
	}

	// Image with empty media type — defaults to image/png.
	img, ok = ToolResultOutputImage("photo.jpg", []byte("jpeg-data"), "")
	if !ok {
		t.Fatal("empty media type should default to image/png")
	}
	if img.MediaType != "image/png" {
		t.Fatalf("default media type = %q", img.MediaType)
	}
}

// TestCovImageSHA covers imageSHA (payloads.go lines 212-215).
func TestCovImageSHA(t *testing.T) {
	// Deterministic: same bytes → same hash.
	data := []byte("hello")
	h1 := imageSHA(data)
	h2 := imageSHA(data)
	if h1 != h2 {
		t.Fatal("same bytes should produce same hash")
	}

	// Verify it's sha256 hex.
	sum := sha256.Sum256(data)
	expected := hex.EncodeToString(sum[:])
	if h1 != expected {
		t.Fatalf("imageSHA = %q, want %q", h1, expected)
	}

	// Different bytes → different hash.
	h3 := imageSHA([]byte("world"))
	if h1 == h3 {
		t.Fatal("different bytes should produce different hash")
	}

	// Empty bytes — still valid.
	h4 := imageSHA(nil)
	if h4 == "" {
		t.Fatal("empty bytes should produce non-empty hash")
	}

	// Verify lower case.
	if strings.ToUpper(h1) == h1 {
		t.Fatal("hash should be lowercase hex")
	}
}
