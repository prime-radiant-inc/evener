package hub

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/rendezvous"
)

// A hub thread/list child alias must carry the child's own watches, which the
// daemon prober samples into LiveEntry.ChildWatches, while the root alias keeps
// only its own. Before this, each alias inherited the root roster entry, so a
// child row carried the root's watches (and, as a read-only alias, had its
// diagnostics suppressed anyway) and the child's own were lost entirely.
func TestHubThreadListChildAliasCarriesItsOwnWatches(t *testing.T) {
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry: rendezvous.Entry{
			PID:       4242,
			SessionID: "sess_root",
			Endpoint:  "ws://127.0.0.1:1/rpc",
			Protocol:  appwire.ProtocolVersion,
		},
		SessionID:             "sess_root",
		Status:                appwire.ThreadStatusIdle,
		RunningSubagentIDs:    []string{"sess_child", "sess_bare_child"},
		RunningSubagentStates: map[string]string{"sess_child": appwire.ThreadStatusIdle, "sess_bare_child": appwire.ThreadStatusIdle},
		Watches:               []appwire.EvenerWatchInfo{{ID: "watch-root", Source: "timer", Events: []string{"output"}}},
		ChildWatches: map[string][]appwire.EvenerWatchInfo{
			"sess_child": {{ID: "watch-child", Source: "self", Events: []string{"output"}}},
		},
	})
	registry := newHubSourceRegistry(hubcore.WebConfig{Roster: roster})
	source, ok := registry.Source("local")
	if !ok {
		t.Fatal("local source missing")
	}
	response, err := source.ListThreads(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("thread/list: %v", err)
	}
	threads := map[string]appwire.Thread{}
	for _, thread := range response.Data {
		threads[thread.ID] = thread
	}
	root, child, bare := threads["sess_root"], threads["sess_child"], threads["sess_bare_child"]
	if root.ID == "" || child.ID == "" || bare.ID == "" {
		t.Fatalf("thread list = %+v, want root, child and bare child", response.Data)
	}
	// The root alias is unchanged: only its own watch.
	if root.Evener.Diagnostics == nil || len(root.Evener.Diagnostics.Watches) != 1 || root.Evener.Diagnostics.Watches[0].ID != "watch-root" {
		t.Fatalf("root diagnostics = %+v, want only its own watch", root.Evener.Diagnostics)
	}
	// The child alias carries the child's own watch, never the root's.
	if child.Evener.Diagnostics == nil || len(child.Evener.Diagnostics.Watches) != 1 || child.Evener.Diagnostics.Watches[0].ID != "watch-child" {
		t.Fatalf("child diagnostics = %+v, want the child's own watch", child.Evener.Diagnostics)
	}
	// A read-only child with no watches still has no diagnostics at all.
	if bare.Evener.Diagnostics != nil {
		t.Fatalf("bare child diagnostics = %+v, want none", bare.Evener.Diagnostics)
	}
}

func TestHubRPCThreadListUsesAppWireRendezvous(t *testing.T) {
	runDir := t.TempDir()
	entry := rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "th_1",
		SessionID: "sess_1",
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		Entry:  entry,
		Status: appwire.ThreadStatusActive,
		RunningJobs: []appwire.EvenerJobInfo{{
			JobID: "job_shell", JobType: "shell", Status: "running",
		}},
	})

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPC(t, hub)
	defer client.Close()

	init, err := client.Initialize(context.Background(), appwire.InitializeParams{ClientInfo: appwire.ClientInfo{Name: "test", Version: "test"}})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if init.ProtocolVersion != appwire.ProtocolVersion {
		t.Fatalf("protocol=%q", init.ProtocolVersion)
	}
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "th_1" || resp.Data[0].Evener.Ref != "local:th_1" {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].Evener.Diagnostics == nil || len(resp.Data[0].Evener.Diagnostics.Jobs) != 1 {
		t.Fatalf("typed hub status omitted running non-agent jobs: %+v", resp.Data[0].Evener.Diagnostics)
	}
	job := resp.Data[0].Evener.Diagnostics.Jobs[0]
	if job.JobID != "job_shell" || job.JobType != "shell" || job.Status != "running" {
		t.Fatalf("typed hub running job = %+v, want shell identity and status", job)
	}
}

func TestHubRPCThreadListUsesRosterStatusAndSessionID(t *testing.T) {
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv2enqVTitaig6F",
		SessionID: "02wMz5Txv2enqVTitaig6F",
	})
	roster := hubcore.NewRoster(runDir, fakeProber{sessionID: "02wMz5Txv1C3Hut0M8GCeB", status: appwire.ThreadStatusAwaiting})
	roster.Refresh()

	hub := newHubRPCTestServer(t, hubcore.WebConfig{
		RunDir: runDir,
		Roster: roster,
		Past:   hubcore.NewPastIndex(""),
	})
	defer hub.Close()

	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	thread := resp.Data[0]
	if thread.ID != "02wMz5Txv1C3Hut0M8GCeB" || thread.SessionID != "02wMz5Txv1C3Hut0M8GCeB" || thread.Evener.Ref != "local:02wMz5Txv1C3Hut0M8GCeB" {
		t.Fatalf("thread identity=%+v", thread)
	}
	if thread.Status.Type != appwire.ThreadStatusAwaiting {
		t.Fatalf("status=%q, want %q", thread.Status.Type, appwire.ThreadStatusAwaiting)
	}
}

func TestHubRPCThreadListIncludesPastThreads(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-past-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	hub := newHubRPCTestServer(t, hubcore.WebConfig{Past: past})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{SearchTerm: "second task"})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != sessionID || resp.Data[0].Status.Type != appwire.ThreadStatusNotLoaded {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListProjectsRunningSubagentActive(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfig(t)
	resp, err := hubThreadList(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	for _, thread := range resp.Data {
		if thread.ID == childID {
			if thread.Status.Type != appwire.ThreadStatusActive {
				t.Fatalf("running subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusActive)
			}
			return
		}
	}
	t.Fatalf("running subagent %s missing from thread list: %+v", childID, resp.Data)
}

func TestHubThreadListProjectsIdleSubagentIdle(t *testing.T) {
	cfg, childID := runningSubagentProjectionConfigWithState(t, appwire.ThreadStatusIdle)
	resp, err := hubThreadList(context.Background(), cfg, appsource.NewRegistry(), appwire.ThreadListParams{IncludeSubagents: true})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	for _, thread := range resp.Data {
		if thread.ID == childID {
			if thread.Status.Type != appwire.ThreadStatusIdle {
				t.Fatalf("idle subagent status = %q, want %q", thread.Status.Type, appwire.ThreadStatusIdle)
			}
			return
		}
	}
	t.Fatalf("idle subagent %s missing from thread list: %+v", childID, resp.Data)
}

func TestHubRPCThreadListOrdersLiveThreadsDeterministically(t *testing.T) {
	base := time.Now().UTC()
	// Nothing answers at these endpoints, so the entries are seeded into a
	// roster rather than left for the rendezvous directory's probe to reject.
	older := rendezvous.Entry{
		PID:       101,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:1/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv2enqVTitaig6F",
		SessionID: "02wMz5Txv2enqVTitaig6F",
		StartedAt: base.Add(-time.Hour),
	}
	newer := rendezvous.Entry{
		PID:       102,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:2/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv47YP64RR3B9YJ",
		SessionID: "02wMz5Txv47YP64RR3B9YJ",
		StartedAt: base,
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{Entry: older, SessionID: older.SessionID},
		hubcore.LiveEntry{Entry: newer, SessionID: newer.SessionID},
	)

	hub := newHubRPCTestServer(t, hubcore.WebConfig{Roster: roster, Past: hubcore.NewPastIndex("")})
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	resp, err := client.ThreadList(context.Background(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("ThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "02wMz5Txv47YP64RR3B9YJ" || resp.Data[1].ID != "02wMz5Txv2enqVTitaig6F" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
}

func TestHubThreadListIncludesEveryRegisteredSource(t *testing.T) {
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	entries := []rendezvous.Entry{
		{
			PID:       101,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:1/rpc",
			SourceID:  "local",
			ThreadID:  "01EVENER",
			SessionID: "01EVENER",
			StartedAt: base.Add(-time.Minute),
		},
		{
			PID:       102,
			Protocol:  appwire.ProtocolVersion,
			Endpoint:  "ws://127.0.0.1:2/rpc",
			SourceID:  "codex",
			ThreadID:  "02CODEX",
			SessionID: "02CODEX",
			StartedAt: base,
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(appsource.NewLocalDaemonSource("local", func() []rendezvous.Entry { return entries }, nil))
	sources.Add(appsource.NewLocalDaemonSource("codex", func() []rendezvous.Entry { return entries }, nil))

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].Evener.Ref != "codex:02CODEX" || resp.Data[1].Evener.Ref != "local:01EVENER" {
		t.Fatalf("refs=%s,%s", resp.Data[0].Evener.Ref, resp.Data[1].Evener.Ref)
	}
}

func TestHubThreadListContinuesWhenOptionalSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener:    appwire.EvenerThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "local:01LOCAL" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListReturnsErrorWhenOnlySelectedSourceFails(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
	}
}

func TestHubThreadListReturnsErrorWhenAnySelectedSourceFails(t *testing.T) {
	localThread := appwire.Thread{
		ID:        "01LOCAL",
		SessionID: "01LOCAL",
		Source:    "local",
		Preview:   "local thread",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener:    appwire.EvenerThread{Ref: "local:01LOCAL"},
	}
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "local", thread: localThread})
	sources.Add(&listThreadSource{id: "codex", listErr: errors.New("codex offline")})

	_, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SourceIDs: []string{"local", "codex"}})
	if err == nil || !strings.Contains(err.Error(), "codex offline") {
		t.Fatalf("hubThreadList error=%v, want codex offline", err)
	}
}

func TestHubThreadListOrdersPastSearchByUpdatedCreatedTitleAndID(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	updated := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	for _, meta := range []schema.SessionMeta{
		{ID: "02wMz5Txv5aIxgf9yVdd0N", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated, OriginalPrompt: "beta task"},
		{ID: "02wMz5Txv1C3Hut0M8GCeB", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, OriginalPrompt: "alpha task"},
		{ID: "02wMz5Txv8Vo4rqb3QYZuV", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "bravo task"},
		{ID: "02wMz5Txv733WHFsVy66SR", CreatedAt: updated.Add(-3 * time.Hour), UpdatedAt: updated.Add(-time.Hour), OriginalPrompt: "alpha task"},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	sources := appsource.NewRegistry()

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{SearchTerm: "task"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	got := make([]string, 0, len(resp.Data))
	for _, thread := range resp.Data {
		got = append(got, thread.ID)
	}
	want := []string{"02wMz5Txv1C3Hut0M8GCeB", "02wMz5Txv5aIxgf9yVdd0N", "02wMz5Txv733WHFsVy66SR", "02wMz5Txv8Vo4rqb3QYZuV"}
	if len(got) != len(want) {
		t.Fatalf("order=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v, want %v", got, want)
		}
	}
}

func TestHubThreadListSearchMatchesProviderOnlyProfile(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex-local", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex-local",
		Preview:   "codex replay",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusNotLoaded},
		Evener: appwire.EvenerThread{
			Ref:     "codex-local:th_codex",
			Profile: "openai",
		},
	}})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{SearchTerm: "openai"})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "codex-local:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

func TestHubThreadListOrdersLiveThreadsUsingPastTimestamps(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-x-0123456789")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 5, 11, 12, 0, 0, 0, time.UTC)
	liveUpdated := base
	pastUpdated := base.Add(-time.Hour)
	liveStarted := base.Add(-24 * time.Hour)

	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             "02wMz5Txv9yYdSRJat13MZ",
		CreatedAt:      base.Add(-2 * time.Hour),
		UpdatedAt:      liveUpdated,
		OriginalPrompt: "live task",
	}); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID:             "02wMz5TxvBRJC3228LTWod",
		CreatedAt:      base.Add(-3 * time.Hour),
		UpdatedAt:      pastUpdated,
		OriginalPrompt: "past task",
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing answers at this endpoint, so the live entry is seeded into a
	// roster rather than left for the rendezvous directory's probe to reject.
	live := rendezvous.Entry{
		PID:       501,
		Protocol:  appwire.ProtocolVersion,
		Endpoint:  "ws://127.0.0.1:501/rpc",
		SourceID:  "local",
		ThreadID:  "02wMz5Txv9yYdSRJat13MZ",
		SessionID: "02wMz5Txv9yYdSRJat13MZ",
		StartedAt: liveStarted,
	}
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	sources := newHubSourceRegistry(hubcore.WebConfig{Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: live, SessionID: live.SessionID})})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("threads=%+v", resp.Data)
	}
	if resp.Data[0].ID != "02wMz5Txv9yYdSRJat13MZ" || resp.Data[1].ID != "02wMz5TxvBRJC3228LTWod" {
		t.Fatalf("order=%s,%s", resp.Data[0].ID, resp.Data[1].ID)
	}
	if resp.Data[0].UpdatedAt != liveUpdated.Unix() || resp.Data[0].CreatedAt != base.Add(-2*time.Hour).Unix() {
		t.Fatalf("live timestamps=%+v", resp.Data[0])
	}
}

// TestMergePastMetadataForList_PropagatesContextCancellation covers the
// contract that mergePastMetadataForList must propagate ctx cancellation
// and deadline errors from pastEntryThread rather than silently falling
// back to the unenriched live thread -- a canceled thread-list request must
// stop, not keep sweeping later threads. The delegate journal fixture
// (seedPastSessionWithActivity) matters here: without it, pastEntryThread's
// own ctx-aware step (pastEntryDelegateStatus -> agent.LoadSessionDelegateStatus,
// gated on delegates.jsonl existing at all) is never reached, so a canceled
// ctx would go unnoticed for a different, uninteresting reason.
func TestMergePastMetadataForList_PropagatesContextCancellation(t *testing.T) {
	cfg, rootID, _, _ := seedPastSessionWithActivity(t, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	live := appwire.Thread{ID: rootID}

	_, err := mergePastMetadataForList(ctx, cfg, "local", live)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled -- a canceled request must not silently fall back to the live thread unenriched", err)
	}
}

func TestHubThreadListKeepsLocalPastWhenNonLocalLiveIDCollides(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "project-local-0000000000")
	sessionID := buildRPCParentSession(t, stateDir)
	past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
	sources := appsource.NewRegistry()
	sources.Add(&relayBroadcastSource{
		thread: appwire.Thread{
			ID:        sessionID,
			SessionID: sessionID,
			Source:    "codex",
			Preview:   "live codex thread",
			Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
			Evener:    appwire.EvenerThread{Ref: "codex:" + sessionID},
		},
	})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: past}, sources, appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	var foundLocalPast, foundCodexLive bool
	for _, thread := range resp.Data {
		switch thread.Evener.Ref {
		case "local:" + sessionID:
			foundLocalPast = true
		case "codex:" + sessionID:
			foundCodexLive = true
		}
	}
	if !foundLocalPast || !foundCodexLive {
		t.Fatalf("found local past=%v codex live=%v threads=%+v", foundLocalPast, foundCodexLive, resp.Data)
	}
}

func TestHubThreadListMatchesCodexNativeStatusFilters(t *testing.T) {
	sources := appsource.NewRegistry()
	sources.Add(&listThreadSource{id: "codex", thread: appwire.Thread{
		ID:        "th_codex",
		SessionID: "th_codex",
		Source:    "codex",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
		Evener:    appwire.EvenerThread{Ref: "codex:th_codex"},
	}})

	resp, err := hubThreadList(context.Background(), hubcore.WebConfig{Past: hubcore.NewPastIndex("")}, sources, appwire.ThreadListParams{
		Statuses: []string{"active"},
	})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if len(resp.Data) != 1 || resp.Data[0].Evener.Ref != "codex:th_codex" {
		t.Fatalf("threads=%+v", resp.Data)
	}
}

type listThreadSource struct {
	relayLifecycleSource
	id      string
	thread  appwire.Thread
	listErr error
}

func (s *listThreadSource) ID() string { return s.id }

func (s *listThreadSource) ListTurns(context.Context, appwire.ThreadTurnsListParams) (appwire.ThreadTurnsListResponse, error) {
	return appwire.ThreadTurnsListResponse{}, nil
}

func (s *listThreadSource) ListThreads(context.Context, appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
	if s.listErr != nil {
		return appwire.ThreadListResponse{}, s.listErr
	}
	return appwire.ThreadListResponse{Data: []appwire.Thread{s.thread}}, nil
}
