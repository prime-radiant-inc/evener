package main

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

// FuzzParseRegistry drives ParseRegistry over arbitrary registry text. The
// oracle is floor "no panic" plus, for cleanly-parsing inputs: every target
// carries the four non-empty identity fields, and parsing is deterministic
// (same bytes → deeply-equal targets on a second pass).
func FuzzParseRegistry(f *testing.F) {
	f.Add([]byte("native:llm:./providers/anthropic:FuzzAnthropicComplete::response.go#fromAnthropicResponse\n"))
	f.Add([]byte("native:.:./cmd/serf-hub:FuzzWebHandler::web.go\n"))
	f.Add([]byte("rapid:agent:.:TestTurnLifecycle\n"))
	f.Add([]byte("native:agent:.:FuzzTurn::turn.go#Feed;turn.go\n"))
	f.Add([]byte("# not a comment format the parser knows\n"))
	f.Add([]byte("native:llm:./x:Fuzz:cover:focus:with:extra:colons\n"))
	f.Add([]byte("too:few:fields\n"))
	f.Add([]byte(""))
	f.Add([]byte("\n\n  \n"))
	f.Add([]byte(strings.Repeat("a", 2048) + ":m:./p:FuzzX\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		targets, err := ParseRegistry(bytes.NewReader(data))
		if err != nil {
			return
		}
		for _, target := range targets {
			if target.Kind == "" || target.Module == "" || target.Package == "" || target.Name == "" {
				t.Fatalf("clean parse yielded empty identity field: %+v", target)
			}
		}
		again, err := ParseRegistry(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("second parse of identical input errored: %v", err)
		}
		if !reflect.DeepEqual(targets, again) {
			t.Fatalf("parse is non-deterministic:\nfirst:  %+v\nsecond: %+v", targets, again)
		}
	})
}
