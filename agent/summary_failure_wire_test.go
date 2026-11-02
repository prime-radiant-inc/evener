package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appoverlay"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// Serial: captures the process logger while exercising real session publication.
func TestSummaryFailureWireFixtures(t *testing.T) {
	var log bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	var recovered atomic.Bool
	client := llm.NewClient()
	client.Register(&agenttest.ModelTrackingAdapter{Provider: "openai", Respond: func(req llm.Request) (llm.Response, error) {
		if req.Model != "summary-model-8af1" {
			return agenttest.FinalResponse("continued-41bc"), nil
		}
		if !recovered.Load() {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 429, "private-error-812f", map[string]any{"prompt": "private-prompt-235d"}, nil)
		}
		return llm.Response{Message: llm.Assistant("## Progress\nrecovered-summary-731c")}, nil
	}})
	stateDir := t.TempDir()
	sess := newSession(t, withClient(client), withProfile(WithCheapModel(NewOpenAIProfile("gpt-5.2"), "summary-model-8af1")), withDir(stateDir), withoutGitSnapshot(), withConfig(SessionConfig{
		StateDir: stateDir, AgentsDocPath: filepath.Join(stateDir, "absent-AGENTS.md"),
	}))
	if err := sess.Rename("summary-wire-fixture"); err != nil {
		t.Fatal(err)
	}
	collect := drainEvents(sess)
	sess.contextMgr.PreserveRecentTurns = 2
	for i := range 12 {
		turn := schema.NewTurn(schema.TurnUserInput, llm.User(fmt.Sprintf("private-human-%d-28ef", i)))
		sess.recordTurn(turn, turn)
	}
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered.Store(true)
	if err := sess.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(currentHistory(t, sess), func(turn schema.Turn) bool {
		return turn.Kind == schema.TurnSummary && strings.Contains(turn.Message.Text(), "recovered-summary-731c")
	}) {
		t.Fatal("successful compaction did not install its summary")
	}
	if _, err := sess.ProcessInput(context.Background(), "continue-94ae", nil); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	var fixture struct {
		Warning  events.WarningData `json:"warning"`
		Item     appwire.ThreadItem `json:"item"`
		Recovery appwire.ThreadItem `json:"recovery"`
	}
	overlay := appoverlay.New(appoverlay.NewBudget(appoverlay.DefaultBudgetBytes))
	defer overlay.Close()
	overlay.Event(events.New(events.ExecutionStartedData{TurnID: "turn_1"}))
	warnings, completed := 0, false
	for _, ev := range collect() {
		switch data := ev.Data.(type) {
		case events.WarningData:
			if data.Code != "summary_failure" {
				continue
			}
			warnings++
			fixture.Warning = data
			changes := overlay.Event(ev)
			if len(changes) != 1 {
				t.Fatalf("warning projection changes = %d", len(changes))
			}
			fixture.Item = changes[0].Params.(appwire.OverlayUpsertedParams).Item.Item
		case events.ContextCompactionData:
			if data.Layer == "summarize" {
				var ok bool
				fixture.Recovery, ok = apptranscript.SystemMessage(apptranscript.ContextCompactionAnnouncement(data), "recovery_1", "turn_1")
				if !ok {
					t.Fatal("recovery notice missing")
				}
			}
		case events.TurnEndedData:
			completed = true
		}
	}
	if warnings != 1 || !completed || fixture.Recovery.ID == "" {
		t.Fatalf("warnings %d, continued %v, recovery %q", warnings, completed, fixture.Recovery.ID)
	}
	encoded, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, canary := range []string{"private-error-812f", "private-prompt-235d", "private-human-", "recovered-summary-731c"} {
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatal("private content entered the shared fixture")
		}
	}
	if records := summaryFailureLogRecords(t, log.Bytes()); len(records) != 1 {
		t.Fatalf("diagnostic lifecycle records = %d", len(records))
	}
	checkWireFixture(t, "testdata/summaryfailurewire/events.json", fixture, "summary failure shared-client, browser, native and terminal tests")
}
