package contextmgr

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/sessionlog"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestSessionLogCheckpointDoesNotMultiplySnapshots(t *testing.T) {
	t.Parallel()
	const partner = "Do not publish PARTNER_591."
	const earlier = `User message 1: "Wait for EARLIER_702 before release."`
	for _, name := range []string{"session-log", "ooda"} {
		for _, mode := range []string{"unchanged", "growing", "replaced"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				cm := partnerTestManager(emptySummaryReply)
				cm.CheckpointThreshold, cm.SummarizeThreshold = 0.00001, 2
				sls := &SessionLogStrategy{cm: cm, log: mustNewSessionLog(t, filepath.Join(t.TempDir(), "log.jsonl"))}
				var strategy Strategy = sls
				if name == "ooda" {
					strategy = &OODAStrategy{SessionLogStrategy: sls}
				}
				history := []schema.Turn{
					schema.NewTurn(schema.TurnSummary, llm.User(earlier)),
					schema.NewTurn(schema.TurnUserInput, llm.User(partner)),
					schema.NewTurn(schema.TurnAssistant, llm.Assistant("WORK_813")),
					schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_924")),
					schema.NewTurn(schema.TurnAssistant, llm.Assistant("TAIL_035")),
				}
				var entries []string
				for fold := range 4 {
					if fold == 0 || mode == "growing" || (mode == "replaced" && fold == 1) {
						if mode == "replaced" && fold == 1 {
							// A shorter/different log cannot supersede context that
							// exists only in the previous checkpoint.
							sls.log = mustNewSessionLog(t, filepath.Join(t.TempDir(), "replacement.jsonl"))
						}
						token := fmt.Sprintf("LOG_ENTRY_%d_146", fold)
						entries = append(entries, token)
						if err := sls.log.Append(sessionlog.SessionLogEntry{Turn: fold + 1, Action: "assistant", Outcome: "success", Summary: token}); err != nil {
							t.Fatal(err)
						}
					}
					if err := strategy.ManageContext(context.Background(), &history, 0, noopEmit); err != nil {
						t.Fatal(err)
					}
					text := history[0].Message.Text()
					for _, entry := range entries {
						if got := strings.Count(text, entry); got != 1 {
							t.Errorf("fold %d: log source %s occurs %d times, want once", fold, entry, got)
						}
					}
					data := collectCheckpointData(history, 1, "communicate")
					if !reflect.DeepEqual(data.conversation, []checkpointConversationEntry{{Role: "user", Text: partner}}) {
						t.Errorf("fold %d: partner carry changed: %#v", fold, data.conversation)
					}
					if strings.Count(strings.Join(data.earlierSummaries, "\n"), earlier) != 1 {
						t.Errorf("fold %d: independent earlier quotation changed: %#v", fold, data.earlierSummaries)
					}
					history = append(history, schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_257")), schema.NewTurn(schema.TurnAssistant, llm.Assistant("NEXT_368")))
				}
			})
		}
	}
}
