package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

// The provider controls only tool choices and awaitable progress. Delegate
// creation, watch ownership, journals and notifications use the real session.
type activityRelayAdapter struct {
	root                                 *agent.Session
	mu                                   sync.Mutex
	steps                                map[string]int
	rootReady, childReady, grandReady    chan struct{}
	childSend, grandSend, clearRootWatch chan struct{}
	rootCleared                          chan struct{}
}

func (*activityRelayAdapter) Name() string { return "openai" }
func (*activityRelayAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}
func activityRelayTool(name string, args any) llm.Response {
	raw, _ := json.Marshal(args)
	call := llm.ToolCallData{ID: "activity-" + name, Name: name, Type: "function", Arguments: raw}
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}}}
}
func (a *activityRelayAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	role := "root"
	if req.SessionID != a.root.ID() {
		resolved, err := a.root.ActivitySummary(ctx, appwire.SessionActivityReadParams{Ref: "local:" + req.SessionID})
		if err != nil {
			return llm.Response{}, err
		}
		if len(resolved.Context.Ancestors) == 1 {
			role = "child"
		} else {
			role = "grand"
		}
	}
	a.mu.Lock()
	step := a.steps[role]
	a.steps[role] = step + 1
	a.mu.Unlock()
	if (role == "root" || role == "child") && step == 0 {
		task, allowance := "activity-child-task", 1
		if role == "child" {
			task, allowance = "activity-grand-task", 0
		}
		return activityRelayTool("delegate", map[string]any{"prompt": task, "delegation_allowance": allowance}), nil
	}
	if (role == "root" || role == "child") && step == 1 {
		page, err := a.root.ListActivityDelegates(ctx, appwire.SessionActivityListParams{Ref: "local:" + req.SessionID})
		if err != nil {
			return llm.Response{}, err
		}
		if len(page.Delegates) != 1 {
			return llm.Response{}, fmt.Errorf("script expected one owned child, got %d", len(page.Delegates))
		}
		return activityRelayTool("job_watch", map[string]any{"operation": "create", "source": page.Delegates[0].DelegateID, "events": []string{"communicate"}}), nil
	}
	if role == "root" && step == 2 {
		close(a.rootReady)
		select {
		case <-a.clearRootWatch:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		page, err := a.root.ListActivityWatches(ctx, appwire.SessionActivityListParams{Ref: "local:" + req.SessionID})
		if err != nil {
			return llm.Response{}, err
		}
		if len(page.Watches) != 1 {
			return llm.Response{}, fmt.Errorf("script expected root watch, got %d", len(page.Watches))
		}
		return activityRelayTool("job_watch", map[string]any{"operation": "clear", "watch_id": page.Watches[0].Watch.ID}), nil
	}
	if role == "root" && step == 3 {
		close(a.rootCleared)
	}
	if role == "grand" && step == 1 {
		return activityRelayTool("exec_command", map[string]any{"command": "printf activity-job", "description": "activity fixture", "mode": "background"}), nil
	}
	if (role == "child" && step == 2) || (role == "grand" && step == 0) {
		ready, send := a.childReady, a.childSend
		if role == "grand" {
			ready, send = a.grandReady, a.grandSend
		}
		close(ready)
		select {
		case <-send:
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		}
		return activityRelayTool("communicate", map[string]any{"message": "activity producer delivery", "end_turn": false}), nil
	}
	<-ctx.Done()
	return llm.Response{}, ctx.Err()
}

func awaitActivityRelaySignal(ctx context.Context, t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("%s: %v", name, ctx.Err())
	}
}
func awaitActivityRelayNotice(ctx context.Context, t *testing.T, client *appwire.Client, target, owner string, resource appwire.SessionActivityResource) appwire.SessionActivityChangedParams {
	t.Helper()
	for {
		select {
		case note, ok := <-client.Notifications():
			if !ok {
				t.Fatal("notification connection closed")
			}
			if note.Method != appwire.NotifyEvenerThreadActivityChanged {
				continue
			}
			var params appwire.SessionActivityChangedParams
			if err := json.Unmarshal(note.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID == target && params.SessionID == owner && slices.Contains(params.Resources, resource) {
				return params
			}
		case <-ctx.Done():
			t.Fatalf("no %s notice target=%s owner=%s: %v", resource, target, owner, ctx.Err())
		}
	}
}

func TestSessionActivityLiveProducerRelayAndIndependentSubscribers(t *testing.T) {
	t.Parallel()
	testSessionActivityRelay(t, nil)
}

type activityRelayFixture struct {
	ctx                    context.Context
	cfg                    hubcore.WebConfig
	stateDir               string
	refs                   []string
	open                   func() *appwire.Client
	activity, child, grand *appwire.Client
	adapter                *activityRelayAdapter
	subscriberCount        func(string) int
	stop                   func()
}

func testSessionActivityRelay(t *testing.T, checkpoint func(activityRelayFixture)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	adapter := &activityRelayAdapter{steps: map[string]int{}, rootReady: make(chan struct{}), childReady: make(chan struct{}), grandReady: make(chan struct{}), childSend: make(chan struct{}), grandSend: make(chan struct{}), clearRootWatch: make(chan struct{}), rootCleared: make(chan struct{})}
	llmClient := llm.NewClient()
	llmClient.Register(adapter)
	stateDir := filepath.Join(t.TempDir(), "project-relay-0000000000")
	workDir := t.TempDir()
	sess, err := agent.NewSession(llmClient, provider.NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(workDir), agent.SessionConfig{StateDir: stateDir, MaxSubagentDepth: 2, Sandbox: "off"})
	if err != nil {
		t.Fatal(err)
	}
	adapter.root = sess
	t.Cleanup(func() { cancel(); sess.Close() })
	rootID := sess.ID()
	rootRef := "local:" + rootID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	// Its thread histories project into each transcript's index directory
	// until closed; cleanups run last-registered first, so this runs after
	// the session stops and before the state directory is removed.
	t.Cleanup(daemon.Close)
	prepared, err := daemonserver.PrepareAppIdentityForRef("local", rootID, rootRef, sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(prepared, nil)
	daemon.SetStatus(daemonserver.StatusInfo{SessionID: rootID, State: "idle", WorkingDir: workDir})
	daemon.WireTranscriptHistory(sess)
	daemon.SetDescendantTranscriptPathFunc(func(id string) string { return filepath.Join(stateDir, "sessions", id+".transcript.jsonl") })
	daemon.SetThreadActivityReadFunc(sess.ActivitySummary)
	daemon.SetThreadDelegatesListFunc(sess.ListActivityDelegates)
	daemon.SetThreadJobsListFunc(sess.ListActivityJobs)
	daemon.SetThreadWatchesListFunc(sess.ListActivityWatches)
	physical := make(chan events.SessionEvent, 128)
	sess.SetDescendantEventFunc(func(event events.SessionEvent) {
		if event.Kind == events.EventSessionActivityChanged {
			physical <- event
		}
		daemon.RecordDescendantAppEvent(rootID, event)
	})
	sess.ConsumeEventsLossless(func(event events.SessionEvent) { daemonserver.BridgeEvent(daemon, event, nil) }, func() {})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)
	entry := rendezvous.Entry{Protocol: appwire.ProtocolVersion, Endpoint: daemonHTTP.URL, SourceID: "local", ThreadID: rootID, SessionID: rootID, WorkspaceRef: rootRef, WorkingDir: workDir, StateDir: stateDir}
	var inventoryMu sync.Mutex
	inventory := []appsource.LocalDaemonEntry{{Entry: entry, SessionID: rootID}}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		inventoryMu.Lock()
		defer inventoryMu.Unlock()
		return slices.Clone(inventory)
	}, daemonHTTP.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{HubStateRoot: t.TempDir(), Past: hubcore.NewPastIndex(""), Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: rootID, Status: "active"})}
	cfg.Archive = hubcore.NewArchiveStore(filepath.Join(cfg.HubStateRoot, "index.db"))
	cfg.Past = hubcore.NewPastIndex(stateDir)
	web := NewWebServer(cfg)
	hubServer := newHubAppServerWithNavigation(cfg, sources, web.navigation, web.resolveTopLevelSessionRef)
	hubHTTP := httptest.NewServer(http.HandlerFunc(hubServer.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	open := func() *appwire.Client {
		c := dialHubRPC(t, hubHTTP)
		t.Cleanup(func() { _ = c.Close() })
		if _, err := c.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
			t.Fatal(err)
		}
		return c
	}
	activity, transcript := open(), open()
	if _, err := activity.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := transcript.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true, IncludeTurns: true, ItemLimit: 40}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := sess.ProcessInput(ctx, "activity-root-task", nil); done <- err }()
	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("scripted root did not stop")
			}
			sess.Close()
			inventoryMu.Lock()
			inventory = nil
			inventoryMu.Unlock()
		})
	}
	t.Cleanup(stop)
	for _, ready := range []struct {
		signal chan struct{}
		name   string
	}{{adapter.rootReady, "root watch"}, {adapter.childReady, "child watch"}, {adapter.grandReady, "grandchild"}} {
		awaitActivityRelaySignal(ctx, t, ready.signal, ready.name)
	}
	initialWatches, initialErr := activity.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: rootRef})
	if initialErr != nil || len(initialWatches.Watches) != 1 {
		t.Fatalf("real initial root watch=%+v err=%v", initialWatches, initialErr)
	}
	for _, subscriber := range []*appwire.Client{activity, transcript} {
		_ = awaitActivityRelayNotice(ctx, t, subscriber, rootID, rootID, appwire.SessionActivityResourceWatches)
	}
	if got := hubServer.SubscriberCount(rootRef); got != 2 {
		t.Fatalf("root owners=%d, want 2", got)
	}
	subtree, err := activity.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: rootRef, Scope: appwire.SessionActivityScopeSubtree})
	if err != nil {
		t.Fatal(err)
	}
	if len(subtree.Delegates) != 2 {
		t.Fatalf("live subtree=%+v", subtree)
	}
	var childID, grandID string
	for _, row := range subtree.Delegates {
		if row.ParentDelegateID == "" {
			childID = strings.TrimPrefix(row.ChildRef, "local:")
		} else {
			grandID = strings.TrimPrefix(row.ChildRef, "local:")
		}
	}
	if childID == "" || grandID == "" {
		t.Fatal("missing real descendant identities")
	}
	inventoryMu.Lock()
	for _, id := range []string{childID, grandID} {
		inventory = append(inventory, appsource.LocalDaemonEntry{Entry: entry, SessionID: id, OwnerSessionID: rootID, ReadOnlyAlias: true})
	}
	inventoryMu.Unlock()
	child, grand := open(), open()
	for _, subscriber := range []struct {
		client *appwire.Client
		id     string
	}{{child, childID}, {grand, grandID}} {
		if _, err := subscriber.client.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + subscriber.id, Subscribe: true}); err != nil {
			t.Fatal(err)
		}
	}
	if checkpoint != nil {
		checkpoint(activityRelayFixture{
			ctx: ctx, cfg: cfg, stateDir: stateDir,
			refs: []string{rootRef, "local:" + childID, "local:" + grandID},
			open: open, activity: activity, child: child, grand: grand, adapter: adapter,
			subscriberCount: hubServer.SubscriberCount, stop: stop,
		})
		return
	}
	// One connection owns a rich root view and an additive lean child view.
	// The lease explicitly keeps existing membership on both reads.
	additive := open()
	if _, err := additive.ThreadRead(ctx, appwire.ThreadReadParams{Ref: rootRef, Subscribe: true, IncludeTurns: true, ItemLimit: 40, ReplaceSubscription: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := additive.ThreadRead(ctx, appwire.ThreadReadParams{Ref: "local:" + childID, Subscribe: true, ReplaceSubscription: false}); err != nil {
		t.Fatal(err)
	}
	for _, selected := range []struct {
		id, parent      string
		direct, subtree int
	}{{rootID, "", 1, 2}, {childID, rootID, 1, 1}, {grandID, childID, 0, 0}} {
		for _, scope := range []appwire.SessionActivityScope{appwire.SessionActivityScopeSession, appwire.SessionActivityScopeSubtree} {
			page, err := activity.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: "local:" + selected.id, Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			want := selected.direct
			if scope == appwire.SessionActivityScopeSubtree {
				want = selected.subtree
			}
			if len(page.Delegates) != want || !page.Context.AncestryKnown || page.Context.SessionID != selected.id || page.Context.RootRef != rootRef {
				t.Fatalf("live %s/%s: %+v", selected.id, scope, page)
			}
			parent := ""
			if selected.parent != "" {
				parent = "local:" + selected.parent
			}
			if page.Context.ParentRef != parent {
				t.Fatalf("live parent=%+v", page.Context)
			}
			summary, err := activity.ThreadActivityRead(ctx, appwire.SessionActivityReadParams{Ref: "local:" + selected.id, Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := activity.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: "local:" + selected.id, Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			watches, err := activity.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: "local:" + selected.id, Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			for _, context := range []appwire.SessionActivityContext{summary.Context, jobs.Context, watches.Context} {
				if context.SessionID != selected.id || context.Ref != "local:"+selected.id || context.RootRef != rootRef || context.ParentRef != parent || !context.AncestryKnown {
					t.Fatalf("live resource substituted owner: %+v", context)
				}
			}
		}
	}
	navigation, err := (webNavigationSource{web: web}).Capture(ctx, "activity-generation", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildNavigationProjection(navigation.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	rootFound := false
	for _, project := range navigation.Inputs.Tree.Projects {
		for _, tier := range []string{"current", "recent", "archived"} {
			page, err := projection.ProjectPage(project.Key, tier, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.Sessions {
				if row.SessionID == childID || row.SessionID == grandID {
					t.Fatal("live descendants leaked into navigation")
				}
				rootFound = rootFound || row.SessionID == rootID
			}
		}
	}
	if !rootFound {
		t.Fatal("navigation fixture did not project its live root")
	}
	var watchCut uint64
	for _, record := range daemon.AppNotificationsAfter(0, rootID) {
		watchCut = max(watchCut, record.Seq)
	}
	close(adapter.grandSend)
	for _, subscriber := range []struct {
		client *appwire.Client
		target string
	}{{activity, rootID}, {transcript, rootID}, {child, childID}} {
		notice := awaitActivityRelayNotice(ctx, t, subscriber.client, subscriber.target, childID, appwire.SessionActivityResourceWatches)
		if notice.Ref != "local:"+subscriber.target {
			t.Fatalf("routing ref=%+v", notice)
		}
	}
	// Ancestor and exact-child notices may arrive in either order.
	additiveTargets := map[string]bool{rootID: false, childID: false}
	for remaining := 2; remaining > 0; {
		select {
		case note := <-additive.Notifications():
			if note.Method != appwire.NotifyEvenerThreadActivityChanged {
				continue
			}
			var change appwire.SessionActivityChangedParams
			if err := json.Unmarshal(note.Params, &change); err != nil {
				t.Fatal(err)
			}
			seen, wanted := additiveTargets[change.ThreadID]
			if wanted && !seen && change.SessionID == childID && slices.Contains(change.Resources, appwire.SessionActivityResourceWatches) {
				if change.Ref != "local:"+change.ThreadID {
					t.Fatalf("additive routing ref=%+v", change)
				}
				additiveTargets[change.ThreadID] = true
				remaining--
			}
		case <-ctx.Done():
			t.Fatalf("additive memberships=%v: %v", additiveTargets, ctx.Err())
		}
	}
	if rootOwners, childOwners := hubServer.SubscriberCount(rootRef), hubServer.SubscriberCount("local:"+childID); rootOwners != 3 || childOwners != 2 {
		t.Fatalf("additive root=%d child=%d", rootOwners, childOwners)
	}
	if _, err := additive.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: "local:" + childID}); err != nil {
		t.Fatal(err)
	}
	if rootOwners, childOwners := hubServer.SubscriberCount(rootRef), hubServer.SubscriberCount("local:"+childID); rootOwners != 3 || childOwners != 1 {
		t.Fatalf("child release root=%d child=%d", rootOwners, childOwners)
	}
	watchChanges := 0
	for _, record := range daemon.AppNotificationsAfter(watchCut, rootID) {
		if record.Notification.Method != appwire.NotifyEvenerThreadActivityChanged {
			continue
		}
		var change appwire.SessionActivityChangedParams
		if err := json.Unmarshal(record.Notification.Params, &change); err != nil {
			t.Fatal(err)
		}
		if change.SessionID == childID && slices.Contains(change.Resources, appwire.SessionActivityResourceWatches) {
			watchChanges++
		}
	}
	if watchChanges != 1 {
		t.Fatalf("receiver/ancestor watch notices=%d, want one", watchChanges)
	}
	_ = awaitActivityRelayNotice(ctx, t, grand, grandID, grandID, appwire.SessionActivityResourceJobs)
	grandJobs, err := activity.ThreadJobsList(ctx, appwire.SessionActivityListParams{Ref: "local:" + grandID})
	if err != nil {
		t.Fatal(err)
	}
	if len(grandJobs.Jobs) != 1 || grandJobs.Jobs[0].OwnerSessionID != grandID {
		t.Fatalf("real grandchild jobs=%+v", grandJobs)
	}
	for {
		select {
		case event := <-physical:
			data := event.Data.(events.SessionActivityChangedData)
			if data.SessionID == childID && slices.Contains(data.Resources, appwire.SessionActivityResourceWatches) {
				if event.SessionID != grandID {
					t.Fatalf("physical=%s logical=%s", event.SessionID, data.SessionID)
				}
				goto physicalConfirmed
			}
		case <-ctx.Done():
			t.Fatal("missing physical grandchild watch invalidation")
		}
	}
physicalConfirmed:
	close(adapter.childSend)
	_ = awaitActivityRelayNotice(ctx, t, activity, rootID, rootID, appwire.SessionActivityResourceWatches)
	_ = awaitActivityRelayNotice(ctx, t, additive, rootID, rootID, appwire.SessionActivityResourceWatches)
	watches, err := activity.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: rootRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(watches.Watches) != 1 || watches.Watches[0].ReceiverRef != rootRef || watches.Watches[0].Watch.Deliveries != 1 {
		t.Fatalf("root receiver watches=%+v", watches)
	}
	childWatches, err := activity.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: "local:" + childID})
	if err != nil {
		t.Fatal(err)
	}
	if len(childWatches.Watches) != 1 || childWatches.Watches[0].Watch.Deliveries != 1 {
		t.Fatalf("child receiver watches=%+v", childWatches)
	}
	if _, err := activity.ThreadUnsubscribe(ctx, appwire.ThreadUnsubscribeParams{Ref: rootRef}); err != nil {
		t.Fatal(err)
	}
	if got := hubServer.SubscriberCount(rootRef); got != 2 {
		t.Fatalf("disposing activity removed transcript owner: %d", got)
	}
	close(adapter.clearRootWatch)
	awaitActivityRelaySignal(ctx, t, adapter.rootCleared, "root watch clear")
	_ = awaitActivityRelayNotice(ctx, t, transcript, rootID, rootID, appwire.SessionActivityResourceWatches)
	_ = awaitActivityRelayNotice(ctx, t, additive, rootID, rootID, appwire.SessionActivityResourceWatches)
	ended, err := transcript.ThreadWatchesList(ctx, appwire.SessionActivityListParams{Ref: rootRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(ended.Watches) != 1 || ended.Watches[0].State != appwire.SessionWatchStateEnded || ended.Watches[0].Watch.Deliveries != 1 {
		t.Fatalf("ended receiver watch=%+v", ended)
	}
}

func TestSessionActivityNestedReconnect(t *testing.T) {
	t.Parallel()
	testSessionActivityRelay(t, func(f activityRelayFixture) {
		shared := f.open()
		admit := func(client *appwire.Client) {
			for index, ref := range f.refs {
				if _, err := client.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true, IncludeTurns: index != 1, ItemLimit: 40, ReplaceSubscription: false}); err != nil {
					t.Fatal(err)
				}
			}
		}
		counts := func(root, child, grand int) bool {
			return f.subscriberCount(f.refs[0]) == root && f.subscriberCount(f.refs[1]) == child && f.subscriberCount(f.refs[2]) == grand
		}
		admit(shared)
		peek := f.open()
		if _, err := peek.ThreadRead(f.ctx, appwire.ThreadReadParams{Ref: f.refs[1], Subscribe: true, ReplaceSubscription: false}); err != nil {
			t.Fatal(err)
		}
		if !counts(3, 3, 2) {
			t.Fatal("rich columns and lean sidebar/peek replaced another membership")
		}
		params := appwire.SessionActivityListParams{Ref: f.refs[0], Scope: appwire.SessionActivityScopeSubtree, Limit: 1}
		first, err := shared.ThreadDelegatesList(f.ctx, params)
		if err != nil || len(first.Delegates) != 1 || first.Page.NextCursor == "" {
			t.Fatalf("first actual page: %+v, %v", first, err)
		}
		params.Cursor = first.Page.NextCursor
		second, err := shared.ThreadDelegatesList(f.ctx, params)
		if err != nil || len(second.Delegates) != 1 || !second.Page.Complete {
			t.Fatalf("second actual page: %+v, %v", second, err)
		}
		retained := []appwire.SessionDelegate{first.Delegates[0], second.Delegates[0]}
		if retained[0].DelegateID == retained[1].DelegateID {
			t.Fatal("observed extent contains duplicate real edges")
		}
		if err := shared.Close(); err != nil {
			t.Fatal(err)
		}
		awaitActivityRelayCondition(f.ctx, t, func() bool { return counts(2, 2, 1) }, "closed socket releases only its three memberships")
		reconnected := f.open()
		admit(reconnected)
		if !counts(3, 3, 2) {
			t.Fatal("reconnection did not restore additive membership")
		}
		params.Cursor = ""
		params.Limit = len(retained)
		recovered, err := reconnected.ThreadDelegatesList(f.ctx, params)
		if err != nil || len(recovered.Delegates) != len(retained) || !recovered.Page.Complete {
			t.Fatalf("observed page extent did not recover: %+v, %v", recovered, err)
		}
		for index, row := range recovered.Delegates {
			if row.DelegateID != retained[index].DelegateID || row.ChildRef != retained[index].ChildRef {
				t.Fatalf("reconnect substituted actual edge: %+v", row)
			}
		}
		if _, err := peek.ThreadUnsubscribe(f.ctx, appwire.ThreadUnsubscribeParams{Ref: f.refs[1]}); err != nil {
			t.Fatal(err)
		}
		if !counts(3, 2, 2) {
			t.Fatal("closing peek released a column or sidebar membership")
		}
		assertRealNestedActivity(f.ctx, t, reconnected, f.refs, "live")
		close(f.adapter.grandSend)
		childID := strings.TrimPrefix(f.refs[1], "local:")
		grandID := strings.TrimPrefix(f.refs[2], "local:")
		pending := map[string]appwire.SessionActivityResource{
			strings.TrimPrefix(f.refs[0], "local:"): appwire.SessionActivityResourceWatches,
			childID:                                 appwire.SessionActivityResourceWatches,
			grandID:                                 appwire.SessionActivityResourceJobs,
		}
		for len(pending) > 0 {
			select {
			case notice := <-reconnected.Notifications():
				if notice.Method != appwire.NotifyEvenerThreadActivityChanged {
					continue
				}
				var change appwire.SessionActivityChangedParams
				if err := json.Unmarshal(notice.Params, &change); err != nil {
					t.Fatal(err)
				}
				resource, wanted := pending[change.ThreadID]
				owner := childID
				if resource == appwire.SessionActivityResourceJobs {
					owner = grandID
				}
				if wanted && change.SessionID == owner && slices.Contains(change.Resources, resource) {
					if change.Ref != "local:"+change.ThreadID {
						t.Fatalf("reconnected notice substituted ref: %+v", change)
					}
					delete(pending, change.ThreadID)
				}
			case <-f.ctx.Done():
				t.Fatalf("missing actual reconnected deliveries %v: %v", pending, f.ctx.Err())
			}
		}
		jobs, err := reconnected.ThreadJobsList(f.ctx, appwire.SessionActivityListParams{Ref: f.refs[2]})
		if err != nil || len(jobs.Jobs) != 1 || jobs.Jobs[0].OwnerRef != f.refs[2] {
			t.Fatalf("real producer delivery after reconnect: %+v, %v", jobs, err)
		}
	})
}

func awaitActivityRelayCondition(ctx context.Context, t *testing.T, condition func() bool, name string) {
	t.Helper()
	// Subscriber cleanup has no public acknowledgement after a socket closes.
	// Poll that server condition, with the test context as the tripwire.
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatalf("%s: %v", name, ctx.Err())
		}
	}
}
