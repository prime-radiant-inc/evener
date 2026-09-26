package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

// flakySidecarDirSync swaps the sidecar save's directory-sync seam for one
// that fails the calls fail says to (counted across the whole test) with the
// same error shape the real seam's sync failure produces, and runs the real
// sync for the rest. Tests use it to force the post-rename failure path —
// the one point where a failed save has already replaced the sidecar file —
// deterministically, on any filesystem, including inside the compensation
// save a caller issues right after.
func flakySidecarDirSync(t *testing.T, fail func(call int) bool) {
	t.Helper()
	saved := hostSidecarSyncDir
	var call int
	hostSidecarSyncDir = func(dir string) error {
		call++
		if fail(call) {
			return fmt.Errorf("host sidecar directory sync: %w", syscall.EIO)
		}
		return saved(dir)
	}
	t.Cleanup(func() { hostSidecarSyncDir = saved })
}

// sidecarEntryNames reads the sidecar file the way the next start does, so a
// test can assert the durable state directly.
func sidecarEntryNames(t *testing.T, configPath string) []string {
	t.Helper()
	entries, err := loadHostSidecar(sidecarPathFor(configPath))
	if err != nil {
		t.Fatalf("loadHostSidecar: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names
}

// TestHostManageAddSaveFailureAfterRenameCommitsNothing pins the post-rename
// half of the durable-first discipline: the rename that replaces the sidecar
// file is the save's commit point, so a directory-sync failure behind it must
// not be reported as though nothing was written. The file already holds the
// entry while nothing is live, and a plain refusal would leave the next start
// resurrecting an add the API reported as failed. The refusal must
// compensate: the file is restored to the pre-add contents, disk and the live
// set agree, and the caller can retry.
func TestHostManageAddSaveFailureAfterRenameCommitsNothing(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, nil, nil)

	// The add's own save fails behind its rename; the compensation save that
	// follows runs the real sync.
	flakySidecarDirSync(t, func(call int) bool { return call == 1 })
	_, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "side", Address: "s.example"}})
	if err == nil {
		t.Fatal("Add over a failing directory sync succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "host sidecar directory sync") {
		t.Fatalf("Add error = %q, want the save's refusal", err)
	}
	// The live set committed nothing...
	if _, ok := m.cfg.hosts.Get("side"); ok {
		t.Fatal("Add exposed a registry entry the save never completed")
	}
	if m.cfg.sidecar.isSidecar("side") {
		t.Fatal("Add recorded a sidecar row the save never completed")
	}
	if _, ok := sources.Source("side"); ok {
		t.Fatal("Add registered a source the save never completed")
	}
	// ...and neither does the file: the refusal restored the pre-add
	// contents, so the next start cannot resurrect the refused add.
	if names := sidecarEntryNames(t, configPath); slices.Contains(names, "side") {
		t.Fatalf("the refused add survived in the sidecar file: %v", names)
	}
}

// TestHostManageRemoveSaveFailureAfterRenameKeepsTheHost pins the removal's
// mirror of the post-rename contract: once the rename replaced the file, the
// entry is already gone from disk while the live set still holds the host,
// and a plain refusal would leave the next start losing a host whose removal
// the API reported as failed. The refusal must compensate: the file regains
// the live contents, disk and the live set agree, and the caller can retry.
func TestHostManageRemoveSaveFailureAfterRenameKeepsTheHost(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "keep", Address: "k.example"}}); err != nil {
		t.Fatalf("Add(keep) = %v, want success", err)
	}

	// The removal's own save fails behind its rename; the compensation save
	// that follows runs the real sync.
	flakySidecarDirSync(t, func(call int) bool { return call == 1 })
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "keep"}); err == nil {
		t.Fatal("Remove over a failing directory sync succeeded, want refusal")
	}
	// The live set still holds the host fully intact...
	if _, ok := m.cfg.hosts.Get("keep"); !ok {
		t.Fatal("Remove dropped the registry entry the save never completed")
	}
	if !m.cfg.sidecar.isSidecar("keep") {
		t.Fatal("Remove dropped the sidecar row the save never completed")
	}
	if _, ok := sources.Source("keep"); !ok {
		t.Fatal("Remove dropped the source the save never completed")
	}
	// ...and so does the file: the refusal restored the live contents, so
	// the next start cannot lose a host whose removal reported as failed.
	if names := sidecarEntryNames(t, configPath); !slices.Contains(names, "keep") {
		t.Fatalf("the refused removal lost the host in the sidecar file: %v", names)
	}
	// The refusal left the host fully retryable: a later removal, with the
	// directory sync healthy again, completes.
	resp, err := m.Status(context.Background(), appwire.HostStatusParams{Name: "keep"})
	if err != nil {
		t.Fatalf("Status(keep) after the refused remove = %v, want the host intact", err)
	}
	if resp.Host.Removed {
		t.Fatalf("row = %+v, want the host still present", resp.Host)
	}
	if _, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "keep"}); err != nil {
		t.Fatalf("Remove(keep) retry = %v, want success", err)
	}
	if names := sidecarEntryNames(t, configPath); slices.Contains(names, "keep") {
		t.Fatalf("the retried removal did not take: %v", names)
	}
}

// TestHostManageSidecarRollbackLandsWhenItsOwnSyncFails pins the
// compensation's own post-rename corner: the rollback is itself an atomic
// save, so its directory sync can fail behind a rename that already restored
// the live contents. That failure must be read as "the rollback landed, only
// its durability step failed" — the file holds what the rollback wanted — not
// as a rollback failure that never happened.
func TestHostManageSidecarRollbackLandsWhenItsOwnSyncFails(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(configPath, []byte(""), 0o600); err != nil {
		t.Fatalf("write hub.toml: %v", err)
	}
	sources := appsource.NewRegistry()
	m := newHubHostManager(sources, nil, hubcore.WebConfig{}, configPath, nil, nil)
	if _, err := m.Add(context.Background(), appwire.HostAddParams{Entry: appwire.HostEntry{Name: "keep", Address: "k.example"}}); err != nil {
		t.Fatalf("Add(keep) = %v, want success", err)
	}

	// The removal's save fails behind its rename, and so does the
	// compensation save it owes — the second rename still lands.
	flakySidecarDirSync(t, func(call int) bool { return call == 1 || call == 2 })
	_, err := m.Remove(context.Background(), appwire.HostRemoveParams{Name: "keep"})
	if err == nil {
		t.Fatal("Remove over a failing directory sync succeeded, want refusal")
	}
	if !strings.Contains(err.Error(), "rollback landed") {
		t.Fatalf("Remove error = %q, want the rollback-landed report beside the cause", err)
	}
	// Both sides still hold the host: the live set because the removal
	// refused, the file because the rollback's rename restored it.
	if _, ok := m.cfg.hosts.Get("keep"); !ok {
		t.Fatal("Remove dropped the registry entry the save never completed")
	}
	if names := sidecarEntryNames(t, configPath); !slices.Contains(names, "keep") {
		t.Fatalf("the rollback did not land: the sidecar file lost the host: %v", names)
	}
}
