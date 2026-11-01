package appserver

import (
	"context"

	"primeradiant.com/evener/appwire"
)

type subscriptionAdmission struct {
	requestID string
	threadID  string
	canceled  bool
}

func (c *Connection) beginSubscriptionAdmission(requestID, threadID string) *subscriptionAdmission {
	admission := &subscriptionAdmission{requestID: requestID, threadID: threadID}
	c.admissionMu.Lock()
	if c.pendingAdmissions == nil {
		c.pendingAdmissions = map[*subscriptionAdmission]struct{}{}
	}
	c.pendingAdmissions[admission] = struct{}{}
	c.admissionMu.Unlock()
	return admission
}

func (c *Connection) finishSubscriptionAdmission(admission *subscriptionAdmission) {
	if admission == nil {
		return
	}
	c.admissionMu.Lock()
	delete(c.pendingAdmissions, admission)
	c.admissionMu.Unlock()
}

func (c *Connection) cancelSubscriptionAdmissions(threadID string) {
	c.admissionMu.Lock()
	for admission := range c.pendingAdmissions {
		if admission.threadID == threadID {
			admission.canceled = true
		}
	}
	c.admissionMu.Unlock()
}

func (c *Connection) claimSubscriptionAdmission(requestID string) (*subscriptionAdmission, bool) {
	for admission := range c.pendingAdmissions {
		if admission.requestID == requestID {
			return admission, !admission.canceled
		}
	}
	return nil, true
}

type subscriptionAdmissionFailureContextKey struct{}

type subscriptionAdmissionResolverPanicContextKey struct{}

// CaptureSubscriptionHandoff is the one-shot continuation paired with a
// buffering subscription capture. Commit runs only after the matching response
// enters the connection send queue. Abort runs after that capture has been
// withdrawn because its response cannot be enqueued or a newer lifecycle
// supersedes it.
type CaptureSubscriptionHandoff struct {
	Commit func()
	Abort  func()
}

// unsubscribeTarget is the identity ordered ingress resolved for a
// thread/unsubscribe request, carried to the handler on the context.
type unsubscribeTarget struct {
	// key is the resolved identity. A subscription registered under it is
	// withdrawn with every delivery alias its lifecycle owns.
	key string
	// aliasKey is the second identity of a ref that no longer resolves to a
	// source: admissions under it are canceled with key's, but only the
	// subscription registered under key itself is withdrawn.
	aliasKey string
}

type unsubscribeTargetContextKey struct{}

// Unsubscribe drops the calling connection's subscription to one thread so a
// browser switching views stops receiving its live updates and the relay can
// idle out once no connection remains. Quietly a no-op when there is nothing
// to remove (no connection on the context, an empty threadID, a connection
// already replaced): teardown never needs to distinguish those. Unlike
// Subscribe it takes no projection gate: removing a subscription only
// shrinks delivery.
//
// The identity ordered ingress resolved for the request, when there is one,
// replaces threadID; without it threadID is the raw delivery key the caller
// holds.
func Unsubscribe(ctx context.Context, threadID string) {
	conn, _ := ctx.Value(connectionContextKey{}).(*Connection)
	if conn == nil || threadID == "" {
		return
	}
	target, resolved := ctx.Value(unsubscribeTargetContextKey{}).(unsubscribeTarget)
	if !resolved {
		target = unsubscribeTarget{key: threadID}
	}
	conn.cancelSubscriptionAdmissions(target.key)
	if target.aliasKey != "" {
		conn.cancelSubscriptionAdmissions(target.aliasKey)
	}
	server := conn.server
	server.mu.Lock()
	defer server.mu.Unlock()
	if server.conns[conn.id] != conn {
		return
	}
	if resolved && target.aliasKey == "" {
		server.subs.UnsubscribeLifecycle(conn.id, target.key)
		return
	}
	server.subs.Unsubscribe(conn.id, target.key)
}

// SubscribeWithAdmission installs an immediate subscription and consumes the
// request's admission at one boundary. Registration gates precede admissionMu,
// matching capture; unsubscribe cannot pass eligibility before installation.
// History reads retain a buffering capture until their response is enqueued,
// including sources that do not supply an upstream read handoff.
// An optional expectedLifecycle is the identity already resolved by the handler.
// A mismatch rejects without consuming admission or changing subscriptions.
func SubscribeWithAdmission(ctx context.Context, threadID string, replace bool, expectedLifecycle ...string) error {
	conn, _ := ctx.Value(connectionContextKey{}).(*Connection)
	if conn == nil {
		return nil
	}
	if threadID == "" {
		return context.Canceled
	}
	server := conn.server
	if _, history := ctx.Value(historyContentContextKey{}).(historyContentRequest); history {
		var resolve []func() SubscriptionTarget
		if len(expectedLifecycle) != 0 {
			resolve = append(resolve, func() SubscriptionTarget {
				return SubscriptionTarget{ThreadID: threadID, LifecycleKey: expectedLifecycle[0]}
			})
		}
		if !CaptureSubscriptionWithHandoff(ctx, replace, func() string { return threadID },
			func() uint64 { return server.routedSeq }, func() bool { return true }, CaptureSubscriptionHandoff{}, resolve...) {
			return appwire.SessionUnavailable("thread subscription is unavailable")
		}
		return nil
	}
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	conn.hydrationMu.Lock()
	defer conn.hydrationMu.Unlock()
	server.projectionMu.Lock()
	defer server.projectionMu.Unlock()
	server.deliveryMu.Lock()
	defer server.deliveryMu.Unlock()
	conn.admissionMu.Lock()
	defer conn.admissionMu.Unlock()
	responseID, _ := ctx.Value(requestIDContextKey{}).(string)
	admission, allowed := conn.claimSubscriptionAdmission(responseID)
	if !allowed || (admission != nil && len(expectedLifecycle) != 0 && admission.threadID != expectedLifecycle[0]) {
		return appwire.SessionUnavailable("thread subscription is unavailable")
	}
	server.mu.RLock()
	defer server.mu.RUnlock()
	if server.conns[conn.id] != conn {
		return context.Canceled
	}
	owner := threadID
	if admission != nil {
		owner = admission.threadID
	}
	if conn.deferHistorySubscription(ctx, responseID, threadID, replace) {
		return appwire.SessionUnavailable("history subscription is awaiting its response")
	}
	content := subscriptionHistoryContent(ctx, server.subs, conn.id, threadID)
	server.subs.subscribeOwned(conn.id, threadID, owner, replace)
	server.subs.setContent(conn.id, threadID, 0, content)
	if admission != nil {
		delete(conn.pendingAdmissions, admission)
	}
	return nil
}

// SubscriptionTarget is one resolved delivery/lifecycle pair. Capture resolves
// it under the projection and delivery gates, before taking admissionMu.
// LifecycleKey is compared with ingress admission ownership, never substituted
// for the delivery key or used to retarget a pending admission.
type SubscriptionTarget struct {
	ThreadID     string
	LifecycleKey string
}

// CaptureSubscription registers a buffering hydration generation, captures the
// authoritative snapshot under the projection gate, and arranges to release
// only post-cut records after the matching response enters the connection's
// send queue. A false snapshot result restores the previous ownership.
// When supplied, resolveTarget replaces threadID and atomically resolves D+A1
// under the registration gates, before admission validation or hydration changes.
// currentSequence and snapshot run while the projection and delivery gates are
// held, so they must be cheap and must not acquire a lock ordered before them.
func CaptureSubscription(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		CaptureSubscriptionHandoff{},
		true,
		resolveTarget...,
	)
}

// CaptureSubscriptionWithHandoff extends CaptureSubscription with the narrow
// downstream response boundary required by a materialized upstream handoff.
// It is deliberately specific to hydration rather than a general response
// transaction mechanism.
func CaptureSubscriptionWithHandoff(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	handoff CaptureSubscriptionHandoff,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	return captureSubscription(
		ctx,
		replace,
		threadID,
		currentSequence,
		snapshot,
		handoff,
		false,
		resolveTarget...,
	)
}

func captureSubscription(
	ctx context.Context,
	replace bool,
	threadID func() string,
	currentSequence func() uint64,
	snapshot func() bool,
	handoff CaptureSubscriptionHandoff,
	releaseOnErrorResponse bool,
	resolveTarget ...func() SubscriptionTarget,
) bool {
	conn, ok := ctx.Value(connectionContextKey{}).(*Connection)
	if !ok || conn == nil {
		if handoff.Commit == nil && handoff.Abort == nil {
			if len(resolveTarget) != 0 && resolveTarget[0]().ThreadID == "" {
				return false
			}
			return snapshot()
		}
		if handoff.Abort != nil {
			handoff.Abort()
		}
		return false
	}
	server := conn.server
	if server.beforeSubscriptionRegistration != nil {
		server.beforeSubscriptionRegistration()
	}
	conn.hydrationMu.Lock()
	server.projectionMu.Lock()
	server.deliveryMu.Lock()
	var superseded []*hydrationResponseFinalizer
	abortAfterUnlock := func() bool {
		server.deliveryMu.Unlock()
		server.projectionMu.Unlock()
		conn.hydrationMu.Unlock()
		for _, finalizer := range superseded {
			finalizer.abortAfterWithdrawal()
		}
		if handoff.Abort != nil {
			handoff.Abort()
		}
		return false
	}

	server.mu.RLock()
	registered := server.conns[conn.id] == conn
	server.mu.RUnlock()
	if !registered {
		return abortAfterUnlock()
	}
	var target SubscriptionTarget
	if len(resolveTarget) != 0 {
		target = resolveTarget[0]()
	} else {
		target.ThreadID = threadID()
	}
	targetThreadID := target.ThreadID
	if targetThreadID == "" {
		return abortAfterUnlock()
	}
	responseID, _ := ctx.Value(requestIDContextKey{}).(string)
	conn.admissionMu.Lock()
	admission, allowed := conn.claimSubscriptionAdmission(responseID)
	if admission != nil && (!allowed || (len(resolveTarget) != 0 && admission.threadID != target.LifecycleKey)) {
		conn.admissionMu.Unlock()
		return abortAfterUnlock()
	}
	contentRequest, history := ctx.Value(historyContentContextKey{}).(historyContentRequest)
	if history && contentRequest.preserve && conn.deferHistorySubscription(ctx, responseID, targetThreadID, replace) {
		conn.admissionMu.Unlock()
		return abortAfterUnlock()
	}
	if conn.server.beforeSubscriptionBeginBuffered != nil {
		conn.server.beforeSubscriptionBeginBuffered()
	}
	superseded = conn.takeSupersededHydrations(responseID, targetThreadID, replace)
	for _, finalizer := range superseded {
		server.subs.withdrawBuffered(
			conn.id,
			finalizer.threadID,
			finalizer.generation,
			finalizer.rollback,
		)
	}
	server.nextHydrationGeneration++
	generation := server.nextHydrationGeneration
	owner := targetThreadID
	if admission != nil {
		owner = admission.threadID
	}
	content := subscriptionHistoryContent(ctx, server.subs, conn.id, targetThreadID)
	rollback := server.subs.beginBuffered(conn.id, targetThreadID, replace, generation, owner)
	server.subs.setContent(conn.id, targetThreadID, generation, content)
	if admission != nil {
		delete(conn.pendingAdmissions, admission)
	}
	conn.admissionMu.Unlock()
	if !snapshot() {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	if !server.subs.SetCut(conn.id, targetThreadID, generation, currentSequence()) {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	if ctx.Err() != nil {
		server.subs.withdrawBuffered(conn.id, targetThreadID, generation, rollback)
		return abortAfterUnlock()
	}
	var historyDone chan struct{}
	if history {
		historyDone = make(chan struct{})
	}
	conn.addHydration(&hydrationResponseFinalizer{
		server:                 server,
		conn:                   conn,
		responseID:             responseID,
		threadID:               targetThreadID,
		generation:             generation,
		replace:                replace,
		rollback:               rollback,
		handoff:                handoff,
		releaseOnErrorResponse: releaseOnErrorResponse,
		historyDone:            historyDone,
	})
	server.deliveryMu.Unlock()
	server.projectionMu.Unlock()
	conn.hydrationMu.Unlock()
	for _, finalizer := range superseded {
		finalizer.abortAfterWithdrawal()
	}
	return true
}
