package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/jobstore"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
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
		DelegationAllowance: new(0),
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
		Type:           jobstore.JobType(delegateResourceType),
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
			Type:           jobstore.JobType(delegateResourceType),
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
	_, _, err = store.AppendBatch(state, []delegatestore.Event{
		{
			Kind:       delegatestore.EventDelegateCreated,
			TS:         now,
			DelegateID: delegateID,
			Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
				ChildSessionID:    childID,
				TranscriptRef:     encodeRef("", childID),
				OwnerSessionID:    meta.ID,
				Task:              "resume lazily",
				AgentType:         "default",
				ResolvedProfileID: "openai",
				ResolvedModel:     "gpt-5.2",
				ToolNameCeiling:   []string{"communicate"},
				Resumable:         true,
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
	childMeta := meta
	childMeta.ID = childID
	childMeta.ParentSessionID = meta.ID
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
		t.Fatal(err)
	}
	writer, err := transcript.NewWriter(filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl"), transcript.Header{
		SessionID:       childID,
		ParentSessionID: meta.ID,
		Task:            "resume lazily",
		CreatedAt:       now,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		WorkingDir:      workspace,
		Depth:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
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

func TestDelegateResourceBootstrap_WorkingDirEACCESPreservesResumability(t *testing.T) {
	fixture := newIdleDelegateRestoreInputFixture(t)
	testOnly := testConfig{
		delegateRestoreStat: func(path string) (os.FileInfo, error) {
			if filepath.Clean(path) == filepath.Clean(fixture.workingDir) {
				return nil, &os.PathError{Op: "stat", Path: path, Err: syscall.EACCES}
			}
			return os.Stat(path)
		},
	}

	assertOperationalRestoreInputErrorPreservesDelegate(t, fixture, testOnly, syscall.EACCES)
}

func TestDelegateResourceBootstrap_MetadataEACCESPreservesResumability(t *testing.T) {
	fixture := newIdleDelegateRestoreInputFixture(t)
	testOnly := testConfig{
		delegateRestoreReadFile: func(path string) ([]byte, error) {
			if filepath.Clean(path) == filepath.Clean(fixture.metaPath) {
				return nil, &os.PathError{Op: "read", Path: path, Err: syscall.EACCES}
			}
			return os.ReadFile(path)
		},
	}

	assertOperationalRestoreInputErrorPreservesDelegate(t, fixture, testOnly, syscall.EACCES)
}

func TestDelegateResourceBootstrap_TranscriptEIOPreservesResumability(t *testing.T) {
	fixture := newIdleDelegateRestoreInputFixture(t)
	previousOpen := openTranscriptFile
	openTranscriptFile = func(path string) (io.ReadCloser, error) {
		if filepath.Clean(path) == filepath.Clean(fixture.transcriptPath) {
			return nil, &os.PathError{Op: "open", Path: path, Err: syscall.EIO}
		}
		return previousOpen(path)
	}
	t.Cleanup(func() { openTranscriptFile = previousOpen })

	assertOperationalRestoreInputErrorPreservesDelegate(t, fixture, testConfig{}, syscall.EIO)
}

type idleDelegateRestoreInputFixture struct {
	meta           schema.SessionMeta
	client         *llm.Client
	profile        *provider.Profile
	stateDir       string
	rootWorkingDir string
	delegateID     string
	storePath      string
	storeBytes     []byte
	workingDir     string
	metaPath       string
	transcriptPath string
}

func newIdleDelegateRestoreInputFixture(t *testing.T) idleDelegateRestoreInputFixture {
	t.Helper()
	meta, client, profile, stateDir, rootWorkingDir, _ := closedDelegateResourceBootstrapFixture(t)
	delegateID := identifier.MustNewDelegateID()
	childID := identifier.MustNewSessionID()
	restrictedParent := t.TempDir()
	workingDir := filepath.Join(restrictedParent, "lane")
	if err := os.MkdirAll(workingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storePath := delegateResourceStorePath(stateDir, meta.ID)
	store, err := delegatestore.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Load()
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_100, 0).UTC()
	_, _, err = store.AppendBatch(state, []delegatestore.Event{{
		Kind:       delegatestore.EventDelegateCreated,
		TS:         now,
		DelegateID: delegateID,
		Created: &delegatestore.DelegateCreated{Descriptor: delegatestore.Descriptor{
			ChildSessionID:    childID,
			TranscriptRef:     encodeRef("", childID),
			OwnerSessionID:    meta.ID,
			VisibleSessionID:  meta.ID,
			Task:              "remain resumable across operational I/O failure",
			AgentType:         "default",
			ResolvedProfileID: "openai",
			ResolvedModel:     "gpt-5.2",
			ToolNameCeiling:   []string{"communicate"},
			WorkingDir:        workingDir,
			Resumable:         true,
		}},
	}})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	childMeta := meta
	childMeta.ID = childID
	childMeta.ParentSessionID = meta.ID
	childMeta.IsSubagent = true
	if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
		t.Fatal(err)
	}
	transcriptPath := filepath.Join(stateDir, sessionsSubdir, childID+".transcript.jsonl")
	writer, err := transcript.NewWriter(transcriptPath, transcript.Header{
		SessionID:       childID,
		ParentSessionID: meta.ID,
		Task:            "remain resumable across operational I/O failure",
		CreatedAt:       now,
		ProfileID:       "openai",
		Model:           "gpt-5.2",
		WorkingDir:      workingDir,
		Depth:           1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	storeBytes, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	return idleDelegateRestoreInputFixture{
		meta:           meta,
		client:         client,
		profile:        profile,
		stateDir:       stateDir,
		rootWorkingDir: rootWorkingDir,
		delegateID:     delegateID,
		storePath:      storePath,
		storeBytes:     storeBytes,
		workingDir:     workingDir,
		metaPath:       filepath.Join(stateDir, sessionsSubdir, childID+".meta.json"),
		transcriptPath: transcriptPath,
	}
}

func assertOperationalRestoreInputErrorPreservesDelegate(t *testing.T, fixture idleDelegateRestoreInputFixture, testOnly testConfig, wantErr error) {
	t.Helper()
	restored, restoreErr := restoreDelegateResourceBootstrapSessionWithTestConfig(
		fixture.client,
		fixture.profile,
		fixture.rootWorkingDir,
		fixture.meta,
		fixture.stateDir,
		testOnly,
	)
	if restored != nil {
		restored.Close()
	}
	if !errors.Is(restoreErr, wantErr) {
		t.Errorf("restore error = %v, want operational I/O error %v", restoreErr, wantErr)
	}
	gotBytes, err := os.ReadFile(fixture.storePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBytes, fixture.storeBytes) {
		t.Error("delegate store bytes changed after operational restore-input failure")
	}
	events, err := delegatestore.ReadEvents(fixture.storePath)
	if err != nil {
		t.Fatal(err)
	}
	state, err := delegatestore.Fold(events)
	if err != nil {
		t.Fatal(err)
	}
	aggregate := state[fixture.delegateID]
	if aggregate == nil || aggregate.Phase != delegatestore.PhaseIdle || !aggregate.Resumable || aggregate.NotResumableReason != "" {
		t.Errorf("delegate after operational restore-input failure = %#v, want unchanged idle resumability", aggregate)
	}
}

func newDelegateResourceBootstrapSession(t *testing.T) (*Session, *llm.Client, *provider.Profile) {
	t.Helper()
	stateDir := t.TempDir()
	workspace := t.TempDir()
	adapter := &fakeAdapter{name: "openai"}
	client := llm.NewClient()
	client.Register(adapter)
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
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
	profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
	sess, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
		StateDir:         stateDir,
		MaxSubagentDepth: 2,
		NoProjectPrompts: true,
		ForceRealIO:      true,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			sandboxProber:       bwrapCapableProber(workspace),
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
	return restoreDelegateResourceBootstrapSessionWithTestConfig(client, profile, workspace, meta, stateDir, testConfig{sandboxProber: bwrapCapableProber(workspace)})
}

func restoreDelegateResourceBootstrapSessionWithTestConfig(client *llm.Client, profile *provider.Profile, workspace string, meta schema.SessionMeta, stateDir string, testOnly testConfig) (*Session, error) {
	testOnly.skipGitSnapshot = true
	testOnly.minimalSystemPrompt = true
	return RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
		StateDir:    stateDir,
		ForceRealIO: true,
		testOnly:    testOnly,
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
