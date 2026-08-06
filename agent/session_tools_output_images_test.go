package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/internal/apptranscript"
	"primeradiant.com/serf/llm"
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
		Tool: llm.Tool{Definition: llm.ToolDefinition{
			Name:        toolName,
			Description: "fixture",
			Parameters:  map[string]any{"type": "object"},
		}},
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
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 's', 'h', 'o', 't'}
	sess, stop := imageToolSession(t, "screenshot", func() (any, error) {
		return tool.ImageResult{Text: "captured", Data: png, MediaType: "image/webp"}, nil
	})

	sess.execTool(context.Background(), llm.ToolCallData{
		ID: "call_shot", Name: "screenshot", Arguments: json.RawMessage(`{}`), Type: "function",
	}, "")
	data := toolCallEndData(t, stop(), "call_shot")

	if len(data.OutputImages) != 1 {
		t.Fatalf("OutputImages=%+v, want one descriptor for the returned bytes", data.OutputImages)
	}
	sum := sha256.Sum256(png)
	want := events.OutputImage{
		Source:    "tool-result",
		Name:      "screenshot",
		MediaType: "image/webp",
		Size:      int64(len(png)),
		SHA:       hex.EncodeToString(sum[:]),
	}
	if data.OutputImages[0] != want {
		t.Fatalf("OutputImages[0]=%+v, want %+v", data.OutputImages[0], want)
	}
}

// TestToolCallEndCarriesNoImageForAByteLessResult keeps the descriptor honest:
// every tool call would otherwise announce an image nothing can serve.
func TestToolCallEndCarriesNoImageForAByteLessResult(t *testing.T) {
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
	png := []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a, 'l', 'i', 'v', 'e'}
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

	turns, err := apptranscript.TurnsFromFile(transcriptPath, 128<<20, func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		return apptranscript.ProjectTurn(turnID, turnIndex, turn, map[string]string{}, nil, apptranscript.ToolResultOutputImages)
	})
	if err != nil {
		t.Fatalf("TurnsFromFile: %v", err)
	}
	var reloaded []appwire.OutputImage
	for _, turn := range turns {
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
