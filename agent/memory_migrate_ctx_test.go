package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
)

// TestMemoryBackupHoldsPropagatesCancellation is MEDIUM 2: the backup read's
// context error must not read as "no matching backup", or migration proceeds to
// create a duplicate.
func TestMemoryBackupHoldsPropagatesCancellation(t *testing.T) {
	t.Parallel()
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	raw := []byte("x")
	if err := os.WriteFile(filepath.Join(env.RootDir, memoryLegacyIndexBackup), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := memoryBackupHolds(ctx, env, env.RootDir, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("memoryBackupHolds(cancelled) err = %v, want context.Canceled", err)
	}
}

// TestBackUpMemoryIndexRefusesUnderCancellation pins that a cancelled call
// creates no backup.
func TestBackUpMemoryIndexRefusesUnderCancellation(t *testing.T) {
	t.Parallel()
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	raw := []byte("index")
	backup := filepath.Join(env.RootDir, memoryLegacyIndexBackup)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := backUpMemoryIndexCtx(ctx, env, env.RootDir, raw, backup); !errors.Is(err, context.Canceled) {
		t.Fatalf("backUpMemoryIndexCtx(cancelled) = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(backup); err == nil {
		t.Fatal("a cancelled backup creation still wrote the backup")
	}
}

// TestRemoveMigratedMemoryIndexRefusesUnderCancellation pins that the index is
// never staged once the call is cancelled: a staged index stays hidden under a
// private name until recovery.
func TestRemoveMigratedMemoryIndexRefusesUnderCancellation(t *testing.T) {
	t.Parallel()
	env := execenv.NewLocalExecutionEnvironment(t.TempDir())
	defer env.Cleanup()
	raw := []byte("index bytes")
	legacy := filepath.Join(env.RootDir, "MEMORY.md")
	if err := os.WriteFile(legacy, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := removeMigratedMemoryIndexCtx(ctx, env, env.RootDir, legacy, raw); !errors.Is(err, context.Canceled) {
		t.Fatalf("removeMigratedMemoryIndexCtx(cancelled) = %v, want context.Canceled", err)
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("a cancelled migration staged the index away: %v", err)
	}
}
