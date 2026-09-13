//go:build unix

package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// The hook rendezvous below is a pair of FIFOs, so this case is Unix-only —
// hence the file. The rest of the compaction-replay transcript cases are
// portable and stay in session_compaction_replay_transcript_test.go.

// A fold names the owner of every record it writes, and its PreCompact hook's
// completion is one of them. That name belongs to the fold's OWN run: hooks
// dispatch on a goroutine per hook (hooks.Runner.runAll), so a hook from
// another producer can complete while the fold's hook window is open, and it
// must keep the owner of its own run — whatever turn is executing then, or
// none — instead of inheriting the fold's.
func TestCompactionReplay_HookCompletionBesideAFoldKeepsItsOwnOwner(t *testing.T) {
	t.Parallel()
	// The fold's PreCompact hook announces itself and then waits, so the
	// notification below completes strictly inside the fold's hook window.
	// Both halves are FIFO rendezvous: a reader completes only once the writer
	// arrives, so neither side polls or sleeps.
	rendezvous := t.TempDir()
	started := filepath.Join(rendezvous, "started")
	release := filepath.Join(rendezvous, "release")
	for _, path := range []string{started, release} {
		if err := syscall.Mkfifo(path, 0o600); err != nil {
			t.Fatalf("mkfifo %s: %v", path, err)
		}
	}
	hooks := fmt.Sprintf(`{"hooks":{"PreCompact":[{"matcher":"*","hooks":[{"type":"command","command":"echo open > '%s'; cat '%s' > /dev/null"}]}],"Notification":[{"matcher":"*","hooks":[{"type":"command","command":"true"}]}]}}`, started, release)
	stateDir := t.TempDir()
	s := newScriptedSummaryCompactSession(t, "hook-owner-beside-cheap", func(llm.Request) llm.Response {
		return llm.Response{Message: llm.Assistant("[CONTEXT SUMMARY]\nsummary\n[END SUMMARY]")}
	}, withConfig(SessionConfig{MaxSubagentDepth: 1, NoProjectPrompts: true, StateDir: stateDir, PluginDirs: []string{writePluginHooks(t, "hook-owner-beside-plugin", hooks)}}))
	seedNumberedSessionHistory(t, s, 12)

	compacted := make(chan error, 1)
	go func() { compacted <- s.Compact(context.Background()) }()

	// TRIPWIRE: the hook opens the FIFO as soon as it runs; the ceiling only
	// fires if the fold never reaches its hook at all.
	awaitWithin(t, 30*time.Second, "the fold's PreCompact hook entering its window", func() {
		if _, err := os.ReadFile(started); err != nil {
			t.Errorf("read the hook's start rendezvous: %v", err)
		}
	})

	// Idle beside the fold: nothing is executing, so this completion is owned
	// by nobody.
	s.runNotificationHook(context.Background(), "beside the fold")

	// TRIPWIRE: the hook is already parked on this FIFO; the ceiling only
	// fires if it left without reading.
	awaitWithin(t, 30*time.Second, "releasing the fold's PreCompact hook", func() {
		if err := os.WriteFile(release, []byte("go\n"), 0o600); err != nil {
			t.Errorf("write the hook's release rendezvous: %v", err)
		}
	})
	if err := <-compacted; err != nil {
		t.Fatalf("Compact: %v", err)
	}

	data, err := readTranscriptFull(transcriptPath(s.stateDir, s.id))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	foldOwner := ""
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnSummary || entry.Turn.Kind == schema.TurnCheckpoint {
			foldOwner = entry.Turn.OwningTurnID
		}
	}
	if foldOwner == "" {
		t.Fatal("test setup: the fold wrote no owned marker to compare against")
	}
	notifications := 0
	for _, entry := range data.Entries {
		if entry.Turn.Kind != schema.TurnHookCompleted || entry.Turn.Hook == nil || entry.Turn.Hook.Event != "Notification" {
			continue
		}
		notifications++
		if entry.Turn.OwningTurnID != "" {
			t.Fatalf("a Notification hook completing beside the fold is owned by %q (the fold's own owner is %q), want the owner of its own run: none", entry.Turn.OwningTurnID, foldOwner)
		}
	}
	if notifications == 0 {
		t.Fatal("test setup: no Notification hook completed inside the fold's hook window")
	}
}
