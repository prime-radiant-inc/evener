package agent

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/appwire"
)

// The read side of the intent channel (docs/specs/2026-10-11-delegate-latest-
// intent-design.md, "Read join"): the capture projects the folded journal value
// Excerpt-bounded — the one bound rule every projection of the field applies —
// and the capture site applies the frame builder's join: the controller's live
// value when non-empty, else the folded value.

// TestSessionActivityDelegateReadProjectsTheBoundedFoldedIntent pins the
// capture's folded projection: a journal's intent — however whitespace-laden —
// reads as the same single-line excerpt the projector pushes, so a pushed frame
// and a read produce identical strings for the same intent.
func TestSessionActivityDelegateReadProjectsTheBoundedFoldedIntent(t *testing.T) {
	t.Parallel()
	nasty := "Reading  the\tvery\nmessy   board's row tests " + strings.Repeat("and more words ", 60)
	aggregate := &delegatestore.Aggregate{
		DelegateID:     "dlg",
		Phase:          delegatestore.PhaseRunning,
		CurrentRunOpen: true,
		LatestIntent:   nasty,
	}
	row := captureSessionActivityDelegate("root", delegatestore.State{"dlg": aggregate}, aggregate, time.Unix(100, 0)).project()
	want := appwire.Excerpt(nasty, appwire.MaxIntentRunes)
	if row.LatestIntent != want {
		t.Fatalf("read intent = %q, want the exact excerpt %q", row.LatestIntent, want)
	}
	if strings.ContainsAny(row.LatestIntent, "\n\t") {
		t.Fatalf("read intent keeps flattened whitespace: %q", row.LatestIntent)
	}
}

// TestSessionActivityDelegateReadJoinsLiveOverFolded pins the read's join at
// the real capture site: the controller's live value wins while non-empty, and
// the folded journal value serves in the restored window (live state empty),
// so the read and the pushed frame agree for every row state.
func TestSessionActivityDelegateReadJoinsLiveOverFolded(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir(), MaxSubagentDepth: 1}))
	at := time.Unix(100, 0).UTC()
	id := "dlg_intent"
	s.delegateController.mu.Lock()
	_, err := s.delegateController.appendLocked(
		delegatestore.Event{Kind: delegatestore.EventDelegateCreated, DelegateID: id, Created: &delegatestore.DelegateCreated{Descriptor: stableToolDescriptor(s, id, "")}},
		delegateControllerRunStartedEvent(id, 1, delegatestore.TriggerInitial, at),
		delegatestore.Event{Kind: delegatestore.EventDelegateIntentStated, DelegateID: id, IntentStated: &delegatestore.IntentStated{Generation: 1, Intent: "Inspecting the journal by hand."}},
	)
	if err != nil {
		s.delegateController.mu.Unlock()
		t.Fatal(err)
	}
	live := &delegateLiveState{intent: "Live words from the controller."}
	s.delegateController.live[id] = live
	s.delegateController.mu.Unlock()

	params := appwire.SessionActivityListParams{Ref: encodeRef("", s.ID())}
	readParams := appwire.SessionActivityReadParams{Ref: params.Ref, Scope: appwire.SessionActivityScopeSession}
	capture := func(t *testing.T) appwire.SessionDelegate {
		t.Helper()
		read, err := s.activityRead(t.Context(), readParams)
		if err != nil {
			t.Fatal(err)
		}
		token, walk, err := read.index.token(params, appwire.SessionActivityResourceDelegates, s.ID())
		if err != nil {
			read.index.release()
			t.Fatal(err)
		}
		candidates, complete, err := read.captureDelegateCandidates(t.Context(), appwire.SessionActivityListParams{Limit: 5}, token, walk)
		read.index.release()
		if err != nil || !complete || len(candidates) == 0 {
			t.Fatalf("capture: candidates=%d complete=%v err=%v", len(candidates), complete, err)
		}
		for _, candidate := range candidates {
			if candidate.key.ID == id {
				return candidate.project()
			}
		}
		t.Fatalf("delegate %s missing from the capture", id)
		return appwire.SessionDelegate{}
	}

	// Live wins while non-empty.
	if got := capture(t).LatestIntent; got != "Live words from the controller." {
		t.Fatalf("read joined %q, want the live value", got)
	}
	// The restored window: a fresh live state with no in-process intent serves
	// the standing folded journal line.
	s.delegateController.mu.Lock()
	live.intent = ""
	s.delegateController.mu.Unlock()
	if got := capture(t).LatestIntent; got != "Inspecting the journal by hand." {
		t.Fatalf("read joined %q, want the folded value", got)
	}
}
