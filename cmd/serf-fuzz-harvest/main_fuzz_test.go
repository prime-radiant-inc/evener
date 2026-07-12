package main

import "testing"

func FuzzSanitize(f *testing.F) {
	f.Add([]byte(`{"token":"secret","count":42}`), "json")
	f.Add([]byte("data: {\"id\":\"abc\"}\n\n"), "sse")
	f.Add([]byte("arbitrary bytes"), "raw")
	f.Fuzz(func(t *testing.T, input []byte, surface string) {
		if len(input) > 1<<20 || len(surface) > 64 {
			return
		}
		s := &Sanitizer{}
		_, _ = s.Process(input, surface == "sse")
	})
}
