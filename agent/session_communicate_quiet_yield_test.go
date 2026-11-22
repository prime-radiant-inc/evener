package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func quietYieldResponse(id, args string) llm.Response {
	return agenttest.ToolCallResponse(rawCommunicateCall(id, args))
}

func rawCommunicateCall(id, args string) llm.ToolCallData {
	return llm.ToolCallData{ID: id, Name: "communicate", Arguments: json.RawMessage(args), Type: "function"}
}

func communicateToolResult(t *testing.T, s *Session, id string) *llm.ToolResultData {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if result, ok := findToolResultInHistory(s.history, id); ok {
		return result
	}
	t.Fatalf("no tool result for %q", id)
	return nil
}

func requireQuietToolResult(t *testing.T, s *Session, id string) {
	t.Helper()
	res := communicateToolResult(t, s, id)
	var state struct {
		Accepted bool `json:"communicate_accepted"`
		EndTurn  bool `json:"communicate_end_turn"`
		Quiet    bool `json:"communicate_quiet"`
	}
	if err := json.Unmarshal(res.ToolState, &state); err != nil || res.IsError || !state.Accepted || !state.EndTurn || !state.Quiet {
		t.Fatalf("quiet tool result: IsError=%v, state=%+v, decode=%v", res.IsError, state, err)
	}
}

func TestCommunicateSchemaIsUniformAcrossRoles(t *testing.T) {
	t.Parallel()
	child := SessionConfig{}
	child.spawn.depth = 1
	child.spawn.parentSessionID = "parent-session"
	for _, cfg := range []SessionConfig{{}, {NonInteractive: true}, child} {
		s := newAskTestSession(t, cfg)
		found := false
		for _, def := range s.ToolDefinitions() {
			if def.Name != "communicate" {
				continue
			}
			found = true
			props, _ := def.Parameters["properties"].(map[string]any)
			if len(props) != 3 || props["message"] == nil || props["end_turn"] == nil || props["output"] == nil {
				t.Fatalf("advertised properties = %#v, want only message, end_turn, output", props)
			}
			for key, want := range map[string]string{"message": "string", "end_turn": "boolean", "output": "object"} {
				prop, _ := props[key].(map[string]any)
				if prop["type"] != want || (key == "end_turn" && prop["default"] != true) {
					t.Fatalf("property %s = %#v, want type %s and end_turn default true", key, prop, want)
				}
			}
		}
		if !found {
			t.Fatal("communicate not advertised")
		}
	}
}

func TestCommunicateQuietYieldRoot(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		args      string
		kind      EntryKind
		job       bool
		watch     bool
		oneShot   bool
		wantQuiet bool
		wantError bool
	}{
		{name: "final reply defaults to ending", args: `{"message":"done"}`, kind: EntryUserInput},
		{name: "no wake source", args: `{}`, kind: EntryUserInput, wantError: true},
		{name: "self timer", args: `{}`, kind: EntryUserInput, watch: true, wantQuiet: true},
		{name: "one-shot self timer refused", args: `{}`, kind: EntryUserInput, watch: true, oneShot: true, wantError: true},
		{name: "managed shell", args: `{}`, kind: EntryUserInput, job: true, wantQuiet: true},
		{name: "one-shot managed shell accepted", args: `{}`, kind: EntryUserInput, job: true, oneShot: true, wantQuiet: true},
		{name: "legacy empty output", args: `{"message":"","output":{"message":"","data":{},"artifacts":[]}}`, kind: EntryUserInput, job: true, wantQuiet: true},
		{name: "last notification", args: `{}`, kind: EntryNotification, wantQuiet: true},
		{name: "message required for updates", args: `{"end_turn":false}`, kind: EntryUserInput, wantError: true},
		{name: "message required for structured reply", args: `{"output":{"data":{"answer":1}}}`, kind: EntryUserInput, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := newHeldJobEnvironment(dir)
			steps := []func(llm.Request) llm.Response{}
			if tc.job {
				steps = append(steps, func(llm.Request) llm.Response {
					return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
				})
			}
			steps = append(steps, func(llm.Request) llm.Response { return quietYieldResponse("probe", tc.args) }, func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") })
			adapter := newFakeAdapter("openai", steps...)
			s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{TurnEndsProcess: tc.oneShot}))
			t.Cleanup(env.releaseJob)
			var stops atomic.Int32
			installCommunicateStopHook(s, func() string { stops.Add(1); return `{}` })
			if tc.watch {
				if _, err := s.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600}); err != nil {
					t.Fatal(err)
				}
			}
			evs, mu, done := collectEvents(s)
			input := "start"
			if tc.kind == EntryNotification {
				input = ""
				enqueueCompletedJobNotification(t, s, "last-job")
			}
			ctx := tripwireCtx(t)
			if _, err := s.ProcessInputKind(ctx, input, nil, tc.kind); err != nil {
				t.Fatal(err)
			}
			res := communicateToolResult(t, s, "probe")
			if res.IsError != tc.wantError {
				t.Errorf("probe IsError=%v, want %v", res.IsError, tc.wantError)
			}
			if tc.wantQuiet {
				requireQuietToolResult(t, s, "probe")
				if s.communicated() || !s.quietlyYielded() {
					t.Fatal("quiet turn captured a report or failed to mark the quiet boundary")
				}
			}
			if got := s.State(); got != SessionIdle {
				t.Errorf("state=%s, want idle", got)
			}
			wantRequests, wantReports := 1, 1
			if tc.job {
				wantRequests++
			}
			if tc.wantError {
				wantRequests++
			}
			if tc.wantQuiet {
				wantReports = 0
			}
			if got := int(stops.Load()); got != wantReports {
				t.Errorf("Stop hook calls=%d, want %d", got, wantReports)
			}
			if got := len(adapter.Requests()); got != wantRequests {
				t.Errorf("provider requests=%d, want %d", got, wantRequests)
			}
			env.releaseJob()
			s.Close()
			turns := len(entriesOfKind(transcriptTurnsOf(t, s), schema.TurnCommunicate))
			<-done
			mu.Lock()
			defer mu.Unlock()
			count := 0
			for _, ev := range *evs {
				if ev.Kind == events.EventCommunicate {
					count++
				}
			}
			if count != wantReports || turns != wantReports {
				t.Errorf("EventCommunicate=%d, TurnCommunicate=%d, want %d of each", count, turns, wantReports)
			}
		})
	}
}

func TestCommunicateQuietYieldKeepsPendingQuestionAndGatesNotifications(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(askUserCall("ask", askUserArgsValid()), rawCommunicateCall("quiet", `{}`))
	}, func(llm.Request) llm.Response {
		return agenttest.FinalResponse("unexpected extra request")
	})
	s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
	var stops atomic.Int32
	installCommunicateStopHook(s, func() string {
		stops.Add(1)
		return `{}`
	})
	ctx := tripwireCtx(t)
	if _, err := s.ProcessInput(ctx, "ask me", nil); err != nil {
		t.Fatal(err)
	}
	if s.State() != SessionAwaiting || !s.HasPendingAsk() {
		t.Fatal("fixture has no pending question")
	}
	// A pending ask gates this wake before sampling; it must not call Stop or
	// displace the existing awaiting state.
	enqueueCompletedJobNotification(t, s, "ask-job")
	if _, err := s.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
		t.Fatal(err)
	}
	requireQuietToolResult(t, s, "quiet")
	if s.State() != SessionAwaiting || !s.HasPendingAsk() || stops.Load() != 0 || len(adapter.Requests()) != 1 {
		t.Fatalf("state=%s, has ask=%v, Stop calls=%d, provider requests=%d; want awaiting, pending ask, no Stop calls, one request", s.State(), s.HasPendingAsk(), stops.Load(), len(adapter.Requests()))
	}
}

func installCommunicateStopHook(s *Session, response func() string) {
	hookClient := llm.NewClient()
	hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant(response())}
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookStop, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "Check the reply"})
	s.hookRunner = runner
}

func TestCommunicateCustomSchemaUsesPresenceNotTruthiness(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ typ, value string }{{"boolean", "false"}, {"integer", "0"}, {"null", "null"}} {
		t.Run(tc.typ, func(t *testing.T) {
			t.Parallel()
			profile := WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), map[string]any{"type": tc.typ})
			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
				return quietYieldResponse("progress", `{"message":"working","end_turn":false}`)
			}, func(llm.Request) llm.Response {
				return quietYieldResponse("structured-only", `{"output":`+tc.value+`}`)
			}, func(llm.Request) llm.Response {
				return quietYieldResponse("final", `{"message":"done","output":`+tc.value+`}`)
			})
			s := newSession(t, withAdapter(adapter), withProfile(profile))
			if reply, err := s.ProcessInput(tripwireCtx(t), "start", nil); err != nil || reply != "done" {
				t.Fatalf("reply=%q, err=%v, want done", reply, err)
			}
			if communicateToolResult(t, s, "progress").IsError || !communicateToolResult(t, s, "structured-only").IsError || communicateToolResult(t, s, "final").IsError {
				t.Fatal("omitted progress output and present final output must pass, and structured-only reply must fail")
			}
			if out, err := json.Marshal(s.communicateStructured()); err != nil || s.communicateStructured() == nil || string(out) != tc.value {
				t.Fatalf("captured output=%s, err=%v, want present %s", out, err, tc.value)
			}
		})
	}
}

func TestCommunicateExplicitDefaultOutputSchema(t *testing.T) {
	t.Parallel()
	defaultOutput := tool.DefCommunicate().Parameters["properties"].(map[string]any)["output"].(map[string]any)
	for _, tc := range []struct {
		name, args  string
		wantError   bool
		wantQuiet   bool
		wantFinal   bool
		wantOutput  string
		copyBinding bool
	}{
		{name: "final without output", args: `{"message":"done"}`, wantError: true},
		{name: "copied binding rejects final without output", args: `{"message":"done"}`, wantError: true, copyBinding: true},
		{name: "progress without output", args: `{"message":"working","end_turn":false}`},
		{name: "quiet notification", args: `{}`, wantQuiet: true},
		{name: "final with empty object", args: `{"message":"done","output":{}}`, wantFinal: true, wantOutput: `{}`},
		{name: "output-only message", args: `{"output":{"message":"nested"}}`, wantFinal: true, wantOutput: `{"message":"nested"}`},
		{name: "blank top-level message uses output.message", args: `{"message":"   ","output":{"message":"nested report","data":{"ok":true}}}`, wantFinal: true, wantOutput: `{"data":{"ok":true},"message":"nested report"}`},
		{name: "blank top-level and blank output.message rejected", args: `{"message":"   ","output":{"message":"   "}}`, wantError: true},
		{name: "output encoded as string", args: `{"message":"done","output":"{}"}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), defaultOutput)
			if tc.copyBinding {
				profile = NewOpenAIProfile("gpt-5.2").WithCommunicateOverridesFrom(profile)
			}
			s := newSession(t, withProfile(profile))
			ctx := context.WithValue(t.Context(), communicateEntryKindKey{}, EntryNotification)
			res := s.execTool(ctx, rawCommunicateCall("explicit-default-schema", tc.args), "")
			if res.IsError != tc.wantError {
				t.Fatalf("IsError=%v, want %v; ToolState=%s", res.IsError, tc.wantError, res.ToolState)
			}
			if gotQuiet, gotFinal := s.quietlyYielded(), s.communicated(); gotQuiet != tc.wantQuiet || gotFinal != tc.wantFinal {
				t.Fatalf("quiet=%v, final=%v; want quiet=%v, final=%v", gotQuiet, gotFinal, tc.wantQuiet, tc.wantFinal)
			}
			if tc.wantOutput != "" {
				if got, err := json.Marshal(s.communicateStructured()); err != nil || string(got) != tc.wantOutput {
					t.Fatalf("captured output=%s, err=%v, want %s", got, err, tc.wantOutput)
				}
			}
		})
	}
}

func TestCommunicateOptionalOutputEmitsNoRepairNotice(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args string
	}{
		{name: "message only", args: `{"message":"done"}`},
		{name: "progress without output", args: `{"message":"working","end_turn":false}`},
		{name: "partial default output", args: `{"message":"done","output":{"data":{"sum":1000}}}`},
		{name: "empty default output", args: `{"message":"done","output":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withStateDir(t.TempDir()))
			repairedCh := drainRepairedEvents(s)
			res := s.execTool(t.Context(), rawCommunicateCall("valid-"+tc.name, tc.args), "")
			if res.IsError {
				t.Fatalf("execTool(%s) failed: %s", tc.args, res.FullOutput)
			}
			s.Close()
			if repaired := <-repairedCh; len(repaired) != 0 {
				t.Fatalf("schema-valid communicate(%s) emitted spurious repair notice: %+v", tc.args, repaired)
			}
		})
	}
}

func TestCommunicateQuietYieldPreservesInFlightClientSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := newHeldJobEnvironment(dir)
	var s *Session
	steered := false
	steerDelivered := make(chan struct{})
	adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
	}, func(llm.Request) llm.Response {
		if !steered {
			steered = true
			if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
				ClientMutationID: "steer-during-quiet",
				Input:            []appwire.InputItem{{Type: "text", Text: "STEER_DURING_QUIET_SENTINEL"}},
			}); err != nil {
				t.Errorf("AcceptClientMutationSteer: %v", err)
			}
		}
		return quietYieldResponse("quiet-yield", `{}`)
	}, func(req llm.Request) llm.Response {
		if requestContainsText(req, "STEER_DURING_QUIET_SENTINEL") {
			close(steerDelivered)
		}
		return agenttest.FinalResponse("STEER_ACTED_ON")
	})
	s = newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
	t.Cleanup(env.releaseJob)
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}

	ctx := tripwireCtx(t)
	if _, err := s.ProcessInput(ctx, "launch work", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-steerDelivered:
	case <-ctx.Done():
		t.Fatal("in-flight client steer during quiet yield did not trigger a carrier model turn while background job remained held")
	}
}

func TestCommunicateQuietYieldAfterBlockedStopHookDoesNotAcceptBlockedReply(t *testing.T) {
	t.Parallel()
	t.Run("quiet_yield_with_live_work_leaves_blocked_reply_unaccepted", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{ID: "bg-job", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
		}, func(llm.Request) llm.Response {
			return quietYieldResponse("early-reply", `{"message":"done too soon"}`)
		}, func(llm.Request) llm.Response {
			return quietYieldResponse("quiet-wait", `{}`)
		})
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()), withConfig(SessionConfig{TurnEndsProcess: true}))
		t.Cleanup(env.releaseJob)

		var stopCalls atomic.Int32
		installCommunicateStopHook(s, func() string {
			if stopCalls.Add(1) == 1 {
				return `{"decision":"block","reason":"wait for running job"}`
			}
			return `{}`
		})

		ctx := tripwireCtx(t)
		reply, err := s.ProcessInput(ctx, "run and report", nil)
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		requireQuietToolResult(t, s, "quiet-wait")
		if got := stopCalls.Load(); got != 1 {
			t.Fatalf("Stop hook calls = %d, want 1 (quiet yield must not rerun Stop on the blocked reply)", got)
		}
		if reply != "" || !s.quietlyYielded() || s.communicated() || s.hasAcceptedTerminalCommunicate() {
			t.Fatalf("reply=%q quiet=%v communicated=%v terminal=%v, want quiet yield without accepting the blocked reply",
				reply, s.quietlyYielded(), s.communicated(), s.hasAcceptedTerminalCommunicate())
		}
		env.releaseJob()
		s.Close()
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Completion != nil && turn.Completion.AcceptedCommunicateCallID != "" {
				t.Fatalf("TurnCompletion published AcceptedCommunicateCallID=%q for a Stop-blocked reply", turn.Completion.AcceptedCommunicateCallID)
			}
		}
	})

	t.Run("visible_continuation_preserves_first_winner_and_dead_wait_still_refused", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return quietYieldResponse("first-winner", `{"message":"first reply"}`)
		}, func(llm.Request) llm.Response {
			return quietYieldResponse("dead-quiet", `{}`)
		}, func(llm.Request) llm.Response {
			return quietYieldResponse("second-loser", `{"message":"second reply"}`)
		})
		s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))

		var stopCalls atomic.Int32
		installCommunicateStopHook(s, func() string {
			if stopCalls.Add(1) <= 2 {
				return `{"decision":"block","reason":"finish checking first"}`
			}
			return `{}`
		})

		ctx := tripwireCtx(t)
		reply, err := s.ProcessInput(ctx, "answer", nil)
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		if !communicateToolResult(t, s, "dead-quiet").IsError {
			t.Fatal("communicate({}) without wakeable work succeeded after a Stop-blocked reply")
		}
		if reply != "first reply" {
			t.Fatalf("settled reply = %q, want first-winner %q", reply, "first reply")
		}
		s.Close()
		acceptedID := ""
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Completion != nil && turn.Completion.AcceptedCommunicateCallID != "" {
				acceptedID = turn.Completion.AcceptedCommunicateCallID
			}
		}
		if acceptedID != "first-winner" {
			t.Fatalf("AcceptedCommunicateCallID = %q, want %q", acceptedID, "first-winner")
		}
	})
}

func TestStopHookContinuationPreservesOrInjectsInFlightSteer(t *testing.T) {
	t.Parallel()
	t.Run("stop_allows_after_check_leaves_steer_for_carrier", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		probePath := filepath.Join(dir, "check.txt")
		if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var s *Session
		steered := false
		carrierSawSteer := make(chan struct{})
		adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return quietYieldResponse("draft-reply", `{"message":"draft reply"}`)
		}, func(llm.Request) llm.Response {
			if !steered {
				steered = true
				if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
					ClientMutationID: "steer-during-stop-check",
					Input:            []appwire.InputItem{{Type: "text", Text: "STEER_DURING_STOP_CHECK"}},
				}); err != nil {
					t.Errorf("AcceptClientMutationSteer: %v", err)
				}
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "check-file",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":` + mustJSONString(probePath) + `}`),
				Type:      "function",
			})
		}, func(req llm.Request) llm.Response {
			if requestContainsText(req, "STEER_DURING_STOP_CHECK") {
				close(carrierSawSteer)
			}
			return quietYieldResponse("carrier-reply", `{"message":"handled steer"}`)
		})
		s = newSession(t, withDir(dir), withAdapter(adapter), withStateDir(t.TempDir()))
		if err := s.ensureClientMutationStore(); err != nil {
			t.Fatal(err)
		}
		var stopCalls atomic.Int32
		installCommunicateStopHook(s, func() string {
			if stopCalls.Add(1) == 1 {
				return `{"decision":"block","reason":"read check.txt before finishing"}`
			}
			return `{}`
		})

		ctx := tripwireCtx(t)
		if _, err := s.ProcessInput(ctx, "answer after checking", nil); err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		select {
		case <-carrierSawSteer:
		default:
			t.Fatal("in-flight steer during Stop-hook check was consumed before Stop allowed the reply; carrier never ran")
		}
	})

	t.Run("stop_blocks_again_injects_steer_into_next_round", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		probePath := filepath.Join(dir, "check.txt")
		if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var s *Session
		steered := false
		nextRoundSawSteer := false
		adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
			return quietYieldResponse("draft-reply", `{"message":"draft reply"}`)
		}, func(llm.Request) llm.Response {
			if !steered {
				steered = true
				if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
					ClientMutationID: "steer-during-second-block",
					Input:            []appwire.InputItem{{Type: "text", Text: "STEER_DURING_SECOND_BLOCK"}},
				}); err != nil {
					t.Errorf("AcceptClientMutationSteer: %v", err)
				}
			}
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "check-file-1",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":` + mustJSONString(probePath) + `}`),
				Type:      "function",
			})
		}, func(req llm.Request) llm.Response {
			nextRoundSawSteer = requestContainsText(req, "STEER_DURING_SECOND_BLOCK")
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "check-file-2",
				Name:      "read_file",
				Arguments: json.RawMessage(`{"file_path":` + mustJSONString(probePath) + `}`),
				Type:      "function",
			})
		})
		s = newSession(t, withDir(dir), withAdapter(adapter), withStateDir(t.TempDir()))
		if err := s.ensureClientMutationStore(); err != nil {
			t.Fatal(err)
		}
		var stopCalls atomic.Int32
		installCommunicateStopHook(s, func() string {
			if stopCalls.Add(1) <= 2 {
				return `{"decision":"block","reason":"keep checking"}`
			}
			return `{}`
		})

		ctx := tripwireCtx(t)
		reply, err := s.ProcessInput(ctx, "answer after two checks", nil)
		if err != nil {
			t.Fatalf("ProcessInput: %v", err)
		}
		if !nextRoundSawSteer {
			t.Fatal("when Stop blocked again, pending steer was not injected before the next model request")
		}
		if reply != "draft reply" {
			t.Fatalf("reply = %q, want %q", reply, "draft reply")
		}
	})
}
