package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/identifier"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestSessionActivityPublicRoutes(t *testing.T) {
	for _, method := range []string{
		"evener/thread/activity/read", "evener/thread/delegates/list",
		"evener/thread/jobs/list", "evener/thread/watches/list",
	} {
		t.Run(method, func(t *testing.T) {
			_, err := dispatchHubJobsRPC(t, hubcore.WebConfig{}, newExitedLocalRegistry(), method, `{"ref":"local:missing"}`)
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("registered read of an unavailable session = %v; want unavailable", err)
			}
		})
	}
}

func TestSessionActivityRetainedPublicHierarchyAndSourceFences(t *testing.T) {
	cfg, root, child, stateDir := seedPastSessionWithActivity(t, 1)
	grand, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1700000000, 0).UTC()
	childMeta, err := schema.LoadSessionMeta(stateDir, child)
	if err != nil {
		t.Fatal(err)
	}
	childMeta.IsSubagent = true
	childMeta.ParentSessionID = root
	childMeta.JobTreeRootSessionID = root
	if err := schema.SaveSessionMeta(stateDir, childMeta); err != nil {
		t.Fatal(err)
	}
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: grand, IsSubagent: true, ParentSessionID: child, JobTreeRootSessionID: root, EnvInfo: childMeta.EnvInfo, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "sessions", root, "delegates.jsonl")
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := json.Marshal(map[string]any{"events": []any{map[string]any{"kind": "delegate_created", "seq": 2, "ts": now, "delegate_id": "dlg_grand", "created": map[string]any{"descriptor": map[string]any{"child_session_id": grand, "owner_session_id": child, "visible_session_id": child, "parent_delegate_id": "dlg_child", "transcript_ref": "local:" + grand, "task": "grandchild task", "agent_type": "general", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(batch, '\n')); err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	writePersistedJobsLog(t, stateDir, grand, now, []persistedJobFixture{{id: "job_grand", command: "echo grandchild"}})
	if _, err := cfg.Past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	cfg.Roster = hubcore.NewRosterWithEntries()
	registry := newExitedLocalRegistry()
	dispatch := func(method string, params any) (any, error) {
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		return dispatchHubJobsRPC(t, cfg, registry, method, string(raw))
	}
	for _, owner := range []struct {
		id, parent      string
		direct, subtree int
	}{{root, "", 1, 2}, {child, root, 1, 1}, {grand, child, 0, 0}} {
		for _, scope := range []appwire.SessionActivityScope{appwire.SessionActivityScopeSession, appwire.SessionActivityScopeSubtree} {
			params := appwire.SessionActivityListParams{Ref: "local:" + owner.id, Scope: scope, Limit: 200}
			value, err := dispatch(appwire.MethodEvenerThreadDelegatesList, params)
			if err != nil {
				t.Fatal(err)
			}
			page := value.(appwire.SessionDelegatesResponse)
			want := owner.direct
			if scope == appwire.SessionActivityScopeSubtree {
				want = owner.subtree
			}
			if len(page.Delegates) != want || !page.Page.Complete {
				t.Fatalf("%s/%s rows=%d page=%+v", owner.id, scope, len(page.Delegates), page.Page)
			}
			if page.Context.SessionID != owner.id || page.Context.Ref != params.Ref || page.Context.RootRef != "local:"+root || !page.Context.AncestryKnown {
				t.Fatalf("resolved context=%+v", page.Context)
			}
			wantParent := ""
			if owner.parent != "" {
				wantParent = "local:" + owner.parent
			}
			if page.Context.ParentRef != wantParent {
				t.Fatalf("parent context=%+v", page.Context)
			}
			summary, err := dispatch(appwire.MethodEvenerThreadActivityRead, appwire.SessionActivityReadParams{Ref: params.Ref, Scope: scope})
			if err != nil {
				t.Fatal(err)
			}
			if summary.(appwire.SessionActivitySummary).Context.SessionID != owner.id {
				t.Fatal("summary substituted root")
			}
			jobs, err := dispatch(appwire.MethodEvenerThreadJobsList, params)
			if err != nil {
				t.Fatal(err)
			}
			if jobs.(appwire.SessionJobsResponse).Context.SessionID != owner.id {
				t.Fatal("jobs substituted root")
			}
			watches, err := dispatch(appwire.MethodEvenerThreadWatchesList, params)
			if err != nil {
				t.Fatal(err)
			}
			if watches.(appwire.SessionWatchesResponse).Context.SessionID != owner.id {
				t.Fatal("watches substituted root")
			}
		}
	}
	web := NewWebServer(cfg)
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", now)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildNavigationProjection(snapshot.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, project := range snapshot.Inputs.Tree.Projects {
		for _, tier := range []string{"current", "recent", "archived"} {
			page, err := projection.ProjectPage(project.Key, tier, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.Sessions {
				if row.SessionID == child || row.SessionID == grand {
					t.Fatal("activity descendants leaked into navigation")
				}
			}
		}
	}
	for _, invalid := range []appwire.SessionActivityListParams{{Ref: "local:" + root, Scope: "invalid"}, {Ref: "local:" + root, Limit: -1}, {Ref: "local:" + root, Cursor: "malformed"}} {
		_, err := dispatch(appwire.MethodEvenerThreadJobsList, invalid)
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("invalid public request %+v: %v", invalid, err)
		}
	}
	remote := appsource.NewRemoteHubSource("remote", nil, func(context.Context, string) (*appwire.Client, error) {
		t.Error("read dialed a remote host")
		return nil, errors.New("must not dial")
	})
	remote.SetHostClientIfAttached(func(string) (*appwire.Client, bool) { return nil, false })
	registry.Add(remote)
	for _, method := range []string{appwire.MethodEvenerThreadActivityRead, appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
		_, err := dispatch(method, appwire.SessionActivityListParams{Ref: "remote:" + root})
		if err == nil {
			t.Fatalf("%s borrowed local retained colliding ID", method)
		}
	}
	store, err := hubcore.NewDeletionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg.DeletionStore = store
	cfg.ResumeLocks = hubcore.NewResumeLocks()
	if _, err := store.Begin("project-fence-0123456789", []hubcore.DeletionTarget{{Ref: "local:" + root, ThreadID: root}}); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{appwire.MethodEvenerThreadActivityRead, appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
		_, err := dispatch(method, appwire.SessionActivityListParams{Ref: "local:" + root})
		if !isTargetDeletedError(err) {
			t.Fatalf("%s bypassed deletion fence: %v", method, err)
		}
	}
}

// Context cancellation is a caller boundary. Counting checks makes the cold
// read interruption deterministic without altering the scanner or filesystem.
type activityReadCancellation struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *activityReadCancellation) Err() error {
	if c.checks.Add(1) >= 100 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSessionActivityPublicCanceledReconstructionResumes(t *testing.T) {
	t.Parallel()
	cfg, _, child, _ := seedPastSessionWithActivity(t, 100)
	server := newHubAppServer(cfg, newExitedLocalRegistry())
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	interrupted := &activityReadCancellation{Context: ctx, cancel: cancel}
	params := appwire.SessionActivityListParams{Ref: "local:" + child, Limit: 200}
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	request := appwire.Request{Method: appwire.MethodEvenerThreadJobsList, Params: raw}
	if _, err := server.Router().Dispatch(interrupted, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("public cold read cancellation=%v", err)
	}
	if interrupted.checks.Load() < 100 {
		t.Fatal("fixture did not interrupt reconstruction")
	}
	result, err := server.Router().Dispatch(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	page := result.(appwire.SessionJobsResponse)
	if len(page.Jobs) != 100 || !page.Page.Complete || page.Context.SessionID != child {
		t.Fatalf("reconstruction after cancellation=%+v", page)
	}
	seen := make(map[string]bool)
	for _, job := range page.Jobs {
		if seen[job.JobID] {
			t.Fatalf("duplicate resumed identity %s", job.JobID)
		}
		seen[job.JobID] = true
	}
}

func TestSessionActivityArchivedNestedHierarchy(t *testing.T) {
	t.Parallel()
	testSessionActivityRelay(t, func(f activityRelayFixture) {
		if _, err := f.cfg.Past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		for _, ref := range f.refs {
			if _, err := f.activity.ArchiveSet(f.ctx, appwire.ArchiveParams{Kind: appwire.ArchiveTargetSession, ID: ref, Archived: true}); err != nil {
				t.Fatalf("archive %s: %v", ref, err)
			}
		}
		decisions, err := hubcore.NewArchiveStore(filepath.Join(f.cfg.HubStateRoot, "index.db")).Decisions()
		if err != nil {
			t.Fatal(err)
		}
		for _, ref := range f.refs {
			if !decisions[hubcore.ArchiveKey{Kind: "session", ID: strings.TrimPrefix(ref, "local:")}] {
				t.Fatalf("archive decision not persisted for %s", ref)
			}
		}
		assertActivityArchiveNavigation(t, f.cfg, f.activity, f.refs)
		assertRealNestedActivity(f.ctx, t, f.activity, f.refs, "live")
		f.stop()
		if _, err := f.cfg.Past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		assertRealNestedActivity(t.Context(), t, f.activity, f.refs, "retained")
		assertActivityArchiveNavigation(t, f.cfg, f.activity, f.refs)
	})
}

func assertRealNestedActivity(ctx context.Context, t *testing.T, client *appwire.Client, refs []string, availability string) {
	t.Helper()
	for index, ref := range refs {
		page, err := client.ThreadDelegatesList(ctx, appwire.SessionActivityListParams{Ref: ref, Scope: appwire.SessionActivityScopeSession, Limit: 200})
		if err != nil {
			t.Fatalf("direct %s: %v", ref, err)
		}
		wantRows := 0
		if index < len(refs)-1 {
			wantRows = 1
		}
		if len(page.Delegates) != wantRows || !page.Page.Complete || page.Context.Availability != availability || page.Context.Ref != ref || page.Context.RootRef != refs[0] || !page.Context.AncestryKnown {
			t.Fatalf("direct %s/%s: %+v", ref, availability, page)
		}
		if wantRows == 1 && (page.Delegates[0].ChildRef != refs[index+1] || page.Delegates[0].DelegateID == "") {
			t.Fatalf("undrillable actual edge: %+v", page.Delegates)
		}
		var ancestors []string
		for _, ancestor := range page.Context.Ancestors {
			ancestors = append(ancestors, ancestor.Ref)
		}
		if !slices.Equal(ancestors, refs[:index]) {
			t.Fatalf("%s ancestry=%v, want %v", ref, ancestors, refs[:index])
		}
	}
}

func assertActivityArchiveNavigation(t *testing.T, cfg hubcore.WebConfig, client *appwire.Client, refs []string) {
	t.Helper()
	snapshot, err := (webNavigationSource{web: NewWebServer(cfg)}).Capture(t.Context(), "archive-generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildNavigationProjection(snapshot.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	rootArchived := false
	projects := append(slices.Clone(snapshot.Inputs.Tree.Projects), snapshot.Inputs.Tree.ArchivedProjects...)
	for _, project := range projects {
		for _, tier := range []string{"current", "recent"} {
			page, err := projection.ProjectPage(project.Key, tier, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, row := range page.Sessions {
				if slices.Contains(refs[1:], row.Ref) {
					t.Fatalf("delegate %s fabricated in %s navigation", row.Ref, tier)
				}
				if row.Ref == refs[0] {
					t.Fatalf("archived root projected in %s", tier)
				}
			}
		}
		archived, err := client.ArchivedList(t.Context(), appwire.ArchivedListParams{ProjectKey: project.Key})
		if err != nil {
			t.Fatal(err)
		}
		var rows []hubapi.NavigationSessionSummary
		if err := json.Unmarshal(archived.Sessions, &rows); err != nil {
			t.Fatal(err)
		}
		if archived.Total != 1 || len(rows) != 1 || rows[0].Ref != refs[0] || projection.projectSummary(project).MoreArchived != 1 {
			t.Fatalf("archived list/navigation count changed real root: page=%+v rows=%+v", archived, rows)
		}
		rootArchived = true
	}
	if !rootArchived {
		t.Fatal("real archived root missing from navigation and archived list")
	}
}

func TestSessionActivityPublicInitiallyEmptyIncompletePageProgresses(t *testing.T) {
	t.Parallel()
	testSessionActivityRelay(t, func(f activityRelayFixture) {
		close(f.adapter.grandSend)
		grandID := strings.TrimPrefix(f.refs[2], "local:")
		awaitActivityRelayNotice(f.ctx, t, f.grand, grandID, grandID, appwire.SessionActivityResourceJobs)
		produced, err := f.activity.ThreadJobsList(f.ctx, appwire.SessionActivityListParams{Ref: f.refs[2]})
		if err != nil || len(produced.Jobs) != 1 {
			t.Fatalf("actual producer readiness: %+v, %v", produced, err)
		}
		f.stop()
		retainedDir := filepath.Join(t.TempDir(), "project-relay-0000000000")
		if err := os.CopyFS(retainedDir, os.DirFS(f.stateDir)); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(retainedDir, "sessions", grandID, "jobs.jsonl")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		// Legal blank journal lines spend the real scanner's bounded work
		// allowance without fabricating a single activity record.
		if err := os.WriteFile(path, append([]byte(strings.Repeat("\n", 8192)), original...), 0600); err != nil {
			t.Fatal(err)
		}
		cfg := f.cfg
		cfg.Past = hubcore.NewPastIndex(retainedDir)
		cfg.Roster = hubcore.NewRosterWithEntries()
		if _, err := cfg.Past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		server := newHubAppServer(cfg, newExitedLocalRegistry())
		params := appwire.SessionActivityListParams{Ref: f.refs[2], Scope: appwire.SessionActivityScopeSession, Limit: 1}
		seen := map[string]bool{}
		for call := range 16 {
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			result, err := server.Router().Dispatch(t.Context(), appwire.Request{Method: appwire.MethodEvenerThreadJobsList, Params: raw})
			if err != nil {
				t.Fatal(err)
			}
			page := result.(appwire.SessionJobsResponse)
			if call == 0 && (len(page.Jobs) != 0 || page.Page.Complete || page.Page.NextCursor == "") {
				t.Fatalf("cold first page must be empty, incomplete and advancing: %+v", page)
			}
			for _, job := range page.Jobs {
				if job.JobID != produced.Jobs[0].JobID || job.OwnerRef != f.refs[2] || seen[job.JobID] {
					t.Fatalf("cold walk changed or duplicated producer row: %+v", job)
				}
				seen[job.JobID] = true
			}
			if page.Page.Complete {
				if len(seen) != 1 || page.Page.NextCursor != "" {
					t.Fatalf("completed without exactly one actual job: %+v", page)
				}
				return
			}
			if page.Page.NextCursor == "" || page.Page.NextCursor == params.Cursor {
				t.Fatalf("empty progress stalled: %+v", page)
			}
			params.Cursor = page.Page.NextCursor
		}
		t.Fatal("cold page did not converge within sixteen bounded reads")
	})
}
