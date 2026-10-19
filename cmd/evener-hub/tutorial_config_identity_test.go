package hub

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// A held provider request proves a live launch, not that OriginalPrompt has
// reached meta.json. Holding the external naming reply makes that gap stable.
func TestTutorialConfigRunningIdentityBeforePromptSave(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real hub and daemon")
	}
	p, stack, _ := dailyStack(t, false)
	release := p.HoldNamer()
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client := stack.dialRPC(ctx, t)
	if err := os.WriteFile(filepath.Join(stack.workDir, "input.json"), []byte(configInput), 0600); err != nil {
		t.Fatal(err)
	}
	// The second launch shares a workspace with the completed first one.
	// Selecting a stale list row instead of the held active session must fail.
	for _, rounds := range []int{1, 4} {
		prompt := "config-identity-7c924b-" + strconv.Itoa(rounds)
		started, err := clientRequest[appwire.ThreadStartResponse](ctx, client, appwire.MethodThreadStart, appwire.ThreadStartParams{
			Harness: "evener", CWD: stack.workDir, Model: stack.model,
			Input:           []appwire.InputItem{{Type: "text", Text: prompt}},
			LaunchOverrides: &appwire.LaunchConfigLayer{Sandbox: "off", MaxRounds: &rounds},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancelShutdown()
			if _, err := clientRequest[appwire.EmptyResponse](shutdownCtx, client, appwire.MethodThreadShutdown, appwire.ThreadShutdownParams{Ref: started.Thread.Evener.Ref}); err != nil {
				t.Error(err)
			}
		})
		call := dailyNext(ctx, t, p)
		if dailyUserCount(call, prompt) != 1 {
			t.Fatal("provider did not receive launch input")
		}
		awaitActiveTurn(ctx, t, client, started.Thread.Evener.Ref, "")
		state := filepath.Join(stack.home, "state")
		var disk *schema.SessionMeta
		err = filepath.WalkDir(state, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".meta.json") {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			var meta schema.SessionMeta
			if err := json.Unmarshal(b, &meta); err != nil {
				return err
			}
			if meta.ID == started.Thread.SessionID {
				disk = &meta
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if disk == nil || disk.OriginalPrompt != "" {
			t.Fatalf("expected pre-input disk snapshot, got %+v", disk)
		}
		t.Logf("live session exists and is active, disk OriginalPrompt=%q", disk.OriginalPrompt)
		thread := configRunningThread(ctx, t, client, stack.workDir, prompt, call)
		meta, _ := configMeta(t, state, thread.SessionID)
		if meta.ID != started.Thread.SessionID || meta.Config.MaxToolRoundsPerInput != rounds {
			t.Fatal("wrong effective launch")
		}
		stopScript := configScript(ctx, t, p, call)
		awaitThread(ctx, t, client, started.Thread.Evener.Ref, "completed", func(thread appwire.Thread) bool {
			return thread.Evener.ActiveTurnID == "" && thread.Status.Type != "active"
		})
		stopScript()
		configOutcome(t, state, stack.workDir, thread.SessionID, prompt, rounds)
	}
}
