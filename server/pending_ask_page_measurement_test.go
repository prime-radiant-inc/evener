package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

type pendingAskMeasurementSource struct {
	pendingAskEnvelopeSource
	frontier string
}

func (s *pendingAskMeasurementSource) LatestExecutionTurnID() string { return s.frontier }

// The finite backing population is shared with the hub measurement through a
// real daemon socket. Keep the original daemon projection and normalizer checks
// here, and the whole-result maximality oracle at the final hub boundary.
func TestPendingAskPageWholeResponseMeasurement(t *testing.T) {
	t.Parallel()
	managerRef := "local:" + identifier.MustNewSessionID()
	for _, turnID := range []string{identifier.MustNewTurnID(), appwire.ClientMutationTurnID(4000), appwire.ClientMutationTurnID(math.MaxUint64)} {
		t.Run(turnID, func(t *testing.T) {
			sessionID := identifier.MustNewSessionID()
			s, state := newPendingAskMeasurement(t, sessionID, turnID)
			huge := math.MaxInt
			read, err := s.handleAppThreadRead(appwire.WithHistoryServingFacts(context.Background()), appwire.ThreadReadParams{Ref: "local:" + state.InstanceID, ExpectedAskRevision: &state.Revision, ExpectedAskBootGeneration: state.BootGeneration, Limit: &huge})
			if err != nil {
				t.Fatal(err)
			}
			if read.PendingAskPage == nil || len(read.PendingAskPage.Composites) != appwire.MaxPendingAskPageLimit {
				t.Fatalf("huge limit was not normalized: %+v", read.PendingAskPage)
			}
			supported := true
			read.TargetSessionToolsSupported = &supported
			if read.ServingFacts == nil || read.ServingFacts.ServedFrom != "live-daemon" || read.ServingFacts.InstanceID != state.InstanceID || read.ServingFacts.AsOf == "" || read.Thread.Evener.LatestExecutionTurnID != turnID {
				t.Fatalf("live serving cut omitted measurement fields: %+v", read)
			}
			if read.Epoch != math.MaxUint64 || read.Thread.Evener.ActiveTurnID != turnID {
				t.Fatalf("page omitted epoch or active execution: %+v", read)
			}
			q := read.Thread.Evener.PendingQuestion
			if q == nil || len(q.Question) != appwire.MaxQuestionTextRunes || len(q.Options) != appwire.MaxQuestionOptions || q.Count != 4002 || q.TurnID != turnID || q.CallID != state.Calls[0].CallID || q.Complete == nil || *q.Complete || q.Truncated == nil || !*q.Truncated {
				t.Fatalf("page omitted maximal question fields: %+v", q)
			}
			for _, label := range q.Options {
				if len(label) != appwire.MaxQuestionOptionRunes {
					t.Fatalf("option bytes=%d", len(label))
				}
			}
			// Keep the original low-level target-tools=true population and widest
			// UTC timestamp in the serialized response, not just unused setup.
			read.ServingFacts.AsOf = time.Date(2026, time.November, 8, 0, 0, 0, 999999999, time.UTC).Format(time.RFC3339Nano)
			pageRead := appwire.HistoryReadResponse(read)
			if pageRead.LatestExecutionTurnID != turnID || !pageRead.Authoritative || pageRead.ServingFactsUnavailableReason != "" || pageRead.FallbackReason != "" {
				t.Fatalf("normalizer dropped or combined live serving facts: %+v", pageRead)
			}
			if len(read.Thread.Evener.PendingAsk.Calls) != 10 || !read.Thread.Evener.PendingAsk.CallsTruncated || read.Thread.Evener.PendingAsk.CompositeTotal != 4000 {
				t.Fatalf("row not clamped: %+v", read.Thread.Evener.PendingAsk)
			}
			response := appwire.SessionOperationResponse{Actor: appwire.SessionActor{Kind: "manager", ManagerRef: managerRef}, Read: &pageRead, FollowUps: []appwire.SessionStatusFollowUp{{Tool: "job_status", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: read.Thread.Evener.Ref}}}}}
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) >= 262144 {
				t.Fatal("low-level normalized page exceeds whole Manager-result cap")
			}
			var decoded appwire.SessionOperationResponse
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Read == nil || decoded.Read.TargetSessionToolsSupported == nil || !*decoded.Read.TargetSessionToolsSupported {
				t.Fatal("low-level target-tools=true population was not serialized")
			}
		})
	}
}

// newPendingAskMeasurement deliberately retains the original finite data:
// 4,000 composites, three turn-ID forms supplied by the callers, maximal uint64
// incarnation/revision/epoch, a running frontier, and maximal escaped excerpt.
func newPendingAskMeasurement(t *testing.T, sessionID, turnID string) (*Server, *appwire.PendingAskState) {
	t.Helper()
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	s.SetAppIdentity("local", sessionID)
	s.appBootGeneration = appwire.BootGeneration(math.MaxUint64)
	// A page copies the history epoch without projecting history. It can
	// also observe asks while their execution is still running.
	s.ensureHistory(sessionID, "local:"+sessionID, filepath.Join(t.TempDir(), "transcript.jsonl"), 0, math.MaxUint64, s.appBootGeneration)
	s.SetProcessingTurn(turnID)
	state := &appwire.PendingAskState{Version: 1, InstanceID: sessionID, BootGeneration: s.appBootGeneration, Revision: math.MaxUint64, CalllessCount: 2, CompositeTotal: 4000}
	for i := range 4000 {
		state.Calls = append(state.Calls, appwire.PendingAskCall{TurnID: turnID, CallID: fmt.Sprintf("call_%024d", i)})
	}
	labels := make([]string, appwire.MaxQuestionOptions)
	for i := range labels {
		labels[i] = strings.Repeat("<", appwire.MaxQuestionOptionRunes)
	}
	question := appwire.BoundedPendingQuestion(strings.Repeat("<", appwire.MaxQuestionTextRunes), labels, 4002)
	complete, truncated := false, true
	question.TurnID = turnID
	question.CallID = state.Calls[0].CallID
	question.Complete = &complete
	question.Truncated = &truncated
	source := &pendingAskMeasurementSource{frontier: turnID}
	source.question = &question
	source.sample = func() *appwire.PendingAskState { return state }
	s.SetThreadEnvelopeSource(source)
	s.RefreshThreadEnvelope()
	return s, state
}

// This adapter forwards actual Session metadata and provenance. Only the pending
// ask population is synthetic, exactly as in the original projection fixture.
type pendingAskManagerMeasurementSource struct {
	*pendingAskMeasurementSource
	session *agent.Session
}

func (s *pendingAskManagerMeasurementSource) SessionMeta() schema.SessionMeta {
	return s.session.Meta()
}
func (s *pendingAskManagerMeasurementSource) ConversationProvenance() *appwire.ConversationProvenanceState {
	return s.session.ConversationProvenance()
}

// TestPendingAskPageMeasurementDaemon is the shared test harness, not a second
// measurement oracle or production seam. The hub test compiles this package's
// test binary and starts only this helper, with configuration on stdin and an
// actual-ready socket inventory on fd 3. Closing stdin requests orderly teardown.
func TestPendingAskPageMeasurementDaemon(t *testing.T) {
	if os.Getenv("EVENER_PENDING_ASK_MEASUREMENT_CHILD") != "1" {
		t.Skip("started only by the final hub measurement")
	}
	var config struct {
		WorkingDir, StateDir string
		Sessions             []struct{ SessionID, TurnID string }
	}
	if err := json.NewDecoder(os.Stdin).Decode(&config); err != nil {
		t.Fatal(err)
	}
	type endpoint struct{ SessionID, TurnID, URL string }
	var endpoints []endpoint
	for _, item := range config.Sessions {
		session, err := agent.NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(config.WorkingDir), agent.SessionConfig{
			SessionID: item.SessionID, StateDir: config.StateDir, Manager: true, DisableMemory: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(session.Close)
		s, _ := newPendingAskMeasurement(t, item.SessionID, item.TurnID)
		source := s.appEnvelopeSource.(*pendingAskMeasurementSource)
		s.SetThreadEnvelopeSource(&pendingAskManagerMeasurementSource{pendingAskMeasurementSource: source, session: session})
		s.SetRetrySafeTurnFunctions(RetrySafeTurnFunctions{Start: session.AcceptClientMutationStart, Steer: session.AcceptClientMutationSteer})
		s.RefreshThreadEnvelope()
		upstream := httptest.NewServer(http.HandlerFunc(s.AppServer().ServeWebSocket))
		t.Cleanup(upstream.Close)
		endpoints = append(endpoints, endpoint{item.SessionID, item.TurnID, upstream.URL})
	}
	ready := os.NewFile(3, "pending-ask-measurement-ready")
	if err := json.NewEncoder(ready).Encode(endpoints); err != nil {
		t.Fatal(err)
	}
	if err := ready.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}
