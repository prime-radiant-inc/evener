package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// imageToolSession stands up a session with one registered tool whose result is
// whatever exec returns, and collects every event the session emits. Callers
// stop collection with the returned func before reading the events.
func imageToolSession(t *testing.T, toolName string, exec func() (any, error)) (*Session, func() []events.SessionEvent) {
	t.Helper()
	return newImageToolSession(t, toolName, "", exec)
}

// imageToolSessionWithState is imageToolSession with a state directory, so the
// session writes a real transcript the reload path can be run against.
func imageToolSessionWithState(t *testing.T, toolName string, exec func() (any, error)) (*Session, func() []events.SessionEvent) {
	t.Helper()
	return newImageToolSession(t, toolName, t.TempDir(), exec)
}

func newImageToolSession(t *testing.T, toolName, stateDir string, exec func() (any, error)) (*Session, func() []events.SessionEvent) {
	t.Helper()
	dir := t.TempDir()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	sess, err := NewSession(client, NewOpenAIProfile("test-model"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		// explorer skips the vision side-channel; these tests are about the
		// descriptor, not about describing the image.
		AgentName: "explorer",
		StateDir:  stateDir,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })

	var mu sync.Mutex
	var collected []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for event := range sess.Events() {
			mu.Lock()
			collected = append(collected, event)
			mu.Unlock()
		}
	}()

	if err := sess.reg.Register(tool.RegisteredTool{
		Definition: llm.ToolDefinition{
			Name:        toolName,
			Description: "fixture",
			Parameters:  map[string]any{"type": "object"},
		},
		Exec: func(context.Context, execenv.ExecutionEnvironment, map[string]any) (any, error) {
			return exec()
		},
	}); err != nil {
		t.Fatalf("Register %s: %v", toolName, err)
	}

	return sess, func() []events.SessionEvent {
		sess.Close()
		<-done
		mu.Lock()
		defer mu.Unlock()
		return append([]events.SessionEvent(nil), collected...)
	}
}

// TestToolCallEndCarriesABoundedExcerptOfTheOutput is the wire contract for a
// tool result's text: the deltas carry every byte, and the end event carries
// only the excerpt a reader that missed them renders, plus the true byte
// length, so the two copies cannot both hold the whole payload.
func TestToolCallEndCarriesABoundedExcerptOfTheOutput(t *testing.T) {
	t.Parallel()
	full := strings.Repeat("read file body\n", 4_000)
	sess, stop := imageToolSession(t, "read_file", func() (any, error) {
		return tool.TextResult{Output: full, FullOutput: full}, nil
	})

	sess.execTool(context.Background(), llm.ToolCallData{
		ID: "call_read", Name: "read_file", Arguments: json.RawMessage(`{"path":"/tmp/x"}`), Type: "function",
	}, "")
	collected := stop()

	var streamed strings.Builder
	for _, event := range collected {
		if delta, ok := event.Data.(events.ToolCallOutputDeltaData); ok && delta.CallID == "call_read" {
			streamed.WriteString(delta.Delta)
		}
	}
	if streamed.String() != full {
		t.Fatalf("deltas carried %d bytes, want the whole %d-byte output", streamed.Len(), len(full))
	}

	data := toolCallEndData(t, collected, "call_read")
	if got := len([]rune(data.Output)); got != events.ToolEndExcerptRunes {
		t.Fatalf("end event carried %d runes, want the %d-rune excerpt", got, events.ToolEndExcerptRunes)
	}
	if data.OutputBytes != len(full) {
		t.Fatalf("OutputBytes=%d, want the true %d-byte length", data.OutputBytes, len(full))
	}
	if !strings.HasPrefix(full, data.Output) {
		t.Fatal("the excerpt is not the head of the output the deltas streamed")
	}

	// A delta boundary must not fall inside a rune. JSON encoding turns an
	// invalid fragment into replacement characters, and the end event carries
	// only an excerpt, so a split delta is lost for good. Reassemble from the
	// encoded bytes, since that is where the damage appears.
	t.Run("multibyte", func(t *testing.T) {
		t.Parallel()
		multibyte := strings.Repeat("€", 2_000) // 6,000 bytes: the first 4,000-byte boundary lands mid-rune
		sess, stop := imageToolSession(t, "read_file", func() (any, error) {
			return tool.TextResult{Output: multibyte, FullOutput: multibyte}, nil
		})

		sess.execTool(context.Background(), llm.ToolCallData{
			ID: "call_multibyte", Name: "read_file", Arguments: json.RawMessage(`{"path":"/tmp/x"}`), Type: "function",
		}, "")
		collected := stop()

		var streamed strings.Builder
		for _, event := range collected {
			delta, ok := event.Data.(events.ToolCallOutputDeltaData)
			if !ok || delta.CallID != "call_multibyte" {
				continue
			}
			encoded, err := json.Marshal(delta.Delta)
			if err != nil {
				t.Fatalf("marshal delta: %v", err)
			}
			var decoded string
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("unmarshal delta: %v", err)
			}
			streamed.WriteString(decoded)
		}
		if got := streamed.String(); got != multibyte {
			t.Fatalf("reassembled deltas carried %d runes, want the whole %d-rune output; %d replacement characters",
				len([]rune(got)), len([]rune(multibyte)), strings.Count(got, "\uFFFD"))
		}
	})
}

// TestToolCallEndSendsAShortOutputWhole keeps the single-copy guarantee from
// costing a short result its text: a text within the excerpt bound arrives
// whole and states no length, so no reader has to reassemble it.
func TestToolCallEndSendsAShortOutputWhole(t *testing.T) {
	t.Parallel()
	sess, stop := imageToolSession(t, "write_file", func() (any, error) {
		return tool.TextResult{Output: "wrote 12 bytes", FullOutput: "wrote 12 bytes"}, nil
	})

	sess.execTool(context.Background(), llm.ToolCallData{
		ID: "call_write", Name: "write_file", Arguments: json.RawMessage(`{"path":"/tmp/x"}`), Type: "function",
	}, "")
	data := toolCallEndData(t, stop(), "call_write")

	if data.Output != "wrote 12 bytes" {
		t.Fatalf("Output=%q, want the whole text", data.Output)
	}
	if data.OutputBytes != 0 {
		t.Fatalf("OutputBytes=%d, want 0 so its presence means an excerpt", data.OutputBytes)
	}
}

func toolCallEndData(t *testing.T, collected []events.SessionEvent, callID string) events.ToolCallEndData {
	t.Helper()
	for _, event := range collected {
		if event.Kind != events.EventToolCallEnd {
			continue
		}
		data, ok := event.Data.(events.ToolCallEndData)
		if !ok {
			t.Fatalf("TOOL_CALL_END data is %T, want events.ToolCallEndData", event.Data)
		}
		if data.CallID == callID {
			return data
		}
	}
	t.Fatalf("no TOOL_CALL_END for call %q in %d events", callID, len(collected))
	return events.ToolCallEndData{}
}

// TestToolCallEndCarriesTheToolResultImage is the live half of the fix: a tool
// that hands back image bytes has to say so on the event, or a reader watching
// the session stream has no way to know the image exists until the session is
// read back off disk (kata 2fxm).
func TestToolCallEndCarriesTheToolResultImage(t *testing.T) {
	t.Parallel()
	webp := validWebPFixture(t)
	sess, stop := imageToolSession(t, "screenshot", func() (any, error) {
		return tool.ImageResult{Text: "captured", Data: webp, MediaType: "image/webp"}, nil
	})

	sess.execTool(context.Background(), llm.ToolCallData{
		ID: "call_shot", Name: "screenshot", Arguments: json.RawMessage(`{}`), Type: "function",
	}, "")
	data := toolCallEndData(t, stop(), "call_shot")

	if len(data.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want one descriptor for the returned bytes", data.OutputImages)
	}
	sum := sha256.Sum256(webp)
	want := events.OutputImage{
		Source:    "tool-result",
		Name:      "screenshot",
		MediaType: "image/webp",
		Size:      int64(len(webp)),
		SHA:       hex.EncodeToString(sum[:]),
	}
	if data.OutputImages[0] != want {
		t.Fatalf("OutputImages[0]=%+v, want %+v", data.OutputImages[0], want)
	}
}

// TestToolCallEndCarriesNoImageForAByteLessResult keeps the descriptor honest:
// every tool call would otherwise announce an image nothing can serve.
func TestToolCallEndCarriesNoImageForAByteLessResult(t *testing.T) {
	t.Parallel()
	sess, stop := imageToolSession(t, "shell", func() (any, error) {
		return tool.TextResult{Output: "no image here", FullOutput: "no image here"}, nil
	})

	sess.execTool(context.Background(), llm.ToolCallData{
		ID: "call_shell", Name: "shell", Arguments: json.RawMessage(`{}`), Type: "function",
	}, "")
	if data := toolCallEndData(t, stop(), "call_shell"); len(data.OutputImages) != 0 {
		t.Fatalf("OutputImages=%+v, want none for a result with no bytes", data.OutputImages)
	}
}

// TestLiveToolResultImageMatchesItsReloadedProjection is the no-two-sources-of-
// truth guard at session level: one real session runs an image-returning tool,
// and the descriptor it streamed live is compared against the one projected
// from the transcript it wrote. A reader watching the session and a reader
// opening it later must be told the same thing about the same bytes.
func TestLiveToolResultImageMatchesItsReloadedProjection(t *testing.T) {
	t.Parallel()
	png := validPNGFixture(t)
	sess, stop := imageToolSessionWithState(t, "screenshot", func() (any, error) {
		return tool.ImageResult{Text: "captured", Data: png, MediaType: "image/png"}, nil
	})

	call := llm.ToolCallData{ID: "call_shot", Name: "screenshot", Arguments: json.RawMessage(`{}`), Type: "function"}
	res := sess.execTool(context.Background(), call, "")
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{call}, []tool.ExecResult{res}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	transcriptPath := sess.TranscriptPath()
	if transcriptPath == "" {
		t.Fatal("session has no transcript; the fixture did not enable state persistence")
	}
	live := toolCallEndData(t, stop(), "call_shot").OutputImages
	if len(live) != 1 {
		t.Fatalf("live OutputImages=%+v, want one descriptor", live)
	}

	proj, err := apptranscript.ItemTurnProjectionFromFile(transcriptPath, 128<<20, func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectTurnParts(turnID, turnIndex, turn, apptranscript.NewToolCallRegistry(), nil, apptranscript.ToolResultOutputImages)
		return items
	})
	if err != nil {
		t.Fatalf("ItemTurnProjectionFromFile: %v", err)
	}
	var reloaded []appwire.OutputImage
	for _, turn := range proj.Turns {
		for _, item := range turn.Items {
			if item.CallID == "call_shot" && len(item.OutputImages) > 0 {
				reloaded = item.OutputImages
			}
		}
	}
	if len(reloaded) != 1 {
		t.Fatalf("reloaded OutputImages=%+v, want one descriptor projected from the transcript", reloaded)
	}
	want := appwire.OutputImage{
		Source: live[0].Source, Name: live[0].Name, MediaType: live[0].MediaType,
		Size: live[0].Size, URL: live[0].URL, SHA: live[0].SHA, Path: live[0].Path,
	}
	if reloaded[0] != want {
		t.Fatalf("reloaded descriptor=%+v, live descriptor=%+v", reloaded[0], want)
	}
}
