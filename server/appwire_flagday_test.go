package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestFlagDayDefaultThreadReadIsBoundedItems(t *testing.T) {
	srv := seedTranscriptServer(t, 25)
	response, err := srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{
		Ref:          "local:th_1",
		IncludeTurns: true,
	})
	if err != nil {
		t.Fatalf("handleAppThreadRead: %v", err)
	}
	items := 0
	for _, turn := range response.Thread.Turns {
		items += len(turn.Items)
		for _, item := range turn.Items {
			if item.Position == nil || item.TranscriptKey == "" {
				t.Fatalf("item missing mandatory position/key: %+v", item)
			}
		}
	}
	if items > appwire.TranscriptItemPageLimit {
		t.Fatalf("default thread/read returned %d items, want at most %d", items, appwire.TranscriptItemPageLimit)
	}
	if response.OlderCursor == "" {
		t.Fatal("bounded default thread/read returned empty older cursor")
	}
}
