package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

const (
	visionOutputSentinel = "\x00vsc-7f3a\x00"
	toolResultSentinel   = "\x00vtr-91c2\x00"
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
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(visionOutputSentinel)} },
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
	if desc != visionOutputSentinel {
		t.Fatalf("describeImage output sentinel = %q", desc)
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
			func(req llm.Request) llm.Response { return llm.Response{Message: llm.Assistant(visionOutputSentinel)} },
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
				return visionReadFileResponse(filepath.Join(dir, "image.png"))
			},
			func(req llm.Request) llm.Response {
				fakeClock.Advance(125 * time.Millisecond)
				return llm.Response{
					Message: llm.Assistant(visionOutputSentinel),
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
			func(req llm.Request) llm.Response { return finalResponse("done") },
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

	imagePath := filepath.Join(dir, "image.png")
	if err := os.WriteFile(imagePath, validPNGFixture(t), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	// TRIPWIRE: scripted in-process provider and local fixture; only fires on a genuine turn-loop hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "inspect the image", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want primary, vision, and post-tool primary requests", len(requests))
	}
	stats, fields := parseVisionStatsFromModelRequest(t, requests[2])
	if stats.ElapsedMS != 125 || !stats.UsageAvailable {
		t.Fatalf("vision stats = %#v, want elapsed_ms=125 and usage_available=true", stats)
	}
	wantFields := map[string]int{
		"input_tokens":               101,
		"output_tokens":              13,
		"reasoning_tokens":           7,
		"reasoning_tokens_estimated": 8,
		"cache_read_tokens":          9,
		"cache_write_tokens":         10,
		"cache_write_1h_tokens":      11,
	}
	for name, want := range wantFields {
		var got int
		if raw, ok := fields[name]; !ok {
			t.Errorf("model-visible vision stats omitted %q", name)
		} else if err := json.Unmarshal(raw, &got); err != nil || got != want {
			t.Errorf("model-visible vision stat %q = %s, want %d", name, raw, want)
		}
	}
	if !historyHasSteeringKind(sess, events.SteeringKindImageDescription) {
		t.Fatal("vision stats reached the provider without an image-description steering turn")
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
				return visionReadFileResponse(filepath.Join(dir, "image.png"))
			},
			func(req llm.Request) llm.Response {
				fakeClock.Advance(7 * time.Millisecond)
				return llm.Response{Message: llm.Assistant(visionOutputSentinel), Usage: llm.Usage{}}
			},
			func(req llm.Request) llm.Response { return finalResponse("done") },
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

	imagePath := filepath.Join(dir, "image.png")
	if err := os.WriteFile(imagePath, validPNGFixture(t), 0o600); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}
	// TRIPWIRE: scripted in-process provider and local fixture; only fires on a genuine turn-loop hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "inspect the image", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	requests := adapter.Requests()
	if len(requests) != 3 {
		t.Fatalf("provider requests = %d, want primary, vision, and post-tool primary requests", len(requests))
	}
	stats, fields := parseVisionStatsFromModelRequest(t, requests[2])
	if stats.ElapsedMS != 7 || stats.UsageAvailable {
		t.Fatalf("vision stats = %#v, want elapsed_ms=7 and usage_available=false", stats)
	}
	if len(fields) != 2 {
		t.Fatalf("unavailable usage fields = %v, want only elapsed_ms and usage_available", fields)
	}
	if !historyHasSteeringKind(sess, events.SteeringKindImageDescription) {
		t.Fatal("vision stats reached the provider without an image-description steering turn")
	}
}

func visionReadFileResponse(path string) llm.Response {
	return llm.Response{Message: llm.Message{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{
			Kind: llm.ContentToolCall,
			ToolCall: &llm.ToolCallData{
				ID:        "call-image",
				Name:      "read_file",
				Arguments: []byte(`{"file_path":` + string(visionJSONString(path)) + `}`),
			},
		}},
	}}
}

func visionJSONString(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

type observedVisionStats struct {
	ElapsedMS      int64 `json:"elapsed_ms"`
	UsageAvailable bool  `json:"usage_available"`
}

func parseVisionStatsFromModelRequest(t *testing.T, req llm.Request) (observedVisionStats, map[string]json.RawMessage) {
	t.Helper()
	const (
		openTag  = "<evener:vision_side_channel_stats>"
		closeTag = "</evener:vision_side_channel_stats>"
	)
	var modelText string
	for _, message := range req.Messages {
		if text := message.Text(); strings.Contains(text, visionOutputSentinel) {
			modelText = text
			break
		}
	}
	if modelText == "" {
		t.Fatal("post-tool provider request omitted opaque vision description sentinel")
	}
	start := strings.Index(modelText, openTag)
	end := strings.Index(modelText, closeTag)
	if start < 0 || end < start {
		t.Fatal("post-tool provider request omitted machine-readable vision stats block")
	}
	raw := modelText[start+len(openTag) : end]
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		t.Fatalf("model-visible vision stats are not JSON: %v", err)
	}
	for name := range fields {
		switch name {
		case "elapsed_ms", "usage_available", "input_tokens", "output_tokens",
			"reasoning_tokens", "reasoning_tokens_estimated", "cache_read_tokens",
			"cache_write_tokens", "cache_write_1h_tokens":
		default:
			t.Errorf("model-visible vision stats contain unsupported field %q", name)
		}
	}
	var stats observedVisionStats
	if err := json.Unmarshal([]byte(raw), &stats); err != nil {
		t.Fatalf("decode model-visible vision stats: %v", err)
	}
	return stats, fields
}

func historyHasSteeringKind(sess *Session, kind string) bool {
	sess.mu.Lock()
	defer sess.mu.Unlock()
	for _, turn := range sess.history {
		if turn.SteeringKind == kind {
			return true
		}
	}
	return false
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
					Message: llm.Assistant(visionOutputSentinel),
					Usage:   llm.Usage{InputTokens: 101, OutputTokens: 13, ReasoningTokens: &reasoning},
				}, errors.New("vision failed")
			},
		},
	}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
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
		Output:         toolResultSentinel,
		ImageData:      []byte("png"),
		ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	if steered := sess.drainSteering(); len(steered) != 1 || steered[0].Text != "Vision is unavailable for \"/tmp/a.png\" because the vision provider failed. Use OCR or inspect the source data, or continue without vision." {
		t.Fatalf("failed vision call steering = %#v, want fixed sanitized message", steered)
	}
}

func TestVisionFailureSteering_EmptyPathSanitizesProviderError(t *testing.T) {
	const secret = "https://provider.invalid/body secret-request-id"
	got := visionFailureSteering("", visionSideChannelResult{
		outcome: visionSideChannelProviderFailure,
		err:     errors.New(secret),
	})
	want := "Vision is unavailable because the vision provider failed. Use OCR or inspect the source data, or continue without vision."
	if got != want {
		t.Fatalf("steering = %q, want %q", got, want)
	}
	if strings.Contains(got, secret) {
		t.Fatalf("steering leaked provider error: %q", got)
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
	if steered := sess.drainSteering(); len(steered) != 1 || !strings.Contains(steered[0].Text, "Vision is unavailable") {
		t.Fatalf("timed-out vision call steering = %#v, want one unavailable message", steered)
	}
}

func TestDescribeImage_ParentCancellationDoesNotSteerUnavailable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := make(chan struct{})
	canceled := make(chan struct{})
	adapter := &contextBlockingAdapter{name: "openai", started: started, canceled: canceled}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testConfig{visionSideChannelTimeout: time.Second},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	go func() {
		for range sess.Events() {
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- sess.persistToolResults(ctx, []llm.ToolCallData{{ID: "c1", Name: "read_file"}}, []tool.ExecResult{{
			CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png",
		}})
	}()
	select {
	case <-started:
	// TRIPWIRE: one second is far above the expected scripted adapter start signal.
	case <-time.After(time.Second):
		t.Fatal("vision call did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("persistToolResults error = %v, want context.Canceled", err)
	}
	select {
	case <-canceled:
	// TRIPWIRE: one second is far above the expected scripted parent cancellation signal.
	case <-time.After(time.Second):
		t.Fatal("vision call did not observe parent cancellation")
	}
	if steered := sess.drainSteering(); len(steered) != 0 {
		t.Fatalf("parent cancellation produced stale unavailable steering: %#v", steered)
	}
}

func TestPersistToolResults_VisionTimeoutSteersWithPathAndFallback(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	started := make(chan struct{})
	canceled := make(chan struct{})
	adapter := &contextBlockingAdapter{name: "openai", started: started, canceled: canceled}
	c := llm.NewClient()
	c.Register(adapter)
	sess, err := NewSession(c, NewOpenAIProfile("m"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
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

	path := filepath.Join(dir, "image.png")
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{
		ID: "c1", Name: "read_file", Arguments: []byte(`{"file_path":"` + path + `"}`),
	}}, []tool.ExecResult{{
		CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	select {
	case <-started:
	// TRIPWIRE: one second is far above the expected scripted adapter start signal.
	case <-time.After(time.Second):
		t.Fatal("vision call did not start")
	}
	select {
	case <-canceled:
	// TRIPWIRE: one second is far above the expected scripted side deadline signal.
	case <-time.After(time.Second):
		t.Fatal("vision call did not observe its deadline")
	}
	steered := sess.drainSteering()
	if len(steered) != 1 {
		t.Fatalf("steering count = %d, want 1 (%#v)", len(steered), steered)
	}
	for _, want := range []string{"Vision is unavailable", path, "OCR", "source data", "continue without vision"} {
		if !strings.Contains(steered[0].Text, want) {
			t.Errorf("steering = %q, missing %q", steered[0].Text, want)
		}
	}
}

func TestPersistToolResults_VisionFailureCanceledBeforeInjectionRemovesOnlyOwnedSteering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	adapter := &immediateVisionFailureAdapter{name: "openai"}
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
	sess.Steer("daemon survives")
	sess.SteerFromUser("client survives")
	if err := sess.persistToolResults(context.Background(), []llm.ToolCallData{{ID: "c1", Name: "read_file"}}, []tool.ExecResult{{
		CallID: "c1", ToolName: "read_file", ImageData: []byte("png"), ImageMediaType: "image/png",
	}}); err != nil {
		t.Fatalf("persistToolResults: %v", err)
	}
	sess.mu.Lock()
	if len(sess.visionTurnOwners) != 1 || len(sess.steeringQueue) != 3 {
		sess.mu.Unlock()
		t.Fatalf("published queue/owners = %d/%d, want 3/1", len(sess.steeringQueue), len(sess.visionTurnOwners))
	}
	sess.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	hookEntered := make(chan struct{})
	releaseHook := make(chan struct{})
	ctx = context.WithValue(ctx, sessionToolRoundHooksKey{}, sessionToolRoundHooks{beforeSteering: func() {
		close(hookEntered)
		<-releaseHook
	}})
	var sigs []string
	var failed []bool
	done := make(chan error, 1)
	go func() {
		_, err := sess.injectPostToolSteering(ctx, nil, nil, &sigs, &failed)
		done <- err
	}()
	<-hookEntered
	cancel()
	close(releaseHook)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("injectPostToolSteering error = %v, want context.Canceled", err)
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	if len(sess.visionTurnOwners) != 0 {
		t.Fatalf("owners after canceled injection = %d, want 0", len(sess.visionTurnOwners))
	}
	if len(sess.steeringQueue) != 2 || sess.steeringQueue[0].Text != "daemon survives" || sess.steeringQueue[1].Text != "client survives" {
		t.Fatalf("unrelated queue after canceled injection = %#v", sess.steeringQueue)
	}
}

type contextBlockingAdapter struct {
	name     string
	started  chan<- struct{}
	canceled chan<- struct{}
}

type immediateVisionFailureAdapter struct{ name string }

func (a *immediateVisionFailureAdapter) Name() string { return a.name }

func (a *immediateVisionFailureAdapter) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{}, errors.New("provider secret sentinel")
}

func (a *immediateVisionFailureAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, errors.New("stream not used")
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
