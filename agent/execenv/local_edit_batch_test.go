package execenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/fuzz/fault"
)

func TestEditFileBatch_DisjointForwardReverseAdjacent(t *testing.T) {
	t.Parallel()

	t.Run("forward order", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("f.txt", "alpha\nbeta\ngamma\n"); err != nil {
			t.Fatal(err)
		}
		var editor BatchFileEditor = env
		res, err := editor.EditFileBatchCtx(context.Background(), "f.txt", []FileEdit{
			{OldString: "alpha", NewString: "ALPHA"},
			{OldString: "gamma", NewString: "GAMMA"},
		})
		if err != nil {
			t.Fatalf("EditFileBatchCtx forward: %v", err)
		}
		if res == "" {
			t.Fatal("expected non-empty summary")
		}
		got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "ALPHA\nbeta\nGAMMA\n" {
			t.Fatalf("got %q, want %q", got, "ALPHA\nbeta\nGAMMA\n")
		}
	})

	t.Run("reverse order", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("f.txt", "first\nsecond\nthird\n"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "f.txt", []FileEdit{
			{OldString: "third", NewString: "THIRD"},
			{OldString: "first", NewString: "FIRST"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx reverse: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "f.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "FIRST\nsecond\nTHIRD\n" {
			t.Fatalf("got %q, want %q", got, "FIRST\nsecond\nTHIRD\n")
		}
	})

	t.Run("adjacent touching spans", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("adj.txt", "aaabbbccc"); err != nil {
			t.Fatal(err)
		}
		// [0,3) and [3,6) touch without overlapping.
		if _, err := env.EditFileBatchCtx(context.Background(), "adj.txt", []FileEdit{
			{OldString: "bbb", NewString: "BBB"},
			{OldString: "aaa", NewString: "AAA"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx adjacent: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "adj.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "AAABBBccc" {
			t.Fatalf("got %q, want %q", got, "AAABBBccc")
		}
	})
}

func TestEditFileBatch_AtomicityOnMissAndEmpty(t *testing.T) {
	t.Parallel()

	t.Run("edit 0 matches edit 1 misses", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "one\ntwo\nthree\n"
		if _, err := env.WriteFile("a.txt", orig); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "a.txt", []FileEdit{
			{OldString: "one", NewString: "ONE"},
			{OldString: "missing", NewString: "MISSING"},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil {
			t.Fatal("expected error when second edit misses")
		}
		if finishCalled {
			t.Fatal("finish hook must not be called on failed batch")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "a.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated on failed batch: got %q, want %q", got, orig)
		}
	})

	t.Run("empty old_string in batch", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "keep me\n"
		if _, err := env.WriteFile("b.txt", orig); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "b.txt", []FileEdit{
			{OldString: "keep", NewString: "KEEP"},
			{OldString: "", NewString: "bad"},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil {
			t.Fatal("expected error on empty OldString")
		}
		if finishCalled {
			t.Fatal("finish hook must not be called on empty OldString")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "b.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated: got %q, want %q", got, orig)
		}
	})

	t.Run("empty edits slice", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "keep me\n"
		if _, err := env.WriteFile("c.txt", orig); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "c.txt", nil, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil {
			t.Fatal("expected error on empty edits slice")
		}
		if finishCalled {
			t.Fatal("finish hook must not be called on empty edits slice")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "c.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated: got %q, want %q", got, orig)
		}
	})
}

func TestEditFileBatch_MultiErrorCollectionAndNearestCap(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	env := NewLocalExecutionEnvironment(dir)
	content := strings.Join([]string{
		"line 1 filler",
		"alpha target here",
		"line 3 filler",
		"beta target here",
		"line 5 filler",
		"gamma target here",
		"line 7 filler",
		"delta target here",
	}, "\n") + "\n"
	if _, err := env.WriteFile("multi.txt", content); err != nil {
		t.Fatal(err)
	}

	_, err := env.EditFileBatchCtx(context.Background(), "multi.txt", []FileEdit{
		{OldString: "alpha target extra", NewString: "A"},
		{OldString: "beta target extra", NewString: "B"},
		{OldString: "gamma target extra", NewString: "C"},
		{OldString: "delta target extra", NewString: "D"},
	})
	if err == nil {
		t.Fatal("expected error when all 4 edits miss")
	}
	errMsg := err.Error()
	for i := range 4 {
		idxToken := fmt.Sprintf("[%d]", i)
		if !strings.Contains(errMsg, idxToken) {
			t.Fatalf("expected error to report edit index %s, got:\n%s", idxToken, errMsg)
		}
	}
	// Nearest-region snippets are capped at 3 and carry 1-based L<n>: prefixes.
	lineMarkerRe := regexp.MustCompile(`(?m)^L\d+:$`)
	markers := lineMarkerRe.FindAllString(errMsg, -1)
	if len(markers) != 3 {
		t.Fatalf("expected 3 nearest-region snippets, got %d (%v) in:\n%s", len(markers), markers, errMsg)
	}
	for _, wantMarker := range []string{"L2:", "L4:", "L6:"} {
		if !strings.Contains(errMsg, wantMarker) {
			t.Fatalf("expected %s in nearest-region snippets, got:\n%s", wantMarker, errMsg)
		}
	}
	if strings.Contains(errMsg, "L8:") {
		t.Fatalf("4th missed edit should not include nearest-region L8:, got:\n%s", errMsg)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "multi.txt"))
	if string(got) != content {
		t.Fatalf("file mutated on multi-miss: got %q", got)
	}
}

func TestEditFileBatch_OverlapRejection(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		content string
		edits   []FileEdit
	}{
		{
			name:    "partial overlap",
			content: "abcdefg\n",
			edits: []FileEdit{
				{OldString: "abcd", NewString: "W"},
				{OldString: "cdef", NewString: "Z"},
			},
		},
		{
			name:    "nested span",
			content: "prefix middle suffix\n",
			edits: []FileEdit{
				{OldString: "prefix middle suffix", NewString: "ALL"},
				{OldString: "middle", NewString: "MID"},
			},
		},
		{
			name:    "identical duplicate edits",
			content: "unique token here\n",
			edits: []FileEdit{
				{OldString: "unique token", NewString: "ONE"},
				{OldString: "unique token", NewString: "TWO"},
			},
		},
		{
			name:    "replace_all overlapping another edit",
			content: "foo bar\nbaz bar\n",
			edits: []FileEdit{
				{OldString: "bar", NewString: "BAR", ReplaceAll: true},
				{OldString: "baz bar", NewString: "BAZ"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			env := NewLocalExecutionEnvironment(dir)
			if _, err := env.WriteFile("ov.txt", tc.content); err != nil {
				t.Fatal(err)
			}
			_, err := env.EditFileBatchCtx(context.Background(), "ov.txt", tc.edits)
			if err == nil {
				t.Fatalf("%s: expected overlap error, got nil", tc.name)
			}
			msg := err.Error()
			if !strings.Contains(msg, "[0]") || !strings.Contains(msg, "[1]") {
				t.Fatalf("%s: expected error to name both edit indices [0] and [1], got:\n%s", tc.name, msg)
			}
			got, _ := os.ReadFile(filepath.Join(dir, "ov.txt"))
			if string(got) != tc.content {
				t.Fatalf("%s: file mutated on overlap error: got %q, want %q", tc.name, got, tc.content)
			}
		})
	}
}

func TestEditFileBatch_SelfOverlappingNeedleAndUTF8(t *testing.T) {
	t.Parallel()

	t.Run("ascii self-overlapping ana in banana", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("u.txt", "banana"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "u.txt", []FileEdit{
			{OldString: "ana", NewString: "X", ReplaceAll: false},
		}); err == nil {
			t.Fatal("expected ambiguous error for self-overlapping 'ana' in 'banana'")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "u.txt"))
		if string(got) != "banana" {
			t.Fatalf("file mutated on ambiguous self-overlap: got %q", got)
		}

		if _, err := env.EditFileBatchCtx(context.Background(), "u.txt", []FileEdit{
			{OldString: "ana", NewString: "X", ReplaceAll: true},
		}); err != nil {
			t.Fatalf("ReplaceAll failed: %v", err)
		}
		got, _ = os.ReadFile(filepath.Join(dir, "u.txt"))
		if string(got) != "bXna" {
			t.Fatalf("got %q, want %q", got, "bXna")
		}
	})

	t.Run("utf8 self-overlapping αβα in αβαβα and βαβα", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)

		// "αβαβα" has two overlapping occurrences of "αβα": [0,6) and [4,10).
		if _, err := env.WriteFile("g1.txt", "αβαβα"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "g1.txt", []FileEdit{
			{OldString: "αβα", NewString: "X", ReplaceAll: false},
		}); err == nil {
			t.Fatal("expected ambiguous error for self-overlapping UTF-8 'αβα' in 'αβαβα'")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "g1.txt"))
		if string(got) != "αβαβα" {
			t.Fatalf("file mutated: got %q", got)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "g1.txt", []FileEdit{
			{OldString: "αβα", NewString: "X", ReplaceAll: true},
		}); err != nil {
			t.Fatalf("ReplaceAll UTF-8 failed: %v", err)
		}
		got, _ = os.ReadFile(filepath.Join(dir, "g1.txt"))
		if string(got) != "Xβα" {
			t.Fatalf("got %q, want %q", got, "Xβα")
		}

		// "βαβα" has exactly one occurrence of "αβα" at [2,8); byte-stepping idx+1 must not misfire.
		if _, err := env.WriteFile("g2.txt", "βαβα"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "g2.txt", []FileEdit{
			{OldString: "αβα", NewString: "X", ReplaceAll: false},
		}); err != nil {
			t.Fatalf("single UTF-8 match in 'βαβα' failed: %v", err)
		}
		got, _ = os.ReadFile(filepath.Join(dir, "g2.txt"))
		if string(got) != "βX" {
			t.Fatalf("got %q, want %q", got, "βX")
		}

		// "βαβαβα" has two overlapping occurrences of "αβα" after "β".
		if _, err := env.WriteFile("g3.txt", "βαβαβα"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "g3.txt", []FileEdit{
			{OldString: "αβα", NewString: "X", ReplaceAll: false},
		}); err == nil {
			t.Fatal("expected ambiguous error for 'αβα' in 'βαβαβα'")
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "g3.txt", []FileEdit{
			{OldString: "αβα", NewString: "X", ReplaceAll: true},
		}); err != nil {
			t.Fatalf("ReplaceAll UTF-8 'βαβαβα' failed: %v", err)
		}
		got, _ = os.ReadFile(filepath.Join(dir, "g3.txt"))
		if string(got) != "βXβα" {
			t.Fatalf("got %q, want %q", got, "βXβα")
		}
	})
}

func TestEditFileBatch_FuzzySubstringGuardAndAmbiguityLines(t *testing.T) {
	t.Parallel()

	t.Run("fuzzy substring guard", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "prefix  alpha   beta\n  alpha   beta\n"
		if _, err := env.WriteFile("sub.txt", orig); err != nil {
			t.Fatal(err)
		}
		_, err := env.EditFileBatchCtx(context.Background(), "sub.txt", []FileEdit{
			{OldString: "alpha beta", NewString: "REPLACED", ReplaceAll: false},
		})
		if err == nil {
			t.Fatal("expected ambiguity error when fuzzy match appears as substring on line 1")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "sub.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated: got %q, want %q", got, orig)
		}
	})

	t.Run("two whitespace-variant lines report both line numbers", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "header\n  foo   bar\nmiddle\n\tfoo\tbar\nfooter\n"
		if _, err := env.WriteFile("ambig.txt", orig); err != nil {
			t.Fatal(err)
		}
		_, err := env.EditFileBatchCtx(context.Background(), "ambig.txt", []FileEdit{
			{OldString: "foo bar", NewString: "X", ReplaceAll: false},
		})
		if err == nil {
			t.Fatal("expected ambiguity error for two fuzzy matches")
		}
		msg := err.Error()
		if !strings.Contains(msg, "2") || !strings.Contains(msg, "4") {
			t.Fatalf("expected 1-based line numbers 2 and 4 in error, got:\n%s", msg)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "ambig.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated: got %q", got)
		}
	})

	t.Run("whitespace-only fuzzy miss leaves file unchanged", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "one\n\nthree\n"
		if _, err := env.WriteFile("ws.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ws.txt", []FileEdit{
			{OldString: "\t  ", NewString: "X", ReplaceAll: false},
		}); err == nil {
			t.Fatal("expected miss for whitespace-only OldString")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "ws.txt"))
		if string(got) != orig {
			t.Fatalf("file mutated: got %q", got)
		}
	})

	t.Run("fuzzy ReplaceAll updates all whitespace-variant lines by offset", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "prefix  foo   bar\n  foo   bar\n\tfoo\tbar\n"
		if _, err := env.WriteFile("allfuzzy.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "allfuzzy.txt", []FileEdit{
			{OldString: "foo bar", NewString: "REPLACED", ReplaceAll: true},
		}); err != nil {
			t.Fatalf("fuzzy ReplaceAll failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "allfuzzy.txt"))
		want := "prefix  foo   bar\nREPLACED\nREPLACED\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

func TestEditFileBatch_ExactNoOpAndCancellingEdits(t *testing.T) {
	t.Parallel()

	t.Run("single exact no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "hello world\n"
		if _, err := env.WriteFile("noop.txt", orig); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "noop.txt", []FileEdit{
			{OldString: "world", NewString: "world"},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil {
			t.Fatal("expected no-op error")
		}
		if finishCalled {
			t.Fatal("finish must not be called on no-op")
		}
	})

	t.Run("adjacent cancelling edits", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "abc"
		if _, err := env.WriteFile("cancel.txt", orig); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "cancel.txt", []FileEdit{
			{OldString: "a", NewString: "ab"},
			{OldString: "bc", NewString: "c"},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil {
			t.Fatal("expected error for cancelling edits producing identical bytes")
		}
		if finishCalled {
			t.Fatal("finish must not be called on cancelling no-op")
		}
		got, _ := os.ReadFile(filepath.Join(dir, "cancel.txt"))
		if string(got) != orig {
			t.Fatalf("got %q, want %q", got, orig)
		}
	})

	t.Run("fuzzy whitespace cleanup succeeds", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("clean.txt", "  foo   bar  \n"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "clean.txt", []FileEdit{
			{OldString: "foo bar", NewString: "foo bar"},
		}); err != nil {
			t.Fatalf("fuzzy whitespace cleanup failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "clean.txt"))
		if string(got) != "foo bar\n" {
			t.Fatalf("got %q, want %q", got, "foo bar\n")
		}
	})
}

func TestEditFileBatch_CRLFMixedEndingsAndBOM(t *testing.T) {
	t.Parallel()

	t.Run("pure CRLF with UTF-8 BOM preserves BOM and CRLF", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "\xEF\xBB\xBFfirst\r\nsecond\r\nthird\r\n"
		if err := os.WriteFile(filepath.Join(dir, "bom.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "bom.txt", []FileEdit{
			{OldString: "first", NewString: "FIRST\nLINE2"},
			{OldString: "third", NewString: "THIRD"},
		}); err != nil {
			t.Fatalf("BOM+CRLF edit failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "bom.txt"))
		if err != nil {
			t.Fatal(err)
		}
		want := "\xEF\xBB\xBFFIRST\r\nLINE2\r\nsecond\r\nTHIRD\r\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("mixed endings preserves untouched LF and CRLF verbatim", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "a\r\nb\nc\r\n"
		if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "mixed.txt", []FileEdit{
			{OldString: "a\r\n", NewString: "A1\r\nA2\r\n"},
		}); err != nil {
			t.Fatalf("mixed ending edit failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "mixed.txt"))
		want := "A1\r\nA2\r\nb\nc\r\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("CRLF in OldString and NewString on LF file preserves LF", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "one\ntwo\nthree\n"
		if err := os.WriteFile(filepath.Join(dir, "lf.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "lf.txt", []FileEdit{
			{OldString: "one\r\ntwo\r\n", NewString: "ONE\r\nTWO\r\n"},
		}); err != nil {
			t.Fatalf("CRLF needle on LF file failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "lf.txt"))
		want := "ONE\nTWO\nthree\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("mixed endings preserves untouched LF segment when editing LF slice", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "a\r\nb\nc\r\n"
		if err := os.WriteFile(filepath.Join(dir, "mixed2.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "mixed2.txt", []FileEdit{
			{OldString: "b\n", NewString: "B1\r\nB2\r\n"},
		}); err != nil {
			t.Fatalf("mixed2 edit failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "mixed2.txt"))
		want := "a\r\nB1\nB2\nc\r\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// Not parallel: mutates package-level MaxEditFileSpans and MaxEditFileOutputBytes.
func TestEditFileBatch_ResourceBounds(t *testing.T) {
	origSpans := MaxEditFileSpans
	origOut := MaxEditFileOutputBytes
	t.Cleanup(func() {
		MaxEditFileSpans = origSpans
		MaxEditFileOutputBytes = origOut
	})

	t.Run("span limit across all 4 match paths", func(t *testing.T) {
		MaxEditFileSpans = 3
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)

		// Exact limit (3 spans) succeeds.
		if _, err := env.WriteFile("ok.txt", "e1\ne2\n  f  3  \n"); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ok.txt", []FileEdit{
			{OldString: "e1", NewString: "E1"},
			{OldString: "e2", NewString: "E2"},
			{OldString: "f 3", NewString: "F3"},
		}); err != nil {
			t.Fatalf("exact span limit should succeed: %v", err)
		}

		// +1 via unique exact fails.
		origExact := "e1\ne2\ne3\ne4\n"
		if _, err := env.WriteFile("exact1.txt", origExact); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "exact1.txt", []FileEdit{
			{OldString: "e1", NewString: "E1"},
			{OldString: "e2", NewString: "E2"},
			{OldString: "e3", NewString: "E3"},
			{OldString: "e4", NewString: "E4"},
		}); err == nil {
			t.Fatal("expected span-limit error on 4th unique exact match")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "exact1.txt")); string(got) != origExact {
			t.Fatalf("file mutated on unique exact span-limit refusal: %q", got)
		}

		// +1 via exact replace_all fails.
		origAll := "x\nx\nx\nx\n"
		if _, err := env.WriteFile("exactall.txt", origAll); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "exactall.txt", []FileEdit{
			{OldString: "x", NewString: "X", ReplaceAll: true},
		}); err == nil {
			t.Fatal("expected span-limit error on exact replace_all")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "exactall.txt")); string(got) != origAll {
			t.Fatalf("file mutated on exact replace_all span-limit refusal: %q", got)
		}

		// +1 via unique fuzzy fails.
		origUFuzzy := "e1\ne2\ne3\n  f  4  \n"
		if _, err := env.WriteFile("ufuzzy.txt", origUFuzzy); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ufuzzy.txt", []FileEdit{
			{OldString: "e1", NewString: "E1"},
			{OldString: "e2", NewString: "E2"},
			{OldString: "e3", NewString: "E3"},
			{OldString: "f 4", NewString: "F4"},
		}); err == nil {
			t.Fatal("expected span-limit error on 4th unique fuzzy match")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "ufuzzy.txt")); string(got) != origUFuzzy {
			t.Fatalf("file mutated on unique fuzzy span-limit refusal: %q", got)
		}

		// +1 via fuzzy replace_all fails.
		origFFuzzy := "  a  b\n\ta\tb\na   b\n  a\tb\n"
		if _, err := env.WriteFile("ffuzzy.txt", origFFuzzy); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ffuzzy.txt", []FileEdit{
			{OldString: "a b", NewString: "X", ReplaceAll: true},
		}); err == nil {
			t.Fatal("expected span-limit error on fuzzy replace_all")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "ffuzzy.txt")); string(got) != origFFuzzy {
			t.Fatalf("file mutated on fuzzy replace_all span-limit refusal: %q", got)
		}
	})

	t.Run("CRLF replace_all exceeding output byte cap fails before writing", func(t *testing.T) {
		MaxEditFileSpans = 100
		MaxEditFileOutputBytes = 20
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "x\r\nx\r\nx\r\n"
		if err := os.WriteFile(filepath.Join(dir, "cap.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		// Replacing "x" with "12\n34" on a CRLF file expands each to "12\r\n34" (6 bytes):
		// 3*6 + 6 (\r\n * 3) = 24 bytes > 20. (Without CRLF expansion it would be 3*5 + 6 = 21.)
		// Use "1\n23" -> LF=4 (3*4+6=18 <= 20), CRLF=5 (3*5+6=21 > 20) to prove CRLF scalar length is checked!
		finishCalled := false
		if _, err := env.EditFileBatchWithCtx(context.Background(), "cap.txt", []FileEdit{
			{OldString: "x", NewString: "1\n23", ReplaceAll: true},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		}); err == nil {
			t.Fatal("expected output byte cap error")
		}
		if finishCalled {
			t.Fatal("finish must not be called when output cap is exceeded")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "cap.txt")); string(got) != orig {
			t.Fatalf("file mutated on output-cap refusal: %q", got)
		}
	})
}

func TestEditFileBatch_UnconfinedAtomicWriteAndPermissions(t *testing.T) {
	t.Parallel()

	t.Run("fault-injected write error leaves original intact and cleans temp", func(t *testing.T) {
		t.Parallel()
		seed := map[string]string{"e.txt": "alpha\nbeta\n"}
		// Probe which step index hits inside writeFileAtomic after read succeeds.
		var sawWriteFault bool
		for k := 3; k < 15; k++ {
			base := afero.NewMemMapFs()
			root := "/root"
			if err := base.MkdirAll(root, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := afero.WriteFile(base, filepath.Join(root, "e.txt"), []byte(seed["e.txt"]), 0o644); err != nil {
				t.Fatal(err)
			}
			env := NewLocalExecutionEnvironment(root).SetFs(fault.FS(base, faultOnly(k)))
			_, err := env.EditFileBatchCtx(context.Background(), "e.txt", []FileEdit{
				{OldString: "alpha", NewString: "ALPHA"},
				{OldString: "beta", NewString: "BETA"},
			})
			if err != nil {
				sawWriteFault = true
				got, _ := afero.ReadFile(base, filepath.Join(root, "e.txt"))
				if string(got) != seed["e.txt"] {
					t.Fatalf("fault k=%d mutated original bytes: %q", k, got)
				}
				entries, derr := afero.ReadDir(base, root)
				if derr != nil {
					t.Fatal(derr)
				}
				if len(entries) != 1 || entries[0].Name() != "e.txt" {
					var names []string
					for _, e := range entries {
						names = append(names, e.Name())
					}
					t.Fatalf("fault k=%d left leftover temp files: %v", k, names)
				}
			}
		}
		if !sawWriteFault {
			t.Fatal("expected at least one fault schedule index to hit writeFileAtomic")
		}
	})

	t.Run("read-only target 0444 in writable dir is refused", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("POSIX non-root chmod permissions required")
		}
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		p := filepath.Join(dir, "ro.txt")
		orig := "before\n"
		if err := os.WriteFile(p, []byte(orig), 0o444); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ro.txt", []FileEdit{
			{OldString: "before", NewString: "after"},
		}); err == nil {
			t.Fatal("expected write refusal on 0444 target file")
		}
		got, _ := os.ReadFile(p)
		if string(got) != orig {
			t.Fatalf("0444 file mutated: %q", got)
		}
	})

	t.Run("read-only parent dir refuses without modifying target", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" || os.Geteuid() == 0 {
			t.Skip("POSIX non-root chmod permissions required")
		}
		dir := t.TempDir()
		sub := filepath.Join(dir, "locked")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(sub, "t.txt")
		orig := "keep\n"
		if err := os.WriteFile(p, []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(sub, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })

		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.EditFileBatchCtx(context.Background(), "locked/t.txt", []FileEdit{
			{OldString: "keep", NewString: "changed"},
		}); err == nil {
			t.Fatal("expected error when parent directory is read-only")
		}
		got, _ := os.ReadFile(p)
		if string(got) != orig {
			t.Fatalf("target modified despite read-only parent: %q", got)
		}
	})

	t.Run("symlink on OsFs updates target keeping symlink intact and preserves mode", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("symlinks require privileges on Windows")
		}
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		if err := os.MkdirAll(realDir, 0o755); err != nil {
			t.Fatal(err)
		}
		realPath := filepath.Join(realDir, "target.sh")
		if err := os.WriteFile(realPath, []byte("echo old1\necho old2\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		linkPath := filepath.Join(dir, "link.sh")
		if err := os.Symlink(realPath, linkPath); err != nil {
			t.Fatal(err)
		}
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.EditFileBatchCtx(context.Background(), "link.sh", []FileEdit{
			{OldString: "old1", NewString: "new1"},
			{OldString: "old2", NewString: "new2"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx through symlink failed: %v", err)
		}
		info, err := os.Lstat(linkPath)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatal("expected link.sh to remain a symlink")
		}
		got, err := os.ReadFile(realPath)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "echo new1\necho new2\n" {
			t.Fatalf("target content = %q", got)
		}
		tinfo, err := os.Stat(realPath)
		if err != nil {
			t.Fatal(err)
		}
		if tinfo.Mode().Perm() != 0o755 {
			t.Fatalf("target mode = %o, want 0755", tinfo.Mode().Perm())
		}
	})

	t.Run("MemMapFs and BasePathFs stay in injected namespace", func(t *testing.T) {
		t.Parallel()
		// Use a logical path that cannot exist on the host OS root.
		logicalRoot := "/nonexistent-evener-namespace-root"
		for name, fs := range map[string]afero.Fs{
			"MemMapFs":   afero.NewMemMapFs(),
			"BasePathFs": afero.NewBasePathFs(afero.NewOsFs(), t.TempDir()),
		} {
			if err := fs.MkdirAll(logicalRoot, 0o755); err != nil {
				t.Fatalf("%s MkdirAll: %v", name, err)
			}
			target := filepath.Join(logicalRoot, "file.txt")
			if err := afero.WriteFile(fs, target, []byte("a\nb\n"), 0o600); err != nil {
				t.Fatalf("%s WriteFile: %v", name, err)
			}
			env := NewLocalExecutionEnvironment(logicalRoot).SetFs(fs)
			if _, err := env.EditFileBatchCtx(context.Background(), "file.txt", []FileEdit{
				{OldString: "a", NewString: "A"},
				{OldString: "b", NewString: "B"},
			}); err != nil {
				t.Fatalf("%s EditFileBatchCtx: %v", name, err)
			}
			got, err := afero.ReadFile(fs, target)
			if err != nil {
				t.Fatalf("%s ReadFile: %v", name, err)
			}
			if string(got) != "A\nB\n" {
				t.Fatalf("%s got %q, want %q", name, got, "A\nB\n")
			}
		}
	})
}

func TestEditFileBatch_ConfinedAtomicityAndSymlinkRefusal(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("confined fd sandbox requires Unix")
	}

	root := t.TempDir()
	env, err := NewConfinedFileEnvironment(root, "scope")
	if err != nil {
		t.Fatal(err)
	}
	defer env.Cleanup()

	scopeDir := env.WorkingDirectory()
	if _, err := env.WriteFile("ok.txt", "one\ntwo\nthree\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(scopeDir, "ok.txt"), 0o640); err != nil {
		t.Fatal(err)
	}

	// Multi-edit succeeds atomically and preserves regular file permissions.
	if _, err := env.EditFileBatchCtx(context.Background(), "ok.txt", []FileEdit{
		{OldString: "three", NewString: "THREE"},
		{OldString: "one", NewString: "ONE"},
	}); err != nil {
		t.Fatalf("confined EditFileBatchCtx failed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(scopeDir, "ok.txt"))
	if string(got) != "ONE\ntwo\nTHREE\n" {
		t.Fatalf("got %q", got)
	}
	if info, serr := os.Stat(filepath.Join(scopeDir, "ok.txt")); serr != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("confined edit mode = %v (err=%v), want 0640", info.Mode().Perm(), serr)
	}

	// Confined env refuses symlink targets.
	realOutside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(realOutside, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(scopeDir, "escape.txt")
	if err := os.Symlink(realOutside, linkPath); err != nil {
		t.Fatal(err)
	}
	_, err = env.EditFileBatchCtx(context.Background(), "escape.txt", []FileEdit{
		{OldString: "secret", NewString: "pwned"},
	})
	if _, denied := errors.AsType[*sandbox.DeniedError](err); !denied {
		t.Fatalf("expected sandbox.DeniedError on symlink in confined env, got %v", err)
	}
	if b, _ := os.ReadFile(realOutside); string(b) != "secret\n" {
		t.Fatalf("symlink target mutated: %q", b)
	}

	// Confined env denies read-only / masked / outside-root paths before matching (even for invalid edits).
	outsideTarget := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outsideTarget, []byte("outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = env.EditFileBatchCtx(context.Background(), outsideTarget, []FileEdit{
		{OldString: "", NewString: "bad"},
	})
	if _, denied := errors.AsType[*sandbox.DeniedError](err); !denied {
		t.Fatalf("expected sandbox.DeniedError before input/match check on outside path, got %v", err)
	}
}
