package hub

import (
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
)

// A Failed row says why (S1c): the failure's headline and its cause's kind,
// provider and HTTP status, the title re-cut to the wire's bound. A crashed
// daemon's row names the hub's own crashed cause, and any other row carries
// no key. The summary has no message to give, so none reaches a row.
func TestNavigationRowsCarryTheFailure(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
		{ID: "session-failed", Title: "failed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Title: "Provider error", Cause: &appwire.DiagnosticCause{Kind: "provider", Provider: "codex-jesse-fsck.com", Model: "gpt-5.6", Status: 401},
		}},
		{ID: "session-crashed", Title: "crashed", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{
			Cause: &appwire.DiagnosticCause{Kind: hubapi.NavigationFailureCrashed},
		}},
		{ID: "session-wide", Title: "wide", Kind: "session", State: "errored", Failure: &appwire.ThreadFailure{Title: "Provider\nerror " + strings.Repeat("x", 200)}},
		{ID: "session-idle", Title: "idle", Kind: "session", State: "idle"},
	})
	for id, want := range map[string]string{
		"session-failed":  `{"title":"Provider error","cause_kind":"provider","provider":"codex-jesse-fsck.com","status":401}`,
		"session-crashed": `{"cause_kind":"crashed"}`,
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

// A summary with nothing to say (no title, and no cause kind), which only a
// malformed daemon answer can carry, is dropped from its row instead of
// failing the whole resource; the row stays Failed.
func TestNavigationRowsDropAFailureWithNothingToSay(t *testing.T) {
	rows := liveTaskRows(t, []hubcore.TreeNode{
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

// A cloned summary owns its failure.
func TestCloneNavigationSummaryOwnsTheFailure(t *testing.T) {
	original := hubapi.NavigationSessionSummary{Failure: &hubapi.NavigationFailure{Title: "Provider error", CauseKind: "provider", Status: 401}}
	clone := cloneNavigationSummary(original)
	original.Failure.Status = 500
	if clone.Failure == nil || clone.Failure.Status != 401 {
		t.Fatalf("clone failure = %+v, want its own copy", clone.Failure)
	}
}
