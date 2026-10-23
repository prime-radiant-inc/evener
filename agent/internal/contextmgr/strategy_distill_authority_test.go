package contextmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/cheapmodel"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func distillAuthorityClient(replies ...llm.Response) (*Manager, *llm.Client, *fakeAdapter) {
	adapter := &fakeAdapter{name: "openai"}
	for _, reply := range replies {
		adapter.steps = append(adapter.steps, func(llm.Request) llm.Response { return reply })
	}
	client := llm.NewClient()
	client.Register(adapter)
	return NewManager(NewOpenAIProfile("gpt-5.2"), client, cheapmodel.New(client)), client, adapter
}

func distillAuthorityReply(text, finish string) llm.Response {
	return llm.Response{Message: llm.Assistant(text), Finish: llm.FinishReason{Reason: finish}}
}

func distillAuthoritySteer(text, source string) schema.Turn {
	turn := schema.NewTurn(schema.TurnSteering, llm.User(text))
	turn.SteeringSource = source
	return turn
}

func distillAuthorityActions(n int) []schema.Turn {
	history := make([]schema.Turn, n)
	for i := range history {
		history[i] = schema.NewTurn(schema.TurnAssistant, llm.Assistant(fmt.Sprintf("ACTION_%02d_4c19", i)))
	}
	return history
}

// Source text is deliberately longer than the old per-message detail limits.
// The opaque identifiers belong to this test, not to the summarizer's copy.
func TestDistillAuthoritySourceTransport(t *testing.T) {
	t.Parallel()
	user := "USER_BEGIN_118c\n" + strings.Repeat("user_detail_7a81 ", 80) + "\nYou may edit PERMISSION_9ed2, but do not publish HOLD_842a.\nUSER_END_118c"
	steer := "STEER_BEGIN_03af\n" + strings.Repeat("steer_detail_a6b1 ", 80) + "\nStop changes to HOLD_b7e2 until I approve.\nSTEER_END_03af"
	parent := "PARENT_BEGIN_e2d9\n" + strings.Repeat("parent_detail_51f0 ", 80) + "\nOnly inspect SCOPE_539a.\nPARENT_END_e2d9"
	system := "SYSTEM_BEGIN_b7c0\n" + strings.Repeat("daemon_detail_30c5 ", 80) + "\nNUDGE_f8c1\nSYSTEM_END_b7c0"
	checkpoint := "CHECKPOINT_BEGIN_c7a6\n" + strings.Repeat("checkpoint_detail_9b74 ", 80) + "\nUser message 2: \"Do not publish HOLD_528b.\"\nCHECKPOINT_END_c7a6"
	summary := "SUMMARY_BEGIN_a83c\n" + strings.Repeat("summary_detail_f053 ", 80) + "\nUser message 3: \"You may inspect PERMISSION_421f.\"\nSUMMARY_END_a83c"
	sources := []struct {
		label string
		text  string
		turn  schema.Turn
	}{
		{"User", user, schema.NewTurn(schema.TurnUserInput, llm.User(user))},
		{"User", steer, distillAuthoritySteer(steer, events.SteeringSourceUser)},
		{"Parent", parent, distillAuthoritySteer(parent, events.SteeringSourceParent)},
		{"System", system, distillAuthoritySteer(system, "")},
		{"Previous compaction", checkpoint, schema.NewTurn(schema.TurnCheckpoint, llm.User(checkpoint))},
		{"Previous compaction", summary, schema.NewTurn(schema.TurnSummary, llm.User(summary))},
	}
	for _, tc := range []struct {
		name  string
		width int
	}{
		{"crystals", 6},
		{"micro", 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cm, client, adapter := distillAuthorityClient(distillAuthorityReply("REPLY_529d", llm.FinishReasonStop))
			const excluded = "EXCLUDED_SOURCE_b49e"
			history := append([]schema.Turn{schema.NewTurn(schema.TurnUserInput, llm.User(excluded))}, distillAuthorityActions(tc.width-4)...)
			for _, source := range sources {
				history = append(history, source.turn)
			}
			var strategy Strategy
			if tc.name == "crystals" {
				strategy = NewMemoryCrystalsStrategy(cm)
			} else {
				strategy = NewRecursiveDistillStrategy(cm)
			}
			if err := strategy.AfterAction(context.Background(), history, client); err != nil {
				t.Fatalf("AfterAction: %v", err)
			}
			requests := adapter.Requests()
			if len(requests) != 1 {
				t.Fatalf("summary requests = %d, want 1", len(requests))
			}
			prompt := requests[0].Messages[0].Text()
			for i, source := range sources {
				if !strings.Contains(prompt, source.label+": "+source.text) {
					t.Errorf("source record %d (%s, %d bytes) did not reach the provider whole with its attribution", i, source.label, len(source.text))
				}
				if got := strings.Count(prompt, source.text); got != 1 {
					t.Errorf("source record %d appeared %d times, want once", i, got)
				}
			}
			if strings.Contains(prompt, excluded) {
				t.Error("history outside the selected recent slice reached the provider")
			}
		})
	}
}

func TestDistillAuthorityRejectsLengthReplies(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"crystals", "micro", "macro"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cm, _, adapter := distillAuthorityClient(distillAuthorityReply("PARTIAL_19dc", llm.FinishReasonLength))
			var text string
			var err error
			switch name {
			case "crystals":
				var crystal MemoryCrystal
				crystal, err = NewMemoryCrystalsStrategy(cm).crystallize(context.Background(), distillAuthorityActions(6), 6)
				if crystal != (MemoryCrystal{}) {
					t.Errorf("length-limited crystal escaped: %+v", crystal)
				}
			case "micro":
				text, err = NewRecursiveDistillStrategy(cm).microSummarize(context.Background(), distillAuthorityActions(10))
			case "macro":
				text, err = NewRecursiveDistillStrategy(cm).macroSummarize(context.Background(), []string{"SOURCE_1a49"})
			}
			if !errors.Is(err, errSummaryOutputLimit) {
				t.Errorf("length-limited reply error = %v, want errSummaryOutputLimit", err)
			}
			if text != "" {
				t.Errorf("length-limited summary escaped: %q", text)
			}
			if got := len(adapter.Requests()); got != 1 {
				t.Errorf("requests = %d, want 1", got)
			}
		})
	}
}

func TestDistillAuthorityLengthFailureState(t *testing.T) {
	t.Parallel()
	t.Run("crystals", func(t *testing.T) {
		t.Parallel()
		cm, client, adapter := distillAuthorityClient(
			distillAuthorityReply("PARTIAL_c571", llm.FinishReasonLength),
			distillAuthorityReply("RECOVERED_67ba", llm.FinishReasonStop),
		)
		s := NewMemoryCrystalsStrategy(cm)
		before := []MemoryCrystal{{Turn: 3, Action: "read_file", Facts: "BANKED_54ca"}}
		s.crystals = append([]MemoryCrystal(nil), before...)
		if err := s.AfterAction(context.Background(), distillAuthorityActions(6), client); !errors.Is(err, errSummaryOutputLimit) {
			t.Errorf("length failure = %v, want errSummaryOutputLimit", err)
		}
		if !reflect.DeepEqual(s.crystals, before) {
			t.Errorf("length failure changed crystal bank: %+v", s.crystals)
		}
		if err := s.AfterAction(context.Background(), distillAuthorityActions(7), client); !errors.Is(err, errSummaryOutputLimit) {
			t.Errorf("skipped action lost auxiliary failure: %v", err)
		}
		if got := len(adapter.Requests()); got != 1 {
			t.Errorf("skipped action made a request: count=%d", got)
		}
		if err := s.AfterAction(context.Background(), distillAuthorityActions(9), client); err != nil {
			t.Fatalf("recovery: %v", err)
		}
		if len(s.crystals) != 2 || s.crystals[1].Facts != "RECOVERED_67ba" || s.crystals[1].Turn != 9 || s.aux.stale() != nil {
			t.Errorf("recovery state: bank=%+v failure=%v", s.crystals, s.aux.stale())
		}
	})
	t.Run("micro", func(t *testing.T) {
		t.Parallel()
		cm, client, adapter := distillAuthorityClient(
			distillAuthorityReply("PARTIAL_6c8e", llm.FinishReasonLength),
			distillAuthorityReply("RECOVERED_d113", llm.FinishReasonStop),
		)
		s := NewRecursiveDistillStrategy(cm)
		s.microSummaries = []string{"BANKED_ec08"}
		s.lastMicroAt = 10
		if err := s.AfterAction(context.Background(), distillAuthorityActions(20), client); !errors.Is(err, errSummaryOutputLimit) {
			t.Errorf("length failure = %v, want errSummaryOutputLimit", err)
		}
		if !reflect.DeepEqual(s.microSummaries, []string{"BANKED_ec08"}) || s.lastMicroAt != 10 {
			t.Errorf("length failure consumed micro state: bank=%q watermark=%d", s.microSummaries, s.lastMicroAt)
		}
		if err := s.AfterAction(context.Background(), distillAuthorityActions(11), client); !errors.Is(err, errSummaryOutputLimit) {
			t.Errorf("skipped action lost auxiliary failure: %v", err)
		}
		if got := len(adapter.Requests()); got != 1 {
			t.Errorf("skipped action made a request: count=%d", got)
		}
		if err := s.AfterAction(context.Background(), distillAuthorityActions(20), client); err != nil {
			t.Fatalf("eligible retry: %v", err)
		}
		if !reflect.DeepEqual(s.microSummaries, []string{"BANKED_ec08", "RECOVERED_d113"}) || s.lastMicroAt != 20 || s.aux.stale() != nil {
			t.Errorf("retry state: bank=%q watermark=%d failure=%v", s.microSummaries, s.lastMicroAt, s.aux.stale())
		}
	})
}

// The provider scripts only model responses. All five micro calls, their
// ten-turn selection, and the macro fold run through the real AfterAction.
func TestDistillAuthorityAfterActionMicroToMacro(t *testing.T) {
	t.Parallel()
	for _, finish := range []string{llm.FinishReasonStop, llm.FinishReasonLength} {
		t.Run(finish, func(t *testing.T) {
			t.Parallel()
			var micros []string
			var replies []llm.Response
			history := distillAuthorityActions(51)
			for i := range 5 {
				source := fmt.Sprintf("You may edit SCOPE_%d_3c29, but do not publish HOLD_%d_6f18.", i, i)
				if i%2 == 0 {
					history[i*10] = schema.NewTurn(schema.TurnUserInput, llm.User(source))
				} else {
					history[i*10] = distillAuthoritySteer(source, events.SteeringSourceUser)
				}
				micro := fmt.Sprintf("User message %d: %q\nMICRO_%d_42b7", i+1, source, i)
				micros = append(micros, micro)
				replies = append(replies, distillAuthorityReply(micro, llm.FinishReasonStop))
			}
			const macro = "MACRO_c8f4"
			replies = append(replies, distillAuthorityReply(macro, finish))
			cm, client, adapter := distillAuthorityClient(replies...)
			s := NewRecursiveDistillStrategy(cm)
			for n := 1; n <= 50; n++ {
				err := s.AfterAction(context.Background(), history[:n], client)
				if n == 50 && finish == llm.FinishReasonLength {
					if !errors.Is(err, errSummaryOutputLimit) {
						t.Errorf("macro length failure = %v, want errSummaryOutputLimit", err)
					}
				} else if err != nil {
					t.Fatalf("AfterAction at %d: %v", n, err)
				}
			}
			requests := adapter.Requests()
			if len(requests) != 6 {
				t.Fatalf("requests over 50 actions = %d, want 5 micros and 1 macro", len(requests))
			}
			for i := range 5 {
				if !strings.Contains(requests[i].Messages[0].Text(), "User: "+history[i*10].Message.Text()) {
					t.Errorf("micro %d lost its selected user authority", i)
				}
				if !strings.Contains(requests[5].Messages[0].Text(), micros[i]) {
					t.Errorf("macro input lost micro %d's exact source quotation", i)
				}
			}
			if s.lastMicroAt != 50 {
				t.Errorf("lastMicroAt = %d, want 50", s.lastMicroAt)
			}
			if finish == llm.FinishReasonLength {
				if !reflect.DeepEqual(s.microSummaries, micros) || len(s.macroSummaries) != 0 || s.lastMacroAt != 0 {
					t.Errorf("macro length failure consumed its five sources: micro=%q macro=%q watermark=%d", s.microSummaries, s.macroSummaries, s.lastMacroAt)
				}
				if err := s.AfterAction(context.Background(), history, client); !errors.Is(err, errSummaryOutputLimit) {
					t.Errorf("skipped action lost macro failure: %v", err)
				}
				if got := len(adapter.Requests()); got != 6 {
					t.Errorf("skipped action retried macro: requests=%d", got)
				}
				return
			}
			if s.microSummaries != nil || !reflect.DeepEqual(s.macroSummaries, []string{macro}) || s.lastMacroAt != 50 {
				t.Errorf("successful macro state: micro=%q macro=%q watermark=%d", s.microSummaries, s.macroSummaries, s.lastMacroAt)
			}
		})
	}
}

// The complete adapter requests pin prompt prose. Behavior assertions elsewhere
// use only source text supplied by this test and typed bank/watermark state.
func TestDistillAuthorityPromptGoldens(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"crystals", "micro", "macro"} {
		for _, authority := range []string{"none", "quoted"} {
			t.Run(stage+"_"+authority, func(t *testing.T) {
				t.Parallel()
				history := []schema.Turn{
					schema.NewTurn(schema.TurnUserInput, llm.User("Where is the request parser?")),
					schema.NewTurn(schema.TurnAssistant, llm.Assistant("I would review the diff before making edits.")),
				}
				micros := []string{
					"User question: \"Where is the request parser?\"",
					"Assistant caution: I would review the diff before making edits.",
				}
				if authority == "quoted" {
					history = []schema.Turn{
						schema.NewTurn(schema.TurnUserInput, llm.User("You may edit FILE_8f41, but do not publish HOLD_493a.")),
						schema.NewTurn(schema.TurnAssistant, llm.Assistant("I would get review before touching anything.")),
						distillAuthoritySteer("Stop edits to PATH_0c92.", events.SteeringSourceUser),
						distillAuthoritySteer("Inspect PARENT_c813 only.", events.SteeringSourceParent),
						schema.NewTurn(schema.TurnCheckpoint, llm.User("User message 0: \"Do not restart HOLD_d72a.\"")),
						schema.NewTurn(schema.TurnSummary, llm.User("Assistant caution: I suggest waiting for REVIEW_608e.")),
					}
					micros = []string{
						"User message 1: \"You may edit FILE_8f41, but do not publish HOLD_493a.\"",
						"Assistant caution: I would get review before touching anything.",
						"User steering message 3: \"Stop edits to PATH_0c92.\"",
						"Parent steering message 4: \"Inspect PARENT_c813 only.\"",
						"Previous compaction: User message 0: \"Do not restart HOLD_d72a.\"",
					}
				}
				cm, _, adapter := distillAuthorityClient(distillAuthorityReply("REPLY_5bc1", llm.FinishReasonStop))
				var err error
				switch stage {
				case "crystals":
					_, err = NewMemoryCrystalsStrategy(cm).crystallize(context.Background(), history, 6)
				case "micro":
					_, err = NewRecursiveDistillStrategy(cm).microSummarize(context.Background(), history)
				case "macro":
					_, err = NewRecursiveDistillStrategy(cm).macroSummarize(context.Background(), micros)
				}
				if err != nil {
					t.Fatalf("summary request: %v", err)
				}
				requests := adapter.Requests()
				if len(requests) != 1 {
					t.Fatalf("requests = %d, want 1", len(requests))
				}
				requestJSON, err := json.MarshalIndent(requests[0], "", "  ")
				if err != nil {
					t.Fatalf("marshal request: %v", err)
				}
				golden := "```json\n" + string(requestJSON) + "\n```\n"
				agenttest.CheckGolden(t, filepath.Join("testdata", "strategyauthority", stage+"_"+authority+".md"), []byte(golden), *updateSummaryPromptGoldens,
					"Regenerate with `go test ./agent/internal/contextmgr -run '^TestDistillAuthorityPromptGoldens$' -count=1 -update-prompt` and read every complete request diff.")
			})
		}
	}
}
