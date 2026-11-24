package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/provenance"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/internal/testwait"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func bareTextResponse(text string, reason string) llm.Response {
	return llm.Response{Message: llm.Assistant(text), Finish: llm.FinishReason{Reason: reason}}
}

func communicateEventsIn(evs []events.SessionEvent) []events.CommunicateData {
	var out []events.CommunicateData
	for _, ev := range evs {
		if ev.Kind == events.EventCommunicate {
			if d, ok := ev.Data.(events.CommunicateData); ok {
				out = append(out, d)
			}
		}
	}
	return out
}

func countAgentMessages(t *testing.T, transcriptPath, turnID string) (total int, texts []string) {
	t.Helper()
	idx, err := transcriptindex.Open(transcriptPath, t.TempDir())
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
		if candidate.Item.Type != "agentMessage" {
			continue
		}
		if turnID != "" && candidate.Item.TurnID != turnID {
			continue
		}
		total++
		texts = append(texts, candidate.Item.Text)
	}
	return total, texts
}

func TestRootBareTextFallback(t *testing.T) {
	t.Parallel()
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
		liveJob      bool
		wantFallback bool
		wantQuiet    bool
	}{
		{name: "natural stop", text: "Root answer", finish: llm.FinishReasonStop, kind: EntryUserInput, wantFallback: true},
		{name: "truncated", text: "Partial answer", finish: llm.FinishReasonLength, kind: EntryUserInput},
		{name: "empty", finish: llm.FinishReasonStop, kind: EntryUserInput},
		{name: "idle root notification steers", text: "Final notification answer", finish: llm.FinishReasonStop, kind: EntryNotification},
		{name: "root notification with live owned job steers", text: "Waiting on remaining job", finish: llm.FinishReasonStop, kind: EntryNotification, liveJob: true},
		{name: "typed root notification steers", text: "Untyped notification answer", finish: llm.FinishReasonStop, kind: EntryNotification, profile: structured},
		{name: "goal continuation", text: "Continuation prose", finish: llm.FinishReasonStop, kind: EntryContinuation},
		{name: "idle subagent", text: "Idle subagent answer", finish: llm.FinishReasonStop, kind: EntryUserInput, parent: true, wantFallback: true},
		{name: "root user input round 0 with prior live job", text: "Direct answer", finish: llm.FinishReasonStop, kind: EntryUserInput, liveJob: true, wantFallback: true},
		{name: "typed root user input with prior live job steers", text: "Waiting on job", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: structured, liveJob: true},
		{name: "subagent with live owned job", text: "Waiting on job", finish: llm.FinishReasonStop, kind: EntryUserInput, parent: true, liveJob: true, wantQuiet: true},
		{name: "subagent with output schema", text: "Unstructured answer", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: structured, parent: true},
		{name: "explicit default envelope", text: "Untyped answer", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: boundDefault},
		{name: "output schema", text: "Untyped answer", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: structured},
		{name: "allowed decisions", text: "No decision", finish: llm.FinishReasonStop, kind: EntryUserInput, profile: provider.WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"approve"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls int
			s := newSession(t, withProfile(tc.profile), withStateDir(t.TempDir()), withSteps(func(llm.Request) llm.Response {
				calls++
				return bareTextResponse(tc.text, tc.finish)
			}, func(llm.Request) llm.Response {
				calls++
				switch tc.name {
				case "explicit default envelope":
					return quietYieldResponse("recovered", `{"message":"Explicit answer","output":{}}`)
				case "output schema", "allowed decisions", "subagent with output schema":
					return quietYieldResponse("recovered", `{"message":"Explicit answer","output":{"decision":"approve"}}`)
				default:
					return agenttest.FinalResponse("Explicit answer")
				}
			}))
			evs, evMu, evDone := collectEvents(s)
			if tc.parent {
				s.cfg.spawn.parentSessionID = "parent"
			}
			if tc.liveJob {
				jobID := "job_live_owned"
				s.jobManager.mu.Lock()
				s.jobManager.running[jobID] = &runningJob{
					rec: &jobstore.JobRecord{
						JobID:          jobID,
						Type:           jobstore.JobShell,
						Status:         jobstore.StatusRunning,
						OwnerSessionID: s.ID(),
					},
					done: make(chan struct{}),
				}
				s.jobManager.mu.Unlock()
				t.Cleanup(func() {
					s.jobManager.mu.Lock()
					delete(s.jobManager.running, jobID)
					s.jobManager.mu.Unlock()
				})
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
				s.Close()
				<-evDone
				evMu.Lock()
				commEvs := communicateEventsIn(*evs)
				evMu.Unlock()
				if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != tc.text {
					t.Fatalf("EventCommunicate = %+v, want 1 EndTurn=%q", commEvs, tc.text)
				}
				return
			}
			if tc.wantQuiet {
				if err != nil || got != "" || calls != 1 || s.State() != SessionIdle || !s.quietlyYielded() || s.communicated() {
					t.Fatalf("quiet park reply=%q calls=%d state=%v quiet=%t communicated=%t err=%v",
						got, calls, s.State(), s.quietlyYielded(), s.communicated(), err)
				}
				s.Close()
				<-evDone
				evMu.Lock()
				commEvs := communicateEventsIn(*evs)
				evMu.Unlock()
				if len(commEvs) != 0 {
					t.Fatalf("bare-text quiet park emitted EventCommunicate: %+v", commEvs)
				}
				var sawQuietTurn bool
				for _, turn := range transcriptTurnsOf(t, s) {
					if turn.Kind == schema.TurnAssistant && turn.QuietYield {
						sawQuietTurn = true
					}
				}
				if !sawQuietTurn {
					t.Fatal("bare-text quiet park did not persist QuietYield=true on TurnAssistant")
				}
				return
			}
			if calls != 2 || err != nil || s.State() != SessionIdle {
				t.Fatalf("ineligible reply=%q calls=%d state=%v err=%v", got, calls, s.State(), err)
			}
			if tc.name == "output schema" || tc.name == "allowed decisions" || tc.name == "subagent with output schema" {
				var reply struct{ Message, Decision string }
				if err := json.Unmarshal([]byte(got), &reply); err != nil || reply.Message != "Explicit answer" || reply.Decision != "approve" {
					t.Fatalf("structured reply=%q decode=%v", got, err)
				}
			} else if got != "Explicit answer" {
				t.Fatalf("reply=%q, want the explicit reply", got)
			}
			s.Close()
			<-evDone
			evMu.Lock()
			commEvs := communicateEventsIn(*evs)
			evMu.Unlock()
			if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Explicit answer" {
				t.Fatalf("ineligible bare text leaked or missed EventCommunicate: %+v", commEvs)
			}
		})
	}

	t.Run("steering carrier", func(t *testing.T) {
		t.Parallel()
		var s *Session
		steered := false
		carrierSawSteer := false
		s = newSession(t, withStateDir(t.TempDir()), withSteps(func(llm.Request) llm.Response {
			if !steered {
				steered = true
				if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
					ClientMutationID: "bare-fallback-carrier",
					Input:            []appwire.InputItem{{Type: "text", Text: "CARRIER_STEER_SENTINEL"}},
				}); err != nil {
					t.Errorf("AcceptClientMutationSteer: %v", err)
				}
			}
			return agenttest.FinalResponse("Initial reply")
		}, func(req llm.Request) llm.Response {
			carrierSawSteer = requestContainsText(req, "CARRIER_STEER_SENTINEL")
			return bareTextResponse("Carrier bare answer", llm.FinishReasonStop)
		}, func(llm.Request) llm.Response {
			return agenttest.FinalResponse("Unexpected bare-text retry")
		}))
		if err := s.ensureClientMutationStore(); err != nil {
			t.Fatal(err)
		}
		evs, evMu, evDone := collectEvents(s)
		got, err := s.ProcessInput(tripwireCtx(t), "Question", nil)
		if err != nil || !carrierSawSteer || got != "Initial reply\nCarrier bare answer" || s.State() != SessionIdle {
			t.Fatalf("reply=%q sawSteer=%t state=%v err=%v", got, carrierSawSteer, s.State(), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 2 || !commEvs[1].EndTurn || commEvs[1].Message != "Carrier bare answer" {
			t.Fatalf("EventCommunicate = %+v, want carrier bare answer emitted", commEvs)
		}
	})

	t.Run("post-quiet notification wake steers", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		var calls int
		s := newSession(t, withEnv(env), withStateDir(t.TempDir()), withSteps(
			func(llm.Request) llm.Response {
				calls++
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				calls++
				return quietYieldResponse("quiet-wait", `{"message":"waiting on go test","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				calls++
				return bareTextResponse("Background job completed: summary", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				calls++
				return agenttest.FinalResponse("Background job completed: summary")
			},
		))
		t.Cleanup(env.releaseJob)
		evs, evMu, evDone := collectEvents(s)
		ctx := tripwireCtx(t)

		got1, err := s.ProcessInput(ctx, "Start background job", nil)
		if err != nil || got1 != "" || !s.quietlyYielded() || s.State() != SessionIdle {
			t.Fatalf("turn 1: reply=%q quiet=%t state=%v err=%v", got1, s.quietlyYielded(), s.State(), err)
		}
		env.releaseJob()
		testwait.Until(t, "background job notification enqueued", func() bool {
			return s.peekNotifications() > 0
		})

		got2, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
		if err != nil || calls != 4 || got2 != "Background job completed: summary" || s.quietlyYielded() || !s.communicated() || s.State() != SessionIdle {
			t.Fatalf("turn 2: reply=%q calls=%d quiet=%t communicated=%t state=%v err=%v", got2, calls, s.quietlyYielded(), s.communicated(), s.State(), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Background job completed: summary" {
			t.Fatalf("EventCommunicate = %+v, want 1 EndTurn=\"Background job completed: summary\"", commEvs)
		}
		turns := transcriptTurnsOf(t, s)
		noToolSteers := 0
		for _, turn := range turns {
			if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindNoToolCalls {
				noToolSteers++
			}
			if turn.Kind == schema.TurnAssistant && turn.QuietYield {
				t.Fatalf("unexpected QuietYield on final-notification bare reply: %q", lastAgentText(turn.Message))
			}
		}
		if noToolSteers != 1 {
			t.Fatalf("noToolSteers = %d, want 1", noToolSteers)
		}
	})
}

func TestRootBareTextFallbackNotifiesCommunicateWatches(t *testing.T) {
	t.Parallel()
	t.Run("watch_parent observer receives EventCommunicate", func(t *testing.T) {
		t.Parallel()
		adapter := newMessagingAdapter("OBSERVER-BRIEF")
		allowObserverArm := make(chan struct{})
		observerFrameReq := make(chan llm.Request, 1)
		var rootTurnCount int
		adapter.script("root",
			func(llm.Request) llm.Response {
				rootTurnCount++
				return delegateToolCall("spawn-obs", map[string]any{
					"prompt":       "OBSERVER-BRIEF: watch parent communicate events.",
					"watch_parent": true,
				})
			},
			func(llm.Request) llm.Response {
				rootTurnCount++
				return agenttest.FinalResponse("Observer starting")
			},
			func(llm.Request) llm.Response {
				rootTurnCount++
				return quietYieldResponse("ack-obs-ready", `{"message":"observer ready","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				rootTurnCount++
				return bareTextResponse("Root bare answer", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				rootTurnCount++
				return agenttest.FinalResponse("Unexpected retry")
			},
		)
		adapter.script("OBSERVER-BRIEF",
			func(llm.Request) llm.Response {
				awaitSignal(t, allowObserverArm, "root turn 1 finished")
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "arm-watch",
					Name:      "job_watch",
					Arguments: json.RawMessage(`{"operation":"create","source":"parent","events":["communicate"]}`),
				})
			},
			func(llm.Request) llm.Response {
				return agenttest.FinalResponse("Observer armed")
			},
			func(req llm.Request) llm.Response {
				select {
				case observerFrameReq <- req:
				default:
				}
				return quietYieldResponse("obs-ack", `{"message":"noted","next":"log_quietly_and_end_turn"}`)
			},
		)
		root := newMessagingRoot(t, adapter, nil)
		evs, evMu, evDone := collectEvents(root)
		ctx := tripwireCtx(t)

		if _, err := root.ProcessInput(ctx, "Start observer", nil); err != nil {
			t.Fatalf("setup turn: %v", err)
		}
		close(allowObserverArm)
		testwait.Until(t, "observer watch armed and initial generation idle", func() bool {
			if root.jobManager.watchCount() != 1 || len(rootPendingAttention(root)) != 1 {
				return false
			}
			root.delegateController.mu.Lock()
			defer root.delegateController.mu.Unlock()
			for _, agg := range root.delegateController.durable {
				return !agg.CurrentRunOpen
			}
			return false
		})
		if _, err := root.ProcessInputKind(ctx, "", nil, EntryNotification); err != nil {
			t.Fatalf("ack observer ready: %v", err)
		}

		got, err := root.ProcessInput(ctx, "Question", nil)
		if err != nil || got != "Root bare answer" || rootTurnCount != 4 || root.State() != SessionIdle {
			t.Fatalf("bare reply=%q calls=%d state=%v err=%v", got, rootTurnCount, root.State(), err)
		}

		var obsReq llm.Request
		select {
		case obsReq = <-observerFrameReq:
		case <-ctx.Done():
			t.Fatal("watch_parent=true observer did not receive watch frame for root bare-text reply")
		}
		if !requestContainsText(obsReq, "kind: communicate") || !requestContainsText(obsReq, "message: Root bare answer") || !requestContainsText(obsReq, "end_turn: true") {
			t.Fatalf("observer request did not carry communicate frame for bare-text reply: %+v", obsReq.Messages)
		}

		testwait.Until(t, "observer attention turn idle", func() bool {
			root.delegateController.mu.Lock()
			defer root.delegateController.mu.Unlock()
			for _, agg := range root.delegateController.durable {
				return !agg.CurrentRunOpen
			}
			return false
		})
		root.Close()
		<-evDone

		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 2 ||
			!commEvs[0].EndTurn || commEvs[0].Message != "Observer starting" ||
			!commEvs[1].EndTurn || commEvs[1].Message != "Root bare answer" {
			t.Fatalf("EventCommunicate = %+v, want setup reply \"Observer starting\" followed by one EndTurn=\"Root bare answer\"", commEvs)
		}

		var turn2ID string
		turns := transcriptTurnsOf(t, root)
		for _, turn := range turns {
			if turn.Kind == schema.TurnUserInput && strings.Contains(turn.Message.Text(), "Question") {
				turn2ID = turn.TurnID
			}
		}
		if turn2ID == "" {
			t.Fatal("missing TurnUserInput for Question")
		}
		var turn2CommEntries, turn2AcceptedAssistant int
		for _, turn := range turns {
			if turn.TurnID != turn2ID {
				continue
			}
			if turn.Kind == schema.TurnCommunicate {
				turn2CommEntries++
			}
			if turn.Kind == schema.TurnAssistant && turn.AcceptedReply {
				turn2AcceptedAssistant++
			}
		}
		if turn2CommEntries != 0 || turn2AcceptedAssistant != 1 {
			t.Fatalf("Turn 2 TurnCommunicate=%d TurnAssistant{AcceptedReply}=%d, want 0 and 1", turn2CommEntries, turn2AcceptedAssistant)
		}
		msgCount, msgTexts := countAgentMessages(t, root.TranscriptPath(), turn2ID)
		if msgCount != 1 || msgTexts[0] != "Root bare answer" {
			t.Fatalf("transcriptindex Turn 2 agentMessages = %q (count=%d), want [\"Root bare answer\"]", msgTexts, msgCount)
		}
	})

	t.Run("self watch fires and single-turn transcript has zero TurnCommunicate", func(t *testing.T) {
		t.Parallel()
		var watchReq llm.Request
		var calls int
		s := newSession(t, withStateDir(t.TempDir()), withSteps(
			func(llm.Request) llm.Response {
				calls++
				return bareTextResponse("Root bare answer", llm.FinishReasonStop)
			},
			func(req llm.Request) llm.Response {
				calls++
				watchReq = req
				return quietYieldResponse("ack-self-watch", `{"message":"noted","next":"log_quietly_and_end_turn"}`)
			},
		))
		watchRes := s.reg.ExecuteCall(context.Background(), s.env, llm.ToolCallData{
			ID:        "watch-self",
			Name:      "job_watch",
			Arguments: json.RawMessage(`{"operation":"create","source":"self","events":["communicate"]}`),
		})
		if watchRes.IsError {
			t.Fatalf("job_watch self: %s", watchRes.Output)
		}
		evs, evMu, evDone := collectEvents(s)
		got, err := s.ProcessInput(tripwireCtx(t), "Question", nil)
		if err != nil || got != "Root bare answer" || s.State() != SessionIdle {
			t.Fatalf("reply=%q err=%v state=%v", got, err, s.State())
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Root bare answer" {
			t.Fatalf("EventCommunicate = %+v, want 1 EndTurn=\"Root bare answer\"", commEvs)
		}
		if calls != 2 || !requestContainsText(watchReq, `reason="event: `+string(events.EventCommunicate)+`"`) {
			t.Fatalf("calls=%d, self watch notification request missing event: %s: %+v", calls, events.EventCommunicate, watchReq.Messages)
		}
		turns := transcriptTurnsOf(t, s)
		if gotComm := len(entriesOfKind(turns, schema.TurnCommunicate)); gotComm != 0 {
			t.Fatalf("TurnCommunicate entries = %d, want 0", gotComm)
		}
		var acceptedAssistants int
		for _, turn := range entriesOfKind(turns, schema.TurnAssistant) {
			if turn.AcceptedReply {
				acceptedAssistants++
			}
		}
		if acceptedAssistants != 1 {
			t.Fatalf("TurnAssistant{AcceptedReply: true} entries = %d, want 1", acceptedAssistants)
		}
		msgCount, msgTexts := countAgentMessages(t, s.TranscriptPath(), "")
		if msgCount != 1 || msgTexts[0] != "Root bare answer" {
			t.Fatalf("transcriptindex agentMessages = %q (count=%d), want [\"Root bare answer\"]", msgTexts, msgCount)
		}
	})

	t.Run("watch provenance preserved for classifySelfInfluenceLocked", func(t *testing.T) {
		t.Parallel()
		s := newSession(t, withStateDir(t.TempDir()), withSteps(
			func(llm.Request) llm.Response {
				return bareTextResponse("Root bare answer", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("ack-self-watch", `{"message":"noted","next":"log_quietly_and_end_turn"}`)
			},
		))
		installWatchBelowValidation(t, s.jobManager, watchArgs{
			Target: runtimeMessageAliasCaller,
			Events: []string{"communicate"},
			Send:   &watchSendArgs{To: runtimeMessageAliasCaller, Message: "observe"},
		})
		cfg := onlyWatchConfigForTest(t, s.jobManager)
		const priorDeliveryID = "wd_prior_1"
		s.jobManager.mu.Lock()
		if s.jobManager.deliveredWatchSendIDs == nil {
			s.jobManager.deliveredWatchSendIDs = make(map[string]struct{})
		}
		s.jobManager.deliveredWatchSendIDs[priorDeliveryID] = struct{}{}
		s.jobManager.mu.Unlock()

		inputProv := provenance.WithWatch(nil, cfg.watchID, cfg.generation, priorDeliveryID, s.ID(), runtimeMessageAliasCaller)
		evs, evMu, evDone := collectEvents(s)
		got, err := s.processInputKindWithProvenance(tripwireCtx(t), "Watch follow-up", nil, EntryUserInput, inputProv)
		if err != nil || got != "Root bare answer" {
			t.Fatalf("reply=%q err=%v", got, err)
		}
		s.Close()
		<-evDone

		evMu.Lock()
		var commEv *events.SessionEvent
		for i := range *evs {
			if (*evs)[i].Kind == events.EventCommunicate {
				ev := (*evs)[i]
				commEv = &ev
				break
			}
		}
		evMu.Unlock()
		if commEv == nil {
			t.Fatal("EventCommunicate was not emitted for bare-text reply")
		}
		if !reflect.DeepEqual(commEv.Provenance, inputProv) {
			t.Fatalf("EventCommunicate provenance = %#v, want exact input provenance %#v", commEv.Provenance, inputProv)
		}
		s.jobManager.mu.Lock()
		influence := s.jobManager.classifySelfInfluenceLocked(cfg, commEv.Provenance)
		s.jobManager.mu.Unlock()
		if !influence.self || influence.gradientDepth != 1 || influence.fuseDepth != 1 {
			t.Fatalf("classifySelfInfluenceLocked = %+v, want self=true gradientDepth=1 fuseDepth=1", influence)
		}
		var pendingDepths []int
		for _, jEv := range loadJobStoreEvents(t, s.jobManager) {
			if jEv.Kind == jobstore.EventWatchSendPending && jEv.WatchSend != nil {
				pendingDepths = append(pendingDepths, jEv.WatchSend.SelfInfluenceDepth)
			}
		}
		if len(pendingDepths) == 0 || pendingDepths[0] != 1 {
			t.Fatalf("watch send pending SelfInfluenceDepth = %v, want 1", pendingDepths)
		}
	})
}

func TestRootBareTextFallbackStopHook(t *testing.T) {
	for _, tc := range []struct {
		name, firstResult, want string
		wantCalls               int
		wantAccepted            string
		wantRetrySubstrings     []string
	}{
		{"allow", `{}`, "First draft", 1, "First draft", nil},
		{
			"block",
			`{"decision":"block","reason":"Try again","hookSpecificOutput":{"hookEventName":"Stop","additionalContext":"REQUIRED_NEXT_ACTION_SENTINEL"}}`,
			"Second draft",
			2,
			"Second draft",
			[]string{"REQUIRED_NEXT_ACTION_SENTINEL"},
		},
		{"replace", `{"continue":false,"stopReason":"Replacement"}`, "Replacement", 1, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var retryReq llm.Request
			s := newSession(t, withSteps(func(llm.Request) llm.Response {
				return bareTextResponse("First draft", llm.FinishReasonStop)
			}, func(req llm.Request) llm.Response {
				retryReq = req
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
			ctx := tripwireCtx(t)
			got, err := s.ProcessInput(ctx, "Question", nil)
			for _, sub := range tc.wantRetrySubstrings {
				if !requestContainsText(retryReq, sub) {
					t.Fatalf("blocked bare-text retry request omitted Stop hook instruction %q (messages=%d)", sub, len(retryReq.Messages))
				}
			}
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
			if tc.wantCalls == 1 {
				var steerSess *Session
				steered := false
				carrierSawSteer := make(chan struct{})
				steerSess = newSession(t, withSteps(func(llm.Request) llm.Response {
					if !steered {
						steered = true
						if _, err := steerSess.AcceptClientMutationSteer(appwire.TurnSteerParams{
							ClientMutationID: "bare-stop-" + tc.name,
							Input:            []appwire.InputItem{{Type: "text", Text: "BARE_CARRIER_STEER_" + tc.name}},
						}); err != nil {
							t.Errorf("AcceptClientMutationSteer: %v", err)
						}
					}
					return bareTextResponse("First draft", llm.FinishReasonStop)
				}, func(req llm.Request) llm.Response {
					if requestContainsText(req, "BARE_CARRIER_STEER_"+tc.name) {
						close(carrierSawSteer)
					}
					return agenttest.FinalResponse("Carrier handled steer")
				}), withStateDir(t.TempDir()))
				if err := steerSess.ensureClientMutationStore(); err != nil {
					t.Fatal(err)
				}
				steerStops := 0
				installCommunicateStopHook(steerSess, func() string {
					steerStops++
					if steerStops == 1 {
						return tc.firstResult
					}
					return `{}`
				})
				if _, err := steerSess.ProcessInput(ctx, "Question", nil); err != nil {
					t.Fatalf("steerSess ProcessInput: %v", err)
				}
				select {
				case <-carrierSawSteer:
				case <-ctx.Done():
					t.Fatalf("client steer queued during %s bare-text reply did not reach carrier turn", tc.name)
				}
			}
			if tc.name == "replace" {
				var followUpReq llm.Request
				stateDir := t.TempDir()
				repSess := newSession(t, withConfig(SessionConfig{StateDir: stateDir, OpenAIResponsesContinuation: "auto"}), withSteps(func(llm.Request) llm.Response {
					r := bareTextResponse("First draft", llm.FinishReasonStop)
					r.ID = "resp_vetoed_draft"
					return r
				}, func(req llm.Request) llm.Response {
					followUpReq = req
					return bareTextResponse("Follow-up answer", llm.FinishReasonStop)
				}))
				repStops := 0
				installCommunicateStopHook(repSess, func() string {
					repStops++
					if repStops == 1 {
						return `{"continue":false,"stopReason":"Replacement"}`
					}
					return `{}`
				})
				if got, err := repSess.ProcessInput(tripwireCtx(t), "Question", nil); err != nil || got != "Replacement" {
					t.Fatalf("replace setup reply=%q err=%v", got, err)
				}
				if _, err := repSess.ProcessInput(tripwireCtx(t), "What did I receive?", nil); err != nil {
					t.Fatalf("follow-up ProcessInput: %v", err)
				}
				if followUpReq.PreviousResponseID != "" || !requestContainsText(followUpReq, "Replacement") {
					t.Fatalf("follow-up PreviousResponseID=%q hasReplacement=%t, want empty anchor and Replacement in messages", followUpReq.PreviousResponseID, requestContainsText(followUpReq, "Replacement"))
				}
				meta := repSess.Meta()
				repSess.Close()

				var resumedReq llm.Request
				resumedClient := llm.NewClient()
				resumedClient.Register(newFakeAdapter("openai", func(req llm.Request) llm.Response {
					resumedReq = req
					return bareTextResponse("After resume", llm.FinishReasonStop)
				}))
				resumed, err := RestoreSessionFromMetaWithConfig(resumedClient, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
				if err != nil {
					t.Fatalf("RestoreSessionFromMetaWithConfig: %v", err)
				}
				defer resumed.Close()
				if _, err := resumed.ProcessInput(tripwireCtx(t), "Check after resume", nil); err != nil {
					t.Fatalf("resumed ProcessInput: %v", err)
				}
				if !requestContainsText(resumedReq, "Replacement") {
					t.Fatalf("resumed follow-up omitted Replacement from model history: %+v", resumedReq.Messages)
				}
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

func TestRootBareTextFallbackAfterSubagentReportSteers(t *testing.T) {
	t.Parallel()
	adapter := newMessagingAdapter("SUM_CHILD")
	childEntered, releaseChild := make(chan struct{}), make(chan struct{})
	var closeRelease atomic.Bool
	release := func() {
		if closeRelease.CompareAndSwap(false, true) {
			close(releaseChild)
		}
	}
	t.Cleanup(release)
	adapter.script("root",
		func(llm.Request) llm.Response {
			return delegateToolResponse("spawn-1", "SUM_CHILD: compute sum")
		},
		func(llm.Request) llm.Response {
			return quietYieldResponse("quiet-wait", `{"message":"Waiting on subagent","next":"log_quietly_and_end_turn"}`)
		},
		func(llm.Request) llm.Response {
			return bareTextResponse("SUB_SUM=78", llm.FinishReasonStop)
		},
		func(llm.Request) llm.Response {
			return agenttest.FinalResponse("SUB_SUM=78")
		},
	)
	adapter.script("SUM_CHILD", func(llm.Request) llm.Response {
		close(childEntered)
		<-releaseChild
		return agenttest.FinalResponse("SUB_SUM=78")
	})
	s := newMessagingRoot(t, adapter, func(c *SessionConfig) {
		*c = minimalTestSessionConfig(t)
		c.StateDir = t.TempDir()
		c.MaxSubagentDepth = 1
	})
	evs, evMu, evDone := collectEvents(s)
	defer func() {
		release()
		s.Close()
		<-evDone
	}()

	ctx := tripwireCtx(t)
	select {
	case <-func() <-chan struct{} {
		ch := make(chan struct{})
		go func() {
			defer close(ch)
			if out, err := s.ProcessInput(ctx, "Compute sum", nil); err != nil || out != "" || !s.quietlyYielded() {
				t.Errorf("initial turn = (%q, %v) quiet=%t, want quiet yield", out, err, s.quietlyYielded())
			}
		}()
		return ch
	}():
	case <-ctx.Done():
		t.Fatal("initial root turn timed out")
	}
	select {
	case <-childEntered:
	case <-ctx.Done():
		t.Fatal("subagent never started")
	}
	release()
	testwait.Until(t, "subagent completion attention ready for root notification turn", func() bool {
		return !s.hasWorkingSubagent() && s.pendingRootDelegateAttention()
	})

	got, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("notification ProcessInputKind: %v", err)
	}
	if got != "SUB_SUM=78" || s.quietlyYielded() || !s.communicated() || len(adapter.requestsFor("root")) != 4 {
		t.Fatalf("notification outcome got=%q quiet=%t communicated=%t rootReqs=%d, want SUB_SUM=78 / false / true / 4",
			got, s.quietlyYielded(), s.communicated(), len(adapter.requestsFor("root")))
	}

	s.Close()
	<-evDone
	evMu.Lock()
	commEvs := communicateEventsIn(*evs)
	evMu.Unlock()
	if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "SUB_SUM=78" {
		t.Fatalf("EventCommunicate = %+v, want 1 EndTurn=\"SUB_SUM=78\"", commEvs)
	}

	var quietAssistants, noToolSteers int
	for _, turn := range transcriptTurnsOf(t, s) {
		switch turn.Kind {
		case schema.TurnAssistant:
			if turn.QuietYield {
				quietAssistants++
			}
		case schema.TurnSteering:
			if turn.SteeringKind == events.SteeringKindNoToolCalls {
				noToolSteers++
			}
		}
	}
	if quietAssistants != 0 || noToolSteers != 1 {
		t.Fatalf("transcript quietAssistants=%d noToolSteers=%d, want 0/1",
			quietAssistants, noToolSteers)
	}
}

func TestRootBareTextFallbackArmedWatchSteers(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	s := newSession(t, withStateDir(t.TempDir()), withSteps(
		func(llm.Request) llm.Response {
			calls.Add(1)
			return bareTextResponse("Checking watch status", llm.FinishReasonStop)
		},
		func(llm.Request) llm.Response {
			calls.Add(1)
			return agenttest.FinalResponse("Done")
		},
	))
	if _, err := s.jobManager.configureWatch(watchArgs{Target: runtimeMessageAliasCaller, Source: "self", AfterSeconds: 3600}); err != nil {
		t.Fatal(err)
	}
	enqueueCompletedJobNotification(t, s, "watch-wake-job")

	got, err := s.ProcessInputKind(tripwireCtx(t), "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("ProcessInputKind: %v", err)
	}
	if got != "Done" || calls.Load() != 2 || s.quietlyYielded() {
		t.Fatalf("got=%q calls=%d quiet=%t, want Done / 2 / false", got, calls.Load(), s.quietlyYielded())
	}
	var quietAssistants, noToolSteers int
	for _, turn := range transcriptTurnsOf(t, s) {
		switch turn.Kind {
		case schema.TurnAssistant:
			if turn.QuietYield {
				quietAssistants++
			}
		case schema.TurnSteering:
			if turn.SteeringKind == events.SteeringKindNoToolCalls {
				noToolSteers++
			}
		}
	}
	if quietAssistants != 0 || noToolSteers != 1 {
		t.Fatalf("quietAssistants=%d noToolSteers=%d, want 0/1", quietAssistants, noToolSteers)
	}
}

func TestRootBareTextFallbackOneShotPostReplyDrainNotificationParksQuietly(t *testing.T) {
	t.Parallel()
	for _, viaTool := range []bool{true, false} {
		name := "after_bare_reply"
		if viaTool {
			name = "after_communicate_reply"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			adapter := newMessagingAdapter("DRAIN_CHILD")
			childEntered, releaseChild := make(chan struct{}), make(chan struct{})
			var closeRelease atomic.Bool
			release := func() {
				if closeRelease.CompareAndSwap(false, true) {
					close(releaseChild)
				}
			}
			t.Cleanup(release)
			if viaTool {
				adapter.script("root",
					func(llm.Request) llm.Response {
						return delegateToolResponse("spawn-1", "DRAIN_CHILD: background work")
					},
					func(llm.Request) llm.Response {
						return agenttest.FinalResponse("REPORT_A")
					},
					func(llm.Request) llm.Response {
						return bareTextResponse("housekeeping", llm.FinishReasonStop)
					},
				)
			} else {
				adapter.script("root",
					func(llm.Request) llm.Response {
						return bareTextResponse("REPORT_A", llm.FinishReasonStop)
					},
					func(llm.Request) llm.Response {
						return bareTextResponse("housekeeping", llm.FinishReasonStop)
					},
				)
			}
			adapter.script("DRAIN_CHILD", func(llm.Request) llm.Response {
				close(childEntered)
				<-releaseChild
				return agenttest.FinalResponse("child finished")
			})
			s := newMessagingRoot(t, adapter, func(c *SessionConfig) {
				*c = minimalTestSessionConfig(t)
				c.StateDir = t.TempDir()
				c.MaxSubagentDepth = 1
				c.TurnEndsProcess = true
			})
			evs, evMu, evDone := collectEvents(s)
			defer func() {
				release()
				s.Close()
				<-evDone
			}()

			ctx := tripwireCtx(t)
			if out, err := s.ProcessInput(ctx, "Start work and reply", nil); err != nil || out != "REPORT_A" {
				t.Fatalf("initial turn = (%q, %v), want REPORT_A", out, err)
			}
			if viaTool {
				select {
				case <-childEntered:
				case <-ctx.Done():
					t.Fatal("subagent never started")
				}
				release()
				testwait.Until(t, "subagent completion attention ready for root notification turn", func() bool {
					return !s.hasWorkingSubagent() && s.pendingRootDelegateAttention()
				})
			} else {
				enqueueCompletedJobNotification(t, s, "trailing-job")
			}

			got, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
			if err != nil {
				t.Fatalf("notification ProcessInputKind: %v", err)
			}
			if got != "" || !s.quietlyYielded() || s.communicated() {
				t.Fatalf("post-reply notification got=%q quiet=%t communicated=%t, want \"\" / true / false",
					got, s.quietlyYielded(), s.communicated())
			}

			s.Close()
			<-evDone
			evMu.Lock()
			commEvs := communicateEventsIn(*evs)
			evMu.Unlock()
			if len(commEvs) != 1 || commEvs[0].Message != "REPORT_A" {
				t.Fatalf("EventCommunicate = %+v, want only REPORT_A", commEvs)
			}
		})
	}
}

func TestRootBareTextFallbackInteractivePostReplyNotificationSteers(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	s := newSession(t, withStateDir(t.TempDir()), withSteps(
		func(llm.Request) llm.Response {
			calls.Add(1)
			return agenttest.FinalResponse("Scheduled.")
		},
		func(llm.Request) llm.Response {
			calls.Add(1)
			return bareTextResponse("Reminder: check deploy", llm.FinishReasonStop)
		},
		func(llm.Request) llm.Response {
			calls.Add(1)
			return agenttest.FinalResponse("Reminder: check deploy")
		},
	))
	evs, evMu, evDone := collectEvents(s)
	ctx := tripwireCtx(t)
	if out, err := s.ProcessInput(ctx, "Remind me in a bit", nil); err != nil || out != "Scheduled." {
		t.Fatalf("initial confirmation = (%q, %v), want Scheduled.", out, err)
	}

	// A one-shot timer or completed notification fires and retires before the
	// wake runs, leaving zero wakeable work at the notification boundary.
	enqueueCompletedJobNotification(t, s, "one-shot-timer")
	got, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
	if err != nil {
		t.Fatalf("notification ProcessInputKind: %v", err)
	}
	if got != "Reminder: check deploy" || calls.Load() != 3 || s.quietlyYielded() || !s.communicated() {
		t.Fatalf("interactive notification got=%q calls=%d quiet=%t communicated=%t, want Reminder: check deploy / 3 / false / true",
			got, calls.Load(), s.quietlyYielded(), s.communicated())
	}

	s.Close()
	<-evDone
	evMu.Lock()
	commEvs := communicateEventsIn(*evs)
	evMu.Unlock()
	if len(commEvs) != 2 || commEvs[0].Message != "Scheduled." || commEvs[1].Message != "Reminder: check deploy" {
		t.Fatalf("EventCommunicate = %+v, want [Scheduled., Reminder: check deploy]", commEvs)
	}
	for _, turn := range transcriptTurnsOf(t, s) {
		if turn.Kind == schema.TurnAssistant && turn.QuietYield {
			t.Fatalf("interactive post-reply reminder was misparked as QuietYield")
		}
	}
}

func TestRootBareTextFallbackUserInputRound0WithEarlierBackgroundJob(t *testing.T) {
	t.Parallel()
	t.Run("round_0_unrelated_user_input_delivers_reply", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		t.Cleanup(env.releaseJob)
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-1", `{"message":"waiting on background shell","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("4", llm.FinishReasonStop)
			},
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		evs, evMu, evDone := collectEvents(s)
		ctx := tripwireCtx(t)

		if out, err := s.ProcessInput(ctx, "Start long build", nil); err != nil || out != "" || !s.quietlyYielded() {
			t.Fatalf("turn 1 = (%q, %v) quiet=%t, want quiet yield", out, err, s.quietlyYielded())
		}
		got, err := s.ProcessInput(ctx, "What is 2+2?", nil)
		if err != nil || got != "4" || !s.communicated() || s.quietlyYielded() || len(adapter.Requests()) != 3 {
			t.Fatalf("turn 2 got=%q communicated=%t quiet=%t reqs=%d err=%v, want \"4\" / true / false / 3 / nil",
				got, s.communicated(), s.quietlyYielded(), len(adapter.Requests()), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "4" {
			t.Fatalf("EventCommunicate = %+v, want [EndTurn=\"4\"]", commEvs)
		}
		var sawAccepted, sawQuiet bool
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Kind == schema.TurnAssistant {
				if turn.AcceptedReply && lastAgentText(turn.Message) == "4" {
					sawAccepted = true
				}
				if turn.QuietYield && lastAgentText(turn.Message) == "4" {
					sawQuiet = true
				}
			}
		}
		if !sawAccepted || sawQuiet {
			t.Fatalf("turn 2 assistant flags sawAccepted=%t sawQuiet=%t, want true/false", sawAccepted, sawQuiet)
		}
	})

	t.Run("round_1_after_starting_job_delivers_reply", func(t *testing.T) {
		t.Parallel()
		env := newHeldJobEnvironment(t.TempDir())
		t.Cleanup(env.releaseJob)
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Started job_123", llm.FinishReasonStop)
			},
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		evs, evMu, evDone := collectEvents(s)
		ctx := tripwireCtx(t)

		got, err := s.ProcessInput(ctx, "Start a background build and tell me its job ID", nil)
		if err != nil || got != "Started job_123" || !s.communicated() || s.quietlyYielded() || len(adapter.Requests()) != 2 {
			t.Fatalf("got=%q communicated=%t quiet=%t reqs=%d err=%v, want \"Started job_123\" / true / false / 2 / nil",
				got, s.communicated(), s.quietlyYielded(), len(adapter.Requests()), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Started job_123" {
			t.Fatalf("EventCommunicate = %+v, want [EndTurn=\"Started job_123\"]", commEvs)
		}
		var sawAccepted, sawQuiet bool
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Kind == schema.TurnAssistant && lastAgentText(turn.Message) == "Started job_123" {
				sawAccepted = turn.AcceptedReply
				sawQuiet = turn.QuietYield
			}
		}
		if !sawAccepted || sawQuiet {
			t.Fatalf("assistant flags sawAccepted=%t sawQuiet=%t, want true/false", sawAccepted, sawQuiet)
		}
	})

	t.Run("round_1_foreground_tool_with_earlier_job_delivers_reply", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.txt")
		if err := os.WriteFile(cfgPath, []byte("port=9180\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		env := newHeldJobEnvironment(dir)
		t.Cleanup(env.releaseJob)
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-1", `{"message":"waiting on background shell","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "read-cfg",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":` + mustJSONString(cfgPath) + `,"intent":"Reading config"}`),
				})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Port 9180.", llm.FinishReasonStop)
			},
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		evs, evMu, evDone := collectEvents(s)
		ctx := tripwireCtx(t)

		if out, err := s.ProcessInput(ctx, "Start long build", nil); err != nil || out != "" || !s.quietlyYielded() {
			t.Fatalf("turn 1 = (%q, %v) quiet=%t, want quiet yield", out, err, s.quietlyYielded())
		}
		got, err := s.ProcessInput(ctx, "Which port is configured?", nil)
		if err != nil || got != "Port 9180." || !s.communicated() || s.quietlyYielded() || len(adapter.Requests()) != 4 {
			t.Fatalf("turn 2 got=%q communicated=%t quiet=%t reqs=%d err=%v, want \"Port 9180.\" / true / false / 4 / nil",
				got, s.communicated(), s.quietlyYielded(), len(adapter.Requests()), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Port 9180." {
			t.Fatalf("EventCommunicate = %+v, want [EndTurn=\"Port 9180.\"]", commEvs)
		}
		var sawAccepted, sawQuiet bool
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Kind == schema.TurnAssistant && lastAgentText(turn.Message) == "Port 9180." {
				sawAccepted = turn.AcceptedReply
				sawQuiet = turn.QuietYield
			}
		}
		if !sawAccepted || sawQuiet {
			t.Fatalf("turn 2 assistant flags sawAccepted=%t sawQuiet=%t, want true/false", sawAccepted, sawQuiet)
		}
	})

	t.Run("typed_root_foreground_tool_with_earlier_job_steers", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.txt")
		if err := os.WriteFile(cfgPath, []byte("port=9180\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		structured, err := provider.WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), map[string]any{"type": "object", "additionalProperties": true})
		if err != nil {
			t.Fatal(err)
		}
		env := newHeldJobEnvironment(dir)
		t.Cleanup(env.releaseJob)
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-1", `{"message":"waiting on background shell","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "read-cfg",
					Name:      "read_file",
					Arguments: json.RawMessage(`{"file_path":` + mustJSONString(cfgPath) + `,"intent":"Reading config"}`),
				})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Port 9180.", llm.FinishReasonStop)
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("typed-reply", `{"message":"Port 9180.","output":{"port":9180}}`)
			},
		)
		s := newSession(t, withProfile(structured), withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		ctx := tripwireCtx(t)

		if out, err := s.ProcessInput(ctx, "Start long build", nil); err != nil || out != "" || !s.quietlyYielded() {
			t.Fatalf("turn 1 = (%q, %v) quiet=%t, want quiet yield", out, err, s.quietlyYielded())
		}
		got, err := s.ProcessInput(ctx, "Which port is configured?", nil)
		if err != nil || !strings.Contains(got, "9180") || !s.communicated() || s.quietlyYielded() || len(adapter.Requests()) != 5 {
			t.Fatalf("turn 2 got=%q communicated=%t quiet=%t reqs=%d err=%v, want port 9180 / true / false / 5 / nil",
				got, s.communicated(), s.quietlyYielded(), len(adapter.Requests()), err)
		}
		var noToolSteers int
		for _, turn := range transcriptTurnsOf(t, s) {
			if turn.Kind == schema.TurnSteering && turn.SteeringKind == events.SteeringKindNoToolCalls {
				noToolSteers++
			}
			if turn.Kind == schema.TurnAssistant && turn.QuietYield && lastAgentText(turn.Message) == "Port 9180." {
				t.Fatal("typed root foreground reply was wrongly parked as QuietYield")
			}
		}
		if noToolSteers != 1 {
			t.Fatalf("noToolSteers = %d, want 1", noToolSteers)
		}
	})

	t.Run("round_1_starts_second_job_while_first_runs_delivers_reply", func(t *testing.T) {
		t.Parallel()
		env := newTwoShellDrainEnvironment(t.TempDir())
		t.Cleanup(func() {
			env.finishFirst()
			env.finishSecond()
		})
		adapter := newFakeAdapter("openai",
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell-1",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./agent","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return quietYieldResponse("quiet-1", `{"message":"waiting on first shell","next":"log_quietly_and_end_turn"}`)
			},
			func(llm.Request) llm.Response {
				return agenttest.ToolCallResponse(llm.ToolCallData{
					ID:        "bg-shell-2",
					Name:      "shell",
					Arguments: json.RawMessage(`{"command":"go test ./server","mode":"background"}`),
				})
			},
			func(llm.Request) llm.Response {
				return bareTextResponse("Started second job.", llm.FinishReasonStop)
			},
		)
		s := newSession(t, withEnv(env), withAdapter(adapter), withStateDir(t.TempDir()))
		evs, evMu, evDone := collectEvents(s)
		ctx := tripwireCtx(t)

		if out, err := s.ProcessInput(ctx, "Start first build", nil); err != nil || out != "" || !s.quietlyYielded() {
			t.Fatalf("turn 1 = (%q, %v) quiet=%t, want quiet yield", out, err, s.quietlyYielded())
		}
		got, err := s.ProcessInput(ctx, "Start second build", nil)
		if err != nil || got != "Started second job." || s.quietlyYielded() || !s.communicated() || len(adapter.Requests()) != 4 {
			t.Fatalf("turn 2 got=%q quiet=%t communicated=%t reqs=%d err=%v, want \"Started second job.\" / false / true / 4 / nil",
				got, s.quietlyYielded(), s.communicated(), len(adapter.Requests()), err)
		}
		s.Close()
		<-evDone
		evMu.Lock()
		commEvs := communicateEventsIn(*evs)
		evMu.Unlock()
		if len(commEvs) != 1 || !commEvs[0].EndTurn || commEvs[0].Message != "Started second job." {
			t.Fatalf("EventCommunicate = %+v, want [EndTurn=\"Started second job.\"]", commEvs)
		}
	})
}

func TestQuietYieldRenamedResultToolHasNoProjectedToolCard(t *testing.T) {
	const name = "respond"
	s := newSession(t, withConfig(SessionConfig{ResultToolName: name}), withSteps(func(llm.Request) llm.Response {
		call := rawCommunicateCall("quiet", `{"message":"waiting on watch","next":"log_quietly_and_end_turn"}`)
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
