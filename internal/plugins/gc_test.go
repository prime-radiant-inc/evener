package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGc_NoCacheDirYet(t *testing.T) {
	m := NewManager(t.TempDir())
	removed, err := m.Gc()
	if err != nil {
		t.Fatalf("Gc on empty store: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Gc removed = %v, want none", removed)
	}
}

func TestGc_RemovesOrphanedKeepsReferenced(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, pluginRepo := makeGitBackedMarketplace(t, "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceURL, URL: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	first, err := m.Install(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Advance the plugin's upstream HEAD and upgrade — Upgrade never deletes,
	// so the first sha-dir is left orphaned once the registry repoints.
	advanceGitRepo(t, pluginRepo, "extra.txt", "v2")
	second, err := m.Upgrade(context.Background(), "widget", "acme")
	if err != nil {
		t.Fatalf("Upgrade: %v", err)
	}
	if first.InstallPath == second.InstallPath {
		t.Fatal("test setup: upgrade did not move to a new sha-dir")
	}
	if _, err := os.Stat(first.InstallPath); err != nil {
		t.Fatalf("test setup: old sha-dir missing before gc: %v", err)
	}

	removed, err := m.Gc()
	if err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if len(removed) != 1 || removed[0] != first.InstallPath {
		t.Fatalf("Gc removed = %v, want [%s]", removed, first.InstallPath)
	}
	if _, err := os.Stat(first.InstallPath); !os.IsNotExist(err) {
		t.Fatal("orphaned sha-dir not removed by Gc")
	}
	if _, err := os.Stat(second.InstallPath); err != nil {
		t.Fatalf("referenced sha-dir removed by Gc: %v", err)
	}

	// A second sweep with nothing new to reclaim is a no-op.
	removed, err = m.Gc()
	if err != nil {
		t.Fatalf("second Gc: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("second Gc removed = %v, want none", removed)
	}
}

func TestGc_DirectorySourceInstallPathNeverConsidered(t *testing.T) {
	if !gitAvailable() {
		t.Skip("git not available")
	}
	mktRepo, name := makeInstallableMarketplace(t) // "widget" is a "./plugins/widget" relative source
	pluginDir := filepath.Join(mktRepo, "plugins", "widget")

	m := NewManager(t.TempDir())
	if _, err := m.AddMarketplace(context.Background(), "", Source{Kind: SourceDirectory, Path: mktRepo}); err != nil {
		t.Fatalf("AddMarketplace: %v", err)
	}
	entry, err := m.Install(context.Background(), "widget", name)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if entry.InstallPath != pluginDir {
		t.Fatalf("InstallPath = %q, want %q (referenced in place)", entry.InstallPath, pluginDir)
	}

	if _, err := m.Gc(); err != nil {
		t.Fatalf("Gc: %v", err)
	}
	if _, err := os.Stat(pluginDir); err != nil {
		t.Fatalf("Gc touched a directory-source install path: %v", err)
	}
}
