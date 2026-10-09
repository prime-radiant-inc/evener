//go:build unix

package agent

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// oldBuildRound plays an older Evener build's session in scope: it writes
// three pages with no frontmatter and, finding no MEMORY.md, a fresh
// hand-written index that lists only those pages. It returns the index's
// bytes and the description each page's line gives it.
func oldBuildRound(t *testing.T, scope string, round int) (string, map[string]string) {
	t.Helper()
	descriptions := make(map[string]string)
	var index strings.Builder
	index.WriteString("# Memory\n\n")
	for k := range 3 {
		page := fmt.Sprintf("old-%d-%d.md", round, k)
		description := fmt.Sprintf("old build round %d fact %d", round, k)
		if err := os.WriteFile(filepath.Join(scope, page), fmt.Appendf(nil, "# Fact %d.%d\n\nbody\n", round, k), 0o600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&index, "- [Fact %d.%d](%s) — %s\n", round, k, page, description)
		descriptions[page] = description
	}
	if err := os.WriteFile(filepath.Join(scope, memoryIndexFile), []byte(index.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return index.String(), descriptions
}

// An old build and a new build take turns on one scope five times: each old
// round adds pages and writes a fresh MEMORY.md, each new round migrates it.
// Every old line's description must reach its page, the first backup must
// keep the original index's bytes, every index an old build wrote must keep a
// backup, and no two backups may hold the same bytes. Each round also writes
// the previous round's index again, which must add no backup.
func TestMigrateMemoryScopeAlternatingWithAnOldBuild(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	want := make(map[string]string)
	var original string
	for round := 1; round <= 5; round++ {
		index, described := oldBuildRound(t, scope, round)
		if round == 1 {
			original = index
		}
		maps.Copy(want, described)
		if err := migrateMemoryScope(env); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		for page, description := range want {
			raw, err := os.ReadFile(filepath.Join(scope, page))
			if err != nil {
				t.Fatalf("round %d: %s: %v", round, page, err)
			}
			if got := parseMemoryPage(page, raw, time.Time{}); !got.HasDescription || got.Description != description {
				t.Fatalf("round %d: %s description=%q (has=%t), want %q", round, page, got.Description, got.HasDescription, description)
			}
		}
		if backup, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup)); err != nil || string(backup) != original {
			t.Errorf("round %d: first backup=%q err=%v, want the original index %q", round, backup, err, original)
		}
		backups := memoryBackups(t, scope)
		if len(backups) != round {
			t.Errorf("round %d: %d index backups, want one per distinct index: %q", round, len(backups), backups)
		}
		seen := make(map[string]string)
		for name, raw := range backups {
			if other, dup := seen[raw]; dup {
				t.Errorf("round %d: backups %s and %s hold the same bytes", round, name, other)
			}
			seen[raw] = name
		}
		// An old build writing the same index again adds no backup.
		if err := os.WriteFile(filepath.Join(scope, memoryIndexFile), []byte(index), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := migrateMemoryScope(env); err != nil {
			t.Fatalf("round %d repeat: %v", round, err)
		}
		if again := memoryBackups(t, scope); !maps.Equal(again, backups) {
			t.Errorf("round %d: migrating a repeated index changed the backups to %q", round, again)
		}
		if _, err := os.Stat(filepath.Join(scope, memoryIndexFile)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("round %d: repeated MEMORY.md was not removed: %v", round, err)
		}
	}
}

// Two migrations race on one scope while an old build writes MEMORY.md
// again. Migration B chooses its backup name (the first, still free),
// migration A then finishes (moving the original index to that name), the
// old build recreates MEMORY.md, and B moves what is now MEMORY.md to the
// name it chose. The test drives B's move with the name B chose before A ran.
// The original index must survive, and the recreated index's lines, which B
// never migrated, must stay in MEMORY.md for the next run.
func TestMigrateMemoryScopeRaceKeepsTheOriginalBackup(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	original, _ := oldBuildRound(t, scope, 1)
	legacy := filepath.Join(scope, memoryIndexFile)

	// B has written its descriptions and chooses its backup name.
	backupB, err := freeMemoryBackupPath(env, scope)
	if err != nil {
		t.Fatal(err)
	}
	// A runs to completion: the same descriptions, then the move.
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	// The old build finds no MEMORY.md and writes a fresh one.
	recreatedIndex, _ := oldBuildRound(t, scope, 2)
	// B moves the index it read, the original, to the name it chose before
	// A's move.
	if err := moveLegacyMemoryIndex(env, scope, legacy, []byte(original), backupB); !errors.Is(err, errLegacyMemoryIndexChanged) {
		t.Fatalf("move of a rewritten index returned %v, want errLegacyMemoryIndexChanged", err)
	}

	backup, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup))
	if err != nil || string(backup) != original {
		t.Errorf("first backup=%q err=%v, want the original index %q", backup, err, original)
	}
	if index, err := os.ReadFile(legacy); err != nil || string(index) != recreatedIndex {
		t.Errorf("MEMORY.md=%q err=%v, want the recreated index left for the next run", index, err)
	}
	if backups := memoryBackups(t, scope); len(backups) != 1 {
		t.Errorf("index backups %q, want only the original", backups)
	}
}

// A backup name taken after it was chosen, here by a different index, is
// never replaced: the move links the index to the next free name instead.
func TestMoveLegacyMemoryIndexSkipsATakenBackupName(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	index, earlier := "- [a](a.md) — this index\n", "- [b](b.md) — an earlier index\n"
	legacy := filepath.Join(scope, memoryIndexFile)
	chosen := filepath.Join(scope, memoryLegacyIndexBackup)
	for path, body := range map[string]string{legacy: index, chosen: earlier} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := moveLegacyMemoryIndex(env, scope, legacy, []byte(index), chosen); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{memoryLegacyIndexBackup: earlier, memoryLegacyIndexBackup + ".2": index}
	if got := memoryBackups(t, scope); !maps.Equal(got, want) {
		t.Fatalf("backups %q, want %q", got, want)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("MEMORY.md kept after its move: %v", err)
	}
}

// An index rewritten after migration read it is captured by the staging
// rename, found to differ, and put back as MEMORY.md for the next run; no
// staging file is left behind.
func TestRemoveMigratedMemoryIndexRestoresARewrittenIndex(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	legacy := filepath.Join(scope, memoryIndexFile)
	rewritten := "- [b](b.md) — written after the read\n"
	if err := os.WriteFile(legacy, []byte(rewritten), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeMigratedMemoryIndex(env, scope, legacy, []byte("- [a](a.md) — what migration read\n")); !errors.Is(err, errLegacyMemoryIndexChanged) {
		t.Fatalf("removal of a rewritten index returned %v, want errLegacyMemoryIndexChanged", err)
	}
	if raw, err := os.ReadFile(legacy); err != nil || string(raw) != rewritten {
		t.Fatalf("MEMORY.md=%q err=%v, want the rewritten index back", raw, err)
	}
	entries, err := os.ReadDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), memoryIndexStagingPrefix) {
			t.Fatalf("staging file %s left behind", entry.Name())
		}
	}
}

// When the index cannot be linked to its backup name for a reason other
// than a taken name (here a read-only scope directory; a filesystem without
// hard links fails the same way), migration reports the error and keeps
// MEMORY.md, so a later run can still migrate it. Nothing is backed up.
func TestMigrateMemoryScopeKeepsTheIndexWhenTheLinkFails(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory modes")
	}
	env, scope := newMemoryMigrateScope(t)
	index := "- [Fact](fact.md) — the fact\n"
	for name, body := range map[string]string{memoryIndexFile: index, "fact.md": "---\ndescription: already described\n---\n"} {
		if err := os.WriteFile(filepath.Join(scope, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(scope, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(scope, 0o700) })
	err := migrateMemoryScope(env)
	if err == nil || errors.Is(err, fs.ErrExist) {
		t.Fatalf("migration with an unlinkable index returned %v, want the link error", err)
	}
	if raw, err := os.ReadFile(filepath.Join(scope, memoryIndexFile)); err != nil || string(raw) != index {
		t.Fatalf("MEMORY.md=%q err=%v, want it kept", raw, err)
	}
	if backups := memoryBackups(t, scope); len(backups) != 0 {
		t.Fatalf("backups %q, want none", backups)
	}
}
