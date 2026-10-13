package main

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/schema"
)

// errBareDelegateResume is the sentinel a bare resume of a delegate returns.
// Assert it with errors.Is/errors.As, never by substring.
var errBareDelegateResume = errors.New("bare delegate resume refused")

// delegateOwnerHopLimit caps the owner-chain walk so a malformed or cyclic
// chain cannot spin.
const delegateOwnerHopLimit = 64

// refuseBareDelegateResume returns nil for a root session or a fork, and
// errBareDelegateResume for a delegate resumed on its own.
//
// A delegate's persisted metadata says IsSubagent, but its parent carrier
// (cfg.spawn) is never persisted (json:"-"). A bare `serve --resume` or
// `run --resume` of that id would therefore restore the delegate as a root
// runtime: agent/session_init.go takes the root allowance branch when
// spawn.parentSessionID is empty, handing the session cfg.MaxSubagentDepth and
// the delegate tool, while isSubagentSession() (from the persisted flag) reports
// a subagent — a contradiction Jesse refused the operation over (#4442), rather
// than inventing an allowance for a state the user did not mean to create.
//
// The message names the root of the delegate's persisted owner chain to resume,
// not meta.ParentSessionID: a nested delegate's parent is itself a delegate,
// which the same guard would refuse, and a bare delegate_send reaches only the
// caller's own delegates. A root session (IsSubagent false) and a fork
// (ParentSessionID set with IsSubagent false) are untouched: only a delegate
// carries the flag.
func refuseBareDelegateResume(meta schema.SessionMeta, stateDir string) error {
	if !meta.IsSubagent {
		return nil
	}
	route := "the root session that owns this tree"
	if owner := delegateRootOwner(meta, stateDir); owner != "" {
		route = "the root session " + owner
	}
	return fmt.Errorf("%w: session %s is a delegate (is_subagent) whose parent carrier is not persisted, so resuming it on its own would restore it as a root runtime with a root delegation allowance and the delegate tool. Resume %s instead, then reach this delegate through its owner",
		errBareDelegateResume, meta.ID, route)
}

// delegateRootOwner walks the persisted owner chain from meta to the first
// ancestor that is not itself a delegate. meta.ParentSessionID names the
// spawning session, so the chain can be longer than two hops when a delegate
// spawned a delegate; the first non-delegate is the session that can be
// resumed. It stops at a fork too, because a fork's ParentSessionID is fork
// lineage, not ownership.
//
// It returns "" when the chain cannot be resolved (a missing ancestor meta, an
// empty parent id, a cycle past delegateOwnerHopLimit), so the message falls
// back to generic wording rather than naming a session it cannot vouch for.
func delegateRootOwner(meta schema.SessionMeta, stateDir string) string {
	if stateDir == "" {
		return ""
	}
	owner := meta.ParentSessionID
	for hop := 0; hop < delegateOwnerHopLimit && owner != ""; hop++ {
		parent, err := schema.LoadSessionMeta(stateDir, owner)
		if err != nil {
			return ""
		}
		if !parent.IsSubagent {
			return owner
		}
		owner = parent.ParentSessionID
	}
	return ""
}

// latestResumableRootMeta returns the newest session meta that can be resumed
// at all: the first by UpdatedAt descending whose IsSubagent is false.
//
// An implicit --resume-last must not become useless when a delegate was touched
// more recently than its root (roborev, PR #4589): the operation the user meant
// is "resume my latest session", which has a resumable answer. An explicit
// delegate id is still refused. The selection lives here, not in
// cmdutil.ResolveSessionMeta, so that function keeps its exact
// newest-session meaning for every other caller.
//
// It reports false when the state dir has no resumable session, so the caller
// keeps the delegate meta and lets refuseBareDelegateResume name the refusal.
func latestResumableRootMeta(stateDir string) (schema.SessionMeta, bool) {
	list, err := schema.ListSessionMetas(stateDir)
	if err != nil {
		return schema.SessionMeta{}, false
	}
	for _, meta := range list {
		if !meta.IsSubagent {
			return meta, true
		}
	}
	return schema.SessionMeta{}, false
}
