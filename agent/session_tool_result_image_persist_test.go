package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
)

// registerFixtureTool adds a second tool to a session built by
// newImageToolSession, so a test can drive a ROUND (several calls, one
// persist) instead of a single call.
func registerFixtureTool(t *testing.T, sess *Session, name string, exec func() (any, error)) {
	t.Helper()
	if err := sess.reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        name,
			Description: "fixture",
			Parameters:  map[string]any{"type": "object"},
		}},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return exec()
		},
	}); err != nil {
		t.Fatalf("Register %s: %v", name, err)
	}
}

func fixtureCall(id, name string) llm.ToolCallData {
	return llm.ToolCallData{ID: id, Name: name, Arguments: json.RawMessage(`{}`), Type: "function"}
}

// runFixtureRound runs one tool round exactly as the turn loop does
// (session_lifecycle.go: execToolBatch, then persistToolResults).
func runFixtureRound(t *testing.T, sess *Session, calls []llm.ToolCallData) {
	t.Helper()
	results, err := sess.execToolBatch(context.Background(), calls, sess.currentProfile(), "")
	if err != nil {
		t.Fatalf("execToolBatch: %v", err)
	}
	if err := sess.persistToolResults(context.Background(), calls, results); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
}

func kindIndices(collected []events.SessionEvent, kind events.EventKind) []int {
	var out []int
	for i, event := range collected {
		if event.Kind == kind {
			out = append(out, i)
		}
	}
	return out
}

func imagesPersistedData(t *testing.T, collected []events.SessionEvent) []events.ToolResultImagesPersistedData {
	t.Helper()
	var out []events.ToolResultImagesPersistedData
	for _, event := range collected {
		if event.Kind != events.EventToolResultImagesPersisted {
			continue
		}
		data, ok := event.Data.(events.ToolResultImagesPersistedData)
		if !ok {
			t.Fatalf("TOOL_RESULT_IMAGES_PERSISTED data is %T, want events.ToolResultImagesPersistedData", event.Data)
		}
		out = append(out, data)
	}
	return out
}

// transcriptImageSHAs projects the session's transcript the way a reader
// opening it later does, and returns every tool-result image sha it can serve
// from those bytes.
func transcriptImageSHAs(t *testing.T, sess *Session) []string {
	t.Helper()
	path := sess.TranscriptPath()
	if path == "" {
		t.Fatal("session has no transcript; the fixture did not enable state persistence")
	}
	turns, err := apptranscript.TurnsFromFile(path, 128<<20, func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
	})
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	var shas []string
	for _, turn := range turns {
		for _, item := range turn.Items {
			for _, img := range item.OutputImages {
				shas = append(shas, img.SHA)
			}
		}
	}
	return shas
}

// TestToolResultImagesAreAnnouncedWhenTheRoundIsWritten pins the fix for kata
// v3dv. A tool result's image bytes reach a reader only through the round's
// tool-result turn, and that turn is written once the WHOLE round finishes —
// so the sha a TOOL_CALL_END announces names nothing fetchable until then, for
// as long as the round's remaining calls take. The round therefore says so
// itself, once its results are in the transcript, and this test holds that
// announcement to both halves of the claim: it comes after every call in the
// round has ended, and the bytes it names really are in the transcript by then.
func TestToolResultImagesAreAnnouncedWhenTheRoundIsWritten(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'r', 'o', 'u', 'n', 'd'}
	sess, stop := imageToolSessionWithState(t, "screenshot", func() (any, error) {
		return tool.ImageResult{Text: "captured", Data: png, MediaType: "image/png"}, nil
	})
	// The hazard's own shape: an image read sharing a round with a second call
	// that finishes later. Both results persist together, at the end.
	registerFixtureTool(t, sess, "slow_shell", func() (any, error) {
		return tool.TextResult{Output: "built", FullOutput: "built"}, nil
	})

	runFixtureRound(t, sess, []llm.ToolCallData{
		fixtureCall("call_shot", "screenshot"),
		fixtureCall("call_shell", "slow_shell"),
	})
	sum := sha256.Sum256(png)
	wantSHA := hex.EncodeToString(sum[:])
	if got := transcriptImageSHAs(t, sess); !slices.Contains(got, wantSHA) {
		t.Fatalf("transcript serves image shas %v, want it to contain %s", got, wantSHA)
	}
	collected := stop()

	announced := imagesPersistedData(t, collected)
	if len(announced) != 1 {
		t.Fatalf("got %d TOOL_RESULT_IMAGES_PERSISTED events, want exactly one for the round", len(announced))
	}
	if want := []string{"call_shot"}; !slices.Equal(announced[0].CallIDs, want) {
		t.Fatalf("announced CallIDs=%v, want %v (only the call that returned bytes)", announced[0].CallIDs, want)
	}

	ends := kindIndices(collected, events.EventToolCallEnd)
	if len(ends) != 2 {
		t.Fatalf("got %d TOOL_CALL_END events, want one per call", len(ends))
	}
	persisted := kindIndices(collected, events.EventToolResultImagesPersisted)
	if persisted[0] < ends[len(ends)-1] {
		t.Fatalf("TOOL_RESULT_IMAGES_PERSISTED at index %d precedes the round's last TOOL_CALL_END at %d; "+
			"the round is not written until every call in it has ended", persisted[0], ends[len(ends)-1])
	}
}

// TestNoToolResultImageAnnouncementWithoutATranscript keeps the announcement
// from becoming the same lie it exists to remove: a session with no transcript
// writer has nowhere for the bytes to land, so no reader can ever fetch them
// and there is nothing to announce. The descriptor still rides TOOL_CALL_END —
// that a tool returned an image is true either way.
func TestNoToolResultImageAnnouncementWithoutATranscript(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'n', 'o', 'w', 'h', 'e', 'r', 'e'}
	sess, stop := imageToolSession(t, "screenshot", func() (any, error) {
		return tool.ImageResult{Text: "captured", Data: png, MediaType: "image/png"}, nil
	})
	if sess.TranscriptPath() != "" {
		t.Fatalf("fixture session has transcript %q; this test needs one with none", sess.TranscriptPath())
	}

	runFixtureRound(t, sess, []llm.ToolCallData{fixtureCall("call_shot", "screenshot")})
	collected := stop()

	if announced := imagesPersistedData(t, collected); len(announced) != 0 {
		t.Fatalf("got %d TOOL_RESULT_IMAGES_PERSISTED events, want none: nothing can serve these bytes", len(announced))
	}
	if data := toolCallEndData(t, collected, "call_shot"); len(data.OutputImages) != 1 {
		t.Fatalf("TOOL_CALL_END OutputImages=%+v, want the descriptor to survive", data.OutputImages)
	}
}

// TestARoundWithoutImagesAnnouncesNothing keeps the busiest path in the system
// quiet. Every tool round persists; only the rare one carrying image bytes has
// anything to announce, so the announcement is gated on the bytes rather than
// on the write.
func TestARoundWithoutImagesAnnouncesNothing(t *testing.T) {
	sess, stop := imageToolSessionWithState(t, "shell", func() (any, error) {
		return tool.TextResult{Output: "no image here", FullOutput: "no image here"}, nil
	})

	runFixtureRound(t, sess, []llm.ToolCallData{fixtureCall("call_shell", "shell")})

	if announced := imagesPersistedData(t, stop()); len(announced) != 0 {
		t.Fatalf("got %d TOOL_RESULT_IMAGES_PERSISTED events, want none for a round with no image bytes", len(announced))
	}
}

// TestADocumentResultIsNotAnnouncedAsAnImage keeps the announcement and the
// descriptor agreeing on what counts. read_file routes a PDF through the same
// ImageResult the vision side-channel consumes, and TOOL_CALL_END refuses to
// describe one as an image (a fetch whose bytes no <img> can render, kata
// 2fxm); a round carrying only a document has nothing to announce either.
func TestADocumentResultIsNotAnnouncedAsAnImage(t *testing.T) {
	pdf := []byte("%PDF-1.4 fixture")
	// A fixture name of its own, never a core tool's: registering over
	// "read_file" leaves the real definition's schema in place, and a fixture
	// call with no file_path is then refused before dispatch — so the tool
	// under test never runs and the assertion holds for the wrong reason.
	sess, stop := imageToolSessionWithState(t, "read_document", func() (any, error) {
		return tool.ImageResult{Text: "read", Data: pdf, MediaType: "application/pdf"}, nil
	})

	runFixtureRound(t, sess, []llm.ToolCallData{fixtureCall("call_doc", "read_document")})
	collected := stop()

	if announced := imagesPersistedData(t, collected); len(announced) != 0 {
		t.Fatalf("got %d TOOL_RESULT_IMAGES_PERSISTED events, want none for a document", len(announced))
	}
	if data := toolCallEndData(t, collected, "call_doc"); len(data.OutputImages) != 0 {
		t.Fatalf("TOOL_CALL_END OutputImages=%+v, want none for a document", data.OutputImages)
	}
}
