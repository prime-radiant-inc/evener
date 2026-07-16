package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"
)

func FuzzCoreContracts(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2, 3, 4, 5, 6, 7} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint8) {
		fuzzTimeoutAndHeaders(t, seed)
		fuzzErrorContracts(t, seed)
		fuzzMiddlewareContracts(t)
		fuzzTypeContracts(t)
	})
}

func FuzzCoreDeterministicScenarios(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		TestConfigurationErrorSatisfiesErrorContract(t)
		TestRewriteErrorProviderLeavesEmptyProviderUnstamped(t)
		TestClassifyByMessageAuthenticationSignals(t)
		TestMessageConstructors(t)
		TestIntFromAny(t)
		TestReasoningBudget(t)
		TestReasoningTextJoinsBlocksWithBlankLine(t)
		TestDefaultSleepTimerAndCancel(t)
		TestRetryableError(t)
		TestSaturatingDelay(t)
		TestParseAndValidateArgs(t)
		TestCompileSchemaRejectsUnmarshalableParams(t)
		TestPrepareToolsRejectsInvalidToolName(t)
		TestGenerateOptionsValidate(t)
		TestPrepareGenerationInputValidation(t)
		TestStreamAccumulatorToolCallDeltaWithoutStart(t)
		TestStreamAccumulatorFinishRebuildsEmptyResponse(t)
		TestCopyResponseMetadataNilSource(t)
		TestStreamAccumulatorNilReceiver(t)
		TestModelCatalogLookups(t)
		TestEstimateInputTokensCountsResponseFormat(t)
		TestCountInputTokensConfigErrors(t)
		TestLoadOrCreateContinuationSecretOpenFileError(t)
		TestLoadOrCreateContinuationSecretMissingStateDir(t)
		TestStreamResultNilReceivers(t)
		TestErrorClassStringExhaustive(t)
		TestExpandTilde(t)
		TestInferMimeTypeFromPath(t)
		TestDataURI(t)
		TestStripMarkdownCodeFence(t)
		TestParseIntHelper(t)
		TestParseBoolHelper(t)
		TestParseBoolPtrHelper(t)
		TestParseFloatPtrHelper(t)
		TestStripMarkdownCodeFenceNested(t)
		TestGetLatestModel_CapabilityFilters(t)
		TestGetLatestModel_NilCatalog(t)
		TestMiddlewareFunc_NilPhasesPassThrough(t)
		TestApplyMiddleware_SkipsNilEntries(t)
		TestCloneProviderOptions_DeepIndependence(t)
		TestEstimateInputTokens_AllContentKinds(t)
		TestEstimateOpenAIImageTokens(t)
		TestImageDimensions_LocalFileURL(t)
		TestEstimateImageTokens_FallbackWhenDimensionsUnknown(t)
		TestEstimateImageTokens_DefaultProviderFamily(t)
		TestParseRetryAfterEdgeCases(t)
		TestStampErrorBehaviorTag(t)
		TestNonHTTPBaseErrorMessage(t)
		TestNewUnsupportedToolChoiceError(t)
	})
}

func fuzzTimeoutAndHeaders(t *testing.T, seed uint8) {
	ctx := context.Background()
	got, cancel := ApplyAdapterTimeout(ctx, nil, false)
	cancel()
	if got != ctx || AdapterTransport(nil) != nil || StreamReadSSEOptions(nil) != nil {
		t.Fatal("nil adapter timeout changed defaults")
	}
	at := &AdapterTimeout{}
	if AdapterTransport(at) != nil || StreamReadSSEOptions(at) != nil {
		t.Fatal("zero adapter timeout created transport options")
	}
	at.Connect, at.Request, at.StreamRead = time.Second, time.Second, time.Second
	deadlineCtx, deadlineCancel := ApplyAdapterTimeout(ctx, at, false)
	defer deadlineCancel()
	if _, ok := deadlineCtx.Deadline(); !ok {
		t.Fatal("request timeout did not set deadline")
	}
	streamCtx, streamCancel := ApplyAdapterTimeout(ctx, at, true)
	streamCancel()
	if streamCtx != ctx || AdapterTransport(at) == nil || len(StreamReadSSEOptions(at)) != 1 {
		t.Fatal("positive adapter timeout contract failed")
	}
	client := &http.Client{}
	if ClientWithAdapterTimeout(client, nil) != client {
		t.Fatal("client copied without transport timeouts")
	}
	if cp := ClientWithAdapterTimeout(client, at); cp == client || cp.Transport == nil {
		t.Fatal("client transport was not copied and configured")
	}
	if MergeHeaders(nil, nil) != nil {
		t.Fatal("empty headers must remain nil")
	}
	merged := MergeHeaders(map[string]string{"x-test": "base", "z": "last"}, map[string]string{"X-Test": "override"})
	if merged["X-Test"] != "override" || !reflect.DeepEqual(sortedHeaderKeys(merged), []string{"X-Test", "Z"}) {
		t.Fatalf("header merge = %#v", merged)
	}
	_ = seed
}

func fuzzErrorContracts(t *testing.T, seed uint8) {
	cause := errors.New("cause")
	var cfg Error = &ConfigurationError{Message: " bad ", Cause: cause}
	if cfg.Error() != "configuration error: bad" || cfg.Provider() != "" || cfg.BehaviorTag() != "" || cfg.StatusCode() != 0 || cfg.ErrorCode() != "" || cfg.Retryable() || cfg.RetryAfter() != nil || cfg.Raw() != nil || !errors.Is(cfg, cause) {
		t.Fatal("configuration error contract failed")
	}
	raw := map[string]any{"error": map[string]any{"code": "code", "type": "type"}}
	err := ErrorFromHTTPStatus("provider", []int{400, 401, 403, 404, 408, 413, 422, 429, 500, 418}[int(seed)%10], "message", raw, nil)
	var le Error
	if !errors.As(err, &le) {
		t.Fatalf("typed error missing: %T", err)
	}
	_ = le.Error()
	_ = le.Provider()
	_ = le.BehaviorTag()
	_ = le.StatusCode()
	_ = le.ErrorCode()
	_ = le.Retryable()
	_ = le.RetryAfter()
	_ = le.Raw()
	_ = errors.Unwrap(le)
	err = StampErrorBehaviorTag(err, " behavior ")
	err = RewriteErrorProvider(err, " rewritten ")
	if err == nil {
		t.Fatal("error stamping lost error")
	}
	for _, e := range []error{
		NewAbortError("", cause),
		NewStreamError("p", "", cause),
		NewNoObjectGeneratedError("bad", "raw", cause),
		NewUnsupportedToolChoiceError("p", ""),
	} {
		var nonHTTP Error
		if !errors.As(e, &nonHTTP) {
			t.Fatalf("non-http error missing interface: %T", e)
		}
		_ = nonHTTP.Error()
		_ = nonHTTP.Provider()
		_ = nonHTTP.BehaviorTag()
		_ = nonHTTP.StatusCode()
		_ = nonHTTP.ErrorCode()
		_ = nonHTTP.Retryable()
		_ = nonHTTP.RetryAfter()
		_ = nonHTTP.Raw()
		_ = errors.Unwrap(nonHTTP)
	}
}

func fuzzMiddlewareContracts(t *testing.T) {
	ctx := context.Background()
	base := CompleteFunc(func(context.Context, Request) (Response, error) { return Response{ID: "ok"}, nil })
	streamBase := StreamFunc(func(context.Context, Request) (Stream, error) { return nil, errCauseSentinel })
	pass := MiddlewareFunc{}
	if got, _ := pass.WrapComplete(base)(ctx, Request{}); got.ID != "ok" {
		t.Fatal("nil complete middleware did not pass through")
	}
	if _, err := pass.WrapStream(streamBase)(ctx, Request{}); !errors.Is(err, errCauseSentinel) {
		t.Fatal("nil stream middleware did not pass through")
	}
	m := MiddlewareFunc{
		Complete: func(ctx context.Context, req Request, next CompleteFunc) (Response, error) { return next(ctx, req) },
		Stream:   func(ctx context.Context, req Request, next StreamFunc) (Stream, error) { return next(ctx, req) },
	}
	if got, _ := applyMiddlewareComplete(base, []Middleware{nil, m})(ctx, Request{}); got.ID != "ok" {
		t.Fatal("complete middleware chain failed")
	}
	if _, err := applyMiddlewareStream(streamBase, []Middleware{nil, m})(ctx, Request{}); !errors.Is(err, errCauseSentinel) {
		t.Fatal("stream middleware chain failed")
	}
}

var errCauseSentinel = errors.New("sentinel")

func fuzzTypeContracts(t *testing.T) {
	if Developer("d").Role != RoleDeveloper || ToolResult("id", "out", true).Role != RoleTool {
		t.Fatal("message constructors failed")
	}
	for _, effort := range []string{"minimal", "low", "medium", "high", "xhigh", "max", "unknown", " none "} {
		_ = ReasoningBudget(effort)
		_ = NormalizeReasoningEffort(effort)
		_ = ReasoningEffortRank(effort)
		_ = ClampReasoningEffort(effort, []string{"low", "high", "max", "unknown"})
	}
	if !reflect.DeepEqual(OrderedEffortLevels(map[string]string{"max": "", "low": ""}), []string{"low", "max"}) {
		t.Fatal("effort ordering failed")
	}
	for _, sig := range append(OpenAICompatReasoningFields(), "foreign") {
		_ = IsOpenAICompatReasoningField(sig)
	}
	for _, encrypted := range []string{"", "opaque", "[", `[]`, `[{"type":"reasoning.encrypted"}]`, `[{"type":"other"}]`} {
		_ = IsOpenAICompatEncryptedReasoning(encrypted)
	}
	for _, v := range []any{json.Number("4"), float64(3), 2, "bad"} {
		_ = IntFromAny(v)
	}
	for _, provider := range []string{"anthropic", "google", "openai"} {
		for _, raw := range []string{"", "end_turn", "stop_sequence", "max_tokens", "tool_use", "pause_turn", "refusal", "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "stop", "length", "tool_calls", "content_filter", "other"} {
			_ = NormalizeFinishReason(provider, raw)
		}
	}
	requests := []Request{
		{},
		{Model: "m"},
		{Model: "m", Messages: []Message{User("hi")}, Tools: []ToolDefinition{{Name: "bad name"}}},
		{Model: "m", Messages: []Message{User("hi")}, Tools: []ToolDefinition{{Name: "ok", Parameters: map[string]any{"type": "array"}}}},
		{Model: "m", Messages: []Message{User("hi")}, Tools: []ToolDefinition{{Name: "ok", Parameters: map[string]any{"type": "object"}}}},
	}
	for _, req := range requests {
		_ = req.Validate()
	}
}
