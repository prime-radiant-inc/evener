package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// benchPatchFileLines is the size of the file the patch benchmarks rewrite: a
// generated source file large enough that one whole-file read, split, join and
// write per update operation dominates the hunk work.
const benchPatchFileLines = 50_000

// benchPatchFile writes a generated source file of lines numbered lines and
// returns its path and bytes.
func benchPatchFile(b *testing.B, dir string, lines int) (string, []byte) {
	b.Helper()
	var sb strings.Builder
	sb.Grow(lines * 48)
	for i := range lines {
		fmt.Fprintf(&sb, "line %06d: return value%06d from the generated body\n", i, i)
	}
	content := []byte(sb.String())
	path := filepath.Join(dir, "large.go")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		b.Fatalf("seed %s: %v", path, err)
	}
	return path, content
}

// benchHunkAt returns a one-line replacement hunk for line i of the benchmark
// file, at the spacing the file was generated with.
func benchHunkAt(i int) string {
	return fmt.Sprintf("-line %06d: return value%06d from the generated body\n"+
		"+line %06d: replaced value%06d in the generated body\n", i, i, i, i)
}

// benchManyUpdateOpsPatch builds a patch with one *** Update File: section per
// line in at, each a single small hunk. This is the shape issue #6430 measures:
// N operations on one file, each of which used to rebuild the whole file.
func benchManyUpdateOpsPatch(at []int) string {
	var sb strings.Builder
	sb.WriteString("*** Begin Patch\n")
	for _, i := range at {
		sb.WriteString("*** Update File: large.go\n@@\n")
		sb.WriteString(benchHunkAt(i))
	}
	sb.WriteString("*** End Patch\n")
	return sb.String()
}

// benchManyHunksOneOpPatch builds a patch with one *** Update File: section
// holding one hunk per line in at, which must be ascending.
func benchManyHunksOneOpPatch(at []int) string {
	var sb strings.Builder
	sb.WriteString("*** Begin Patch\n*** Update File: large.go\n")
	for _, i := range at {
		sb.WriteString("@@\n")
		sb.WriteString(benchHunkAt(i))
	}
	sb.WriteString("*** End Patch\n")
	return sb.String()
}

// benchPatchTargets are the lines the patch benchmarks replace, spread through
// the file so no hunk lands next to another.
func benchPatchTargets(n int) []int {
	at := make([]int, 0, n)
	step := benchPatchFileLines / (n + 1)
	for i := 1; i <= n; i++ {
		at = append(at, i*step)
	}
	return at
}

// benchmarkPatchRestoringFile runs apply over a file restored before every
// iteration. The restore is outside the timed section because no caller of
// apply_patch pays it: it only puts the benchmark's input back.
func benchmarkPatchRestoringFile(b *testing.B, patch string) {
	b.Helper()
	dir := b.TempDir()
	path, content := benchPatchFile(b, dir, benchPatchFileLines)
	fm := testMutator(dir)
	b.ReportAllocs()
	for b.Loop() {
		b.StopTimer()
		if err := os.WriteFile(path, content, 0o644); err != nil {
			b.Fatalf("restore %s: %v", path, err)
		}
		b.StartTimer()
		if _, err := ApplyPatch(fm, patch); err != nil {
			b.Fatalf("ApplyPatch: %v", err)
		}
	}
}

// BenchmarkApplyPatchOneHunkLargeFile measures a single small hunk in a large
// file: one operation's unavoidable whole-file read and write.
func BenchmarkApplyPatchOneHunkLargeFile(b *testing.B) {
	benchmarkPatchRestoringFile(b, benchManyUpdateOpsPatch(benchPatchTargets(1)))
}

// BenchmarkApplyPatchManyUpdateOpsLargeFile measures eight *** Update File:
// sections on the same large file. Before issue #6430's fix each section read,
// split, joined and wrote the whole file again.
func BenchmarkApplyPatchManyUpdateOpsLargeFile(b *testing.B) {
	benchmarkPatchRestoringFile(b, benchManyUpdateOpsPatch(benchPatchTargets(8)))
}

// BenchmarkApplyPatchManyHunksOneOpLargeFile measures one *** Update File:
// section holding eight hunks, which already shared one read and one write; it
// isolates the per-hunk search cost the fix does not change.
func BenchmarkApplyPatchManyHunksOneOpLargeFile(b *testing.B) {
	benchmarkPatchRestoringFile(b, benchManyHunksOneOpPatch(benchPatchTargets(8)))
}
