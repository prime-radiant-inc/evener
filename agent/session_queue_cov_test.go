package agent

import (
	"context"
	"testing"
)

// TestWithQueuedClientMutationAndExtraction covers withQueuedClientMutation
// and queuedClientMutationFromContext (session_queue.go:29-40).
func TestWithQueuedClientMutationAndExtraction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	// No queued mutation in bare context.
	if got := queuedClientMutationFromContext(ctx); got.ClientMutationID != "" || got.StableTurnID != "" || got.QueueEntryID != "" {
		t.Fatalf("bare context: %+v, want zero", got)
	}
	// With a queued mutation.
	queued := queuedInput{
		ClientMutationID: "mut_1",
		StableTurnID:     "turn_1",
		ID:               "queue_1",
	}
	ctx = withQueuedClientMutation(ctx, queued)
	got := queuedClientMutationFromContext(ctx)
	if got.ClientMutationID != "mut_1" || got.StableTurnID != "turn_1" || got.QueueEntryID != "queue_1" {
		t.Fatalf("extracted: %+v, want mut_1/turn_1/queue_1", got)
	}
}

// TestWithQueuedInputDrainOnInterrupt covers WithQueuedInputDrainOnInterrupt
// and WithQueuedInputDrainOnInterruptHandler (session_queue.go:49-72).
func TestWithQueuedInputDrainOnInterrupt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rootCtx := context.Background()
	// Basic drain context.
	drained := WithQueuedInputDrainOnInterrupt(ctx, rootCtx)
	cfg, ok := drained.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok {
		t.Fatal("drain config not found in context")
	}
	if cfg.rootCtx == nil {
		t.Fatal("rootCtx should not be nil")
	}
	// With nil rootCtx -> defaults to context.Background().
	drained = WithQueuedInputDrainOnInterrupt(ctx, nil)
	cfg, ok = drained.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if !ok {
		t.Fatal("drain config not found")
	}
	if cfg.rootCtx == nil {
		t.Fatal("nil rootCtx should default to Background")
	}
	// With a custom nextCtx handler.
	called := false
	nextCtx := func(ctx context.Context) (context.Context, context.CancelFunc) {
		called = true
		return ctx, func() {}
	}
	drained = WithQueuedInputDrainOnInterruptHandler(ctx, rootCtx, nextCtx)
	cfg, _ = drained.Value(queuedInputDrainContextKey{}).(queuedInputDrainConfig)
	if cfg.nextCtx == nil {
		t.Fatal("nextCtx handler should be stored")
	}
	// Call the handler to verify it's the one we passed.
	_, cancel := cfg.nextCtx(ctx)
	defer cancel()
	if !called {
		t.Fatal("nextCtx handler should have been called")
	}
}

// TestSteeringInjectedDataFromMessage covers steeringInjectedDataFromMessage
// (session_queue.go:98-107).
func TestSteeringInjectedDataFromMessage(t *testing.T) {
	t.Parallel()
	msg := steeringMessage{
		Text:             "steer message",
		ClientMutationID: "mut_1",
		StableTurnID:     "turn_1",
		Source:           "user",
		Kind:             "agent_message",
	}
	data := steeringInjectedDataFromMessage(msg)
	if data.Text != "steer message" || data.ClientMutationID != "mut_1" ||
		data.StableTurnID != "turn_1" || data.Source != "user" || data.Kind != "agent_message" {
		t.Fatalf("data = %+v", data)
	}
	// With images.
	msg.Images = []ImageAttachment{{
		MediaType: "image/png",
		Data:      []byte("img"),
		Name:      "test.png",
	}}
	data = steeringInjectedDataFromMessage(msg)
	if len(data.Images) != 1 || data.Images[0].MediaType != "image/png" {
		t.Fatalf("images = %+v", data.Images)
	}
}
