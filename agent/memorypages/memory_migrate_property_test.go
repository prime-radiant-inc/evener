package memorypages

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
)

// memoryRootNames lists a scope's entries for a failure message.
func memoryRootNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

// TestRemoveMigratedMemoryIndexRecoversFromCancellation is MEDIUM 3 (round 7):
// round 6's check covered only a call already cancelled BEFORE the rename, so a
// cancellation landing during the read that follows it returned with the index
// still hidden under its private staging name until another migration ran. The
// property is enumerated over every cancellation point: the scope's index is
// never reachable only under that private name.
func TestRemoveMigratedMemoryIndexRecoversFromCancellation(t *testing.T) {
	t.Parallel()
	// n=0 cancels at the check before the rename, so nothing is staged; n=1
	// passes that check and cancels at the first Err inside the post-rename
	// read, which is the point round 6's fix missed; larger n cancels later, if
	// the call makes that many checks at all.
	for cancelAt := 0; cancelAt <= 4; cancelAt++ {
		env, scope := newMemoryMigrateScope(t)
		raw := []byte("index bytes")
		legacy := filepath.Join(scope, "MEMORY.md")
		if err := os.WriteFile(legacy, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		ctx := &agenttest.CountdownContext{Context: context.Background(), Allow: int32(cancelAt)}
		err := removeMigratedMemoryIndexCtx(ctx, env, scope, legacy, raw)
		names := memoryRootNames(t, scope)
		staged := false
		for _, name := range names {
			if strings.HasPrefix(name, memoryIndexStagingPrefix) {
				staged = true
			}
		}
		if _, statErr := os.Stat(legacy); statErr != nil && staged {
			t.Fatalf("cancel point %d left the index only under its staging name: err=%v root=%v",
				cancelAt, err, names)
		}
		// n=1 is the post-rename read: the check before the rename passed, so
		// this is the point the round-6 fix missed.
		if cancelAt == 1 {
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel point 2 did not reach the read after the rename: err=%v", err)
			}
			if _, statErr := os.Stat(legacy); statErr != nil {
				t.Fatalf("a cancellation during the post-rename read left MEMORY.md hidden: %v (root=%v)",
					statErr, names)
			}
		}
	}
}

// TestRestoreStagedMemoryIndexKeepsAConcurrentReplacement is the no-clobber half
// of MEDIUM 3: putting a staged index back must not overwrite a rewrite that
// landed meanwhile, and the staged file is kept for the next run when it cannot
// go back.
func TestRestoreStagedMemoryIndexKeepsAConcurrentReplacement(t *testing.T) {
	t.Parallel()
	env, scope := newMemoryMigrateScope(t)
	staged := filepath.Join(scope, memoryIndexStagingPrefix+"test")
	legacy := filepath.Join(scope, "MEMORY.md")
	if err := os.WriteFile(staged, []byte("staged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacy, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := restoreStagedMemoryIndex(env, staged, legacy); err != nil {
		t.Fatalf("restoreStagedMemoryIndex = %v, want nil: the name being taken is not a failure", err)
	}
	if got, err := os.ReadFile(legacy); err != nil || string(got) != "replacement" {
		t.Fatalf("the restore clobbered a concurrent replacement: %q, %v", got, err)
	}
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("the staged file was not kept for the next run: %v", err)
	}
}
