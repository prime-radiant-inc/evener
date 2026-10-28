package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"reflect"
	"sync"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/llm"
)

// These tests drive real request construction, response persistence, folds and
// restore. The adapter scripts only the external provider boundary. Opaque
// payload sentinels prove request membership independently of receipt metadata.
func TestNotificationObservation(t *testing.T) {
	// Not parallel: the two failed-provider children capture the default logger.
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
		pause := captureNotificationPause(t)
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		pause.watch(t, s)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		providerFailure := llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{}, providerFailure
		}}
		_, runErr := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
		if runErr == nil {
			t.Fatal("provider failure succeeded")
		}
		assertNotificationAuthenticationFailure(t, runErr, providerFailure)
		observationReceipts(t, s, nil)
		reopened := observationReopen(t, s, a)
		pause.watch(t, reopened)
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
	t.Run("retained_response_requires_durability", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		fs := observationRetainedWriter(t, s, `"kind":"ASSISTANT"`)
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); !errors.Is(err, transcript.ErrRetainedUnsynced) {
			t.Fatalf("response append = %v, want retained-unsynced error", err)
		}
		observationReceipts(t, s, [][]string{{"obs-a"}}) // readable, but not synced
		if markers := observationResolutionCount(t, s); markers != 0 {
			t.Errorf("unsynced response authorized %d resolution records, want 0", markers)
		}
		if ids, err := s.pendingNotificationIDs(); err == nil && !reflect.DeepEqual(ids, []string{"obs-a"}) {
			t.Errorf("unsynced response granted observation: pending=%v, want obs-a or durability error", ids)
		}
		if covered := s.unionCoveredAttention(nil); len(covered) != 0 {
			t.Errorf("unsynced response authorized marker repair: %v", covered)
		}
		if err := s.ackNotifications(nil); err == nil {
			t.Error("repair succeeded while retained response remained unsynced")
		}
		fs.release(t)
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		assertNotificationPending(t, s, []string{})
		observationReceipts(t, s, [][]string{{"obs-a"}})
		observationOneResolution(t, s)
		fs.assertRecovered(t)
		reopened := observationReopen(t, s, a)
		if _, err := reopened.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 1 {
			t.Fatalf("requests after recovery and restart = %d, want 1", len(a.Requests()))
		}
		observationReceipts(t, reopened, [][]string{{"obs-a"}})
		observationOneResolution(t, reopened)
		t.Log("retained response recovery/restart: requests=1 responses=1 markers=1")
	})
	t.Run("retained_resolution_requires_durability", func(t *testing.T) {
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		fs := observationRetainedWriter(t, s, `"kind":"ATTENTION_RESOLUTION"`)
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, nil)
			return llm.Response{Message: llm.Assistant("answer-822d")}, nil
		}}
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); !errors.Is(err, transcript.ErrRetainedUnsynced) {
			t.Fatalf("resolution append = %v, want retained-unsynced error", err)
		}
		observationReceipts(t, s, [][]string{{"obs-a"}}) // this response synced
		observationOneResolution(t, s)                   // this marker did not
		if !observationRepairOwed(s) {
			t.Fatal("failed marker append lost its retry debt")
		}
		if err := s.ackNotifications(nil); err == nil {
			t.Error("repair succeeded while retained resolution remained unsynced")
		}
		if !observationRepairOwed(s) {
			t.Error("unsynced retained resolution cleared retry debt")
		}
		fs.release(t)
		if _, err := s.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		unsettled := s.notificationSchedule.retry.active || len(notificationTestPending(t, s)) != 0
		s.mu.Unlock()
		if unsettled {
			t.Fatal("synced resolution did not clear retry debt")
		}
		assertNotificationPending(t, s, []string{})
		observationReceipts(t, s, [][]string{{"obs-a"}})
		observationOneResolution(t, s)
		fs.assertRecovered(t)
		reopened := observationReopen(t, s, a)
		if _, err := reopened.ProcessInputKind(context.Background(), "", nil, EntryNotification); err != nil {
			t.Fatal(err)
		}
		if len(a.Requests()) != 1 {
			t.Fatalf("requests after recovery and restart = %d, want 1", len(a.Requests()))
		}
		observationReceipts(t, reopened, [][]string{{"obs-a"}})
		observationOneResolution(t, reopened)
		t.Log("retained resolution recovery/restart: requests=1 responses=1 markers=1")
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
		pause := captureNotificationPause(t)
		a := &skillContinuationAdapter{}
		s := observationSession(t, a, false)
		pause.watch(t, s)
		observationEnqueue(t, s, "obs-a", "body-a-71c9")
		providerFailure := llm.ErrorFromHTTPStatus("openai", 401, "", nil, nil)
		a.steps = []func(llm.Request) (llm.Response, error){func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-a-71c9"}, []string{"body-b-56e2"})
			observationEnqueue(t, s, "obs-b", "body-b-56e2")
			return jobListResponse("observed-round"), nil
		}, func(req llm.Request) (llm.Response, error) {
			observationRequest(t, req, []string{"body-b-56e2"}, nil)
			return llm.Response{}, providerFailure
		}}
		_, runErr := s.ProcessInputKind(context.Background(), "", nil, EntryNotification)
		if runErr == nil {
			t.Fatal("failed later round succeeded")
		}
		assertNotificationAuthenticationFailure(t, runErr, providerFailure)
		observationReceipts(t, s, [][]string{{"obs-a"}})
		assertNotificationPending(t, s, []string{"obs-b"})
		if !s.notificationSchedule.paused {
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
			if !bytes.Equal(before, after) || len(a.Requests()) != 0 || s.notificationSchedule.hinted || s.notificationSchedule.retry.active {
				t.Fatalf("empty repair changed transcript or armed execution: requests=%d wake=%t retry=%t", len(a.Requests()), s.notificationSchedule.hinted, s.notificationSchedule.retry.active)
			}
		})
	}
}

// These helpers are used only by serial warning-producing fixtures. Register
// restoration first, so session shutdown and lossless event drains finish before
// inspecting records or restoring the process logger. Never compare warning copy.
type notificationPauseCapture struct {
	logs     *reclaimLogCapture
	mu       sync.Mutex
	warnings []events.SessionEvent
	receiver string
}

func captureNotificationPause(t *testing.T) *notificationPauseCapture {
	t.Helper()
	c := &notificationPauseCapture{logs: &reclaimLogCapture{Handler: slog.NewJSONHandler(io.Discard, nil)}}
	previous := slog.Default()
	slog.SetDefault(slog.New(c.logs))
	t.Cleanup(func() {
		defer slog.SetDefault(previous)
		c.mu.Lock()
		warnings := append([]events.SessionEvent(nil), c.warnings...)
		c.mu.Unlock()
		if len(warnings) != 1 {
			t.Errorf("receiver %s warning events=%d, want exactly one", c.receiver, len(warnings))
		}
		for _, event := range warnings {
			data, ok := event.Data.(events.WarningData)
			if event.SessionID != c.receiver || !ok || data.Code != events.WarningCodeAttentionPaused || data.Source != string(diagnostic.SourceEvener) {
				t.Errorf("unexpected warning attribution: receiver=%s kind=%s data_type=%T code=%s source=%s", event.SessionID, event.Kind, event.Data, data.Code, data.Source)
			}
		}
		c.logs.mu.Lock()
		records := append([]slog.Record(nil), c.logs.records...)
		c.logs.mu.Unlock()
		warningCount := 0
		for _, record := range records {
			if record.Level < slog.LevelWarn {
				continue
			}
			warningCount++
			attrs := make(map[string]slog.Value)
			record.Attrs(func(attr slog.Attr) bool { attrs[attr.Key] = attr.Value.Resolve(); return true })
			if record.Level != slog.LevelWarn || len(attrs) != 2 || attrs["session"].Kind() != slog.KindString || attrs["session"].String() != c.receiver || attrs["failure"].Kind() != slog.KindString {
				t.Errorf("unexpected structured warning: level=%s receiver=%s attribute_count=%d failure_kind=%s", record.Level, attrs["session"], len(attrs), attrs["failure"].Kind())
			}
		}
		if warningCount != 1 {
			t.Errorf("receiver %s structured warnings=%d, want exactly one", c.receiver, warningCount)
		}
		t.Logf("joined warning capture: receiver=%s pause_events=%d structured_warnings=%d", c.receiver, len(warnings), warningCount)
	})
	return c
}

func (c *notificationPauseCapture) watch(t *testing.T, s *Session) {
	t.Helper()
	if c.receiver != "" && c.receiver != s.ID() {
		t.Fatal("warning fixture changed receiver identity across restore")
	}
	c.receiver = s.ID()
	drained := make(chan struct{})
	s.ConsumeEventsLossless(func(event events.SessionEvent) {
		if event.Kind == events.EventWarning {
			c.mu.Lock()
			c.warnings = append(c.warnings, event)
			c.mu.Unlock()
		}
	}, func() { close(drained) })
	t.Cleanup(func() {
		s.Close()
		<-drained
	})
}

func assertNotificationAuthenticationFailure(t *testing.T, actual, injected error) {
	t.Helper()
	if !errors.Is(actual, injected) {
		t.Fatalf("provider failure identity changed: %v", actual)
	}
	var providerError llm.Error
	if !errors.As(actual, &providerError) {
		t.Fatalf("provider failure type=%T, want llm.Error", actual)
	}
	if providerError.Provider() != "openai" || providerError.StatusCode() != 401 || providerError.Retryable() || llm.Kind(actual) != llm.KindAuthentication || llm.Classify(actual) != llm.ErrorClassPermanent {
		t.Fatalf("provider failure fields: provider=%s status=%d retryable=%t kind=%s class=%v", providerError.Provider(), providerError.StatusCode(), providerError.Retryable(), llm.Kind(actual), llm.Classify(actual))
	}
	t.Logf("injected failure validated: provider=%s status=%d retryable=%t kind=%s class=%v", providerError.Provider(), providerError.StatusCode(), providerError.Retryable(), llm.Kind(actual), llm.Classify(actual))
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

// Fault only filesystem operations, starting at the target record's write.
// Sync (including the recovery barrier) and rollback Truncate keep failing
// until release, so readable records cannot accidentally become durable.
type observationRetainedFS struct {
	afero.Fs
	mu                           sync.Mutex
	target                       []byte
	failing                      bool
	syncFailures, rollbacks      int
	successfulSyncsAfterFailures int
}

func observationRetainedWriter(t *testing.T, s *Session, target string) *observationRetainedFS {
	t.Helper()
	fs := &observationRetainedFS{Fs: afero.NewOsFs(), target: []byte(target)}
	s.attentionMu.Lock()
	defer s.attentionMu.Unlock()
	old := s.attachedTranscript()
	w, entries, err := transcript.OpenWriterForSessionWithFS(fs, s.TranscriptPath(), s.ID())
	if err != nil {
		t.Fatal(err)
	}
	w.TrackFailures(entries, s.fork.divergence)
	s.mu.Lock()
	s.setTranscriptLocked(w)
	s.mu.Unlock()
	// A lifecycle recovery must use the same faulty filesystem as the append.
	s.cfg.testOnly.delegateAttentionOpenWriter = func(path, id string) (*transcript.Writer, []transcript.Entry, error) {
		return transcript.OpenWriterForSessionWithFS(fs, path, id)
	}
	t.Cleanup(func() { _ = old.Close() })
	return fs
}

func (fs *observationRetainedFS) OpenFile(path string, flag int, mode os.FileMode) (afero.File, error) {
	f, err := fs.Fs.OpenFile(path, flag, mode)
	if err != nil {
		return nil, err
	}
	return &observationRetainedFile{File: f, fs: fs}, nil
}

func (fs *observationRetainedFS) release(t *testing.T) {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.syncFailures < 2 || fs.rollbacks == 0 {
		t.Fatalf("did not exercise append sync, rollback and recovery barrier: sync faults=%d rollback faults=%d", fs.syncFailures, fs.rollbacks)
	}
	fs.failing = false
	fs.target = nil
}

func (fs *observationRetainedFS) assertRecovered(t *testing.T) {
	t.Helper()
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if fs.successfulSyncsAfterFailures == 0 {
		t.Error("recovery reported success without a successful filesystem sync")
	}
}

type observationRetainedFile struct {
	afero.File
	fs *observationRetainedFS
}

func (f *observationRetainedFile) Write(p []byte) (int, error) {
	f.fs.mu.Lock()
	if len(f.fs.target) != 0 && bytes.Contains(p, f.fs.target) {
		f.fs.failing = true
	}
	f.fs.mu.Unlock()
	return f.File.Write(p)
}

func (f *observationRetainedFile) Sync() error {
	f.fs.mu.Lock()
	fail := f.fs.failing
	if fail {
		f.fs.syncFailures++
	}
	f.fs.mu.Unlock()
	if fail {
		return errors.New("injected retained record sync failure")
	}
	if err := f.File.Sync(); err != nil {
		return err
	}
	f.fs.mu.Lock()
	if f.fs.syncFailures != 0 {
		f.fs.successfulSyncsAfterFailures++
	}
	f.fs.mu.Unlock()
	return nil
}

func (f *observationRetainedFile) Truncate(size int64) error {
	f.fs.mu.Lock()
	fail := f.fs.failing
	if fail {
		f.fs.rollbacks++
	}
	f.fs.mu.Unlock()
	if fail {
		return errors.New("injected retained record rollback failure")
	}
	return f.File.Truncate(size)
}

func observationOneResolution(t *testing.T, s *Session) {
	t.Helper()
	if markers := observationResolutionCount(t, s); markers != 1 {
		t.Fatalf("resolution records = %d, want 1", markers)
	}
}

func observationRepairOwed(s *Session) bool {
	fold, err := readDelegateAttentionFold(transcriptPath(s.stateDir, s.id), s.id)
	s.mu.Lock()
	defer s.mu.Unlock()
	_, accepted := fold.content["obs-a"]
	return err != nil || accepted && (s.notificationSchedule.retry.active || s.notificationSchedule.hinted)
}

func observationResolutionCount(t *testing.T, s *Session) int {
	t.Helper()
	markers := 0
	for _, entry := range notificationRawEntries(t, s.TranscriptPath()) {
		if r := entry.Turn.AttentionResolution; r != nil && r.AttentionID == "obs-a" {
			markers++
		}
	}
	return markers
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
