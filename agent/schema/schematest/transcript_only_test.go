package schematest

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

func TestInterleaveTranscriptOnlyAddsOnlyTranscriptOnlyEntries(t *testing.T) {
	plain := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("u")),
		schema.NewTurn(schema.TurnAssistant, llm.Assistant("a")),
		schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool}),
	}
	interleaved := InterleaveTranscriptOnly(plain)
	if len(interleaved) != 2*len(plain)+1 {
		t.Fatalf("interleaved %d turns, want %d", len(interleaved), 2*len(plain)+1)
	}
	var kept []schema.Turn
	for i, turn := range interleaved {
		if i%2 == 0 {
			if !turn.Kind.TranscriptOnly() {
				t.Fatalf("position %d holds %s, want a transcript-only sample", i, turn.Kind)
			}
			continue
		}
		kept = append(kept, turn)
	}
	if !reflect.DeepEqual(kept, plain) {
		t.Fatal("removing the samples does not give the input back")
	}
}

func TestTranscriptOnlySamplesCoverEveryKindAndCarryNothingCountable(t *testing.T) {
	kinds := map[schema.TurnKind]bool{}
	notices := map[schema.NoticeKind]bool{}
	for _, sample := range TranscriptOnlySamples() {
		kinds[sample.Kind] = true
		if sample.Notice != nil {
			notices[sample.Notice.Kind] = true
		}
		if !reflect.DeepEqual(sample.Usage, llm.Usage{}) || len(sample.Message.Content) != 0 {
			t.Fatalf("sample %s carries usage or content parts", sample.Kind)
		}
		if sample.Format != schema.TurnFormatIdentity || sample.TurnID == "" {
			t.Fatalf("sample %s lacks identity", sample.Kind)
		}
	}
	for _, kind := range []schema.TurnKind{schema.TurnCompletion, schema.TurnReopen, schema.TurnCommunicate, schema.TurnNotice} {
		if !kinds[kind] {
			t.Errorf("no sample of %s", kind)
		}
	}
	for _, kind := range []schema.NoticeKind{schema.NoticeToolRepair, schema.NoticeGoalEnded, schema.NoticeTurnLimit, schema.NoticeSkillActivated} {
		if !notices[kind] {
			t.Errorf("no notice sample of %s", kind)
		}
	}
}
