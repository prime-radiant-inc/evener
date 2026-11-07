package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestConversationInputIDsIsolation(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	if got := s.conversationInputIDs("missing"); got != nil {
		t.Fatalf("missing mutation returned IDs: %v", got)
	}
	if _, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "input-719", Input: []appwire.InputItem{{Type: "text", Text: "opaque-719"}},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"client:input-719"}
	first := s.conversationInputIDs("input-719")
	second := s.conversationInputIDs("input-719")
	if !slices.Equal(first, want) || !slices.Equal(second, want) {
		t.Fatalf("admitted input IDs: %v, %v", first, second)
	}
	first[0] = "changed-831"
	if !slices.Equal(second, want) || !slices.Equal(s.conversationInputIDs("input-719"), want) || !slices.Equal(s.clientMutations.snapshot().Journal["input-719"].ConversationInputIDs, want) {
		t.Fatal("caller mutation changed another read or the committed journal")
	}
	if got := s.conversationInputIDs("missing"); got != nil {
		t.Fatalf("missing journal record returned IDs: %v", got)
	}
}

func TestConversationInputIDsWithoutMutation(t *testing.T) {
	t.Parallel()
	s := &Session{}
	if got := s.conversationInputIDs("missing"); got != nil {
		t.Fatalf("absent store returned IDs: %v", got)
	}
	first := s.conversationInputIDs("")
	second := s.conversationInputIDs("")
	if len(first) != 1 || len(second) != 1 || first[0] == "" || second[0] == "" || first[0] == second[0] {
		t.Fatalf("direct inputs lack distinct identities: %v, %v", first, second)
	}
}
