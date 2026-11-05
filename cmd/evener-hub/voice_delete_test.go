package hub

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
)

type voiceDeletionRecords struct {
	binding    hubcore.VoiceBinding
	turnID     string
	deliveryID string
}

type voiceDeletionFixture struct {
	web        *WebServer
	stateDir   string
	projectID  string
	projectDir string
	targetID   string
	siblingID  string
	target     voiceDeletionRecords
	sibling    voiceDeletionRecords
}

func newVoiceDeletionFixture(t *testing.T, opened, retired bool) voiceDeletionFixture {
	t.Helper()
	root := t.TempDir()
	projectDir := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}
	project, err := identifier.ResolveProject(projectDir)
	if err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(root, "projects", project.ID)
	targetID, siblingID := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
	writeSession(t, stateDir, targetID, project.CanonicalPath)
	writeSession(t, stateDir, siblingID, project.CanonicalPath)
	past := hubcore.NewPastIndexWithDB(filepath.Join(root, "projects", "*"), filepath.Join(root, "index.db"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(hubcore.WebConfig{StateDir: root, HubStateRoot: filepath.Join(root, "hub"), Past: past, Roster: hubcore.NewRosterWithEntries()})
	t.Cleanup(web.voice.close)
	var journal *hubcore.VoiceJournal
	if opened {
		journal, err = web.voice.store()
	} else {
		journal, err = hubcore.NewVoiceJournal(web.cfg.HubStateRoot)
	}
	if err != nil {
		t.Fatal(err)
	}
	target := seedVoiceDeletionRecords(t, journal, targetID)
	sibling := seedVoiceDeletionRecords(t, journal, siblingID)
	if retired {
		if err := journal.RetireBinding(t.Context(), target.binding.ID, target.binding.Epoch); err != nil {
			t.Fatal(err)
		}
	}
	if !opened {
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return voiceDeletionFixture{web: web, stateDir: stateDir, projectID: project.ID, projectDir: project.CanonicalPath, targetID: targetID, siblingID: siblingID, target: target, sibling: sibling}
}

func seedVoiceDeletionRecords(t *testing.T, journal *hubcore.VoiceJournal, id string) voiceDeletionRecords {
	t.Helper()
	ctx := t.Context()
	binding := hubcore.VoiceBinding{ID: "binding-" + id, Ref: localAppRef(id), InstanceID: "instance", Epoch: "epoch", DeviceID: "device", ExpiresAt: time.Now().Add(time.Hour), Active: true}
	if err := journal.CreateBinding(ctx, binding, false); err != nil {
		t.Fatal(err)
	}
	delivery := hubcore.SpeechDelivery{ID: "delivery-" + id, BindingID: binding.ID, Epoch: binding.Epoch, InstanceID: binding.InstanceID, CanonicalSourceItemID: "source", CanonicalText: "canonical-" + id, OriginatingRequestID: "request", FrontierID: "frontier", PresentationGeneration: 2}
	if err := journal.CreateSpeechDelivery(ctx, delivery); err != nil {
		t.Fatal(err)
	}
	spoken := "spoken-" + id
	if _, err := journal.UpdateSpeechDelivery(ctx, delivery.ID, hubcore.SpeechDeliveryUpdate{Epoch: binding.Epoch, PresentationGeneration: 2, GeneratedTranscript: &spoken}); err != nil {
		t.Fatal(err)
	}
	turn, err := journal.CreateTurn(ctx, binding.ID, binding.Epoch, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.CommitTurnInput(ctx, turn.ID, "provider-item"); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.PrepareIntent(ctx, turn.ID, appwire.MethodTurnStart, "input-"+id, "mutation-"+id); err != nil {
		t.Fatal(err)
	}
	return voiceDeletionRecords{binding: binding, turnID: turn.ID, deliveryID: delivery.ID}
}

func assertVoiceDeletionRecords(t *testing.T, journal *hubcore.VoiceJournal, records voiceDeletionRecords, present bool) {
	t.Helper()
	_, bindingErr := journal.Binding(t.Context(), records.binding.ID)
	_, turnErr := journal.Turn(t.Context(), records.turnID)
	intent, intentErr := journal.Intent(t.Context(), records.turnID)
	speech, speechErr := journal.SpeechDelivery(t.Context(), records.deliveryID)
	for kind, err := range map[string]error{"binding": bindingErr, "turn": turnErr, "intent": intentErr, "speech": speechErr} {
		if present && err != nil {
			t.Errorf("surviving %s inaccessible: %v", kind, err)
		}
		if !present && !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("deleted session %s remains readable, error=%v", kind, err)
		}
	}
	if present && (intent.Text == "" || speech.CanonicalText == "" || speech.GeneratedTranscript == "") {
		t.Error("surviving session lost its voice text")
	}
}

func TestVoiceSessionDeletionPurgesJournal(t *testing.T) {
	for _, opened := range []bool{false, true} {
		for _, retired := range []bool{false, true} {
			t.Run(fmt.Sprintf("opened=%v/retired=%v", opened, retired), func(t *testing.T) {
				f := newVoiceDeletionFixture(t, opened, retired)
				response := mustDeleteSession(t, f.web, f.targetID)
				if len(response.Deleted) != 1 || response.Deleted[0] != f.targetID || len(response.Skipped) != 0 {
					t.Fatalf("deletion did not execute: %+v", response)
				}
				for _, suffix := range []string{".meta.json", ".transcript.jsonl"} {
					if _, err := os.Stat(filepath.Join(f.stateDir, "sessions", f.targetID+suffix)); !os.IsNotExist(err) {
						t.Fatalf("session deletion not exercised: %v", err)
					}
				}
				journal, err := f.web.voice.store()
				if err != nil {
					t.Fatal(err)
				}
				assertVoiceDeletionRecords(t, journal, f.target, false)
				assertVoiceDeletionRecords(t, journal, f.sibling, true)
				// An acknowledged delete stays idempotent with no row left in Past.
				if retry := mustDeleteSession(t, f.web, f.targetID); len(retry.Deleted) != 0 || len(retry.Skipped) != 0 {
					t.Fatalf("retry: %+v", retry)
				}
			})
		}
	}
}

func TestVoiceSessionDeletionRefusalPreservesJournal(t *testing.T) {
	f := newVoiceDeletionFixture(t, true, false)
	f.web.cfg.Roster = hubcore.NewRosterWithEntries(hubcore.LiveEntry{SessionID: f.targetID, Status: "active"})
	response := mustDeleteSession(t, f.web, f.targetID)
	if len(response.Deleted) != 0 || len(response.Skipped) != 1 {
		t.Fatalf("live deletion was not refused: %+v", response)
	}
	journal, err := f.web.voice.store()
	if err != nil {
		t.Fatal(err)
	}
	assertVoiceDeletionRecords(t, journal, f.target, true)
	assertVoiceDeletionRecords(t, journal, f.sibling, true)
}

func TestVoiceProjectDeletionPurgesJournal(t *testing.T) {
	f := newVoiceDeletionFixture(t, false, true)
	response, err := dispatchProjectDelete(t, f.web, appwire.ProjectDeleteParams{Key: f.projectID, WorkingDir: f.projectDir})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Deleted) != 2 || len(response.Skipped) != 0 {
		t.Fatalf("project deletion did not execute: %+v", response)
	}
	journal, err := f.web.voice.store()
	if err != nil {
		t.Fatal(err)
	}
	assertVoiceDeletionRecords(t, journal, f.target, false)
	assertVoiceDeletionRecords(t, journal, f.sibling, false)
}

func TestVoiceSessionDeletionRetriesJournalFailure(t *testing.T) {
	f := newVoiceDeletionFixture(t, false, false)
	path := filepath.Join(f.web.cfg.HubStateRoot, "voice", "journal.db")
	backup := path + ".preserved"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := dispatchSessionDelete(t, f.web, appwire.SessionDeleteParams{Ref: localAppRef(f.targetID)})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInternalError {
		t.Fatalf("journal failure was not reported: %v", err)
	}
	if _, ok := f.web.cfg.Past.Find(f.targetID); ok {
		t.Fatal("test did not reach post-artifact cleanup")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	response := mustDeleteSession(t, f.web, f.targetID)
	if len(response.Deleted) != 0 || len(response.Skipped) != 0 {
		t.Fatalf("retry: %+v", response)
	}
	journal, err := f.web.voice.store()
	if err != nil {
		t.Fatal(err)
	}
	assertVoiceDeletionRecords(t, journal, f.target, false)
	assertVoiceDeletionRecords(t, journal, f.sibling, true)
}
