package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/identifier"
	"testing"
	"time"

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
		return nil, fmt.Errorf("must not dial")
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
