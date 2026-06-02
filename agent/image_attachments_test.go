package agent

// Tests covering image-attachment round-trip through the session's queue
// surface (kata t5j6 paths 3 and 4). The non-queue user-input path is
// covered by TestSession_ProcessInput_AttachesImageToUserTurn in
// session_test.go; here we focus on Enqueue / DrainAsSteer carrying images
// alongside text, because the queue infrastructure was previously
// text-only.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

var sessionPngSig = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

// TestSession_DrainAsSteer_CarriesImagesIntoSteeringMessage verifies
// that DrainAsSteer with image-bearing queue entries produces a steering
// queue entry that carries both text and image attachments — so the
// subsequent appendTurn(TurnSteering, ...) produces a message with
// ContentImage parts (kata t5j6 path 4).
func TestSession_DrainAsSteer_CarriesImagesIntoSteeringMessage(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgs := []ImageAttachment{{
		MediaType: "image/png",
		Data:      sessionPngSig,
		Name:      "drain.png",
	}}
	if err := sess.EnqueueWithImages(context.Background(), "drain me", imgs); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	if err := sess.DrainAsSteer(context.Background()); err != nil {
		t.Fatalf("DrainAsSteer: %v", err)
	}

	// After drain, the steeringQueue must contain one entry that carries
	// the same image bytes.
	sess.mu.Lock()
	queue := append([]steeringMessage{}, sess.steeringQueue...)
	sess.mu.Unlock()
	if len(queue) != 1 {
		t.Fatalf("steeringQueue: got %d entries, want 1", len(queue))
	}
	entry := queue[0]
	if !strings.Contains(entry.Text, "drain me") {
		t.Errorf("steering text=%q, want to contain %q", entry.Text, "drain me")
	}
	if len(entry.Images) != 1 {
		t.Fatalf("steering images: got %d, want 1", len(entry.Images))
	}
	if entry.Images[0].MediaType != "image/png" || !bytes.Equal(entry.Images[0].Data, sessionPngSig) {
		t.Errorf("steering image mismatch: %+v", entry.Images[0])
	}

	// The steering message converted to an LLM message must include a
	// ContentImage part so the model receives the bytes when the steering
	// is appended as a TurnSteering on the next round.
	msg := steeringMessageToLLM(entry)
	var sawImage bool
	for _, p := range msg.Content {
		if p.Kind == llm.ContentImage && p.Image != nil && p.Image.MediaType == "image/png" && bytes.Equal(p.Image.Data, sessionPngSig) {
			sawImage = true
		}
	}
	if !sawImage {
		t.Errorf("steeringMessageToLLM did not produce ContentImage; parts=%+v", msg.Content)
	}
}

func TestSession_DrainAsSteerWithInput_AppendsAndDrainsAtomically(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{name: "openai"}
	c.Register(f)
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if err := sess.Enqueue(context.Background(), "already queued"); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	sess.mu.Lock()
	sess.state = SessionProcessing
	sess.mu.Unlock()
	imgs := []ImageAttachment{{
		MediaType: "image/png",
		Data:      sessionPngSig,
		Name:      "atomic.png",
	}}
	if err := sess.DrainAsSteerWithInput(context.Background(), "composer payload", imgs); err != nil {
		t.Fatalf("DrainAsSteerWithInput: %v", err)
	}

	if depth := sess.QueueDepth(); depth != 0 {
		t.Fatalf("QueueDepth=%d, want 0", depth)
	}
	queue := sess.SteeringQueueSnapshot()
	if len(queue) != 1 {
		t.Fatalf("steeringQueue: got %d entries, want 1", len(queue))
	}
	if !strings.Contains(queue[0].Text, "already queued") || !strings.Contains(queue[0].Text, "composer payload") {
		t.Fatalf("steering text=%q", queue[0].Text)
	}
	if len(queue[0].Images) != 1 || queue[0].Images[0].Name != "atomic.png" {
		t.Fatalf("steering images=%+v", queue[0].Images)
	}
}

// TestSession_Enqueue_DrainCarriesImagesIntoUserTurn verifies that an
// image enqueued during one turn becomes a ContentImage user-message part
// when the queue is drained as a fresh turn after the active turn finishes
// (kata t5j6 path 3).
func TestSession_Enqueue_DrainCarriesImagesIntoUserTurn(t *testing.T) {
	dir := t.TempDir()
	c := llm.NewClient()
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("first reply") },
			func(req llm.Request) llm.Response { return finalResponse("second reply") },
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	imgs := []ImageAttachment{{
		MediaType: "image/png",
		Data:      sessionPngSig,
		Name:      "queued.png",
	}}
	if err := sess.EnqueueWithImages(context.Background(), "look at this", imgs); err != nil {
		t.Fatalf("EnqueueWithImages: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first input", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// Two user-input turns must appear in history: the original (text-only)
	// and the queued one, in order. The queued turn must carry the image as
	// a ContentImage part.
	sess.mu.Lock()
	history := append([]schema.Turn{}, sess.history...)
	sess.mu.Unlock()
	var userTurns []schema.Turn
	for _, tr := range history {
		if tr.Kind == schema.TurnUserInput {
			userTurns = append(userTurns, tr)
		}
	}
	if len(userTurns) != 2 {
		t.Fatalf("user turns: got %d, want 2 (%v)", len(userTurns), userTurns)
	}
	queuedTurn := userTurns[1]
	var sawText, sawImage bool
	for _, p := range queuedTurn.Message.Content {
		switch p.Kind {
		case llm.ContentText:
			if strings.Contains(p.Text, "look at this") {
				sawText = true
			}
		case llm.ContentImage:
			if p.Image == nil {
				t.Fatal("image content part has nil Image")
			}
			if p.Image.MediaType == "image/png" && bytes.Equal(p.Image.Data, sessionPngSig) {
				sawImage = true
			}
		}
	}
	if !sawText {
		t.Errorf("queued user turn missing text 'look at this'; parts=%+v", queuedTurn.Message.Content)
	}
	if !sawImage {
		t.Errorf("queued user turn missing ContentImage; parts=%+v", queuedTurn.Message.Content)
	}
}
