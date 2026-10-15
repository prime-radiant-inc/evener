package contextmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/apilog"
)

func TestSummaryFailureWarningsNeverExposeProviderContent(t *testing.T) {
	t.Parallel()
	canaries := []string{"private-message-9b3a", "private-code-2f6c", "private-body-8d1e", "private-prompt-7a4b", "private-summary-6c5d"}
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		return llm.Response{}, llm.ErrorFromHTTPStatus("private-provider-0e1f", 429, canaries[0], map[string]any{"error": map[string]any{"code": canaries[1], "message": canaries[2]}, "prompt": canaries[3], "summary": canaries[4]}, nil)
	}})
	profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), "cheap-fixture")
	cm := NewManager(profile, client, cheapmodel.New(client))
	_, err := cm.completeSummarization(context.Background(), profile, canaries[3], nil)
	if err == nil {
		t.Fatal("summary unexpectedly succeeded")
	}
	warning := summarizerFailureWarning(err)
	encoded, err := json.Marshal(warning)
	if err != nil {
		t.Fatal(err)
	}
	// Check the serialized contract independently of the Go payload type.
	var wire struct {
		Code    string `json:"code"`
		Failure *struct {
			Terminal struct {
				Model, Provider, Route, Kind string
				Status                       int
				Retryable                    *bool
			} `json:"terminal"`
		} `json:"summary_failure"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Code != "summary_failure" || wire.Failure == nil {
		t.Fatalf("missing diagnostic: %s", encoded)
	}
	got := wire.Failure.Terminal
	if got.Model != "cheap-fixture" || got.Provider != "openai" || got.Route != "cheap" || got.Kind != "rate_limit" || got.Status != 429 || got.Retryable == nil || !*got.Retryable {
		t.Fatalf("wrong failure details: %+v", got)
	}
	for _, canary := range append(canaries, "private-provider-0e1f") {
		if bytes.Contains(encoded, []byte(canary)) {
			t.Fatal("private content escaped")
		}
	}
}

func TestSummaryFailureRoutingAndRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name             string
		cheap            string
		refused, succeed bool
		wantCalls        []string
		priorKind        string
	}{
		{"validation then failure", "cheap-fixture", false, false, []string{"cheap-fixture", "active-fixture"}, "rejected_output"},
		{"validation then success", "cheap-fixture", false, true, []string{"cheap-fixture", "active-fixture"}, ""},
		{"refusal then failure", "cheap-fixture", true, false, []string{"cheap-fixture", "active-fixture"}, "invalid_request"},
		{"no cheap route", "", false, false, []string{"active-fixture"}, ""},
		{"unservable cheap route", "missing-instance/cheap-fixture", false, false, []string{"active-fixture"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			activeErr := llm.ErrorFromHTTPStatus("openai", 503, "private-active-e4f1", nil, nil)
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
				calls = append(calls, req.Model)
				if req.Model == "cheap-fixture" {
					if tc.refused {
						return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "model unavailable", nil, nil)
					}
					return llm.Response{Message: llm.Assistant("private-summary-b6d4")}, nil
				}
				if tc.succeed {
					return llm.Response{Message: llm.Assistant("## Progress\nrecovered-summary-8c2f")}, nil
				}
				return llm.Response{}, activeErr
			}})
			profile := WithCheapModel(testProfile("openai", "active-fixture", 1000), tc.cheap)
			cm := NewManager(profile, client, cheapmodel.New(client))
			resp, err := cm.completeSummarization(context.Background(), profile, "private-prompt-1f2e", func(resp llm.Response) error { return checkSummaryReply(resp.Text(), "") })
			if !reflect.DeepEqual(calls, tc.wantCalls) {
				t.Fatalf("routes = %v, want %v", calls, tc.wantCalls)
			}
			if tc.succeed {
				if err != nil || !strings.Contains(resp.Text(), "recovered-summary-8c2f") {
					t.Fatalf("fallback failed: %v", err)
				}
				return
			}
			if !errors.Is(err, activeErr) {
				t.Fatalf("lost terminal cause: %v", err)
			}
			warning := summarizerFailureWarning(err)
			if warning.SummaryFailure == nil {
				t.Fatal("missing route evidence")
			}
			failure := warning.SummaryFailure
			if failure.Terminal.Model != "active-fixture" || failure.Terminal.Route != "session" || failure.Terminal.Kind != "server" || failure.Terminal.Status != 503 {
				t.Fatalf("wrong terminal: %+v", failure)
			}
			if tc.priorKind == "" {
				if len(failure.Previous) != 0 {
					t.Fatalf("invented skipped attempt: %+v", failure)
				}
			} else if len(failure.Previous) != 1 || failure.Previous[0].Model != "cheap-fixture" || failure.Previous[0].Route != "cheap" || failure.Previous[0].Kind != tc.priorKind {
				t.Fatalf("lost prior attempt: %+v", failure)
			}
			encoded, marshalErr := json.Marshal(warning)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			for _, canary := range []string{"private-active-e4f1", "private-summary-b6d4", "private-prompt-1f2e"} {
				if bytes.Contains(encoded, []byte(canary)) {
					t.Fatal("private fallback content escaped")
				}
			}
		})
	}
}

func TestSummaryFailureValidationReasons(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		run          func(*Manager) error
	}{
		{"empty", "empty_reply", func(_ *Manager) error { return checkSummaryReply("", "") }},
		{"malformed", "missing_summary_section", func(_ *Manager) error { return checkSummaryReply("private-summary-5e6d", "") }},
		{"length", "output_limit", func(_ *Manager) error {
			return checkNotCutOff(llm.Response{Finish: llm.FinishReason{Reason: llm.FinishReasonLength, Raw: "private-finish-2e4c"}})
		}},
		{"missing model", "missing_model", func(cm *Manager) error {
			_, err := cm.completeSummarization(context.Background(), nil, "private-prompt-9f1a", nil)
			return err
		}},
		{"empty batch", "empty_batch", func(cm *Manager) error {
			_, err := cm.condensePartnerMessages(context.Background(), cm.currentProfile(), []string{strings.Repeat("x", 100)}, 30)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewClient()
			client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) { return llm.Response{}, nil }})
			cm := NewManager(testOpenAIProfileWithContextWindow(minSummarizingWindow), client, cheapmodel.New(client))
			warning := summarizerFailureWarning(tc.run(cm))
			if warning.SummaryFailure == nil {
				t.Fatal("missing local diagnostic")
			}
			got := warning.SummaryFailure.Terminal
			kind, stage := "rejected_output", "validation"
			if tc.name == "missing model" {
				kind, stage = "configuration", "pre_dispatch"
			}
			if got.Kind != kind || got.Stage != stage || got.Reason != tc.reason || got.Status != 0 || got.Retryable != nil {
				t.Fatalf("wrong validation detail: %+v", got)
			}
			if tc.name != "empty batch" && (got.Route != "none" || got.Model != "" || got.Provider != "") {
				t.Fatalf("invented route: %+v", got)
			}
			encoded, err := json.Marshal(warning)
			if err != nil {
				t.Fatal(err)
			}
			for _, canary := range []string{"private-summary-5e6d", "private-finish-2e4c", "private-prompt-9f1a"} {
				if bytes.Contains(encoded, []byte(canary)) {
					t.Fatal("private validation content escaped")
				}
			}
		})
	}
}

func TestSummaryFailureValidationPreservesAPISuccess(t *testing.T) {
	t.Parallel()
	client, logger, path := contextManagerAttemptClient(t,
		&contextManagerAttemptAdapter{name: "anthropic"},
		&contextManagerAttemptAdapter{name: "openai"},
	)
	profile := WithCheapModel(NewOpenAIProfile("gpt-5.2"), "anthropic/claude-haiku-4-5-20251001")
	cm := NewManager(profile, client, cheapmodel.New(client))
	_, err := cm.completeSummarization(context.Background(), profile, "prompt", func(resp llm.Response) error { return checkSummaryReply(resp.Text(), "") })
	if err == nil {
		t.Fatal("empty replies accepted")
	}
	warning := summarizerFailureWarning(err)
	if warning.SummaryFailure == nil || warning.SummaryFailure.Terminal.Reason != "empty_reply" {
		t.Fatal("missing validation diagnostic")
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	assertContextManagerLogicalGroup(t, readContextManagerAttemptRecords(t, path), apilog.AttemptSuccess)
}

func TestSummaryFailureUncondensablePartnerMessages(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, cheap, model, route string
		refused                   bool
	}{
		{"session", "", "active-fixture", "session", false},
		{"cheap", "cheap-fixture", "cheap-fixture", "cheap", false},
		{"refused cheap", "cheap-fixture", "active-fixture", "session", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := llm.NewClient()
			var calls []string
			client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(req llm.Request) (llm.Response, error) {
				calls = append(calls, req.Model)
				if tc.refused && req.Model == "cheap-fixture" {
					return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 400, "provided model identifier is invalid", nil, nil)
				}
				return llm.Response{Message: llm.Assistant(strings.Repeat("x", 6001))}, nil
			}})
			profile := WithCheapModel(testProfile("openai", "active-fixture", 100000), tc.cheap)
			cm := NewManager(profile, client, cheapmodel.New(client))
			lines := []string{strings.Repeat("x", 6001)}
			original := append([]string(nil), lines...)
			_, err := cm.condensePartnerMessages(context.Background(), cm.currentProfile(), lines, 6000)
			if !errors.Is(err, errSummaryUncondensable) || len(calls) < 4 {
				t.Fatal("non-shrinking summary was accepted")
			}
			if !reflect.DeepEqual(lines, original) {
				t.Fatal("condensation changed the original messages")
			}
			for i, model := range calls {
				want := tc.model
				if tc.refused && i == 0 {
					want = "cheap-fixture"
				}
				if model != want {
					t.Fatalf("call %d selected %q, want %q", i, model, want)
				}
			}
			warning := summarizerFailureWarning(err)
			if warning.SummaryFailure == nil {
				t.Fatal("missing condensation diagnostic")
			}
			got := warning.SummaryFailure.Terminal
			if got.Kind != "rejected_output" || got.Stage != "validation" || got.Reason != "uncondensable_partner_messages" || got.Route != tc.route || got.Model != tc.model || got.Provider != "openai" {
				t.Fatalf("wrong condensation diagnostic: %+v", got)
			}
		})
	}
}

func TestSummaryFailureLaterCompactionRecovers(t *testing.T) {
	t.Parallel()
	failing := true
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: func(llm.Request) (llm.Response, error) {
		if failing {
			return llm.Response{}, llm.ErrorFromHTTPStatus("openai", 429, "private-error-3e4d", nil, nil)
		}
		return llm.Response{Message: llm.Assistant("## Progress\nrecovery-marker-9a2c")}, nil
	}})
	cm := NewManager(testOpenAIProfileWithContextWindow(minSummarizingWindow), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	history := canaryHistory()
	emit, warnings := warningsFrom()
	cm.ForceCompact(context.Background(), &history, "", emit)
	if len(*warnings) != 1 || (*warnings)[0].Code != events.WarningCodeSummaryFailure || history[0].Kind != schema.TurnCheckpoint {
		t.Fatalf("failure lost retained state: %+v", *warnings)
	}
	failing = false
	cm.ForceCompact(context.Background(), &history, "", emit)
	if len(*warnings) != 1 || history[0].Kind != schema.TurnSummary || !strings.Contains(history[0].Message.Text(), "recovery-marker-9a2c") {
		t.Fatal("later summary did not recover")
	}
	for _, original := range canaryHistory() {
		if original.Kind == schema.TurnUserInput && !strings.Contains(history[0].Message.Text(), original.Message.Text()) {
			found := false
			for _, retained := range history {
				found = found || retained.Message.Text() == original.Message.Text()
			}
			if !found {
				t.Fatal("human message lost during recovery")
			}
		}
	}
}
