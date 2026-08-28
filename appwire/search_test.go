package appwire

import (
	"encoding/json"
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
		Live: []SearchResult{{ID: "live", Ref: "local:live"}},
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
		if len(result.resp.Past) != 1 || result.resp.Past[0].Ref != "local:past" {
			t.Fatalf("past=%+v", result.resp.Past)
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
