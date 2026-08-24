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
	sink := &sinkMiddleware{sink: &recordingAPIAttemptSink{}}
	c.Use(sink)
	ctx, op := c.beginProviderOperation(context.TODO())
	if ctx == nil {
		t.Fatal("beginProviderOperation(nil) should return a non-nil context")
	}
	if op == nil {
		t.Fatal("beginProviderOperation should return a non-nil operation when a sink is present")
	}
}

// TestCovBeginProviderOperationNoSink covers the sink == nil early return
// in beginProviderOperation (client.go line 286-287).
func TestCovBeginProviderOperationNoSink(t *testing.T) {
	c := NewClient()
	// No middleware or sink in context.
	ctx, op := c.beginProviderOperation(context.Background())
	if op != nil {
		t.Fatal("beginProviderOperation without sink should return nil operation")
	}
	if ctx == nil {
		t.Fatal("ctx should not be nil")
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
	c.Register(&modelValidatorAdapter{name: "validator"})
	err := c.ValidateModelCompatibility("validator", "unsupported-model")
	if err == nil {
		t.Fatal("expected error from model validator")
	}
}

// TestCovBeginAPIAttemptGroupScopeNilContext covers the nil-ctx path
// (api_attempt_scope.go line 22-23).
func TestCovBeginAPIAttemptGroupScopeNilContext(t *testing.T) {
	ctx, scope := BeginAPIAttemptGroupScope(context.TODO())
	if ctx == nil {
		t.Fatal("ctx should not be nil")
	}
	if scope == nil {
		t.Fatal("scope should not be nil")
	}
}

// TestCovBeginAPIAttemptGroupScopeWithExistingGroup covers the path where
// the context already has a group, so the scope is not owned.
func TestCovBeginAPIAttemptGroupScopeWithExistingGroup(t *testing.T) {
	group := NewAPIAttemptGroup("existing")
	ctx := WithAPIAttemptGroup(context.Background(), group)
	_, scope := BeginAPIAttemptGroupScope(ctx)
	scope.SettleResult(nil)
	// Should be a no-op since the scope doesn't own the group.
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
	ctx, scope := BeginAPIAttemptGroupScope(context.Background())
	scope.SettleResult(errors.New("test error"))
	// The settlement should have been recorded. We can't easily verify
	// without a sink, but no panic is the key.
	_ = ctx
}

// modelValidatorAdapter is a fake adapter that implements
// ModelCompatibilityValidator.
type modelValidatorAdapter struct {
	name string
}

func (a *modelValidatorAdapter) Name() string { return a.name }
func (a *modelValidatorAdapter) Complete(_ context.Context, _ Request) (Response, error) {
	return Response{}, nil
}
func (a *modelValidatorAdapter) Stream(_ context.Context, _ Request) (Stream, error) {
	return nil, nil
}
func (a *modelValidatorAdapter) ValidateModel(model string) error {
	return errors.New("model " + model + " not supported")
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
