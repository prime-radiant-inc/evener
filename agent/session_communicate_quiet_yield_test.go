package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/plugin"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/transcriptindex"
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
		Accepted  bool   `json:"communicate_accepted"`
		EndTurn   bool   `json:"communicate_end_turn"`
		Quiet     bool   `json:"communicate_quiet"`
		QuietNote string `json:"communicate_quiet_note"`
	}
	if err := json.Unmarshal(res.ToolState, &state); err != nil || res.IsError || !state.Accepted || !state.EndTurn || !state.Quiet || state.QuietNote == "" {
		t.Fatalf("quiet tool result: IsError=%v, state=%+v, decode=%v", res.IsError, state, err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(fmt.Sprint(res.Content)), &out); err != nil || out["next"] != tool.CommunicateNextLogQuietlyAndEndTurn || out["quiet"] != true {
		t.Fatalf("quiet tool output = %v, decode=%v, want next=%q quiet=true", res.Content, err, tool.CommunicateNextLogQuietlyAndEndTurn)
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
			if len(props) != 3 || props["message"] == nil || props["next"] == nil || props["output"] == nil {
				t.Fatalf("advertised properties = %#v, want only message, next, output", props)
			}
			for key, want := range map[string]string{"message": "string", "next": "string", "output": "object"} {
				prop, _ := props[key].(map[string]any)
				if prop["type"] != want || (key == "next" && prop["default"] != tool.CommunicateNextSendAndEndTurn) {
					t.Fatalf("property %s = %#v, want type %s and next default %q", key, prop, want, tool.CommunicateNextSendAndEndTurn)
				}
			}
			if req, _ := def.Parameters["required"].([]string); len(req) != 1 || req[0] != "message" {
				t.Fatalf("required = %#v, want [message]", def.Parameters["required"])
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
		{name: "empty object rejected", args: `{}`, kind: EntryUserInput, job: true, wantError: true},
		{name: "quiet empty message rejected", args: `{"message":"   ","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, job: true, wantError: true},
		{name: "quiet with output rejected", args: `{"message":"waiting on job","next":"log_quietly_and_end_turn","output":{"data":{"answer":1}}}`, kind: EntryUserInput, job: true, wantError: true},
		{name: "unknown next rejected", args: `{"message":"done","next":"unknown_mode"}`, kind: EntryUserInput, wantError: true},
		{name: "no wake source", args: `{"message":"waiting on nothing","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, wantError: true},
		{name: "self timer", args: `{"message":"waiting on timer","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, watch: true, wantQuiet: true},
		{name: "one-shot self timer refused", args: `{"message":"waiting on timer","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, watch: true, oneShot: true, wantError: true},
		{name: "managed shell", args: `{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, job: true, wantQuiet: true},
		{name: "one-shot managed shell accepted", args: `{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`, kind: EntryUserInput, job: true, oneShot: true, wantQuiet: true},
		{name: "last notification", args: `{"message":"no action needed","next":"log_quietly_and_end_turn"}`, kind: EntryNotification, wantQuiet: true},
		{name: "message required for updates", args: `{"message":"   ","next":"send_and_continue"}`, kind: EntryUserInput, wantError: true},
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
		return agenttest.ToolCallResponse(askUserCall("ask", askUserArgsValid()), rawCommunicateCall("quiet", `{"message":"waiting for user answer","next":"log_quietly_and_end_turn"}`))
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
				return quietYieldResponse("progress", `{"message":"working","next":"send_and_continue"}`)
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
		{name: "progress without output", args: `{"message":"working","next":"send_and_continue"}`},
		{name: "quiet notification", args: `{"message":"no action needed","next":"log_quietly_and_end_turn"}`, wantQuiet: true},
		{name: "quiet with empty output object rejected under bound schema", args: `{"message":"no action needed","next":"log_quietly_and_end_turn","output":{}}`, wantError: true},
		{name: "empty object rejected under bound schema", args: `{}`, wantError: true},
		{name: "final with empty object", args: `{"message":"done","output":{}}`, wantFinal: true, wantOutput: `{}`},
		{name: "missing top-level message rejected under bound schema", args: `{"output":{"message":"nested"}}`, wantError: true},
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
		{name: "progress without output", args: `{"message":"working","next":"send_and_continue"}`},
		{name: "quiet log note", args: `{"message":"no action needed","next":"log_quietly_and_end_turn"}`},
		{name: "partial default output", args: `{"message":"done","output":{"data":{"sum":1000}}}`},
		{name: "empty default output", args: `{"message":"done","output":{}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withStateDir(t.TempDir()))
			repairedCh := drainRepairedEvents(s)
			if strings.Contains(tc.args, "log_quietly_and_end_turn") {
				enqueueCompletedJobNotification(t, s, "notify-repair")
			}
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
		return quietYieldResponse("quiet-yield", `{"message":"waiting on shell","next":"log_quietly_and_end_turn"}`)
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
			return quietYieldResponse("quiet-wait", `{"message":"waiting on running job","next":"log_quietly_and_end_turn"}`)
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
			return quietYieldResponse("dead-quiet", `{"message":"waiting","next":"log_quietly_and_end_turn"}`)
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

func TestRestoredLegacyPromptEmitsCommunicateContractDiffOnce(t *testing.T) {
	t.Parallel()
	legacyPrompt := "Legacy prompt fixture. Use communicate with end_turn=false for updates and communicate({}) to wait."
	upgradedPrompt := "Upgraded prompt fixture. Use communicate with next=\"log_quietly_and_end_turn\" to end quietly."

	setupLegacySession := func(t *testing.T, stateDir string, prompt, resultTool string, withInitialTurn bool) string {
		t.Helper()
		seedToolName := resultTool
		if seedToolName == "" {
			seedToolName = tool.DefaultResultToolName
		}
		s := newSession(t, withDir(stateDir), withSteps(func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID: "seed-call", Name: seedToolName, Arguments: json.RawMessage(`{"message":"seed","next":"send_and_end_turn"}`), Type: "function",
			})
		}), withConfig(SessionConfig{
			StateDir:       stateDir,
			ResultToolName: resultTool,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				envProbes:           fixedEnvProbes(),
			},
		}))
		if withInitialTurn {
			if _, err := s.ProcessInput(tripwireCtx(t), "seed turn", nil); err != nil {
				t.Fatalf("seed ProcessInput: %v", err)
			}
		}
		id := s.ID()
		s.Close()
		rewriteLegacyForkHeader(t, stateDir, id, prompt)
		meta, err := schema.LoadSessionMeta(stateDir, id)
		if err != nil {
			t.Fatalf("LoadSessionMeta: %v", err)
		}
		meta.LaunchSystemPrompt = ""
		meta.LaunchEnvBaseline.CommunicateContract = ""
		if !withInitialTurn {
			meta.EnvContext = nil
		}
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatalf("SaveSessionMeta: %v", err)
		}
		return id
	}

	for _, tc := range []struct {
		name            string
		prompt          string
		resultTool      string
		withInitialTurn bool
		entryKind       EntryKind
		wantDiff        bool
	}{
		{
			name:            "legacy_user_input_emits_once_and_replays_silently",
			prompt:          legacyPrompt,
			withInitialTurn: true,
			entryKind:       EntryUserInput,
			wantDiff:        true,
		},
		{
			name:            "legacy_notification_first_wake_emits_before_steering",
			prompt:          legacyPrompt,
			withInitialTurn: false,
			entryKind:       EntryNotification,
			wantDiff:        true,
		},
		{
			name:            "legacy_custom_result_tool_named_respond",
			prompt:          "Legacy prompt fixture. Call respond({}) when waiting.",
			resultTool:      "respond",
			withInitialTurn: true,
			entryKind:       EntryUserInput,
			wantDiff:        true,
		},
		{
			name:            "upgraded_prompt_emits_no_contract_diff",
			prompt:          upgradedPrompt,
			withInitialTurn: true,
			entryKind:       EntryUserInput,
			wantDiff:        false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stateDir := t.TempDir()
			id := setupLegacySession(t, stateDir, tc.prompt, tc.resultTool, tc.withInitialTurn)

			toolName := tc.resultTool
			if toolName == "" {
				toolName = tool.DefaultResultToolName
			}
			finalResp := func(msg string) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "done-" + msg,
					Name:      toolName,
					Arguments: json.RawMessage(`{"message":` + mustJSONString(msg) + `,"next":"send_and_end_turn"}`),
					Type:      "function",
				})
			}

			r1 := restoreQueuePersistTestSessionWith(t, stateDir, id, RestoreSessionConfig{
				testOnly: testConfig{
					skipGitSnapshot: true,
					noSyncJobStore:  true,
					envProbes:       fixedEnvProbes(),
				},
			})
			adapter1 := newFakeAdapter("openai", func(llm.Request) llm.Response { return finalResp("first") }, func(llm.Request) llm.Response { return finalResp("second") })
			r1.client.Register(adapter1)
			if tc.entryKind == EntryNotification {
				enqueueCompletedJobNotification(t, r1, "job_legacy_restore")
				if _, err := r1.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification); err != nil {
					t.Fatalf("ProcessInputKind(notification): %v", err)
				}
			} else {
				if _, err := r1.ProcessInput(tripwireCtx(t), "after restore", nil); err != nil {
					t.Fatalf("ProcessInput(turn 1): %v", err)
				}
			}
			reqs1 := adapter1.Requests()
			if len(reqs1) != 1 {
				t.Fatalf("restored turn 1 requests = %d, want 1", len(reqs1))
			}
			if gotSys := reqs1[0].Messages[0].Text(); gotSys != tc.prompt {
				t.Fatalf("saved system prompt was altered (prefix cache broken):\ngot  %q\nwant %q", gotSys, tc.prompt)
			}
			contractCount := 0
			for _, msg := range reqs1[0].Messages {
				txt := msg.Text()
				if strings.Contains(txt, "<environment_context>") && strings.Contains(txt, "communicate contract:") {
					contractCount++
					if !strings.Contains(txt, toolName) ||
						!strings.Contains(txt, schema.CommunicateNextSendAndEndTurn) ||
						!strings.Contains(txt, schema.CommunicateNextSendAndContinue) ||
						!strings.Contains(txt, schema.CommunicateNextLogQuietlyAndEndTurn) {
						t.Fatalf("contract diff missing result tool or next values:\n%s", txt)
					}
				}
			}
			if want := map[bool]int{false: 0, true: 1}[tc.wantDiff]; contractCount != want {
				t.Fatalf("first restored request contract diff count = %d, want %d", contractCount, want)
			}
			beforeSecondTurn := countEnvironmentTurns(r1)
			if _, err := r1.ProcessInput(tripwireCtx(t), "follow up", nil); err != nil {
				t.Fatalf("ProcessInput(turn 2): %v", err)
			}
			if countEnvironmentTurns(r1) != beforeSecondTurn {
				t.Fatalf("second turn in same process re-emitted environment turn: before=%d after=%d", beforeSecondTurn, countEnvironmentTurns(r1))
			}
			r1.Close()

			r2 := restoreQueuePersistTestSessionWith(t, stateDir, id, RestoreSessionConfig{
				testOnly: testConfig{
					skipGitSnapshot: true,
					noSyncJobStore:  true,
					envProbes:       fixedEnvProbes(),
				},
			})
			adapter2 := newFakeAdapter("openai", func(llm.Request) llm.Response { return finalResp("third") })
			r2.client.Register(adapter2)
			defer r2.Close()
			beforeSecondRestoreTurn := countEnvironmentTurns(r2)
			if _, err := r2.ProcessInput(tripwireCtx(t), "after second restore", nil); err != nil {
				t.Fatalf("ProcessInput(second restore): %v", err)
			}
			if countEnvironmentTurns(r2) != beforeSecondRestoreTurn {
				t.Fatalf("second restore duplicated environment turn (ReplayBlock missed communicate contract): before=%d after=%d",
					beforeSecondRestoreTurn, countEnvironmentTurns(r2))
			}
		})
	}
}

func TestCommunicateQuietNoteNormalizationPreservesFullMessage(t *testing.T) {
	t.Parallel()
	longBody := strings.Repeat("x", 260) + " final word"
	for _, tc := range []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "collapses_whitespace_and_newlines",
			message: "  waiting\non\t\tbuild\n  step 2  ",
			want:    "waiting on build step 2",
		},
		{
			name:    "preserves_long_message_without_truncation",
			message: "Waiting on:\n\t  " + longBody,
			want:    "Waiting on: " + longBody,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withStateDir(t.TempDir()))
			enqueueCompletedJobNotification(t, s, "note-job")
			ctx := context.WithValue(t.Context(), communicateEntryKindKey{}, EntryNotification)
			args, _ := json.Marshal(map[string]any{"message": tc.message, "next": tool.CommunicateNextLogQuietlyAndEndTurn})
			res := s.execTool(ctx, rawCommunicateCall("quiet-note", string(args)), "")
			if res.IsError {
				t.Fatalf("execTool quiet note failed: %s", res.FullOutput)
			}
			got := schema.QuietCommunicateNote(res.ToolState)
			if got != tc.want {
				t.Fatalf("QuietCommunicateNote = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCommunicateQuietYieldHubClientMutations(t *testing.T) {
	t.Parallel()

	t.Run("turn_start_without_other_work_rejects_quiet_yield", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-start", `{"message":"waiting","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response { return quietYieldResponse("reply-start", `{"message":"addressed"}`) },
		)
		s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: "cm-quiet-start",
			Input:            []appwire.InputItem{{Type: "text", Text: "yeah. looks reasonable"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationStart: %v", err)
		}
		reply, ran, err := s.ProcessClientMutationStart(tripwireCtx(t), nil)
		if err != nil || !ran {
			t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
		}
		if res := communicateToolResult(t, s, "quiet-start"); !res.IsError {
			t.Fatalf("communicate({}) on turn/start without other work succeeded: state=%s", res.ToolState)
		}
		if s.quietlyYielded() || !s.communicated() || reply != "addressed" {
			t.Fatalf("quiet=%v communicated=%v reply=%q; want refused quiet yield and reply=addressed",
				s.quietlyYielded(), s.communicated(), reply)
		}
		if got := len(adapter.Requests()); got != 2 {
			t.Fatalf("provider requests=%d, want 2 (rejected quiet yield + reply)", got)
		}
	})

	t.Run("turn_queue_without_other_work_rejects_quiet_yield", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-queue", `{"message":"waiting","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response { return quietYieldResponse("reply-queue", `{"message":"addressed"}`) },
		)
		s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: "cm-quiet-queue",
			Input:            []appwire.InputItem{{Type: "text", Text: "queued message"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationQueue: %v", err)
		}
		reply, ran, err := s.ProcessPendingUserInput(tripwireCtx(t), nil)
		if err != nil || !ran {
			t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
		}
		if res := communicateToolResult(t, s, "quiet-queue"); !res.IsError {
			t.Fatalf("communicate({}) on turn/queue without other work succeeded: state=%s", res.ToolState)
		}
		if s.quietlyYielded() || !s.communicated() || reply != "addressed" {
			t.Fatalf("quiet=%v communicated=%v reply=%q; want refused quiet yield and reply=addressed",
				s.quietlyYielded(), s.communicated(), reply)
		}
		if got := len(adapter.Requests()); got != 2 {
			t.Fatalf("provider requests=%d, want 2 (rejected quiet yield + reply)", got)
		}
	})

	t.Run("turn_start_with_pending_queued_input_allows_quiet_yield", func(t *testing.T) {
		t.Parallel()
		var s *Session
		queued := false
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				if !queued {
					queued = true
					if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
						ClientMutationID: "cm-queued-followup",
						Input:            []appwire.InputItem{{Type: "text", Text: "QUEUED_FOLLOWUP_SENTINEL"}},
					}); err != nil {
						t.Errorf("AcceptClientMutationQueue: %v", err)
					}
				}
				return quietYieldResponse("quiet-start-with-queue", `{"message":"handling queued follow-up","next":"log_quietly_and_end_turn"}`)
			},
			func(req llm.Request) llm.Response {
				if !requestContainsText(req, "QUEUED_FOLLOWUP_SENTINEL") {
					t.Errorf("second turn did not receive queued followup")
				}
				return quietYieldResponse("reply-queued-followup", `{"message":"handled queued"}`)
			},
		)
		s = newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: "cm-start-with-queue",
			Input:            []appwire.InputItem{{Type: "text", Text: "first turn"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationStart: %v", err)
		}
		reply, ran, err := s.ProcessClientMutationStart(tripwireCtx(t), nil)
		if err != nil || !ran {
			t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
		}
		requireQuietToolResult(t, s, "quiet-start-with-queue")
		if reply != "handled queued" || len(adapter.Requests()) != 2 {
			t.Fatalf("reply=%q requests=%d, want handled queued in 2 requests", reply, len(adapter.Requests()))
		}
	})

	t.Run("turn_queue_with_pending_queued_input_allows_quiet_yield", func(t *testing.T) {
		t.Parallel()
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-queue-with-second", `{"message":"handling second queued","next":"log_quietly_and_end_turn"}`)
			},
			func(req llm.Request) llm.Response {
				if !requestContainsText(req, "SECOND_QUEUED_SENTINEL") {
					t.Errorf("second queued turn did not receive expected text")
				}
				return quietYieldResponse("reply-second-queued", `{"message":"handled second queued"}`)
			},
		)
		s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
		if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: "cm-queue-first",
			Input:            []appwire.InputItem{{Type: "text", Text: "first queued"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationQueue first: %v", err)
		}
		if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
			ClientMutationID: "cm-queue-second",
			Input:            []appwire.InputItem{{Type: "text", Text: "SECOND_QUEUED_SENTINEL"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationQueue second: %v", err)
		}
		reply, ran, err := s.ProcessPendingUserInput(tripwireCtx(t), nil)
		if err != nil || !ran {
			t.Fatalf("ProcessPendingUserInput: ran=%v err=%v", ran, err)
		}
		requireQuietToolResult(t, s, "quiet-queue-with-second")
		if reply != "handled second queued" || len(adapter.Requests()) != 2 {
			t.Fatalf("reply=%q requests=%d, want handled second queued in 2 requests", reply, len(adapter.Requests()))
		}
	})

	t.Run("turn_start_with_live_background_job_allows_quiet_yield", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "bg-shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-start-with-job", `{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`)
			},
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		t.Cleanup(env.releaseJob)
		if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{
			ClientMutationID: "cm-start-with-job",
			Input:            []appwire.InputItem{{Type: "text", Text: "run in background"}},
		}); err != nil {
			t.Fatalf("AcceptClientMutationStart: %v", err)
		}
		if _, ran, err := s.ProcessClientMutationStart(tripwireCtx(t), nil); err != nil || !ran {
			t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
		}
		requireQuietToolResult(t, s, "quiet-start-with-job")
		if !s.quietlyYielded() || len(adapter.Requests()) != 2 {
			t.Fatalf("quiet=%v requests=%d, want true and 2", s.quietlyYielded(), len(adapter.Requests()))
		}
	})

	t.Run("recovered_incorporated_turn_start_self_excluded_unless_second_start_pending", func(t *testing.T) {
		t.Parallel()
		for _, withFollowUp := range []bool{false, true} {
			name := "without_followup"
			if withFollowUp {
				name = "with_second_turn_start_pending"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				crashed := newSession(t, withDir(dir), withStateDir(dir))
				id := crashed.ID()
				if _, err := crashed.AcceptClientMutationStart(appwire.TurnStartParams{
					ClientMutationID: "cm-dead-turn",
					Input:            []appwire.InputItem{{Type: "text", Text: "recovered prompt"}},
				}); err != nil {
					t.Fatalf("AcceptClientMutationStart: %v", err)
				}
				if _, err := crashed.SimulateCrashMidExecutionForTest(); err != nil {
					t.Fatalf("SimulateCrashMidExecutionForTest: %v", err)
				}
				crashed.Close()

				var restored *Session
				followUpAccepted := false
				steps := []func(llm.Request) llm.Response{
					func(llm.Request) llm.Response {
						if withFollowUp && !followUpAccepted {
							followUpAccepted = true
							if _, err := restored.AcceptClientMutationStart(appwire.TurnStartParams{
								ClientMutationID: "cm-second-start",
								Input:            []appwire.InputItem{{Type: "text", Text: "second turn start"}},
							}); err != nil {
								t.Errorf("AcceptClientMutationStart follow-up: %v", err)
							}
						}
						return quietYieldResponse("quiet-recovered", `{"message":"waiting on follow-up","next":"log_quietly_and_end_turn"}`)
					},
					func(llm.Request) llm.Response {
						return quietYieldResponse("reply-recovered", `{"message":"recovered reply"}`)
					},
				}
				adapter := newFakeAdapter("openai", steps...)
				c := registryClient(t, nil, adapter)
				meta, err := schema.LoadSessionMeta(dir, id)
				if err != nil {
					t.Fatalf("LoadSessionMeta: %v", err)
				}
				restored, err = RestoreSessionFromMetaWithConfig(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), meta, RestoreSessionConfig{
					StateDir: dir,
					testOnly: sharingHostProbe(testConfig{}),
				})
				if err != nil {
					t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
				}
				t.Cleanup(restored.Close)

				if !restored.hasRunnableClientMutationStart() || restored.WireState() != string(SessionProcessing) {
					t.Fatalf("before reclaim: runnable=%v wireState=%q, want true and %q",
						restored.hasRunnableClientMutationStart(), restored.WireState(), SessionProcessing)
				}
				reply, ran, err := restored.ProcessClientMutationStart(tripwireCtx(t), nil)
				if err != nil || !ran {
					t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
				}
				if withFollowUp {
					requireQuietToolResult(t, restored, "quiet-recovered")
					if !restored.quietlyYielded() || len(adapter.Requests()) != 1 {
						t.Fatalf("with second start: quiet=%v requests=%d, want true and 1",
							restored.quietlyYielded(), len(adapter.Requests()))
					}
					reply2, ran2, err2 := restored.ProcessClientMutationStart(tripwireCtx(t), nil)
					if err2 != nil || !ran2 || reply2 != "recovered reply" {
						t.Fatalf("second start run: ran=%v reply=%q err=%v", ran2, reply2, err2)
					}
				} else {
					if res := communicateToolResult(t, restored, "quiet-recovered"); !res.IsError {
						t.Fatalf("communicate({}) on recovered turn without follow-up succeeded: state=%s", res.ToolState)
					}
					if restored.quietlyYielded() || !restored.communicated() || reply != "recovered reply" || len(adapter.Requests()) != 2 {
						t.Fatalf("without follow-up: quiet=%v communicated=%v reply=%q requests=%d",
							restored.quietlyYielded(), restored.communicated(), reply, len(adapter.Requests()))
					}
				}
			})
		}
	})
}

func newAttentionDelegateTestSession(t *testing.T, dir string, adapter llm.ProviderAdapter) (*Session, *delegateTreeController, delegateLease) {
	t.Helper()
	c, _ := newDelegateControllerTestHarness(t, 1, 1)
	const delegateID = "dlg_attention_target"
	seedDelegateControllerIdle(t, c, delegateID, "")
	cfg := SessionConfig{MaxSubagentDepth: 2}
	cfg.AgentsDocPath = filepath.Join(t.TempDir(), "no-personal-AGENTS.md")
	cfg.spawn.depth = 1
	cfg.spawn.parentSessionID = "parent-session"
	s := newSession(t, withDir(dir), withAdapter(adapter), withStateDir(t.TempDir()), withConfig(cfg))
	ownController := s.delegateController
	ownDelegateID := s.owningDelegateID
	s.mu.Lock()
	s.delegateController = c
	s.owningDelegateID = delegateID
	s.mu.Unlock()
	t.Cleanup(func() {
		s.mu.Lock()
		s.delegateController = ownController
		s.owningDelegateID = ownDelegateID
		s.mu.Unlock()
	})
	return s, c, startDelegateAttentionEvidenceGenerationFor(t, c, delegateID, s)
}

func TestQuietYieldAttentionNoActionNotLatchedWhenRoundStoppedByHook(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		event   plugin.HookEvent
		matcher string
	}{
		{name: "PostToolBatch", event: plugin.HookPostToolBatch, matcher: "*"},
		{name: "PostToolUse_on_sibling", event: plugin.HookPostToolUse, matcher: "Read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			probePath := filepath.Join(dir, "check.txt")
			if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(
					rawCommunicateCall("quiet-0", `{"message":"no action needed","next":"log_quietly_and_end_turn"}`),
					llm.ToolCallData{
						ID:        "read-0",
						Name:      "read_file",
						Arguments: json.RawMessage(`{"file_path":` + mustJSONString(probePath) + `}`),
						Type:      "function",
					},
				)
			})
			s, c, lease := newAttentionDelegateTestSession(t, dir, adapter)
			var hookCalls atomic.Int32
			hookClient := llm.NewClient()
			hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
				hookCalls.Add(1)
				return llm.Response{Message: llm.Assistant(`{"continue":false,"stopReason":"hook stopped"}`)}
			}})
			runner := hooks.NewRunner(hookClient, "gpt-5.2")
			runner.Add(tc.event, plugin.RegisteredHook{Matcher: tc.matcher, Type: "prompt", Prompt: "Check round"})
			s.hookRunner = runner

			ctx := context.WithValue(tripwireCtx(t), delegateRunLeaseContextKey{}, lease)
			if _, _, err := s.processOneInput(ctx, "inspect", nil, EntryDelegateAttention, nil); err != nil {
				t.Fatalf("processOneInput: %v", err)
			}
			if got := hookCalls.Load(); got != 1 {
				t.Fatalf("%s hook calls = %d, want 1", tc.event, got)
			}
			requireQuietToolResult(t, s, "quiet-0")
			if s.quietlyYielded() {
				t.Fatalf("quietlyYielded() = true after %s stopped the round", tc.event)
			}
			decision, err := c.completionDecision(lease)
			if err != nil {
				t.Fatalf("completionDecision: %v", err)
			}
			if decision == delegateCompletionFinishNoAction {
				t.Fatalf("completionDecision = %v, want not delegateCompletionFinishNoAction after %s stop", decision, tc.event)
			}
			snap, err := c.completionSnapshot(lease)
			if err != nil {
				t.Fatalf("completionSnapshot: %v", err)
			}
			if snap.outcome != delegateCompletionOutcomeNone {
				t.Fatalf("evidence.outcome = %v, want delegateCompletionOutcomeNone", snap.outcome)
			}
		})
	}
}

func TestQuietYieldAttentionNoActionNotLatchedAlongsideVisibleCommunicate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		quietFirst bool
	}{
		{name: "quiet_before_visible", quietFirst: true},
		{name: "visible_before_quiet", quietFirst: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			probePath := filepath.Join(dir, "check.txt")
			if err := os.WriteFile(probePath, []byte("ok\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			var s *Session
			var c *delegateTreeController
			var lease delegateLease
			var secondRoundSnap delegateCompletionSnapshot
			adapter := newFakeAdapter("openai", func(llm.Request) llm.Response {
				quietCall := rawCommunicateCall("quiet-0", `{"message":"no action needed","next":"log_quietly_and_end_turn"}`)
				visibleCall := rawCommunicateCall("visible-0", `{"message":"real reply"}`)
				if tc.quietFirst {
					return agenttest.ToolCallResponse(quietCall, visibleCall)
				}
				return agenttest.ToolCallResponse(visibleCall, quietCall)
			}, func(llm.Request) llm.Response {
				var err error
				secondRoundSnap, err = c.completionSnapshot(lease)
				if err != nil {
					t.Errorf("round 2 completionSnapshot: %v", err)
				}
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "read-0",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":` + mustJSONString(probePath) + `}`),
					Type:      "function",
				})
			})
			s, c, lease = newAttentionDelegateTestSession(t, dir, adapter)
			var stopCalls atomic.Int32
			var stopHookSnap delegateCompletionSnapshot
			installCommunicateStopHook(s, func() string {
				if stopCalls.Add(1) == 1 {
					var err error
					stopHookSnap, err = c.completionSnapshot(lease)
					if err != nil {
						t.Errorf("Stop hook completionSnapshot: %v", err)
					}
					return `{"decision":"block","reason":"verify first"}`
				}
				return `{}`
			})

			ctx := context.WithValue(tripwireCtx(t), delegateRunLeaseContextKey{}, lease)
			reply, _, err := s.processOneInput(ctx, "inspect", nil, EntryDelegateAttention, nil)
			if err != nil {
				t.Fatalf("processOneInput: %v", err)
			}
			if reply != "real reply" {
				t.Fatalf("reply = %q, want %q", reply, "real reply")
			}
			if stopHookSnap.outcome != delegateCompletionOutcomeNone {
				t.Fatalf("during Stop hook: evidence.outcome = %v, want delegateCompletionOutcomeNone", stopHookSnap.outcome)
			}
			if secondRoundSnap.outcome != delegateCompletionOutcomeNone {
				t.Fatalf("between Stop block and settlement: evidence.outcome = %v, want delegateCompletionOutcomeNone", secondRoundSnap.outcome)
			}
			finalSnap, err := c.completionSnapshot(lease)
			if err != nil {
				t.Fatalf("final completionSnapshot: %v", err)
			}
			if finalSnap.outcome != delegateCompletionOutcomeNone || !finalSnap.terminalSeen {
				t.Fatalf("final evidence = %+v, want outcome=None and terminalSeen=true", finalSnap)
			}
			decision, err := c.completionDecision(lease)
			if err != nil {
				t.Fatalf("completionDecision: %v", err)
			}
			if decision != delegateCompletionUseExistingTerminal {
				t.Fatalf("completionDecision = %v, want delegateCompletionUseExistingTerminal", decision)
			}
		})
	}
}

func TestCommunicateBoundaryStateMachine(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "None -> Settled",
			run: func(t *testing.T) {
				s := newSession(t)
				s.mu.Lock()
				s.state = SessionProcessing
				s.mu.Unlock()
				s.beginCommunicateRound()
				if !s.acceptCommunicateTerminal(t.Context(), "msg", "reply", "out", nil) {
					t.Fatal("expected terminal communicate to be accepted")
				}
				if s.comm.phase != commBoundarySettled || !s.comm.roundVisible || !s.communicated() || !s.communicatedUnblocked() || s.quietlyYielded() {
					t.Fatalf("after accept: comm=%+v", s.comm)
				}
				done, text, err := s.deliverIfCommunicated(t.Context(), false)
				if err != nil || !done || text != "reply" {
					t.Fatalf("deliverIfCommunicated = done:%v text:%q err:%v, want done=true reply=reply", done, text, err)
				}
				if s.comm.phase != commBoundarySettled || s.latestReport.phase != commBoundarySettled || s.State() != SessionIdle {
					t.Fatalf("after deliver: phase=%v latest=%v state=%v", s.comm.phase, s.latestReport.phase, s.State())
				}
			},
		},
		{
			name: "None -> StopPending -> Settled",
			run: func(t *testing.T) {
				s := newSession(t)
				installCommunicateStopHook(s, func() string { return `{}` })
				s.mu.Lock()
				s.state = SessionProcessing
				s.mu.Unlock()
				s.beginCommunicateRound()
				ctx := context.WithValue(t.Context(), ctxToolCallID, "call-pending")
				if !s.acceptCommunicateTerminal(ctx, "msg", "reply", "out", nil) {
					t.Fatal("expected terminal communicate to be accepted")
				}
				if s.comm.phase != commBoundaryStopPending || s.comm.stopPendingCallID != "call-pending" || s.latestReport.phase != commBoundaryNone {
					t.Fatalf("after pending accept: comm=%+v latest=%+v", s.comm, s.latestReport)
				}
				done, text, err := s.deliverIfCommunicated(ctx, false)
				if err != nil || !done || text != "reply" {
					t.Fatalf("deliverIfCommunicated = done:%v text:%q err:%v", done, text, err)
				}
				if s.comm.phase != commBoundarySettled || s.latestReport.phase != commBoundarySettled || s.execution.acceptedCommunicateCallID != "call-pending" {
					t.Fatalf("after settle: phase=%v latest=%v callID=%q", s.comm.phase, s.latestReport.phase, s.execution.acceptedCommunicateCallID)
				}
			},
		},
		{
			name: "None -> StopPending -> StopBlocked -> RoundVisible -> Settled",
			run: func(t *testing.T) {
				s := newSession(t)
				var calls atomic.Int32
				installCommunicateStopHook(s, func() string {
					if calls.Add(1) == 1 {
						return `{"decision":"block","reason":"verify first"}`
					}
					return `{}`
				})
				s.mu.Lock()
				s.state = SessionProcessing
				s.mu.Unlock()
				s.beginCommunicateRound()
				if !s.acceptCommunicateTerminal(t.Context(), "first", "first", "", nil) {
					t.Fatal("expected first terminal communicate to win")
				}
				if done, _, err := s.deliverIfCommunicated(t.Context(), false); err != nil || done {
					t.Fatalf("round 1 deliverIfCommunicated = done:%v err:%v, want blocked", done, err)
				}
				s.beginCommunicateRound()
				s.recordQuietYield()
				if s.acceptCommunicateTerminal(t.Context(), "second", "second", "", nil) {
					t.Fatal("competing visible communicate must lose to blocked first winner")
				}
				if s.comm.phase != commBoundaryStopBlocked || !s.comm.roundVisible || s.quietlyYielded() {
					t.Fatalf("after competing visible call: comm=%+v", s.comm)
				}
				done, text, err := s.deliverIfCommunicated(t.Context(), false)
				if err != nil || !done || text != "first" {
					t.Fatalf("round 2 deliverIfCommunicated = done:%v text:%q err:%v, want first", done, text, err)
				}
				if s.comm.phase != commBoundarySettled || s.quietlyYielded() {
					t.Fatalf("after settle: comm=%+v", s.comm)
				}
			},
		},
		{
			name: "Quiet + batchBlocked cleared",
			run: func(t *testing.T) {
				s := newSession(t)
				s.beginCommunicateRound()
				s.recordQuietYield()
				if !s.comm.roundQuiet || !s.quietlyYielded() {
					t.Fatalf("after recordQuietYield: comm=%+v", s.comm)
				}
				s.clearQuietBoundaryForBatchStop()
				if s.comm.phase != commBoundaryNone || s.comm.roundQuiet || s.quietlyYielded() {
					t.Fatalf("after clearQuietBoundaryForBatchStop: comm=%+v", s.comm)
				}
			},
		},
		{
			name: "canRecordAttentionNoAction is read-only, reportRequired quiet settle is no-op, and stale lease keeps SessionProcessing",
			run: func(t *testing.T) {
				s, c, lease := newAttentionDelegateTestSession(t, t.TempDir(), newFakeAdapter("openai"))
				c.mu.Lock()
				versionBefore := c.evidenceVersion
				c.mu.Unlock()

				eligible, err := c.canRecordAttentionNoAction(lease)
				if err != nil || !eligible {
					t.Fatalf("canRecordAttentionNoAction = %t, %v; want true, nil", eligible, err)
				}
				snap, err := c.completionSnapshot(lease)
				if err != nil {
					t.Fatalf("completionSnapshot: %v", err)
				}
				c.mu.Lock()
				versionAfter := c.evidenceVersion
				c.mu.Unlock()
				if snap.outcome != delegateCompletionOutcomeNone || versionAfter != versionBefore {
					t.Fatalf("read-only probe mutated state: outcome=%v version=%d->%d", snap.outcome, versionBefore, versionAfter)
				}

				// Escalated reportRequired delegate with quiet boundary: finishReplyBoundary
				// rechecks via recordAttentionNoAction safely without latching outcome.
				if err := c.escalateCompletionRequirement(lease); err != nil {
					t.Fatalf("escalateCompletionRequirement: %v", err)
				}
				attnCtx := context.WithValue(
					context.WithValue(t.Context(), delegateRunLeaseContextKey{}, lease),
					communicateEntryKindKey{}, EntryDelegateAttention,
				)
				s.mu.Lock()
				s.state = SessionProcessing
				s.mu.Unlock()
				s.beginCommunicateRound()
				s.recordQuietYield()
				done, _, err := s.finishReplyBoundary(attnCtx, SessionIdle, false, "", nil)
				if err != nil || !done || s.State() != SessionIdle {
					t.Fatalf("reportRequired quiet finishReplyBoundary = done:%t err:%v state:%v; want done, nil, idle", done, err, s.State())
				}
				snap, err = c.completionSnapshot(lease)
				if err != nil || snap.outcome != delegateCompletionOutcomeNone {
					t.Fatalf("reportRequired snapshot = %+v, err=%v; want outcome=None", snap, err)
				}

				s.mu.Lock()
				s.state = SessionProcessing
				s.mu.Unlock()
				if _, err := c.FinishGeneration(lease, delegateFinish{outcome: delegatestore.OutcomeCancelled, reason: "stale"}); err != nil {
					t.Fatalf("FinishGeneration: %v", err)
				}
				done, _, err = s.finishReplyBoundary(attnCtx, SessionIdle, false, "", nil)
				if err == nil || done || s.State() != SessionProcessing {
					t.Fatalf("stale finishReplyBoundary = done:%t err:%v state:%v; want error, done=false, state=processing", done, err, s.State())
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.run(t)
		})
	}
}

func TestKeepingReportRestoresSettledReportWhenFollowUpAbortsBeforeStopSettles(t *testing.T) {
	t.Parallel()
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response {
			return quietYieldResponse("settled-call", `{"message":"settled report","output":{"message":"settled summary","data":{"version":1}}}`)
		},
		func(llm.Request) llm.Response {
			return quietYieldResponse("aborted-call", `{"message":"aborted follow-up","output":{"message":"aborted summary","data":{"version":2}}}`)
		},
	)
	s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
	installCommunicateStopHook(s, func() string { return `{}` })

	ctx := tripwireCtx(t)
	const wantReply = `{"message":"settled summary","data":{"version":1},"artifacts":[]}`
	reply, err := s.ProcessInput(ctx, "initial work", nil)
	if err != nil || reply != wantReply {
		t.Fatalf("initial ProcessInput = (%q, %v), want (%q, nil)", reply, err, wantReply)
	}

	execFault := errors.New("tool batch aborted before stop")
	enqueueCompletedJobNotification(t, s, "followup-job")
	faultCtx := context.WithValue(ctx, sessionLifecycleFaultsKey{}, map[string]error{"exec_tools": execFault})
	res, err := s.keepingReport(s.ProcessInputKind)(faultCtx, "", nil, EntryNotification)
	if !errors.Is(err, execFault) {
		t.Fatalf("keepingReport err = %v, want preserved %v", err, execFault)
	}
	if res != wantReply {
		t.Fatalf("keepingReport res = %q, want %q", res, wantReply)
	}

	inputs := delegateTerminalRunInputs{runErr: err}
	inputs.captureCommunicateReport(s)
	if !inputs.communicated || !inputs.structuredResultPresent || !errors.Is(inputs.runErr, execFault) {
		t.Fatalf("captureCommunicateReport = communicated:%t structuredPresent:%t runErr:%v, want settled report and preserved error", inputs.communicated, inputs.structuredResultPresent, inputs.runErr)
	}
	gotStructured, marshalErr := json.Marshal(inputs.structuredResult)
	if marshalErr != nil || string(gotStructured) != `{"data":{"version":1},"message":"settled summary"}` {
		t.Fatalf("captured structuredResult = %s (err=%v), want prior settled output", gotStructured, marshalErr)
	}
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

func companionQuietResponse(id, text, args string) llm.Response {
	call := rawCommunicateCall(id, args)
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: text},
		{Kind: llm.ContentToolCall, ToolCall: &call},
	}}}
}

func assistantTurnWithText(t *testing.T, turns []schema.Turn, want string) schema.Turn {
	t.Helper()
	for _, turn := range entriesOfKind(turns, schema.TurnAssistant) {
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentText && part.Text == want {
				return turn
			}
		}
	}
	t.Fatalf("no assistant turn carried text %q", want)
	return schema.Turn{}
}

func assistantTurnWithToolCall(t *testing.T, turns []schema.Turn, callID string) schema.Turn {
	t.Helper()
	for _, turn := range entriesOfKind(turns, schema.TurnAssistant) {
		for _, part := range turn.Message.Content {
			if part.Kind == llm.ContentToolCall && part.ToolCall != nil && part.ToolCall.ID == callID {
				return turn
			}
		}
	}
	t.Fatalf("no assistant turn carried tool call %q", callID)
	return schema.Turn{}
}

func TestQuietCompanionStampedAtProducer(t *testing.T) {
	t.Parallel()
	run := func(t *testing.T, args string) []schema.Turn {
		t.Helper()
		env := newHeldJobEnvironment(t.TempDir())
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
			},
			func(llm.Request) llm.Response { return companionQuietResponse("probe", "Running the suite.", args) },
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		t.Cleanup(env.releaseJob)
		installCommunicateStopHook(s, func() string { return `{}` })
		if _, err := s.ProcessInputKind(tripwireCtx(t), "start", nil, EntryUserInput); err != nil {
			t.Fatal(err)
		}
		env.releaseJob()
		return transcriptTurnsOf(t, s)
	}

	t.Run("quiet call stamps companion", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
			},
			func(llm.Request) llm.Response {
				return companionQuietResponse("probe", "  Running the suite.  ", `{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		t.Cleanup(env.releaseJob)
		installCommunicateStopHook(s, func() string { return `{}` })
		if _, err := s.ProcessInputKind(tripwireCtx(t), "start", nil, EntryUserInput); err != nil {
			t.Fatal(err)
		}
		res := communicateToolResult(t, s, "probe")
		state, ok := schema.DecodeCommunicateToolState(res.ToolState)
		if !ok || !state.IsQuiet() {
			t.Fatalf("probe tool state = %+v, decode ok=%v", state, ok)
		}
		if state.QuietCommentary != "Running the suite." {
			t.Fatalf("QuietCommentary = %q, want trimmed companion prose", state.QuietCommentary)
		}
		if state.QuietNote == "" {
			t.Fatal("QuietNote is empty, want the quiet message summary")
		}
		env.releaseJob()
		turns := transcriptTurnsOf(t, s)
		turn := assistantTurnWithText(t, turns, "  Running the suite.  ")
		if !turn.QuietCompanion {
			t.Fatalf("assistant turn QuietCompanion = false, want true")
		}
		if turn.QuietYield {
			t.Fatalf("assistant turn QuietYield = true, want false")
		}
	})

	t.Run("loud call keeps companion visible", func(t *testing.T) {
		t.Parallel()
		turns := run(t, `{"message":"done","next":"send_and_end_turn"}`)
		turn := assistantTurnWithText(t, turns, "Running the suite.")
		if turn.QuietCompanion {
			t.Fatalf("assistant turn QuietCompanion = true, want false for a loud call")
		}
	})

	t.Run("content part run boundaries", func(t *testing.T) {
		t.Parallel()
		quietCall := llm.ContentPart{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID: "probe", Name: "communicate",
				Arguments: json.RawMessage(`{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`),
				Type:      "function",
			},
		}
		jobListCall := func(id string) llm.ContentPart {
			return llm.ContentPart{
				Kind:     llm.ContentToolCall,
				ToolCall: &llm.ToolCallData{ID: id, Name: "job_list", Arguments: json.RawMessage(`{}`), Type: "function"},
			}
		}
		cases := []struct {
			name               string
			parts              []llm.ContentPart
			wantCommentary     string
			wantQuietCompanion bool
		}{
			{
				name: "split across tool call joins trimmed runs with blank line",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "  alpha-run"},
					jobListCall("jobs-1"),
					{Kind: llm.ContentText, Text: "beta-run  "},
					quietCall,
				},
				wantCommentary:     "alpha-run\n\nbeta-run",
				wantQuietCompanion: true,
			},
			{
				name: "adjacent text parts stay in one run",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "  alpha-"},
					{Kind: llm.ContentText, Text: "run  "},
					quietCall,
				},
				wantCommentary:     "alpha-run",
				wantQuietCompanion: true,
			},
			{
				name: "whitespace-only runs omitted across tool calls",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: " \n\t "},
					jobListCall("jobs-1"),
					{Kind: llm.ContentText, Text: "  alpha-run  "},
					jobListCall("jobs-2"),
					{Kind: llm.ContentText, Text: "  \n  "},
					jobListCall("jobs-3"),
					{Kind: llm.ContentText, Text: "  beta-run  "},
					jobListCall("jobs-4"),
					{Kind: llm.ContentText, Text: "\t "},
					quietCall,
				},
				wantCommentary:     "alpha-run\n\nbeta-run",
				wantQuietCompanion: true,
			},
			{
				name: "all-whitespace text across tool calls stamps no companion",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "  \n "},
					jobListCall("jobs-1"),
					{Kind: llm.ContentText, Text: "\t "},
					quietCall,
				},
				wantCommentary:     "",
				wantQuietCompanion: false,
			},
			{
				name: "metadata-only reasoning keeps text in one run",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "alpha-"},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{GoogleInteraction: true, Signature: "sig_1"}},
					{Kind: llm.ContentText, Text: "run"},
					quietCall,
				},
				wantCommentary:     "alpha-run",
				wantQuietCompanion: true,
			},
			{
				name: "visible thinking separates text runs",
				parts: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "alpha-run"},
					{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "thought-sentinel"}},
					{Kind: llm.ContentText, Text: "beta-run"},
					quietCall,
				},
				wantCommentary:     "alpha-run\n\nbeta-run",
				wantQuietCompanion: true,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				env := newHeldJobEnvironment(t.TempDir())
				adapter := newFakeAdapter("openai",
					func(llm.Request) llm.Response {
						return agenttest.ToolCallResponse(llm.ToolCallData{ID: "shell", Name: "shell", Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`)})
					},
					func(llm.Request) llm.Response {
						return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: tc.parts}}
					},
					func(llm.Request) llm.Response { return agenttest.FinalResponse("recovered") },
				)
				s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
				t.Cleanup(env.releaseJob)
				installCommunicateStopHook(s, func() string { return `{}` })
				if _, err := s.ProcessInputKind(tripwireCtx(t), "start", nil, EntryUserInput); err != nil {
					t.Fatal(err)
				}
				res := communicateToolResult(t, s, "probe")
				state, ok := schema.DecodeCommunicateToolState(res.ToolState)
				if !ok || !state.IsQuiet() {
					t.Fatalf("probe tool state = %+v, decode ok=%v", state, ok)
				}
				if state.QuietCommentary != tc.wantCommentary {
					t.Fatalf("QuietCommentary = %q, want %q", state.QuietCommentary, tc.wantCommentary)
				}
				env.releaseJob()
				turns := transcriptTurnsOf(t, s)
				turn := assistantTurnWithToolCall(t, turns, "probe")
				if turn.QuietCompanion != tc.wantQuietCompanion {
					t.Fatalf("assistant turn QuietCompanion = %v, want %v", turn.QuietCompanion, tc.wantQuietCompanion)
				}
				reg := apptranscript.NewToolCallRegistry()
				appTurns, err := apptranscript.ItemTurnsFromFile(s.TranscriptPath(), transcript.DefaultMaxLineBytes, func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
					items, _ := apptranscript.ProjectEntryParts(turnID, turnIndex, turn, reg, nil, nil)
					return items
				})
				if err != nil {
					t.Fatalf("apptranscript projection: %v", err)
				}
				var gotCommentary string
				foundQuiet := false
				for _, appTurn := range appTurns {
					for _, item := range appTurn.Items {
						if item.EventKind == appwire.ThreadItemEventKindQuietYield && item.CallID == "probe" {
							foundQuiet = true
							if len(item.Raw) > 0 {
								var raw struct {
									Commentary string `json:"commentary"`
								}
								if err := json.Unmarshal(item.Raw, &raw); err != nil {
									t.Fatalf("decode quiet_yield raw %s: %v", item.Raw, err)
								}
								gotCommentary = raw.Commentary
							}
						}
					}
				}
				if !foundQuiet {
					t.Fatal("projected transcript missing quiet_yield item for probe")
				}
				if gotCommentary != tc.wantCommentary {
					t.Fatalf("projected quiet_yield commentary = %q, want %q", gotCommentary, tc.wantCommentary)
				}
			})
		}
	})
}

// A real dead-wait rejection exercises the whole contract: the producer stamps
// the companion prose and the runtime error, both readers hide the prose and
// the error item, and the next round's reply renders normally on both.
func TestQuietCompanionRuntimeRejectionStaysOutOfHistory(t *testing.T) {
	t.Parallel()
	const (
		companionSentinel = "CHECKING BEFORE THE QUIET YIELD"
		replySentinel     = "FINAL ANSWER SENTINEL"
	)
	roundOne := llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: companionSentinel},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{
				ID: "quiet-dead", Name: "communicate", Arguments: json.RawMessage(`{"message":"Waiting","next":"log_quietly_and_end_turn"}`), Type: "function",
			}},
		}},
		Finish: llm.FinishReason{Reason: llm.FinishReasonStop},
	}
	adapter := newFakeAdapter("openai",
		func(llm.Request) llm.Response { return roundOne },
		func(llm.Request) llm.Response {
			return quietYieldResponse("final", `{"message":"`+replySentinel+`","next":"send_and_end_turn"}`)
		},
	)
	s := newSession(t, withAdapter(adapter), withStateDir(t.TempDir()))
	reply, err := s.ProcessInput(tripwireCtx(t), "start", nil)
	if err != nil || reply != replySentinel {
		t.Fatalf("reply=%q err=%v, want the round 2 reply", reply, err)
	}
	path := s.TranscriptPath()
	defer s.Close()

	companionStamped := false
	var rejection *llm.ToolResultData
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.Kind == schema.TurnAssistant && turn.QuietCompanion && turn.Message.Text() != "" {
			companionStamped = true
		}
		if turn.Kind != schema.TurnToolResults {
			continue
		}
		for _, part := range turn.Message.Content {
			if part.ToolResult != nil && part.ToolResult.ToolCallID == "quiet-dead" {
				rejection = part.ToolResult
			}
		}
	}
	if !companionStamped {
		t.Fatal("no TurnAssistant carried QuietCompanion")
	}
	if rejection == nil || !rejection.IsError || rejection.PrevalOnly {
		t.Fatalf("quiet-dead result=%+v, want IsError=true PrevalOnly=false", rejection)
	}

	reg := apptranscript.NewToolCallRegistry()
	project := func(turn schema.Turn, turnID string, turnIndex int) []appwire.ThreadItem {
		items, _ := apptranscript.ProjectEntryParts(turnID, turnIndex, turn, reg, nil, nil)
		return items
	}
	appTurns, err := apptranscript.ItemTurnsFromFile(path, transcript.DefaultMaxLineBytes, project)
	if err != nil {
		t.Fatalf("apptranscript projection: %v", err)
	}
	var projected []appwire.ThreadItem
	for _, turn := range appTurns {
		projected = append(projected, turn.Items...)
	}
	idx, err := transcriptindex.Open(path, t.TempDir())
	if err != nil {
		t.Fatalf("transcriptindex.Open: %v", err)
	}
	defer func() {
		if err := idx.Close(); err != nil {
			t.Error(err)
		}
	}()
	window, err := idx.Latest(40)
	if err != nil {
		t.Fatalf("transcriptindex Latest: %v", err)
	}
	var indexed []appwire.ThreadItem
	for _, candidate := range window.Candidates {
		indexed = append(indexed, candidate.Item)
	}

	fingerprint := func(items []appwire.ThreadItem) []string {
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, item.Type+"|"+item.CallID+"|"+item.Text)
		}
		return out
	}
	if got, want := fingerprint(indexed), fingerprint(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("apptranscript and transcriptindex diverge:\nprojected: %q\nindexed:   %q", want, got)
	}
	for _, items := range [][]appwire.ThreadItem{projected, indexed} {
		replies := 0
		for _, item := range items {
			if item.Type == "commandExecution" {
				t.Errorf("tool item=%+v, want the runtime rejection kept out of history", item)
			}
			if strings.Contains(item.Text, companionSentinel) {
				t.Errorf("item text=%q, want companion prose suppressed", item.Text)
			}
			if item.Type == "agentMessage" && strings.Contains(item.Text, replySentinel) {
				replies++
			}
		}
		if replies != 1 {
			t.Errorf("agentMessages carrying the reply=%d, want 1 in %+v", replies, items)
		}
	}
}
