package llm

import (
	"context"
	"testing"
)

// A scope begun without a context still gets one, owning a fresh attempt
// group attached to it.
func TestBeginAPIAttemptGroupScopeOwnsAGroupForANilContext(t *testing.T) {
	var input context.Context
	ctx, scope := BeginAPIAttemptGroupScope(input)
	if ctx == nil || scope == nil {
		t.Fatalf("BeginAPIAttemptGroupScope(nil) = (%v, %v), want a context and a scope", ctx, scope)
	}
	if !scope.owned || scope.ctx != ctx || scope.group == nil || apiAttemptGroupFromContext(ctx) != scope.group {
		t.Fatalf("scope = %+v, context group = %p; want one owned attached group", scope, apiAttemptGroupFromContext(ctx))
	}
}
