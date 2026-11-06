package agent

import (
	"slices"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestConversationProvenance_AdmissionAndQueueRetirementAreAuthoritative(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	sampler, ok := any(s).(interface {
		ConversationProvenance() *appwire.ConversationProvenanceState
	})
	if !ok {
		t.Fatal("runtime has no authoritative conversation provenance snapshot")
	}
	before := sampler.ConversationProvenance()
	response, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "waiting-input", Input: []appwire.InputItem{{Type: "text", Text: "held until selected"}}})
	if err != nil {
		t.Fatal(err)
	}
	admitted := sampler.ConversationProvenance()
	if !admitted.Authoritative || admitted.Version != 1 || admitted.Revision <= before.Revision || !slices.Contains(admitted.AdmittedInputs, "client:waiting-input") || admitted.ConsumedFrontierID != "" {
		t.Fatalf("admission confused with consumption: %+v", admitted)
	}
	_, err = s.AcceptClientMutationCancelQueued(appwire.TurnCancelQueuedParams{ClientMutationID: "cancel-input", Index: 0, ExpectedEntryID: response.Receipt.QueueEntryIDs[0]})
	if err != nil {
		t.Fatal(err)
	}
	retired := sampler.ConversationProvenance()
	if retired.Revision <= admitted.Revision || !slices.Contains(retired.RetiredInputs, "client:waiting-input") || !slices.Contains(retired.AdmittedInputs, "client:waiting-input") {
		t.Fatalf("explicit removal lost identity or retained impossible fence: %+v", retired)
	}
}

func TestConversationProvenance_LegacyPendingIdentityIsUnknown(t *testing.T) {
	t.Parallel()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(t.TempDir()))
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "old", Input: []appwire.InputItem{{Type: "text", Text: "old pending input"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.clientMutations.mutate(func(snapshot *clientMutationSnapshot) error {
		record := snapshot.Journal["old"]
		record.ConversationInputIDs = nil
		snapshot.Journal["old"] = record
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if state := s.ConversationProvenance(); state.Authoritative {
		t.Fatalf("historical accepted input without identity synthesized known-empty: %+v", state)
	}
}
