package server

import (
	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// stubThreadEnvelopeSource is a test's stand-in for the live session behind the
// thread envelope. It is the production seam (ThreadEnvelopeSource), not a mock
// of anything internal: cmd/evener installs one over agent.Session, and a test
// installs this one, so both go through the same sampler and the same store.
//
// Zero value reports the same thing a daemon with nothing to say reports.
type stubThreadEnvelopeSource struct {
	contextPressure  float64
	contextMetrics   ContextMetrics
	detailedStatus   DetailedStatus
	queue            appwire.QueueState
	pendingMutations []appwire.PendingMutation
	tasks            *appwire.TaskAggregate
	taskCalls        int
	metaCalls        int
	// Retained only for older fuzz fixtures that mutate the historical fields.
	// Goal projection reads meta.Goal exclusively.
	goalStatus       string
	goalIterations   int
	goalSet          bool
	workMillis       int64
	usage            *appwire.EvenerUsage
	turnStartedAt    int64
	failedToolCalls  int
	failuresMeasured bool
	askPending       bool
	escalations      []appwire.SandboxEscalationRequested
	reasoningEffort  string
	reasoningLevels  []string
	supportsReason   bool
	visionModel      string
	meta             schema.SessionMeta
	// parkOnMeta runs at the start of SessionMeta(), the last facet
	// refreshFacets samples. Parking there holds a fully-populated sample open.
	parkOnMeta func()
	// parkAfterMeta runs after SessionMeta has copied its return value, allowing a
	// test to hold an old goal sample in flight while a direct carrier commits.
	parkAfterMeta func()
}

func (s *stubThreadEnvelopeSource) ContextPressure() float64       { return s.contextPressure }
func (s *stubThreadEnvelopeSource) ContextMetrics() ContextMetrics { return s.contextMetrics }
func (s *stubThreadEnvelopeSource) DetailedStatus() DetailedStatus { return s.detailedStatus }
func (s *stubThreadEnvelopeSource) TaskAggregate() *appwire.TaskAggregate {
	s.taskCalls++
	return s.tasks
}
func (s *stubThreadEnvelopeSource) AskPending() bool { return s.askPending }

func (s *stubThreadEnvelopeSource) SessionMeta() schema.SessionMeta {
	s.metaCalls++
	if s.parkOnMeta != nil {
		s.parkOnMeta()
	}
	meta := s.meta
	if s.parkAfterMeta != nil {
		s.parkAfterMeta()
	}
	return meta
}

func (s *stubThreadEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return s.queue, s.pendingMutations
}

func (s *stubThreadEnvelopeSource) WorkMetrics() (int64, *appwire.EvenerUsage, int64) {
	return s.workMillis, s.usage, s.turnStartedAt
}

func (s *stubThreadEnvelopeSource) FailedToolCalls() (int, bool) {
	return s.failedToolCalls, s.failuresMeasured
}

func (s *stubThreadEnvelopeSource) PendingEscalations() []appwire.SandboxEscalationRequested {
	return append([]appwire.SandboxEscalationRequested(nil), s.escalations...)
}

func (s *stubThreadEnvelopeSource) ReasoningInfo() (string, []string, bool) {
	return s.reasoningEffort, s.reasoningLevels, s.supportsReason
}

func (s *stubThreadEnvelopeSource) VisionModel() string { return s.visionModel }

// publishEnvelope installs src as the server's envelope source and seeds every
// facet from it, which is what serve.go does at session install.
func publishEnvelope(srv *Server, src *stubThreadEnvelopeSource) *stubThreadEnvelopeSource {
	srv.SetThreadEnvelopeSource(src)
	srv.RefreshThreadEnvelope()
	return src
}

// envelopeSourceFor returns the server's installed stub source, installing an
// empty one first if the test has not. Use it to mutate a value and re-seed,
// mirroring a session whose state moved.
func envelopeSourceFor(srv *Server) *stubThreadEnvelopeSource {
	srv.mu.RLock()
	existing, _ := srv.appEnvelopeSource.(*stubThreadEnvelopeSource)
	srv.mu.RUnlock()
	if existing != nil {
		return existing
	}
	return publishEnvelope(srv, &stubThreadEnvelopeSource{})
}

// setEnvelope mutates the server's envelope source and re-seeds every facet.
func setEnvelope(srv *Server, mutate func(*stubThreadEnvelopeSource)) {
	src := envelopeSourceFor(srv)
	mutate(src)
	srv.RefreshThreadEnvelope()
}

// sessionQueueEnvelopeSource is a stub whose QUEUE facet comes from a real
// agent.Session, for the tests that drive queue mutations through the session's
// own durable client-mutation path and then read them back off the wire.
//
// Only the queue facet is session-backed. The rest stay stub values, so a test
// that asserts on the queue cannot accidentally depend on live session state it
// never set up.
type sessionQueueEnvelopeSource struct {
	stubThreadEnvelopeSource
	sess *agent.Session
}

func (s *sessionQueueEnvelopeSource) ClientMutationProjection() (appwire.QueueState, []appwire.PendingMutation) {
	return s.sess.ClientMutationProjection()
}

// publishSessionQueueEnvelope installs sess as the queue facet's source and
// seeds it. Call refreshEnvelope after mutating the session's queue: the bridge
// does that on QUEUE_CHANGED in production, and a test that drives the session
// directly has to stand in for it.
func publishSessionQueueEnvelope(srv *Server, sess *agent.Session) {
	srv.SetThreadEnvelopeSource(&sessionQueueEnvelopeSource{sess: sess})
	srv.RefreshThreadEnvelope()
}

// materializedQueueDepth reads the queue depth the daemon publishes, the same
// value /drain-as-steer preflights on.
func (s *Server) materializedQueueDepth() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appEnvelope.Queue.Depth
}

// publishFailureCount publishes a measured running failure count, standing in
// for the bridge's refresh of the failures facet. The bridge does this BEFORE
// the commit that projects the event announcing the change, so a test that
// publishes before driving its event reproduces the production ordering.
func publishFailureCount(srv *Server, count int) {
	setEnvelope(srv, func(e *stubThreadEnvelopeSource) {
		e.failedToolCalls = count
		e.failuresMeasured = true
	})
}
