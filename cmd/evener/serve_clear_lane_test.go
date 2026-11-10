//go:build unix

package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/execenv"
	agentprovider "primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener/internal/rvreg"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
)

// TestRunServeClearKeepsTheWorktreeLane drives the exact #2957 path on a real
// daemon: the live session enters a managed worktree through manage_worktree,
// then thread/clear replaces it. The replacement must be built in the lane,
// adopt its path/restore root, take over its occupancy lock, and publish the
// lane as the working directory on the server and in the rendezvous entry.
func TestRunServeClearKeepsTheWorktreeLane(t *testing.T) {
	repo := newClearLaneRepo(t)
	stateDir := t.TempDir()
	runDir := t.TempDir()

	scripted := &scriptedProvider{
		name: "openai",
		steps: []func(llm.Request) llm.Response{
			func(llm.Request) llm.Response { return scriptedToolCalls(manageWorktreeCreateCall(t, "lane")) },
			func(llm.Request) llm.Response { return scriptedCommunicate("entered the lane") },
		},
	}
	installServeScriptedProvider(t, scripted)

	deps, state, _ := newClearServeDeps(t)
	deps.newClient = func(string, io.Writer) (*llm.Client, func() error, error) {
		client, err := serveLoadClient("")
		return client, func() error { return nil }, err
	}
	innerClearSession := deps.newClearSession
	var clearEnvDir string
	deps.newClearSession = func(client *llm.Client, profile *agentprovider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		clearEnvDir = env.WorkingDirectory()
		return innerClearSession(client, profile, env, cfg)
	}
	innerUpdateIdentity := deps.updateIdentity
	var updatedEntry rendezvous.Entry
	var updatedEntries int
	var lane string
	deps.updateIdentity = func(reg *rvreg.Registration, id, workingDir string) error {
		err := innerUpdateIdentity(reg, id, workingDir)
		if err == nil {
			if entries, listErr := rendezvous.List(runDir); listErr == nil {
				updatedEntries = len(entries)
				if len(entries) == 1 {
					updatedEntry = entries[0]
				}
			}
		}
		return err
	}

	args := []string{
		"--model", "openai/gpt-test",
		"--addr", "127.0.0.1:0",
		"--dir", repo,
		"--state-dir", stateDir,
		"--run-dir", runDir,
	}

	state.beforeClear = func(sess *agent.Session) {
		if _, err := sess.ProcessInput(context.Background(), "enter the managed lane", nil); err != nil {
			t.Fatalf("enter the managed lane: %v", err)
		}
		meta := sess.Meta()
		lane = strings.TrimSpace(meta.WorktreePath)
		if lane == "" || !meta.WorktreeManaged {
			t.Fatalf("old session lane = %+v, want a managed WorktreePath", meta)
		}
		if meta.WorktreeRestoreRoot != repo {
			t.Fatalf("old session restore root = %q, want %q", meta.WorktreeRestoreRoot, repo)
		}
	}

	var newMeta schema.SessionMeta
	obs := runClearAttempt(t, deps, state, args, func(obs *clearObservation) {
		newSess := state.session(1)
		if newSess == nil {
			t.Fatal("clear installed no replacement session")
		}
		newMeta = newSess.Meta()
	})
	if obs.clearErr != nil {
		t.Fatalf("thread/clear: %v", obs.clearErr)
	}
	if clearEnvDir != lane {
		t.Errorf("replacement environment = %q, want the lane %q", clearEnvDir, lane)
	}
	if newMeta.WorktreePath != lane || !newMeta.WorktreeManaged {
		t.Errorf("replacement worktree = %q managed=%v, want %q managed=true", newMeta.WorktreePath, newMeta.WorktreeManaged, lane)
	}
	if newMeta.WorktreeRestoreRoot != repo {
		t.Errorf("replacement restore root = %q, want the main checkout %q", newMeta.WorktreeRestoreRoot, repo)
	}
	if got := strings.TrimSpace(newMeta.EnvInfo.WorkingDir); got != lane {
		t.Errorf("replacement environment info working dir = %q, want %q", got, lane)
	}

	if updatedEntries != 1 {
		t.Fatalf("rendezvous entries after update = %d, want 1", updatedEntries)
	}
	if updatedEntry.SessionID != newMeta.ID || updatedEntry.WorkingDir != lane {
		t.Errorf("rendezvous = session %q dir %q, want session %q dir %q", updatedEntry.SessionID, updatedEntry.WorkingDir, newMeta.ID, lane)
	}

}

// TestRunServeClearHoldsAutomaticInputAdmission pins the clear/input ordering:
// while clear prepares a replacement, an automatic input turn must not be
// admitted. It must wait for the whole preparation and publication, then run
// against the replacement.
func TestRunServeClearHoldsAutomaticInputAdmission(t *testing.T) {
	deps, state, args := newClearServeDeps(t)
	state.clearDispatch = true
	innerClearSession := deps.newClearSession
	started := make(chan struct{})
	deps.newClearSession = func(client *llm.Client, profile *agentprovider.Profile, env execenv.ExecutionEnvironment, cfg agent.SessionConfig) (*agent.Session, error) {
		go func() {
			processWithMutationRead(state.srv, func() bool {
				close(started)
				return true
			})
		}()
		select {
		case <-started:
			t.Error("automatic input was admitted while clear was preparing the replacement")
		case <-time.After(50 * time.Millisecond):
		}
		return innerClearSession(client, profile, env, cfg)
	}

	obs := runClearAttempt(t, deps, state, args, nil)
	if obs.clearErr != nil {
		t.Fatalf("thread/clear: %v", obs.clearErr)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("automatic input did not run after clear published the replacement")
	}
}

func manageWorktreeCreateCall(t *testing.T, name string) llm.ToolCallData {
	t.Helper()
	args, err := json.Marshal(map[string]any{"operation": "create", "name": name})
	if err != nil {
		t.Fatalf("marshal manage_worktree call: %v", err)
	}
	return llm.ToolCallData{ID: "manage_worktree_create", Name: "manage_worktree", Arguments: args, Type: "function"}
}

func newClearLaneRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "clear-lane@example.com")
	run("config", "user.name", "Clear Lane Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("clear lane\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	run("add", "README.md")
	run("commit", "-q", "-m", "init")
	return dir
}
