package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestConfigurationErrorSatisfiesErrorContract covers ConfigurationError's Error
// interface accessors, which document fixed no-attribution values.
func TestConfigurationErrorSatisfiesErrorContract(t *testing.T) {
	cause := errors.New("root cause")
	var e Error = &ConfigurationError{Message: "  bad setup  ", Cause: cause}

	if got := e.Error(); got != "configuration error: bad setup" {
		t.Fatalf("Error() = %q", got)
	}
	if e.Provider() != "" {
		t.Fatalf("Provider() = %q, want empty", e.Provider())
	}
	if e.BehaviorTag() != "" {
		t.Fatalf("BehaviorTag() = %q, want empty", e.BehaviorTag())
	}
	if e.StatusCode() != 0 {
		t.Fatalf("StatusCode() = %d, want 0", e.StatusCode())
	}
	if e.ErrorCode() != "" {
		t.Fatalf("ErrorCode() = %q, want empty", e.ErrorCode())
	}
	if e.Retryable() {
		t.Fatal("Retryable() = true, want false")
	}
	if e.RetryAfter() != nil {
		t.Fatal("RetryAfter() != nil, want nil")
	}
	if e.Raw() != nil {
		t.Fatal("Raw() != nil, want nil")
	}
	if !errors.Is(e, cause) {
		t.Fatal("Unwrap() did not expose the cause to errors.Is")
	}
}

// TestErrorFromHTTPStatusWithRawBodiesAttachesBodies covers the raw-body
// attachment branch (only taken when raw logging is enabled) and the
// RawHTTPBodies accessor on the resulting error.
func TestErrorFromHTTPStatusWithRawBodiesAttachesBodies(t *testing.T) {
	orig := rawBodyEnabled
	rawBodyEnabled = true
	t.Cleanup(func() { rawBodyEnabled = orig })

	err := ErrorFromHTTPStatusWithRawBodies("openai", 500, "boom", nil, nil, "REQ", "RESP")
	rhe, ok := err.(RawHTTPBodyError)
	if !ok {
		t.Fatalf("error %T does not implement RawHTTPBodyError", err)
	}
	req, resp := rhe.RawHTTPBodies()
	if req != "REQ" || resp != "RESP" {
		t.Fatalf("RawHTTPBodies() = (%q, %q), want (REQ, RESP)", req, resp)
	}
}

// TestRewriteErrorProviderLeavesEmptyProviderUnstamped covers the guard that
// refuses to attribute a provider to an error that intentionally has none.
func TestRewriteErrorProviderLeavesEmptyProviderUnstamped(t *testing.T) {
	err := ErrorFromHTTPStatus("", 500, "boom", nil, nil)
	got := RewriteErrorProvider(err, "ollama")
	var le Error
	if !errors.As(got, &le) {
		t.Fatalf("error %T is not an llm.Error", got)
	}
	if le.Provider() != "" {
		t.Fatalf("Provider() = %q, want empty (guard should have left it alone)", le.Provider())
	}
}

// TestClassifyByMessageAuthenticationSignals covers the unauthorized/invalid-key
// message-classification arm of a 400 response.
func TestClassifyByMessageAuthenticationSignals(t *testing.T) {
	err := ErrorFromHTTPStatus("openai", 400, "invalid key supplied", nil, nil)
	if Kind(err) != KindAuthentication {
		t.Fatalf("Kind(invalid key) = %v, want authentication", Kind(err))
	}
}

// TestMessageConstructors covers the Developer and ToolResult constructors.
func TestMessageConstructors(t *testing.T) {
	dev := Developer("guidance")
	if dev.Role != RoleDeveloper || dev.Text() != "guidance" {
		t.Fatalf("Developer() = %+v", dev)
	}

	tr := ToolResult("call-1", "output", true)
	if tr.Role != RoleTool || tr.ToolCallID != "call-1" {
		t.Fatalf("ToolResult() role/id = %+v", tr)
	}
	if len(tr.Content) != 1 || tr.Content[0].ToolResult == nil {
		t.Fatalf("ToolResult() content = %+v", tr.Content)
	}
	if !tr.Content[0].ToolResult.IsError || tr.Content[0].ToolResult.Content != "output" {
		t.Fatalf("ToolResult() data = %+v", tr.Content[0].ToolResult)
	}
}

// TestIntFromAny covers all supported JSON-decoded numeric shapes plus the
// unsupported-type default.
func TestIntFromAny(t *testing.T) {
	tests := []struct {
		name string
		in   any
		want int
	}{
		{"json.Number", json.Number("42"), 42},
		{"float64", float64(7), 7},
		{"int", 9, 9},
		{"unsupported", "nope", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IntFromAny(tt.in); got != tt.want {
				t.Fatalf("IntFromAny(%v) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

// TestReasoningBudget covers every recognized effort level and the default.
func TestReasoningBudget(t *testing.T) {
	tests := map[string]int{
		"minimal": 512, "low": 1024, "medium": 8192, "high": 32768,
		"xhigh": 131072, "max": 131072, "unknown": 0, "": 0,
	}
	for effort, want := range tests {
		if got := ReasoningBudget(effort); got != want {
			t.Fatalf("ReasoningBudget(%q) = %d, want %d", effort, got, want)
		}
	}
}

// TestReasoningTextJoinsBlocksWithBlankLine covers the blank-line separator arm
// between multiple thinking blocks.
func TestReasoningTextJoinsBlocksWithBlankLine(t *testing.T) {
	r := Response{Message: Message{Content: []ContentPart{
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: "first"}},
		{Kind: ContentThinking, Thinking: &ThinkingData{Text: "second"}},
	}}}
	if got := r.ReasoningText(); got != "first\n\nsecond" {
		t.Fatalf("ReasoningText() = %q, want joined blocks", got)
	}
}

// TestDefaultSleepTimerAndCancel covers the timer-elapsed and context-cancelled
// arms of DefaultSleep.
func TestDefaultSleepTimerAndCancel(t *testing.T) {
	if err := DefaultSleep(context.Background(), time.Millisecond); err != nil {
		t.Fatalf("DefaultSleep(1ms) = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := DefaultSleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("DefaultSleep(cancelled) = %v, want context.Canceled", err)
	}
}

// TestRetryableError covers the nil-error and bare-sentinel guards plus a
// genuinely retryable typed error.
func TestRetryableError(t *testing.T) {
	if retryableError(nil) {
		t.Fatal("retryableError(nil) = true, want false")
	}
	if retryableError(context.Canceled) {
		t.Fatal("retryableError(context.Canceled) = true, want false")
	}
	if !retryableError(ErrorFromHTTPStatus("openai", 429, "slow down", nil, nil)) {
		t.Fatal("retryableError(429 rate limit) = false, want true")
	}
}

// TestSaturatingDelay covers the zero-base, negative-value, and overflow/NaN arms.
func TestSaturatingDelay(t *testing.T) {
	if got := saturatingDelay(0, 100); got != 0 {
		t.Fatalf("saturatingDelay(0, 100) = %v, want 0", got)
	}
	if got := saturatingDelay(time.Second, -5); got != 0 {
		t.Fatalf("saturatingDelay(1s, -5) = %v, want 0", got)
	}
	if got := saturatingDelay(time.Second, math.NaN()); got != time.Duration(math.MaxInt64) {
		t.Fatalf("saturatingDelay(1s, NaN) = %v, want MaxInt64", got)
	}
}

// TestParseAndValidateArgs covers the malformed-JSON, nil-schema, and
// schema-validation-failure arms of parseAndValidateArgs.
func TestParseAndValidateArgs(t *testing.T) {
	if _, err := parseAndValidateArgs(nil, json.RawMessage("{not json")); err == nil {
		t.Fatal("parseAndValidateArgs(bad json) error = nil, want unmarshal failure")
	}

	v, err := parseAndValidateArgs(nil, json.RawMessage(`{"a":1}`))
	if err != nil {
		t.Fatalf("parseAndValidateArgs(nil schema) error = %v", err)
	}
	if m, ok := v.(map[string]any); !ok || m["a"] != float64(1) {
		t.Fatalf("parseAndValidateArgs(nil schema) = %v", v)
	}

	schema, err := compileSchema(map[string]any{
		"type":     "object",
		"required": []any{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
	})
	if err != nil {
		t.Fatalf("compileSchema() error = %v", err)
	}
	if _, err := parseAndValidateArgs(schema, json.RawMessage(`{}`)); err == nil {
		t.Fatal("parseAndValidateArgs(missing required) error = nil, want validation failure")
	}
}

// TestCompileSchemaRejectsUnmarshalableParams covers the json.Marshal error arm
// of compileSchema.
func TestCompileSchemaRejectsUnmarshalableParams(t *testing.T) {
	if _, err := compileSchema(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("compileSchema(channel) error = nil, want marshal failure")
	}
}

// TestPrepareToolsRejectsInvalidToolName covers prepareTools' name-validation
// error arm.
func TestPrepareToolsRejectsInvalidToolName(t *testing.T) {
	_, _, err := prepareTools([]Tool{{Definition: ToolDefinition{Name: ""}}})
	if err == nil {
		t.Fatal("prepareTools(empty name) error = nil, want validation failure")
	}
}

// TestGenerateOptionsValidate covers both arms of GenerateOptions.validate.
func TestGenerateOptionsValidate(t *testing.T) {
	if err := (GenerateOptions{}).validate(); err == nil {
		t.Fatal("validate(no model) error = nil, want configuration error")
	}
	if err := (GenerateOptions{Model: "gpt"}).validate(); err != nil {
		t.Fatalf("validate(model set) error = %v, want nil", err)
	}
}

// TestPrepareGenerationInputValidation covers prepareGeneration's request-shape
// guards using an explicit client so the DefaultClient path is skipped.
func TestPrepareGenerationInputValidation(t *testing.T) {
	c := NewClient()
	prompt := "hi"

	if _, err := prepareGeneration(GenerateOptions{Client: c}); err == nil {
		t.Fatal("prepareGeneration(no model) error = nil, want validation failure")
	}

	both := GenerateOptions{Client: c, Model: "m", Prompt: &prompt, Messages: []Message{User("x")}}
	if _, err := prepareGeneration(both); err == nil {
		t.Fatal("prepareGeneration(prompt+messages) error = nil, want mutual-exclusion failure")
	}

	if _, err := prepareGeneration(GenerateOptions{Client: c, Model: "m"}); err == nil {
		t.Fatal("prepareGeneration(no prompt/messages) error = nil, want required failure")
	}

	badTool := GenerateOptions{Client: c, Model: "m", Prompt: &prompt, Tools: []Tool{{Definition: ToolDefinition{Name: ""}}}}
	if _, err := prepareGeneration(badTool); err == nil {
		t.Fatal("prepareGeneration(bad tool) error = nil, want tool-preparation failure")
	}

	sys := "be terse"
	if _, err := prepareGeneration(GenerateOptions{Client: c, Model: "m", System: &sys, Prompt: &prompt}); err != nil {
		t.Fatalf("prepareGeneration(system+prompt) error = %v, want success", err)
	}
}

// TestWithAPILogAttemptContextInheritsUnsetFields covers the inheritance arms
// that pull SessionID, Round, AttemptGroupID, and AttemptRecorder from an
// existing API-log context when the incoming meta leaves them unset.
func TestWithAPILogAttemptContextInheritsUnsetFields(t *testing.T) {
	recorderCalls := 0
	base := APILogContext{
		SessionID:      "sess-1",
		Round:          3,
		AttemptGroupID: "grp-1",
		AttemptRecorder: func(_ context.Context, r AdapterAttemptRecord) AdapterAttemptRecord {
			recorderCalls++
			return r
		},
	}
	ctx := context.WithValue(context.Background(), apiLogKey{}, base)

	ctx = WithAPILogAttemptContext(ctx, APILogContext{AttemptIndex: 2})
	got, ok := getAPILogContext(ctx)
	if !ok {
		t.Fatal("getAPILogContext returned ok=false")
	}
	if got.SessionID != "sess-1" || got.Round != 3 || got.AttemptGroupID != "grp-1" {
		t.Fatalf("inherited fields = %+v, want sess-1/3/grp-1", got)
	}
	if got.AttemptRecorder == nil {
		t.Fatal("AttemptRecorder was not inherited")
	}
	got.AttemptRecorder(context.Background(), AdapterAttemptRecord{})
	if recorderCalls != 1 {
		t.Fatalf("inherited recorder invoked %d times, want 1", recorderCalls)
	}
}

// TestStreamAccumulatorToolCallDeltaWithoutStart covers the arms where a
// ToolCallDelta and ToolCallEnd arrive for tool-call IDs that were never
// opened with a ToolCallStart, so the accumulator must create the record and
// fill its name/item/type/arguments lazily. Also exercises the empty-TextID
// default path.
func TestStreamAccumulatorToolCallDeltaWithoutStart(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventTextStart}) // empty TextID -> "text_0"
	acc.Process(StreamEvent{Type: StreamEventTextDelta, Delta: "hello"})

	acc.Process(StreamEvent{Type: StreamEventToolCallDelta, ToolCall: &ToolCallData{
		ID: "call-a", ItemID: "item-a", Name: "search", Type: "function", Arguments: []byte(`{"q":`),
	}})
	acc.Process(StreamEvent{Type: StreamEventToolCallEnd, ToolCall: &ToolCallData{
		ID: "call-b", ItemID: "item-b", Name: "fetch", Type: "function", Arguments: []byte(`{}`),
	}})
	acc.Process(StreamEvent{Type: StreamEventFinish})

	resp := acc.Response()
	if resp == nil {
		t.Fatal("Response() = nil after finish")
	}
	calls := resp.ToolCalls()
	if len(calls) != 2 {
		t.Fatalf("ToolCalls() = %d, want 2", len(calls))
	}
	if calls[0].Name != "search" || calls[1].Name != "fetch" {
		t.Fatalf("tool call names = %q, %q", calls[0].Name, calls[1].Name)
	}
	if acc.PartialResponse() == nil {
		t.Fatal("PartialResponse() = nil, want accumulated partial")
	}
}

// TestStreamAccumulatorFinishRebuildsEmptyResponse covers the FINISH arm where
// the provider Response carries no content, so the accumulator rebuilds it from
// accumulated deltas and copies the provider metadata across.
func TestStreamAccumulatorFinishRebuildsEmptyResponse(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t0", Delta: "body"})
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &Response{
		Model:     "gpt-x",
		Provider:  "openai",
		Warnings:  []Warning{{Message: "deprecated", Code: "warn"}},
		RateLimit: &RateLimitInfo{},
	}})

	resp := acc.Response()
	if resp == nil {
		t.Fatal("Response() = nil after finish")
	}
	if resp.Model != "gpt-x" || resp.Provider != "openai" {
		t.Fatalf("metadata not copied: %+v", resp)
	}
	if len(resp.Warnings) != 1 || resp.RateLimit == nil {
		t.Fatalf("warnings/ratelimit not copied: %+v", resp)
	}
	if resp.Text() != "body" {
		t.Fatalf("rebuilt text = %q, want body", resp.Text())
	}
}

// TestCopyResponseMetadataNilSource covers the nil-source guard.
func TestCopyResponseMetadataNilSource(t *testing.T) {
	dst := &Response{ID: "keep"}
	copyResponseMetadata(dst, nil)
	if dst.ID != "keep" {
		t.Fatalf("copyResponseMetadata(nil) mutated dst: %+v", dst)
	}
}

// TestStreamAccumulatorNilReceiver covers the nil-receiver guards.
func TestStreamAccumulatorNilReceiver(t *testing.T) {
	var acc *StreamAccumulator
	acc.Process(StreamEvent{Type: StreamEventTextDelta, Delta: "x"}) // must be a no-op
	if acc.Response() != nil {
		t.Fatal("(*StreamAccumulator)(nil).Response() != nil")
	}
	if acc.PartialResponse() != nil {
		t.Fatal("(*StreamAccumulator)(nil).PartialResponse() != nil")
	}
	if acc.buildResponse() != nil {
		t.Fatal("(*StreamAccumulator)(nil).buildResponse() != nil")
	}
}

// TestModelCatalogLookups covers the nil-receiver guards, the duplicate-ID
// skip in buildIndex, and the file-read error arm of the LiteLLM loader.
func TestModelCatalogLookups(t *testing.T) {
	var nilCatalog *ModelCatalog
	if nilCatalog.GetModelInfo("x") != nil {
		t.Fatal("nil ModelCatalog GetModelInfo != nil")
	}
	if nilCatalog.LookupModelInfo("x") != nil {
		t.Fatal("nil ModelCatalog LookupModelInfo != nil")
	}

	cat := &ModelCatalog{Models: []ModelInfo{
		{ID: "dup", ContextWindow: 100},
		{ID: "dup", ContextWindow: 999}, // duplicate ID: must be ignored
	}}
	mi := cat.GetModelInfo("dup")
	if mi == nil || mi.ContextWindow != 100 {
		t.Fatalf("duplicate-ID entry not resolved to first: %+v", mi)
	}

	if _, err := LoadModelCatalogFromLiteLLMJSON(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("LoadModelCatalogFromLiteLLMJSON(missing) error = nil, want read failure")
	}
}

// TestEstimateInputTokensCountsResponseFormat covers the response-format
// accounting arm of EstimateInputTokens.
func TestEstimateInputTokensCountsResponseFormat(t *testing.T) {
	rf := &ResponseFormat{Type: "json_object"}
	withRF := EstimateInputTokens(Request{Model: "m", Messages: []Message{User("hi")}, ResponseFormat: rf})
	without := EstimateInputTokens(Request{Model: "m", Messages: []Message{User("hi")}})
	if withRF.Tokens <= without.Tokens {
		t.Fatalf("response format not counted: with=%d without=%d", withRF.Tokens, without.Tokens)
	}
}

// TestCountInputTokensConfigErrors covers the validation, missing-provider, and
// unknown-provider guards of Client.CountInputTokens.
func TestCountInputTokensConfigErrors(t *testing.T) {
	c := NewClient()
	ctx := context.Background()

	if _, err := c.CountInputTokens(ctx, Request{Messages: []Message{User("hi")}}); err == nil {
		t.Fatal("CountInputTokens(no model) error = nil, want validation failure")
	}
	if _, err := c.CountInputTokens(ctx, Request{Model: "m", Messages: []Message{User("hi")}}); err == nil {
		t.Fatal("CountInputTokens(no provider) error = nil, want configuration failure")
	}
	_, err := c.CountInputTokens(ctx, Request{Model: "m", Provider: "ghost", Messages: []Message{User("hi")}})
	if err == nil {
		t.Fatal("CountInputTokens(unknown provider) error = nil, want configuration failure")
	}
}

// TestLoadOrCreateContinuationSecretOpenFileError covers the create-secret-file
// error arm: the continuation directory exists but is read-only, so the
// exclusive create fails.
func TestLoadOrCreateContinuationSecretOpenFileError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root bypasses filesystem permission checks")
	}
	stateDir := t.TempDir()
	contDir := filepath.Join(stateDir, "continuation")
	if err := os.MkdirAll(contDir, 0o500); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(contDir, 0o700) })

	if _, err := LoadOrCreateContinuationSecret(stateDir); err == nil {
		t.Fatal("LoadOrCreateContinuationSecret(read-only dir) error = nil, want create failure")
	}
}

// TestLoadOrCreateContinuationSecretMissingStateDir covers the empty-state-dir guard.
func TestLoadOrCreateContinuationSecretMissingStateDir(t *testing.T) {
	if _, err := LoadOrCreateContinuationSecret(""); !errors.Is(err, ErrContinuationSecretUnavailable) {
		t.Fatalf("LoadOrCreateContinuationSecret(\"\") error = %v, want ErrContinuationSecretUnavailable", err)
	}
}

// TestStreamResultNilReceivers covers the nil-receiver guards on StreamResult
// and StreamObjectResult.
func TestStreamResultNilReceivers(t *testing.T) {
	var sr *StreamResult
	if _, err := sr.Response(); err == nil {
		t.Fatal("(*StreamResult)(nil).Response() error = nil, want error")
	}
	if sr.PartialResponse() != nil {
		t.Fatal("(*StreamResult)(nil).PartialResponse() != nil, want nil")
	}

	var sor *StreamObjectResult
	if sor.Output() != nil {
		t.Fatal("(*StreamObjectResult)(nil).Output() != nil, want nil")
	}
	if _, err := sor.Response(); err == nil {
		t.Fatal("(*StreamObjectResult)(nil).Response() error = nil, want error")
	}
}
