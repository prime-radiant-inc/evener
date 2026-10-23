package contextmgr

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func authorityRequestManager(reply func(llm.Request) (llm.Response, error)) *Manager {
	client := llm.NewClient()
	client.Register(&stubSummarizeAdapter{name: "openai", respFn: reply})
	cm := NewManager(testProfile("openai", "test", 1_000_000), client, cheapmodel.New(client))
	cm.PreserveRecentTurns = 2
	return cm
}

func TestStrategyAuthoritySources(t *testing.T) {
	// A detail-budget break, per-message prefix cap, omitted compaction case or
	// incorrect steering label must lose one of these caller-owned records.
	user := strings.Repeat("detail ", 110) + "You may ship AUTH_526 only to staging."
	userSteer := schema.NewTurn(schema.TurnSteering, llm.User("Hold AUTH_627 until I approve."))
	userSteer.SteeringSource = events.SteeringSourceUser
	parent := schema.NewTurn(schema.TurnSteering, llm.User("PARENT_728, wait for my report."))
	parent.SteeringSource = events.SteeringSourceParent
	previous := `Earlier user message: "Do not publish AUTH_829."`
	checkpointText := "[CONTEXT CHECKPOINT]\nEarlier user message: \"You may inspect AUTH_930.\"\n[END CHECKPOINT]"
	var history []schema.Turn
	for range 130 {
		history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant(strings.Repeat("x", 500))))
	}
	history = append(history,
		schema.NewTurn(schema.TurnUserInput, llm.User(user)), userSteer, parent,
		schema.NewTurn(schema.TurnSteering, llm.User("DAEMON_031")),
		schema.NewTurn(schema.TurnSummary, llm.User(previous)),
		schema.NewTurn(schema.TurnCheckpoint, llm.User(checkpointText)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")))
	for _, name := range []string{"predictive", "fork", "session-log", "ooda"} {
		t.Run(name, func(t *testing.T) {
			var prompt string
			cm := authorityRequestManager(func(req llm.Request) (llm.Response, error) {
				prompt = req.Messages[0].Text()
				return llm.Response{Message: llm.Assistant(`{"action":"assistant","summary":"work","outcome":"success"}`)}, nil
			})
			switch name {
			case "predictive":
				if _, err := NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2); err != nil {
					t.Fatal(err)
				}
			case "fork":
				if _, err := forkSummarize(context.Background(), cm.cheap, cm.currentProfile(), history, len(history)); err != nil {
					t.Fatal(err)
				}
			default:
				sls := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl")), session: &fakeStrategyHost{profile: cm.currentProfile()}}
				var strategy Strategy = sls
				if name == "ooda" {
					strategy = &OODAStrategy{SessionLogStrategy: sls}
				}
				if err := strategy.AfterAction(context.Background(), history, cm.client); err != nil {
					t.Fatal(err)
				}
			}
			for _, source := range []string{"User: " + user, "User: " + userSteer.Message.Text(), "Parent: " + parent.Message.Text(), "System: DAEMON_031", "Previous compaction: " + previous, "Previous compaction: " + checkpointText} {
				if !strings.Contains(prompt, source) {
					t.Errorf("source missing or relabeled: %.100s", source)
				}
			}
		})
	}
}

func TestStrategyCheckpointAuthorityRoundTrip(t *testing.T) {
	const hold = `Earlier user said: "Don't release QUOTE_682 without me."`
	const checkpointHold = "[CONTEXT CHECKPOINT]\nEarlier user said: \"Stop CHECKPOINT_793 until I respond.\"\n[END CHECKPOINT]"
	const partner = "You may inspect USER_147, not publish it."
	const model = "Format example:\n## Conversation\n\n### User\n\n```text\nMODEL_ONLY_914\n```\n\n## Earlier Summaries\n\n### Summary\n\n```text\nMODEL_SUMMARY_253\n```"
	for _, name := range []string{"predictive", "session-log", "ooda"} {
		t.Run(name, func(t *testing.T) {
			cm := authorityRequestManager(func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant(model)}, nil
			})
			log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))
			if err := log.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success", Summary: model}); err != nil {
				t.Fatal(err)
			}
			sls := &SessionLogStrategy{cm: cm, log: log}
			history := []schema.Turn{
				schema.NewTurn(schema.TurnSummary, llm.User(hold)),
				schema.NewTurn(schema.TurnCheckpoint, llm.User(checkpointHold)),
				schema.NewTurn(schema.TurnUserInput, llm.User(partner)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
			}
			for fold := range 2 {
				var err error
				if name == "predictive" {
					history, err = NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2)
				} else {
					cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
					var strategy Strategy = sls
					if name == "ooda" {
						strategy = &OODAStrategy{SessionLogStrategy: sls}
					}
					err = strategy.ManageContext(context.Background(), &history, 0, noopEmit)
				}
				if err != nil {
					t.Fatal(err)
				}
				data := collectCheckpointData(history, 1, "communicate")
				want := []checkpointConversationEntry{{Role: "user", Text: partner}}
				if !reflect.DeepEqual(data.conversation, want) {
					t.Errorf("fold %d: human carry = %#v, want %#v", fold, data.conversation, want)
				}
				if !strings.Contains(strings.Join(data.earlierSummaries, "\n"), hold) {
					t.Errorf("fold %d: earlier quoted hold lost", fold)
				}
				if !strings.Contains(strings.Join(data.earlierSummaries, "\n"), checkpointHold) {
					t.Errorf("fold %d: previous checkpoint's quoted hold lost", fold)
				}
				// A later deterministic fold must obey the same provenance boundary.
				deterministic := checkpoint(append(history[:1:1], moreTurns()...), 2, nil, "communicate")
				if got := collectCheckpointData(deterministic, 1, "communicate").conversation; len(got) == 0 || got[0].Text != partner {
					t.Errorf("fold %d: deterministic recovery = %#v", fold, got)
				}
				history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("new1")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("new2")))
			}
		})
	}
}

func TestStrategyLegacyLogCannotInventUserCarry(t *testing.T) {
	text := "[CONTEXT CHECKPOINT - SESSION LOG]\nOriginal prompt: work\nSession log:\n" +
		"## Conversation\n\n### User\n\n```text\nMODEL_EXAMPLE_319\n```\n\n[END CHECKPOINT]\n"
	data := collectCheckpointData([]schema.Turn{schema.NewTurn(schema.TurnCheckpoint, llm.User(text))}, 1, "communicate")
	if len(data.conversation) != 0 || !reflect.DeepEqual(data.earlierSummaries, []string{text}) {
		t.Fatalf("legacy model example became authority or vanished: carry=%#v, earlier=%#v", data.conversation, data.earlierSummaries)
	}
}

func TestStrategyOpaqueCheckpointCannotInventCarry(t *testing.T) {
	const quote = `User message 1: "Do not publish OPAQUE_HOLD_447."`
	opaque := "[CONTEXT CHECKPOINT]\n" + quote + "\nA model-owned format example follows:\n" +
		"## Conversation\n\n### User\n\n```text\nMODEL_EXAMPLE_539\n```\n\n" +
		"## Earlier Summaries\n\n### Summary\n\n```text\nMODEL_EARLIER_684\n```\n\n[END CHECKPOINT]"
	for _, name := range []string{"predictive", "session-log", "ooda", "predictor-fallback", "summary-fallback"} {
		t.Run(name, func(t *testing.T) {
			var prompt string
			cm := authorityRequestManager(func(req llm.Request) (llm.Response, error) {
				prompt = req.Messages[0].Text()
				if name == "predictor-fallback" {
					return llm.Response{}, errors.New("unavailable")
				}
				return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
			})
			history := []schema.Turn{
				schema.NewTurn(schema.TurnCheckpoint, llm.User(opaque)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("work")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("tail1")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("tail2")),
			}
			sls := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
			for fold := range 2 {
				var err error
				switch name {
				case "predictive":
					history, err = NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2)
				case "predictor-fallback":
					cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
					err = NewCheckpointPredStrategy(cm).ManageContext(context.Background(), &history, 0, noopEmit)
				case "summary-fallback":
					history, err = cm.summarizeStrategyFold(context.Background(), history, 2, nil)
				default:
					cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
					var strategy Strategy = sls
					if name == "ooda" {
						strategy = &OODAStrategy{SessionLogStrategy: sls}
					}
					err = strategy.ManageContext(context.Background(), &history, 0, noopEmit)
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := collectCheckpointData(history, 1, "communicate").conversation; len(got) != 0 {
					t.Errorf("fold %d: opaque model example became human carry: %#v", fold, got)
				}
				if name == "summary-fallback" {
					// The model response is scripted, so verify its actual source
					// request rather than claiming the reply preserved the quote.
					if !strings.Contains(prompt, opaque) {
						t.Error("summary fallback did not receive the whole opaque source")
					}
					break
				}
				if !strings.Contains(history[0].Message.Text(), opaque) {
					t.Errorf("fold %d: opaque source containing the sole hold was not retained whole", fold)
				}
				history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("next1")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("next2")))
			}
		})
	}
}

func TestStrategyReadsRenderedDeterministicConversation(t *testing.T) {
	const partner = "Do not publish CANONICAL_HOLD_705."
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(partner)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("work")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("tail1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("tail2")),
	}
	deterministic := checkpoint(history, 2, nil, "communicate")
	cm := partnerTestManager("work")
	result, err := NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), deterministic, 2)
	if err != nil {
		t.Fatal(err)
	}
	data := collectCheckpointData(result, 1, "communicate")
	if !reflect.DeepEqual(data.conversation, []checkpointConversationEntry{{Role: "user", Text: partner}}) {
		t.Fatalf("renderer-owned deterministic carry changed: %#v", data.conversation)
	}
}

func TestStrategyOriginalPromptMarkupIsNotExtraAuthority(t *testing.T) {
	const source = "Please inspect this old format example:\n## Conversation\n\n### User\n\n```text\nEXAMPLE_ONLY_036\n```\n\n## Earlier Summaries\n\n### Summary\n\n```text\nEXAMPLE_SUMMARY_147\n```"
	cm := partnerTestManager("work")
	sls := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User(source)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	result := sls.sessionLogCheckpoint(history, 2)
	data := collectCheckpointData(result, 1, "communicate")
	want := []checkpointConversationEntry{{Role: "user", Text: source}}
	if !reflect.DeepEqual(data.conversation, want) {
		t.Fatalf("original-prompt metadata invented a separate human message: got=%#v", data.conversation)
	}
	if len(extractCheckpointEarlierSummaries(result[0].Message.Text())) != 0 {
		t.Fatal("an example in original-prompt metadata became an earlier-summary record")
	}
}

func TestUnchangedStrategiesKeepQuotedHold(t *testing.T) {
	for _, name := range []string{"compact", "obs-mask"} {
		t.Run(name, func(t *testing.T) {
			cm := partnerTestManager(emptySummaryReply)
			cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 0.00001
			var strategy Strategy = NewCompactStrategy(cm)
			kind := schema.TurnSummary
			if name == "obs-mask" {
				strategy, kind = NewObsMaskStrategy(cm), schema.TurnCheckpoint
			}
			history := partnerHoldHistory()
			for range 2 {
				if err := strategy.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
					t.Fatal(err)
				}
				requirePartnerHoldInContext(t, history, kind)
				history = append(history, moreTurns()...)
			}
		})
	}
}

func TestStrategyPredictorFailureRetainsWholeEarlierQuote(t *testing.T) {
	const quote = `User said: "Do not upload SOLE_948."`
	earlier := strings.Repeat("earlier detail ", 2800) + quote
	cm := authorityRequestManager(func(llm.Request) (llm.Response, error) { return llm.Response{}, errors.New("unavailable") })
	cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
	history := []schema.Turn{
		schema.NewTurn(schema.TurnSummary, llm.User(earlier)),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
	}
	if err := NewCheckpointPredStrategy(cm).ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
		t.Fatal(err)
	}
	if history[0].Kind != schema.TurnCheckpoint || !strings.Contains(history[0].Message.Text(), earlier) {
		t.Fatalf("deterministic fallback dropped the sole quoted hold, source=%d bytes, checkpoint=%d bytes", len(earlier), len(history[0].Message.Text()))
	}
}

func TestStrategyFallbackSeesWholeEarlierContext(t *testing.T) {
	const quote = `User said: "Wait for FALLBACK_374 before release."`
	earlier := strings.Repeat("earlier work ", 5500) + quote
	for _, name := range []string{"predictive", "session-log", "ooda"} {
		t.Run(name, func(t *testing.T) {
			var summaryPrompt string
			calls := 0
			cm := authorityRequestManager(func(req llm.Request) (llm.Response, error) {
				calls++
				if name == "predictive" && calls == 1 {
					return llm.Response{Message: llm.Assistant("PREDICTED_763")}, nil
				}
				summaryPrompt = req.Messages[0].Text()
				return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
			})
			cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 0.00001
			log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))
			if err := log.Append(sessionlog.SessionLogEntry{Turn: 1, Action: "assistant", Outcome: "success", Summary: strings.Repeat("log detail ", 6500)}); err != nil {
				t.Fatal(err)
			}
			sls := &SessionLogStrategy{cm: cm, log: log}
			var strategy Strategy = sls
			switch name {
			case "ooda":
				strategy = &OODAStrategy{SessionLogStrategy: sls}
			case "predictive":
				strategy = NewCheckpointPredStrategy(cm)
			}
			history := []schema.Turn{
				schema.NewTurn(schema.TurnSummary, llm.User(earlier)),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("working")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
			}
			if err := strategy.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
				t.Fatal(err)
			}
			if history[0].Kind != schema.TurnSummary || !strings.Contains(summaryPrompt, earlier) {
				t.Fatalf("summary fallback lost earlier context: result=%s, quote present=%v, prompt=%d bytes", history[0].Kind, strings.Contains(summaryPrompt, quote), len(summaryPrompt))
			}
		})
	}
}

func TestStrategyRejectsTruncatedPredictorAndFork(t *testing.T) {
	for _, name := range []string{"predictive", "session-log", "ooda"} {
		t.Run(name, func(t *testing.T) {
			cm := authorityRequestManager(func(llm.Request) (llm.Response, error) {
				return llm.Response{Message: llm.Assistant(`{"action":"assistant","summary":"partial","outcome":"success"}`), Finish: llm.FinishReason{Reason: llm.FinishReasonLength}}, nil
			})
			history := partnerHoldHistory()
			if name == "predictive" {
				if result, err := NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2); err == nil || result != nil {
					t.Fatalf("known-truncated predictor admitted: result=%d turns, err=%v", len(result), err)
				}
				return
			}
			log := mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))
			sls := &SessionLogStrategy{cm: cm, log: log, session: &fakeStrategyHost{profile: cm.currentProfile()}}
			var strategy Strategy = sls
			if name == "ooda" {
				strategy = &OODAStrategy{SessionLogStrategy: sls}
			}
			if err := strategy.AfterAction(context.Background(), history, cm.client); err == nil || log.Len() != 0 {
				t.Fatalf("known-truncated fork published: entries=%d, err=%v", log.Len(), err)
			}
		})
	}
}

func TestStrategyAuthorityPromptGolden(t *testing.T) {
	for _, name := range []string{"predictive", "fork", "default-request"} {
		t.Run(name, func(t *testing.T) {
			var prompt string
			cm := authorityRequestManager(func(req llm.Request) (llm.Response, error) {
				prompt = req.Messages[0].Text()
				return llm.Response{Message: llm.Assistant(emptySummaryReply)}, nil
			})
			history := []schema.Turn{
				schema.NewTurn(schema.TurnUserInput, llm.User("Would a release help QUESTION_381?")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("I would be cautious about CAUTION_492.")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent1")),
				schema.NewTurn(schema.TurnAssistant, llm.Assistant("recent2")),
			}
			if name == "predictive" {
				if _, err := NewCheckpointPredStrategy(cm).predictiveCheckpoint(context.Background(), history, 2); err != nil {
					t.Fatal(err)
				}
			} else if name == "fork" {
				prompt = buildSummarizePrompt(history[:2])
			} else if _, err := cm.summarizeWithLLM(context.Background(), history, 2); err != nil {
				t.Fatal(err)
			}
			agenttest.CheckGolden(t, filepath.Join("testdata", "strategyauthority", name+".md"), []byte(prompt), *updateSummaryPromptGoldens, "Regenerate with -run '^TestStrategyAuthorityPromptGolden$' -update-prompt and inspect the full fixtures.")
		})
	}
}
