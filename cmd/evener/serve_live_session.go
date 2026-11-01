package main

import (
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
)

// liveSession owns which session the daemon currently serves and who decides
// how the process exits. Shutdown, retirement and thread/clear all act on it,
// so the rules that keep them from closing a session twice or never live here.
type liveSession struct {
	mu sync.RWMutex
	// transitionMu serializes shutdown's ownership claim with a clear's final
	// identity swap. It remains held through the old session's drain and
	// identity projection; mu is held only for the brief ownership check.
	transitionMu sync.Mutex
	sess         *agent.Session
	// env tracks the CURRENT session's execution environment (each session
	// owns its own). thread/clear reads it to inherit the live sandbox and swaps it
	// alongside sess, so a cleared session's sandbox reflects what the running
	// session actually enforces (on resume the persisted mode, not the launch flag).
	env *execenv.LocalExecutionEnvironment
	// closed records that shutdown has already made its ONE closing
	// pass over the live session, so no session installed after it is covered by
	// that pass. It is monotonic and it is written inside the same mu
	// hold that reads the session to close, which is what makes the pass and the
	// swap one decision instead of two racing ones.
	closed bool
	// exitPolicy records who owns the process exit: "" while undecided,
	// "shutdown" once closeForShutdown claims it, "retirement" once a
	// retirement consumer reserves it. First writer wins; the loser joins the
	// winner's exit instead of making a second closing pass.
	exitPolicy string
}

func newLiveSession(sess *agent.Session, env *execenv.LocalExecutionEnvironment) *liveSession {
	return &liveSession{sess: sess, env: env}
}

func (l *liveSession) session() *agent.Session {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sess
}

func (l *liveSession) environment() *execenv.LocalExecutionEnvironment {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.env
}

func (l *liveSession) set(next *agent.Session, nextEnv *execenv.LocalExecutionEnvironment) {
	l.mu.Lock()
	l.sess = next
	l.env = nextEnv
	l.mu.Unlock()
}

// closeForShutdown is shutdown's single pass. Marking inside the SAME hold
// that reads the session is the whole point: a swap that lands after this
// critical section cannot observe the pass as still pending, so it knows it
// owns its replacement's teardown.
func (l *liveSession) closeForShutdown() {
	l.transitionMu.Lock()
	defer l.transitionMu.Unlock()
	l.mu.Lock()
	l.closed = true
	live := l.sess
	owned := false
	if l.exitPolicy == "" {
		l.exitPolicy = "shutdown"
		owned = true
	}
	l.mu.Unlock()
	if !owned {
		// Retirement's consumer reserved the exit and already released
		// the root; this pass is the ctx.Done() tail of that same exit
		// and must not close the released session a second time.
		return
	}
	live.CloseForShutdown()
}

// shutdownClosed reports whether that pass has already run.
// Read it only AFTER the session in question is the current one, which is
// what makes a negative answer a promise rather than a guess: the pass reads
// sess under the same lock, so a pass still to come will find that
// session and close it.
func (l *liveSession) shutdownClosed() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.closed
}

// reserveRetirementExit hands the process exit to the retirement consumer
// exactly once: it refuses when shutdown already owns the exit, when the
// shutdown pass already ran, or when the published root moved under the
// claim (a thread/clear replacement is not the prepared tree).
func (l *liveSession) reserveRetirementExit(root *agent.Session) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.exitPolicy != "" || l.closed || l.sess != root {
		return false
	}
	l.exitPolicy = "retirement"
	return true
}

// releaseRetirementExit drops the reservation reserveRetirementExit took when
// the retirement commit fails, so the exit is genuinely undecided again.
// Without this, closeForShutdown sees a policy already set and returns early
// forever, leaking the live session's open delegates, running jobs, worktree
// locks and transcript flushes.
func (l *liveSession) releaseRetirementExit() {
	l.mu.Lock()
	l.exitPolicy = ""
	l.mu.Unlock()
}
