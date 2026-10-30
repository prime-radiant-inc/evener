package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
)

// The orphaned-delegate reclaim arm (#5471). A delegate lane's evener:dlg:
// lock belongs to its parent's disposal lifecycle: only the parent's
// dispose call or clean close releases it, and prune skips every locked
// lane owner-independently. When the parent session dies without clean
// closing — a crash, a kill — that lifecycle never runs and the lock can
// never be released by anyone: 79 such lanes held 35 GB on one host, each
// reducible only by a manual per-delegate dispose. This arm is the
// operator-directed exception: prune runs it only when the caller passes
// orphaned_delegate_days, and it releases exactly the locks whose parent
// provably cannot act — the parent's own store says the delegate is not
// resumable, or the parent session is gone or has been inactive past the
// operator's threshold. Everything else about the lane still flows through
// the ordinary prune rungs: live work under it keeps it, and only an
// unchanged or merged lane is removed.

// orphanedDelegateMarker reports whether a lock reason is a delegate marker
// whose parent can no longer claim the lane, returning the parsed marker when
// it is. The parent must be another session (this session's own delegates go
// through dispose, which can still act). The parent's transcript is checked
// first: a parent whose session directory is gone, or whose transcript has
// been inactive at least minInactivity (the activity signal the session
// listings read), cannot act as the disposer by definition, and settling that
// costs one stat. The parent's delegates store is consulted only for a parent
// that is still active, where the store is the remaining authority: it proves
// the delegate reclaimable exactly when the parent's own disposal lifecycle
// already ran — the delegate is not resumable — even while the session lives.
// Unreadable state keeps the lane locked: the arm fails safe, and a resumed
// delegate is never stranded — an unlocked lane re-locks on revival
// (lockstate's EvDelegateRevive row), and a removed lane fails revival loudly
// into the construction-failed closure.
//
// memo caches one folded store state per parent session for the sweep pass,
// so many lanes of one parent fold that parent's journal once instead of once
// per lane.
func (s *Session) orphanedDelegateMarker(ctx context.Context, reason string, minInactivity time.Duration, now time.Time, memo map[string]delegatestore.State) (worktree.Marker, bool) {
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID == "" || m.SessionID == "" || m.SessionID == s.id {
		return worktree.Marker{}, false
	}
	// The cheap activity signal first: a parent whose session directory is
	// gone, or whose transcript has been inactive past the operator's
	// threshold, cannot act as the disposer.
	if info, err := os.Stat(schema.SessionTranscriptPath(s.stateDir, m.SessionID)); err == nil {
		if now.Sub(info.ModTime()) >= minInactivity {
			return m, true
		}
	} else if os.IsNotExist(err) {
		return m, true
	}
	// A parent that is still active, or whose activity could not be read: the
	// parent's delegates store is the remaining authority.
	return m, s.parentStoreClosedDelegate(ctx, m.SessionID, m.DelegateID, memo)
}

// parentStoreClosedDelegate reports whether the parent session's delegates
// store has durably closed the delegate's resumability — the parent's own
// disposal lifecycle already ran, and only the lane's lock lagged behind it.
// memo caches one folded store state per parent session; only a successful
// fold is memoized. Any read or fold failure is not a death verdict: the lane
// stays locked.
func (s *Session) parentStoreClosedDelegate(ctx context.Context, parentSID, delegateID string, memo map[string]delegatestore.State) bool {
	state, ok := memo[parentSID]
	if !ok {
		events, _, err := delegatestore.ScanEvents(ctx, delegateJournalPath(s.stateDir, parentSID), delegatestore.ScanLimits{})
		if err != nil {
			return false
		}
		state, err = delegatestore.Fold(events)
		if err != nil {
			return false
		}
		memo[parentSID] = state
	}
	agg, exists := state[delegateID]
	return exists && !agg.Resumable
}

// delegateJournalPath is the per-session delegates journal the doctor tools
// read: <stateDir>/sessions/<sid>/delegates.jsonl.
func delegateJournalPath(stateDir, sessionID string) string {
	return filepath.Join(jobsDir(stateDir, sessionID), "delegates.jsonl")
}

// isExpectedDelegateMarker reports whether a lock file's contents are the
// same delegate marker the sweep planned around. The comparison is
// semantic (parsed delegate and session ids), not byte equality: git writes
// the lock file with a trailing newline the porcelain listing strips.
func isExpectedDelegateMarker(lockFileContents []byte, expectedDlg, expectedSID string) bool {
	m, ok := worktree.ParseMarker(strings.TrimSpace(string(lockFileContents)))
	return ok && m.DelegateID != "" && m.DelegateID == expectedDlg && m.SessionID == expectedSID
}

// releaseOrphanedDelegateMarker releases the lock on a lane iff it still
// carries exactly the delegate marker the sweep planned around, reporting
// whether it did. The claim-rename-verify-restore protocol is the shared
// releaseClaimedLock's, for the same reason git's own unlock is not usable
// here: see releaseStrandedOwnMarker. The control policy's write authority
// applies exactly as it does to the sandboxed git calls that follow.
func (s *Session) releaseOrphanedDelegateMarker(files execenv.FileMutator, lanePath string, expected worktree.Marker) (released bool, err error) {
	if s.occupiesManagedLane(lanePath) {
		return false, nil
	}
	return s.releaseClaimedLock(files, lanePath, func(contents []byte) bool {
		return isExpectedDelegateMarker(contents, expected.DelegateID, expected.SessionID)
	}, "locked.orphan-release")
}

// restoreOrphanedDelegateMarker puts the marker back after a release whose
// removal did not happen, so a lane this pass could not collect is exactly as
// protected as it was rather than open to another session's sweep. The shared
// restoreLockMarker never displaces a lock taken in the meantime.
func (s *Session) restoreOrphanedDelegateMarker(lanePath, expectedReason string) {
	s.restoreLockMarker(lanePath, expectedReason, "orphaned delegate")
}
