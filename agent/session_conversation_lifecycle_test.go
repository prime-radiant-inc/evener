package agent

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

func conversationLifecycleResponse(req llm.Request) llm.Response {
	call := communicateCall("lifecycle-output", "opaque-result-483")
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
}

func newConversationLifecycleSession(t *testing.T) *Session {
	t.Helper()
	cfg := minimalTestSessionConfig(t)
	cfg.LLMRetryPolicy = &llm.RetryPolicy{MaxRetries: 1}
	cfg.LLMSleep = func(context.Context, time.Duration) error { return nil }
	s := newScriptedSummaryCompactSession(t, "lifecycle-summary", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\n## Progress\nRetain the input history.\n[END SUMMARY]")}
	}, withConfig(cfg), withStateDir(t.TempDir()))
	s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{conversationLifecycleResponse, conversationLifecycleResponse, conversationLifecycleResponse, conversationLifecycleResponse}})
	for i := range 4 {
		if _, err := s.ProcessInput(context.Background(), fmt.Sprintf("opaque-seed-%d", i), nil); err != nil {
			t.Fatal(err)
		}
	}
	s.contextMgr.PreserveRecentTurns = 0
	return s
}

func restoreConversationLifecycleSession(t *testing.T, s *Session) *Session {
	t.Helper()
	s.Close()
	meta, err := schema.LoadSessionMeta(s.stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	restored, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(s.stateDir), meta, RestoreSessionConfig{StateDir: s.stateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	return restored
}

func TestConversationProvenance_FailedSelectionLifecycle(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"skill", "command"} {
		for _, recovery := range []string{"live", "restore", "fold-restore"} {
			t.Run(kind+"/"+recovery, func(t *testing.T) {
				t.Parallel()
				s := newConversationLifecycleSession(t)
				old := interruptedConversationOutput(t, s)
				params := appwire.TurnSteerParams{ClientMutationID: "failed-selection", Input: []appwire.InputItem{{Type: "text", Text: "opaque-failed-729"}, {Type: kind, Name: "absent-selection"}}}
				accepted, err := s.AcceptClientMutationSteer(params)
				if err != nil {
					t.Fatal(err)
				}
				id := s.clientMutations.snapshot().Journal[params.ClientMutationID].ConversationInputIDs[0]
				state := s.ConversationProvenance()
				if slices.Contains(state.RetiredInputs, id) || appwire.ConversationOutputEligible(state, old) {
					t.Fatal("accepted selection was retired or failed to fence old output")
				}
				messages := s.drainSteering()
				if len(messages) != 1 || s.consumeSteeringMessage(messages[0]) != steeringRetired {
					t.Fatal("real selected input did not fail preparation")
				}
				check := func() {
					t.Helper()
					journal := s.clientMutations.snapshot()
					record := journal.Journal[params.ClientMutationID]
					if record.OperationState != clientMutationOperationTerminal || record.ExecutionState != "failed" || len(record.Payload) != 0 || len(journal.PendingExecutions) != 0 {
						t.Fatalf("selection did not terminalize: %+v", record)
					}
					canonical, failures := 0, 0
					for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
						if entry.Turn.OriginalOrdinal != nil {
							continue
						}
						if slices.Contains(entry.Turn.ConversationInputIDs, id) {
							canonical++
						}
						if entry.Turn.ClientMutationID == params.ClientMutationID && entry.Turn.Kind == schema.TurnFailure {
							failures++
						}
					}
					if canonical != 0 || failures != 1 {
						t.Fatalf("canonical input records=%d failure records=%d", canonical, failures)
					}
					replayed, replayErr := s.AcceptClientMutationSteer(params)
					want := accepted.Receipt
					want.Disposition = appwire.MutationDispositionReplayed
					want.ProjectionState = appwire.MutationProjectionReflected
					if replayErr != nil || !reflect.DeepEqual(want, replayed.Receipt) {
						t.Fatalf("receipt replay changed: %+v err=%v", replayed, replayErr)
					}
					t.Logf("%s failure: canonical input records=%d, failure records=%d, pending=%d, exact receipt replay preserved", kind, canonical, failures, len(journal.PendingExecutions))
				}
				check()
				if recovery == "fold-restore" {
					if err := s.Compact(context.Background()); err != nil {
						t.Fatal(err)
					}
					requireConversationSummary(t, s)
				}
				if recovery != "live" {
					s = restoreConversationLifecycleSession(t, s)
					check()
				}
				s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(req llm.Request) llm.Response {
					if !conversationRequestContains(req, "opaque-fresh-382") || conversationRequestContains(req, "opaque-failed-729") {
						t.Error("fresh request omitted its input or consumed failed selection")
					}
					return conversationLifecycleResponse(req)
				}}})
				if _, err := s.ProcessInput(context.Background(), "opaque-fresh-382", nil); err != nil {
					t.Fatal(err)
				}
				state = s.ConversationProvenance()
				output := interruptedConversationOutput(t, s)
				if !slices.Contains(state.RetiredInputs, id) || !appwire.ConversationOutputEligible(state, output) {
					t.Errorf("fresh committed output ineligible after definitive selection failure: retired=%v authoritative=%t", state.RetiredInputs, state.Authoritative)
				}
			})
		}
	}
}

func requireConversationSummary(t *testing.T, s *Session) {
	t.Helper()
	for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
		if entry.Turn.Kind == schema.TurnSummary && entry.Turn.OriginalOrdinal == nil {
			return
		}
	}
	t.Fatal("real summarizer did not publish a summary")
}

func TestConversationProvenance_FoldDuringDispatchedRequest(t *testing.T) {
	t.Parallel()
	for _, fold := range []string{"summary", "summary-tail", "checkpoint"} {
		for _, incorporate := range []bool{false, true} {
			for _, retry := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/incorporated=%t/retry=%t", fold, incorporate, retry), func(t *testing.T) {
					t.Parallel()
					s := newConversationLifecycleSession(t)
					if fold == "summary-tail" {
						s.contextMgr.PreserveRecentTurns = 6
					}
					if fold == "checkpoint" {
						s.client.Register(&conversationRetryAdapter{fakeAdapter: &fakeAdapter{name: "lifecycle-summary"}, complete: func(llm.Request) (llm.Response, error) {
							return llm.Response{}, llm.ErrorFromHTTPStatus("lifecycle-summary", 400, "opaque-summary-failure", nil, nil)
						}})
					}
					var frozen []string
					calls := 0
					compacting := false
					s.client.Register(&conversationRetryAdapter{fakeAdapter: &fakeAdapter{name: "openai"}, complete: func(req llm.Request) (llm.Response, error) {
						if compacting {
							return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "opaque-summary-fallback-failure", nil, nil)
						}
						calls++
						attempts := 1
						if retry {
							attempts = 2
						}
						if calls > attempts {
							if !conversationRequestContains(req, "opaque-later-682") {
								t.Fatal("pending correction did not reach subsequent request")
							}
							return conversationLifecycleResponse(req), nil
						}
						if !conversationRequestContains(req, "opaque-dispatched-471") || conversationRequestContains(req, "opaque-later-682") {
							t.Fatal("actual dispatched request input set changed")
						}
						if calls == 1 {
							frozen = slices.Clone(s.ConversationProvenance().AdmittedInputs)
							before := s.ConversationProvenance().Frontiers
							if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "later-correction", Input: []appwire.InputItem{{Type: "text", Text: "opaque-later-682"}}}); err != nil {
								t.Fatal(err)
							}
							if incorporate {
								messages := s.drainSteering()
								if len(messages) != 1 || s.consumeSteeringMessage(messages[0]) != steeringDelivered {
									t.Fatal("correction did not incorporate")
								}
							}
							compacting = true
							if err := s.Compact(context.Background()); err != nil {
								t.Fatal(err)
							}
							compacting = false
							if fold == "checkpoint" {
								found := false
								for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
									found = found || (entry.Turn.Kind == schema.TurnCheckpoint && entry.Turn.ConversationCoverageRef != "")
								}
								if !found {
									t.Fatal("failed summarizer did not publish checkpoint coverage")
								}
							} else {
								requireConversationSummary(t, s)
							}
							copies, corrections := 0, 0
							for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
								if entry.Turn.OriginalOrdinal != nil {
									copies++
								} else if slices.Contains(entry.Turn.ConversationInputIDs, "client:later-correction") {
									corrections++
								}
							}
							if (incorporate && corrections != 1) || (!incorporate && corrections != 0) || (fold == "summary-tail" && copies == 0) {
								t.Fatalf("fold branch not exercised: correction records=%d retained copies=%d", corrections, copies)
							}
							after := s.ConversationProvenance().Frontiers
							if len(after) <= len(before) || after[len(after)-1].ID == before[len(before)-1].ID {
								t.Fatal("compaction did not advance canonical frontier during actual provider dispatch")
							}
							t.Logf("provider dispatched, compaction advanced frontier nodes %d -> %d, frozen inputs=%d incorporated correction=%t", len(before), len(after), len(frozen), incorporate)
							if retry {
								return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "retry", nil, nil)
							}
						}
						call := communicateCall("dispatched-output", "opaque-output-571")
						return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}, nil
					}})
					if _, err := s.ProcessInput(context.Background(), "opaque-dispatched-471", nil); err != nil {
						t.Fatal(err)
					}
					wantCalls := 1
					if retry {
						wantCalls = 2
					}
					if !incorporate {
						wantCalls++
					}
					if calls != wantCalls {
						t.Fatalf("provider attempts=%d want=%d", calls, wantCalls)
					}
					var output *appwire.ConversationOutputProvenance
					for index, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
						if entry.Turn.Communicate != nil && entry.Turn.Communicate.CallID == "dispatched-output" {
							item, ok := apptranscript.CommunicateItem(entry.Turn.TurnID, index, entry.Turn)
							if ok {
								output = item.ConversationProvenance
							}
						}
					}
					if output == nil {
						t.Fatal("dispatched response never committed public output")
					}
					check := func() {
						t.Helper()
						state := s.ConversationProvenance()
						covered, ok := appwire.ResolveConversationFrontier(state, output.FrontierID)
						if !state.Authoritative || !ok || !slices.Equal(covered, frozen) {
							t.Errorf("published response lost frozen coverage: authoritative=%t resolved=%t got=%v want=%v", state.Authoritative, ok, covered, frozen)
						}
						if appwire.ConversationOutputEligible(state, output) {
							t.Error("old response inherited correction admitted after dispatch")
						}
						previous := ""
						for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
							if entry.Turn.OriginalOrdinal != nil {
								continue
							}
							if node := entry.Turn.ConversationFrontier; node != nil {
								if node.BaseID != previous {
									t.Errorf("canonical frontier base=%s want=%s", node.BaseID, previous)
								}
								previous = node.ID
							}
						}
					}
					check()
					for _, recovery := range []string{"live", "restore"} {
						if recovery == "restore" {
							s = restoreConversationLifecycleSession(t, s)
							check()
						}
						s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{conversationLifecycleResponse}})
						if _, err := s.ProcessInput(context.Background(), "opaque-next-593-"+recovery, nil); err != nil {
							t.Fatal(err)
						}
						state := s.ConversationProvenance()
						fresh := interruptedConversationOutput(t, s)
						coverage, ok := appwire.ResolveConversationFrontier(state, fresh.FrontierID)
						wantInputs := len(frozen) + 2
						if recovery == "restore" {
							wantInputs++
						}
						if !ok || len(coverage) != wantInputs || !slices.Contains(coverage, "client:later-correction") || !appwire.ConversationOutputEligible(state, fresh) {
							t.Errorf("subsequent response ineligible or lost correction: authoritative=%t coverage=%v", state.Authoritative, coverage)
						}
					}
				})
			}
		}
	}
}

func TestConversationProvenance_FailedIncorporatedInputRemainsRequired(t *testing.T) {
	t.Parallel()
	for _, recovery := range []string{"live", "restore", "fold-restore"} {
		t.Run(recovery, func(t *testing.T) {
			t.Parallel()
			s := newConversationLifecycleSession(t)
			old := interruptedConversationOutput(t, s)
			params := appwire.TurnStartParams{ClientMutationID: "failed-after-incorporation", Input: []appwire.InputItem{{Type: "text", Text: "opaque-incorporated-173"}}}
			if _, err := s.AcceptClientMutationStart(params); err != nil {
				t.Fatal(err)
			}
			calls := 0
			s.client.Register(&conversationRetryAdapter{fakeAdapter: &fakeAdapter{name: "openai"}, complete: func(req llm.Request) (llm.Response, error) {
				calls++
				if !conversationRequestContains(req, "opaque-incorporated-173") {
					t.Error("failed provider request never consumed admitted input")
				}
				record := s.clientMutations.snapshot().Journal[params.ClientMutationID]
				if record.ExecutionState != "incorporated" || slices.Contains(s.ConversationProvenance().RetiredInputs, record.ConversationInputIDs[0]) {
					t.Fatal("pending or retryable incorporated input was retired")
				}
				if calls == 1 {
					return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 503, "opaque-retry", nil, nil)
				}
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "opaque-failure", nil, nil)
			}})
			if _, ran, err := s.ProcessClientMutationStart(context.Background(), nil); !ran || err == nil || calls != 2 {
				t.Fatalf("failed start ran=%t err=%v calls=%d", ran, err, calls)
			}
			check := func() {
				t.Helper()
				record := s.clientMutations.snapshot().Journal[params.ClientMutationID]
				if record.ExecutionState != "failed" || record.OperationState != clientMutationOperationTerminal {
					t.Fatalf("canonical input did not terminalize failed: %+v", record)
				}
				id := record.ConversationInputIDs[0]
				canonical := 0
				for _, entry := range readPersistedTranscriptEntries(t, s.stateDir, s.ID()) {
					if entry.Turn.OriginalOrdinal == nil && slices.Contains(entry.Turn.ConversationInputIDs, id) {
						canonical++
					}
				}
				state := s.ConversationProvenance()
				if canonical != 1 || !state.Authoritative || slices.Contains(state.RetiredInputs, id) || appwire.ConversationOutputEligible(state, old) {
					t.Fatalf("incorporated failure stopped fencing old reply: canonical=%d state=%+v", canonical, state)
				}
				t.Logf("failed after provider dispatch: canonical input records=%d, retired=false", canonical)
			}
			check()
			if recovery == "fold-restore" {
				if err := s.Compact(context.Background()); err != nil {
					t.Fatal(err)
				}
				requireConversationSummary(t, s)
			}
			if recovery != "live" {
				s = restoreConversationLifecycleSession(t, s)
				check()
			}
			s.client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{conversationLifecycleResponse}})
			if _, err := s.ProcessInput(context.Background(), "opaque-recovery-184", nil); err != nil {
				t.Fatal(err)
			}
			if !appwire.ConversationOutputEligible(s.ConversationProvenance(), interruptedConversationOutput(t, s)) {
				t.Fatal("fresh response after canonical failure is ineligible")
			}
		})
	}
}

func TestConversationProvenance_UnsettledAndRejectedAdmission(t *testing.T) {
	t.Parallel()
	s := newConversationLifecycleSession(t)
	old := interruptedConversationOutput(t, s)
	if _, err := s.AcceptClientMutationStart(appwire.TurnStartParams{ClientMutationID: "pending-start", Input: []appwire.InputItem{{Type: "text", Text: "opaque-pending-715"}}}); err != nil {
		t.Fatal(err)
	}
	inspected := false
	if _, err := s.InterruptClientMutation(context.Background(), appwire.TurnInterruptParams{ClientMutationID: "inflight-stop"}, func() {
		inspected = true
		refused := appwire.TurnSteerParams{ClientMutationID: "rejected-during-stop", Input: []appwire.InputItem{{Type: "text", Text: "opaque-refused-826"}}}
		if _, err := s.AcceptClientMutationSteer(refused); err == nil {
			t.Fatal("input accepted while interrupt fence was pending")
		}
		before := s.clientMutations.snapshot()
		if before.Journal["inflight-stop"].OperationState != clientMutationOperationInFlight || before.Journal[refused.ClientMutationID].OperationState != clientMutationOperationRejected || before.Journal["pending-start"].ExecutionState != "accepted" {
			t.Fatal("real unsettled/rejected admission states not reached")
		}
		state := s.ConversationProvenance()
		if !state.Authoritative || slices.Contains(state.RetiredInputs, "client:pending-start") || slices.Contains(state.AdmittedInputs, "client:"+refused.ClientMutationID) || len(state.RetiredInputs) != 0 || appwire.ConversationOutputEligible(state, old) {
			t.Fatalf("unsettled input retired or rejected input reclassified: %+v", state)
		}
		if !reflect.DeepEqual(before, s.clientMutations.snapshot()) {
			t.Fatal("provenance projection mutated the admission journal")
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !inspected {
		t.Fatal("inflight interrupt boundary never executed")
	}
	if !slices.Contains(s.ConversationProvenance().RetiredInputs, "client:pending-start") {
		t.Fatal("settled interrupted start was not retired")
	}
}
