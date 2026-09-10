package agent

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

func newDurableHumanNoteSession(t *testing.T) *Session {
	t.Helper()
	s := newNotesToolSession(t)
	s.stateDir = t.TempDir()
	store, err := newClientMutationStore(s.stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.clientMutations = store
	return s
}

func isHumanNoteConflict(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && wire.Code == appwire.CodeConflict
}

func isHumanNoteMismatch(err error) bool {
	var wire appwire.WireError
	return errors.As(err, &wire) && wire.Code == appwire.CodeInvalidRequest
}

func reloadHumanNoteStore(t *testing.T, s *Session) {
	t.Helper()
	store, err := newClientMutationStore(s.stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.clientMutations = store
	s.restoreDurableClientMutationQueues()
}

func TestHumanNoteFenceRejectsWholeSave(t *testing.T) {
	s := newNotesToolSession(t)
	s.stateDir = t.TempDir()
	storeForTest, storeErr := newClientMutationStore(s.stateDir, s.ID())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	s.clientMutations = storeForTest
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("save crossed the interrupt fence")
	}
	human, _ := s.notesSnapshot()
	if human != "" {
		t.Fatalf("refused save changed note to %q", human)
	}
	if n := len(s.clientMutations.snapshot().SteeringOrder); n != 0 {
		t.Fatalf("refused save queued %d notifications", n)
	}
}

func TestHumanNoteAtomicDurability(t *testing.T) {
	for _, boundary := range []string{"reservation", "before-effect", "after-effect"} {
		t.Run(boundary, func(t *testing.T) {
			s := newNotesToolSession(t)
			s.stateDir = t.TempDir()
			storeForTest, storeErr := newClientMutationStore(s.stateDir, s.ID())
			if storeErr != nil {
				t.Fatal(storeErr)
			}
			s.clientMutations = storeForTest
			if _, err := s.SetHumanNote("initial", "old"); err != nil {
				t.Fatal(err)
			}
			fault := func() error { return errors.New("filesystem boundary") }
			switch boundary {
			case "reservation":
				s.clientMutations.faults.AfterReservation = fault
			case "before-effect":
				s.clientMutations.faults.BeforeEffectSnapshotRename = fault
			case "after-effect":
				s.clientMutations.faults.AfterEffectSnapshotRename = fault
			}
			if _, err := s.SetHumanNote("save", " new\nvalue "); err == nil {
				t.Fatal("fault was not reported")
			}
			disk, err := loadClientMutationSnapshotFS(afero.NewOsFs(), s.stateDir, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			want, count := "old", 1
			if boundary == "after-effect" {
				want, count = "new value", 2
			}
			if disk.HumanNote == nil || *disk.HumanNote != want || len(disk.SteeringOrder) != count {
				t.Fatalf("disk note/steering = %+v / %v; want %q / %d", disk.HumanNote, disk.SteeringOrder, want, count)
			}
			if live, _ := s.notesSnapshot(); live != want {
				t.Fatalf("live = %q, want %q", live, want)
			}
			if got := s.Meta().HumanNote; got != want {
				t.Fatalf("meta = %q, want %q", got, want)
			}
			restored, err := newClientMutationStore(s.stateDir, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			s.clientMutations = restored
			s.restoreDurableClientMutationQueues()
			response, err := s.SetHumanNote("save", " new\nvalue ")
			if err != nil {
				t.Fatal(err)
			}
			if response.Note != "new value" || response.Receipt.ClientMutationID != "save" {
				t.Fatalf("response = %+v", response)
			}
			if _, err := s.SetHumanNote("newer", "latest"); err != nil {
				t.Fatal(err)
			}
			replay, err := s.SetHumanNote("save", " new\nvalue ")
			if err != nil {
				t.Fatal(err)
			}
			if replay.Note != "new value" || replay.Receipt.Disposition != appwire.MutationDispositionReplayed {
				t.Fatalf("replay = %+v", replay)
			}
			if live, _ := s.notesSnapshot(); live != "latest" {
				t.Fatalf("retry reverted note: %q", live)
			}
			disk, err = loadClientMutationSnapshotFS(afero.NewOsFs(), s.stateDir, s.ID())
			if err != nil {
				t.Fatal(err)
			}
			if len(disk.SteeringOrder) != 3 || len(disk.Journal) != 3 {
				t.Fatalf("duplicate transaction: %+v", disk.SteeringOrder)
			}
			if disk.Journal["save"].SteeringKind != events.SteeringKindHumanNote {
				t.Fatal("durable human-note kind missing")
			}
		})
	}
}

func TestHumanNoteRecoveryChecksEffectFence(t *testing.T) {
	s := newNotesToolSession(t)
	s.stateDir = t.TempDir()
	storeForTest, storeErr := newClientMutationStore(s.stateDir, s.ID())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	s.clientMutations = storeForTest
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	s.clientMutations.faults.AfterReservation = func() error { return errors.New("reservation crash") }
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("missing reservation fault")
	}
	store, err := newClientMutationStore(s.stateDir, s.ID())
	if err != nil {
		t.Fatal(err)
	}
	s.clientMutations = store
	if err := store.mutate(func(snapshot *clientMutationSnapshot) error {
		snapshot.InterruptFence = &clientMutationInterruptFence{ClientMutationID: "stop"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("recovery bypassed effect fence")
	}
	if human, _ := s.notesSnapshot(); human != "" {
		t.Fatalf("refused recovery stored %q", human)
	}
	if len(store.snapshot().SteeringOrder) != 0 {
		t.Fatal("refused recovery queued notification")
	}
}

func TestHumanNoteAtomicNoOpHeldAndConsumedReceipt(t *testing.T) {
	s := newNotesToolSession(t)
	s.stateDir = t.TempDir()
	storeForTest, storeErr := newClientMutationStore(s.stateDir, s.ID())
	if storeErr != nil {
		t.Fatal(storeErr)
	}
	s.clientMutations = storeForTest
	if err := s.ensureClientMutationStore(); err != nil {
		t.Fatal(err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error { snapshot.SteeringHeld = true; return nil }); err != nil {
		t.Fatal(err)
	}
	first, err := s.SetHumanNote("save", " sentinel ")
	if err != nil {
		t.Fatal(err)
	}
	same, err := s.SetHumanNote("same", "sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if first.Note != "sentinel" || first.Receipt.ProjectionState != appwire.MutationProjectionPending {
		t.Fatalf("accepted = %+v", first)
	}
	if same.Receipt.ProjectionState != appwire.MutationProjectionRemoved {
		t.Fatalf("noop = %+v", same)
	}
	snapshot := s.clientMutations.snapshot()
	if !snapshot.SteeringHeld || len(snapshot.SteeringOrder) != 1 || len(snapshot.PendingExecutions) != 1 {
		t.Fatalf("held noop state = %+v", snapshot)
	}
	if _, err := s.SetHumanNote("save", " sentinel "); err != nil {
		t.Fatal(err)
	}
	if !s.clientMutations.steeringHeld() {
		t.Fatal("replay unparked steering")
	}
	if turnID, ok := s.claimSteeringCarrierTurn(); ok {
		t.Fatalf("held notification claimed carrier %q", turnID)
	}
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("different raw payload reused ID")
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error { snapshot.SteeringHeld = false; return nil }); err != nil {
		t.Fatal(err)
	}
	drained := s.drainSteering()
	if len(drained) != 1 || drained[0].Kind != events.SteeringKindHumanNote || drained[0].ClientMutationID != "save" {
		t.Fatalf("drained = %+v", drained)
	}
	if !s.consumeSteeringMessage(drained[0]) {
		t.Fatal("steering consumption failed")
	}
	replay, err := s.SetHumanNote("save", " sentinel ")
	if err != nil {
		t.Fatal(err)
	}
	if replay.Receipt.ProjectionState != appwire.MutationProjectionReflected || len(s.clientMutations.snapshot().PendingExecutions) != 0 {
		t.Fatalf("consumed receipt = %+v", replay)
	}
}

func TestHumanNoteRestoreCanonicalFixtures(t *testing.T) {
	for _, note := range []string{"saved-sentinel", ""} {
		t.Run(note, func(t *testing.T) {
			s := newNotesToolSession(t)
			meta := s.Meta()
			meta.HumanNote = "stale-sentinel"
			stateDir := t.TempDir()
			dir := filepath.Join(stateDir, "mutations")
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			data := []byte(fmt.Sprintf(`{"version":1,"session_id":%q,"human_note":%q,"accepted_turns":0,"journal":{},"input_queue":[],"queue_revision":0,"next_turn_sequence":0,"next_queue_entry_sequence":0,"budget_reservations":{},"pending_executions":{}}`, meta.ID, note))
			path := filepath.Join(dir, meta.ID+".json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			read, present, err := ReadCanonicalHumanNote(stateDir, meta.ID)
			if err != nil || !present || read != note {
				t.Fatalf("read = %q, %v, %v", read, present, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, data) {
				t.Fatal("read helper rewrote journal")
			}
			restored, err := RestoreSessionFromMeta(s.client, s.profile, s.env, meta, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			if got := restored.Meta().HumanNote; got != note {
				t.Fatalf("restored metadata = %q, want %q", got, note)
			}
			if got, _ := restored.notesSnapshot(); got != note {
				t.Fatalf("restored notes = %q, want %q", got, note)
			}
			context := restored.notesContextBlock()
			if strings.Contains(context, "stale-sentinel") {
				t.Fatal("model context includes stale metadata sentinel")
			}
			if note != "" && !strings.Contains(context, note) {
				t.Fatal("committed sentinel missing from model context")
			}
			snapshot := restored.clientMutations.snapshot()
			*snapshot.HumanNote = "mutated copy"
			if got, _ := restored.notesSnapshot(); got != note {
				t.Fatal("snapshot clone aliases canonical authority")
			}
		})
	}
}

func TestHumanNoteReadAuthorityRejectsUnknownFields(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	if _, err := s.SetHumanNote("save", "sentinel"); err != nil {
		t.Fatal(err)
	}
	path := clientMutationFilePath(s.stateDir, s.ID())
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append([]byte(`{"unknown_notes_state":true,`), data[1:]...)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadCanonicalHumanNote(s.stateDir, s.ID()); err == nil {
		t.Fatal("read helper weakened strict decoding")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, data) {
		t.Fatal("failed read rewrote journal")
	}
}

func TestHumanNoteCommittedErrorPublishesAcceptance(t *testing.T) {
	s := newDurableHumanNoteSession(t)
	for len(s.Events()) > 0 {
		<-s.Events()
	}
	s.clientMutations.faults.AfterEffectSnapshotRename = func() error { return errors.New("lost response") }
	if _, err := s.SetHumanNote("save", "sentinel"); err == nil {
		t.Fatal("missing uncertainty")
	}
	updates := 0
	for len(s.Events()) > 0 {
		event := <-s.Events()
		if event.Kind == events.EventNotesUpdated {
			data, ok := event.Data.(events.NotesUpdatedData)
			if !ok || data.HumanNote != "sentinel" {
				t.Fatalf("publication = %+v", event.Data)
			}
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("committed save published %d updates, want 1", updates)
	}
	if queue := s.SteeringQueueSnapshot(); len(queue) != 1 {
		t.Fatalf("committed save reflected %d notifications", len(queue))
	}
}

func TestHumanNoteCleanCutoverDoesNotImportMetadata(t *testing.T) {
	s := newNotesToolSession(t)
	meta := s.Meta()
	meta.HumanNote = "unsupported-old-note"
	stateDir := t.TempDir()
	note, present, err := ReadCanonicalHumanNote(stateDir, meta.ID)
	if err != nil || present || note != "" {
		t.Fatalf("absent authority = %q, %v, %v", note, present, err)
	}
	restored, err := RestoreSessionFromMeta(s.client, s.profile, s.env, meta, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if got := restored.Meta().HumanNote; got != "" {
		t.Fatalf("clean cutover imported metadata %q", got)
	}
	if _, err := restored.SetHumanNote("first", ""); err != nil {
		t.Fatal(err)
	}
	note, present, err = ReadCanonicalHumanNote(stateDir, meta.ID)
	if err != nil || !present || note != "" {
		t.Fatalf("explicit empty authority = %q, %v, %v", note, present, err)
	}
}

func TestHumanNoteCleanCutoverRejectsOldJournalFields(t *testing.T) {
	for _, field := range []string{"notes_delivery_pending", "notes_stored_value", "notes_stored_value_set", "notes_inner_steer_id", "notes_adopted_intent"} {
		t.Run(field, func(t *testing.T) {
			s := newDurableHumanNoteSession(t)
			if _, err := s.SetHumanNote("save", "sentinel"); err != nil {
				t.Fatal(err)
			}
			path := clientMutationFilePath(s.stateDir, s.ID())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			value := `"old"`
			if field == "notes_delivery_pending" || field == "notes_stored_value_set" {
				value = "true"
			}
			data = []byte(strings.Replace(string(data), `"journal":{"save":{`, `"journal":{"save":{`+fmt.Sprintf(`%q:%s,`, field, value), 1))
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := newClientMutationStore(s.stateDir, s.ID()); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("old field %s was not rejected: %v", field, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, data) {
				t.Fatal("rejected journal was reset or migrated")
			}
		})
	}
}
