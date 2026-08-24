package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// The vision side-channel builds its request manually, so its fixed low cap must
// still clamp to the model's supported levels rather than sending an unsupported
// value.
func TestDescribeImage_ClampsEffortToProfileLevels(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("an image of a cat")} },
		},
	}
	c := llm.NewClient()
	c.Register(adapter)

	// Model tops out at "high" (no xhigh/max), but the session requests "max".
	profile := NewOpenAIProfile("m").WithLiveModelInfo(llm.ModelInfo{ReasoningEffortLevels: []string{"low", "medium", "high"}})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()

	desc := sess.describeImage(context.Background(), tool.ExecResult{
		ImageData:      []byte("fake-png-bytes"),
		ImageMediaType: "image/png",
		ImagePurpose:   "what is in this image",
	})
	if desc == "" {
		t.Fatal("describeImage returned empty description")
	}
	requests := adapter.Requests()
	if len(requests) != 1 || requests[0].ReasoningEffort == nil || *requests[0].ReasoningEffort != visionReasoningEffort {
		t.Fatalf("vision request effort = %#v, want %q", requests, visionReasoningEffort)
	}
}

func TestDescribeImage_UsesLowEffortIndependentOfSession(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant("an image of a cat")} },
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	profile := NewOpenAIProfile("m").WithLiveModelInfo(llm.ModelInfo{
		ReasoningEffortLevels: llm.ReasoningEffortVocabulary(),
	})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:        dir,
		ReasoningEffort: "max",
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{ID: "c1", Name: "read_file"}}, []tool.ExecResult{{
		CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 1 {
		t.Fatalf("vision requests = %d, want 1", len(requests))
	}
	if requests[0].ReasoningEffort == nil || *requests[0].ReasoningEffort != visionReasoningEffort {
		t.Fatalf("vision request effort = %v, want %q independent of session max", requests[0].ReasoningEffort, visionReasoningEffort)
	}
	if len(requests[0].Tools) != 0 {
		t.Fatalf("vision request tools = %d, want 0", len(requests[0].Tools))
	}
	if requests[0].AdapterTimeout == nil || requests[0].AdapterTimeout.Request != visionSideChannelTimeout {
		t.Fatalf("vision request timeout = %#v, want request=%s", requests[0].AdapterTimeout, visionSideChannelTimeout)
	}
}

func TestDescribeImage_CancelsTheSideChannelOnTimeout(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := make(chan struct{})
	canceled := make(chan struct{})
	var gotErr error
	blocking := &contextBlockingAdapter{
		name:     "openai",
		started:  started,
		canceled: canceled,
	}
	c := llm.NewClient()
	c.Register(blocking)
	profile := NewOpenAIProfile("m").WithLiveModelInfo(llm.ModelInfo{
		ReasoningEffortLevels: llm.ReasoningEffortVocabulary(),
	})
	sess, err := NewSession(c, profile, execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testConfig{visionSideChannelTimeout: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{ID: "c1", Name: "read_file"}}, []tool.ExecResult{{
			CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png",
		}}); err != nil {
			gotErr = err
		}
	}()
	select {
	case <-started:
	// TRIPWIRE: one second is far above the 20ms test timeout and only reports
	// a broken provider-start signal.
	case <-time.After(time.Second):
		t.Fatal("vision call did not start")
	}
	select {
	case <-canceled:
	// TRIPWIRE: one second is far above the 20ms test timeout and only reports
	// a missing context cancellation signal.
	case <-time.After(time.Second):
		t.Fatal("vision context was not canceled")
	}
	select {
	case <-done:
	// TRIPWIRE: one second is far above the 20ms test timeout and only reports
	// cleanup failing to return after cancellation.
	case <-time.After(time.Second):
		t.Fatal("describeImage did not clean up after cancellation")
	}
	if gotErr != nil {
		t.Fatal(gotErr)
	}
}

type contextBlockingAdapter struct {
	name     string
	started  chan<- struct{}
	canceled chan<- struct{}
}

func (a *contextBlockingAdapter) Name() string { return a.name }

func (a *contextBlockingAdapter) Complete(ctx context.Context, _ llm.Request) (llm.Response, error) {
	close(a.started)
	if _, ok := ctx.Deadline(); !ok {
		return llm.Response{}, errors.New("missing side-channel deadline")
	}
	<-ctx.Done()
	close(a.canceled)
	return llm.Response{}, ctx.Err()
}

func (a *contextBlockingAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("stream not used")
}
