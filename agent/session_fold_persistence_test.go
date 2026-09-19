package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"primeradiant.com/evener/agent/internal/tool"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

type foldFaultFS struct {
	afero.Fs
	mu      sync.Mutex
	mode    string
	marker  bool
	reached int
}
type foldFaultFile struct {
	afero.File
	fs *foldFaultFS
}

func (fs *foldFaultFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &foldFaultFile{f, fs}, nil
}
func (f *foldFaultFile) Write(p []byte) (int, error) {
	var entry transcript.Entry
	_ = json.Unmarshal(p, &entry)
	f.fs.mu.Lock()
	isMarker := entry.Turn.Compaction != nil
	if isMarker {
		f.fs.marker = true
		f.fs.reached++
	}
	mode := f.fs.mode
	f.fs.mu.Unlock()
	if isMarker && mode == "partial" {
		n, err := f.File.Write(p[:len(p)/2])
		return n, errors.Join(err, errors.New("injected partial marker"))
	}
	return f.File.Write(p)
}
func (f *foldFaultFile) Sync() error {
	f.fs.mu.Lock()
	mode, marker := f.fs.mode, f.fs.marker
	if mode == "source" && !marker {
		f.fs.reached++
	}
	f.fs.mu.Unlock()
	if mode == "source" && !marker || marker && (mode == "rollback" || mode == "retained") {
		return errors.New("injected sync failure")
	}
	return f.File.Sync()
}
func (f *foldFaultFile) Truncate(n int64) error {
	f.fs.mu.Lock()
	mode := f.fs.mode
	f.fs.mu.Unlock()
	if mode == "retained" || mode == "partial" {
		return errors.New("injected rollback refusal")
	}
	return f.File.Truncate(n)
}

func TestFoldPendingMarkerSettlesWithoutAnotherSummaryOrClaim(t *testing.T) {
	calls := 0
	s := newScriptedSummaryCompactSession(t, "fold-pending", func(llm.Request) llm.Response { calls++; return llm.Response{Message: llm.Assistant("summary")} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	for range 12 {
		s.appendTurn(schema.TurnUserInput, llm.User("older input"))
	}
	s.setPinnedNote("retained handoff")
	s.elicitNoteFn = func(_ context.Context, _ []schema.Turn) (string, error) { return "", nil }
	if err := s.transcript.Close(); err != nil {
		t.Fatal(err)
	}
	fs := &foldFaultFS{Fs: afero.NewOsFs(), mode: "retained"}
	w, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.id)
	if err != nil {
		t.Fatal(err)
	}
	s.attachTranscript(w)
	before := len(s.history)
	if err := s.Compact(t.Context()); !errors.Is(err, transcript.ErrRetainedUnsynced) {
		t.Fatalf("retained marker err=%v", err)
	}
	if fs.reached != 1 || len(s.history) != before || s.PinnedNote() != "retained handoff" || len(s.skillLifecycle.PendingHandoffs) != 0 {
		t.Fatal("uncommitted fold changed history or ownership")
	}
	if _, err := s.requestSkillCompaction(t.Context(), "conflicting note", "", schema.SkillReloadSelection{State: "absent"}); err == nil {
		t.Error("conflicting note mutation crossed pending marker")
	}
	fs.mu.Lock()
	fs.mode = ""
	fs.mu.Unlock()
	if err := s.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || fs.reached != 1 {
		t.Fatalf("settlement repeated work: summaries=%d marker writes=%d", calls, fs.reached)
	}
	if s.PinnedNote() != "" || len(s.skillLifecycle.PendingHandoffs) != 1 {
		t.Fatal("durable handoff did not settle exactly once")
	}
	data, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	history := mustResumeHistory(t, data.Entries)
	if len(history) < 2 || history[0].Kind != schema.TurnSummary {
		t.Fatal("whole retained marker did not reconstruct its tail")
	}
}

func TestFoldSummaryInputUsesRecordedPrivateProjection(t *testing.T) {
	const secret = "PRIVATE_FOLD_INPUT_SENTINEL"
	sawSummary := false
	s := newScriptedSummaryCompactSession(t, "private-fold", func(req llm.Request) llm.Response {
		sawSummary = true
		for _, message := range req.Messages {
			if strings.Contains(message.Text(), secret) {
				t.Error("automatic fold exposed private live evidence to the persisted-summary input")
			}
		}
		return llm.Response{Message: llm.Assistant("safe summary")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	for range 12 {
		s.appendTurn(schema.TurnUserInput, llm.User("older input"))
	}
	call := llm.ToolCallData{ID: "private", Name: "read_session_transcript", Arguments: json.RawMessage(`{"source":"api_log"}`)}
	s.recordTurn(schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}), schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}))
	result := tool.ExecResult{CallID: call.ID, ToolName: call.Name, Output: `{"private":"` + secret + `","source":"api_log"}`}
	s.appendCanceledToolResults([]llm.ToolCallData{call}, []tool.ExecResult{result}, context.Canceled)
	for range 4 {
		s.appendTurn(schema.TurnUserInput, llm.User("recent input"))
	}
	s.setPinnedNote("handoff")
	s.getOrCreateGoalStore().Set("retain goal", time.Now())
	if err := s.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !sawSummary {
		t.Fatal("did not reach actual summary input")
	}
	bytes, err := os.ReadFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(bytes), secret) {
		t.Fatal("private evidence reached durable fold data")
	}
}

func TestFoldMarkerFaultsLeaveCompleteOldHistory(t *testing.T) {
	for _, mode := range []string{"source", "rollback", "partial"} {
		t.Run(mode, func(t *testing.T) {
			s := newScriptedSummaryCompactSession(t, "fault-"+mode, func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("summary")} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
			disableSessionNaming(s)
			for range 12 {
				s.appendTurn(schema.TurnUserInput, llm.User("retained old history"))
			}
			s.setPinnedNote("unclaimed note")
			old := len(s.history)
			if err := s.transcript.Close(); err != nil {
				t.Fatal(err)
			}
			fs := &foldFaultFS{Fs: afero.NewOsFs(), mode: mode}
			w, _, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.id)
			if err != nil {
				t.Fatal(err)
			}
			s.attachTranscript(w)
			if err := s.Compact(t.Context()); err == nil {
				t.Fatal("faulted fold claimed success")
			}
			if fs.reached != 1 {
				t.Fatalf("fault barrier reached %d times", fs.reached)
			}
			if len(s.history) != old || s.PinnedNote() != "unclaimed note" || len(s.skillLifecycle.PendingHandoffs) != 0 || s.pendingFold != nil {
				t.Fatal("failed marker published history or ownership")
			}
			data, err := readTranscriptFull(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if history := mustResumeHistory(t, data.Entries); len(history) != old {
				t.Fatal("crash prefix lost prior complete history")
			}
			if mode == "partial" && (!w.Poisoned() || data.Skipped != 1) {
				t.Fatal("partial marker did not poison writer and remain an incomplete crash tail")
			}
			_ = w.Close()
			repaired, entries, err := transcript.OpenWriterForSession(s.TranscriptPath(), s.id)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repaired.Close() }()
			if len(mustResumeHistory(t, entries)) != old {
				t.Fatal("normal reopen lost old projection")
			}
		})
	}
}

func TestDurableFoldRefusesUnrecordedRetainedHistory(t *testing.T) {
	for _, mode := range []string{"unrecorded", "closed", "absent", "held"} {
		t.Run(mode, func(t *testing.T) {
			s := newScriptedSummaryCompactSession(t, "unrecorded-"+mode, func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("summary")} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
			disableSessionNaming(s)
			for range 12 {
				s.appendTurn(schema.TurnUserInput, llm.User("old"))
			}
			switch mode {
			case "unrecorded":
				s.history = append(s.history, schema.NewTurn(schema.TurnSteering, llm.User("not recorded")))
			case "closed":
				_ = s.transcript.Close()
				s.appendTurn(schema.TurnUserInput, llm.User("closed writer input"))
			case "absent":
				_ = s.transcript.Close()
				s.transcript = nil
				s.appendTurn(schema.TurnUserInput, llm.User("absent writer input"))
			case "held":
				w := s.transcript
				s.transcript = nil
				s.transcriptReady = false
				s.appendTurn(schema.TurnUserInput, llm.User("held input"))
				defer s.attachTranscript(w)
			}
			if err := s.Compact(t.Context()); err == nil {
				t.Fatal("unrecorded durable history was folded as if persisted")
			}
		})
	}
}

func TestDurableShortFoldDoesNotConsumeUnrecordedNote(t *testing.T) {
	s := newScriptedSummaryCompactSession(t, "short-fold", func(llm.Request) llm.Response { t.Fatal("short fold must not summarize"); return llm.Response{} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	if err := s.appendTurn(schema.TurnUserInput, llm.User("short history")); err != nil {
		t.Fatal(err)
	}
	if err := s.setPinnedNote("must survive refusal"); err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(t.Context()); err == nil {
		t.Fatal("fold without final marker claimed durable handoff")
	}
	if s.PinnedNote() != "must survive refusal" || len(s.skillLifecycle.PendingHandoffs) != 0 {
		t.Fatal("unrecorded handoff consumed ownership")
	}
}

func TestDurableShortFoldWithoutArtifactsIsNoOp(t *testing.T) {
	s := newScriptedSummaryCompactSession(t, "pure-short", func(llm.Request) llm.Response { t.Fatal("short fold must not summarize"); return llm.Response{} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	if err := s.appendTurn(schema.TurnUserInput, llm.User("short")); err != nil {
		t.Fatal(err)
	}
	before, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Compact(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != len(before.Entries) || len(s.skillLifecycle.PendingHandoffs) != 0 {
		t.Fatal("no-op wrote a marker or claimed handoff")
	}
}

func TestPrivateResultProjectionRespectsActualModelBoundary(t *testing.T) {
	for _, strategy := range []string{"compact", "ooda"} {
		t.Run(strategy, func(t *testing.T) {
			const secret = "PRIVATE_NEXT_REQUEST_SENTINEL"
			s := newScriptedSummaryCompactSession(t, "private-boundary", func(llm.Request) llm.Response {
				return llm.Response{Message: llm.Assistant(`{"action":"assistant","summary":"action logged","outcome":"success"}`)}
			}, withConfig(SessionConfig{StateDir: t.TempDir(), ContextStrategy: strategy, NoProjectPrompts: true}))
			disableSessionNaming(s)
			if strategy == "ooda" {
				if err := s.strategy.AfterAction(t.Context(), []schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User("prior action"))}, s.client); err != nil {
					t.Fatal(err)
				}
			}
			appendPrivateFoldResult(t, s, secret)
			reached := false
			s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
				reached = true
				found := false
				for _, message := range req.Messages {
					for _, part := range message.Content {
						if part.ToolResult != nil && strings.Contains(fmt.Sprint(part.ToolResult.Content), secret) {
							found = true
						}
					}
				}
				if !found {
					t.Error("ordinary provider request lost newly read private output without compaction")
				}
				if strategy == "ooda" && !requestContainsText(req, "[SESSION ORIENTATION]") {
					t.Error("ordinary provider request lost transient orientation")
				}
				return finalResponse("done")
			}}})
			if _, err := s.ProcessInput(t.Context(), "use the result", nil); err != nil {
				t.Fatal(err)
			}
			if !reached {
				t.Fatal("provider not reached")
			}
			data, err := readTranscriptFull(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range data.Entries {
				if entry.Turn.Compaction != nil {
					t.Fatal("low-pressure request fabricated a durable fold")
				}
			}
		})
	}
}

func appendPrivateFoldResult(t *testing.T, s *Session, text string) {
	t.Helper()
	call := llm.ToolCallData{ID: "private-boundary-call", Name: "read_session_transcript", Arguments: json.RawMessage(`{"source":"api_log"}`)}
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	if err := s.recordTurn(assistant, assistant); err != nil {
		t.Fatal(err)
	}
	s.appendCanceledToolResults([]llm.ToolCallData{call}, []tool.ExecResult{{CallID: call.ID, ToolName: call.Name, Output: text}}, context.Canceled)
}

func TestPrivateResultLivePressureTriggersCompaction(t *testing.T) {
	s := newScriptedSummaryCompactSession(t, "private-pressure", func(llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("safe summary")} }, withConfig(SessionConfig{StateDir: t.TempDir(), NoProjectPrompts: true}))
	disableSessionNaming(s)
	appendPrivateFoldResult(t, s, strings.Repeat("PRIVATE_PRESSURE_SENTINEL ", 50000))
	for range 12 {
		if err := s.appendTurn(schema.TurnUserInput, llm.User("recent input")); err != nil {
			t.Fatal(err)
		}
	}
	s.elicitNoteFn = func(context.Context, []schema.Turn) (string, error) { return "", nil }
	live := append([]schema.Turn(nil), s.history...)
	projected, err := s.canonicalFoldHistory(append([]schema.Turn(nil), live...))
	if err != nil {
		t.Fatal(err)
	}
	livePressure := s.contextMgr.Pressure(live, len(s.cachedSystemPrompt))
	canonicalPressure := s.contextMgr.Pressure(projected, len(s.cachedSystemPrompt))
	if livePressure <= canonicalPressure*2 {
		t.Fatal("fixture did not establish distinct live pressure")
	}
	s.contextMgr.CheckpointThreshold = (livePressure + canonicalPressure) / 2
	s.contextMgr.SummarizeThreshold = 1
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return finalResponse("done") }}})
	if _, err := s.ProcessInput(t.Context(), "continue", nil); err != nil {
		t.Fatal(err)
	}
	data, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range data.Entries {
		found = found || entry.Turn.Compaction != nil
	}
	if !found {
		t.Fatal("canonical placeholder hid actual model pressure and skipped required fold")
	}
}

func TestFoldTwiceReopensInlineNoteAndDistinctReusedCalls(t *testing.T) {
	summaries := 0
	s := newScriptedSummaryCompactSession(t, "twice", func(llm.Request) llm.Response {
		summaries++
		return llm.Response{Message: llm.Assistant("summarized older prefix")}
	}, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, NoProjectPrompts: true}))
	disableSessionNaming(s)
	for range 12 {
		if err := s.appendTurn(schema.TurnUserInput, llm.User("older prefix")); err != nil {
			t.Fatal(err)
		}
	}
	for _, group := range []string{"attempt-one", "attempt-two"} {
		call := llm.ToolCallData{ID: "reused", Name: "read_file", Arguments: json.RawMessage(`{}`)}
		assistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
		assistant.AttemptGroupID = group
		if err := s.recordTurn(assistant, assistant); err != nil {
			t.Fatal(err)
		}
		result := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{ToolCallID: call.ID, Name: call.Name, Content: group, ManagedInvocationID: "inv-" + group, ManagedAttemptGroupID: group}}}})
		if err := s.recordTurn(result, result); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.setPinnedNote("inline note survives twice"); err != nil {
		t.Fatal(err)
	}
	for fold := 0; fold < 2; fold++ {
		if err := s.Compact(t.Context()); err != nil {
			t.Fatal(err)
		}
		if summaries != fold+1 {
			t.Fatalf("actual summary calls=%d", summaries)
		}
		var err error
		s, err = restoreManagedFixture(t, s, &managedFixture{})
		if err != nil {
			t.Fatal(err)
		}
		disableSessionNaming(s)
		notes, results := 0, 0
		for resultIndex, turn := range s.history {
			if strings.Contains(turn.Message.Text(), "inline note survives twice") {
				notes++
			}
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.ManagedInvocationID != "" {
					results++
					index := schema.ManagedResultAssistantIndex(s.history, resultIndex, part.ToolResult)
					if index < 0 || s.history[index].AttemptGroupID != part.ToolResult.Content {
						t.Fatal("reused provider ID lost exact attempt pairing")
					}
				}
			}
		}
		if notes != 1 || results != 2 {
			t.Fatalf("reopened notes=%d results=%d", notes, results)
		}
	}
	data, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	markers, rawResults, inlineSources := 0, 0, 0
	for _, entry := range data.Entries {
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil {
				rawResults++
			}
		}
		if entry.Turn.Compaction != nil {
			markers++
			for _, item := range entry.Turn.Compaction.History {
				if item.Source != nil && item.Source.AddedIndex != nil {
					inlineSources++
				}
			}
		}
	}
	if markers != 2 || rawResults != 2 || inlineSources < 1 {
		t.Fatalf("markers=%d original results=%d concrete inline refs=%d", markers, rawResults, inlineSources)
	}
}
