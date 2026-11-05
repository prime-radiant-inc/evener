package server

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

func TestHistoryPublicationWiringOrder(t *testing.T) {
	for _, order := range []string{"wire-first", "identity-first"} {
		t.Run(order, func(t *testing.T) {
			sess := newMutationReplaySession(t)
			s := NewServer(ServerConfig{})
			t.Cleanup(s.Close)
			identity, err := PrepareAppIdentityForRef("local", sess.ID(), "local:"+sess.ID(), sess.TranscriptPath())
			if err != nil {
				t.Fatal(err)
			}
			if order == "wire-first" {
				s.WireTranscriptHistory(sess)
			}
			s.ReplaceAppIdentity(identity.WithBootGeneration("7@late-wire"), nil)
			h := s.appHistoryForID(sess.ID())
			if order == "identity-first" {
				s.WireTranscriptHistory(sess)
			}
			if h == nil || s.appHistoryForID(sess.ID()) != h {
				t.Fatal("wiring lost installed history identity")
			}
			registered, _ := s.appHistories.publications.Load(sess.ID())
			if registered != h.historyPublication {
				t.Error("wired producer and installed history have different holders")
			}
			h.serial.Lock()
			defer h.serial.Unlock()
			cursor := s.appNotifier.CurrentSequence()
			w, _, err := transcript.OpenWriterForSession(sess.TranscriptPath(), sess.ID())
			if err != nil {
				t.Fatal(err)
			}
			defer w.Close()
			if err := w.Append(schema.NewTurn(schema.TurnUserInput, llm.User("late-wire-input-251"))); err != nil {
				t.Fatal(err)
			}
			if got, _ := h.RecordedLength(); got != w.RecordedLength() {
				t.Errorf("wired raw hook lost entry: got %d want %d", got, w.RecordedLength())
			}
			boundary := w.RecordedLength()
			if err := w.Append(publicationDemandTurn(sess.ID(), "late-wire-output-252")); err != nil {
				t.Fatal(err)
			}
			if got, _ := h.RecordedLength(); got != boundary {
				t.Errorf("wired history exposed unpaired output: got %d want %d", got, boundary)
			}
			state := &appwire.ConversationProvenanceState{Version: 1, InstanceID: sess.ID(), Revision: sess.ConversationProvenance().Revision + 1, Authoritative: true,
				AdmittedInputs: []string{"publication-input"}, Frontiers: []appwire.ConversationFrontier{{ID: "late-wire-output-252", AddedInputs: []string{"publication-input"}}}}
			registered.(*historyPublication).complete(w.RecordedLength(), state)
			if got, _ := h.RecordedLength(); got != w.RecordedLength() || h.conversationProvenance() != state {
				t.Error("wired publication did not pair bytes with the exact snapshot")
			}
			h.step()
			if updates := s.appNotifier.ReplayAfter(cursor, ""); len(updates) == 0 {
				t.Fatal("wired history did not notify readiness observers")
			}
		})
	}
}

// Output demand, not input adoption alone, must stay hidden until paired publication.
func publicationDemandTurn(instance, text string) schema.Turn {
	turn := schema.NewTurn(schema.TurnAssistant, llm.Assistant(text))
	turn.ConversationFrontier = &schema.ConversationFrontier{ID: text, AddedInputs: []string{"publication-input"}}
	turn.ConversationProvenance = &schema.ConversationOutputProvenance{Version: 1, InstanceID: instance, FrontierID: text, RequestID: text}
	return turn
}

func TestHistoryPublicationStagedDemandBoundary(t *testing.T) {
	for _, demand := range []string{"frontier", "provenance", "both"} {
		t.Run(demand, func(t *testing.T) {
			hx := newHistoryHarness(t)
			h := hx.history
			h.serial.Lock()
			defer h.serial.Unlock()
			h.mu.Lock()
			h.paired = true
			h.completeLength = hx.writer.RecordedLength()
			h.mu.Unlock()
			input := schema.NewTurn(schema.TurnUserInput, llm.User("side-writer-input-241"))
			input.ConversationInputIDs = []string{"publication-input"}
			safe := hx.recordTurn(t, input)
			if got, _ := h.RecordedLength(); got != safe.Offset+safe.Length {
				t.Errorf("non-demand input requires adoption: got %d want %d", got, safe.Offset+safe.Length)
			}
			turn := publicationDemandTurn(h.threadID, "pending-output-242")
			switch demand {
			case "frontier":
				turn.ConversationProvenance = nil
			case "provenance":
				turn.ConversationFrontier = nil
			}
			pending := hx.recordTurn(t, turn)
			if got, _ := h.RecordedLength(); got != pending.Offset {
				t.Errorf("demand boundary: got %d want %d", got, pending.Offset)
			}
			later := hx.record(t, "later-side-writer-input-243")
			if got, _ := h.RecordedLength(); got != pending.Offset {
				t.Errorf("later non-demand input jumped pending output: got %d want %d", got, pending.Offset)
			}
			state := &appwire.ConversationProvenanceState{Version: 1, InstanceID: h.threadID, Revision: 1, Authoritative: true,
				AdmittedInputs: []string{"publication-input"}, Frontiers: []appwire.ConversationFrontier{{ID: "pending-output-242", AddedInputs: []string{"publication-input"}}}}
			h.complete(pending.Offset+pending.Length, state)
			if got, _ := h.RecordedLength(); got != later.Offset+later.Length {
				t.Errorf("publication failed to release safe suffix: got %d want %d", got, later.Offset+later.Length)
			}
			if h.conversationProvenance() != state {
				t.Fatal("publication did not reuse exact producer snapshot")
			}
		})
	}
}

func TestHistoryPublicationCaptureAndNotificationIdentity(t *testing.T) {
	s := NewServer(ServerConfig{})
	t.Cleanup(s.Close)
	s.SetAppIdentity("local", "thread")
	_, p := s.transcriptPublicationHooks("thread")
	path := writeDelegateTranscript(t, "thread", "capture-211")
	h := s.ensureHistory("thread", "local:thread", path, 0, 0, "1")
	p.complete(0, &appwire.ConversationProvenanceState{InstanceID: "thread", Revision: 7, AdmittedInputs: []string{"paired-input-212"}})
	if got := s.appThread().Evener.ConversationProvenance; got == nil || got.Revision != 7 {
		t.Fatalf("subscription capture missed shared publication: %+v", got)
	}
	_, fresh := s.transcriptPublicationHooks("thread")
	fresh.complete(0, &appwire.ConversationProvenanceState{InstanceID: "thread", Revision: 1})
	s.ensureHistory("thread", "local:thread", path, 0, 0, "1")
	cursor := s.appNotifier.CurrentSequence()
	if err := h.publish(appwire.HistoryUpdatedParams{ThreadID: "thread"}); err != nil {
		t.Fatal(err)
	}
	if got := s.appNotifier.ReplayAfter(cursor, ""); len(got) != 0 {
		t.Fatalf("replaced history emitted notification: %+v", got)
	}
}

func TestHistoryPublicationFailedHistoryKeepsSafeSuffix(t *testing.T) {
	hx := newHistoryHarness(t)
	h := hx.history
	h.serial.Lock()
	defer h.serial.Unlock()
	h.mu.Lock()
	h.paired = true
	h.completeLength = hx.writer.RecordedLength()
	h.failed = &transcriptindex.EntryError{Ordinal: 1, Err: errors.New("projection failed")}
	h.mu.Unlock()
	staged := hx.recordTurn(t, publicationDemandTurn(h.threadID, "failed-history-input-194"))
	later := hx.recordTurn(t, schema.NewTurn(schema.TurnHookCompleted, llm.User("safe-suffix-195")))
	if got, _ := h.RecordedLength(); got > staged.Offset {
		t.Fatal("failed history exposed unfinished prefix")
	}
	h.complete(staged.Offset+staged.Length, &appwire.ConversationProvenanceState{InstanceID: h.threadID, Revision: 1})
	if got, _ := h.RecordedLength(); got != later.Offset+later.Length {
		t.Fatalf("failed history lost safe suffix: got %d want %d", got, later.Offset+later.Length)
	}
}

func TestHistoryPublicationSideWriteWithoutPendingFrontier(t *testing.T) {
	hx := newHistoryHarness(t)
	h := hx.history
	h.mu.Lock()
	h.paired = true
	h.completeLength = hx.writer.RecordedLength()
	h.mu.Unlock()
	rec := hx.recordTurn(t, schema.NewTurn(schema.TurnHookCompleted, llm.User("side-write-179")))
	if got, _ := h.RecordedLength(); got != rec.Offset+rec.Length {
		t.Fatalf("non-provenance side-write hidden: %d < %d", got, rec.Offset+rec.Length)
	}
	staged := hx.recordTurn(t, publicationDemandTurn(h.threadID, "unfinished-input-180"))
	later := hx.recordTurn(t, schema.NewTurn(schema.TurnHookCompleted, llm.User("later-side-write-181")))
	if got, _ := h.RecordedLength(); got > staged.Offset {
		t.Fatal("side-write bypassed unfinished provenance prefix")
	}
	h.complete(staged.Offset+staged.Length, &appwire.ConversationProvenanceState{InstanceID: h.threadID, Revision: 1})
	if got, _ := h.RecordedLength(); got != later.Offset+later.Length {
		t.Fatal("adoption did not release the safe non-provenance suffix")
	}
}

func TestHistoryPublicationReplacementRejectsOldProducer(t *testing.T) {
	r := newThreadHistories(2, 1024)
	t.Cleanup(r.close)
	path := writeDelegateTranscript(t, "same-instance", "replacement-178")
	old := &historyPublication{paired: true}
	r.publications.Store("same-instance", old)
	h := r.ensure("same-instance", "local:same-instance", path, 0, 0, "1", noopHistoryPublish, noopHistoryResync, nil)
	fresh := &historyPublication{paired: true}
	fresh.complete(12, &appwire.ConversationProvenanceState{InstanceID: "same-instance", Revision: 1})
	r.publications.Store("same-instance", fresh)
	next := r.ensure("same-instance", "local:same-instance", path, 0, 0, "1", noopHistoryPublish, noopHistoryResync, nil)
	if next == h {
		t.Fatal("replacement kept the prior producer's history identity")
	}
	old.complete(100, &appwire.ConversationProvenanceState{InstanceID: "same-instance", Revision: 100})
	if got, _ := next.RecordedLength(); got != 12 {
		t.Fatalf("delayed publication changed replacement boundary to %d", got)
	}
	if next.conversationProvenance().Revision != 1 {
		t.Fatal("delayed publication replaced the new producer snapshot")
	}
}

func TestHistoryPublicationOverflowKeepsCompletedBoundary(t *testing.T) {
	hx := newHistoryHarnessWith(t, 1)
	h := hx.history
	h.mu.Lock()
	h.paired = true
	h.completeLength = hx.writer.RecordedLength()
	h.mu.Unlock()
	first := hx.record(t, "published-171")
	h.complete(first.Offset+first.Length, &appwire.ConversationProvenanceState{Version: 1, InstanceID: h.threadID, Revision: 1, Authoritative: true})
	hx.awaitPublished(t, first.Offset+first.Length)
	h.serial.Lock()
	defer h.serial.Unlock()
	raw := hx.recordTurn(t, publicationDemandTurn(h.threadID, "staged-172"))
	hx.record(t, "later-173")
	last := hx.record(t, "later-174")
	other, err := transcriptindex.Open(h.path, transcriptindex.DirFor(h.path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	ahead, err := other.Latest(10)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.rebuildThroughBoundary(); err != nil {
		t.Fatal(err)
	}
	_, _, during, _, _, err := h.latest(h.capture(), h.ref, 10)
	if err != nil {
		t.Fatal(err)
	}
	if during.Length > raw.Offset {
		t.Fatalf("overflow exposed unfinished prefix: %d > %d", during.Length, raw.Offset)
	}
	stillAhead, err := other.Latest(10)
	if err != nil {
		t.Fatal(err)
	}
	if stillAhead.Length < ahead.Length {
		t.Fatalf("recovery rolled shared sidecar backward: %d < %d", stillAhead.Length, ahead.Length)
	}
	h.complete(last.Offset+last.Length, &appwire.ConversationProvenanceState{Version: 1, InstanceID: h.threadID, Revision: 2, Authoritative: true})
	if !h.step() {
		t.Fatal("completed prefix did not project")
	}
	h.mu.Lock()
	gap := h.gap
	h.mu.Unlock()
	if gap != nil {
		t.Fatalf("completed prefix left overlay gap: %+v", gap)
	}
}
