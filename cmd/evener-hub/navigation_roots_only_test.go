package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/rendezvous"
)

// rootsOnlyWeb is a hub whose past index holds a root, its subagent and
// grandchild, and an orphan subagent, with the root and both subagents live and
// every subagent awaiting the user.
func rootsOnlyWeb(t *testing.T) (*WebServer, *hubcore.PinSectionStore) {
	t.Helper()
	now := timeNowForTest()
	pins := hubcore.NewPinSectionStore(filepath.Join(t.TempDir(), "pins.db"))
	root := liveForPinTest("01ROOT", "01CHILD")
	root.Status = appwire.ThreadStatusIdle
	child := liveForPinTest("01CHILD", "01GRAND")
	child.Status = appwire.ThreadStatusAwaiting
	child.PendingAsk = true
	orphan := liveForPinTest("01ORPHAN")
	orphan.Status = appwire.ThreadStatusAwaiting
	web := NewWebServer(hubcore.WebConfig{
		Past:        hubcore.NewPastIndex(""),
		PinSections: pins,
		Roster:      hubcore.NewRosterWithEntries(root, child, orphan),
	})
	web.injectMetasForTest([]schema.SessionMeta{
		{ID: "01ROOT", UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
		{ID: "01CHILD", ParentSessionID: "01ROOT", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
		{ID: "01GRAND", ParentSessionID: "01CHILD", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
		{ID: "01ORPHAN", ParentSessionID: "01GONE", IsSubagent: true, UpdatedAt: now, EnvInfo: schema.EnvironmentInfo{WorkingDir: "/work/p"}},
	})
	section, _, err := pins.CreateOrReuseAndAssign("Research", "", "01ROOT", now)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"01CHILD", "01ORPHAN"} {
		if _, _, err := pins.Assign(section.ID, "", id, now); err != nil {
			t.Fatal(err)
		}
	}
	return web, pins
}

// No navigation section, project page or pin page carries a subagent row, and
// a subagent's own attention never reaches Needs you.
func TestNavigationListsRootsOnly(t *testing.T) {
	web, pins := rootsOnlyWeb(t)
	snapshot, err := (webNavigationSource{web: web}).Capture(t.Context(), "generation", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	projection, err := buildNavigationProjection(snapshot.Inputs)
	if err != nil {
		t.Fatal(err)
	}
	sections, err := pins.Sections()
	if err != nil || len(sections) != 1 {
		t.Fatalf("pin sections = %+v, %v", sections, err)
	}
	pinPage, ok := projection.PinSectionPage(sections[0].ID, 0, 0)
	if !ok {
		t.Fatal("pin section page missing")
	}
	pages := map[string]hubapi.NavigationSectionResource{
		"live":      projection.LivePage(0, 0),
		"needs you": projection.NeedsYouPage(0, 0),
		"pin":       pinPage,
	}
	for _, project := range snapshot.Inputs.Tree.Projects {
		for _, tier := range []string{"current", "recent", "archived"} {
			page, err := projection.ProjectPage(project.Key, tier, 0, 0)
			if err != nil {
				t.Fatal(err)
			}
			pages["project "+project.Key+" "+tier] = hubapi.NavigationSectionResource{Sessions: page.Sessions}
		}
	}
	subagents := []string{"01CHILD", "01GRAND", "01ORPHAN"}
	var visit func(name string, rows []hubapi.NavigationSessionSummary)
	visit = func(name string, rows []hubapi.NavigationSessionSummary) {
		for _, row := range rows {
			if slices.Contains(subagents, row.SessionID) || row.Kind == "subagent" {
				t.Errorf("%s carries subagent row %s", name, row.SessionID)
			}
			visit(name, row.Children)
		}
	}
	for name, page := range pages {
		visit(name, page.Sessions)
	}
	if got := pages["pin"].Sessions; len(got) != 1 || got[0].SessionID != "01ROOT" {
		t.Errorf("pin rows = %+v, want only the root", got)
	}
	if got := pages["needs you"].Sessions; len(got) != 0 {
		t.Errorf("needs you = %+v, want none: only the top-level agent can need the user", got)
	}
	if snapshot.Inputs.AttentionSummary.NeedsYou != 0 {
		t.Errorf("attention summary = %+v, want no needs-you", snapshot.Inputs.AttentionSummary)
	}
}

// A subagent has no row, so its location resolves by alias to its root's row,
// and an orphan's location is gone.
func TestNavigationSubagentLocationResolvesByAlias(t *testing.T) {
	web, _ := rootsOnlyWeb(t)
	location := func(id string) hubapi.NavigationSessionLocation {
		t.Helper()
		key := navigationResourceKey{Kind: navigationResourceLocation, ID: "local:" + id}
		_, versioned, projection, err := web.navigation.versionedCore(t.Context(), key)
		if err != nil {
			t.Fatalf("location %s: %v", id, err)
		}
		object, _, err := projection.Resource(versioned)
		if err != nil {
			t.Fatal(err)
		}
		return object.(hubapi.NavigationSessionLocation)
	}
	root := location("01ROOT")
	for _, id := range []string{"01CHILD", "01GRAND"} {
		got := location(id)
		if got.TopLevel || got.TopLevelRef != root.Ref || got.ProjectKey != root.ProjectKey {
			t.Errorf("location of %s = %+v, want it routed to root %s", id, got, root.Ref)
		}
	}
	key := navigationResourceKey{Kind: navigationResourceLocation, ID: "local:01ORPHAN"}
	response, err := web.navigation.readV2(t.Context(), key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Response.Status != "gone" {
		t.Errorf("orphan location = %+v, want gone", response.Response)
	}
}

// delegateLink records that a session spawned a child; parentDelegateID names
// the delegate the spawn happened under, empty for a direct child of the root.
type delegateLink struct{ childID, parentDelegateID string }

// writeDelegateDescriptors writes the root-owned delegate journal a daemon
// keeps, one descriptor per link, each under delegate id "dlg_<child>".
func writeDelegateDescriptors(t *testing.T, stateDir, rootID string, links ...delegateLink) {
	t.Helper()
	path := filepath.Join(stateDir, "sessions", rootID, "delegates.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for i, link := range links {
		descriptor := map[string]any{"child_session_id": link.childID, "transcript_ref": "local:" + link.childID, "owner_session_id": rootID, "parent_delegate_id": link.parentDelegateID, "task": "sentinel", "agent_type": "explorer", "tool_name_ceiling": []string{"communicate"}, "resumable": true, "config": map[string]any{}}
		events = append(events, map[string]any{"kind": "delegate_created", "seq": i + 1, "delegate_id": "dlg_" + link.childID, "created": map[string]any{"descriptor": descriptor}})
	}
	batch, err := json.Marshal(map[string]any{"events": events})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(append([]byte("{\"version\":1}\n"), batch...), '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Under a restart-required daemon, a fork continuation and a job-tree root keep
// their restart_required state, including one reachable only through a
// subagent. The subagents themselves produce no row and no ownership error,
// even without a delegate descriptor.
func TestNavigationRestartRequired_ForkAndJobTreeRootsKeepState(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "projects", "upgrade-0000000000")
	daemonID := buildRPCParentSession(t, stateDir)
	forkID, subID, bareSubID, viaSubID := mustSessionID(t), mustSessionID(t), mustSessionID(t), mustSessionID(t)
	save := func(id, parent string, subagent bool) {
		writer, err := transcript.NewWriter(filepath.Join(stateDir, "sessions", id+".transcript.jsonl"), transcript.Header{SessionID: id, ParentSessionID: parent, ProfileID: "openai", Model: "gpt-5"})
		if err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: id, ParentSessionID: parent, IsSubagent: subagent, JobTreeRootSessionID: daemonID, ProfileID: "openai", Model: "gpt-5"}); err != nil {
			t.Fatal(err)
		}
	}
	save(forkID, daemonID, false)
	save(subID, daemonID, true)
	save(bareSubID, daemonID, true)
	save(viaSubID, subID, false)
	writeDelegateDescriptors(t, stateDir, daemonID, delegateLink{childID: forkID}, delegateLink{childID: subID}, delegateLink{childID: viaSubID, parentDelegateID: "dlg_" + subID})

	past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	writeRendezvous(t, runDir, rendezvous.Entry{PID: 1001, Protocol: "evener-appwire-v3", ThreadID: daemonID, SessionID: daemonID, Endpoint: protocolMismatchPeer(t)})
	roster := liveClaimRoster(runDir, &hubcore.StatusProber{})
	roster.Refresh()

	web := &WebServer{cfg: hubcore.WebConfig{Past: past, Roster: roster}}
	snapshot := web.navigationSnapshotInputs(t.Context())

	restartRequired := func(id string) bool {
		return slices.ContainsFunc(snapshot.live, func(entry hubcore.LiveEntry) bool {
			return entry.SessionID == id && entry.Status == appwire.ThreadStatusRestartRequired
		})
	}
	for name, id := range map[string]string{"fork continuation": forkID, "job-tree root under a subagent": viaSubID} {
		if !restartRequired(id) {
			t.Errorf("%s %s lost its restart_required state", name, id)
		}
	}
	for _, id := range []string{subID, bareSubID} {
		if slices.ContainsFunc(snapshot.live, func(entry hubcore.LiveEntry) bool { return entry.SessionID == id }) {
			t.Errorf("subagent %s produced a live entry", id)
		}
	}
	if snapshot.ownershipErr != nil {
		t.Errorf("ownership error = %v, want none: a subagent's ownership is never verified", snapshot.ownershipErr)
	}
}
