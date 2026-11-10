package hub

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A Failed row says why (S1c): the failure's headline and its cause's kind,
// provider and HTTP status, the title re-cut to the wire's bound. A crashed
// daemon's row names the hub's own crashed cause, and any other row carries
// no key. The summary has no message to give, so none reaches a row.
func TestNavigationRowsCarryTheFailure(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-failed", Title: "failed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Model: "gpt-5.6", Status: 401},
		}},
		{ID: "session-crashed", Title: "crashed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed},
		}},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Title: "Provider\nerror " + strings.Repeat("x", 200)}},
		{ID: "session-quota", Title: "quota", Kind: "session", State: "active", Failure: &appwire.ThreadFailure{
			Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "oai-jrv", Status: 429},
			ResumesAt: time.Date(2026, 11, 7, 14, 30, 0, 0, time.UTC).UnixMilli(),
		}},
		{ID: "session-idle", Title: "idle", Kind: "session", State: "idle"},
	})
	for id, want := range map[string]string{
		"session-failed":  `{"title":"Provider error","cause_kind":"provider","provider":"codex-jesse-fsck.com","status":401}`,
		"session-crashed": `{"cause_kind":"crashed"}`,
		"session-quota":   `{"title":"Usage limit reached","cause_kind":"provider","provider":"oai-jrv","status":429,"resumes_at":"2026-11-07T14:30:00Z"}`,
	} {
		if got := string(navigationSummaryJSONFields(t, rows[id])["failure"]); got != want {
			t.Errorf("%s failure on the wire = %s, want %s", id, got, want)
		}
	}
	wide := rows["session-wide"].Failure
	if wide == nil || strings.ContainsAny(wide.Title, "\r\n") || utf8.RuneCountInString(wide.Title) > appwire.MaxFailureTitleRunes {
		t.Fatalf("wide failure = %+v, want one line of at most %d runes", wide, appwire.MaxFailureTitleRunes)
	}
	if failure, carried := navigationSummaryJSONFields(t, rows["session-idle"])["failure"]; carried {
		t.Fatalf("a row that is not failed carries %s", failure)
	}
}

// A session whose model call waits out a usage limit is working, not Failed,
// and its daemon's thread carries the limit's summary with the time the call
// resumes. That summary must survive the hub's own tree builder onto the
// working row the web rail and the phone Board read, while a working row
// with no resume time still carries no failure.
func TestNavigationRowsCarryAQuotaWaitFromAWorkingThread(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	resumesAt := now.Add(2 * time.Hour)
	thread := func(id string, failure *appwire.ThreadFailure) hubcore.LiveEntry {
		t.Helper()
		_, entry, ok := appThreadTreeEntries(appwire.Thread{
			ID: id, Source: "local", ModelProvider: "gpt-5.6",
			Status: appwire.ThreadStatus{Type: appwire.ThreadStatusActive},
			Evener: appwire.EvenerThread{Ref: "local:" + id, Failure: failure},
		})
		if !ok {
			t.Fatalf("appThreadTreeEntries rejected %s", id)
		}
		entry.PID = len(id)
		return entry
	}
	live := []hubcore.LiveEntry{
		thread("01WAITING", &appwire.ThreadFailure{
			Title: "Usage limit reached", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "oai-jrv", Status: 429},
			ResumesAt: resumesAt.UnixMilli(),
		}),
		thread("01WORKING", &appwire.ThreadFailure{Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Status: 500}}),
	}
	var metas []schema.SessionMeta
	liveIDs := map[string]bool{}
	for _, entry := range live {
		liveIDs[entry.SessionID] = true
		metas = append(metas, schema.SessionMeta{ID: entry.SessionID, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), EnvInfo: schema.EnvironmentInfo{WorkingDir: "/projects/evener"}})
	}
	projection, err := buildNavigationProjection(navigationBuildInputs{GenerationID: "generation", Tree: hubcore.BuildTreeAt(metas, live, nil, now), Live: liveIDs})
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]hubapi.NavigationSessionSummary{}
	for _, row := range projection.LivePage(0, 0).Sessions {
		rows[row.SessionID] = row
	}
	waiting, listed := rows["01WAITING"]
	if !listed || hubcore.NormalizeState(waiting.State) == "errored" {
		t.Fatalf("waiting row = %+v (listed %v), want a working row", waiting, listed)
	}
	if waiting.Failure == nil || waiting.Failure.ResumesAt == nil || !waiting.Failure.ResumesAt.Equal(resumesAt) || waiting.Failure.Status != 429 {
		t.Fatalf("waiting row failure = %+v, want the usage limit resuming at %v", waiting.Failure, resumesAt)
	}
	if working := rows["01WORKING"]; working.Failure != nil {
		t.Fatalf("a working row with no resume time carries the failure %+v", working.Failure)
	}
}

// A summary with nothing to say (no title, and no cause kind), which only a
// malformed daemon answer can carry, is dropped from its row instead of
// failing the whole resource; the row stays Failed.
func TestNavigationRowsDropAFailureWithNothingToSay(t *testing.T) {
	rows := liveNavigationRows(t, []hubcore.TreeNode{
		{ID: "session-blank", Title: "blank", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Title: " \n "}},
		{ID: "session-kindless", Title: "kindless", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Cause: &appwire.DiagnosticCause{Provider: "openai", Status: 500}}},
	})
	for _, id := range []string{"session-blank", "session-kindless"} {
		row, listed := rows[id]
		if !listed || row.State != "errored" || row.Failure != nil {
			t.Errorf("%s = %+v (listed %v), want the Failed row listed without a failure", id, row.Failure, listed)
		}
	}
}

// The hub schema refuses a failure the codec would refuse, and a title the
// projector's cut never yields.
func TestNavigationSchemaBoundsTheFailure(t *testing.T) {
	session := navigationSchemaSession("local:schema-session", "schema-session")
	session.Failure = &hubapi.NavigationFailure{
		Title:     strings.Repeat("é", appwire.MaxFailureTitleRunes),
		CauseKind: "provider",
		Provider:  strings.Repeat("p", maxNavigationIdentityBytes),
		Status:    401,
	}
	if !navigationSessionValueValid(session) {
		t.Fatal("a failure at every bound was refused")
	}
	for name, failure := range map[string]hubapi.NavigationFailure{
		"nothing to say":                     {},
		"a status alone":                     {Status: 500},
		"a title past its bound":             {Title: strings.Repeat("é", appwire.MaxFailureTitleRunes+1)},
		"a title with a line break":          {Title: "Provider\nerror"},
		"a provider past the identity bound": {CauseKind: "provider", Provider: strings.Repeat("p", maxNavigationIdentityBytes+1)},
		"invalid UTF-8 in the cause kind":    {CauseKind: "prov\xffider"},
		"a negative status":                  {CauseKind: "provider", Status: -1},
	} {
		session.Failure = &failure
		if navigationSessionValueValid(session) {
			t.Errorf("a failure with %s was accepted", name)
		}
	}
}

// A cloned summary owns its failure, the resume time included.
func TestCloneNavigationSummaryOwnsTheFailure(t *testing.T) {
	resumesAt := time.Date(2026, 11, 7, 14, 30, 0, 0, time.UTC)
	originalResumesAt := resumesAt
	original := hubapi.NavigationSessionSummary{Failure: &hubapi.NavigationFailure{Title: "Provider error", CauseKind: "provider", Status: 401, ResumesAt: &originalResumesAt}}
	clone := cloneNavigationSummary(original)
	original.Failure.Status = 500
	*original.Failure.ResumesAt = resumesAt.Add(time.Hour)
	if clone.Failure == nil || clone.Failure.Status != 401 || !clone.Failure.ResumesAt.Equal(resumesAt) {
		t.Fatalf("clone failure = %+v, want its own copy", clone.Failure)
	}
}
