package main

import (
	"errors"
	"fmt"

	"primeradiant.com/evener/agent/schema"
)

// errBareDelegateResume is the sentinel a bare resume of a delegate returns.
// Assert it with errors.Is/errors.As, never by substring; its prose is pinned
// whole-output by the resume_guard_*.golden files under testdata/.
var errBareDelegateResume = errors.New("bare delegate resume refused")

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
// A root session (IsSubagent false) and a fork (ParentSessionID set with
// IsSubagent false) are untouched: only a delegate carries the flag.
func refuseBareDelegateResume(meta schema.SessionMeta) error {
	if !meta.IsSubagent {
		return nil
	}
	route := "the root session that spawned it"
	if meta.ParentSessionID != "" {
		route = "its parent session " + meta.ParentSessionID
	}
	return fmt.Errorf("%w: session %s is a delegate (is_subagent) whose parent carrier is not persisted, so resuming it on its own would restore it as a root runtime with a root delegation allowance and the delegate tool. Resume %s instead, then reach this delegate with delegate_send from that session",
		errBareDelegateResume, meta.ID, route)
}
