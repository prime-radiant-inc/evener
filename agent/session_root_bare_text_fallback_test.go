package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func bareTextResponse(text string, reason string) llm.Response {
	return llm.Response{Message: llm.Assistant(text), Finish: llm.FinishReason{Reason: reason}}
}

func TestRootBareTextFallback(t *testing.T) {
	structured, err := provider.WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), map[string]any{"type": "object", "additionalProperties": true})
	if err != nil {
		t.Fatal(err)
	}
	base := tool.DefCommunicateNamed("communicate")
	defaultOutput := base.Parameters["properties"].(map[string]any)["output"].(map[string]any)
	boundDefault, err := provider.WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), defaultOutput)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, text   string
		finish       string
		kind         EntryKind
		profile      *provider.Profile
		parent       bool
		wantFallback bool
	}{
		{name: "natural stop", text: "Root answer", finish: llm.FinishReasonStop, kind: EntryUserInput, wantFallback: true},
		{name: "truncated", text: "Partial answer", finish: llm.FinishReasonLength, kind: EntryUserInput},
		{name: "empty", finish: llm.FinishReasonStop, kind: EntryUserInput},
		{name: "notification", text: "Acknowledgement", finish: llm.FinishReasonStop, kind: EntryNotification},
		{name: "subagent", text: "Unreported answer", finish: llm.FinishReasonStop, kind: EntryUserInput, parent: true},
		{name: "explicit default envelope", text: "Untyped answer", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: boundDefault},
		{name: "output schema", text: "Untyped answer", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: structured},
		{name: "allowed decisions", text: "No decision", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: provider.WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"approve"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			s := newSession(t, withProfile(tc.profile), withSteps(func(llm.Request) llm.Response {
				calls++
				return bareTextResponse(tc.text, tc.finish)
			}, func(llm.Request) llm.Response {
				calls++
				switch tc.name {
				case "explicit default envelope":
					return quietYieldResponse("recovered", `{"message":"Explicit answer","output":{}}`)
				case "output schema", "allowed decisions":
					return quietYieldResponse("recovered", `{"message":"Explicit answer","output":{"decision":"approve"}}`)
				default:
					return agenttest.FinalResponse("Explicit answer")
				}
			}))
			if tc.parent {
				s.cfg.spawn.parentSessionID = "parent"
			}
			input := "Question"
			if tc.kind == EntryNotification {
				input = ""
				enqueueCompletedJobNotification(t, s, "last-job")
			}
			got, err := s.ProcessInputKind(tripwireCtx(t), input, nil, tc.kind)
			if tc.wantFallback {
				if err != nil || got != tc.text || calls != 1 || s.State() != SessionIdle {
					t.Fatalf("reply=%q calls=%d state=%v err=%v", got, calls, s.State(), err)
				}
				return
			}
			if calls != 2 || err != nil || s.State() != SessionIdle {
				t.Fatalf("ineligible reply=%q calls=%d state=%v err=%v", got, calls, s.State(), err)
			}
			if tc.name == "output schema" || tc.name == "allowed decisions" {
				var reply struct{ Message, Decision string }
				if err := json.Unmarshal([]byte(got), &reply); err != nil || reply.Message != "Explicit answer" || reply.Decision != "approve" {
					t.Fatalf("structured reply=%q decode=%v", got, err)
				}
			} else if got != "Explicit answer" {
				t.Fatalf("reply=%q, want the explicit reply", got)
			}
		})
	}
}

func TestRootBareTextFallbackStopHook(t *testing.T) {
	for _, tc := range []struct {
		name, firstResult, want string
		wantCalls               int
		wantAccepted            string
	}{
		{"allow", `{}`, "First draft", 1, "First draft"},
		{"block", `{"decision":"block","reason":"Try again"}`, "Second draft", 2, "Second draft"},
		{"replace", `{"continue":false,"stopReason":"Replacement"}`, "Replacement", 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSession(t, withSteps(func(llm.Request) llm.Response {
				return bareTextResponse("First draft", llm.FinishReasonStop)
			}, func(llm.Request) llm.Response {
				return bareTextResponse("Second draft", llm.FinishReasonStop)
			}, func(llm.Request) llm.Response { return agenttest.FinalResponse("Unexpected retry") }), withStateDir(t.TempDir()))
			stops := 0
			installCommunicateStopHook(s, func() string {
				stops++
				if stops == 1 {
					return tc.firstResult
				}
				return `{}`
			})
			got, err := s.ProcessInput(tripwireCtx(t), "Question", nil)
			if err != nil || got != tc.want || stops != tc.wantCalls || s.State() != SessionIdle {
				t.Fatalf("reply=%q stop calls=%d state=%v err=%v", got, stops, s.State(), err)
			}
			var marked, communicated []string
			raw, err := os.ReadFile(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n"))[1:] { // skip header
				entry, err := transcript.DecodeEntry(line)
				if err != nil {
					t.Fatal(err)
				}
				turn := entry.Turn
				if turn.Kind == schema.TurnAssistant && turn.AcceptedReply {
					marked = append(marked, lastAgentText(turn.Message))
				}
				if turn.Kind == schema.TurnCommunicate && turn.Communicate != nil && turn.AcceptedReply {
					communicated = append(communicated, turn.Communicate.Message)
				}
			}
			if tc.wantAccepted != "" && (len(marked) != 1 || marked[0] != tc.wantAccepted || len(communicated) != 0) {
				t.Fatalf("assistant acceptance=%q, communicate=%q", marked, communicated)
			}
			if tc.wantAccepted == "" && (len(marked) != 0 || len(communicated) != 1 || communicated[0] != tc.want) {
				t.Fatalf("assistant acceptance=%q, communicate=%q", marked, communicated)
			}
		})
	}
}

func TestRootBareTextFallbackHookContextFailureIsNotTerminal(t *testing.T) {
	s := newSession(t, withSteps(func(llm.Request) llm.Response {
		return bareTextResponse("Unaccepted draft", llm.FinishReasonStop)
	}), withStateDir(t.TempDir()))
	controller, err := NewRetirementController(time.Second, RealRetirementClock())
	if err != nil {
		t.Fatal(err)
	}
	controller.phase = "retired"
	installCommunicateStopHook(s, func() string {
		// Deny subsequent hook-context admission, not the inbound user turn.
		s.retirementController.Store(controller)
		return `{"hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"Required context"}}`
	})
	got, err := s.ProcessInput(tripwireCtx(t), "Question", nil)
	s.retirementController.Store(nil)
	if !errors.Is(err, ErrRetirementUnavailable) || got != "" || s.communicated() {
		t.Fatalf("reply=%q, accepted=%t, error=%v", got, s.communicated(), err)
	}
	idx, err := transcriptindex.Open(s.TranscriptPath(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := idx.Close(); err != nil {
			t.Error(err)
		}
	}()
	window, err := idx.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	var draft bool
	for _, candidate := range window.Candidates {
		if candidate.Item.Type != "agentMessage" || candidate.Item.Text != "Unaccepted draft" {
			continue
		}
		draft = true
		obs := candidate.Item.Observation
		if obs != nil && obs.TerminalAccepted != nil && *obs.TerminalAccepted {
			t.Error("replay accepted a failed draft")
		}
	}
	if !draft {
		t.Fatal("failed draft was not recorded")
	}
}

func TestQuietYieldRenamedResultToolHasNoProjectedToolCard(t *testing.T) {
	const name = "respond"
	s := newSession(t, withConfig(SessionConfig{ResultToolName: name}), withSteps(func(llm.Request) llm.Response {
		call := rawCommunicateCall("quiet", `{}`)
		call.Name = name
		return agenttest.ToolCallResponse(call)
	}), withStateDir(t.TempDir()))
	if _, err := s.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	if out, err := s.ProcessInput(tripwireCtx(t), "Wait for work", nil); err != nil || out != "" || !s.quietlyYielded() {
		t.Fatalf("reply=%q, quiet=%t, error=%v", out, s.quietlyYielded(), err)
	}
	requireQuietToolResult(t, s, "quiet")
	reg := apptranscript.NewToolCallRegistry()
	turns, err := apptranscript.ItemTurnsFromFile(s.TranscriptPath(), transcript.DefaultMaxLineBytes, func(entry schema.Turn, id string, i int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(id, i, entry, reg, nil, nil)
		return items
	})
	if err != nil {
		t.Fatal(err)
	}
	check := func(source string, item appwire.ThreadItem) {
		if item.Type == "commandExecution" || item.Type == "agentMessage" {
			t.Errorf("%s: quiet call projected %s (call=%q status=%q)", source, item.Type, item.CallID, item.Status)
		}
	}
	for _, turn := range turns {
		for _, item := range turn.Items {
			check("whole transcript", item)
		}
	}
	idx, err := transcriptindex.Open(s.TranscriptPath(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := idx.Close(); err != nil {
			t.Error(err)
		}
	}()
	window, err := idx.Latest(40)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range window.Candidates {
		check("index", candidate.Item)
	}
}
