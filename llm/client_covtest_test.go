package llm

import (
	"context"
	"errors"
	"testing"

	apilog "primeradiant.com/evener/llm/apilog"
)

// TestCovBeginProviderOperationNilContext covers the nil-ctx path in
// beginProviderOperation (client.go line 274-275).
func TestCovBeginProviderOperationNilContext(t *testing.T) {
	c := NewClient()
	// Register a sink middleware so the function does not return early.
	recorder := &recordingAPIAttemptSink{}
	sink := &sinkMiddleware{sink: recorder}
	c.Use(sink)
	ctx, op := c.beginProviderOperation(nil)
	if ctx == nil {
		t.Fatal("beginProviderOperation(nil) should return a non-nil context")
	}
	if op == nil {
		t.Fatal("beginProviderOperation should return a non-nil operation when a sink is present")
	}
	if !op.ownsGroup || op.group == nil || apiAttemptGroupFromContext(ctx) != op.group {
		t.Fatalf("operation = %+v, context group = %p; want one owned attached group", op, apiAttemptGroupFromContext(ctx))
	}
	state, _ := ctx.Value(apiAttemptSinkContextKey{}).(apiAttemptSinkContext)
	if state.sink != sink {
		t.Fatalf("context sink = %T %p, want middleware %p", state.sink, state.sink, sink)
	}
}

// TestCovBeginProviderOperationNoSink covers the sink == nil early return
// in beginProviderOperation (client.go line 286-287).
func TestCovBeginProviderOperationNoSink(t *testing.T) {
	c := NewClient()
	// No middleware or sink in context.
	input := context.Background()
	ctx, op := c.beginProviderOperation(input)
	if op != nil {
		t.Fatal("beginProviderOperation without sink should return nil operation")
	}
	if ctx != input {
		t.Fatal("beginProviderOperation without sink replaced the caller context")
	}
}

// TestCovValidateModelCompatibilityNilClient covers the nil-client guard
// in ValidateModelCompatibility (client.go line 413-414).
func TestCovValidateModelCompatibilityNilClient(t *testing.T) {
	var c *Client
	if err := c.ValidateModelCompatibility("any", "any"); err != nil {
		t.Fatalf("ValidateModelCompatibility on nil client = %v, want nil", err)
	}
}

// TestCovValidateModelCompatibilityWithValidator covers the path where
// the adapter implements ModelCompatibilityValidator.
func TestCovValidateModelCompatibilityWithValidator(t *testing.T) {
	c := NewClient()
	wantErr := errors.New("unsupported by validator")
	validator := &modelValidatorAdapter{name: "validator", validateErr: wantErr}
	c.Register(validator)
	err := c.ValidateModelCompatibility("validator", "unsupported-model")
	if !errors.Is(err, wantErr) {
		t.Fatalf("validation error = %v, want validator error identity", err)
	}
	if len(validator.validatedModels) != 1 || validator.validatedModels[0] != "unsupported-model" {
		t.Fatalf("validated models = %q, want [unsupported-model]", validator.validatedModels)
	}
}

// TestCovBeginAPIAttemptGroupScopeNilContext covers the nil-ctx path
// (api_attempt_scope.go line 22-23).
func TestCovBeginAPIAttemptGroupScopeNilContext(t *testing.T) {
	ctx, scope := BeginAPIAttemptGroupScope(nil)
	if ctx == nil {
		t.Fatal("ctx should not be nil")
	}
	if scope == nil {
		t.Fatal("scope should not be nil")
	}
	if !scope.owned || scope.ctx != ctx || scope.group == nil || apiAttemptGroupFromContext(ctx) != scope.group {
		t.Fatalf("scope = %+v, context group = %p; want one owned attached group", scope, apiAttemptGroupFromContext(ctx))
	}
}

// TestCovBeginAPIAttemptGroupScopeWithExistingGroup covers the path where
// the context already has a group, so the scope is not owned.
func TestCovBeginAPIAttemptGroupScopeWithExistingGroup(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	group := NewAPIAttemptGroup("existing")
	ctx := WithAPIAttemptSink(WithAPIAttemptGroup(context.Background(), group), sink)
	gotCtx, scope := BeginAPIAttemptGroupScope(ctx)
	if gotCtx != ctx || scope == nil || scope.owned || scope.ctx != nil || scope.group != nil {
		t.Fatalf("existing-group result = (%v, %+v), want unchanged context and inert scope", gotCtx, scope)
	}
	scope.SettleResult(nil)
	_, settlements, _ := sink.snapshot()
	if len(settlements) != 0 {
		t.Fatalf("inert scope appended settlements: %+v", settlements)
	}
}

// TestCovBeginAPIAttemptGroupScopeNilSettleResult covers the nil-receiver
// guard in SettleResult.
func TestCovBeginAPIAttemptGroupScopeNilSettleResult(t *testing.T) {
	var scope *APIAttemptGroupScope
	scope.SettleResult(nil)
	// No panic.
}

// TestCovBeginAPIAttemptGroupScopeOwnedSettleResult covers the owned path
// of SettleResult.
func TestCovBeginAPIAttemptGroupScopeOwnedSettleResult(t *testing.T) {
	sink := &recordingAPIAttemptSink{}
	ctx, scope := BeginAPIAttemptGroupScope(WithAPIAttemptSink(context.Background(), sink))
	scope.SettleResult(errors.New("test error"))
	attempts, settlements, _ := sink.snapshot()
	if len(attempts) != 0 {
		t.Fatalf("attempt count = %d, want 0", len(attempts))
	}
	if len(settlements) != 1 || settlements[0].AttemptGroupID != scope.group.ID || settlements[0].FinalAttemptCount != 0 || settlements[0].Outcome != apilog.AttemptTransportFail {
		t.Fatalf("settlements = %+v, want one zero-attempt transport failure for %q", settlements, scope.group.ID)
	}
	if apiAttemptGroupFromContext(ctx) != scope.group {
		t.Fatal("owned scope group was not attached to returned context")
	}
}

// modelValidatorAdapter is a fake adapter that implements
// ModelCompatibilityValidator.
type modelValidatorAdapter struct {
	name            string
	validateErr     error
	validatedModels []string
}

func (a *modelValidatorAdapter) Name() string { return a.name }
func (a *modelValidatorAdapter) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{}, nil
}
func (a *modelValidatorAdapter) Stream(_ context.Context, _ Request) (Stream, error) {
	return nil, nil
}
func (a *modelValidatorAdapter) ValidateModel(model string) error {
	a.validatedModels = append(a.validatedModels, model)
	return a.validateErr
}

// sinkMiddleware wraps a recordingAPIAttemptSink as a Middleware so it can be
// registered via Client.Use and discovered by beginProviderOperation as an
// APIAttemptSink.
type sinkMiddleware struct {
	sink *recordingAPIAttemptSink
}

func (m *sinkMiddleware) WrapComplete(next CompleteFunc) CompleteFunc { return next }
func (m *sinkMiddleware) WrapStream(next StreamFunc) StreamFunc       { return next }
func (m *sinkMiddleware) AppendAttempt(ctx context.Context, rec apilog.APIAttemptRecord) error {
	return m.sink.AppendAttempt(ctx, rec)
}
func (m *sinkMiddleware) AppendSettlement(ctx context.Context, rec apilog.APIAttemptGroupSettlement) error {
	return m.sink.AppendSettlement(ctx, rec)
}
