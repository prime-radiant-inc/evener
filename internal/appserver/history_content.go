package appserver

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

type historyContentContextKey struct{}

type historyContentRequest struct {
	filter   *apphistory.Filter
	preserve bool
}

// HandleThreadHistory applies the public selection at this server's client
// boundary. The underlying handlers keep full producer snapshots and shared
// relays; each downstream subscription owns its own projection.
func HandleThreadHistory(router *Router, read apphistory.ReadFunc, list apphistory.ListFunc) {
	HandleTyped(router, appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		filter := apphistory.NewFilter(params.Selection())
		ctx = context.WithValue(ctx, historyContentContextKey{}, historyContentRequest{filter: filter, preserve: !params.IncludeTurns})
		response, err := RetrySubscription(ctx, func(ctx context.Context) (appwire.ThreadReadResponse, error) {
			return apphistory.Read(ctx, params, read, list)
		})
		if err == nil {
			filter.Seed(response.Overlay)
		}
		return response, err
	})
	HandleTyped(router, appwire.MethodThreadTurnsList, func(ctx context.Context, params appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
		return apphistory.List(ctx, params, list)
	})
}

func historyContent(ctx context.Context) *apphistory.Filter {
	request, _ := ctx.Value(historyContentContextKey{}).(historyContentRequest)
	return request.filter
}

// Metadata reads and relay preparation do not replace a client's history view.
// Resolve the old owner under the projection gate, before replacing its entry.
func subscriptionHistoryContent(ctx context.Context, subs *Subscriptions, connID, threadID string) *apphistory.Filter {
	request, explicit := ctx.Value(historyContentContextKey{}).(historyContentRequest)
	if !explicit || request.preserve {
		if previous, exists := subs.contentOwner(connID, threadID); exists {
			return previous
		}
	}
	return historyContent(ctx)
}

func (s *Subscriptions) setContent(connID, threadID string, generation uint64, filter *apphistory.Filter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.byConn[connID][threadID]; sub != nil && sub.generation == generation {
		sub.content = filter
	}
}

// Failed daemon reads still release recovery notifications. Restore only the
// previous view, including its live overlay identities, before that release.
func (s *Subscriptions) restoreContent(connID, threadID string, generation uint64, rollback subscriptionCaptureRollback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.byConn[connID][threadID]
	if current == nil || !current.buffering || current.generation != generation {
		return
	}
	previous := rollback.threadPrevious
	if rollback.replace {
		for i := range rollback.connection.subscriptions {
			if sub := &rollback.connection.subscriptions[i]; sub.threadID == threadID {
				previous = sub
				break
			}
		}
	}
	if previous != nil && !previous.withdrawn {
		current.content = previous.content
	}
}

func (s *Subscriptions) content(connID, threadID string) *apphistory.Filter {
	filter, _ := s.contentOwner(connID, threadID)
	return filter
}

func (s *Subscriptions) contentOwner(connID, threadID string) (*apphistory.Filter, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sub := s.byConn[connID][threadID]; sub != nil {
		return sub.content, true
	}
	return nil, false
}

func (c *Connection) enqueueHistory(record SequencedNotification, filter *apphistory.Filter) bool {
	notification, keep, err := filter.Notification(record.Notification)
	if err != nil {
		c.server.logf("history notification selection: %v", err)
		return false
	}
	return !keep || c.enqueue(appwire.Message{Notification: &notification})
}

type subscriptionRetryContextKey struct{}

type subscriptionAttempt struct {
	wait <-chan struct{}
}

// RetrySubscription retries subscription preparation after a conflicting history
// response commits or aborts. The operation must return without holding its
// ownership locks and must not dispatch mutations. Nested scopes leave retry to
// the outer owner so waits cannot retain a caller's relay or deletion lock.
func RetrySubscription[T any](ctx context.Context, prepare func(context.Context) (T, error)) (T, error) {
	if _, nested := ctx.Value(subscriptionRetryContextKey{}).(*subscriptionAttempt); nested {
		return prepare(ctx)
	}
	for {
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		attempt := &subscriptionAttempt{}
		result, err := prepare(context.WithValue(ctx, subscriptionRetryContextKey{}, attempt))
		if attempt.wait == nil {
			return result, err
		}
		select {
		case <-attempt.wait:
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		}
	}
}

// Defer preservation while a history owner is awaiting its response. Callers
// hold hydrationMu; returning lets their own ownership scopes unwind before the
// outer preparation retries. The pending owner, buffer and admission stay intact.
func (c *Connection) deferHistorySubscription(ctx context.Context, responseID, threadID string, replace bool) bool {
	c.responseMu.Lock()
	defer c.responseMu.Unlock()
	for key, pending := range c.hydrations {
		if key == responseID || pending.historyDone == nil || (!replace && !pending.replace && pending.threadID != threadID) {
			continue
		}
		if attempt, ok := ctx.Value(subscriptionRetryContextKey{}).(*subscriptionAttempt); ok {
			attempt.wait = pending.historyDone
		}
		return true
	}
	return false
}
