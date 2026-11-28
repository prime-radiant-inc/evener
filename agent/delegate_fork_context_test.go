package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// Only an explicit opt-in may send the parent's conversation to a delegate.
// Completed tool exchanges must survive, while the in-flight spawn must not.
func TestDelegateForkContext_OptInHistory(t *testing.T) {
	t.Parallel()
	for _, option := range []any{nil, false, true} {
		name := "default"
		if option != nil {
			name = map[bool]string{false: "clean", true: "fork"}[option.(bool)]
		}
		t.Run(name, func(t *testing.T) {
			root, client, _ := newDelegateResourceBootstrapSession(t)
			adapter := newTask6FrozenDescriptorAdapter()
			client.Register(adapter)
			t.Cleanup(adapter.releaseRun)
			root.appendTurn(schema.TurnUserInput, llm.User("parent-context-sentinel"))
			root.appendTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentImage, Image: &llm.ImageData{Data: []byte("image-sentinel"), MediaType: "image/png"}}}})
			completed := schema.NewTurn(schema.TurnAssistant, forkContextToolCall("completed", "read_file"))
			completed.ResponseID = "parent-response-anchor"
			completed.Usage = llm.Usage{TotalTokens: 999}
			root.recordTurn(completed, completed)
			root.appendTurn(schema.TurnTool, llm.ToolResult("completed", "parent-result-sentinel", true))
			root.appendTurn(schema.TurnUserInput, llm.User("current-input-sentinel"))
			root.appendTurn(schema.TurnAssistant, forkContextToolCall("pending-spawn", "delegate"))
			// A model setting can change while a tool is still executing.
			root.appendTurn(schema.TurnModelSwitch, llm.System("model-marker-sentinel"))

			params := map[string]any{"prompt": "child-assignment-sentinel", "delegation_allowance": float64(0)}
			if option != nil {
				params["fork_context"] = option
			}
			args, err := decodeDelegateArgs(params)
			if err != nil {
				t.Fatal(err)
			}
			result := root.createDelegate(context.Background(), args)
			if result.Err != nil {
				t.Fatal(result.Err)
			}
			var request llm.Request
			select {
			case request = <-adapter.entered:
			case <-time.After(10 * time.Second): // TRIPWIRE: the scripted provider signals the first request.
				t.Fatal("child did not issue a request")
			}
			wantHistory := option == true
			for _, marker := range []string{"parent-context-sentinel", "current-input-sentinel"} {
				if got := requestContainsText(request, marker); got != wantHistory {
					t.Errorf("inherited %s = %v, want %v", marker, got, wantHistory)
				}
			}
			if !requestContainsText(request, "child-assignment-sentinel") {
				t.Error("child assignment missing")
			}
			foundToolResult := false
			foundImage := false
			for _, message := range request.Messages {
				for _, part := range message.Content {
					if part.Image != nil && string(part.Image.Data) == "image-sentinel" {
						foundImage = true
					}
					if part.ToolResult != nil && part.ToolResult.ToolCallID == "completed" && part.ToolResult.Content == "parent-result-sentinel" {
						foundToolResult = true
					}
					if part.ToolCall != nil && part.ToolCall.ID == "pending-spawn" {
						t.Error("unfinished spawn call reached the child")
					}
				}
			}
			if foundToolResult != wantHistory {
				t.Errorf("inherited completed tool result = %v, want %v", foundToolResult, wantHistory)
			}
			if foundImage != wantHistory {
				t.Errorf("inherited image = %v, want %v", foundImage, wantHistory)
			}
			child := root.getSub(result.ChildSessionID).sess
			meta := child.Meta()
			if !meta.IsSubagent || meta.OriginalPrompt != "child-assignment-sentinel" || meta.CumulativeUsage.TotalTokens != 0 {
				t.Errorf("child identity or accounting inherited: %+v", meta)
			}
			if child.reg.Get("ask_user") != nil || child.reg.Get("delegate") != nil {
				t.Error("leaf child gained parent tools")
			}
			if failures, measured := child.FailedToolCallsSnapshot(); !measured || failures != 0 {
				t.Errorf("child charged for parent's tool failure: %d, measured=%v", failures, measured)
			}
			data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, child.id), "")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range data.Entries {
				if entry.Turn.ResponseID == "parent-response-anchor" {
					t.Error("parent continuation anchor copied into child")
				}
			}
		})
	}
}

func TestDelegateForkContext_DescendantsStartClean(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	root.appendTurn(schema.TurnUserInput, llm.User("ancestor-context-sentinel"))
	grandchild := newTask6FrozenDescriptorAdapter()
	t.Cleanup(grandchild.releaseRun)
	var constructed atomic.Int32
	root.cfg.testOnly.childClientFactory = func() *llm.Client {
		client := llm.NewClient()
		if constructed.Add(1) == 1 {
			client.Register(newFakeAdapter("openai", func(llm.Request) llm.Response {
				message := forkContextToolCall("nested-spawn", "delegate")
				message.Content[0].ToolCall.Arguments = []byte(`{"prompt":"grandchild-unit-sentinel","delegation_allowance":0}`)
				return llm.Response{Message: message}
			}, func(llm.Request) llm.Response { return agenttest.FinalResponse("child finished") }))
		} else {
			client.Register(grandchild)
		}
		return client
	}
	result := root.createDelegate(context.Background(), delegateArgs{Task: "middle-assignment-sentinel", ForkContext: true, DelegationAllowance: new(1)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	select {
	case req := <-grandchild.entered:
		if requestContainsText(req, "ancestor-context-sentinel") || requestContainsText(req, "middle-assignment-sentinel") || !requestContainsText(req, "grandchild-unit-sentinel") {
			t.Fatal("grandchild inherited context without opting in")
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: real delegate dispatch must reach the scripted grandchild provider.
		t.Fatal("grandchild did not issue a request")
	}
}

func TestDelegateForkContext_RejectsMismatchedSourceTranscript(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	if err := root.closeAttachedTranscript(); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(transcriptPath(root.stateDir, root.id), transcript.Header{SessionID: identifier.MustNewSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.snapshotDelegateContext(); err == nil {
		t.Fatal("fork accepted another session's transcript")
	}
}

// A cold delegate resumes from its own saved snapshot, even after its parent
// has continued. Parent compaction controls the working context but must not
// erase the earlier conversation from the child's transcript.
func TestDelegateForkContext_ColdResumePreservesSnapshot(t *testing.T) {
	t.Parallel()
	root, client, _ := newDelegateResourceBootstrapSession(t)
	root.appendTurn(schema.TurnUserInput, llm.User("archived-parent-sentinel"))
	root.appendTurn(schema.TurnCheckpoint, llm.User("parent-summary-sentinel"))
	root.appendTurn(schema.TurnUserInput, llm.User("recent-parent-sentinel"))
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response { return agenttest.FinalResponse("child-result-sentinel") })
	client.Register(adapter)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "child-unit-sentinel", ForkContext: true, DelegationAllowance: new(0)})
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	sub := root.getSub(result.ChildSessionID)
	sub.mu.Lock()
	done := sub.done
	sub.mu.Unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: scripted completion; synchronization is the closed run channel.
		t.Fatal("child did not complete")
	}
	root.delegateController.mu.Lock()
	root.delegateController.maxRetainedTerminal = 1
	root.delegateController.mu.Unlock()
	if err := root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatal(err)
	}
	if root.getSub(result.ChildSessionID) != nil {
		t.Fatal("child runtime was not reclaimed")
	}
	root.appendTurn(schema.TurnUserInput, llm.User("after-fork-sentinel"))
	resumedAdapter := newTask6FrozenDescriptorAdapter()
	client.Register(resumedAdapter)
	t.Cleanup(resumedAdapter.releaseRun)
	outcome := (delegateRuntime{owner: root}).send(context.Background(), result.DelegateID, "followup-sentinel", 0)
	if outcome.result.Err != nil {
		t.Fatal(outcome.result.Err)
	}
	var req llm.Request
	select {
	case req = <-resumedAdapter.entered:
	case <-time.After(10 * time.Second): // TRIPWIRE: the restored runtime must reach the scripted provider.
		t.Fatal("resumed child did not issue a request")
	}
	for _, marker := range []string{"parent-summary-sentinel", "recent-parent-sentinel", "child-unit-sentinel", "followup-sentinel"} {
		if !requestContainsText(req, marker) {
			t.Errorf("missing resumed context %s", marker)
		}
	}
	for _, marker := range []string{"archived-parent-sentinel", "after-fork-sentinel"} {
		if requestContainsText(req, marker) {
			t.Errorf("unexpected context %s", marker)
		}
	}
	child := root.getSub(result.ChildSessionID).sess
	meta := child.Meta()
	if !meta.IsSubagent || meta.DivergenceTurn == 0 || meta.OriginalPrompt != "child-unit-sentinel" || child.reg.Get("ask_user") != nil {
		t.Fatalf("restored delegate lost identity or gained permissions: %+v", meta)
	}
	data, err := readTranscriptFull(context.Background(), transcriptPath(root.stateDir, child.id), "")
	if err != nil {
		t.Fatal(err)
	}
	foundArchive := false
	for _, entry := range data.Entries {
		if entry.Turn.Message.Text() == "archived-parent-sentinel" {
			foundArchive = true
		}
	}
	if !foundArchive {
		t.Fatal("fork discarded the parent's archived conversation")
	}
}

func TestDelegateForkContext_RejectsDifferentModelBeforeCreation(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	result := root.createDelegate(context.Background(), delegateArgs{Task: "unit", ForkContext: true, Model: "gpt-5.5"})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "fork_context") || result.DelegateID != "" {
		t.Fatalf("different-model fork = %+v", result)
	}
}

// A direct resume has no live parent spawn config. It must still identify the
// delegate by its assignment rather than the first inherited user message.
func TestDelegateForkContext_DirectResumeKeepsAssignment(t *testing.T) {
	t.Parallel()
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	meta.IsSubagent = true
	meta.ParentSessionID = identifier.MustNewSessionID()
	meta.DivergenceTurn = 2
	meta.OriginalPrompt = "assigned-unit-sentinel"
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(transcriptPath(stateDir, meta.ID), transcript.Header{SessionID: meta.ID, ParentSessionID: meta.ParentSessionID, Task: meta.OriginalPrompt})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"parent-request-sentinel", "assigned-unit-sentinel"} {
		if _, err := writer.Record(schema.NewTurn(schema.TurnUserInput, llm.User(input)), transcript.RecordOptions{Door: transcript.DoorBuffered}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.Meta(); !got.IsSubagent || got.OriginalPrompt != "assigned-unit-sentinel" {
		t.Fatalf("directly resumed delegate identity = %+v", got)
	}
}

func TestDelegateForkContext_SchemaOffersOptionalBoolean(t *testing.T) {
	t.Parallel()
	root, _, _ := newDelegateResourceBootstrapSession(t)
	definition := root.delegateToolDefinition()
	property, ok := definition.Parameters["properties"].(map[string]any)["fork_context"].(map[string]any)
	if !ok || property["type"] != "boolean" || property["default"] != false {
		t.Fatalf("fork_context schema = %#v", property)
	}
	for _, required := range definition.Parameters["required"].([]string) {
		if required == "fork_context" {
			t.Fatal("clean-session callers must not need to supply fork_context")
		}
	}
}

func TestDelegateForkContext_RejectsMalformedOption(t *testing.T) {
	t.Parallel()
	for _, option := range []any{"true", 1, nil, []any{true}} {
		if _, err := decodeDelegateArgs(map[string]any{"prompt": "unit", "fork_context": option}); err == nil {
			t.Errorf("accepted fork_context=%#v", option)
		}
	}
}

func forkContextToolCall(id, name string) llm.Message {
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{
		Kind:     llm.ContentToolCall,
		ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: []byte(`{}`), Type: "function"},
	}}}
}

// A forked delegate inherits the parent's quiet rounds as conversation. The
// assistant entry keeps its quiet flags so the child's projection hides the
// companion prose and mutes the bare-text quiet park, while the model still
// sees that text in the inherited message.
func TestDelegateContextPreservesQuietCompanionAndQuietYield(t *testing.T) {
	t.Parallel()
	const (
		companionProse = "COMPANION PROSE SENTINEL"
		bareProse      = "BARE QUIET PARK SENTINEL"
		commentary     = "QUIET COMMENTARY SENTINEL"
		note           = "Waiting on CI"
	)
	yes := true
	quietState, err := json.Marshal(schema.CommunicateToolState{
		Accepted: &yes, EndTurn: &yes, Quiet: &yes,
		QuietNote: note, QuietCommentary: commentary,
	})
	if err != nil {
		t.Fatal(err)
	}
	author := &schema.TranscriptAuthorship{Author: schema.TranscriptAuthor{Kind: schema.TranscriptAuthorAgent, SessionID: "parent-session"}}
	ts := time.Unix(1_790_000_000, 0).UTC()
	parent := []schema.Turn{
		{Format: schema.TurnFormatIdentity, TurnID: "turn_quiet", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Authorship: author.Clone(), Message: llm.User("start"), Timestamp: ts},
		{Format: schema.TurnFormatIdentity, TurnID: "turn_quiet", RoundID: "r1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnAssistant, Authorship: author.Clone(), QuietCompanion: true, Timestamp: ts.Add(time.Second), Message: llm.Message{
			Role: llm.RoleAssistant,
			Content: []llm.ContentPart{
				{Kind: llm.ContentText, Text: companionProse},
				{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_quiet", Name: "communicate", Arguments: json.RawMessage(`{"message":"Waiting","next":"log_quietly_and_end_turn"}`)}},
			},
		}},
		{Format: schema.TurnFormatIdentity, TurnID: "turn_quiet", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnToolResults, Authorship: author.Clone(), Timestamp: ts.Add(2 * time.Second), Message: llm.Message{
			Role: llm.RoleTool,
			Content: []llm.ContentPart{{
				Kind:       llm.ContentToolResult,
				ToolResult: &llm.ToolResultData{ToolCallID: "call_quiet", Name: "communicate", Content: `{"accepted":true,"end_turn":true}`, ToolState: quietState},
			}},
		}},
		{Format: schema.TurnFormatIdentity, TurnID: "turn_park", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnUserInput, Authorship: author.Clone(), Message: llm.User("next"), Timestamp: ts.Add(3 * time.Second)},
		{Format: schema.TurnFormatIdentity, TurnID: "turn_park", RoundID: "r1", TurnKind: schema.TurnSpanExecution, Kind: schema.TurnAssistant, Authorship: author.Clone(), QuietYield: true, Timestamp: ts.Add(4 * time.Second), Message: llm.Assistant(bareProse)},
	}

	inherited := delegateContextEntries(entriesOf(parent))
	if len(inherited) != len(parent) {
		t.Fatalf("inherited %d entries, want %d completed rounds", len(inherited), len(parent))
	}
	companionTurn := inherited[1].Turn
	parkTurn := inherited[4].Turn
	if !companionTurn.QuietCompanion {
		t.Fatalf("inherited quiet companion turn lost QuietCompanion: %+v", companionTurn)
	}
	if !parkTurn.QuietYield {
		t.Fatalf("inherited bare-text quiet park lost QuietYield: %+v", parkTurn)
	}
	if got := forkContextMessageText(companionTurn.Message); !strings.Contains(got, companionProse) {
		t.Fatalf("inherited quiet companion message text %q does not carry the prose for model context", got)
	}
	if got := forkContextMessageText(parkTurn.Message); !strings.Contains(got, bareProse) {
		t.Fatalf("inherited quiet park message text %q does not carry the prose for model context", got)
	}

	reg := apptranscript.NewToolCallRegistry()
	var items []appwire.ThreadItem
	for i, entry := range inherited {
		partItems, _ := apptranscript.ProjectEntryParts("parent-session", i, entry.Turn, reg, nil, nil)
		items = append(items, partItems...)
	}
	var quiets []appwire.ThreadItem
	for _, item := range items {
		if item.Type == "agentMessage" && (strings.Contains(item.Text, companionProse) || strings.Contains(item.Text, bareProse)) {
			t.Fatalf("inherited quiet prose rendered as a chat bubble: %+v", item)
		}
		if item.EventKind == appwire.ThreadItemEventKindQuietYield {
			quiets = append(quiets, item)
		}
	}
	if len(quiets) != 2 {
		t.Fatalf("quiet items=%+v, want the companion notice and the bare-text park", quiets)
	}
	var companionNotice, parkNotice *appwire.ThreadItem
	for i := range quiets {
		switch quiets[i].CallID {
		case "call_quiet":
			companionNotice = &quiets[i]
		case "":
			parkNotice = &quiets[i]
		}
	}
	if companionNotice == nil || companionNotice.Text != note {
		t.Fatalf("companion notice=%+v, want quiet yield carrying note %q", companionNotice, note)
	}
	var payload struct {
		Commentary string `json:"commentary"`
	}
	if err := json.Unmarshal(companionNotice.Raw, &payload); err != nil {
		t.Fatalf("decode companion notice raw %s: %v", companionNotice.Raw, err)
	}
	if payload.Commentary != commentary {
		t.Fatalf("companion notice commentary=%q, want stamped %q", payload.Commentary, commentary)
	}
	if parkNotice == nil || parkNotice.Text != bareProse {
		t.Fatalf("park notice=%+v, want muted quiet yield carrying %q", parkNotice, bareProse)
	}
}

func forkContextMessageText(message llm.Message) string {
	var texts []string
	for _, part := range message.Content {
		if part.Kind == llm.ContentText {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}
