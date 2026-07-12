package fuzzroutes

import (
	"strings"
	"testing"
)

func FuzzReadOnly(f *testing.F) {
	for i := range ReadOnly {
		f.Add(uint8(i))
	}
	f.Fuzz(func(t *testing.T, index uint8) {
		route := ReadOnly[int(index)%len(ReadOnly)]
		if !strings.HasPrefix(route, "/") {
			t.Fatalf("route %q is not absolute", route)
		}
	})
}
