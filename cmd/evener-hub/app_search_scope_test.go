package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtest"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

func searchIDs(results []appwire.SearchResult) []string {
	ids := []string{}
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

// A live session's meta sits in the past index too. Its prompt matching lists
// it once, live, with its live state; before, it came back a second time, as
// an ended past result.
func TestHubSearchListsALiveSessionOnceAsLive(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	liveID, endedID := hubtest.SessionID(t), hubtest.SessionID(t)
	for _, meta := range []schema.SessionMeta{
		{ID: liveID, UpdatedAt: time.Now(), Name: "Refactor the queue", OriginalPrompt: "fix the frobnitz drain", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
		{ID: endedID, UpdatedAt: time.Now().Add(-time.Hour), Name: "Frobnitz audit", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"}},
	} {
		if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive})

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Live); !reflect.DeepEqual(got, []string{liveID}) || resp.Live[0].State != "active" {
		t.Fatalf("live = %+v, want the live session once, working", resp.Live)
	}
	if got := searchIDs(resp.Past); !reflect.DeepEqual(got, []string{endedID}) {
		t.Fatalf("past = %v, want only the ended session", got)
	}
	if resp.Scope != appwire.SearchScopeAll {
		t.Fatalf("scope = %q, want the default all echoed", resp.Scope)
	}
}

// A bounded past fetch must not crowd out a live session's own prompt match:
// more than searchPastLimit other past-only sessions also matching the query,
// all newer than the live session's own past-index entry, must not push that
// entry below whatever page size the fetch uses.
func TestHubSearchLivePromptMatchSurvivesManyNewerPastMatches(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	for i := range searchPastLimit + 5 {
		id := hubtest.SessionID(t)
		if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
			ID: id, UpdatedAt: now.Add(-time.Duration(i) * time.Minute),
			Name: "Unrelated frobnitz chatter", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	liveID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: liveID, UpdatedAt: now.Add(-time.Hour), Name: "Refactor the queue",
		OriginalPrompt: "fix the frobnitz drain", EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{PID: 1, WorkingDir: "/projects/alpha", SessionID: liveID, Status: appwire.ThreadStatusActive})

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Live); !reflect.DeepEqual(got, []string{liveID}) {
		t.Fatalf("live = %v, want the live session found via its own past-index prompt, not crowded out by newer matches", got)
	}
}

// The scopes and the archived flag follow the rail (S14, spec 7.1 and 7.4):
// an explicit decision wins; with none, a session whose project is archived,
// or one two weeks without activity, is archived. Live keeps the Live
// section's sessions (live and unarchived); Archived keeps every archived
// session, live or ended.
func TestHubSearchScopesByTheRailsArchiveRules(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	alpha := hubtest.ProjectDir(t, projectsRoot, "alpha")
	beta := hubtest.ProjectDir(t, projectsRoot, "beta")
	ids := map[string]string{}
	for _, s := range []struct {
		name     string
		stateDir string
		updated  time.Time
	}{
		{"live", alpha, now},
		{"liveArchived", alpha, now},
		{"recent", alpha, now.Add(-2 * 24 * time.Hour)},
		{"old", alpha, now.Add(-20 * 24 * time.Hour)},
		{"oldUnarchived", alpha, now.Add(-20 * 24 * time.Hour)},
		{"inArchivedProject", beta, now.Add(-time.Hour)},
	} {
		ids[s.name] = hubtest.SessionID(t)
		if err := schema.SaveSessionMeta(s.stateDir, schema.SessionMeta{ID: ids[s.name], UpdatedAt: s.updated, Name: "Settle " + s.name}); err != nil {
			t.Fatal(err)
		}
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(root, "index.db"))
	for _, d := range []struct {
		kind, id string
		archived bool
	}{
		{"session", ids["liveArchived"], true},
		{"session", ids["oldUnarchived"], false},
		{"project", filepath.Base(beta), true},
	} {
		if err := archive.Set("", d.kind, d.id, d.archived, now); err != nil {
			t.Fatal(err)
		}
	}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SessionID: ids["live"], Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 2, SessionID: ids["liveArchived"], Status: appwire.ThreadStatusIdle},
	)
	cfg := hubcore.WebConfig{Past: past, Roster: roster, Archive: archive}
	search := func(scope string) (live, ended []string, archived map[string]bool) {
		t.Helper()
		resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: scope}, now)
		if err != nil {
			t.Fatal(err)
		}
		archived = map[string]bool{}
		for _, result := range append(append([]appwire.SearchResult{}, resp.Live...), resp.Past...) {
			archived[result.ID] = result.Archived
		}
		return searchIDs(resp.Live), searchIDs(resp.Past), archived
	}
	name := map[string]string{}
	for n, id := range ids {
		name[id] = n
	}
	names := func(got []string) []string {
		out := []string{}
		for _, id := range got {
			out = append(out, name[id])
		}
		return out
	}

	live, ended, archived := search(appwire.SearchScopeAll)
	if len(live) != 2 || len(ended) != 4 {
		t.Fatalf("all: live %v ended %v, want every session", names(live), names(ended))
	}
	want := map[string]bool{"live": false, "liveArchived": true, "recent": false, "old": true, "oldUnarchived": false, "inArchivedProject": true}
	for n, id := range ids {
		if archived[id] != want[n] {
			t.Errorf("archived[%s] = %t, want %t", n, archived[id], want[n])
		}
	}
	if live, ended, _ := search(appwire.SearchScopeLive); !reflect.DeepEqual(names(live), []string{"live"}) || len(ended) != 0 {
		t.Fatalf("live scope: live %v ended %v, want the one unarchived live session", names(live), names(ended))
	}
	live, ended, _ = search(appwire.SearchScopeArchived)
	if got := append(names(live), names(ended)...); len(got) != 3 || strings.Contains(strings.Join(got, ","), "recent") {
		t.Fatalf("archived scope: %v, want liveArchived, old and inArchivedProject", got)
	}
}

// A scope the hub does not know is refused rather than read as all.
func TestHubSearchRefusesAnUnknownScope(t *testing.T) {
	_, err := hubSearch(context.Background(), hubcore.WebConfig{}, appwire.SearchParams{Query: "x", Scope: "pinned"}, time.Now())
	if wire, ok := errors.AsType[appwire.WireError](err); !ok || wire.Code != appwire.CodeInvalidParams {
		t.Fatalf("err = %v, want InvalidParams", err)
	}
}

// A broken archive store must not take down ID/title/prompt search: it never
// failed before archive decisions existed, and a flaky auxiliary index is not
// a reason to stop answering the query a client is waiting on.
func TestHubSearchDegradesWhenArchiveDecisionsFail(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	sessionID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: sessionID, UpdatedAt: time.Now(), Name: "Frobnitz audit"}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// A file that exists but is not a SQLite database: Decisions() fails
	// deterministically rather than taking the "no store configured" no-op
	// path an empty dbPath would.
	dbPath := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged []string
	cfg := hubcore.WebConfig{Past: past, Archive: hubcore.NewArchiveStore(dbPath), Logf: func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}}

	resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "frobnitz"}, time.Now())
	if err != nil {
		t.Fatalf("hubSearch returned an error instead of degrading: %v", err)
	}
	if len(resp.Past) != 1 || resp.Past[0].ID != sessionID {
		t.Fatalf("past = %+v, want the session found despite the broken archive store", resp.Past)
	}
	if len(logged) == 0 {
		t.Fatal("want the archive failure logged, not silently swallowed")
	}
}

// Unlike scope=all, where archive decisions only decorate the Archived flag,
// scope=live and scope=archived filter on it: serving a scope-filtered
// response built from empty (unavailable) decisions while still claiming that
// scope was applied would misreport which sessions are archived. A broken
// archive store must fail closed for these two scopes rather than silently
// answer as if every explicit decision were "not archived."
func TestHubSearchFailsClosedOnAScopedQueryWhenDecisionsFail(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "index.db")
	if err := os.WriteFile(dbPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Archive: hubcore.NewArchiveStore(dbPath), Logf: func(string, ...any) {}}
	for _, scope := range []string{appwire.SearchScopeLive, appwire.SearchScopeArchived} {
		if _, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "x", Scope: scope}, time.Now()); err == nil {
			t.Errorf("scope %q: hubSearch returned no error with unavailable archive decisions, want it to fail closed", scope)
		}
	}
}

// A broken message index must not take down ID/title/prompt search either:
// the In sessions group is simply absent.
func TestHubSearchDegradesWhenMessageIndexFails(t *testing.T) {
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	sessionID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{ID: sessionID, UpdatedAt: time.Now(), Name: "Frobnitz audit"}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := index.Close(); err != nil {
		t.Fatal(err)
	}
	var logged []string
	cfg := hubcore.WebConfig{Past: past, MessageSearch: index, Logf: func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}}

	resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "frobnitz"}, time.Now())
	if err != nil {
		t.Fatalf("hubSearch returned an error instead of degrading: %v", err)
	}
	if len(resp.Past) != 1 || resp.Past[0].ID != sessionID {
		t.Fatalf("past = %+v, want the session found despite the broken message index", resp.Past)
	}
	if resp.InSessions != nil {
		t.Fatalf("inSessions = %+v, want none from a broken index", resp.InSessions)
	}
	if len(logged) == 0 {
		t.Fatal("want the message index failure logged, not silently swallowed")
	}
}

// The In sessions group (S14, spec 7.4): the sessions whose messages match,
// each with how many match and its newest three hits, each hit naming the
// item to open at and a one-line snippet with the matched words marked. A hub
// without a message index answers without the group, and still echoes the
// scope.
func TestHubSearchInSessionsCarriesHitsWithSnippets(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	var turns []schema.Turn
	for i := range 4 {
		turns = append(turns, schema.NewTurn(schema.TurnUserInput, llm.User(strings.Repeat("background ", 10)+"why does the settle pass race? #"+string(rune('a'+i)))))
	}
	_, sessionID := seedSearchSession(t, projectsRoot, "alpha", now.Add(-time.Hour), turns...)
	seedSearchSession(t, projectsRoot, "beta", now.Add(-time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("unrelated work")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, MessageSearch: index}, appwire.SearchParams{Query: "Settle race"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.InSessions) != 1 || resp.InSessions[0].ID != sessionID || resp.InSessions[0].HitCount != 4 || len(resp.InSessions[0].Hits) != 3 {
		t.Fatalf("inSessions = %+v, want the alpha session with 4 matches and its newest 3 hits", resp.InSessions)
	}
	hits := resp.InSessions[0].Hits
	for i, hit := range hits {
		if hit.TranscriptKey == "" || (i > 0 && hit.Position.Entry >= hits[i-1].Position.Entry) {
			t.Fatalf("hits = %+v, want keyed hits, newest first", hits)
		}
		var text strings.Builder
		var matched []string
		for _, part := range hit.Snippet {
			text.WriteString(part.Text)
			if part.Match {
				matched = append(matched, part.Text)
			}
		}
		if !reflect.DeepEqual(matched, []string{"settle", "race"}) || !strings.HasPrefix(text.String(), "…") || strings.ContainsAny(text.String(), "\n") {
			t.Fatalf("snippet %q marks %q, want one line opening with an ellipsis and settle and race marked", text.String(), matched)
		}
	}

	resp, err = hubSearch(context.Background(), hubcore.WebConfig{Past: past}, appwire.SearchParams{Query: "settle"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if resp.InSessions != nil || resp.Scope != appwire.SearchScopeAll {
		t.Fatalf("without an index: inSessions %+v scope %q, want no group and the scope echoed", resp.InSessions, resp.Scope)
	}
}

// The In sessions group keeps only what the scope admits, before its limit.
func TestHubSearchInSessionsFollowsTheScope(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	recent, recentID := seedSearchSession(t, projectsRoot, "alpha", now.Add(-time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("settle it")))
	_, oldID := seedSearchSession(t, projectsRoot, "beta", now.Add(-20*24*time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("settle it")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(root, "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}
	cfg := hubcore.WebConfig{Past: past, MessageSearch: index}
	for scope, want := range map[string][]string{
		appwire.SearchScopeAll:      {recentID, oldID},
		appwire.SearchScopeArchived: {oldID},
		appwire.SearchScopeLive:     {},
	} {
		resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: scope}, now)
		if err != nil {
			t.Fatal(err)
		}
		if got := searchIDs(resp.InSessions); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: inSessions %v, want %v (recent %s)", scope, got, want, recent.ID)
		}
	}
}

// An orphaned live subagent is not a navigation row — the roots-only tree
// gives a subagent no top-level row and lets an orphan (its parent not live)
// vanish (#3082). Search's Live group mirrors the Board's Live section, so it
// must not offer that subagent as a top-level hit either: before, hubSearch
// listed every roster entry, and the subagent's raw "awaiting" state reached
// the palette as a needs-you dot, attention a turn-ended delegate never needs.
// Its own meta, not its parent's liveness, is what marks it a subagent.
func TestHubSearchOmitsALiveSubagentFromTheLiveGroup(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	stateDir := hubtest.ProjectDir(t, projectsRoot, "alpha")
	subagentID := hubtest.SessionID(t)
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: subagentID, UpdatedAt: now, Name: "Refactor the frobnitz queue",
		ParentSessionID: "02wMz5TxvParentAbsent", IsSubagent: true,
		EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/alpha"},
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	// The subagent has its own live daemon and its normal turn-ended resting
	// state ("awaiting"), with no real ask_pending; its parent is not live.
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		PID: 1, WorkingDir: "/projects/alpha", SessionID: subagentID, Status: appwire.ThreadStatusAwaiting,
	})

	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.Live); len(got) != 0 {
		t.Fatalf("live = %v, want no live result: an orphaned subagent is not a top-level search hit", got)
	}
	if prev, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster}, appwire.SearchParams{Query: "frobnitz", Scope: appwire.SearchScopeLive}, now); err != nil {
		t.Fatal(err)
	} else if got := searchIDs(prev.Live); len(got) != 0 {
		t.Fatalf("live scope: live = %v, want no result either", got)
	}
}

// The primary production configuration (a roster and a message index both
// configured) must find a live session whose only match is its message text:
// it is listed once, in Live order ahead of ended sessions, not duplicated as
// an ended result, and follows the live/archived scopes like any other live
// session.
func TestHubSearchInSessionsIncludesALiveSessionMatchedOnlyByMessage(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	projectsRoot := filepath.Join(root, "projects")
	liveEntry, liveID := seedSearchSession(t, projectsRoot, "alpha", now, schema.NewTurn(schema.TurnUserInput, llm.User("settle the drain")))
	_, endedID := seedSearchSession(t, projectsRoot, "beta", now.Add(-time.Hour), schema.NewTurn(schema.TurnUserInput, llm.User("settle it too")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	index, err := hubcore.OpenMessageSearch(filepath.Join(root, "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.Close() })
	if _, err := index.Refresh(context.Background(), messageSearchSessions(past.All())); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		PID: 1, WorkingDir: liveEntry.Meta.EnvInfo.WorkingDir, SessionID: liveID, Status: appwire.ThreadStatusIdle,
	})
	cfg := hubcore.WebConfig{Past: past, Roster: roster, MessageSearch: index}

	resp, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(resp.InSessions); !reflect.DeepEqual(got, []string{liveID, endedID}) {
		t.Fatalf("inSessions = %v, want the live session once (Live order first), then the ended one", got)
	}
	if resp.InSessions[0].State == "ended" {
		t.Fatalf("inSessions[0] = %+v, want the live session's real state, not the past group's \"ended\"", resp.InSessions[0])
	}

	live, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: appwire.SearchScopeLive}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := searchIDs(live.InSessions); !reflect.DeepEqual(got, []string{liveID}) {
		t.Fatalf("live scope: inSessions %v, want only the live session", got)
	}
	archived, err := hubSearch(context.Background(), cfg, appwire.SearchParams{Query: "settle", Scope: appwire.SearchScopeArchived}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(archived.InSessions) != 0 {
		t.Fatalf("archived scope: inSessions = %+v, want none: neither session is archived", archived.InSessions)
	}
}

// A live session's archived flag consults the project decision of the source
// that owns it, as the Board's Live section does: a remote host archiving its
// project archives its own live session there, never this hub's live session
// in a project of the same ID.
func TestHubSearchLiveArchivedFollowsTheOwningSource(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "index.db"))
	if err := archive.Set("h", "project", "shared", true, now); err != nil {
		t.Fatal(err)
	}
	project := identifier.Project{ID: "shared"}
	roster := hubcore.NewRosterWithEntries(
		hubcore.LiveEntry{PID: 1, SourceID: "h", SessionID: "h:remote", Project: project, Status: appwire.ThreadStatusIdle},
		hubcore.LiveEntry{PID: 2, SourceID: "local", SessionID: "own", Project: project, Status: appwire.ThreadStatusIdle},
	)
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Roster: roster, Archive: archive}, appwire.SearchParams{}, now)
	if err != nil {
		t.Fatal(err)
	}
	archived := map[string]bool{}
	for _, result := range resp.Live {
		archived[result.ID] = result.Archived
	}
	if want := map[string]bool{"h:remote": true, "own": false}; !reflect.DeepEqual(archived, want) {
		t.Fatalf("live archived = %v, want %v", archived, want)
	}
}

// A live session's archived flag must still resolve its owning project when
// the roster entry carries none: production roster ingestion never sets
// LiveEntry.Project (only the Board's tree-building path resolves one, onto
// its own local copy that search never sees), so the flag falls back to the
// past entry's state directory, named by its project's ID, the same way
// pastSearchResult already does.
func TestHubSearchLiveArchivedFallsBackToThePastEntrysProjectWhenRosterCarriesNone(t *testing.T) {
	now := time.Now()
	projectsRoot := filepath.Join(t.TempDir(), "projects")
	pe, sessionID := seedSearchSession(t, projectsRoot, "alpha", now, schema.NewTurn(schema.TurnUserInput, llm.User("hello")))
	past := hubcore.NewPastIndex(filepath.Join(projectsRoot, "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	archive := hubcore.NewArchiveStore(filepath.Join(t.TempDir(), "index.db"))
	if err := archive.Set("", "project", filepath.Base(pe.StateDir), true, now); err != nil {
		t.Fatal(err)
	}
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{PID: 1, SessionID: sessionID, Status: appwire.ThreadStatusIdle})
	resp, err := hubSearch(context.Background(), hubcore.WebConfig{Past: past, Roster: roster, Archive: archive}, appwire.SearchParams{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Live) != 1 || !resp.Live[0].Archived {
		t.Fatalf("live = %+v, want the session marked archived", resp.Live)
	}
}

// A state-dir basename names a project only when it is a well-formed project
// ID. The navigation tree skips a past entry whose basename fails
// identifier.ValidateProjectID when it builds its project candidates
// (web_api_tree.go), so search's ended-session archived lookup must skip the
// same malformed ID rather than applying a project decision the tree would
// never see — the two paths must agree on what counts as a valid project ID
// for the same session data (#2775).
func TestPastSearchResultSkipsAMalformedStateDirProjectID(t *testing.T) {
	now := time.Now()
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	entry := hubcore.PastEntry{
		ID:       sessionID,
		Meta:     schema.SessionMeta{ID: sessionID, UpdatedAt: now},
		StateDir: filepath.Join(t.TempDir(), "stray-dir"),
	}
	// A decision keyed by the malformed basename, as a stray store row could
	// be. The tree never builds a candidate from this basename, so search must
	// not apply its project decision either.
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "project", ID: filepath.Base(entry.StateDir)}: true,
	}
	if got := pastSearchResult(entry, decisions, now); got.Archived {
		t.Fatalf("Archived = true for state-dir basename %q, want false: a basename that is not a valid project ID must skip the project decision, as the navigation tree does", filepath.Base(entry.StateDir))
	}
}

// The same rule for the live fallback: the roster carries no project, so a
// live result falls back to its past entry's state directory. An empty state
// dir has basename "." — not a well-formed project ID — and the tree skips
// such a basename, so the live archived lookup must skip a project decision
// keyed by it too (#2775).
func TestLiveSearchResultSkipsAMalformedStateDirProjectID(t *testing.T) {
	now := time.Now()
	sessionID, err := identifier.NewSessionID()
	if err != nil {
		t.Fatal(err)
	}
	// SeedForTest leaves StateDir blank: filepath.Base("") is ".".
	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{{ID: sessionID, UpdatedAt: now}})
	decisions := map[hubcore.ArchiveKey]bool{
		{Kind: "project", ID: filepath.Base("")}: true,
	}
	le := hubcore.LiveEntry{PID: 1, SessionID: sessionID, Status: appwire.ThreadStatusIdle}
	if got := liveSearchResult(hubcore.WebConfig{Past: past}, le, decisions, now); got.Archived {
		t.Fatalf("Archived = true for an empty state dir (basename %q), want false: a basename that is not a valid project ID must skip the project decision, as the navigation tree does", filepath.Base(""))
	}
}

// stateDirProjectID is the one derivation the navigation tree and search
// share: it accepts a well-formed project-ID basename and rejects anything
// else, so both paths agree on which sessions carry a project decision
// (#2775).
func TestStateDirProjectIDValidatesTheBasename(t *testing.T) {
	valid := filepath.Base(hubtest.ProjectDir(t, filepath.Join(t.TempDir(), "projects"), "alpha"))
	if id, ok := stateDirProjectID(filepath.Join("projects", valid)); !ok || id != valid {
		t.Fatalf("stateDirProjectID(%q) = %q, %t, want %q, true", valid, id, ok, valid)
	}
	for _, bad := range []string{"/projects/stray-dir", "", ".", "/projects/no-suffix"} {
		if id, ok := stateDirProjectID(bad); ok {
			t.Errorf("stateDirProjectID(%q) = %q, true, want ok=false", bad, id)
		}
	}
}
