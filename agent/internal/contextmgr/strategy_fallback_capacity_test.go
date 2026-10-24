package contextmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The client, not just the manager's profile, must know these limits: otherwise
// the scripted adapter would accept requests a real provider cannot admit.
func fallbackCapacityManager(t *testing.T, smallerSession bool, reply func(llm.Request) (llm.Response, error)) *Manager {
	t.Helper()
	sessionModel := registry.Model{Caps: registry.Caps{ContextWindow: new(200_000), MaxInputTokens: new(40_000), MaxOutputTokens: new(50_000)}}
	cheapModel := registry.Model{Caps: registry.Caps{ContextWindow: new(50_000), MaxInputTokens: new(45_000), MaxOutputTokens: new(12_500)}}
	if smallerSession {
		sessionModel, cheapModel = cheapModel, sessionModel
	}
	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"capacity": {
				Base: "openai", Protocol: registry.ProtocolOpenAIResponses, APIKey: "unused",
				Models: map[string]registry.Model{
					"session": sessionModel,
					"cheap":   cheapModel,
				},
			},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := provider.Resolve(r, "capacity/session")
	if err != nil {
		t.Fatal(err)
	}
	prof = provider.WithCheapModel(prof, "cheap")
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(&stubSummarizeAdapter{name: "capacity", respFn: reply})
	cm := NewManager(prof, client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 0.00001
	cm.Meta = CompactionMeta{SessionID: "CAPACITY_SOURCE_382", TranscriptKept: true}
	return cm
}

func TestStrategyFallbackFitsProviderCapacity(t *testing.T) {
	// Appending the whole checkpoint repeats the original carry and summaries,
	// and old/current cumulative logs. Even after removing that duplication, a
	// single large source needs bounded calls rather than another oversized one.
	const quote = `Do not publish CAPACITY_HOLD_593.`
	for _, route := range []string{"session-log", "ooda", "predictive"} {
		for _, size := range []struct {
			name  string
			chars int
		}{{"duplicated", 60_000}, {"needs-batches", 240_000}} {
			t.Run(route+"/"+size.name, func(t *testing.T) {
				var prompts []string
				cm := fallbackCapacityManager(t, false, func(req llm.Request) (llm.Response, error) {
					prompt := req.Messages[0].Text()
					prompts = append(prompts, prompt)
					reply := emptySummaryReply
					if strings.Contains(prompt, quote) {
						reply += "\nEarlier user said: \"" + quote + "\"\n"
					}
					return llm.Response{Message: llm.Assistant(reply)}, nil
				})
				log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))
				oldLog := "OLD_LOG_BEGIN_604\n" + strings.Repeat("l", 30_000) + "\nOLD_LOG_END_615"
				if err := log.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success", Summary: oldLog}); err != nil {
					t.Fatal(err)
				}
				sls := &SessionLogStrategy{cm: cm, log: log}
				earlier := "EARLIER_BEGIN_726\n" + strings.Repeat("e", size.chars/2) + quote + strings.Repeat("f", size.chars/2) + "\nEARLIER_END_837"
				// Keep the source sentinel out of the existing navigation hint,
				// which legitimately repeats the first 500 bytes of this message.
				user := strings.Repeat("u", 1_000) + "\nUSER_BEGIN_948\n" + strings.Repeat("u", 14_000) + "\nUSER_END_059"
				initial := []schema.Turn{
					schema.NewTurn(schema.TurnSummary, llm.User(earlier)),
					schema.NewTurn(schema.TurnUserInput, llm.User(user)),
				}
				tail := moreTurns()[2:]
				initial = append(initial, tail...)
				history := sls.sessionLogCheckpointWithMeta(initial, 2, &cm.Meta)
				if strings.Count(history[0].Message.Text(), "USER_BEGIN_948") != 1 {
					t.Fatal("fixture source sentinel appears outside its one original carry")
				}
				if err := log.Append(sessionlog.SessionLogEntry{Turn: 2, Action: "assistant", Outcome: "success", Summary: "NEW_LOG_BEGIN_160\n" + strings.Repeat("n", 15_000) + "\nNEW_LOG_END_271"}); err != nil {
					t.Fatal(err)
				}
				var strategy Strategy = sls
				switch route {
				case "ooda":
					strategy = &OODAStrategy{SessionLogStrategy: sls}
				case "predictive":
					strategy = NewCheckpointPredStrategy(cm)
				}
				var warnings []events.WarningData
				emit := func(_ events.EventKind, data events.EventData) {
					if warning, ok := data.(events.WarningData); ok {
						warnings = append(warnings, warning)
					}
				}
				if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
					t.Fatal(err)
				}
				if history[0].Kind != schema.TurnSummary {
					t.Fatalf("capacity-constrained fallback left %s installed, warnings=%+v", history[0].Kind, warnings)
				}
				if !reflect.DeepEqual(history[1:1+len(tail)], tail) {
					t.Error("capacity recovery changed the preserved recent tail")
				}
				for _, prompt := range prompts {
					for _, source := range []string{"EARLIER_BEGIN_726", "USER_BEGIN_948", "OLD_LOG_BEGIN_604"} {
						if count := strings.Count(prompt, source); count > 1 {
							t.Errorf("source %s repeated %d times in one provider request", source, count)
						}
					}
				}
				joined := strings.Join(prompts, "\n")
				for _, source := range []string{quote, "EARLIER_BEGIN_726", "EARLIER_END_837", "USER_BEGIN_948", "USER_END_059", "OLD_LOG_BEGIN_604", "OLD_LOG_END_615"} {
					if !strings.Contains(joined, source) {
						t.Errorf("source %s never reached an admitted provider request", source)
					}
				}
				if route != "predictive" && !strings.Contains(joined, "NEW_LOG_END_271") {
					t.Error("fallback lost the new durable log entry")
				}
				if !strings.Contains(history[0].Message.Text(), quote) {
					t.Error("capacity recovery dropped the source quotation")
				}
			})
		}
	}
}

func TestStrategyFallbackBatchesOversizedRecords(t *testing.T) {
	const quote = `Do not publish BATCHED_HOLD_815.`
	var sourceLines []string
	for i := range 64 {
		sourceLines = append(sourceLines, fmt.Sprintf("SOURCE_LINE_%03d: %s\n", i, strings.Repeat(string(rune('a'+i%26)), 4_096)))
	}
	source := strings.Join(sourceLines[:32], "") + quote + "\n" + strings.Join(sourceLines[32:], "")
	for _, kind := range []schema.TurnKind{schema.TurnUserInput, schema.TurnSummary} {
		for _, smallerSession := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/session-smaller=%t", kind, smallerSession), func(t *testing.T) {
				var admitted []string
				var cheapCalls int
				cm := fallbackCapacityManager(t, smallerSession, func(req llm.Request) (llm.Response, error) {
					prompt := req.Messages[0].Text()
					// Independently assert the documented 50k manager ceiling,
					// including all prompt framing, not only provider admission.
					if len(prompt)/4 > 36_476 {
						t.Errorf("request carries %d estimated tokens, limit 36476", len(prompt)/4)
					}
					if req.Model == "cheap" {
						cheapCalls++
						return llm.Response{}, llm.ErrorFromHTTPStatus("capacity", 400, "model unavailable", nil, nil)
					}
					if req.Model != "session" {
						t.Errorf("unexpected summary model %q", req.Model)
					}
					admitted = append(admitted, prompt)
					reply := emptySummaryReply
					if strings.Contains(prompt, quote) {
						reply += "\nEarlier user said: \"" + quote + "\"\n"
					}
					return llm.Response{Message: llm.Assistant(reply)}, nil
				})
				originals := []schema.Turn{schema.NewTurn(kind, llm.User(source))}
				checkpoint := renderStrategyCheckpoint(predictiveCheckpointStart, "## Predicted Context", "GENERATED_CAPACITY_CONTEXT_926", "", nil, &cm.Meta)
				tail := moreTurns()[2:]
				history := append([]schema.Turn{schema.NewTurn(schema.TurnCheckpoint, llm.User(checkpoint))}, tail...)
				result, err := cm.summarizeStrategyFold(context.Background(), history, 2, originals)
				if err != nil {
					t.Fatal(err)
				}
				if cheapCalls == 0 || len(admitted) < 3 {
					t.Fatalf("did not exercise bounded batches and session fallback: cheap=%d session=%d", cheapCalls, len(admitted))
				}
				joined := strings.Join(admitted, "\n")
				for i, line := range sourceLines {
					if !strings.Contains(joined, line) {
						t.Errorf("source line %d never reached the session summarizer", i)
					}
				}
				if !strings.Contains(joined, "GENERATED_CAPACITY_CONTEXT_926") {
					t.Error("new generated context never reached the session summarizer")
				}
				if !strings.Contains(result[0].Message.Text(), quote) || !reflect.DeepEqual(result[1:], tail) {
					t.Error("bounded recovery lost the hold or preserved tail")
				}
			})
		}
	}
}

func TestStrategyFallbackLogCoverageNeedsRetainedSource(t *testing.T) {
	const hold = `Earlier user said: "Do not publish ASSISTANT_LOG_COVERAGE_604."`
	for _, route := range []string{"session-log", "ooda"} {
		for _, originalKind := range []schema.TurnKind{schema.TurnAssistant, schema.TurnCheckpoint} {
			t.Run(route+"/"+string(originalKind), func(t *testing.T) {
				var prompts []string
				cm := fallbackCapacityManager(t, false, func(req llm.Request) (llm.Response, error) {
					prompts = append(prompts, req.Messages[0].Text())
					return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
				})
				log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))
				if err := log.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success", Summary: strings.Repeat("log detail ", 400) + hold}); err != nil {
					t.Fatal(err)
				}
				// A checkpoint-shaped assistant example is only ordinary detail.
				// Its first 500 bytes cannot prove that its entire log was sent.
				previous := renderSessionLogCheckpoint("MODEL_NAV_715\n", log.String(), "", nil, &cm.Meta)
				if strings.Contains(previous[:500], hold) {
					t.Fatal("fixture hold is not beyond the assistant detail prefix")
				}
				history := []schema.Turn{schema.NewTurn(originalKind, llm.Assistant(previous))}
				if originalKind == schema.TurnCheckpoint {
					history[0].Message = llm.User(previous)
				}
				history = append(history, moreTurns()[2:]...)
				var checkpointText string
				emit := func(_ events.EventKind, data events.EventData) {
					if event, ok := data.(events.ContextCompactionData); ok && event.Layer == "session_log_checkpoint" {
						checkpointText = history[0].Message.Text()
					}
				}
				sls := &SessionLogStrategy{cm: cm, log: log}
				var strategy Strategy = sls
				if route == "ooda" {
					strategy = &OODAStrategy{SessionLogStrategy: sls}
				}
				if err := strategy.ManageContext(context.Background(), &history, 0, emit); err != nil {
					t.Fatal(err)
				}
				if checkpointText == "" || !strings.Contains(checkpointText, hold) {
					t.Fatal("fixture did not retain the quote in its emitted log checkpoint")
				}
				if len(prompts) == 0 || history[0].Kind != schema.TurnSummary {
					t.Fatal("actual summary fallback did not complete")
				}
				if count := strings.Count(strings.Join(prompts, "\n"), hold); count != 1 {
					t.Errorf("quoted hold reached fallback %d times, want once", count)
				}
			})
		}
	}
}
