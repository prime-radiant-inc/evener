package main

import (
	"strings"
	"testing"
)

// FuzzDecodeModuleGraph drives the decoder over arbitrary `go list -m -json
// all` streams. The oracle is floor "no panic" plus determinism: a stream that
// decodes to a graph must diff cleanly against itself.
func FuzzDecodeModuleGraph(f *testing.F) {
	f.Add(`{"Path":"github.com/a/b","Version":"v1.0.0"}`)
	f.Add(`{"Path":"example.com/main","Main":true}` + "\n" + `{"Path":"github.com/a/b","Version":"v1.2.0","Replace":{"Path":"example.com/fork","Version":"v1.2.1"}}`)
	f.Add(`{"Path":"github.com/a/b"` + "\n" + `{"Path":`)
	f.Add("")

	f.Fuzz(func(t *testing.T, stream string) {
		modules, err := decodeModules(strings.NewReader(stream))
		if err != nil {
			return
		}
		if changes := diffModules(modules, modules); len(changes) != 0 {
			t.Fatalf("decoded graph %+v differs from itself: %+v", modules, changes)
		}
	})
}
