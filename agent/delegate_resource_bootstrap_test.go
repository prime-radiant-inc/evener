package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/delegatestore"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/identifier"
	"primeradiant.com/serf/llm"
)

func TestDelegateResourceBootstrap_RootOwnsOneController(t *testing.T) {
	sess, _, _ := newDelegateResourceBootstrapSession(t)

	if pointer := delegateControllerPointer(t, sess); pointer == 0 {
		t.Fatal("root delegate controller is nil")
	}
}

func TestDelegateResourceBootstrap_ChildInheritsControllerAndStableOwnerID(t *testing.T) {
	root, _, _ := newDelegateResourceBootstrapSession(t)
	rootController := delegateControllerPointer(t, root)

	result := root.createDelegate(context.Background(), delegateArgs{
		Task:                "report the result",
		Background:          true,
		DelegationAllowance: 0,
	})
	if result.Err != nil {
		t.Fatalf("createDelegate: %v", result.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked child count = %d, want 1", len(children))
	}
	child := children[0]
	if got := delegateControllerPointer(t, child); got != rootController {
		t.Fatalf("child controller pointer = %#x, want root pointer %#x", got, rootController)
	}
	if got := stableDelegateOwnerID(t, child); got != result.DelegateID {
		t.Fatalf("child stable owner ID = %q, want %q", got, result.DelegateID)
	}
}

func TestDelegateResourceBootstrap_LegacyDelegateStateFailsClosed(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	removeDelegateStoreIfPresent(t, stateDir, meta.ID)
	jobPath := filepath.Join(jobsDir(stateDir, meta.ID), "jobs.jsonl")
	appendLegacyBootstrapEvents(t, jobPath, jobstore.Event{
		Kind:           jobstore.EventJobStarted,
		JobID:          "job_legacy_delegate",
		Type:           jobstore.JobDelegate,
		OwnerSessionID: meta.ID,
	})
	wantBytes, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if restored != nil {
		restored.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "legacy_delegate_state") || !strings.Contains(err.Error(), "fresh state root") {
		t.Fatalf("restore error = %v, want legacy_delegate_state with fresh-state-root guidance", err)
	}
	assertBootstrapScanWasReadOnly(t, stateDir, meta.ID, jobPath, wantBytes)
}

func TestDelegateResourceBootstrap_LegacyDelegateWatchStateFailsClosed(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	removeDelegateStoreIfPresent(t, stateDir, meta.ID)
	jobPath := filepath.Join(jobsDir(stateDir, meta.ID), "jobs.jsonl")
	appendLegacyBootstrapEvents(t, jobPath,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			JobID:          "job_legacy_delegate",
			Type:           jobstore.JobDelegate,
			OwnerSessionID: meta.ID,
		},
		jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			WatchID: "watch_legacy_delegate_job",
			Watch: &jobstore.WatchEvent{
				Generation:       "wg_legacy",
				OwnerSessionID:   meta.ID,
				VisibleSessionID: meta.ID,
				Target:           "job_legacy_delegate",
				SendTo:           meta.ID,
				ConfigHash:       "legacy",
			},
		},
	)
	wantBytes, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if restored != nil {
		restored.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "legacy_delegate_watch_state") || !strings.Contains(err.Error(), "fresh state root") {
		t.Fatalf("restore error = %v, want legacy_delegate_watch_state with fresh-state-root guidance", err)
	}
	assertBootstrapScanWasReadOnly(t, stateDir, meta.ID, jobPath, wantBytes)
}

func TestDelegateResourceBootstrap_ShellOnlyAndStableWatchStateOpen(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	removeDelegateStoreIfPresent(t, stateDir, meta.ID)
	jobPath := filepath.Join(jobsDir(stateDir, meta.ID), "jobs.jsonl")
	appendLegacyBootstrapEvents(t, jobPath,
		jobstore.Event{
			Kind:           jobstore.EventJobStarted,
			JobID:          "job_shell",
			Type:           jobstore.JobShell,
			OwnerSessionID: meta.ID,
		},
		jobstore.Event{
			Kind:    jobstore.EventWatchRegistered,
			WatchID: "watch_shell",
			Watch: &jobstore.WatchEvent{
				Generation:       "wg_shell",
				OwnerSessionID:   meta.ID,
				VisibleSessionID: meta.ID,
				Target:           "job_shell",
				SendTo:           meta.ID,
				ConfigHash:       "stable",
			},
		},
	)

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore shell-only state: %v", err)
	}
	defer restored.Close()
	if pointer := delegateControllerPointer(t, restored); pointer == 0 {
		t.Fatal("restored root delegate controller is nil")
	}
}

func TestDelegateResourceBootstrap_UnknownStoreVersionFailsClosed(t *testing.T) {
	meta, client, profile, stateDir, workspace, _ := closedDelegateResourceBootstrapFixture(t)
	path := delegateResourceStorePath(stateDir, meta.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const unknownVersion = "{\"version\":999}\n"
	if err := os.WriteFile(path, []byte(unknownVersion), 0o600); err != nil {
		t.Fatal(err)
	}

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if restored != nil {
		restored.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "unsupported version 999") {
		t.Fatalf("restore error = %v, want unsupported delegate-store version", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != unknownVersion {
		t.Fatalf("unknown-version store changed during failed bootstrap: %q", got)
	}
}

func TestDelegateResourceBootstrap_RestartIsProviderFreeAndLazy(t *testing.T) {
	meta, client, profile, stateDir, workspace, adapter := closedDelegateResourceBootstrapFixture(t)
	path := delegateResourceStorePath(stateDir, meta.ID)
	store, err := delegatestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(nil)
	if err != nil {
		t.Fatal(err)
	}
	delegateID := identifier.MustNewDelegateID()
	childID := identifier.MustNewSessionID()
	now := time.Unix(1_700_000_000, 0).UTC()
	_, state, err = store.AppendBatch(state, []delegatestore.Event{
		{
			Kind:       delegatestore.EventDelegateCreated,
			TS:         now,
			DelegateID: delegateID,
			Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
				ChildSessionID: childID,
				TranscriptRef:  encodeRef("", childID),
				OwnerSessionID: meta.ID,
				Task:           "resume lazily",
				AgentType:      "default",
				Resumable:      true,
			}},
		},
		{
			Kind:       delegatestore.EventDelegateRunStarted,
			TS:         now,
			DelegateID: delegateID,
			RunStarted: &delegatestore.RunStarted{
				Generation: 1,
				Trigger:    delegatestore.TriggerInitial,
				StartedAt:  now,
			},
		},
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := restoreDelegateResourceBootstrapSession(client, profile, workspace, meta, stateDir)
	if err != nil {
		t.Fatalf("restore with open delegate generation: %v", err)
	}
	if pointer := delegateControllerPointer(t, restored); pointer == 0 {
		t.Fatal("restored root delegate controller is nil")
	}
	if got := len(adapter.Requests()); got != 0 {
		t.Fatalf("provider request count during restore = %d, want 0", got)
	}
	restored.Close()

	events, err := delegatestore.ReadEvents(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err = delegatestore.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state[delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || aggregate.CurrentRunOpen {
		t.Fatalf("reconciled aggregate = %+v, want provider-free idle runtime_lost settlement", aggregate)
	}
	if aggregate.LatestOutcome == nil || aggregate.LatestOutcome.Status != delegatestore.OutcomeFailed || aggregate.LatestOutcome.Reason != "runtime_lost" {
		t.Fatalf("reconciled outcome = %+v, want failed/runtime_lost", aggregate.LatestOutcome)
	}
}

func newDelegateResourceBootstrapSession(t *testing.T) (*Session, *llm.Client, *provider.Profile) {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := NewOpenAIProfile("gpt-5.2")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(sess.Close)
	return sess, client, profile
}

func closedDelegateResourceBootstrapFixture(t *testing.T) (schema.SessionMeta, *llm.Client, *provider.Profile, string, string, *fakeAdapter) {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := NewOpenAIProfile("gpt-5.2")
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	meta := sess.Meta()
	if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
		sess.Close()
		t.Fatal(err)
	}
	sess.Close()
	return meta, client, profile, stateDir, workspace, adapter
}

func restoreDelegateResourceBootstrapSession(client *llm.Client, profile *provider.Profile, workspace string, meta schema.SessionMeta, stateDir string) (*Session, error) {
	return RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
		},
	})
}

func appendLegacyBootstrapEvents(t *testing.T, path string, events ...jobstore.Event) {
	t.Helper()
	store, err := jobstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close() //nolint:errcheck
	for _, event := range events {
		if err := store.Append(event); err != nil {
			t.Fatal(err)
		}
	}
}

func delegateControllerPointer(t *testing.T, sess *Session) uintptr {
	t.Helper()
	field := reflect.ValueOf(sess).Elem().FieldByName("delegateController")
	if !field.IsValid() {
		t.Fatal("Session has no root-owned delegate controller")
	}
	if field.Kind() != reflect.Pointer {
		t.Fatalf("Session.delegateController kind = %s, want pointer", field.Kind())
	}
	return field.Pointer()
}

func stableDelegateOwnerID(t *testing.T, sess *Session) string {
	t.Helper()
	field := reflect.ValueOf(sess).Elem().FieldByName("owningDelegateID")
	if !field.IsValid() {
		t.Fatal("Session has no stable delegate owner ID")
	}
	if field.Kind() != reflect.String {
		t.Fatalf("Session.owningDelegateID kind = %s, want string", field.Kind())
	}
	return field.String()
}

func removeDelegateStoreIfPresent(t *testing.T, stateDir, rootSessionID string) {
	t.Helper()
	if err := os.Remove(delegateResourceStorePath(stateDir, rootSessionID)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func delegateResourceStorePath(stateDir, rootSessionID string) string {
	return filepath.Join(stateDir, sessionsSubdir, rootSessionID, "delegates.jsonl")
}

func assertBootstrapScanWasReadOnly(t *testing.T, stateDir, rootSessionID, jobPath string, wantJobBytes []byte) {
	t.Helper()
	gotJobBytes, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJobBytes, wantJobBytes) {
		t.Fatal("legacy job log changed during failed bootstrap")
	}
	if _, err := os.Stat(delegateResourceStorePath(stateDir, rootSessionID)); !os.IsNotExist(err) {
		t.Fatalf("delegate store created before legacy scan failed: %v", err)
	}
}
