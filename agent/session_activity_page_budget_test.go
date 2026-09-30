package agent

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

type sessionActivityCountedRow struct {
	calls *int
	text  string
}

func (row sessionActivityCountedRow) MarshalJSON() ([]byte, error) {
	*row.calls++
	return json.Marshal(row.text)
}

func TestSessionActivityPageBudgetEncodesEachCandidateOnce(t *testing.T) {
	t.Parallel()
	calls := 0
	response := struct {
		Rows []sessionActivityCountedRow `json:"rows"`
	}{Rows: []sessionActivityCountedRow{}}
	budget := newSessionActivityPageBudget(response)
	for range 200 {
		row := sessionActivityCountedRow{calls: &calls, text: "candidate"}
		response.Rows = append(response.Rows, row)
		if !budget.fits(row, response) {
			t.Fatal("compact page failed to fit")
		}
	}
	if calls != 200 {
		t.Fatalf("200 candidates caused %d row encodings; want 200", calls)
	}
	row := sessionActivityCountedRow{calls: &calls, text: strings.Repeat("x", sessionActivityPageBytes)}
	response.Rows = append(response.Rows, row)
	if budget.fits(row, response) || calls != 201 {
		t.Fatalf("rejected row re-encoded earlier candidates: calls=%d", calls)
	}
}

func TestSessionActivityPageBudgetPreservesMarshalFailureFallback(t *testing.T) {
	t.Parallel()
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		response := appwire.SessionWatchesResponse{}
		budget := newSessionActivityPageBudget(response)
		for _, row := range []appwire.SessionWatch{
			{Watch: appwire.EvenerWatchInfo{ID: "healthy"}},
			{Watch: appwire.EvenerWatchInfo{ID: "invalid", Cadence: []appwire.EvenerWatchCadence{{Kind: "every", Seconds: invalid}}}},
			{Watch: appwire.EvenerWatchInfo{ID: "large", Note: strings.Repeat("x", sessionActivityPageBytes)}},
		} {
			response.Watches = append(response.Watches, row)
			encoded, _ := json.Marshal(response)
			want := len(encoded) <= sessionActivityPageBytes-2048
			if got := budget.fits(row, response); got != want || budget.bytes != len(encoded) {
				t.Fatalf("marshal fallback diverged: fit=%v want=%v bytes=%d encoded=%d", got, want, budget.bytes, len(encoded))
			}
		}
	}
	// An unencodable fixed envelope must also use the original whole-page probe.
	response := struct {
		Number float64  `json:"number"`
		Rows   []string `json:"rows"`
	}{Number: math.NaN(), Rows: []string{}}
	budget := newSessionActivityPageBudget(response)
	row := strings.Repeat("x", sessionActivityPageBytes)
	response.Rows = append(response.Rows, row)
	encoded, _ := json.Marshal(response)
	if !budget.fits(row, response) || budget.bytes != len(encoded) {
		t.Fatal("unencodable envelope changed the error-ignored fit result")
	}
}

func TestSessionActivityPageBudgetMatchesTypedEncoding(t *testing.T) {
	t.Parallel()
	text := "<>&\"\\\n\t界\u2028\u2029" + string([]byte{0xff})
	for _, variant := range []string{"nil", "empty", "populated"} {
		t.Run(variant, func(t *testing.T) {
			context := appwire.SessionActivityContext{Ref: text, SessionID: "root", RootRef: "local:root", Epoch: "epoch", AncestryKnown: true}
			page := appwire.SessionActivityPage{}
			if variant == "empty" {
				context.Ancestors = []appwire.SessionActivityAncestor{}
				page.Issues = []appwire.SessionActivityIssue{}
			}
			if variant == "populated" {
				context.Ancestors = []appwire.SessionActivityAncestor{{Ref: "local:parent", SessionID: "parent", Title: text}}
				page.Issues = []appwire.SessionActivityIssue{{Ref: text, Code: "unavailable"}}
			}
			delegates := []appwire.SessionDelegate{
				{DelegateID: "one", Description: text, Task: text},
				{DelegateID: "two", Reason: text, Error: text, Usage: &appwire.EvenerUsage{TotalTokens: 9007199254740993}, Worktree: &appwire.JobActivityWorktree{Path: text, Dirty: true}},
				{DelegateID: "three", Task: strings.Repeat(text, 200)},
			}
			jobs := []appwire.JobActivityJob{
				{JobID: "one", OwnerRef: text, Description: text},
				{JobID: "two", TranscriptRef: text, Command: text, OutputBytes: 9007199254740993, ExitCode: new(0)},
				{JobID: "three", Task: strings.Repeat(text, 200)},
			}
			watches := []appwire.SessionWatch{
				{OwnerRef: text, ReceiverRef: text, Watch: appwire.EvenerWatchInfo{ID: "one", Note: text}},
				{Watch: appwire.EvenerWatchInfo{ID: "two", Events: []string{text}, Cadence: []appwire.EvenerWatchCadence{{Kind: "every", Seconds: 1.25}}, DeliveryTimes: []string{text}}},
				{Watch: appwire.EvenerWatchInfo{ID: "three", OutputMatch: strings.Repeat(text, 200)}},
			}
			t.Run("delegates", func(t *testing.T) {
				assertSessionActivityPageEncoding(t, delegates, func(rows []appwire.SessionDelegate) any {
					return appwire.SessionDelegatesResponse{Context: context, Scope: appwire.SessionActivityScopeSubtree, Page: page, Delegates: rows}
				})
			})
			t.Run("jobs", func(t *testing.T) {
				assertSessionActivityPageEncoding(t, jobs, func(rows []appwire.JobActivityJob) any {
					return appwire.SessionJobsResponse{Context: context, Scope: appwire.SessionActivityScopeSubtree, Page: page, Jobs: rows}
				})
			})
			t.Run("watches", func(t *testing.T) {
				assertSessionActivityPageEncoding(t, watches, func(rows []appwire.SessionWatch) any {
					return appwire.SessionWatchesResponse{Context: context, Scope: appwire.SessionActivityScopeSubtree, Page: page, Watches: rows}
				})
			})
		})
	}
}

func assertSessionActivityPageEncoding[T any](t *testing.T, candidates []T, response func([]T) any) {
	t.Helper()
	for _, empty := range [][]T{nil, {}} {
		budget := newSessionActivityPageBudget(response(empty))
		encoded, err := json.Marshal(response(empty))
		if err != nil || budget.bytes != len(encoded) {
			t.Fatalf("empty envelope bytes=%d, encoded=%d err=%v", budget.bytes, len(encoded), err)
		}
		rows := empty
		for _, row := range candidates {
			rows = append(rows, row)
			full := response(rows)
			encoded, err = json.Marshal(full)
			if err != nil {
				t.Fatal(err)
			}
			want := len(encoded) <= sessionActivityPageBytes-2048
			if got := budget.fits(row, full); got != want {
				t.Fatalf("fit=%v want=%v at %d bytes", got, want, len(encoded))
			}
			if want && budget.bytes != len(encoded) {
				t.Fatalf("admitted bytes=%d encoded=%d", budget.bytes, len(encoded))
			}
		}
	}
}

func TestSessionActivityPageBudgetBoundaryAndRejectedRow(t *testing.T) {
	t.Parallel()
	for _, resource := range []struct {
		name      string
		empty     any
		candidate func(string) (any, any)
	}{
		{"delegates", appwire.SessionDelegatesResponse{}, func(text string) (any, any) {
			row := appwire.SessionDelegate{DelegateID: "boundary", Description: text}
			return row, appwire.SessionDelegatesResponse{Delegates: []appwire.SessionDelegate{row}}
		}},
		{"jobs", appwire.SessionJobsResponse{}, func(text string) (any, any) {
			row := appwire.JobActivityJob{JobID: "boundary", Description: text}
			return row, appwire.SessionJobsResponse{Jobs: []appwire.JobActivityJob{row}}
		}},
		{"watches", appwire.SessionWatchesResponse{}, func(text string) (any, any) {
			row := appwire.SessionWatch{Watch: appwire.EvenerWatchInfo{ID: "boundary", Note: text}}
			return row, appwire.SessionWatchesResponse{Watches: []appwire.SessionWatch{row}}
		}},
	} {
		t.Run(resource.name, func(t *testing.T) {
			for _, delta := range []int{-1, 0, 1} {
				t.Run(strconv.Itoa(delta), func(t *testing.T) {
					budget := newSessionActivityPageBudget(resource.empty)
					_, full := resource.candidate("x")
					encoded, err := json.Marshal(full)
					if err != nil {
						t.Fatal(err)
					}
					row, full := resource.candidate(strings.Repeat("x", sessionActivityPageBytes-2048+delta-len(encoded)+1))
					encoded, err = json.Marshal(full)
					if err != nil || len(encoded) != sessionActivityPageBytes-2048+delta {
						t.Fatalf("boundary fixture bytes=%d err=%v", len(encoded), err)
					}
					before := budget.bytes
					if got := budget.fits(row, full); got != (delta <= 0) {
						t.Fatalf("boundary delta=%d fit=%v", delta, got)
					}
					if delta > 0 {
						if budget.bytes != before {
							t.Fatal("rejected row changed admitted bytes")
						}
						row, full = resource.candidate("small replacement")
						encoded, _ = json.Marshal(full)
						if !budget.fits(row, full) || budget.bytes != len(encoded) {
							t.Fatal("rejected candidate polluted the following probe")
						}
					}
				})
			}
		})
	}
}
