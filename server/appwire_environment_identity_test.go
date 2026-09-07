package server

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestLiveInputIdentityMatchesPersistedEnvironmentHistory(t *testing.T) {
	sess := newMutationReplaySessionWithAdapter(t, &mutationProjectionAdapter{blockAt: 2})
	srv := NewServer(ServerConfig{})
	prepared, err := PrepareAppIdentity("local", sess.ID(), sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	srv.ReplaceAppIdentity(prepared, nil)
	drained := make(chan struct{})
	sess.ConsumeEventsLossless(srv.RecordAppEvent, func() { close(drained) })

	const mutationID = "reader-environment-input"
	if _, err := sess.AcceptClientMutationStart(appwire.TurnStartParams{
		ClientMutationID:   mutationID,
		ExpectedInstanceID: sess.ID(),
		Input:              []appwire.InputItem{{Type: "text", Text: "reader-input"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := sess.ProcessClientMutationStart(context.Background(), nil); err != nil || !ran {
		t.Fatalf("ProcessClientMutationStart: ran=%v err=%v", ran, err)
	}
	sess.Close()
	<-drained

	saved, _, err := appTurnsFromTranscriptFile(sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	live := prepared.turns.Snapshot()
	var liveEnvironment, savedEnvironment *appwire.ThreadItem
	for _, source := range []struct {
		turns []appwire.Turn
		item  **appwire.ThreadItem
	}{{live, &liveEnvironment}, {saved, &savedEnvironment}} {
		for _, turn := range source.turns {
			for _, item := range turn.Items {
				if item.EventKind != appwire.ThreadItemEventKindEnvironment {
					continue
				}
				if *source.item != nil {
					t.Fatal("first input produced duplicate environment context")
				}
				environment := item
				*source.item = &environment
			}
		}
	}
	if liveEnvironment == nil || savedEnvironment == nil {
		t.Fatal("environment missing from live or persisted projection")
	}
	if liveEnvironment.TranscriptKey != savedEnvironment.TranscriptKey || liveEnvironment.TurnID != savedEnvironment.TurnID || !reflect.DeepEqual(liveEnvironment.Position, savedEnvironment.Position) {
		t.Fatal("environment identity differs between live and persisted projections")
	}
	findInput := func(turns []appwire.Turn) appwire.ThreadItem {
		t.Helper()
		for _, turn := range turns {
			for _, item := range turn.Items {
				if item.Type == "userMessage" && item.ClientMutationID == mutationID {
					return item
				}
			}
		}
		t.Fatal("accepted input missing from projection")
		return appwire.ThreadItem{}
	}
	liveItem := findInput(live)
	savedItem := findInput(saved)
	if liveItem.TurnID != savedItem.TurnID || liveItem.TranscriptKey != savedItem.TranscriptKey || !reflect.DeepEqual(liveItem.Position, savedItem.Position) {
		t.Fatalf("input identity changed: live turn=%s key=%s position=%+v; persisted turn=%s key=%s position=%+v", liveItem.TurnID, liveItem.TranscriptKey, liveItem.Position, savedItem.TurnID, savedItem.TranscriptKey, savedItem.Position)
	}
	if liveEnvironment.Position == nil || liveItem.Position == nil || liveEnvironment.Position.Entry >= liveItem.Position.Entry {
		t.Fatal("environment must precede the input it describes")
	}
}
