package agent

import "context"

// drainStop is the ordinary stop drain as a package-local test helper: the same
// reconciliation as the close drain, executing every plan the pass settled, with
// no abandon signal and no warnings-only mode.
//
// It lives here rather than in delegate_tree_stop.go because production has no
// caller for it any more. closeRuntimeTree routes the close through
// drainStopForClose, which needs the warningsOnly mode this signature cannot
// name, so the wrapper's only users are tests. A production symbol needs a
// production caller; the tests that drive the ordinary drain keep their seam.
func (c *delegateTreeController) drainStop(ctx context.Context, stop *delegateStopState, root *Session) error {
	return c.drainStopAbandonable(ctx, stop, root, nil, false)
}
