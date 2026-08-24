package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
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

func TestPersistToolResults_VisionSteeringIncludesLatencyAndUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fakeClock := agenttest.NewFakeClock()
	reasoning := 7
	reasoningEstimated := 8
	cacheRead := 9
	cacheWrite := 10
	cacheWrite1h := 11
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				fakeClock.Advance(125 * time.Millisecond)
				return llm.Response{
					Message: llm.Assistant("a red square"),
					Usage: llm.Usage{
						InputTokens:              101,
						OutputTokens:             13,
						TotalTokens:              123,
						ReasoningTokens:          &reasoning,
						ReasoningTokensEstimated: &reasoningEstimated,
						CacheReadTokens:          &cacheRead,
						CacheWriteTokens:         &cacheWrite,
						CacheWrite1hTokens:       &cacheWrite1h,
					},
				}
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		clock:    fakeClock,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	calls := []llm.ToolCallData{{
		ID:        "c1",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"/tmp/a.png"}`),
	}}
	results := []tool.ExecResult{{
		CallID:         "c1",
		ToolName:       "read_file",
		Output:         "raw image result",
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	}}
	if err := sess.persistToolResults(context.Background(), calls, results); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	steered := sess.drainSteering()
	if len(steered) != 1 {
		t.Fatalf("steering messages = %d, want 1", len(steered))
	}
	for _, want := range []string{
		"a red square",
		"elapsed=125ms",
		"input_tokens=101",
		"output_tokens=13",
		"reasoning_tokens=7",
		"reasoning_tokens_estimated=8",
		"cache_read_tokens=9",
		"cache_write_tokens=10",
		"cache_write_1h_tokens=11",
	} {
		if !strings.Contains(steered[0].Text, want) {
			t.Errorf("vision steering missing %q: %s", want, steered[0].Text)
		}
	}
	if strings.Contains(strings.ToLower(steered[0].Text), "cost") || strings.Contains(steered[0].Text, "$") {
		t.Fatalf("vision steering invented a cost signal: %q", steered[0].Text)
	}

	// The model-visible metrics must not replace or rewrite the existing raw
	// tool-result contract persisted before the side-channel steering.
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.history) == 0 || len(sess.history[len(sess.history)-1].Message.Content) != 1 {
		t.Fatalf("tool-result history = %#v", sess.history)
	}
	toolResult := sess.history[len(sess.history)-1].Message.Content[0].ToolResult
	if toolResult == nil || toolResult.Content != "raw image result" || string(toolResult.ImageData) != "png" || toolResult.ImageMediaType != "image/png" {
		t.Fatalf("persisted tool result = %#v", toolResult)
	}
}

func TestPersistToolResults_VisionSteeringOmitsAbsentUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	fakeClock := agenttest.NewFakeClock()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				fakeClock.Advance(7 * time.Millisecond)
				return llm.Response{Message: llm.Assistant("a blank page"), Usage: llm.Usage{}}
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		clock:    fakeClock,
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{
		ID:        "c1",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"/tmp/a.png"}`),
	}}, []tool.ExecResult{{
		CallID:         "c1",
		ToolName:       "read_file",
		Output:         "raw image result",
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}

	steered := sess.drainSteering()
	if len(steered) != 1 {
		t.Fatalf("steering messages = %d, want 1", len(steered))
	}
	if !strings.Contains(steered[0].Text, "a blank page") || !strings.Contains(steered[0].Text, "elapsed=7ms") {
		t.Fatalf("vision steering = %q", steered[0].Text)
	}
	if !strings.Contains(steered[0].Text, "usage=unavailable") {
		t.Fatalf("vision steering missing unavailable usage marker: %q", steered[0].Text)
	}
	for _, field := range []string{"input_tokens=", "output_tokens=", "reasoning_tokens=", "cache_read_tokens=", "cache_write_tokens="} {
		if strings.Contains(steered[0].Text, field) {
			t.Errorf("vision steering invented absent %s: %q", field, steered[0].Text)
		}
	}
}

func TestPersistToolResults_VisionErrorDoesNotClaimSuccessfulUsage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &fakeErrAdapter{
		name: "openai",
		steps: []func(req llm.Request) (llm.Response, error){
			func(req llm.Request) (llm.Response, error) {
				reasoning := 7
				return llm.Response{
					Message: llm.Assistant("should not be surfaced"),
					Usage:   llm.Usage{InputTokens: 101, OutputTokens: 13, ReasoningTokens: &reasoning},
				}, errors.New("vision failed")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()

	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{
		ID:        "c1",
		Name:      "read_file",
		Arguments: []byte(`{"file_path":"/tmp/a.png"}`),
	}}, []tool.ExecResult{{
		CallID:         "c1",
		ToolName:       "read_file",
		Output:         "raw image result",
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	if steered := sess.drainSteering(); len(steered) != 0 {
		t.Fatalf("failed vision call produced model steering: %#v", steered)
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
	if steered := sess.drainSteering(); len(steered) != 0 {
		t.Fatalf("timed-out vision call produced model steering: %#v", steered)
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
