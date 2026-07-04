package plugins

import (
	"context"
	"os"
	"testing"
)

func TestList_FlagsBroken(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t)
	m := NewManager(t.TempDir())
	m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo})
	entry, _ := m.Install(context.Background(), "widget", name)

	items, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Plugin != "widget" || items[0].Broken {
		t.Fatalf("List = %+v, want one healthy widget", items)
	}

	// Corrupt the installed plugin on disk → List must flag it broken.
	os.RemoveAll(entry.InstallPath)
	items, _ = m.List()
	if !items[0].Broken {
		t.Fatal("List did not flag a missing install dir as broken")
	}
}
