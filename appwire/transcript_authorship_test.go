package appwire

import "testing"

func TestTranscriptAuthorshipClone(t *testing.T) {
	original := Thread{Turns: []Turn{{Items: []ThreadItem{{Authorship: &TranscriptAuthorship{Author: TranscriptAuthor{Kind: TranscriptAuthorAgent, SessionRef: "local:parent"}, Inherited: true}}}}}}
	clonedThread := CloneThread(original)
	clonedThread.Turns[0].Items[0].Authorship.Author.SessionRef = "local:changed"
	if original.Turns[0].Items[0].Authorship.Author.SessionRef != "local:parent" {
		t.Fatal("clone shares author pointer")
	}
	overlay := OverlayItem{Item: original.Turns[0].Items[0]}
	cloned := CloneOverlayItem(overlay)
	cloned.Item.Authorship.Inherited = false
	if !overlay.Item.Authorship.Inherited {
		t.Fatal("overlay clone shares authorship pointer")
	}
	if CloneThread(Thread{Turns: []Turn{{Items: []ThreadItem{{}}}}}).Turns[0].Items[0].Authorship != nil {
		t.Fatal("clone invented metadata")
	}
}
