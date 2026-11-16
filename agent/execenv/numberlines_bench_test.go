package execenv

import (
	"strings"
	"testing"
)

// BenchmarkNumberLinesSmallWindowLargeText isolates the line-numbering and
// normalization cost from the file read, to show which half of the read_file
// cost the window extraction actually owns.
func BenchmarkNumberLinesSmallWindowLargeText(b *testing.B) {
	var sb strings.Builder
	for i := 0; sb.Len() < 8<<20; i++ {
		sb.WriteString("line of representative text content here\n")
	}
	text := sb.String()
	one, twenty := 1, 20
	b.ResetTimer()
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	for range b.N {
		if out := NumberLines(text, &one, &twenty); out == "" {
			b.Fatal("empty window")
		}
	}
}
