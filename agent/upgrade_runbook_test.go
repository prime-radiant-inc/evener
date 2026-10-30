package agent

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
	"primeradiant.com/evener/test/e2e/fakellm"
)

func upgradeHashes(t *testing.T, root string) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		hashes[rel] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return hashes
}

func TestUpgradeRunbookPreservesHistoryAndContinuesInFreshRoot(t *testing.T) {
	t.Parallel()
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	client := llm.NewClient(llm.WithRegistry(mustTestRegistry(map[string]registry.Provider{
		"runbook": {Base: "openai-compatible", APIKey: "synthetic-runbook", Transport: registry.Transport{BaseURL: provider.BaseURL()}},
	})))
	profile := testOpenAICompatProfile("runbook", fakellm.ModelID, 100_000)
	workspace := t.TempDir()
	newSession := func(root string) *Session {
		t.Helper()
		session, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
			StateDir: root, ForceRealIO: true,
			testOnly: testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace)},
		})
		if err != nil {
			t.Fatal(err)
		}
		muteNoteElicitation(session)
		t.Cleanup(session.Close)
		return session
	}
	// Only the HTTP model boundary is scripted. A real write_file and communicate
	// execute in the session loop and persist their results before ProcessInput returns.
	completeTask := func(session *Session, filename string) {
		t.Helper()
		// TRIPWIRE: scripted loopback requests and local file writes finish in seconds.
		// provider.Next and done drive completion; 30s only guards against a hang.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := session.ProcessInput(ctx, "runbook-input-"+filename, nil); done <- err }()
		call, err := provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		call.RespondToolCall("write_file", map[string]any{"file_path": filepath.Join(workspace, filename), "content": "runbook-artifact-" + filename})
		call, err = provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		call.RespondToolCall("communicate", map[string]any{"end_turn": true, "message": "runbook-result-" + filename, "output": map[string]any{"message": "", "data": map[string]any{}, "artifacts": []string{}}})
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-ctx.Done():
			t.Fatal("scripted task did not complete")
		}
		data, err := os.ReadFile(filepath.Join(workspace, filename))
		if err != nil || string(data) != "runbook-artifact-"+filename {
			t.Fatal("task did not write its independent artifact")
		}
	}
	original := t.TempDir()
	seed := newSession(original)
	completeTask(seed, "before-upgrade.txt")
	meta := seed.Meta()
	if err := schema.SaveSessionMeta(original, meta); err != nil {
		t.Fatal(err)
	}
	seed.Close()
	before := upgradeHashes(t, original)
	copyRoot := func() string {
		t.Helper()
		root := filepath.Join(t.TempDir(), "copy")
		if err := os.CopyFS(root, os.DirFS(original)); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, upgradeHashes(t, root)) {
			t.Fatal("backup copy differs")
		}
		return root
	}
	compatible := copyRoot()
	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, compatible)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restored.Close)
	muteNoteElicitation(restored)
	completeTask(restored, "compatible.txt")
	restored.Close()
	_, restoredEntries, _, err := readTranscript(context.Background(), schema.SessionTranscriptPath(compatible, meta.ID), "")
	if err != nil {
		t.Fatal(err)
	}
	var restoredInputs []string
	for _, entry := range restoredEntries {
		if entry.Turn.Kind == schema.TurnUserInput {
			restoredInputs = append(restoredInputs, entry.Turn.Message.Text())
		}
	}
	if !reflect.DeepEqual(restoredInputs, []string{"runbook-input-before-upgrade.txt", "runbook-input-compatible.txt"}) {
		t.Fatal("compatible continuation lost or replaced saved user input")
	}

	for _, watch := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy-watch-%t", watch), func(t *testing.T) {
			root := copyRoot()
			removeDelegateStoreIfPresent(t, root, meta.ID)
			events := []jobstore.Event{{Kind: jobstore.EventJobStarted, JobID: "job_legacy_delegate", Type: jobstore.JobType(delegateResourceType), OwnerSessionID: meta.ID}}
			if watch {
				events = append(events, jobstore.Event{Kind: jobstore.EventWatchRegistered, WatchID: "watch_legacy", Watch: &jobstore.WatchEvent{Generation: "wg_legacy", OwnerSessionID: meta.ID, VisibleSessionID: meta.ID, Target: "job_legacy_delegate", SendTo: meta.ID, ConfigHash: "legacy"}})
			}
			appendLegacyBootstrapEvents(t, filepath.Join(jobsDir(root, meta.ID), "jobs.jsonl"), events...)
			restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, root)
			if err != nil {
				t.Fatalf("legacy delegate state must not stop the session restoring: %v", err)
			}
			t.Cleanup(restored.Close)
			muteNoteElicitation(restored)
			completeTask(restored, fmt.Sprintf("legacy-%t.txt", watch))
			restored.Close()
			_, entries, _, err := readTranscript(context.Background(), schema.SessionTranscriptPath(root, meta.ID), "")
			if err != nil {
				t.Fatal(err)
			}
			var inputs []string
			for _, entry := range entries {
				if entry.Turn.Kind == schema.TurnUserInput {
					inputs = append(inputs, entry.Turn.Message.Text())
				}
			}
			want := []string{"runbook-input-before-upgrade.txt", fmt.Sprintf("runbook-input-legacy-%t.txt", watch)}
			if !reflect.DeepEqual(inputs, want) {
				t.Fatalf("restored legacy session inputs = %v, want saved history kept and the new input appended", inputs)
			}
		})
	}
	freshRoot := t.TempDir()
	fresh := newSession(freshRoot)
	if fresh.ID() == meta.ID {
		t.Fatal("fresh task reused incompatible identity")
	}
	completeTask(fresh, "fresh-root.txt")
	fresh.Close()
	_, entries, _, err := readTranscript(context.Background(), schema.SessionTranscriptPath(freshRoot, fresh.ID()), "")
	if err != nil {
		t.Fatal(err)
	}
	inputs := 0
	for _, entry := range entries {
		if entry.Turn.Kind == schema.TurnUserInput {
			inputs++
			if entry.Turn.Message.Text() != "runbook-input-fresh-root.txt" {
				t.Fatal("fresh root imported old history")
			}
		}
	}
	if inputs != 1 {
		t.Fatalf("fresh root user inputs = %d", inputs)
	}
	if !reflect.DeepEqual(before, upgradeHashes(t, original)) {
		t.Fatal("upgrade rehearsal mutated original backup")
	}
	t.Log("legacy delegate and delegate-watch copies restored with the retired state ignored; compatible restore and separate fresh root completed real file-writing tasks through scripted HTTP provider")
}
