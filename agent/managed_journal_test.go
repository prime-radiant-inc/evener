package agent

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestManagedJournalRetainsIdentityAfterDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.managed.json")
	j, err := openManagedJournal(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("directory sync unavailable")
	j.fault = func(point string) error {
		if point == "directory_sync" {
			return injected
		}
		return nil
	}
	request := managedInvocation{ID: "invocation", SessionID: "session", AttemptGroupID: "attempt", ToolIndex: 0, AssistantSeq: 7, ToolName: "managed", CallID: "call", Arguments: []byte(`{ "n":1e+03 }`), Request: ManagedRequest{Identity: ManagedIdentity{BindingID: "binding", ServiceID: "service", RealmID: "realm", PrincipalID: "principal", NamespaceID: "namespace"}, Operation: "write", MutationID: "mutation", Arguments: json.RawMessage(`{ "n":9007199254740993 }`)}}
	if err := j.put(request); !errors.Is(err, injected) {
		t.Fatalf("save: %v", err)
	}
	if got := j.pending(); len(got) != 1 || got[0].Request.MutationID != "mutation" {
		t.Fatalf("forgot potentially durable operation: %+v", got)
	}
	reopened, err := openManagedJournal(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.pending()
	if len(got) != 1 || string(got[0].Request.Arguments) != `{ "n":9007199254740993 }` {
		t.Fatalf("reopen lost exact request: %+v", got)
	}
	j.fault = nil
	if err := j.establishDurability(); err != nil {
		t.Fatal(err)
	}
	if err := j.remove("invocation"); err != nil {
		t.Fatal(err)
	}
	reopened, err = openManagedJournal(path, "session")
	if err != nil || len(reopened.pending()) != 0 {
		t.Fatalf("settlement: %v", err)
	}
}

func TestManagedJournalRefusesCorruptOrWrongOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.managed.json")
	j, err := openManagedJournal(path, "session")
	if err != nil {
		t.Fatal(err)
	}
	if err := j.put(managedInvocation{ID: "x", SessionID: "session"}); err == nil {
		t.Fatal("admitted incomplete request")
	}
	if err := j.establishDurability(); err != nil {
		t.Fatal(err)
	}
	if _, err := openManagedJournal(path, "other"); err == nil {
		t.Fatal("accepted another session journal")
	}
}
