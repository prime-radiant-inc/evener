package agent

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/events"
)

// envWorkID handles one admission in envWork, so its label can be dropped
// again when the work returns.
type envWorkID uint64

type envWorkRecord struct {
	label   string
	release func()
}

// registerEnvWorkLocked records an admission described by label and returns its
// handle. The caller holds s.mu and has already established that the session is
// not closing — that pairing is the whole point (see beginEnvWork).
func (s *Session) registerEnvWorkLocked(label string, release func()) envWorkID {
	s.envWorkSeq++
	id := envWorkID(s.envWorkSeq)
	if s.envWork == nil {
		s.envWork = make(map[envWorkID]envWorkRecord)
	}
	if len(s.envWork) == 0 {
		s.envWorkDrained = make(chan struct{})
	}
	s.envWork[id] = envWorkRecord{label: label, release: release}
	return id
}

// beginEnvWork admits work that runs commands on the session's execution
// environment and must therefore finish before Close reaps that environment's
// process table: a whole manage_worktree call at its dispatch, the cut of a
// delegate's isolation lane and the rollback that undoes it, and the rollback a
// refused or failed operation owes after its swap has already released the
// swap's own admission. It is the beginDispose idiom again — the closing check
// AND the admission happen under one s.mu hold, so a successful admission
// happens-before Close()'s join. A true return MUST be paired with a (deferred)
// endEnvWork().
//
// label says what the work is, in the terms a human reading a shutdown warning
// would want: it is what the close names if its bounded join gives up on this
// admission.
//
// swapEnvAndRefresh calls registerEnvWorkLocked directly rather than this,
// because it needs the environment it is adopting from out of the same lock
// hold that reads `closing`; it is the same admission Close() joins.
//
// A false return means the close was already under way when this work began.
// There is nothing left to fence it against — the environment it would run on
// is already being torn down, and the close may already be past its join — and
// the two kinds of caller answer that differently, on purpose:
//
//   - The manage_worktree DISPATCH refuses the call, and a delegate's
//     isolation step refuses the spawn (errWorktreeOpWhileClosing). The work
//     has not started, and running it unfenced would lock lanes and write
//     sidecars against an environment being reaped and stores being closed. A
//     closing session was never going to complete it anyway.
//   - A ROLLBACK proceeds best-effort, exactly as it did before the fence
//     existed. Its admission failing is the expected case — the close is what
//     made the rollback necessary — and it is an undo the session already owes,
//     so skipping it would leave the residue the rollback exists to prevent.
//     Better unfenced cleanup than none.
func (s *Session) beginEnvWork(label string) (envWorkID, bool) {
	release, err := s.beginRetirementMutation("environment")
	if err != nil {
		return 0, false
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		release()
		return 0, false
	}
	id := s.registerEnvWorkLocked(label, release)
	s.mu.Unlock()
	return id, true
}

// relabelEnvWork renames a live admission. An operation's admission is taken
// before the work it will eventually have to undo, so the label that is honest
// during the operation is not the one that is honest once its rollback starts;
// the rollback renames itself as it begins. A handle no longer live (the work
// returned) renames nothing.
func (s *Session) relabelEnvWork(id envWorkID, label string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if work, live := s.envWork[id]; live {
		work.label = label
		s.envWork[id] = work
	}
}

// endEnvWork releases an admission obtained from beginEnvWork(). Ending a
// handle that already ended releases nothing, so it cannot end the join early
// while another admission is still live.
func (s *Session) endEnvWork(id envWorkID) {
	s.mu.Lock()
	work, live := s.envWork[id]
	if live {
		delete(s.envWork, id)
		if len(s.envWork) == 0 {
			close(s.envWorkDrained)
			s.envWorkDrained = nil
		}
	}
	s.mu.Unlock()
	if work.release != nil {
		work.release()
	}
}

// outstandingEnvWork lists the labels of every admission still in flight,
// ordered so a warning reads the same way twice.
func (s *Session) outstandingEnvWork() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	labels := make([]string, 0, len(s.envWork))
	for _, work := range s.envWork {
		labels = append(labels, work.label)
	}
	sort.Strings(labels)
	return labels
}

// joinEnvWorkWithinCloseBudget waits for every admitted environment work item
// before the close touches anything that work is still using — the delegate
// tree, this session's lanes and locks, the stores recording them, and finally
// the environment's process table — and says what it walked past when the
// shared close budget expires first.
//
// The four kinds of admission reach this join by different routes:
//
//   - A whole OPERATION, admitted at the manage_worktree dispatch, runs its git
//     on the request context of the tool call. A close cancels the turn context
//     that call descends from, so it stops of its own accord — but only the
//     operations that reach a cancellation point do, and an operation that has
//     already committed to a git command finishes it.
//   - A delegate's LANE CREATE, admitted in prepareIsolation, runs its git on
//     the request context of the spawn, which descends from that same turn
//     context: it stops exactly the way an operation does, and likewise
//     finishes a git command it has already committed to.
//   - A SWAP's refresh runs under the session's own context, which this close
//     cancelled in its step 2, well before reaching here. It stops on its own.
//   - A ROLLBACK (worktreeCleanupRun) is DETACHED on purpose: it runs on
//     context.Background() with a close-cascade budget of its own, because the
//     close that refused the swap — or the spawn whose lane it is taking back —
//     has already cancelled the request context the op's runner was bound to,
//     and a rollback through that would fail silently. Cancelling the close
//     cannot shorten it.
//
// So this bound is not a restatement of the rollback's bound: the close budget
// is one close-cascade budget minted when the close began and partly spent by
// the time it gets here, while a rollback's is a full close-cascade budget
// starting later. The join can therefore always expire first. That is accepted
// rather than fixed by waiting longer, for two reasons. The cascade budget
// (spec §P0) exists so a whole close is bounded, and this join is a participant
// in it, not an exception: giving rollback admissions their own full budget
// would let one refused create roughly double a shutdown, which is the
// unbounded-fence failure in slower motion. And when time is left the gap
// rarely bites — a rollback of a just-created, still-empty lane is three git
// commands and a sidecar unlink, and only approaches its ceiling when git is
// already wedged, which is exactly the case the bound exists for.
//
// Sometimes there is no time left at all, and then a perfectly healthy rollback
// is walked past: a close whose delegate-tree stop was hopeless calls
// cancelBudget() outright, so the join below is zero-length by the time it
// runs. That is the deliberate order of a shutdown that has already given up on
// one subtree — but it is why the warning matters, and why it must name a real
// admission rather than fire on every such close.
//
// If the two budgets ever do need reconciling, the move is to shorten the
// ROLLBACK's (it cleans up an empty lane; it is not a disposal pass) rather
// than lengthen the close — a budget change, and its own decision, not
// something to smuggle in here.
//
// Walking past is the lesser failure either way. But the cleanup below is about
// to reap the process table under whatever is still running, which must not
// happen silently, so the warning names it.
func (s *Session) joinEnvWorkWithinCloseBudget(ctx context.Context) {
	s.mu.Lock()
	drained := s.envWorkDrained
	s.mu.Unlock()
	if drained == nil {
		return
	}
	// testOnly seam: see testConfig.closeAwaitingEnvWork. Nil in production.
	// Only an endEnvWork of the last live admission closes drained, and the
	// budget is not yet spent, so the select below blocks.
	if observe := s.cfg.testOnly.closeAwaitingEnvWork; observe != nil && ctx.Err() == nil {
		observe()
	}
	select {
	case <-drained:
		return
	case <-ctx.Done():
	}
	// The budget is spent. Warn only about work that is actually still there:
	// a close reaches this join with an already-expired budget whenever the
	// delegate-tree stop cancelled the cascade outright, and one that also
	// admitted nothing has nothing the cleanup below can run under. An empty
	// list is likewise what a drain that won the race by a hair leaves — both
	// are silence, not a warning naming nobody.
	outstanding := s.outstandingEnvWork()
	if len(outstanding) == 0 {
		return
	}
	s.emit(events.WarningData{Message: fmt.Sprintf(
		"close budget expired with environment work still in flight (%s); the environment is cleaned under it",
		strings.Join(outstanding, "; "))})
}
