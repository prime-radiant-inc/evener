package appserver

import (
	"context"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apphistory"
)

type historyContentContextKey struct{}

// HandleThreadHistory applies the public selection at this server's client
// boundary. The underlying handlers keep full producer snapshots and shared
// relays; each downstream subscription owns its own projection.
func HandleThreadHistory(router *Router, read apphistory.ReadFunc, list apphistory.ListFunc) {
	HandleTyped(router, appwire.MethodThreadRead, func(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
		filter := apphistory.NewFilter(params.Selection())
		ctx = context.WithValue(ctx, historyContentContextKey{}, filter)
		response, err := apphistory.Read(ctx, params, read, list)
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
	filter, _ := ctx.Value(historyContentContextKey{}).(*apphistory.Filter)
	return filter
}

func (s *Subscriptions) setContent(connID, threadID string, generation uint64, filter *apphistory.Filter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sub := s.byConn[connID][threadID]; sub != nil && sub.generation == generation {
		sub.content = filter
	}
}

func (s *Subscriptions) content(connID, threadID string) *apphistory.Filter {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if sub := s.byConn[connID][threadID]; sub != nil {
		return sub.content
	}
	return nil
}

func (c *Connection) enqueueHistory(record SequencedNotification, filter *apphistory.Filter) bool {
	notification, keep, err := filter.Notification(record.Notification)
	if err != nil {
		c.server.logf("history notification selection: %v", err)
		return false
	}
	return !keep || c.enqueue(appwire.Message{Notification: &notification})
}
