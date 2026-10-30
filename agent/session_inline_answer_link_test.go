package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestAskUser_AnswerLinkInlineSuccessiveQuestions(t *testing.T) {
	t.Parallel()
	sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	fifoDir := t.TempDir()
	readyFIFO := filepath.Join(fifoDir, "ready")
	releaseFIFO := filepath.Join(fifoDir, "release")
	if output, err := exec.Command("mkfifo", readyFIFO, releaseFIFO).CombinedOutput(); err != nil {
		t.Fatalf("create execution barrier FIFOs: %v: %s", err, output)
	}
	shellArgs, err := json.Marshal(map[string]any{
		"command": "printf x > '" + readyFIFO + "'; cat '" + releaseFIFO + "' >/dev/null",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.steps[0] = func(llm.Request) llm.Response {
		return agenttest.ToolCallResponse(
			askUserCall("ask1", askUserArgsValid()),
			llm.ToolCallData{ID: "barrier", Name: "shell", Arguments: shellArgs},
		)
	}
	adapter.steps = append(adapter.steps,
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(askUserCall("ask2", askUserArgsValid()))
		},
		func(llm.Request) llm.Response { return agenttest.CommunicateResponse(true, "completed") },
	)
	process := make(chan error, 1)
	go func() {
		_, err := sess.ProcessInput(ctx, "which db should we use?", nil)
		process <- err
	}()
	ready, err := os.Open(readyFIFO)
	if err != nil {
		t.Fatal(err)
	}
	var marker [1]byte
	if _, err := ready.Read(marker[:]); err != nil || marker[0] != 'x' {
		ready.Close()
		t.Fatalf("execution barrier marker=%q err=%v", marker, err)
	}
	if !sess.HasPendingAsk() {
		ready.Close()
		t.Fatal("first question was not pending while execution A was active")
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "inline-answer",
		Input:            clientMutationInput("clarify before answer", nil, nil),
	}); err != nil {
		ready.Close()
		t.Fatal(err)
	}
	release, err := os.OpenFile(releaseFIFO, os.O_WRONLY, 0)
	if err != nil {
		ready.Close()
		t.Fatal(err)
	}
	if _, err := release.Write([]byte("continue")); err != nil {
		release.Close()
		ready.Close()
		t.Fatal(err)
	}
	if err := release.Close(); err != nil {
		ready.Close()
		t.Fatal(err)
	}
	ready.Close()
	if err := <-process; err != nil {
		t.Fatalf("first execution: %v", err)
	}
	firstQuestion := ""
	for _, turn := range answerLinkRecords(t, sess) {
		if turn.Kind == schema.TurnToolResults {
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.Name == "ask_user" {
					firstQuestion = turn.TurnID
					break
				}
			}
		}
	}
	if firstQuestion == "" || !sess.HasPendingAsk() {
		t.Fatalf("second question was not left pending: execution=%s pending=%v", firstQuestion, sess.HasPendingAsk())
	}
	if _, err := sess.ProcessInput(ctx, "answer second question", nil); err != nil {
		t.Fatal(err)
	}

	var completion string
	inlineRecorded := false
	for _, turn := range answerLinkRecords(t, sess) {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID == "inline-answer" {
			inlineRecorded = true
			for _, linked := range turn.AnswerToTurnIDs {
				if turn.TurnID == firstQuestion && linked == firstQuestion {
					t.Fatalf("inline answer retained a self-link: execution=%s links=%v", turn.TurnID, turn.AnswerToTurnIDs)
				}
			}
		}
		if turn.Communicate != nil && turn.Communicate.EndTurn && turn.Communicate.EndReason == "done" {
			completion = turn.TurnID
		}
	}
	if !inlineRecorded {
		t.Fatal("accepted inline answer was not recorded")
	}
	if completion == "" || completion == firstQuestion {
		t.Fatalf("second answer did not complete a later execution: completion=%s question=%s", completion, firstQuestion)
	}

	index, err := transcriptindex.Open(sess.TranscriptPath(), filepath.Join(t.TempDir(), "index"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	matches, _, err := index.AnswerContinuation(ctx, firstQuestion, completion, nil)
	if err != nil {
		t.Fatalf("continuation lookup for completion %s from question %s: %v", completion, firstQuestion, err)
	}
	if !matches {
		t.Fatalf("completion %s did not continue question %s", completion, firstQuestion)
	}
}
