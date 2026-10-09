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

	"primeradiant.com/evener/agent/execenv"
)

// memoryMigrationScope is a confined scope environment for migration tests
// and the scope's directory on disk.
func memoryMigrationScope(t *testing.T) (*execenv.LocalExecutionEnvironment, string) {
	t.Helper()
	root := t.TempDir()
	env, err := execenv.NewConfinedFileEnvironment(root, filepath.Join("memory", "personal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(env.Cleanup)
	return env, filepath.Join(root, "memory", "personal")
}

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

// memoryBackupCount counts a scope's backups of hand-written indexes.
func memoryBackupCount(t *testing.T, scope string) int {
	t.Helper()
	entries, err := os.ReadDir(scope)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), memoryLegacyIndexBackup) {
			n++
		}
	}
	return n
}

// memoryMaxBackups is the most index backups a scope should keep however
// many times old and new builds alternate on it: the original index and,
// at most, the latest one an old build wrote.
const memoryMaxBackups = 2

// An old build and a new build take turns on one scope five times: each old
// round adds pages and writes a fresh MEMORY.md, each new round migrates it.
// Every old line's description must reach its page, the first backup must
// keep the original index's bytes, and the backups must not pile up.
func TestMigrateMemoryScopeAlternatingWithAnOldBuild(t *testing.T) {
	t.Parallel()
	env, scope := memoryMigrationScope(t)
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
		if n := memoryBackupCount(t, scope); n > memoryMaxBackups {
			t.Errorf("round %d: %d index backups in the scope, want at most %d", round, n, memoryMaxBackups)
		}
	}
}

// Two migrations race on one scope while an old build writes MEMORY.md
// again. Migration B chooses its backup name, migration A then finishes
// (renaming the original index to that same name), the old build recreates
// MEMORY.md, and B renames what is now MEMORY.md onto its chosen name.
// migrateMemoryScope has no seam between choosing the name and renaming, so
// this test replays B's last statement, freeMemoryBackupPath then
// RenamePath, in that interleaving. The original index must survive, and the
// recreated index's line must reach its page or stay for the next run.
func TestMigrateMemoryScopeRaceKeepsTheOriginalBackup(t *testing.T) {
	t.Parallel()
	env, scope := memoryMigrationScope(t)
	original, _ := oldBuildRound(t, scope, 1)
	legacy := filepath.Join(scope, memoryIndexFile)

	// B has written its descriptions and chooses where the index goes.
	backupB := freeMemoryBackupPath(env, scope)
	// A runs to completion: the same descriptions, then the rename.
	if err := migrateMemoryScope(env); err != nil {
		t.Fatal(err)
	}
	// The old build finds no MEMORY.md and writes a fresh one.
	_, recreated := oldBuildRound(t, scope, 2)
	// B renames onto the name it chose before A's rename.
	if err := env.RenamePath(legacy, backupB); err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}

	backup, err := os.ReadFile(filepath.Join(scope, memoryLegacyIndexBackup))
	if err != nil || string(backup) != original {
		t.Errorf("first backup=%q err=%v, want the original index %q", backup, err, original)
	}
	_, indexErr := os.Stat(legacy)
	for page, description := range recreated {
		raw, err := os.ReadFile(filepath.Join(scope, page))
		if err != nil {
			t.Fatal(err)
		}
		if got := parseMemoryPage(page, raw, time.Time{}); got.Description != description && indexErr != nil {
			t.Errorf("%s description=%q and MEMORY.md is gone (%v): its line %q is lost", page, got.Description, indexErr, description)
		}
	}
}
