package doctor

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
)

// writeFile writes content to path, creating parent dirs. Test-only helper.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeDelegateEvents(t *testing.T, delegatesPath string, events []delegatestore.Event) {
	t.Helper()
	store, err := delegatestore.Open(delegatesPath)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AppendBatch(state, events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}
