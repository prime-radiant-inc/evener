package agent

import (
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestAskUser_AnswerLinkInlineSuccessiveQuestions(t *testing.T) {
	t.Parallel()
	sess, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	ctx := tripwireCtx(t)
	fifoDir := t.TempDir()
	readyFIFO := filepath.Join(fifoDir, "ready")
	releaseFIFO := filepath.Join(fifoDir, "release")
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skipf("FIFO execution barrier is supported on Darwin and Linux, not %s", runtime.GOOS)
	}
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
	marker, err := readFIFOByte(ctx, readyFIFO)
	if err != nil || marker != 'x' {
		t.Fatalf("execution barrier marker=%q err=%v", marker, err)
	}
	if !sess.HasPendingAsk() {
		t.Fatal("first question was not pending while execution A was active")
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "inline-answer",
		Input:            clientMutationInput("clarify before answer", nil, nil),
	}); err != nil {
		t.Fatal(err)
	}
	if err := writeFIFO(ctx, releaseFIFO, "continue"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-process:
		if err != nil {
			t.Fatalf("first execution: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("first execution did not finish: %v", ctx.Err())
	}
	var questions []string
	for _, turn := range answerLinkRecords(t, sess) {
		if turn.Kind == schema.TurnToolResults {
			for _, part := range turn.Message.Content {
				if part.ToolResult != nil && part.ToolResult.Name == "ask_user" {
					questions = append(questions, turn.TurnID)
				}
			}
		}
	}
	if len(questions) != 2 || questions[0] == "" || questions[0] != questions[1] || !sess.HasPendingAsk() {
		t.Fatalf("successive questions must belong to execution A and leave the second pending: questions=%v pending=%v", questions, sess.HasPendingAsk())
	}
	firstQuestion := questions[0]
	if _, err := sess.ProcessInput(ctx, "answer second question", nil); err != nil {
		t.Fatal(err)
	}

	var completion string
	inlineRecorded := false
	for _, turn := range answerLinkRecords(t, sess) {
		if turn.Kind == schema.TurnSteering && turn.ClientMutationID == "inline-answer" {
			inlineRecorded = true
			if turn.TurnID != firstQuestion {
				t.Fatalf("inline steering was stamped into execution %s, want execution A %s", turn.TurnID, firstQuestion)
			}
			for _, linked := range turn.AnswerToTurnIDs {
				if linked == firstQuestion {
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

// readFIFOByte uses a context-bound helper process so cancellation can interrupt
// a FIFO open even when the execution never reaches its writer.
func readFIFOByte(ctx context.Context, path string) (byte, error) {
	cmd := exec.CommandContext(ctx, "cat", path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	type result struct {
		marker byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		var marker [1]byte
		_, readErr := io.ReadFull(stdout, marker[:])
		waitErr := cmd.Wait()
		if readErr == nil {
			readErr = waitErr
		}
		done <- result{marker: marker[0], err: readErr}
	}()
	select {
	case got := <-done:
		return got.marker, got.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// writeFIFO runs the blocking FIFO writer in a process that the tripwire can
// kill, rather than opening the FIFO in the test goroutine.
func writeFIFO(ctx context.Context, path, value string) error {
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$2" > "$1"`, "fifo-writer", path, value)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
