package agent

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
)

// This file carries the item-8 and item-9 discriminators from
// docs/superpowers/plans/2026-09-12-shared-notes-followups.md.
//
// Item 8: the notes mutators stage their new value in the live store under
// Session.mu and only then persist meta.json under notesUpdateMu+metaSaveMu;
// on a persistence failure they restore the previous value with no emission.
// Readers — Meta(), notesSnapshotAll(), and the notes_read tool, which all
// funnel through notesProjectionSnapshot — must observe only COMMITTED
// values: while a save is parked, and after it fails, they must keep reporting
// the value whose save already landed.
//
// Item 9: the four reader-visible fields — human note, agent note, URL list,
// notesEverProjected — must be one atomic cut. A reader that straddles a notes
// mutation must see either the whole previous tuple or the whole new tuple,
// never a mix.
//
// Both discriminators park a real mutation inside its metadata save through
// cfg.testOnly.notesAutoSaveFault, so the observation window is deterministic
// rather than a scheduling race. The mutation runs on its own goroutine; the
// fault signals entry and waits for the test to release it.

// errParkedNotesSave is the injected metadata-save failure the parked mutation
// under test receives. The failure path is what item 8 must make invisible to
// readers.
var errParkedNotesSave = errors.New("injected meta save failure")

// notesCut is one reader-visible notes tuple.
type notesCut struct {
	human string
	agent string
	urls  []schema.SessionURL
}

// notesProjectionCut is the full four-field tuple notesProjectionSnapshot
// returns: the cut readers of the projection (Meta, notes_read, the context
// block) consume.
type notesProjectionCut struct {
	human         string
	agent         string
	urls          []schema.SessionURL
	everProjected bool
}

func (c notesProjectionCut) String() string {
	return fmt.Sprintf("{human: %q, agent: %q, urls: %s, everProjected: %t}",
		c.human, c.agent, formatNotesCutURLs(c.urls), c.everProjected)
}

// readNotesProjectionCut takes one notesProjectionSnapshot cut.
func readNotesProjectionCut(s *Session) notesProjectionCut {
	human, agent, urls, everProjected := s.notesProjectionSnapshot()
	return notesProjectionCut{
		human:         human,
		agent:         agent,
		urls:          append([]schema.SessionURL(nil), urls...),
		everProjected: everProjected,
	}
}

func formatNotesCutURLs(urls []schema.SessionURL) string {
	parts := make([]string, 0, len(urls))
	for _, u := range urls {
		parts = append(parts, u.URL)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// parkNotesSave installs a blocking notesAutoSaveFault, starts mutate on its
// own goroutine, and returns once the mutation is parked inside the metadata
// save with its staged value already written to the live store. The returned
// release lets the parked save proceed with faultErr and returns the
// mutation's error; it is idempotent and is also run from t.Cleanup, so a
// failing read can never strand the mutation goroutine.
func parkNotesSave(t *testing.T, s *Session, mutate func(*Session) error) func(error) error {
	t.Helper()
	entered := make(chan struct{})
	releaseCh := make(chan error, 1)
	var signaled sync.Once
	s.cfg.testOnly.notesAutoSaveFault = func() error {
		signaled.Do(func() { close(entered) })
		return <-releaseCh
	}
	done := make(chan error, 1)
	go func() { done <- mutate(s) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second): // TRIPWIRE: the entry signal is the awaitable completion
		// Unblock the fault so the mutation goroutine cannot be stranded, then fail.
		s.cfg.testOnly.notesAutoSaveFault = nil
		close(releaseCh)
		<-done
		t.Fatalf("notes mutation never reached the metadata save")
	}
	var (
		releaseOnce sync.Once
		releaseErr  error
	)
	release := func(faultErr error) error {
		releaseOnce.Do(func() {
			s.cfg.testOnly.notesAutoSaveFault = nil
			releaseCh <- faultErr
			releaseErr = <-done
		})
		return releaseErr
	}
	t.Cleanup(func() { release(errParkedNotesSave) })
	return release
}

// drainNotesEvents clears the seeded mutations' events so a later assertion
// that a rejected save emitted nothing cannot trip over seed traffic.
func drainNotesEvents(s *Session) {
	for {
		select {
		case <-s.Events():
		default:
			return
		}
	}
}

// readNotesReaders reads every reader surface item 8 names: Meta(), the
// notesSnapshotAll tuple (what the notes_read tool reports), and the notes_read
// tool output itself.
func readNotesReaders(t *testing.T, s *Session) (meta, snapshot notesCut, toolOutput string) {
	t.Helper()
	metaState := s.Meta()
	human, agent, urls := s.notesSnapshotAll()
	meta = notesCut{
		human: metaState.HumanNote,
		agent: metaState.AgentNote,
		urls:  append([]schema.SessionURL(nil), metaState.SessionURLs...),
	}
	snapshot = notesCut{
		human: human,
		agent: agent,
		urls:  append([]schema.SessionURL(nil), urls...),
	}
	toolOutput = notesToolExec(t.Context(), t, s, "notes-read-committed", "notes_read", map[string]any{})
	return meta, snapshot, toolOutput
}

// assertNotesCutCommitted fails when a reader surface reports anything other
// than want, naming the field that exposed an uncommitted value.
func assertNotesCutCommitted(t *testing.T, surface string, got, want notesCut) {
	t.Helper()
	if got.human != want.human {
		t.Errorf("%s human note = %q while a notes save was parked or had failed; want the committed %q", surface, got.human, want.human)
	}
	if got.agent != want.agent {
		t.Errorf("%s agent note = %q while a notes save was parked or had failed; want the committed %q (a tentative value reached readers before its save landed)", surface, got.agent, want.agent)
	}
	if !reflect.DeepEqual(got.urls, want.urls) {
		t.Errorf("%s URL list = %s while a notes save was parked or had failed; want the committed %s (a tentative list reached readers before its save landed)", surface, formatNotesCutURLs(got.urls), formatNotesCutURLs(want.urls))
	}
}

// assertNotesSaveRejected checks the post-release half of the item-8 contract:
// the mutation reports the injected persistence failure, the live store rolls
// back to the committed values, every reader surface still reports those
// values, and no success event was emitted.
func assertNotesSaveRejected(t *testing.T, s *Session, mutationErr error, want notesCut) {
	t.Helper()
	if !errors.Is(mutationErr, errParkedNotesSave) {
		t.Errorf("parked mutation error = %v, want the injected save failure", mutationErr)
	}
	if liveNote := s.agentNoteForTest(); liveNote != want.agent {
		t.Errorf("live agent note after rejected save = %q, want rolled back to %q", liveNote, want.agent)
	}
	if liveURLs := s.SessionURLsForTest(); !reflect.DeepEqual(liveURLs, want.urls) {
		t.Errorf("live URL list after rejected save = %s, want rolled back to %s", formatNotesCutURLs(liveURLs), formatNotesCutURLs(want.urls))
	}
	meta, snapshot, _ := readNotesReaders(t, s)
	assertNotesCutCommitted(t, "Meta() after rejected save", meta, want)
	assertNotesCutCommitted(t, "notesSnapshotAll() after rejected save", snapshot, want)
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == events.EventNotesUpdated || ev.Kind == events.EventUrlsUpdated {
				t.Errorf("rejected notes save emitted %s: %+v", ev.Kind, ev.Data)
			}
		default:
			return
		}
	}
}

// TestReadersSeeOnlyCommittedNotesWhileSaveIsParked is the item-8
// discriminator: with a mutation parked inside its metadata save, and again
// after that save fails and the live store rolls back, every reader surface
// must report the last committed values and never the staged tentative value.
func TestReadersSeeOnlyCommittedNotesWhileSaveIsParked(t *testing.T) {
	t.Run("agent-note", func(t *testing.T) {
		s := newNotesToolSession(t)
		s.stateDir = t.TempDir()
		if _, err := s.SetHumanNote("seed-human", "committed human note"); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := s.mutateAgentNoteSerialized("committed agent note"); err != nil {
			t.Fatal(err)
		}
		drainNotesEvents(s)
		want := notesCut{human: "committed human note", agent: "committed agent note"}
		release := parkNotesSave(t, s, func(s *Session) error {
			_, _, _, _, err := s.mutateAgentNoteSerialized("tentative agent note")
			return err
		})
		meta, snapshot, toolOutput := readNotesReaders(t, s)
		if strings.Contains(toolOutput, "tentative agent note") {
			t.Errorf("notes_read output carried the tentative agent note while its save was parked:\n%s", toolOutput)
		}
		if !strings.Contains(toolOutput, "committed agent note") {
			t.Errorf("notes_read output dropped the committed agent note while a new value's save was parked:\n%s", toolOutput)
		}
		mutationErr := release(errParkedNotesSave)
		assertNotesCutCommitted(t, "Meta() while the agent-note save was parked", meta, want)
		assertNotesCutCommitted(t, "notesSnapshotAll() while the agent-note save was parked", snapshot, want)
		assertNotesSaveRejected(t, s, mutationErr, want)
	})

	t.Run("url-add", func(t *testing.T) {
		s := newNotesToolSession(t)
		s.stateDir = t.TempDir()
		if _, err := s.SetHumanNote("seed-human", "committed human note"); err != nil {
			t.Fatal(err)
		}
		committed, _, err := s.mutateSessionURLAddSerialized("https://committed.test/committed", "committed")
		if err != nil {
			t.Fatal(err)
		}
		drainNotesEvents(s)
		want := notesCut{human: "committed human note", urls: []schema.SessionURL{committed}}
		release := parkNotesSave(t, s, func(s *Session) error {
			_, _, err := s.mutateSessionURLAddSerialized("https://tentative.test/tentative", "tentative")
			return err
		})
		meta, snapshot, toolOutput := readNotesReaders(t, s)
		if strings.Contains(toolOutput, "tentative.test") {
			t.Errorf("notes_read output carried the tentative URL while its save was parked:\n%s", toolOutput)
		}
		if !strings.Contains(toolOutput, "committed.test/committed") {
			t.Errorf("notes_read output dropped the committed URL while a new URL's save was parked:\n%s", toolOutput)
		}
		mutationErr := release(errParkedNotesSave)
		assertNotesCutCommitted(t, "Meta() while the URL-add save was parked", meta, want)
		assertNotesCutCommitted(t, "notesSnapshotAll() while the URL-add save was parked", snapshot, want)
		assertNotesSaveRejected(t, s, mutationErr, want)
	})

	t.Run("url-remove", func(t *testing.T) {
		s := newNotesToolSession(t)
		s.stateDir = t.TempDir()
		if _, err := s.SetHumanNote("seed-human", "committed human note"); err != nil {
			t.Fatal(err)
		}
		keep, _, err := s.mutateSessionURLAddSerialized("https://committed.test/keep", "keep")
		if err != nil {
			t.Fatal(err)
		}
		doomed, _, err := s.mutateSessionURLAddSerialized("https://committed.test/doomed", "doomed")
		if err != nil {
			t.Fatal(err)
		}
		drainNotesEvents(s)
		want := notesCut{human: "committed human note", urls: []schema.SessionURL{keep, doomed}}
		release := parkNotesSave(t, s, func(s *Session) error {
			removed, _, err := s.mutateSessionURLRemoveSerialized(doomed.ID)
			if err == nil && !removed {
				return errors.New("parked removal did not remove the entry")
			}
			return err
		})
		meta, snapshot, toolOutput := readNotesReaders(t, s)
		for _, url := range []string{keep.URL, doomed.URL} {
			if !strings.Contains(toolOutput, url) {
				t.Errorf("notes_read output dropped %q while its removal's save was parked:\n%s", url, toolOutput)
			}
		}
		mutationErr := release(errParkedNotesSave)
		assertNotesCutCommitted(t, "Meta() while the URL-remove save was parked", meta, want)
		assertNotesCutCommitted(t, "notesSnapshotAll() while the URL-remove save was parked", snapshot, want)
		assertNotesSaveRejected(t, s, mutationErr, want)
	})
}

// runParkedAtomicCut seeds an already-projected notes state, parks mutate in
// its metadata save, and returns the projection cut before the mutation, the
// cut read while the save was parked, and the cut after the save committed.
// It fails the test when the parked cut is not the whole previous tuple: the
// reader must see either all of the old state or all of the new state.
func runParkedAtomicCut(t *testing.T, s *Session, mutate func(*Session) error) (previous, parked, advanced notesProjectionCut) {
	t.Helper()
	previous = readNotesProjectionCut(s)
	if !previous.everProjected {
		t.Fatalf("seed did not project notes context; previous cut = %s", previous)
	}
	release := parkNotesSave(t, s, mutate)
	parked = readNotesProjectionCut(s)
	mutationErr := release(nil)
	if mutationErr != nil {
		t.Fatalf("parked save did not commit: %v", mutationErr)
	}
	advanced = readNotesProjectionCut(s)
	if !reflect.DeepEqual(parked, previous) {
		t.Errorf("reader saw a mixed notes cut while a save was parked:\n"+
			"  parked:   %s\n  previous: %s\n"+
			"readers must see the whole previous tuple or the whole new tuple, never a mix "+
			"(here one field advanced while the others had not committed yet)", parked, previous)
	}
	return previous, parked, advanced
}

// TestNotesProjectionSnapshotIsOneAtomicCut is the item-9 discriminator: the
// (human note, agent note, URL list, notesEverProjected) tuple a reader takes
// from notesProjectionSnapshot must be one committed cut. While a mutation is
// parked in its metadata save the tuple must equal the previous cut as a
// whole; after the save commits it must advance as a whole.
func TestNotesProjectionSnapshotIsOneAtomicCut(t *testing.T) {
	seed := func(t *testing.T) *Session {
		t.Helper()
		s := newNotesToolSession(t)
		s.stateDir = t.TempDir()
		if _, err := s.SetHumanNote("seed-human", "committed human note"); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := s.mutateAgentNoteSerialized("committed agent note"); err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.mutateSessionURLAddSerialized("https://committed.test/x", "committed"); err != nil {
			t.Fatal(err)
		}
		// Project once so the seed includes notesEverProjected=true; the
		// projection record and flag are part of the tuple under test.
		s.maybeAppendNotesContext()
		drainNotesEvents(s)
		return s
	}

	t.Run("agent-note", func(t *testing.T) {
		s := seed(t)
		previous, _, advanced := runParkedAtomicCut(t, s, func(s *Session) error {
			_, _, _, _, err := s.mutateAgentNoteSerialized("tentative agent note")
			return err
		})
		want := previous
		want.agent = "tentative agent note"
		if !reflect.DeepEqual(advanced, want) {
			t.Errorf("notes cut after the agent-note save committed = %s, want the whole new tuple %s", advanced, want)
		}
	})

	t.Run("url-add", func(t *testing.T) {
		s := seed(t)
		previous, _, advanced := runParkedAtomicCut(t, s, func(s *Session) error {
			_, _, err := s.mutateSessionURLAddSerialized("https://tentative.test/added", "added")
			return err
		})
		if advanced.human != previous.human || advanced.agent != previous.agent || advanced.everProjected != previous.everProjected {
			t.Errorf("notes cut after the URL-add save committed = %s, want human/agent/everProjected unchanged from %s", advanced, previous)
		}
		if len(advanced.urls) != len(previous.urls)+1 {
			t.Fatalf("URL list after commit = %s, want exactly one appended entry on %s", formatNotesCutURLs(advanced.urls), formatNotesCutURLs(previous.urls))
		}
		if !reflect.DeepEqual(advanced.urls[:len(previous.urls)], previous.urls) {
			t.Errorf("URL list after commit = %s, want the previous entries %s unchanged", formatNotesCutURLs(advanced.urls), formatNotesCutURLs(previous.urls))
		}
		if last := advanced.urls[len(advanced.urls)-1]; last.URL != "https://tentative.test/added" || last.Label != "added" {
			t.Errorf("appended URL entry = %+v, want the committed https://tentative.test/added (added)", last)
		}
	})
}
