package appwire

// NotificationTargeted lets the daemon re-address a notification's params to
// the fanout target it is committing them under (threadId/ref) without a JSON
// round trip. Every params struct the app-event projector emits implements it;
// the value receiver mutates a copy and returns it, so callers holding the
// params as `any` get the restamped value back. Params the projector builds as
// map[string]any carry threadId/ref keys instead and are restamped directly.
type NotificationTargeted interface {
	WithNotificationTarget(threadID, ref string) NotificationTargeted
}

func (p ThreadStartedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p TurnStartedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ItemLifecycleParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p AgentMessageDeltaParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p AgentMessageResetParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ReasoningSummaryDeltaParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ToolOutputDeltaParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadStatusChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadQueueChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadNameChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadModelChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadModelRetryParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadReasoningEffortChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p ThreadVisionModelChangedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p TaskUpdatedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p GoalUpdatedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p SandboxEscalationRequested) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p SandboxEscalationResolved) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p EvenerJobParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p JobsTreeUpdatedParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}

func (p EvenerDelegateParams) WithNotificationTarget(threadID, ref string) NotificationTargeted {
	p.ThreadID, p.Ref = threadID, ref
	return p
}
