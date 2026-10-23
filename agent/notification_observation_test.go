package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// These tests drive real request construction, response persistence, folds and
// restore. The adapter scripts only the external provider boundary. Opaque
// payload sentinels prove request membership independently of receipt metadata.
func TestNotificationObservation(t *testing.T) {
	t.Parallel()
	t.Run("launch claim is not observation", func(t *testing.T) {
		c, s, _ := newDelegateAttentionAcceptanceHarness(t, "child-dlg_target", afero.NewOsFs(), "obs-a")
		reservation, err := c.ReserveAttention(s, "obs-a")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.acceptDelegateAttention(reservation); err != nil {
			t.Fatal(err)
		}
		if _, err := c.CommitStart(reservation); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"obs-a"})
		observationReceipts(t, s, nil)
	})
	t.Run("request success", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{})
		observationReceipts(t, s, [][]string{{"obs-a"}})
	})
	t.Run("request failure survives reopen", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
			t.Fatal("provider failure succeeded")
		}
		observationReceipts(t, s, nil)
		reopened := observationReopen(t, s, a)
		assertNotificationPending(t, reopened, []string{"obs-a"})
		if len(a.Requests()) != 1 {
			t.Fatalf("requests before retry = %d", len(a.Requests()))
		}
		if _, err := reopened.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, reopened, []string{})
		if len(a.Requests()) != 2 {
			t.Fatalf("requests across crash before response = %d, want 2", len(a.Requests()))
		}
	})
	t.Run("late arrival", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, []string{"body-b-56e2"})
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"obs-b"})
		observationReceipts(t, s, [][]string{{"obs-a"}})
	})
	t.Run("failed response sync survives reopen", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		fs := newAttentionFailNextSyncFS()
		old := s.attachedTranscript()
		w, entries, err := transcript.OpenWriterForSessionWithFS(observationSyncFS{fs}, s.TranscriptPath(), s.ID())
		if err != nil {
			t.Fatal(err)
		}
		w.TrackFailures(entries, s.fork.divergence)
		s.mu.Lock()
		s.setTranscriptLocked(w)
		s.mu.Unlock()
		t.Cleanup(func() { _ = old.Close() })
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			fs.failNextSync()
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
			t.Fatal("failed assistant sync succeeded")
		}
		observationReceipts(t, s, nil)
		assertNotificationPending(t, s, []string{"obs-a"})
		reopened := observationReopen(t, s, a)
		assertNotificationPending(t, reopened, []string{"obs-a"})
		if _, err := reopened.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 2 {
			t.Fatalf("requests across failed response sync = %d, want 2", len(a.Requests()))
		}
		assertNotificationPending(t, reopened, []string{})
		t.Log("provider requests before restart=1, after restart=2, failed assistant sync left work pending")
	})
	t.Run("later round failure preserves earlier observation", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return jobListResponse("observed-round"), nil
		}, func(llm.Request) (llm.Response, error) {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
			t.Fatal("failed later round succeeded")
		}
		observationReceipts(t, s, [][]string{{"obs-a"}})
		assertNotificationPending(t, s, []string{})
	})
	t.Run("retry snapshot after compaction", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, []string{"body-b-56e2"})
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "content filter triggered", map[string]any{"error": map[string]any{"code": "invalid_prompt"}}, nil)
		}, func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9", "body-b-56e2"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 2 {
			t.Fatalf("request attempts = %d, want 2", len(a.Requests()))
		}
		assertNotificationPending(t, s, []string{})
		observationReceipts(t, s, [][]string{{"obs-a", "obs-b"}})
	})
	t.Run("earlier receipt does not hide later pending failure", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, []string{"body-b-56e2"})
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return jobListResponse("observed-round"), nil
		}, func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-b-56e2"}, nil)
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err == nil {
			t.Fatal("failed later round succeeded")
		}
		observationReceipts(t, s, [][]string{{"obs-a"}})
		assertNotificationPending(t, s, []string{"obs-b"})
		if !s.rootAttentionPaused {
			t.Fatal("earlier receipt hid the permanent failure for pending obs-b")
		}
	})
	t.Run("compaction preserves unobserved body", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		if err := s.Compact(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"obs-a"})
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		observationReceipts(t, s, [][]string{{"obs-a"}})
	})
	t.Run("unknown delta coverage chooses full history without spinning", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, true)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		// The pending notification predates the anchor. A delta can contain no
		// proof that the provider's stored prefix includes this accepted message.
		s.mu.Lock()
		s.history = append(s.history, phase9MatchingAnchor("resp_prior"), schema.NewTurn(schema.TurnUserInput, llm.User("input-3fe1")))
		s.mu.Unlock()
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			if req.HistoryMode != llm.HistoryModeFullHistory || req.PreviousResponseID != "" {
				t.Errorf("pending unknown coverage dispatched mode=%q previous=%q", req.HistoryMode, req.PreviousResponseID)
			}
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{})
		observationReceipts(t, s, [][]string{{"obs-a"}})
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 1 {
			t.Fatalf("repeated wake made %d requests, want 1", len(a.Requests()))
		}
	})
	t.Run("successful delta does not credit late arrival", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, true)
		s.mu.Lock()
		s.history = append(s.history, phase9MatchingAnchor("resp_prior"))
		s.mu.Unlock()
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			if req.HistoryMode != llm.HistoryModeResponsesDelta {
				t.Errorf("mode=%q, want responses_delta", req.HistoryMode)
			}
			observationRequest(t, req, nil, []string{"body-b-56e2"})
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return finalResponse("answer-822d"), nil
		}}
		if _, _, err := s.processOneInput(context.Background(), "input-3fe1", nil, EntryUserInput, nil); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"obs-b"})
		if len(a.Requests()) != 1 {
			t.Fatalf("delta requests = %d, want 1", len(a.Requests()))
		}
		receipts := observationReceiptRows(t, s)
		if len(receipts) == 0 || len(receipts[0]) != 0 {
			t.Fatalf("delta receipt = %v", receipts)
		}
	})
	t.Run("full history fallback excludes post snapshot arrival", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, true)
		s.mu.Lock()
		s.history = append(s.history, phase9MatchingAnchor("resp_prior"))
		s.mu.Unlock()
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			if req.HistoryMode != llm.HistoryModeResponsesDelta {
				t.Errorf("mode=%q, want responses_delta", req.HistoryMode)
			}
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 404, "", map[string]any{"error": map[string]any{"code": "previous_response_not_found"}}, nil)
		}, func(req llm.Request) (llm.Response, error) {
			if req.HistoryMode != llm.HistoryModeFullHistoryFallback {
				t.Errorf("mode=%q, want full_history_fallback", req.HistoryMode)
			}
			observationRequest(t, req, []string{"input-3fe1"}, []string{"body-b-56e2"})
			return finalResponse("answer-822d"), nil
		}}
		if _, _, err := s.processOneInput(context.Background(), "input-3fe1", nil, EntryUserInput, nil); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{"obs-b"})
		if len(a.Requests()) != 2 {
			t.Fatalf("fallback requests = %d, want 2", len(a.Requests()))
		}
		receipts := observationReceiptRows(t, s)
		if len(receipts) == 0 || len(receipts[0]) != 0 {
			t.Fatalf("fallback credited late arrival: %v", receipts)
		}
	})
	t.Run("synced response repairs missing resolution across restart", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		fs := installResolutionSyncFailuresWriter(t, s, 1)
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		_, runErr := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
		if runErr == nil || fs.injectedFailures() != 1 {
			t.Fatalf("resolution fault not exercised: err=%v failures=%d", runErr, fs.injectedFailures())
		}
		markers := 0
		for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
			if entry.Turn.AttentionResolution != nil {
				markers++
			}
		}
		if markers != 0 {
			t.Fatalf("derived markers after failed sync = %d", markers)
		}
		// This is the RED's critical distinction: successful model work is durable,
		// even though its derived marker failed. It must not become pending again.
		observationReceipts(t, s, [][]string{{"obs-a"}})
		assertNotificationPending(t, s, []string{})
		if len(a.Requests()) != 1 {
			t.Fatalf("requests before restart = %d", len(a.Requests()))
		}
		reopened := observationReopen(t, s, a)
		assertNotificationPending(t, reopened, []string{})
		if _, err := reopened.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 1 {
			t.Fatalf("requests across restart = %d, want 1", len(a.Requests()))
		}
		markers = 0
		for _, entry := range notificationRawEntries(t, reopened.TranscriptPath()) {
			if r := entry.Turn.AttentionResolution; r != nil && r.AttentionID == "obs-a" {
				markers++
			}
		}
		if markers != 1 {
			t.Fatalf("repaired derived markers = %d, want 1", markers)
		}
		t.Log("provider requests before restart=1, after restart=1, repaired markers=1")
	})
	for _, root := range []bool{true, false} {
		name := "root empty repair is a no-op"
		if !root {
			name = "delegate empty repair is a no-op"
		}
		t.Run(name, func(t *testing.T) {
			a := &skillContinuationAdapter{}
			var s *Session
			if root {
				s = observationSession(t, a, false)
			} else {
				_, s, _ = newDelegateAttentionAcceptanceHarness(t, "child-dlg_target", nil)
			}
			before, err := os.ReadFile(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if err := s.finishAttentionTurn(nil, nil); err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(s.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || len(a.Requests()) != 0 || s.rootAttentionWake || s.rootAttentionRetry.active {
				t.Fatalf("empty repair changed transcript or armed execution: requests=%d wake=%t retry=%t", len(a.Requests()), s.rootAttentionWake, s.rootAttentionRetry.active)
			}
		})
	}
}

func observationSession(t *testing.T, a *skillContinuationAdapter, continuation bool) *Session {
	t.Helper()
	client := llm.NewClient()
	client.Register(a)
	cfg := SessionConfig{StateDir: t.TempDir()}
	if continuation {
		cfg.OpenAIResponsesContinuation = "auto"
		cfg.testOnly.responsesContinuationSupportRegistry = map[llm.ResponsesEndpointFamily]llm.ResponsesContinuationSupport{llm.ResponsesEndpointFamilyOpenAIPublic: phase4DIEnabledSupport()}
	}
	s, err := NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

// The existing next-sync fault wraps Create. A real restart/replacement opens
// an existing transcript, so that boundary must wrap OpenFile too.
type observationSyncFS struct{ *attentionFailNextSyncFS }

func (fs observationSyncFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &attentionFailNextSyncFile{File: f, fs: fs.attentionFailNextSyncFS}, nil
}
func observationEnqueue(t *testing.T, s *Session, id, body string) {
	t.Helper()
	if err := s.enqueueNotification(notification{ID: id, Message: llm.User(body)}); err != nil {
		t.Fatal(err)
	}
}
func observationRequest(t *testing.T, req llm.Request, present, absent []string) {
	t.Helper()
	for _, body := range present {
		if !requestContainsText(req, body) {
			t.Errorf("request omitted payload %q", body)
		}
	}
	for _, body := range absent {
		if requestContainsText(req, body) {
			t.Errorf("request prematurely carried payload %q", body)
		}
	}
}
func observationReceiptRows(t *testing.T, s *Session) [][]string {
	t.Helper()
	var rows [][]string
	data, err := os.ReadFile(s.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(data, []byte("\n"))[1:] {
		if len(line) == 0 {
			continue
		}
		var raw struct {
			Turn struct {
				Kind string   `json:"kind"`
				IDs  []string `json:"observed_notification_ids"`
			} `json:"turn"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Turn.Kind == "ASSISTANT" {
			rows = append(rows, raw.Turn.IDs)
		}
	}
	return rows
}
func observationReceipts(t *testing.T, s *Session, want [][]string) {
	t.Helper()
	if got := observationReceiptRows(t, s); !reflect.DeepEqual(got, want) {
		t.Fatalf("successful response receipts = %v, want %v", got, want)
	}
}
func observationReopen(t *testing.T, s *Session, a *skillContinuationAdapter) *Session {
	t.Helper()
	stateDir, id := s.stateDir, s.ID()
	s.Close()
	meta, err := schema.LoadSessionMeta(stateDir, id)
	if err != nil {
		t.Fatal(err)
	}
	client := llm.NewClient()
	client.Register(a)
	reopened, err := RestoreSessionFromMetaWithConfig(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.4")), execenv.NewLocalExecutionEnvironment(t.TempDir()), meta, RestoreSessionConfig{StateDir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	return reopened
}
