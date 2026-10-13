package appwire

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestClientSearchRoundTrip(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)

	done := make(chan struct {
		resp SearchResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.Search(ctx, SearchParams{Query: "frobnitz"})
		done <- struct {
			resp SearchResponse
			err  error
		}{resp: resp, err: err}
	}()

	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	if written.Request.Method != MethodEvenerSearch {
		t.Fatalf("method=%q, want %q", written.Request.Method, MethodEvenerSearch)
	}
	var params SearchParams
	if err := json.Unmarshal(written.Request.Params, &params); err != nil {
		t.Fatalf("params decode: %v", err)
	}
	if params.Query != "frobnitz" {
		t.Fatalf("query=%q", params.Query)
	}

	transport.reads <- ResponseMessage(written.Request.ID, SearchResponse{
		Live: []SearchResult{{ID: "live", Ref: "local:live", AskPending: true, ApprovalPending: true}},
		Past: []SearchResult{{ID: "past", Ref: "local:past"}},
	})
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Search: %v", result.err)
		}
		if len(result.resp.Live) != 1 || result.resp.Live[0].Ref != "local:live" {
			t.Fatalf("live=%+v", result.resp.Live)
		}
		// AskPending and ApprovalPending round-trip over the wire like any
		// other field (#2567): both additive, omitempty keys.
		if !result.resp.Live[0].AskPending || !result.resp.Live[0].ApprovalPending {
			t.Fatalf("live=%+v, want both AskPending and ApprovalPending to round-trip true", result.resp.Live[0])
		}
		if len(result.resp.Past) != 1 || result.resp.Past[0].Ref != "local:past" {
			t.Fatalf("past=%+v", result.resp.Past)
		}
		// The past fixture never set either flag: they must decode as their
		// omitempty zero value, not leak true from the live entry above.
		if result.resp.Past[0].AskPending || result.resp.Past[0].ApprovalPending {
			t.Fatalf("past=%+v, want neither flag set", result.resp.Past[0])
		}
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
}

func TestSearchMethodIsHubScoped(t *testing.T) {
	for _, method := range Methods {
		if method.Name != MethodEvenerSearch {
			continue
		}
		if method.Scope != ScopeHub {
			t.Fatalf("search scope=%q, want %q", method.Scope, ScopeHub)
		}
		if _, ok := method.Params.(SearchParams); !ok {
			t.Fatalf("search params=%T, want SearchParams", method.Params)
		}
		if _, ok := method.Result.(SearchResponse); !ok {
			t.Fatalf("search result=%T, want SearchResponse", method.Result)
		}
		return
	}
	t.Fatalf("method catalog missing %q", MethodEvenerSearch)
}

// TopLevel rides on a Past result like any additive omitempty field: present
// when a matching subagent's ancestry resolves, absent otherwise and from an
// older hub, and its own ask/approval flags round-trip like SearchResult's.
func TestSearchTopLevelRoundTrip(t *testing.T) {
	transport := newMemoryTransport()
	client := NewClient(transport)
	ctx := t.Context()
	client.Start(ctx)

	anchor := &SearchTopLevel{
		ID: "root", Ref: "local:root", Title: "Alpha root",
		State: "active", Age: "now", AskPending: true, ApprovalPending: true,
	}
	done := make(chan struct {
		resp SearchResponse
		err  error
	}, 1)
	go func() {
		resp, err := client.Search(ctx, SearchParams{Query: "delegate"})
		done <- struct {
			resp SearchResponse
			err  error
		}{resp: resp, err: err}
	}()
	var written Message
	select {
	case written = <-transport.writes:
	case <-time.After(time.Second):
		t.Fatal("request was not written")
	}
	transport.reads <- ResponseMessage(written.Request.ID, SearchResponse{
		Past: []SearchResult{
			{ID: "sub", Ref: "local:sub", TopLevel: anchor},
			{ID: "plain", Ref: "local:plain"},
		},
	})
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("Search: %v", result.err)
		}
		resp := result.resp
		if len(resp.Past) != 2 {
			t.Fatalf("past=%+v", resp.Past)
		}
		got := resp.Past[0].TopLevel
		if got == nil || got.ID != anchor.ID || got.Ref != anchor.Ref || got.Title != anchor.Title {
			t.Fatalf("topLevel=%+v, want the anchor's identity", got)
		}
		if got.State != "active" || got.Age != "now" || !got.AskPending || !got.ApprovalPending {
			t.Fatalf("topLevel=%+v, want the anchor's live state and flags", got)
		}
		if resp.Past[1].TopLevel != nil {
			t.Fatalf("plain result topLevel=%+v, want nil", resp.Past[1].TopLevel)
		}
	case <-time.After(time.Second):
		t.Fatal("response was not routed")
	}
}

// The generated TypeScript must carry the grouping field as OPTIONAL
// (`topLevel?: SearchTopLevel;`): an older hub omits it, so a required
// property would break every existing client build against the new types.
func TestSearchTopLevelGeneratedOptional(t *testing.T) {
	generated, err := os.ReadFile("../appwire-client/typescript/types.gen.ts")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(generated, []byte("topLevel?: SearchTopLevel;")) {
		t.Fatal("generated SearchResult is missing the optional topLevel field")
	}
	if !bytes.Contains(generated, []byte("export interface SearchTopLevel {")) {
		t.Fatal("generated TypeScript missing the SearchTopLevel interface")
	}
}
