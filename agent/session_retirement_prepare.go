package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
)

// retirementLaneEvidence records one occupied worktree lane a prepared
// retirement must preserve: the exact path, its branch, and the session/delegate
// that owns its lock. It is identity evidence captured at preparation, not a
// handle to take or release.
type retirementLaneEvidence struct {
	sessionID  string
	delegateID string
	path       string
	branch     string
	owner      string
}

// RetirementPreparation is the one-use proof that a preparing claim's runtime
// tree is durably reconstructible. It carries the exact claim, root generation,
// leaf-first resident sessions, verified identities, occupied lanes and the
// tree evidence version. It is not a restored runtime: nothing is closed,
// cancelled, unlocked, hooked, swept or allocated while building it.
type RetirementPreparation struct {
	claim           *RetirementClaim
	root            *Session
	generation      uint64
	sessions        []*Session
	evidenceVersion uint64
	lanes           []retirementLaneEvidence
	released        bool
}

// checkRetirementReady validates this job manager's primary jobs.jsonl through
// the store's own serialization boundary, rereading from the file rather than a
// cached fold. A manager without an open store has no durable obligation.
func (jm *jobManager) checkRetirementReady() error {
	if jm == nil {
		return nil
	}
	jm.mu.Lock()
	store := jm.store
	jm.mu.Unlock()
	if store == nil {
		return nil
	}
	return store.CheckRetirementReady()
}

// Prepare validates that the exact preparing claim's whole runtime tree is
// durably reconstructible before any release is attempted. It never closes a
// session, cancels work, releases a lease, runs a hook, sweeps scratch or
// allocates a lane. On any failure the claim is left intact for Abort.
func (c *RetirementController) Prepare(ctx context.Context, claim *RetirementClaim) (*RetirementPreparation, error) {
	c.mu.Lock()
	if !c.validClaimLocked(claim) {
		c.mu.Unlock()
		return nil, ErrRetirementUnavailable
	}
	root, generation, tree := claim.root, claim.generation, claim.tree
	c.mu.Unlock()

	var (
		blockers []RetirementBlocker
		sessions []*Session
		err      error
	)
	if tree != nil {
		blockers, sessions, err = tree.retirementEvidence()
	} else {
		blockers, err = root.retirementEvidence()
	}
	if err != nil {
		return nil, fmt.Errorf("retirement preparation: runtime evidence: %w", err)
	}
	if len(blockers) != 0 {
		return nil, fmt.Errorf("retirement preparation: %d obligation(s) remain", len(blockers))
	}
	// sessions is leaf-first and excludes the root; validate each resident child
	// before its owner, then the root last, so reconstruction prerequisites are
	// proven bottom-up. No Session lock is held across the I/O inside.
	for _, child := range sessions {
		if err := child.validateRetirementRestore(ctx); err != nil {
			return nil, err
		}
	}
	if err := root.validateRetirementRestore(ctx); err != nil {
		return nil, err
	}
	// Capture occupied-lane identity and lock ownership from the live lock for
	// every resident runtime, leaf-first then root. A lane that is unlocked or
	// owned by someone else blocks preparation.
	var lanes []retirementLaneEvidence
	for _, child := range sessions {
		childLanes, err := child.retirementLaneEvidence(ctx)
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, childLanes...)
	}
	rootLanes, err := root.retirementLaneEvidence(ctx)
	if err != nil {
		return nil, err
	}
	lanes = append(lanes, rootLanes...)
	// Required scratch dependencies must still exist at their original paths
	// under exact ownership before any release is attempted.
	if err := root.validateRetainedScratchPresent(); err != nil {
		return nil, fmt.Errorf("retirement preparation: %w", err)
	}
	// Durable stores must be readable from their primary files. The delegate
	// store is validated through its strict Load; the job store rereads its
	// journal rather than trusting a cached fold.
	if tree != nil && tree.store != nil {
		if err := tree.store.CheckRetirementReady(); err != nil {
			return nil, fmt.Errorf("retirement preparation: delegate store: %w", err)
		}
	}
	if root.jobManager != nil {
		if err := root.jobManager.checkRetirementReady(); err != nil {
			return nil, fmt.Errorf("retirement preparation: job store: %w", err)
		}
	}
	// Recheck the controller claim and current root generation, then the exact
	// tree version/pointers, so a transition that landed during validation is
	// never laundered into a successful preparation.
	c.mu.Lock()
	if !c.validClaimLocked(claim) || c.root != root || c.generation != generation {
		c.mu.Unlock()
		return nil, ErrRetirementUnavailable
	}
	c.mu.Unlock()
	if tree != nil {
		var reblockers []RetirementBlocker
		reblockers, _, err = tree.retirementEvidence()
		if err != nil {
			return nil, fmt.Errorf("retirement preparation: tree recheck: %w", err)
		}
		if len(reblockers) != 0 {
			return nil, fmt.Errorf("retirement preparation: tree changed with %d obligation(s)", len(reblockers))
		}
	}
	var evidenceVersion uint64
	if tree != nil {
		tree.mu.Lock()
		evidenceVersion = tree.evidenceVersion
		tree.mu.Unlock()
	}
	return &RetirementPreparation{
		claim:           claim,
		root:            root,
		generation:      generation,
		sessions:        sessions,
		evidenceVersion: evidenceVersion,
		lanes:           lanes,
	}, nil
}

// retirementLaneEvidence captures the occupied worktree lane for one resident
// runtime: its exact path, branch, owning delegate and the lock owner's marker,
// verified against the live lock. It reads session fields under mu, then forks
// git with no Session lock held. A runtime occupying no lane returns nil.
func (s *Session) retirementLaneEvidence(ctx context.Context) ([]retirementLaneEvidence, error) {
	s.mu.Lock()
	path := s.worktreeCurrentPath
	sessionID := s.id
	delegateID := s.owningDelegateID
	branch := s.envInfo.GitBranch
	s.mu.Unlock()
	if path == "" {
		return nil, nil
	}
	run := s.newWorktreeGitRunner(ctx, s.currentEnv())
	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("retirement preparation: lane %q lock read: %w", path, err)
	}
	entries := worktree.ParsePorcelain(out)
	target := canonicalOrClean(path)
	locked, reason := lockStateFromPorcelain(entries, target)
	if !locked {
		return nil, fmt.Errorf("retirement preparation: occupied lane %q is not locked", target)
	}
	state := worktree.ClassifyReason(reason, sessionID, delegateID)
	if state != worktree.OwnSession && state != worktree.OwnDelegate {
		return nil, fmt.Errorf("retirement preparation: occupied lane %q is locked by another owner", target)
	}
	if branch == "" {
		for _, entry := range entries {
			if canonicalOrClean(entry.Path) == target && entry.Branch != "" {
				branch = strings.TrimPrefix(entry.Branch, "refs/heads/")
				break
			}
		}
	}
	owner := reason
	if owner == "" {
		if state == worktree.OwnDelegate && delegateID != "" {
			owner = worktree.FormatDelegateMarker(delegateID, sessionID)
		} else {
			owner = worktree.FormatSessionMarker(sessionID)
		}
	}
	return []retirementLaneEvidence{{
		sessionID:  sessionID,
		delegateID: delegateID,
		path:       target,
		branch:     branch,
		owner:      owner,
	}}, nil
}

// validateRetirementRestore proves one session's durable artifacts are
// reconstructible from their primary files. It finishes pending metadata and
// mutation writes, fsyncs the live transcript without closing it or appending an
// end event, and rereads the original meta, transcript and mutation records with
// the production parsers. It never holds Session.mu over I/O and never repairs a
// corrupt primary record from a stale memory snapshot.
func (s *Session) validateRetirementRestore(ctx context.Context) error {
	if s == nil {
		return errors.New("retirement preparation: nil session")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.stateDir == "" {
		return errors.New("retirement preparation: session has no state directory")
	}
	if err := s.saveMeta(); err != nil {
		return fmt.Errorf("retirement preparation: metadata persistence: %w", err)
	}
	if tw := s.attachedTranscript(); tw != nil {
		if err := tw.EstablishDurability(); err != nil {
			return fmt.Errorf("retirement preparation: transcript durability: %w", err)
		}
	}
	data, err := readTranscriptFull(s.TranscriptPath())
	if err != nil {
		return fmt.Errorf("retirement preparation: transcript reconstruction: %w", err)
	}
	if data.Header.SessionID != s.id {
		return fmt.Errorf("retirement preparation: transcript header session %q does not match %q", data.Header.SessionID, s.id)
	}
	meta, err := schema.LoadSessionMeta(s.stateDir, s.id)
	if err != nil {
		return fmt.Errorf("retirement preparation: metadata reconstruction: %w", err)
	}
	if meta.ID != s.id {
		return fmt.Errorf("retirement preparation: metadata session %q does not match %q", meta.ID, s.id)
	}
	// Finish and surface the task store's durable writes. Task mutations persist
	// synchronously, so rereading the primary file proves it is reconstructible
	// and surfaces a corrupt list as a preparation failure.
	if store := s.getOrCreateTaskStore(); store != nil {
		if err := store.Load(); err != nil {
			return fmt.Errorf("retirement preparation: task store reconstruction: %w", err)
		}
	}
	// Attention state is transcript-backed. The fold read is the strict
	// readiness check (it stats before and after); EstablishDurability above
	// already flushed any pending attention bytes.
	if _, err := readExistingDelegateAttentionFold(s.TranscriptPath(), s.id); err != nil {
		return fmt.Errorf("retirement preparation: attention reconstruction: %w", err)
	}
	s.clientMutationsInitMu.Lock()
	mutations := s.clientMutations
	s.clientMutationsInitMu.Unlock()
	if err := mutations.checkRetirementReady(); err != nil {
		return fmt.Errorf("retirement preparation: client mutation reconstruction: %w", err)
	}
	return nil
}
