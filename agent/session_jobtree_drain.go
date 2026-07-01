package agent

import "context"

// activeJobCount reports how many jobs in this session's own tree are not yet
// terminal (running or queued), including delegate children recorded in this
// session's store. It is the drain-loop's "is the tree still busy?" signal.
func (s *Session) activeJobCount() int {
	if s.jobManager == nil {
		return 0
	}
	n := 0
	for _, rec := range s.jobManager.list(listFilter{IncludeNested: true}) {
		if !rec.Status.IsTerminal() {
			n++
		}
	}
	return n
}

// DrainJobTree keeps re-driving the coordinator on delegate completions until
// the session's job tree is terminal, then returns the last notification turn's
// result (empty when no drain turn ran). It is the one-shot analogue of the
// serve loop's notification pump: a coordinator that fires a fire-and-return
// delegate (max_wait_ms=0) ends its turn while the child is still running, so
// without this drain the caller's Close() would SIGKILL the child before it
// finishes (PRI-2441).
//
// The wait is bounded by ctx: a coordinator whose delegated work never
// completes blocks until the caller's context is cancelled. Individual delegate
// turns carry their own round/time caps, so a well-formed tree always
// quiesces on its own.
func (s *Session) DrainJobTree(ctx context.Context) (string, error) {
	wake := make(chan struct{}, 1)
	s.SetNotifyFunc(func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	defer s.SetNotifyFunc(nil)

	lastResult := ""
	for {
		if s.peekNotifications() > 0 {
			// A completion is queued: run a notification turn so the coordinator's
			// model receives it and can dispatch more work or wrap up. The turn's
			// internal loop drains any further already-pending notifications.
			res, err := s.ProcessInputKind(ctx, "", nil, EntryNotification)
			if err != nil {
				return lastResult, err
			}
			if res != "" {
				lastResult = res
			}
			continue
		}
		if s.activeJobCount() == 0 {
			// Nothing pending and nothing running: the tree has quiesced.
			return lastResult, nil
		}
		// Work is still in flight but has not signalled yet. Block until a child
		// completion wakes us or the caller's context is cancelled.
		select {
		case <-wake:
		case <-ctx.Done():
			return lastResult, ctx.Err()
		}
	}
}
