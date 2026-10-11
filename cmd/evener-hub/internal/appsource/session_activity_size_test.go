package appsource

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityTranslatedPagesStayBounded(t *testing.T) {
	for _, method := range []string{appwire.MethodEvenerThreadDelegatesList, appwire.MethodEvenerThreadJobsList, appwire.MethodEvenerThreadWatchesList} {
		t.Run(method, func(t *testing.T) {
			const total = 451
			sourceID := strings.Repeat("remote-", 260)
			offsets := map[string]int{"": 0}
			source, calls := newScriptedRemote(t, sourceID, func(got string, raw json.RawMessage) scriptedReply {
				if got != method {
					return scriptedReply{wireErr: new(appwire.MethodNotFound(got))}
				}
				var params appwire.SessionActivityListParams
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Error(err)
				}
				start, ok := offsets[params.Cursor]
				if !ok {
					t.Errorf("unknown source cursor %q", params.Cursor)
				}
				end := min(start+params.Limit, total)
				page := appwire.SessionActivityPage{Complete: end == total}
				if end < total {
					page.NextCursor = fmt.Sprintf("opaque-page-%d", end)
					offsets[page.NextCursor] = end
				}
				ctx := appwire.SessionActivityContext{Ref: "local:root", SessionID: "root", RootRef: "local:root", AncestryKnown: true}
				delegates := appwire.SessionDelegatesResponse{Context: ctx, Page: page}
				jobs := appwire.SessionJobsResponse{Context: ctx, Page: page}
				watches := appwire.SessionWatchesResponse{Context: ctx, Page: page}
				for i := start; i < end; i++ {
					id := fmt.Sprintf("row-%d", i)
					delegates.Delegates = append(delegates.Delegates, appwire.SessionDelegate{DelegateID: id, OwnerRef: "local:root", RootRef: "local:root", ChildRef: "local:" + id})
					jobs.Jobs = append(jobs.Jobs, appwire.JobActivityJob{JobID: id, OwnerRef: "local:root", TranscriptRef: "local:root"})
					watches.Watches = append(watches.Watches, appwire.SessionWatch{OwnerRef: "local:root", ReceiverRef: "local:root", Watch: appwire.EvenerWatchInfo{ID: id}})
				}
				switch method {
				case appwire.MethodEvenerThreadDelegatesList:
					return scriptedReply{result: delegates}
				case appwire.MethodEvenerThreadJobsList:
					return scriptedReply{result: jobs}
				default:
					return scriptedReply{result: watches}
				}
			})
			cursor := ""
			seen := map[string]bool{}
			for range total {
				params := appwire.SessionActivityListParams{Ref: sourceID + ":root", Limit: 200, Cursor: cursor}
				value, page, ids := readActivitySizedPage(t, source, method, params)
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if len(raw) > 256<<10 {
					t.Fatalf("translated response is %d bytes", len(raw))
				}
				for _, id := range ids {
					if seen[id] {
						t.Fatalf("duplicate identity %s", id)
					}
					seen[id] = true
				}
				if page.Complete {
					break
				}
				if page.NextCursor == "" || page.NextCursor == cursor {
					t.Fatal("page did not advance")
				}
				cursor = page.NextCursor
			}
			if len(seen) != total {
				t.Fatalf("walk retained %d/%d identities", len(seen), total)
			}
			requests := wireCalls(calls())
			if len(requests) <= 3 {
				t.Fatalf("no measured overflow rereads: %d calls", len(requests))
			}
			for i := 1; i < len(requests); i++ {
				var prior, next appwire.SessionActivityListParams
				_ = json.Unmarshal(requests[i-1].params, &prior)
				_ = json.Unmarshal(requests[i].params, &next)
				if next.Limit < prior.Limit && next.Cursor != prior.Cursor {
					t.Fatal("fitting advanced the opaque cursor before admitting a page")
				}
			}
		})
	}
}

func readActivitySizedPage(t *testing.T, source *RemoteHubSource, method string, params appwire.SessionActivityListParams) (any, appwire.SessionActivityPage, []string) {
	t.Helper()
	ids := []string{}
	switch method {
	case appwire.MethodEvenerThreadDelegatesList:
		out, err := source.ThreadDelegatesList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out.Delegates {
			ids = append(ids, row.DelegateID)
		}
		return out, out.Page, ids
	case appwire.MethodEvenerThreadJobsList:
		out, err := source.ThreadJobsList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out.Jobs {
			ids = append(ids, row.JobID)
		}
		return out, out.Page, ids
	default:
		out, err := source.ThreadWatchesList(t.Context(), params)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range out.Watches {
			ids = append(ids, row.Watch.ID)
		}
		return out, out.Page, ids
	}
}

func TestSessionActivityTranslatedPageSingleRowOversize(t *testing.T) {
	source, calls := newScriptedRemote(t, strings.Repeat("host", 70000), func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.SessionJobsResponse{Context: appwire.SessionActivityContext{Ref: "local:root"}, Jobs: []appwire.JobActivityJob{{JobID: "only", OwnerRef: "local:root"}}}}
	})
	_, err := source.ThreadJobsList(t.Context(), appwire.SessionActivityListParams{Ref: source.ID() + ":root", Limit: 1})
	var wire appwire.WireError
	if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
		t.Fatalf("oversized compact response = %v", err)
	}
	if data, ok := wire.Data.(appwire.ErrorData); !ok || data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.RetryDisposition != "" {
		t.Fatalf("unrepresentable response must be permanently unavailable: %+v", wire)
	}
	if len(wireCalls(calls())) != 1 {
		t.Fatal("limit-one overflow was reread")
	}
}

func TestSessionActivityTranslatedPageFitsWithoutReread(t *testing.T) {
	source, calls := newScriptedRemote(t, "remote", func(_ string, _ json.RawMessage) scriptedReply {
		return scriptedReply{result: appwire.SessionJobsResponse{Jobs: []appwire.JobActivityJob{{JobID: "only", OwnerRef: "local:root"}}, Page: appwire.SessionActivityPage{Complete: true}}}
	})
	out, err := source.ThreadJobsList(t.Context(), appwire.SessionActivityListParams{Ref: "remote:root"})
	if err != nil || len(out.Jobs) != 1 || out.Jobs[0].OwnerRef != "remote:root" {
		t.Fatalf("fitting response = %+v, %v", out, err)
	}
	if len(wireCalls(calls())) != 1 {
		t.Fatal("fitting response dispatched more than once")
	}
}

func TestSessionActivityTranslatedSummaryEnvelope(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		t.Run(fmt.Sprintf("oversized=%t", oversized), func(t *testing.T) {
			context := appwire.SessionActivityContext{Ref: "local:selected", SessionID: "selected", RootRef: "local:root", ParentRef: "local:parent", AncestryKnown: true, Epoch: "epoch", Availability: "live"}
			if oversized {
				for i := range 150 {
					id := fmt.Sprintf("ancestor-%d", i)
					context.Ancestors = append(context.Ancestors, appwire.SessionActivityAncestor{Ref: "local:" + id, SessionID: id})
				}
			}
			summary := appwire.SessionActivitySummary{Context: context, Scope: appwire.SessionActivityScopeSubtree}
			raw, err := json.Marshal(summary)
			if err != nil || len(raw) > 256<<10 {
				t.Fatalf("source summary is not a fitting fixture: %d bytes, %v", len(raw), err)
			}
			sourceID := "remote"
			if oversized {
				sourceID = strings.Repeat("remote-", 260)
			}
			source, calls := newScriptedRemote(t, sourceID, func(method string, raw json.RawMessage) scriptedReply {
				if method != appwire.MethodEvenerThreadActivityRead {
					t.Errorf("unexpected method %s", method)
				}
				var params appwire.SessionActivityReadParams
				if err := json.Unmarshal(raw, &params); err != nil {
					t.Error(err)
				}
				if params.Ref != "local:selected" || params.Scope != appwire.SessionActivityScopeSubtree {
					t.Errorf("source request=%+v", params)
				}
				return scriptedReply{result: summary}
			})
			out, err := source.ThreadActivityRead(t.Context(), appwire.SessionActivityReadParams{Ref: sourceID + ":selected", Scope: appwire.SessionActivityScopeSubtree})
			if oversized {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
					encoded, _ := json.Marshal(out)
					t.Fatalf("qualified summary %d bytes from source %d bytes: %v", len(encoded), len(raw), err)
				}
				data, ok := wire.Data.(appwire.ErrorData)
				if !ok || data.EvenerErrorInfo != appwire.ErrorActionUnavailable || data.RetryDisposition != "" {
					t.Fatalf("unrepresentable context classification=%+v", wire)
				}
			} else if err != nil || out.Context.Ref != "remote:selected" || out.Context.RootRef != "remote:root" || out.Context.ParentRef != "remote:parent" || out.Context.SessionID != "selected" || !out.Context.AncestryKnown || out.Context.Epoch != "epoch" {
				t.Fatalf("healthy translated summary=%+v, %v", out, err)
			}
			if len(wireCalls(calls())) != 1 {
				t.Fatal("summary dispatched more than once")
			}
		})
	}
}

func TestSessionActivitySummaryIssuesQualifiedEnvelopeBound(t *testing.T) {
	for _, count := range []int{1, 200} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			sourceID := strings.Repeat("remote-", 260)
			summary := appwire.SessionActivitySummary{Context: appwire.SessionActivityContext{Ref: "local:root", RootRef: "local:root", SessionID: "root", AncestryKnown: true}}
			for i := range count {
				summary.Issues = append(summary.Issues, appwire.SessionActivityIssue{Ref: fmt.Sprintf("local:child-%d", i), Code: "unavailable"})
			}
			source, _ := newScriptedRemote(t, sourceID, func(method string, _ json.RawMessage) scriptedReply { return scriptedReply{result: summary} })
			result, err := source.ThreadActivityRead(t.Context(), appwire.SessionActivityReadParams{Ref: sourceID + ":root"})
			if count == 200 {
				var wire appwire.WireError
				if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
					t.Fatalf("oversized qualified summary error=%v", err)
				}
				return
			}
			if err != nil || len(result.Issues) != 1 || result.Issues[0].Ref != sourceID+":child-0" {
				t.Fatalf("summary=%+v error=%v", result, err)
			}
			raw, err := json.Marshal(result)
			if err != nil || len(raw) > 256<<10 {
				t.Fatalf("qualified envelope bytes=%d error=%v", len(raw), err)
			}
		})
	}
}
