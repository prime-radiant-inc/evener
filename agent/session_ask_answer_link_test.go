package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func answerLinkRecords(t *testing.T, sess *Session) []schema.Turn {
	t.Helper()
	data, err := os.ReadFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	var turns []schema.Turn
	for _, line := range bytes.Split(data, []byte("\n")) {
		var kind struct {
			Kind string `json:"kind"`
		}
		if len(line) == 0 {
			continue
		}
		if err := json.Unmarshal(line, &kind); err != nil {
			t.Fatal(err)
		}
		if kind.Kind != "entry" {
			continue
		}
		entry, err := transcript.DecodeEntry(line)
		if err != nil {
			t.Fatal(err)
		}
		turns = append(turns, entry.Turn)
	}
	return turns
}

func answerLinkIDs(t *testing.T, turn schema.Turn) []string {
	t.Helper()
	raw, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var record struct {
		AnswerToTurnIDs []string `json:"answer_to_turn_ids"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	return record.AnswerToTurnIDs
}

func TestAskUser_AnswerLinkDurableAdmission(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"direct", "queued", "restore_after_failed_append"} {
		t.Run(mode, func(t *testing.T) {
			sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
			ctx := seedDurableAdmissionAsk(t, sess)
			adapter.steps = append(adapter.steps, func(llm.Request) llm.Response { return communicateResponse(true, "answer received") })
			questionID := ""
			for _, turn := range answerLinkRecords(t, sess) {
				if turn.Kind == schema.TurnToolResults {
					questionID = turn.TurnID
				}
			}
			if questionID == "" {
				t.Fatal("missing recorded question execution")
			}
			if mode == "restore_after_failed_append" {
				failure := errors.New("answer append failed before recording")
				fs := attachEnvironmentFailureFS(t, sess)
				fs.mu.Lock()
				fs.writeFailure = failure
				fs.transferBeforeWriteFailure = 0
				fs.mu.Unlock()
				if _, err := sess.ProcessInput(ctx, "failed answer", nil); !errors.Is(err, failure) {
					t.Fatalf("append error=%v", err)
				}
				if !sess.HasPendingAsk() {
					t.Fatal("failed append resolved question")
				}
				for _, turn := range answerLinkRecords(t, sess) {
					if len(answerLinkIDs(t, turn)) != 0 {
						t.Fatal("failed append published answer link")
					}
				}
				meta, dir := sess.Meta(), sess.stateDir
				sess.Close()
				client := llm.NewClient()
				client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return communicateResponse(true, "answer received") }}})
				restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(restored.Close)
				sess = restored
			}
			var claimed queuedInput
			if mode == "queued" {
				params := appwire.TurnStartParams{ClientMutationID: "answer-link", Input: []appwire.InputItem{{Type: "text", Text: "accepted answer"}}}
				if _, err := sess.AcceptClientMutationStart(params); err != nil {
					t.Fatal(err)
				}
				var ok bool
				var err error
				claimed, ok, err = sess.claimClientMutationStart()
				if err != nil || !ok {
					t.Fatalf("claim=%v err=%v", ok, err)
				}
				ctx = withQueuedClientMutation(ctx, claimed)
				if ids := sess.pendingAskTurnIDs(); !reflect.DeepEqual(ids, []string{questionID}) {
					t.Fatalf("claim changed pending question identity: %v", ids)
				}
			}
			if mode == "queued" {
				if err := sess.acceptUserInput(ctx, "accepted answer", nil, nil, false); err != nil {
					t.Fatal(err)
				}
				// The durable incorporated-record guard must adopt the same answer on
				// recovery, not append a second record or rediscover a cleared question.
				if err := sess.acceptUserInput(withQueuedClientMutation(context.Background(), claimed), "accepted answer", nil, nil, false); err != nil {
					t.Fatal(err)
				}
			} else if _, err := sess.ProcessInput(ctx, "accepted answer", nil); err != nil {
				t.Fatal(err)
			}
			if sess.HasPendingAsk() {
				t.Fatal("accepted answer left question pending")
			}
			answers := 0
			for _, turn := range answerLinkRecords(t, sess) {
				if turn.Kind != schema.TurnUserInput || turn.Message.Text() != "accepted answer" {
					continue
				}
				answers++
				if got := answerLinkIDs(t, turn); !reflect.DeepEqual(got, []string{questionID}) {
					t.Fatalf("answer link=%v want pending execution %s", got, questionID)
				}
			}
			if answers != 1 {
				t.Fatalf("accepted answer records=%d want 1", answers)
			}
		})
	}
}

func TestAskUser_AnswerLinkSuccessiveQuestions(t *testing.T) {
	t.Parallel()
	sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	ctx := seedDurableAdmissionAsk(t, sess)
	adapter.steps = append(adapter.steps,
		func(llm.Request) llm.Response { return toolCallResponse(askUserCall("ask2", askUserArgsValid())) },
		func(llm.Request) llm.Response { return communicateResponse(true, "finished") },
		func(llm.Request) llm.Response { return communicateResponse(true, "unrelated finished") },
	)
	for _, input := range []string{"first answer", "second answer", "unrelated input"} {
		if _, err := sess.ProcessInput(ctx, input, nil); err != nil {
			t.Fatal(err)
		}
	}
	previousQuestion := ""
	answers := 0
	for _, turn := range answerLinkRecords(t, sess) {
		if turn.Kind == schema.TurnToolResults {
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.Name == "ask_user" {
					previousQuestion = turn.TurnID
				}
			}
		}
		if turn.Kind != schema.TurnUserInput {
			continue
		}
		switch turn.Message.Text() {
		case "first answer", "second answer":
			answers++
			if previousQuestion == "" || !reflect.DeepEqual(turn.AnswerToTurnIDs, []string{previousQuestion}) || turn.TurnID == previousQuestion {
				t.Fatalf("answer=%s execution=%s link=%v pending=%s", turn.Message.Text(), turn.TurnID, turn.AnswerToTurnIDs, previousQuestion)
			}
		case "unrelated input":
			if len(turn.AnswerToTurnIDs) != 0 {
				t.Fatalf("unrelated work inherited link: %v", turn.AnswerToTurnIDs)
			}
		}
	}
	if answers != 2 {
		t.Fatalf("linked answers=%d", answers)
	}
}

func TestAskUser_AnswerLinkSteeringAdmission(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"inline", "carrier", "carrier_restore_retry", "human_note"} {
		t.Run(mode, func(t *testing.T) {
			sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
			ctx := seedDurableAdmissionAsk(t, sess)
			questionIDs := sess.pendingAskTurnIDs()
			if len(questionIDs) != 1 {
				t.Fatalf("pending question identities=%v", questionIDs)
			}
			adapter.steps = append(adapter.steps, func(llm.Request) llm.Response { return communicateResponse(true, "steering received") })
			if mode == "human_note" {
				if _, err := sess.SetHumanNote("steer-link", "a note, not an answer"); err != nil {
					t.Fatal(err)
				}
			} else if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "steer-link", Input: clientMutationInput("accepted steering answer", nil, nil)}); err != nil {
				t.Fatal(err)
			}
			if mode == "carrier_restore_retry" {
				fs := attachEnvironmentFailureFS(t, sess)
				fs.mu.Lock()
				fs.writeFailure = errors.New("steering append failed")
				fs.transferBeforeWriteFailure = 0
				fs.mu.Unlock()
				if _, _, err := sess.ProcessPendingUserInput(ctx, nil); err == nil {
					t.Fatal("carrier append failure was not reported")
				}
				for _, turn := range answerLinkRecords(t, sess) {
					if len(turn.AnswerToTurnIDs) != 0 {
						t.Fatal("failed carrier published answer evidence")
					}
				}
				meta, dir := sess.Meta(), sess.stateDir
				sess.Close()
				client := llm.NewClient()
				client.Register(&fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{func(llm.Request) llm.Response { return communicateResponse(true, "retry received") }}})
				restored, err := RestoreSessionFromMeta(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(restored.Close)
				sess = restored
			}
			if mode == "inline" || mode == "human_note" {
				if !sess.injectDrainedSteering() {
					t.Fatal("accepted steer was not recorded")
				}
			} else if _, ran, err := sess.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
				t.Fatalf("carrier ran=%v error=%v", ran, err)
			}
			count := 0
			for _, turn := range answerLinkRecords(t, sess) {
				if turn.Kind != schema.TurnSteering || turn.ClientMutationID != "steer-link" {
					continue
				}
				count++
				want := questionIDs
				if mode == "human_note" {
					want = nil
				}
				if !reflect.DeepEqual(turn.AnswerToTurnIDs, want) {
					t.Fatalf("saved steering links=%v want=%v", turn.AnswerToTurnIDs, want)
				}
			}
			if count != 1 {
				t.Fatalf("saved steering records=%d want 1", count)
			}
			if mode == "human_note" && !sess.HasPendingAsk() {
				t.Fatal("human note answered pending question")
			}
		})
	}
}

func TestAskUser_AnswerLinkJournalCopyIsolation(t *testing.T) {
	t.Parallel()
	sess := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, sess)
	want := sess.pendingAskTurnIDs()
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "copy-link", Input: clientMutationInput("answer", nil, nil)}); err != nil {
		t.Fatal(err)
	}
	claim, err := sess.claimSteeringCarrierInput()
	if err != nil || !claim.SteeringCarrier {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	copy := sess.clientMutations.snapshot()
	if !reflect.DeepEqual(copy.Journal["copy-link"].AnswerToTurnIDs, want) || len(want) == 0 {
		t.Fatalf("claim lost question IDs: %+v", copy.Journal["copy-link"])
	}
	copy.Journal["copy-link"].AnswerToTurnIDs[0] = "unrelated"
	if got := sess.clientMutations.snapshot().Journal["copy-link"].AnswerToTurnIDs; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot mutation changed committed journal: got=%v want=%v", got, want)
	}
}
