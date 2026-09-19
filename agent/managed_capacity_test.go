package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

func TestManagedProjectionBoundUsesEncodedBytes(t *testing.T) {
	// A separately authored projection has its own bound, independent of host
	// data. HTML escaping, not Go string length, determines durable JSON size.
	text := strings.Repeat("<", ((8<<20)-2)/6)
	result := ManagedResult{ModelText: text}
	if err := validateManagedResult(result, ManagedRequest{}, "session"); err != nil {
		t.Fatalf("valid encoded projection: %v", err)
	}
	result.ModelText += "<"
	if err := validateManagedResult(result, ManagedRequest{}, "session"); err == nil {
		t.Fatal("accepted over-limit encoded model projection")
	}
}

func managedCapacityResponse(ids ...string) llm.Response {
	response := llm.Response{Message: llm.Message{Role: llm.RoleAssistant}}
	for index, id := range ids {
		response.Message.Content = append(response.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: id, Name: "managed_write", Arguments: json.RawMessage(fmt.Sprintf(`{"n":%d}`, index+1))}})
	}
	return response
}

func TestManagedBatchUsesIndividualDurableTurns(t *testing.T) {
	f := &managedFixture{}
	response := managedCapacityResponse("first", "second")
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}), withSteps(func(llm.Request) llm.Response { return response }, managedDoneStep))
	if _, err := s.ProcessInput(t.Context(), "run", nil); err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range full.Entries {
		if entry.Turn.Kind == schema.TurnToolResults && hasManagedResults(entry.Turn.Message) {
			count++
			if len(entry.Turn.Message.Content) != 1 {
				t.Fatal("managed batch aggregated into one transcript line")
			}
		}
	}
	if count != 2 || len(s.managedJournal.pending()) != 0 {
		t.Fatalf("turns=%d pending=%d", count, len(s.managedJournal.pending()))
	}
}

func TestManagedChunkFailureRetainsOnlyUnsettledInvocation(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	fs := installManagedTranscriptFault(t, s)
	s.RegisterTool("ordinary", "ordinary fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return "ordinary-output", nil })
	response := managedCapacityResponse("first", "second")
	response.Message.Content = []llm.ContentPart{response.Message.Content[0], {Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "ordinary", Name: "ordinary", Arguments: json.RawMessage(`{}`)}}, response.Message.Content[1]}
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "chunk-round"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	fs.beforeWrite = func(data []byte) {
		entry, err := transcript.DecodeEntry(data)
		if err != nil {
			return
		}
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "second" {
				reached = true
				fs.set(true, true)
			}
		}
	}
	err = s.persistToolResults(t.Context(), response.ToolCalls(), results)
	if !reached || !errors.Is(err, transcript.ErrRetainedUnsynced) {
		t.Fatalf("barrier reached=%t err=%v", reached, err)
	}
	pending := s.managedJournal.pending()
	if len(pending) != 1 || pending[0].CallID != "second" || pending[0].Result == nil {
		t.Fatalf("settled first chunk retained or second lost: pending=%d", len(pending))
	}
	fs.beforeWrite = nil
	fs.set(false, false)
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || len(restored.managedJournal.pending()) != 0 {
		t.Fatal("reopen replayed backend or failed to settle retained chunk")
	}
	full, err := readTranscriptFull(restored.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, entry := range full.Entries {
		for _, part := range entry.Turn.Message.Content {
			if part.ToolResult != nil {
				counts[part.ToolResult.ToolCallID]++
			}
		}
	}
	if counts["first"] != 1 || counts["second"] != 1 || counts["ordinary"] != 1 {
		t.Fatalf("duplicated result: %v", counts)
	}
}

func TestManagedAdmissionReservesWholeResultBeforeDispatch(t *testing.T) {
	f := &managedFixture{executeErr: errors.New("unknown")}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	response := managedCapacityResponse("one", "two", "three", "four", "five")
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "reserved-capacity"}); err != nil {
		t.Fatal(err)
	}
	for index, call := range response.ToolCalls() {
		result := s.execTool(s.managedCallContext(context.Background(), index), call, "")
		if index == 4 && (!result.IsError || result.ManagedInvocationID != "") {
			t.Fatalf("unadmitted capacity result=%+v", result)
		}
	}
	if len(f.requests) != 4 || len(s.managedJournal.pending()) != 4 {
		t.Fatalf("reservation allowed requests=%d pending=%d", len(f.requests), len(s.managedJournal.pending()))
	}
	for _, request := range f.requests {
		if request.InvocationID == "" {
			t.Fatalf("missing identity for %s", request.ToolCallID)
		}
	}
}

func managedPayloadResult(s *Session, source, model string, domainError bool) ManagedResult {
	return ManagedResult{ModelText: model, Host: &llm.MCPResult{Version: 1, Origin: llm.MCPOrigin{BindingID: "logical-binding", ServiceID: "backend", SessionID: s.id}, StructuredContent: json.RawMessage(`{"host":"` + source + `"}`), IsError: domainError}}
}
func managedMaximumTranscriptLine(t *testing.T, path string) int {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReader(file)
	largest := 0
	for {
		line, complete, read, err := transcript.ReadLine(reader, transcript.DefaultMaxLineBytes)
		if err != nil {
			t.Fatal(err)
		}
		if !complete {
			if read != 0 {
				t.Fatal("partial transcript record")
			}
			break
		}
		largest = max(largest, len(line))
	}
	return largest
}
func assertManagedHostPayload(t *testing.T, host *llm.MCPResult, want string) {
	t.Helper()
	if host == nil {
		t.Fatal("lost host payload")
	}
	var payload struct {
		Host string `json:"host"`
	}
	if err := json.Unmarshal(host.StructuredContent, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Host != want {
		t.Fatalf("host changed: bytes=%d want=%d", len(payload.Host), len(want))
	}
}

func TestManagedNearWireBoundaryEscapesAndReopens(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, ToolOutputLimits: map[string]schema.ToolOutputLimit{"managed_write": {MaxChars: 2 << 20}}}))
	source := "host-only:" + strings.Repeat("<", (16<<20)-1024) + "&>\u2028\u2029"
	model := strings.Repeat("<", ((8<<20)-2)/6)
	result := managedPayloadResult(s, source, model, false)
	if len(result.Host.StructuredContent) >= 16<<20 {
		t.Fatal("fixture exceeds unescaped wire envelope")
	}
	f.result = &result
	// Exercise the actual registry/persistence boundary, stopping before another
	// provider call: this intentionally maximal projection exceeds normal model
	// token budgets, whose guard remains unchanged.
	response := managedCallStep(llm.Request{})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "large-initial"}); err != nil {
		t.Fatal(err)
	}
	results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.persistToolResults(t.Context(), response.ToolCalls(), results); err != nil {
		t.Fatal(err)
	}
	if len(s.managedJournal.pending()) != 0 {
		t.Fatal("valid large result remained permanently unknown")
	}
	largest := managedMaximumTranscriptLine(t, s.TranscriptPath())
	if largest <= 100<<20 {
		t.Fatalf("did not exercise expanded host and both model copies: %d", largest)
	}
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, turn := range restored.history {
		for _, part := range turn.Message.Content {
			if r := part.ToolResult; r != nil && r.ManagedInvocationID != "" {
				seen++
				assertManagedHostPayload(t, r.MCPResult, source)
				if r.ManagedModelText != model || r.Content != model {
					t.Fatal("model projection changed")
				}
			}
		}
	}
	if seen != 1 || len(f.requests) != 1 {
		t.Fatalf("results=%d executions=%d", seen, len(f.requests))
	}
	t.Logf("actual near16MiB raw host, largest durable line=%d bytes (<%d)", largest, transcript.DefaultMaxLineBytes)
}

func TestManagedLargeOutcomesRecoverAndResolve(t *testing.T) {
	for _, paired := range []bool{false, true} {
		t.Run(fmt.Sprintf("paired=%t", paired), func(t *testing.T) {
			f := &managedFixture{}
			s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, ToolOutputLimits: map[string]schema.ToolOutputLimit{"managed_write": {MaxChars: 5}}}))
			source := "host-only:" + strings.Repeat("<", (16<<20)-1024)
			result := managedPayloadResult(s, source, strings.Repeat("<", ((8<<20)-2)/6), true)
			if paired {
				f.executeErr = errors.New("lost acknowledgment")
			} else {
				f.result = &result
			}
			response := managedCapacityResponse("recover")
			if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "large-recovery"}); err != nil {
				t.Fatal(err)
			}
			results, err := s.execToolBatch(t.Context(), response.ToolCalls(), s.currentProfile(), "")
			if err != nil {
				t.Fatal(err)
			}
			if paired {
				if err := s.persistToolResults(t.Context(), response.ToolCalls(), results); err != nil {
					t.Fatal(err)
				}
			}
			f.result = &result
			f.executeErr = nil
			restored, err := restoreManagedFixture(t, s, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(restored.managedJournal.pending()) != 0 {
				t.Fatal("valid outcome never settled")
			}
			full, err := readTranscriptFull(restored.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, entry := range full.Entries {
				if r := entry.Turn.ManagedResolution; r != nil {
					found++
					assertManagedHostPayload(t, r.MCPResult, source)
					if !paired || r.ModelText != result.ModelText || !r.MCPResult.IsError {
						t.Fatal("late domain outcome changed")
					}
				}
				for _, part := range entry.Turn.Message.Content {
					if r := part.ToolResult; r != nil && r.MCPResult != nil {
						found++
						assertManagedHostPayload(t, r.MCPResult, source)
						if paired || r.ManagedModelText != result.ModelText || !r.IsError || len(r.Content.(string)) >= len(result.ModelText) {
							t.Fatal("recovered domain outcome changed or bypassed limits")
						}
					}
				}
			}
			if found != 1 {
				t.Fatalf("outcomes=%d", found)
			}
			largest := managedMaximumTranscriptLine(t, restored.TranscriptPath())
			if largest <= 100<<20 {
				t.Fatal("did not exercise expanded boundary host and original projection")
			}
			t.Logf("domain recovery paired=%t: largest line=%d bytes", paired, largest)
			calls := len(f.requests)
			reopened, err := restoreManagedFixture(t, restored, f)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.requests) != calls || len(reopened.managedJournal.pending()) != 0 {
				t.Fatal("settled outcome replayed")
			}
		})
	}
}

func TestManagedMixedChunksKeepDelegateReceiptAndSkillObligation(t *testing.T) {
	root := t.TempDir()
	markGitRoot(t, root)
	writeSkillMD(t, root, "greet", "---\nname: greet\ndescription: Greeting skill\n---\nUse greeting style.\n")
	f := &managedFixture{}
	s := newSession(t, withDir(root), withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	childWriter, err := transcript.NewWriter(filepath.Join(c.stateDir, sessionsSubdir, "child-dlg_target.transcript.jsonl"), transcript.Header{SessionID: "child-dlg_target"})
	if err != nil {
		t.Fatal(err)
	}
	if err := childWriter.Close(); err != nil {
		t.Fatal(err)
	}
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "delivered").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatal(err)
	}
	resolution := <-waiter.resolution
	originalController := s.delegateController
	s.delegateController = c
	c.rootRuntime = s
	defer func() { s.delegateController = originalController; c.rootRuntime = nil }()
	s.queueDelegateDeliveryCommit("delivery", resolution.commit)
	s.RegisterTool("ordinary", "ordinary fixture", map[string]any{"type": "object"}, func(context.Context, any) (any, error) { return "ordinary-output", nil })
	response := managedCapacityResponse("managed")
	skillCall := useSkillCall("skill", "greet")
	response.Message.Content = append(response.Message.Content,
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "delivery", Name: "delegate_send", Arguments: json.RawMessage(`{}`)}},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &skillCall},
		llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "ordinary", Name: "ordinary", Arguments: json.RawMessage(`{}`)}})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "mixed-round"}); err != nil {
		t.Fatal(err)
	}
	calls := response.ToolCalls()
	results := []tool.ExecResult{
		s.execTool(s.managedCallContext(t.Context(), 0), calls[0], ""),
		{CallID: "delivery", ToolName: "delegate_send", Output: `{"status":"completed"}`},
		s.execTool(t.Context(), calls[2], ""), s.execTool(t.Context(), calls[3], ""),
	}
	for _, r := range results {
		if r.IsError {
			t.Fatalf("tool %s failed: %s", r.ToolName, r.Output)
		}
	}
	if err := s.persistToolResults(t.Context(), calls, results); err != nil {
		t.Fatal(err)
	}
	full, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	turns := map[string]schema.Turn{}
	for _, entry := range full.Entries {
		if entry.Turn.Kind == schema.TurnToolResults {
			if len(entry.Turn.Message.Content) != 1 {
				t.Fatal("mixed round remained aggregated")
			}
			turns[entry.Turn.Message.Content[0].ToolResult.ToolCallID] = entry.Turn
		}
	}
	if len(turns) != 4 {
		t.Fatalf("result turns=%d", len(turns))
	}
	delivery := turns["delivery"]
	if len(delivery.DelegateDeliveryCommits) != 1 || delivery.DelegateDeliveryCommits[0].DeliveryID != plan.deliveryID || delivery.DelegateDeliveryCommits[0].ToolCallID != "delivery" {
		t.Fatal("delegate commit did not stay with its result")
	}
	if len(c.durable["dlg_target"].PendingDeliveries) != 0 {
		t.Fatal("durable delivery was not acknowledged")
	}
	skill := turns["skill"].SkillState
	if skill == nil || len(skill.Obligations) != 1 || skill.Obligations[0].ToolCallID != "skill" {
		t.Fatal("skill obligation lost its carrier")
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Skills == nil || len(meta.Skills.Obligations) != 1 || meta.Skills.Obligations[0].InvocationID != skill.Obligations[0].InvocationID {
		t.Fatal("skill carrier lost durable obligation")
	}
	for _, id := range []string{"managed", "ordinary"} {
		if turns[id].SkillState != nil || len(turns[id].DelegateDeliveryCommits) != 0 {
			t.Fatal("unrelated result inherited receipt or skill state")
		}
	}
	if len(s.managedJournal.pending()) != 0 {
		t.Fatal("managed chunk not settled")
	}
}

func TestManagedCanceledLargeBatchRetainsSeparateReceipts(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f, ToolOutputLimits: map[string]schema.ToolOutputLimit{"managed_write": {MaxChars: 2 << 20}}}))
	fs := installManagedTranscriptFault(t, s)
	source := strings.Repeat("<", 8<<20)
	result := managedPayloadResult(s, source, strings.Repeat("<", ((8<<20)-2)/6), false)
	f.result = &result
	response := managedCapacityResponse("cancel-first", "cancel-second")
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "large-canceled"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	completed := 0
	f.afterExecute = func() {
		completed++
		if completed == 2 {
			cancel()
		}
	}
	reached := false
	fs.beforeWrite = func(data []byte) {
		entry, err := transcript.DecodeEntry(data)
		if err != nil {
			return
		}
		for _, part := range entry.Turn.Message.Content {
			if r := part.ToolResult; r != nil && r.ToolCallID == "cancel-second" {
				reached = true
				fs.set(true, true)
			}
		}
	}
	_, err := s.execToolBatch(ctx, response.ToolCalls(), s.currentProfile(), "")
	if !errors.Is(err, context.Canceled) || !reached || fs.syncFailures == 0 {
		t.Fatalf("cancellation barrier err=%v reached=%t sync failures=%d", err, reached, fs.syncFailures)
	}
	if len(s.managedJournal.pending()) != 2 {
		t.Fatal("canceled result lost pending receipts")
	}
	fs.beforeWrite = nil
	fs.set(false, false)
	f.afterExecute = nil
	largest := managedMaximumTranscriptLine(t, s.TranscriptPath())
	restored, err := restoreManagedFixture(t, s, f)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, turn := range restored.history {
		for _, part := range turn.Message.Content {
			if r := part.ToolResult; r != nil && r.ManagedInvocationID != "" {
				counts[r.ToolCallID]++
				assertManagedHostPayload(t, r.MCPResult, source)
				if r.IsError || r.ManagedModelText != result.ModelText {
					t.Fatal("cancellation manufactured failure or lost original projection")
				}
			}
		}
	}
	if counts["cancel-first"] != 1 || counts["cancel-second"] != 1 || len(f.requests) != 2 || len(restored.managedJournal.pending()) != 0 {
		t.Fatalf("canceled recovery counts=%v executions=%d", counts, len(f.requests))
	}
	t.Logf("canceled/retained managed batch: largest line=%d bytes", largest)
}

func TestManagedEarlyChunkFailureAbortsLaterDelegateCommit(t *testing.T) {
	f := &managedFixture{}
	s := newSession(t, withConfig(SessionConfig{StateDir: t.TempDir(), ForceRealIO: true, ManagedRuntime: f}))
	c, path := newDelegateControllerTestHarness(t, 1, 1)
	seedDelegateControllerIdle(t, c, "dlg_target", "")
	lease, waiter := startDelegateDeliveryGeneration(t, c, "dlg_target", true)
	plan := finishDelegateDeliveryGeneration(t, c, lease, "delivered").deliveries[0]
	if _, err := deliverDelegatePacket(plan, nil); err != nil {
		t.Fatal(err)
	}
	resolution := <-waiter.resolution
	s.queueDelegateDeliveryCommit("later-delivery", resolution.commit)
	fs := installManagedTranscriptFault(t, s)
	response := managedCapacityResponse("managed")
	response.Message.Content = append(response.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "later-delivery", Name: "delegate_send", Arguments: json.RawMessage(`{}`)}})
	if err := s.appendAssistantTurn(response, ModelAttemptMetadata{AttemptGroupID: "abort-later-commit"}); err != nil {
		t.Fatal(err)
	}
	calls := response.ToolCalls()
	results := []tool.ExecResult{s.execTool(s.managedCallContext(t.Context(), 0), calls[0], ""), {CallID: "later-delivery", ToolName: "delegate_send", Output: `{"status":"completed"}`}}
	fs.set(true, true)
	err := s.persistToolResults(t.Context(), calls, results)
	fs.set(false, false)
	if !errors.Is(err, transcript.ErrRetainedUnsynced) {
		t.Fatalf("expected reached retained failure, got %v", err)
	}
	s.delegateDeliveryMu.Lock()
	left := len(s.delegateDeliveryCommits)
	s.delegateDeliveryMu.Unlock()
	if left != 0 {
		t.Fatal("failed earlier chunk stranded later delegate commit")
	}
	if len(c.durable["dlg_target"].PendingDeliveries) != 1 {
		t.Fatal("unwritten delegate delivery was acknowledged")
	}
	restarted := restartDelegateDeliveryController(t, c, path)
	replay := restarted.ReplayDeliveries()
	if len(replay) != 1 || replay[0].deliveryID != plan.deliveryID || replay[0].waiter != nil {
		t.Fatal("unwritten durable delivery head did not replay after reopen")
	}
}
