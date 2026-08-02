package schema

import "testing"

func TestSaveSessionMetaPreservesObservedBy(t *testing.T) {
	dir := t.TempDir()
	const worker = "02wMz5TxvEMoJEDTDGOTil"
	const observer = "02wMz5TxvCu3kdckfnw0Gh"
	if err := SaveSessionMeta(dir, SessionMeta{ID: worker, ObservedBy: []string{observer}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveSessionMeta(dir, SessionMeta{ID: worker, Name: "new", TurnCount: 2}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadSessionMeta(dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "new" || got.TurnCount != 2 || len(got.ObservedBy) != 1 || got.ObservedBy[0] != observer {
		t.Fatalf("meta = %+v", got)
	}
}

func TestAppendSessionObservedByPreservesFieldsAndDeduplicates(t *testing.T) {
	dir := t.TempDir()
	const worker = "02wMz5TxvEMoJEDTDGOTil"
	if err := SaveSessionMeta(dir, SessionMeta{ID: worker}); err != nil {
		t.Fatal(err)
	}
	for _, observer := range []string{"observer_a", "observer_b", "observer_a"} {
		if err := AppendSessionObservedBy(dir, worker, observer); err != nil {
			t.Fatal(err)
		}
	}
	got, err := LoadSessionMeta(dir, worker)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ObservedBy) != 2 || got.ObservedBy[0] != "observer_a" || got.ObservedBy[1] != "observer_b" {
		t.Fatalf("ObservedBy = %q, want [observer_a observer_b]", got.ObservedBy)
	}
}
