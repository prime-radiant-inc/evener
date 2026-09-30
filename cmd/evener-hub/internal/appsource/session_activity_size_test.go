package appsource

import (
	"encoding/json"
	"errors"
	"fmt"
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
					return scriptedReply{wireErr: ptrActivityWireError(appwire.MethodNotFound(got))}
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
			for pages := 0; pages < total; pages++ {
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
	if !errors.As(err, &wire) || wire.Code != appwire.CodeInternalError {
		t.Fatalf("oversized compact response = %v", err)
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
