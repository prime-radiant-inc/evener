package llm

import (
	"context"

	"primeradiant.com/serf/identifier"
)

// APIAttemptGroupScope owns a logical provider-attempt group only when its
// caller did not already supply one in the context.
type APIAttemptGroupScope struct {
	ctx   context.Context
	group *APIAttemptGroup
	owned bool
}

// BeginAPIAttemptGroupScope reuses a caller-owned group when present. Otherwise
// it attaches a new group that the returned scope settles after the logical
// model call reaches its final result.
func BeginAPIAttemptGroupScope(ctx context.Context) (context.Context, *APIAttemptGroupScope) {
	if ctx == nil {
		ctx = context.Background()
	}
	if apiAttemptGroupFromContext(ctx) != nil {
		return ctx, &APIAttemptGroupScope{}
	}
	group := NewAPIAttemptGroup(identifier.MustNewAgentCallID())
	ctx = WithAPIAttemptGroup(ctx, group)
	return ctx, &APIAttemptGroupScope{ctx: ctx, group: group, owned: true}
}

// SettleResult appends finality only for a group created by this scope.
func (s *APIAttemptGroupScope) SettleResult(err error) {
	if s == nil || !s.owned {
		return
	}
	s.group.SettleResult(s.ctx, err)
}
