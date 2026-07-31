package agent

import (
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"
)

// These tests enforce the rule stated on EnvelopeSampling
// (session_envelope_sampling.go): a sample the daemon's authoritative event
// consumer takes runs on the drain goroutine, so it may block on Session.mu and
// on nothing else in this package.
//
// Session.mu is exempt because the emit path re-acquires it, which makes
// "held across an emit" a self-deadlock rather than a convention
// (session_emit_lock_guard_test.go). Every other mutex here is treated as
// forbidden whether or not it is held across an emit TODAY — four of them are
// (responseSideEffectsMu, queueEventsMu, queuePersistMu, subagent.mu), and for
// the rest nothing would announce it if a future emit moved inside their
// critical section. A sampler that takes one is one refactor away from wedging
// the daemon, and the refactor would be in a different file from the sampler.
//
// Boundary, stated rather than implied: these tests classify sync.Mutex and
// sync.RWMutex fields. A sync.Once whose body emitted would be the same hazard
// through a different primitive (taskStoreOnce and goalStoreOnce are both
// reachable from a sampler); the cb1k audit verified neither body emits, and
// there is no way to park inside a Once from outside it, so that half is
// carried by that audit rather than by this file.

// samplerBudget bounds one sampling call. A healthy sample reads memory, or at
// worst one small file, so this is a wedge detector with four orders of
// magnitude of headroom rather than a latency assertion. It bounds how long a
// FAILING run holds a goroutine; it never decides whether a value was produced.
const samplerBudget = 10 * time.Second

// envelopeSamplingLock is one lock an envelope sample must not block on, with a
// way to hold it. owner/field name the struct field so the classification test
// can prove the table still covers every mutex those structs declare.
type envelopeSamplingLock struct {
	owner string
	field string
	hold  func(*Session) (release func())
}

func (l envelopeSamplingLock) String() string { return l.owner + "." + l.field }

// envelopeSamplingForbiddenLocks is every agent mutex an envelope sample must
// never block on. eventsMu is held as a READER because that is what the emit
// path holds at the blocking send; a sampler taking it for reading is harmless,
// one taking it for writing wedges exactly like the rest.
var envelopeSamplingForbiddenLocks = []envelopeSamplingLock{
	{owner: "Session", field: "eventsMu", hold: func(s *Session) func() {
		s.eventsMu.RLock()
		return s.eventsMu.RUnlock
	}},
	{owner: "Session", field: "responseSideEffectsMu", hold: func(s *Session) func() {
		s.responseSideEffectsMu.Lock()
		return s.responseSideEffectsMu.Unlock
	}},
	{owner: "Session", field: "queueEventsMu", hold: func(s *Session) func() {
		s.queueEventsMu.Lock()
		return s.queueEventsMu.Unlock
	}},
	{owner: "Session", field: "queuePersistMu", hold: func(s *Session) func() {
		s.queuePersistMu.Lock()
		return s.queuePersistMu.Unlock
	}},
	{owner: "Session", field: "clientMutationsInitMu", hold: func(s *Session) func() {
		s.clientMutationsInitMu.Lock()
		return s.clientMutationsInitMu.Unlock
	}},
	{owner: "Session", field: "pendingJobNotifsMu", hold: func(s *Session) func() {
		s.pendingJobNotifsMu.Lock()
		return s.pendingJobNotifsMu.Unlock
	}},
	{owner: "Session", field: "readFilesMu", hold: func(s *Session) func() {
		s.readFilesMu.Lock()
		return s.readFilesMu.Unlock
	}},
	{owner: "subagent", field: "mu", hold: func(s *Session) func() {
		sub := &subagent{id: "envelope-sampling-probe", done: make(chan struct{})}
		s.subagents.track(sub)
		sub.mu.Lock()
		return func() {
			sub.mu.Unlock()
			s.subagents.mu.Lock()
			delete(s.subagents.subs, sub.id)
			s.subagents.mu.Unlock()
		}
	}},
}

// envelopeSamplingPermittedLocks are the locks a sample MAY block on, each with
// the reason it is safe. Nothing belongs here without a structural argument;
// "no emit is inside it today" is not one.
var envelopeSamplingPermittedLocks = map[string]string{
	"Session.mu": "the emit path re-acquires it (emit -> activeCausalProvenance), " +
		"so holding it across an emit self-deadlocks on the first execution",
}

// TestEnvelopeSamplingNeverBlocksOnAnEmitHeldLock runs every method the daemon's
// sampling surface declares while each forbidden lock is held, and requires them
// all to return.
//
// A facet author adding a value to EnvelopeSampling gets this coverage with no
// test edit: the method set is read off the interface type, so a method added
// tomorrow is exercised tomorrow.
func TestEnvelopeSamplingNeverBlocksOnAnEmitHeldLock(t *testing.T) {
	sess := newTestSession(t)
	if sess.subagents == nil {
		t.Fatal("test session has no subagent manager; the subagent.mu case cannot be set up")
	}

	methods := envelopeSamplingMethodNames()
	if len(methods) == 0 {
		t.Fatal("EnvelopeSampling declares no methods; this test has stopped checking anything")
	}

	for _, lock := range envelopeSamplingForbiddenLocks {
		func() {
			release := lock.hold(sess)
			defer release()
			for _, method := range methods {
				if !callSamplerUnderHeldLock(t, sess, method, lock.String()) {
					// The next method would just spend another budget parking on
					// the same lock; one wedge names the problem.
					return
				}
			}
		}()
	}
}

// TestEverySamplingRelevantMutexIsClassified is the completeness half. The test
// above can only check the locks the table names, so a mutex added to Session or
// subagent must be classified as permitted or forbidden before it can be
// silently uncovered.
func TestEverySamplingRelevantMutexIsClassified(t *testing.T) {
	forbidden := map[string]bool{}
	for _, lock := range envelopeSamplingForbiddenLocks {
		forbidden[lock.String()] = true
	}

	owners := []struct {
		name string
		typ  reflect.Type
	}{
		{"Session", reflect.TypeFor[Session]()},
		{"subagent", reflect.TypeFor[subagent]()},
	}

	declared := map[string]bool{}
	for _, owner := range owners {
		for _, field := range mutexFieldNames(owner.typ) {
			key := owner.name + "." + field
			declared[key] = true
			if forbidden[key] {
				continue
			}
			if _, ok := envelopeSamplingPermittedLocks[key]; ok {
				continue
			}
			t.Errorf("%s is a mutex no envelope-sampling rule classifies: add it to "+
				"envelopeSamplingForbiddenLocks, or to envelopeSamplingPermittedLocks with the "+
				"structural reason a sample may block on it", key)
		}
	}

	for key := range forbidden {
		if !declared[key] {
			t.Errorf("envelopeSamplingForbiddenLocks names %s, which is not a mutex field on that "+
				"struct any more: the table has drifted and is holding nothing", key)
		}
	}
	for key := range envelopeSamplingPermittedLocks {
		if !declared[key] {
			t.Errorf("envelopeSamplingPermittedLocks names %s, which is not a mutex field on that "+
				"struct any more: the exemption is stale", key)
		}
	}
}

// callSamplerUnderHeldLock invokes one sampling method and reports whether it
// returned inside the budget. It never touches t from the sampling goroutine, so
// a wedged sampler that finishes after the test cannot log into a finished test.
func callSamplerUnderHeldLock(t *testing.T, sess *Session, method, lockName string) bool {
	t.Helper()
	type outcome struct{ panicked any }
	done := make(chan outcome, 1)
	go func() {
		var out outcome
		defer func() {
			out.panicked = recover()
			done <- out
		}()
		callEnvelopeSamplingMethod(sess, method)
	}()

	select {
	case out := <-done:
		if out.panicked != nil {
			t.Errorf("EnvelopeSampling.%s panicked while %s was held: %v", method, lockName, out.panicked)
		}
		return true
	case <-time.After(samplerBudget):
		t.Errorf("EnvelopeSampling.%s did not return within %s while %s was held.\n\n"+
			"That is the daemon wedge: this method runs on the authoritative consumer's drain "+
			"goroutine, so a sample that waits on a lock an emitter holds stops the drain, fills "+
			"the event buffer, blocks the emitter inside that same critical section, and leaves "+
			"the session unclosable (the blocked send holds eventsMu.RLock; Close needs "+
			"eventsMu.Lock). Sample a value the session already published, or move the read out "+
			"from under %s.\n\n%s", method, samplerBudget, lockName, lockName, goroutineDump())
		return false
	}
}

// envelopeSamplingMethodNames reads the sampling surface off the interface type
// rather than a list, so the coverage tracks the contract.
func envelopeSamplingMethodNames() []string {
	iface := reflect.TypeFor[EnvelopeSampling]()
	names := make([]string, 0, iface.NumMethod())
	for i := range iface.NumMethod() {
		names = append(names, iface.Method(i).Name)
	}
	return names
}

// callEnvelopeSamplingMethod calls method on sess with zero-valued arguments.
// Every method on the interface is a read, so the arguments only have to be
// well-typed; what is under test is whether the call returns at all.
func callEnvelopeSamplingMethod(sess *Session, method string) {
	fn := reflect.ValueOf(sess).MethodByName(method)
	args := make([]reflect.Value, fn.Type().NumIn())
	for i := range args {
		args[i] = reflect.New(fn.Type().In(i)).Elem()
	}
	if fn.Type().IsVariadic() {
		fn.CallSlice(args)
		return
	}
	fn.Call(args)
}

func mutexFieldNames(typ reflect.Type) []string {
	mutex := reflect.TypeFor[sync.Mutex]()
	rwMutex := reflect.TypeFor[sync.RWMutex]()
	var names []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		if field.Type == mutex || field.Type == rwMutex {
			names = append(names, field.Name)
		}
	}
	return names
}

// goroutineDump is the evidence a wedge failure needs: the parked goroutine's
// stack names the exact mutex and the exact frame that reached it.
//
// It grows until the whole dump fits. runtime.Stack TRUNCATES at the buffer it
// is given and reports only what it wrote, so a fixed buffer silently drops the
// tail — which in a full-package run is most of the goroutines, including the
// one being looked for. A caller searching a truncated dump concludes the
// goroutine is not there.
func goroutineDump() string {
	for size := 1 << 20; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size {
			return string(buf[:n])
		}
	}
}
