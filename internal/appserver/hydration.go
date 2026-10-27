package appserver

import (
	"context"
	"sort"

	"primeradiant.com/evener/appwire"
)

func (c *Connection) addHydration(finalizer *hydrationResponseFinalizer) {
	c.responseMu.Lock()
	if c.hydrations == nil {
		c.hydrations = map[string]*hydrationResponseFinalizer{}
	}
	c.hydrations[finalizer.responseID] = finalizer
	c.responseMu.Unlock()
}

func (c *Connection) takeHydration(responseID string) *hydrationResponseFinalizer {
	c.responseMu.Lock()
	finalizer := c.hydrations[responseID]
	delete(c.hydrations, responseID)
	c.responseMu.Unlock()
	return finalizer
}

func (c *Connection) takeSupersededHydrations(
	responseID, threadID string,
	replace bool,
) []*hydrationResponseFinalizer {
	c.responseMu.Lock()
	var superseded []*hydrationResponseFinalizer
	for key, finalizer := range c.hydrations {
		if key != responseID && !replace && !finalizer.replace && finalizer.threadID != threadID {
			continue
		}
		superseded = append(superseded, finalizer)
		delete(c.hydrations, key)
	}
	c.responseMu.Unlock()
	sort.Slice(superseded, func(i, j int) bool {
		return superseded[i].generation > superseded[j].generation
	})
	return superseded
}

func (c *Connection) takeAllHydrations() []*hydrationResponseFinalizer {
	c.responseMu.Lock()
	pending := make([]*hydrationResponseFinalizer, 0, len(c.hydrations))
	for key, finalizer := range c.hydrations {
		pending = append(pending, finalizer)
		delete(c.hydrations, key)
	}
	c.responseMu.Unlock()
	return pending
}

// responseWritten runs the after-write callback registered for msg's request,
// if any, now that the frame has reached the transport.
func (c *Connection) responseWritten(msg appwire.Message) {
	responseID, _ := responseHydrationOutcome(msg)
	if responseID == "" {
		return
	}
	c.responseMu.Lock()
	fn := c.afterWrite[responseID]
	delete(c.afterWrite, responseID)
	c.responseMu.Unlock()
	if fn != nil {
		fn()
	}
}

// runPendingAfterWrite runs every after-write callback whose response was
// never written, because the send loop stopped first. Callers that only ever
// act once (a self-update restart) must still act when the browser vanished.
func (c *Connection) runPendingAfterWrite() {
	c.responseMu.Lock()
	pending := make([]func(), 0, len(c.afterWrite))
	for key, fn := range c.afterWrite {
		pending = append(pending, fn)
		delete(c.afterWrite, key)
	}
	c.afterWriteDrained = true
	c.responseMu.Unlock()
	for _, fn := range pending {
		fn()
	}
}

func responseHydrationOutcome(msg appwire.Message) (string, bool) {
	switch {
	case msg.Response != nil:
		return requestIDKey(msg.Response.ID), true
	case msg.Error != nil:
		return requestIDKey(msg.Error.ID), false
	default:
		return "", false
	}
}

type hydrationResponseFinalizer struct {
	server                 *Server
	conn                   *Connection
	responseID             string
	threadID               string
	generation             uint64
	replace                bool
	rollback               subscriptionCaptureRollback
	handoff                CaptureSubscriptionHandoff
	releaseOnErrorResponse bool
	historyDone            chan struct{}
}

func (f *hydrationResponseFinalizer) commit(responseSucceeded bool) {
	var failedCapture *subscriptionCaptureRollback
	if !responseSucceeded {
		failedCapture = &f.rollback
	}
	f.server.releaseHydration(f.conn, f.threadID, f.generation, failedCapture)
}

func (f *hydrationResponseFinalizer) commitAfterRelease() {
	defer f.finishHistory()
	if f.handoff.Commit != nil {
		f.handoff.Commit()
	}
}

func (f *hydrationResponseFinalizer) abort() {
	f.server.withdrawHydration(f.conn, f.threadID, f.generation, f.rollback)
}

func (f *hydrationResponseFinalizer) abortAfterWithdrawal() {
	defer f.finishHistory()
	if f.handoff.Abort != nil {
		f.handoff.Abort()
	}
}

func (f *hydrationResponseFinalizer) finishHistory() {
	if f.historyDone != nil {
		close(f.historyDone)
	}
}

// AfterResponseWritten runs fn once the response to the request being
// handled in ctx has been written to the transport, or once the connection
// tears down without writing it. It reports false, and does not retain fn,
// when ctx carries no appserver connection or when the connection has
// already torn down -- a handler that ran long enough for the send loop to
// stop first must act for itself rather than wait for a callback nothing
// will ever run.
//
// A second registration for the same request replaces the first: callbacks
// are not chained. One handler owns one response, which is all any caller
// needs today.
func AfterResponseWritten(ctx context.Context, fn func()) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		return false
	}
	responseID, ok := ctx.Value(requestIDContextKey{}).(string)
	if !ok || responseID == "" {
		return false
	}
	conn.responseMu.Lock()
	defer conn.responseMu.Unlock()
	if conn.afterWriteDrained {
		return false
	}
	if conn.afterWrite == nil {
		conn.afterWrite = map[string]func(){}
	}
	conn.afterWrite[responseID] = fn
	return true
}

// RunAfterResponseWritten is AfterResponseWritten for callers that must act
// exactly once either way: fn runs after the response is written, or right
// now when there is no transport that will write one.
func RunAfterResponseWritten(ctx context.Context, fn func()) {
	if !AfterResponseWritten(ctx, fn) {
		fn()
	}
}

func (s *Server) releaseHydration(conn *Connection, threadID string, generation uint64, failedCapture *subscriptionCaptureRollback) {
	s.projectionMu.Lock()
	if failedCapture != nil {
		s.subs.restoreContent(conn.id, threadID, generation, *failedCapture)
	}
	content := s.subs.content(conn.id, threadID)
	records, ok := s.subs.Release(conn.id, threadID, generation)
	s.deliveryMu.Lock()
	s.projectionMu.Unlock()
	disconnected := false
	if ok {
		for _, record := range records {
			if !conn.enqueueHistory(record, content) {
				disconnected = true
				break
			}
		}
	}
	s.deliveryMu.Unlock()
	if disconnected {
		s.evictSlowConsumer(conn, "a hydration release replay")
	}
}

func (s *Server) withdrawHydration(
	conn *Connection,
	threadID string,
	generation uint64,
	rollback subscriptionCaptureRollback,
) {
	s.projectionMu.Lock()
	s.deliveryMu.Lock()
	s.subs.withdrawBuffered(conn.id, threadID, generation, rollback)
	s.deliveryMu.Unlock()
	s.projectionMu.Unlock()
}
