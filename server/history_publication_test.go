package server

import (
	"errors"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/transcriptindex"
	"primeradiant.com/evener/llm"
)

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
	staged := hx.record(t, "failed-history-input-194")
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
	staged := hx.record(t, "unfinished-input-180")
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
	raw := hx.record(t, "staged-172")
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
