package agent

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

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
	response := appwire.SessionJobsResponse{}
	for _, delta := range []int{-1, 0, 1} {
		t.Run(strconv.Itoa(delta), func(t *testing.T) {
			budget := newSessionActivityPageBudget(response)
			row := appwire.JobActivityJob{JobID: "boundary"}
			full := response
			full.Jobs = []appwire.JobActivityJob{row}
			encoded, err := json.Marshal(full)
			if err != nil {
				t.Fatal(err)
			}
			row.Description = strings.Repeat("x", sessionActivityPageBytes-2048+delta-len(encoded))
			full.Jobs[0] = row
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
				row.Description = "small replacement"
				full.Jobs[0] = row
				encoded, _ = json.Marshal(full)
				if !budget.fits(row, full) || budget.bytes != len(encoded) {
					t.Fatal("rejected candidate polluted the following probe")
				}
			}
		})
	}
}
