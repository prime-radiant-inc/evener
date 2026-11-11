//go:build browserguard

package hub

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestBackgroundHistoryTraceMethodAliases(t *testing.T) {
	for _, tc := range []struct {
		name string
		read string
		page string
	}{
		{"legacy", "thread/read", "thread/turns/list"},
		{"versioned", "evener/thread/history/read", "evener/thread/history/turns/list"},
		{"versioned read", "evener/thread/history/read", "thread/turns/list"},
		{"versioned page", "thread/read", "evener/thread/history/turns/list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var trace strings.Builder
			encoder := json.NewEncoder(&trace)
			record := func(connection, direction string, frame map[string]any) {
				t.Helper()
				data, err := json.Marshal(frame)
				if err != nil {
					t.Fatal(err)
				}
				if err := encoder.Encode(map[string]any{
					"event": "frame", "connection": connection, "direction": direction, "frame": string(data),
				}); err != nil {
					t.Fatal(err)
				}
			}
			request := func(connection string, id int, method string, params map[string]any) {
				t.Helper()
				record(connection, "browser_to_hub", map[string]any{"id": id, "method": method, "params": params})
			}
			response := func(connection string, id int, result map[string]any) {
				t.Helper()
				record(connection, "hub_to_browser", map[string]any{"id": id, "result": result})
			}
			request("a", 1, tc.read, map[string]any{"ref": "local:tgt", "includeTurns": true, "subscribe": true})
			request("b", 1, tc.read, map[string]any{"ref": "local:other", "subscribe": true})
			response("b", 1, map[string]any{"olderCursor": "foreign"})
			response("a", 1, map[string]any{"olderCursor": "p1"})
			request("a", 2, tc.page, map[string]any{"ref": "local:tgt", "cursor": "p1"})
			response("a", 2, map[string]any{"nextCursor": "p2"})
			request("a", 3, tc.page, map[string]any{"threadId": "tgt", "cursor": "p2"})
			response("a", 3, map[string]any{"nextCursor": nil})

			t.Run("responses", func(t *testing.T) {
				frames, err := decodeTraceFrames([]byte(trace.String()))
				if err != nil {
					t.Fatal(err)
				}
				got, err := targetTraceEvidenceFor(frames, "local:tgt")
				if err != nil {
					t.Fatal(err)
				}
				want := targetTraceEvidence{Subscribed: true, InitialOlderCursor: "p1", ReadResponses: 1, PageCount: 2}
				if got != want {
					t.Fatalf("target evidence=%+v, want %+v", got, want)
				}
			})
			t.Run("read window", func(t *testing.T) {
				got, err := countTargetThreadReads(trace.String(), "local:tgt")
				if err != nil || got != 1 {
					t.Fatalf("target read requests=%d, err=%v, want 1", got, err)
				}
			})
			t.Run("page window", func(t *testing.T) {
				got, err := countTargetPageRequestsInWindow(trace.String(), "local:tgt")
				if err != nil || got != 2 {
					t.Fatalf("target page requests=%d, err=%v, want 2", got, err)
				}
			})
			t.Run("page cursors", func(t *testing.T) {
				got, err := targetPageRequestCursorsInWindow([]byte(trace.String()), "local:tgt")
				if err != nil || !slices.Equal(got, []string{"p1", "p2"}) {
					t.Fatalf("target cursors=%v, err=%v, want [p1 p2]", got, err)
				}
			})
		})
	}
}
