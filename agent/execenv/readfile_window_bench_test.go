package execenv

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BenchmarkReadFileSmallWindowLargeFile measures read_file asking for 20 lines
// from a large text file, both near the top and near EOF. It exists to size the
// cost the issue describes: the whole file is read and normalized before the
// window is extracted.
func BenchmarkReadFileSmallWindowLargeFile(b *testing.B) {
	dir := b.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	var sb strings.Builder
	for i := 0; sb.Len() < 8<<20; i++ {
		sb.WriteString("line of representative text content here\n")
	}
	path := filepath.Join(dir, "large.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		b.Fatalf("write: %v", err)
	}
	totalLines := strings.Count(sb.String(), "\n")
	one, twenty := 1, 20
	nearEOF := totalLines - 25
	for _, c := range []struct {
		name   string
		offset int
	}{{"top", one}, {"nearEOF", nearEOF}} {
		b.Run(c.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for range b.N {
				if _, err := env.ReadFile(path, &c.offset, &twenty); err != nil {
					b.Fatalf("ReadFile: %v", err)
				}
			}
		})
	}
}
