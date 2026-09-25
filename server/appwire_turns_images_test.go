package server

import (
	"context"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// A recorded input image is in the value every thread/read returns: the read
// path (handleAppThreadRead -> the thread's history -> the transcript index ->
// RegroupTurnFragments) carries the user message's image, and a later entry in
// the same turn leaves it in place. History carries the image's description,
// not its bytes, as every read of a transcript always has.
func TestAppThreadReadKeepsRecordedInputImages(t *testing.T) {
	look := llm.User("look")
	look.Content = append(look.Content, llm.ContentPart{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{1, 2, 3}}})
	st := newServedTranscript(t, NewServer(ServerConfig{}), "01T", schema.NewTurn(schema.TurnUserInput, look))

	assertInputImage := func() {
		t.Helper()
		read, err := st.srv.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:01T", IncludeTurns: true})
		if err != nil {
			t.Fatalf("handleAppThreadRead: %v", err)
		}
		if len(read.Thread.Turns) != 1 || len(read.Thread.Turns[0].Items) == 0 {
			t.Fatalf("turns = %+v, want one turn led by the user message", read.Thread.Turns)
		}
		images := read.Thread.Turns[0].Items[0].Images
		if len(images) != 1 {
			t.Fatalf("Images = %+v, want the recorded image", images)
		}
		if images[0].MediaType != "image/png" {
			t.Fatalf("Images[0] = %+v, want mediaType image/png", images[0])
		}
	}
	assertInputImage()

	// A later entry of the turn says nothing about images; the read still has them.
	st.record(t, schema.NewTurn(schema.TurnAssistant, llm.Assistant("seen")))
	assertInputImage()
}
