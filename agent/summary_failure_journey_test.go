package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/diagnostic"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// Replacing the process logger keeps these tests serial. Removing the log at
// publication must fail even though the warning still reaches the event stream.
func TestSummaryFailureJourney(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	stateDir := t.TempDir()
	hookPath := filepath.Join(stateDir, "hook-input.json")
	command, err := json.Marshal("cat > " + fmt.Sprintf("%q", hookPath))
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := writePluginHooks(t, "summary-notification", fmt.Sprintf(`{"hooks":{"Notification":[{"matcher":"*","hooks":[{"type":"command","command":%s}]}]}}`, command))
	var recovered atomic.Bool
	adapter := &agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(req llm.Request) (llm.Response, error) {
		if req.Model != "cheap-fixture" {
			return agenttest.FinalResponse("next-turn-32b1"), nil
		}
		if !recovered.Load() {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 429, "private-body-a8d9", map[string]any{"prompt": "private-prompt-f6c2"}, nil)
		}
		return llm.Response{Message: llm.Assistant("## Progress\nsummary-restored-94a1")}, nil
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess := newSession(t, withClient(client), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "cheap-fixture")), withDir(stateDir), withoutGitSnapshot(), withConfig(SessionConfig{
		StateDir: stateDir, AgentsDocPath: filepath.Join(stateDir, "absent-AGENTS.md"), PluginDirs: []string{pluginDir},
	}))
	if err := sess.Rename("summary-journey"); err != nil {
		t.Fatal(err)
	}
	collect := drainEvents(sess)
	sess.contextMgr.PreserveRecentTurns = 2
	var humanMarkers []string
	for i := range 12 {
		marker := fmt.Sprintf("human-content-%02d-71e9", i)
		humanMarkers = append(humanMarkers, marker)
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(marker))
		sess.recordTurn(turn, turn)
	}
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	failedHistory, err := json.Marshal(currentHistory(t, sess))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range humanMarkers {
		if !bytes.Contains(failedHistory, []byte(marker)) {
			t.Errorf("failed compaction lost human input %s", marker)
		}
	}
	beforeRecovery := summaryFailureLogRecords(t, output.Bytes())
	if len(beforeRecovery) != 1 {
		t.Errorf("failure log count = %d, want 1", len(beforeRecovery))
	} else {
		assertSummaryFailureLogRecord(t, beforeRecovery[0], sess.ID())
	}
	hookInput, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	var hook struct{ Message string }
	if err := json.Unmarshal(hookInput, &hook); err != nil {
		t.Fatal(err)
	}

	recovered.Store(true)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(currentHistory(t, sess), func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnSummary && strings.Contains(turn.Message.Text(), "summary-restored-94a1")
	}) {
		t.Error("recovery did not install the summary")
	}
	if _, err := sess.ProcessInput(context.Background(), "continue-11de", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()
	captured := collect()
	var warnings []events.WarningData
	completed := false
	for _, ev := range captured {
		if warning, ok := ev.Data.(events.WarningData); ok && warning.Code == "summary_failure" {
			warnings = append(warnings, warning)
			if ev.SessionID != sess.ID() || warning.SummaryFailure == nil || hook.Message != warning.Message {
				t.Error("warning attribution, metadata or hook delivery lost")
			}
		}
		if ev.Kind == events.EventTurnEnded {
			completed = true
		}
	}
	if len(warnings) != 1 || !completed {
		t.Errorf("warnings = %d, next turn completed = %v", len(warnings), completed)
	}
	if got := len(summaryFailureLogRecords(t, output.Bytes())); got != len(beforeRecovery) {
		t.Errorf("recovery logged another failure: before %d, after %d", len(beforeRecovery), got)
	}
	transcript, err := os.ReadFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range append(humanMarkers, "next-turn-32b1") {
		if !bytes.Contains(transcript, []byte(marker)) {
			t.Errorf("durable transcript lost %s", marker)
		}
	}
	encodedWarnings, err := json.Marshal(warnings)
	if err != nil {
		t.Fatal(err)
	}
	for _, surface := range [][]byte{output.Bytes(), hookInput, encodedWarnings, transcript} {
		for _, canary := range []string{"private-body-a8d9", "private-prompt-f6c2"} {
			if bytes.Contains(surface, []byte(canary)) {
				t.Error("provider content escaped to a retained or public surface")
			}
		}
	}
}

func TestSummaryFailureChildWithUnreadEvents(t *testing.T) {
	for _, brokenSink := range []bool{false, true} {
		t.Run(fmt.Sprintf("broken_sink_%t", brokenSink), func(t *testing.T) {
			var output bytes.Buffer
			var sink io.Writer = &output
			if brokenSink {
				file, err := os.CreateTemp(t.TempDir(), "closed-log")
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString("probe"); !errors.Is(err, os.ErrClosed) {
					t.Fatalf("sink must reject writes: %v", err)
				}
				sink = file
			}
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(sink, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			var forwarded []events.SessionEvent
			var recovered atomic.Bool
			client := llm.NewClient()
			client.Register(&agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(llm.Request) (llm.Response, error) {
				if recovered.Load() {
					return llm.Response{Message: llm.Assistant("## Progress\nchild-summary-f3a7")}, nil
				}
				return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 429, "private-child-body-f6e1", nil, nil)
			}})
			stateDir := t.TempDir()
			sess := newSession(t, withClient(client), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "cheap-fixture")), withDir(stateDir), withoutGitSnapshot(), withConfig(SessionConfig{
				StateDir: stateDir, AgentsDocPath: filepath.Join(stateDir, "absent-AGENTS.md"),
				spawn: spawnConfig{parentSessionID: "root-fixture", descendantEvent: func(ev events.SessionEvent) { forwarded = append(forwarded, ev) }},
			}))
			if err := sess.Rename("child-evidence"); err != nil {
				t.Fatal(err)
			}
			sess.contextMgr.PreserveRecentTurns = 2
			for i := range 12 {
				turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("child-human-%d-a8f3", i)))
				sess.recordTurn(turn, turn)
			}
			for len(sess.events) < cap(sess.events) {
				sess.events <- events.New(events.WarningData{Code: "filler"})
			}
			if err := sess.Compact(context.Background()); err != nil {
				t.Fatal(err)
			}
			failures := 0
			for _, ev := range forwarded {
				if warning, ok := ev.Data.(events.WarningData); ok && warning.Code == "summary_failure" {
					failures++
					if ev.SessionID != sess.ID() || ev.SessionID == "root-fixture" {
						t.Error("child callback lost its identity")
					}
				}
			}
			if failures != 1 {
				t.Errorf("forwarded failure count = %d", failures)
			}
			history, err := json.Marshal(currentHistory(t, sess))
			if err != nil {
				t.Fatal(err)
			}
			for i := range 12 {
				if !bytes.Contains(history, fmt.Appendf(nil, "child-human-%d-a8f3", i)) {
					t.Error("logging changed preservation")
				}
			}
			recovered.Store(true)
			if err := sess.Compact(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(currentHistory(t, sess), func(turn schema.Turn) bool {
				return turn.Kind == schema.TurnSummary && strings.Contains(turn.Message.Text(), "child-summary-f3a7")
			}) {
				t.Error("logging prevented later summary installation")
			}
			sess.Close()
			if !brokenSink {
				records := summaryFailureLogRecords(t, output.Bytes())
				if len(records) != 1 {
					t.Fatalf("child log records = %d", len(records))
				}
				assertSummaryFailureLogRecord(t, records[0], sess.ID())
			}
		})
	}
}

func TestSummaryFailurePublicationFilters(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	sess := newSession(t, withoutGitSnapshot())
	failure := &diagnostic.SummaryFailure{Terminal: diagnostic.SummaryAttempt{Kind: "unknown"}}
	for _, warning := range []events.WarningData{
		{Code: "another_warning", SummaryFailure: failure},
		{Code: "summary_failure"},
	} {
		sess.sendEvent(warning, nil)
	}
	if got := len(summaryFailureLogRecords(t, output.Bytes())); got != 0 {
		t.Errorf("unrelated or incomplete warnings logged: %d", got)
	}
	warning := events.WarningData{Code: "summary_failure", SummaryFailure: failure, Message: "private-copy-e4c2", Hint: "private-hint-2e7c"}
	sess.sendEvent(warning, nil)
	sess.Close()
	sess.sendEvent(warning, nil)
	if got := len(summaryFailureLogRecords(t, output.Bytes())); got != 1 {
		t.Errorf("open/closed publication records = %d, want 1", got)
	}
	for _, canary := range []string{"private-copy-e4c2", "private-hint-2e7c"} {
		if bytes.Contains(output.Bytes(), []byte(canary)) {
			t.Error("warning copy entered the safe metadata log")
		}
	}
}

func summaryFailureLogRecords(t *testing.T, output []byte) []map[string]json.RawMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(output))
	var records []map[string]json.RawMessage
	for {
		var record map[string]json.RawMessage
		err := decoder.Decode(&record)
		if err == io.EOF {
			return records
		}
		if err != nil {
			t.Fatal(err)
		}
		var code string
		if err := json.Unmarshal(record["code"], &code); err == nil && code == "summary_failure" {
			records = append(records, record)
		}
	}
}

func assertSummaryFailureLogRecord(t *testing.T, record map[string]json.RawMessage, sessionID string) {
	t.Helper()
	var gotSession, payload string
	if err := json.Unmarshal(record["session_id"], &gotSession); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(record["summary_failure"], &payload); err != nil {
		t.Fatal(err)
	}
	var failure struct {
		Terminal struct {
			Route, Provider, Model, Kind string
			Status                       int
			Retryable                    *bool
		}
	}
	if err := json.Unmarshal([]byte(payload), &failure); err != nil {
		t.Fatal(err)
	}
	got := failure.Terminal
	if gotSession != sessionID || got.Route != "cheap" || got.Provider != "openai" || got.Model != "cheap-fixture" || got.Kind != "rate_limit" || got.Status != 429 || got.Retryable == nil || !*got.Retryable {
		t.Errorf("wrong diagnostic session %q, attempt %+v", gotSession, got)
	}
}
