package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

var deferredHeadDelegateID = regexp.MustCompile(`dlg_[A-Za-z0-9_-]+`)

// deferredHeadAdapter scripts a root that starts a background delegate, then
// sends it a second message with a positive wait, then ends its turn. The
// child's first generation answers only once released, so the test chooses
// the moment it finishes.
type deferredHeadAdapter struct {
	mu            sync.Mutex
	rootSessionID string
	rootCalls     int
	childCalls    int
	sendResult    string
	nextRequest   string
	delegateID    string

	releaseFirstGeneration chan struct{}
}

func (*deferredHeadAdapter) Name() string { return "openai" }

func (*deferredHeadAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *deferredHeadAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	a.mu.Lock()
	root := req.SessionID == a.rootSessionID
	var call int
	if root {
		a.rootCalls++
		call = a.rootCalls
	} else {
		a.childCalls++
		call = a.childCalls
	}
	a.mu.Unlock()
	var response llm.Response
	switch {
	case !root && call == 1:
		select {
		case <-a.releaseFirstGeneration:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		response = toolCallResponse(communicateCall("child_first", "FIRST_GENERATION_DONE"))
	case !root:
		response = toolCallResponse(communicateCall("child_second", "SECOND_GENERATION_DONE"))
	case call == 1:
		args, _ := json.Marshal(map[string]any{"prompt": "Do the first part."})
		response = toolCallResponse(llm.ToolCallData{ID: "create", Name: "delegate", Arguments: args, Type: "function"})
	case call == 2:
		delegateID := deferredHeadDelegateID.FindString(deferredHeadToolResults(req.Messages))
		if delegateID == "" {
			return llm.Response{}, errors.New("delegate creation result named no delegate")
		}
		a.mu.Lock()
		a.delegateID = delegateID
		a.mu.Unlock()
		args, _ := json.Marshal(map[string]any{"to": delegateID, "message": "Do the second part.", "max_wait_ms": 60_000})
		response = toolCallResponse(llm.ToolCallData{ID: "send", Name: "delegate_send", Arguments: args, Type: "function"})
	default:
		a.mu.Lock()
		if a.sendResult == "" {
			a.sendResult = lastToolResultText(req.Messages)
			a.nextRequest = deferredHeadToolResults(req.Messages) + messagesText(req.Messages)
		}
		a.mu.Unlock()
		response = finalResponse("Root done.")
	}
	response.Provider = a.Name()
	response.Model = req.Model
	return response, nil
}

func deferredHeadToolResults(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		for _, p := range m.Content {
			if p.ToolResult != nil {
				fmt.Fprintln(&b, p.ToolResult.Content)
			}
		}
	}
	return b.String()
}

func lastToolResultText(messages []llm.Message) string {
	for _, message := range slices.Backward(messages) {
		for _, p := range message.Content {
			if p.ToolResult != nil {
				return fmt.Sprint(p.ToolResult.Content)
			}
		}
	}
	return ""
}

// A delegate's earlier generation can finish while its parent is mid-turn,
// after the parent's last delivery flush: the parent holds that result for its
// next round. When the parent's very next tool call is a delegate_send with a
// positive wait to the same delegate, the new generation's result queues behind
// the held one, which waits on the parent's tool round, which waits on the new
// generation's result (#3906). The send answers as soon as the new generation
// finishes, with both results in order, and the held result is consumed by
// that reply rather than delivered again as a notification.
func TestDelegateSendWaitAnswersWhileAnEarlierResultIsDeferred(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	client := llm.NewClient()
	adapter := &deferredHeadAdapter{releaseFirstGeneration: make(chan struct{})}
	client.Register(adapter)

	var sess *Session
	firstDeferred := make(chan struct{})
	var releaseOnce, deferredOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(adapter.releaseFirstGeneration) }) }
	t.Cleanup(release)
	cfg := SessionConfig{StateDir: t.TempDir()}
	cfg.testOnly.delegateDeliveryClassified = func(s *Session, deferred bool) {
		if s == sess && deferred {
			deferredOnce.Do(func() { close(firstDeferred) })
		}
	}
	// The send is past the parent's last flush here: finish the first
	// generation now and let the send go on only once the parent has deferred
	// that result, which is the interleaving CI hit by chance (#3906).
	cfg.testOnly.delegateSendBeforePositiveWaitAdmission = func() {
		release()
		select {
		case <-firstDeferred:
		// TRIPWIRE: a hang guard only; the released child finishes within
		// milliseconds.
		case <-time.After(30 * time.Second):
		}
	}
	var err error
	sess, err = NewSession(client, withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	adapter.mu.Lock()
	adapter.rootSessionID = sess.ID()
	adapter.mu.Unlock()

	// TRIPWIRE: half the send's max_wait_ms. Every step is a scripted in-process
	// answer, so the turn ends within a second unless the send sits out its wait.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "start", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	select {
	case <-firstDeferred:
	default:
		t.Fatal("the first generation's result was not deferred behind the parent's turn; the test did not force the interleaving")
	}
	adapter.mu.Lock()
	sendResult := adapter.sendResult
	adapter.mu.Unlock()
	first, second := strings.Index(sendResult, "FIRST_GENERATION_DONE"), strings.Index(sendResult, "SECOND_GENERATION_DONE")
	if first < 0 || second < first {
		t.Fatalf("delegate_send result = %q, want the held first result and then the second, in order", sendResult)
	}
	adapter.mu.Lock()
	nextRequest, delegateID := adapter.nextRequest, adapter.delegateID
	adapter.mu.Unlock()
	if n := strings.Count(nextRequest, "FIRST_GENERATION_DONE"); n != 1 {
		t.Fatalf("the parent's next request carries the first result %d times, want once (in the send's reply, never again as a notification):\n%s", n, nextRequest)
	}
	if pending := delegateAggregateSnapshot(t, sess.delegateController, delegateID).PendingDeliveries; len(pending) != 0 {
		t.Fatalf("pending deliveries after the reply = %+v, want both acknowledged", pending)
	}
	// The reply's turn commits both deliveries to its one call, and the
	// transcript still folds, as restore needs it to.
	fold, err := readDelegateAttentionFold(transcriptPath(sess.delegateController.stateDir, sess.ID()), sess.ID())
	if err != nil {
		t.Fatalf("fold the root transcript: %v", err)
	}
	if fold.deliveryCommits[delegateDeliveryID(delegateID, 1)] != "send" || fold.deliveryCommits[delegateDeliveryID(delegateID, 2)] != "send" {
		t.Fatalf("delivery commits = %v, want both on the send call", fold.deliveryCommits)
	}
}
