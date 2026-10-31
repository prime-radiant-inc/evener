package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/task"
	"primeradiant.com/evener/llm"
)

// A valid waiting_on_work call yields a durable checkpoint rather than entering
// the ordinary finalization drain. A child's report must resume that wait too.
func TestNotificationReportWakesPollingParent(t *testing.T) {
	t.Parallel()
	notificationReportWakesParent(t, true)
}

func rootDispositionRegistry(t *testing.T, validate func(context.Context, string) error) (*tool.Registry, *[]events.CommunicateData) {
	t.Helper()
	delivered := []events.CommunicateData{}
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{
		StateDir: t.TempDir(), testOnly: testConfig{minimalSystemPrompt: true},
	}))
	deps := &toolDeps{
		emit: func(events.EventKind, events.EventData) {},
		deliverCommunicate: func(data events.CommunicateData) error {
			delivered = append(delivered, data)
			return nil
		},
		abort:           func(context.Context) error { return nil },
		drainSteering:   func() []steeringMessage { return nil },
		prependSteering: func([]steeringMessage) {},
		resultToolName:  func() string { return "communicate" },
		// Terminal capture owns publication, unlike a nonterminal update.
		setCommunicateTerminal: func(ctx context.Context, info schema.CommunicateInfo, reply, output string, structured any) (bool, error) {
			accepted, err := s.acceptCommunicateTerminal(ctx, info, reply, output, structured)
			for {
				select {
				case event := <-s.Events():
					if data, ok := event.Data.(events.CommunicateData); ok {
						delivered = append(delivered, data)
					}
				default:
					return accepted, err
				}
			}
		},
		offersEndReason:    true,
		validateWaitTarget: validate,
	}
	reg := tool.NewRegistry()
	if err := registerCommunicateTool(reg, deps); err != nil {
		t.Fatalf("register communicate: %v", err)
	}
	return reg, &delivered
}

func TestDelegateReportExplicitDoneSettles(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, nil)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "acknowledged",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonDone,
	}))
	if res.IsError {
		t.Fatalf("done report: %s", res.Output)
	}
	if len(*delivered) != 1 || (*delivered)[0].EndReason != tool.CommunicateEndReasonDone {
		t.Fatalf("delivered = %+v, want one done settlement", *delivered)
	}
}

func TestDelegateReportWaitingWithoutWakerIsRefused(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, func(context.Context, string) error {
		return errors.New("no live waker")
	})
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "waiting",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonWaitingOnWork,
		"waiting_on": "job-1",
	}))
	if !res.IsError {
		t.Fatalf("result = %+v, want the absent waker refused", res)
	}
	if len(*delivered) != 0 {
		t.Fatalf("delivered = %+v, want nothing delivered on a refusal", *delivered)
	}
}

func TestDelegateReportNeedsResponseKeepsExistingQuestion(t *testing.T) {
	reg, delivered := rootDispositionRegistry(t, nil)
	res := reg.ExecuteCall(context.Background(), nil, communicateCallArgs("comm-1", map[string]any{
		"message":    "which one?",
		"end_turn":   true,
		"end_reason": tool.CommunicateEndReasonNeedsResponse,
	}))
	if res.IsError {
		t.Fatalf("needs_response: %s", res.Output)
	}
	if len(*delivered) != 1 || (*delivered)[0].EndReason != tool.CommunicateEndReasonNeedsResponse {
		t.Fatalf("delivered = %+v, want one needs_response question", *delivered)
	}
}

// Labels are payload semantics, not a reason to demand a second model round.
// Exercise the real request, synced response and raw consumption records rather
// than route helpers. An unfinished task is neither input nor a live producer.
func TestNotificationLabelsShareIdleDisposition(t *testing.T) {
	t.Parallel()
	for _, id := range []string{
		"delegate:dlg_target/delivery/1",
		"delegate:dlg_target/update/1",
		"watch:label-731a",
		"system:label-731a",
	} {
		t.Run(id, func(t *testing.T) {
			a := &skillContinuationAdapter{}
			s := observationSession(t, a, false)
			if _, err := s.getOrCreateTaskStore().Append([]task.TaskInput{{Description: "stale-task-731a"}}); err != nil {
				t.Fatal(err)
			}
			const body = "label-payload-731a"
			observationEnqueue(t, s, id, body)
			a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
				observationRequest(t, req, []string{body}, nil)
				return llm.Response{Message: llm.Assistant("label-answer-731a")}, nil
			}}
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if got := len(a.Requests()); got != 1 {
				t.Fatalf("provider requests = %d, want one independent of payload label", got)
			}
			observationReceipts(t, s, [][]string{{id}})
			accepted, consumed := 0, 0
			for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
				if entry.Turn.AttentionID == id {
					accepted++
				}
				if r := entry.Turn.AttentionResolution; r != nil && r.AttentionID == id && r.Disposition == string(delegateAttentionConsumed) {
					consumed++
				}
			}
			if accepted != 1 || consumed != 1 {
				t.Fatalf("acceptances=%d consumed=%d, want one each", accepted, consumed)
			}
			assertNotificationPending(t, s, []string{})
			if s.State() != SessionIdle || s.sessionWorkPending() {
				t.Fatalf("state=%v pending=%t with only stale task", s.State(), s.sessionWorkPending())
			}
			// A second empty wake must not turn the stale task into model work.
			if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
				t.Fatal(err)
			}
			if len(a.Requests()) != 1 || len(s.getOrCreateTaskStore().View()) != 1 {
				t.Fatalf("empty wake requests=%d tasks=%d", len(a.Requests()), len(s.getOrCreateTaskStore().View()))
			}
		})
	}
}
