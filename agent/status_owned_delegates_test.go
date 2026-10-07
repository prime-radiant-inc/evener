package agent

import (
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
)

// SessionOwnedDelegateIDs lists the sessions below a session in its tree: the
// whole tree for a root, the subtree under a subagent's own delegate for a
// subagent, never the session itself, and never a row the root does not own
// nor one whose parentage runs through such a row.
func TestSessionOwnedDelegateIDs_ListsDescendantSessionsTheRootOwns(t *testing.T) {
	t.Parallel()
	stateDir := t.TempDir()
	rootID := identifier.MustNewSessionID()
	session := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d", "foreign", "underForeign"} {
		session[name] = identifier.MustNewSessionID()
	}
	created := func(delegateID, parentID, owner, child string) delegatestore.Event {
		return delegatestore.Event{Kind: delegatestore.EventDelegateCreated, TS: time.Unix(1_700_000_000, 0).UTC(), DelegateID: delegateID, Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			OwnerSessionID: owner, ParentDelegateID: parentID, ChildSessionID: child, TranscriptRef: encodeRef("", child), Task: "task", AgentType: "default", ToolNameCeiling: []string{"communicate"}, Resumable: true,
		}}}
	}
	store, err := delegatestore.Open(jobsDir(stateDir, rootID) + "/delegates.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.AppendBatch(state, []delegatestore.Event{
		created("dlg_a", "", rootID, session["a"]),
		created("dlg_b", "dlg_a", rootID, session["b"]),
		created("dlg_c", "dlg_b", rootID, session["c"]),
		created("dlg_d", "", rootID, session["d"]),
		created("dlg_foreign", "", identifier.MustNewSessionID(), session["foreign"]),
		created("dlg_under_foreign", "dlg_foreign", rootID, session["underForeign"]),
	})
	if closeErr := store.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b", "c"} {
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: session[name], JobTreeRootSessionID: rootID, IsSubagent: true}); err != nil {
			t.Fatal(err)
		}
	}
	sorted := func(ids ...string) []string {
		slices.Sort(ids)
		return ids
	}
	for _, tc := range []struct {
		name      string
		sessionID string
		want      []string
	}{
		{name: "root", sessionID: rootID, want: sorted(session["a"], session["b"], session["c"], session["d"])},
		{name: "subagent a", sessionID: session["a"], want: sorted(session["b"], session["c"])},
		{name: "subagent b", sessionID: session["b"], want: sorted(session["c"])},
		{name: "leaf c", sessionID: session["c"], want: nil},
	} {
		got, err := SessionOwnedDelegateIDs(t.Context(), stateDir, tc.sessionID)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !slices.Equal(got, tc.want) {
			t.Fatalf("%s: descendant sessions = %v, want %v", tc.name, got, tc.want)
		}
	}
}
