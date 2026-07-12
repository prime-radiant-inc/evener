package modeldisplay

import (
	"testing"
)

func FuzzAbbreviateModel(f *testing.F) {
	for _, id := range []string{"", "openai/gpt-5-20260101", "openai/gpt-5-2026x101", "bare-model"} {
		f.Add(id)
	}
	f.Fuzz(func(t *testing.T, id string) {
		got := AbbreviateModel(id)
		if len(got) > len(id) {
			t.Fatalf("abbreviation grew from %d to %d bytes", len(id), len(got))
		}
	})
}

func FuzzAbbreviatePath(f *testing.F) {
	f.Add("/home/user/a/long/path", 12)
	f.Add("/tmp/file", 0)
	f.Fuzz(func(t *testing.T, path string, width int) {
		if width < -1024 || width > 1024 {
			return
		}
		got := AbbreviatePath(path, width)
		if width > 0 && len(path) > width && len(got) != width+len("…")-1 {
			// Home contraction may make the result shorter than the requested width.
			if len(got) == 0 || got[0] != '~' || len(got) > width {
				t.Fatalf("unexpected abbreviated length: %q (%d), width %d", got, len(got), width)
			}
		}
	})
}
