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

	t.Run("editing one line in a mixed file keeps untouched line endings", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "a\r\nb\nc\r\n"
		if err := os.WriteFile(filepath.Join(dir, "mixed.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "mixed.txt", []FileEdit{
			{OldString: "a", NewString: "A"},
		}); err != nil {
			t.Fatalf("mixed ending edit failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "mixed.txt"))
		want := "A\r\nb\nc\r\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("CRLF-first mixed endings use CRLF for replacements only", func(t *testing.T) {
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

	t.Run("LF-first mixed endings use LF for replacements only", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "a\nb\r\nc\n"
		if err := os.WriteFile(filepath.Join(dir, "mixed2.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "mixed2.txt", []FileEdit{
			{OldString: "b", NewString: "B1\nB2"},
		}); err != nil {
			t.Fatalf("mixed2 edit failed: %v", err)
		}
		got, _ := os.ReadFile(filepath.Join(dir, "mixed2.txt"))
		want := "a\nB1\nB2\r\nc\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("read_file line 1 with leading UTF-8 BOM round-trips cleanly", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "\xEF\xBB\xBFfirst\r\nsecond\r\n"
		if err := os.WriteFile(filepath.Join(dir, "bom_read.txt"), []byte(orig), 0o644); err != nil {
			t.Fatal(err)
		}
		readOut, err := env.ReadFile("bom_read.txt", nil, nil)
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		// Extract line 1 payload after the "1\t" prefix just as a model does.
		firstLine, _, _ := strings.Cut(readOut, "\n")
		_, line1Body, ok := strings.Cut(firstLine, "\t")
		if !ok || !strings.HasPrefix(line1Body, "\xEF\xBB\xBF") {
			t.Fatalf("expected ReadFile line 1 to carry leading BOM, got %q", firstLine)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "bom_read.txt", []FileEdit{
			{OldString: line1Body, NewString: "\xEF\xBB\xBFFIRST"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx with BOM copied from ReadFile failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "bom_read.txt"))
		if err != nil {
			t.Fatal(err)
		}
		want := "\xEF\xBB\xBFFIRST\r\nsecond\r\n"
		if string(got) != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		// Bare BOM alone must never be stripped into an empty match.
		if _, err := env.EditFileBatchCtx(context.Background(), "bom_read.txt", []FileEdit{
			{OldString: "\xEF\xBB\xBF", NewString: "X"},
		}); err == nil {
			t.Fatal("expected bare BOM OldString to fail rather than match empty text")
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

	t.Run("mixed CRLF/LF restored length decides the cap before writing", func(t *testing.T) {
		MaxEditFileSpans = 100
		MaxEditFileOutputBytes = 20
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)

		// The LF-normalized output is 15 bytes, but restoring each line's own
		// terminator (two of the three are CRLF) makes it exactly 20, so the
		// edit fits the cap.
		origAtCap := "x\r\nx\nx\r\n"
		if err := os.WriteFile(filepath.Join(dir, "atcap.txt"), []byte(origAtCap), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "atcap.txt", []FileEdit{
			{OldString: "x", NewString: "1\n23", ReplaceAll: true},
		}); err != nil {
			t.Fatalf("edit at the restored-length cap failed: %v", err)
		}
		wantAtCap := "1\r\n23\r\n1\r\n23\n1\r\n23\r\n"
		if got, _ := os.ReadFile(filepath.Join(dir, "atcap.txt")); string(got) != wantAtCap {
			t.Fatalf("at-cap output = %q, want %q", got, wantAtCap)
		}

		// A third CRLF terminator pushes the restored output to 21 bytes, one
		// over the cap, though the LF-normalized output is still 15 bytes. The
		// refusal must name the restored length and happen before writing.
		origOver := "x\r\nx\r\nx\r\n"
		if err := os.WriteFile(filepath.Join(dir, "over.txt"), []byte(origOver), 0o644); err != nil {
			t.Fatal(err)
		}
		finishCalled := false
		_, err := env.EditFileBatchWithCtx(context.Background(), "over.txt", []FileEdit{
			{OldString: "x", NewString: "1\n23", ReplaceAll: true},
		}, func(b []byte) []byte {
			finishCalled = true
			return b
		})
		if err == nil || !strings.Contains(err.Error(), "would be 21 bytes") {
			t.Fatalf("expected a 21-byte restored-length cap error, got %v", err)
		}
		if finishCalled {
			t.Fatal("finish must not be called when the restored-length cap is exceeded")
		}
		if got, _ := os.ReadFile(filepath.Join(dir, "over.txt")); string(got) != origOver {
			t.Fatalf("over-cap file mutated: %q", got)
		}
	})
}

func TestEditFileBatch_UnconfinedAtomicWriteAndPermissions(t *testing.T) {
	t.Parallel()

	t.Run("unconfined edit writes in place preserving inode identity and mode", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		p := filepath.Join(dir, "tagged.txt")
		if err := os.WriteFile(p, []byte("alpha\nbeta\n"), 0o750); err != nil {
			t.Fatal(err)
		}
		beforeInfo, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "tagged.txt", []FileEdit{
			{OldString: "alpha", NewString: "ALPHA"},
			{OldString: "beta", NewString: "BETA"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx: %v", err)
		}
		afterInfo, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(beforeInfo, afterInfo) {
			t.Fatal("unconfined edit replaced the file inode instead of writing in place")
		}
		if afterInfo.Mode().Perm() != beforeInfo.Mode().Perm() {
			t.Fatalf("mode changed: got %o, want %o", afterInfo.Mode().Perm(), beforeInfo.Mode().Perm())
		}
		got, _ := os.ReadFile(p)
		if string(got) != "ALPHA\nBETA\n" {
			t.Fatalf("got %q, want %q", got, "ALPHA\nBETA\n")
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

func TestEditFile_BlankLineTolerantUniqueFallback(t *testing.T) {
	t.Parallel()

	t.Run("missing blank line replaces exact unique span", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "function a() {\n  return 1\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		if _, err := env.WriteFile("rank.js", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("rank.js", "}\nfunction boundaryRank(x) {", "}\n\nfunction boundaryRank(x, y) {", false); err != nil {
			t.Fatalf("EditFile missing blank line failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "rank.js"))
		if err != nil {
			t.Fatal(err)
		}
		want := "function a() {\n  return 1\n}\n\nfunction boundaryRank(x, y) {\n  return x\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("extra blank line in old_string replaces exact unique span", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "function a() {\n  return 1\n}\nfunction boundaryRank(x) {\n  return x\n}\n"
		if _, err := env.WriteFile("rank.js", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("rank.js", "}\n\nfunction boundaryRank(x) {", "}\nfunction boundaryRank(x, y) {", false); err != nil {
			t.Fatalf("EditFile extra blank line failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "rank.js"))
		if err != nil {
			t.Fatal(err)
		}
		want := "function a() {\n  return 1\n}\nfunction boundaryRank(x, y) {\n  return x\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("missing blank line combined with tabs vs spaces", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "func a() {\n\tif ok {\n\t\treturn\n\t}\n\n\tfmt.Println(1)\n}\n"
		if _, err := env.WriteFile("a.go", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("a.go", "  }\n  fmt.Println(1)", "\t}\n\n\tfmt.Println(2)", false); err != nil {
			t.Fatalf("EditFile tabs+blank fallback failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "a.go"))
		if err != nil {
			t.Fatal(err)
		}
		want := "func a() {\n\tif ok {\n\t\treturn\n\t}\n\n\tfmt.Println(2)\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}
	})

	t.Run("ambiguous duplicate fails and leaves file untouched", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "}\n\nfunction dup() {\n  return 1\n}\n\nfunction dup() {\n  return 2\n}\n"
		if _, err := env.WriteFile("dup.js", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("dup.js", "}\nfunction dup() {", "}\nfunction renamed() {", false); err == nil {
			t.Fatal("expected ambiguity error for duplicate blank-line match")
		}
		got, err := os.ReadFile(filepath.Join(dir, "dup.js"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated on ambiguous fallback:\n%q", string(got))
		}
	})

	t.Run("replace_all does not use blank-line fallback", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		if _, err := env.WriteFile("all.js", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("all.js", "}\nfunction boundaryRank(x) {", "}\nfunction renamed(x) {", true); err == nil {
			t.Fatal("expected replace_all with missing blank line to fail")
		}
		got, err := os.ReadFile(filepath.Join(dir, "all.js"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated when replace_all refused:\n%q", string(got))
		}
	})

	t.Run("leading or trailing blank line in old_string does not corrupt boundaries", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "top\n}\n\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		if _, err := env.WriteFile("edge.js", orig); err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{
			"\n}\nfunction boundaryRank(x) {",
			"}\nfunction boundaryRank(x) {\n",
		} {
			if _, err := env.EditFile("edge.js", old, "REPLACED", false); err == nil {
				t.Fatalf("expected failure for boundary-blank old_string %q", old)
			}
			got, err := os.ReadFile(filepath.Join(dir, "edge.js"))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != orig {
				t.Fatalf("file mutated for old_string %q:\n%q", old, string(got))
			}
		}
	})

	t.Run("batch edits apply atomically and abort on ambiguous fallback", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "const v = 1\n}\n\nfunction boundaryRank(x) {\n  return x\n}\n"
		if _, err := env.WriteFile("batch_ok.js", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "batch_ok.js", []FileEdit{
			{OldString: "const v = 1", NewString: "const v = 2"},
			{OldString: "}\nfunction boundaryRank(x) {", NewString: "}\n\nfunction boundaryRank(x, y) {"},
		}); err != nil {
			t.Fatalf("atomic batch with blank-line fallback failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "batch_ok.js"))
		if err != nil {
			t.Fatal(err)
		}
		want := "const v = 2\n}\n\nfunction boundaryRank(x, y) {\n  return x\n}\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}

		dupOrig := "const v = 1\n}\n\nfunction dup() {\n  return 1\n}\n\nfunction dup() {\n  return 2\n}\n"
		if _, err := env.WriteFile("batch_ambig.js", dupOrig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "batch_ambig.js", []FileEdit{
			{OldString: "const v = 1", NewString: "const v = 2"},
			{OldString: "}\nfunction dup() {", NewString: "}\nfunction renamed() {"},
		}); err == nil {
			t.Fatal("expected atomic batch abort when second edit is ambiguous")
		}
		got, err = os.ReadFile(filepath.Join(dir, "batch_ambig.js"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != dupOrig {
			t.Fatalf("batch partially mutated file on ambiguous fallback:\n%q", string(got))
		}
	})

	t.Run("preserves unchanged blank lines in shared context", func(t *testing.T) {
		t.Parallel()
		cases := []struct {
			name string
			orig string
			old  string
			new  string
			want string
		}{
			{
				name: "unchanged leading paragraph break",
				orig: "Overview\n\nDetails\nVersion 1\n",
				old:  "Overview\nDetails\nVersion 1",
				new:  "Overview\nDetails\nVersion 2",
				want: "Overview\n\nDetails\nVersion 2\n",
			},
			{
				name: "boundary blank gap before changed line",
				orig: "Overview\n\nVersion 1\n",
				old:  "Overview\nVersion 1",
				new:  "Overview\nVersion 2",
				want: "Overview\n\nVersion 2\n",
			},
			{
				name: "explicit blank-line edit is honored",
				orig: "Overview\n\nVersion 1\n",
				old:  "Overview\nVersion 1",
				new:  "Overview\n\n\nVersion 2",
				want: "Overview\n\n\nVersion 2\n",
			},
			{
				name: "shared prefix and suffix preserve internal and boundary blanks",
				orig: "a\n\nold\n\nb\n\nc\n",
				old:  "a\nold\nb\nc",
				new:  "a\nnew\nb\nc",
				want: "a\n\nnew\n\nb\n\nc\n",
			},
			{
				name: "pure deletion keeps single paragraph break",
				orig: "a\n\ndrop\n\nb\n",
				old:  "a\ndrop\nb",
				new:  "a\nb",
				want: "a\n\nb\n",
			},
			{
				name: "pure insertion preserves blank break once",
				orig: "a\n\nb\n",
				old:  "a\nb",
				new:  "a\nmid\nb",
				want: "a\nmid\n\nb\n",
			},
			{
				name: "pure append and prepend preserve internal blank line",
				orig: "a\n\nb\n",
				old:  "a\nb",
				new:  "a\nb\nc",
				want: "a\n\nb\nc\n",
			},
			{
				name: "pure prepend preserves internal blank line",
				orig: "a\n\nb\n",
				old:  "a\nb",
				new:  "z\na\nb",
				want: "z\na\n\nb\n",
			},
			{
				name: "CRLF file preserves CRLF endings and blank break",
				orig: "Overview\r\n\r\nDetails\r\nVersion 1\r\n",
				old:  "Overview\nDetails\nVersion 1",
				new:  "Overview\nDetails\nVersion 2",
				want: "Overview\r\n\r\nDetails\r\nVersion 2\r\n",
			},
			{
				name: "tab-indented unchanged context keeps real indentation and blank line",
				orig: "\tOverview\n\n\tDetails\n\tVersion 1\n",
				old:  "  Overview\n  Details\n  Version 1",
				new:  "  Overview\n  Details\n\tVersion 2",
				want: "\tOverview\n\n\tDetails\n\tVersion 2\n",
			},
			{
				name: "changed first and last lines preserve interior paragraph breaks",
				orig: "Status 1\n\nP1\n\nP2\n\nOwner 1\n",
				old:  "Status 1\nP1\nP2\nOwner 1",
				new:  "Status 2\nP1\nP2\nOwner 2",
				want: "Status 2\n\nP1\n\nP2\n\nOwner 2\n",
			},
			{
				name: "explicit blank-line reduction before changed line is honored",
				orig: "Heading\n\nOld text\n\nFooter\n",
				old:  "Heading\n\nOld text\nFooter",
				new:  "Heading\nNew text\nFooter",
				want: "Heading\nNew text\n\nFooter\n",
			},
			{
				name: "explicit blank-line removal between unchanged lines is honored",
				orig: "Heading\n\nBody\n\nFooter\n",
				old:  "Heading\n\nBody\nFooter",
				new:  "Heading\nBody\nFooter",
				want: "Heading\nBody\n\nFooter\n",
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				dir := t.TempDir()
				env := NewLocalExecutionEnvironment(dir)
				if _, err := env.WriteFile("doc.txt", tc.orig); err != nil {
					t.Fatal(err)
				}
				if _, err := env.EditFile("doc.txt", tc.old, tc.new, false); err != nil {
					t.Fatalf("EditFile failed: %v", err)
				}
				got, err := os.ReadFile(filepath.Join(dir, "doc.txt"))
				if err != nil {
					t.Fatal(err)
				}
				if string(got) != tc.want {
					t.Fatalf("got:\n%q\nwant:\n%q", string(got), tc.want)
				}
			})
		}
	})

	t.Run("identical old and new on blank-tolerant match fails as no-op", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "Overview\n\nVersion 1\n"
		if _, err := env.WriteFile("noop.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("noop.txt", "Overview\nVersion 1", "Overview\nVersion 1", false); err == nil {
			t.Fatal("expected no-op error when old_string equals new_string")
		}
		got, err := os.ReadFile(filepath.Join(dir, "noop.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated on no-op:\n%q", string(got))
		}
	})

	t.Run("batch preserves blanks and keeps full matched span for overlap detection", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		orig := "Header\nOverview\n\nDetails\nVersion 1\n"
		if _, err := env.WriteFile("ok.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ok.txt", []FileEdit{
			{OldString: "Header", NewString: "Title"},
			{OldString: "Overview\nDetails\nVersion 1", NewString: "Overview\nDetails\nVersion 2"},
		}); err != nil {
			t.Fatalf("EditFileBatchCtx failed: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "ok.txt"))
		if err != nil {
			t.Fatal(err)
		}
		want := "Title\nOverview\n\nDetails\nVersion 2\n"
		if string(got) != want {
			t.Fatalf("got:\n%q\nwant:\n%q", string(got), want)
		}

		// Second edit touches "Overview", which sits inside the full blank-tolerant
		// matched span even though it is unchanged context: overlap must still reject.
		if _, err := env.WriteFile("ov.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "ov.txt", []FileEdit{
			{OldString: "Overview", NewString: "Summary"},
			{OldString: "Overview\nDetails\nVersion 1", NewString: "Overview\nDetails\nVersion 2"},
		}); err == nil {
			t.Fatal("expected overlap rejection across full blank-tolerant matched span")
		}
		got, err = os.ReadFile(filepath.Join(dir, "ov.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatalf("file mutated on overlapping batch:\n%q", string(got))
		}
	})

	t.Run("oversized unaligned interior refuses blank-tolerant fallback while localized large edit succeeds", func(t *testing.T) {
		t.Parallel()
		var midFile, midOld []string
		for i := range 127 {
			line := fmt.Sprintf("interior line %d", i)
			midFile = append(midFile, line)
			midOld = append(midOld, line)
			if i == 60 {
				midFile = append(midFile, "")
			}
		}
		// 129 non-blank lines with both endpoints changed (om=129, nm=129 > 16384)
		// and an omitted interior blank line: must refuse cleanly rather than drop the blank line.
		orig := "Header\nStart 1\n" + strings.Join(midFile, "\n") + "\nEnd 1\n"
		oldStr := "Start 1\n" + strings.Join(midOld, "\n") + "\nEnd 1"
		newStr := "Start 2\n" + strings.Join(midOld, "\n") + "\nEnd 2"

		dir := t.TempDir()
		env := NewLocalExecutionEnvironment(dir)
		if _, err := env.WriteFile("single.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFile("single.txt", oldStr, newStr, false); err == nil {
			t.Fatal("expected blank-tolerant fallback refusal when unaligned interior exceeds alignment cap")
		}
		got, err := os.ReadFile(filepath.Join(dir, "single.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatal("single EditFile mutated file after refusing oversized blank-tolerant edit")
		}

		if _, err := env.WriteFile("batch.txt", orig); err != nil {
			t.Fatal(err)
		}
		if _, err := env.EditFileBatchCtx(context.Background(), "batch.txt", []FileEdit{
			{OldString: "Header", NewString: "Title"},
			{OldString: oldStr, NewString: newStr},
		}); err == nil {
			t.Fatal("expected atomic batch refusal when blank-tolerant edit exceeds alignment cap")
		}
		got, err = os.ReadFile(filepath.Join(dir, "batch.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != orig {
			t.Fatal("batch mutated file after refusing oversized blank-tolerant edit")
		}

		// Same 130+ line block with only one interior line changed: prefix/suffix
		// trimming shrinks om*nm to 1 <= 16384, so it still succeeds and keeps the blank line.
		localNew := append([]string(nil), midOld...)
		localNew[20] = "interior line 20 updated"
		localNewStr := "Start 1\n" + strings.Join(localNew, "\n") + "\nEnd 1"
		wantLocal := append([]string(nil), midFile...)
		wantLocal[20] = "interior line 20 updated"
		want := "Header\nStart 1\n" + strings.Join(wantLocal, "\n") + "\nEnd 1\n"
		if _, err := env.EditFile("single.txt", oldStr, localNewStr, false); err != nil {
			t.Fatalf("localized edit in 130-line block failed: %v", err)
		}
		got, err = os.ReadFile(filepath.Join(dir, "single.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatal("localized large-block edit did not preserve interior blank line")
		}
	})
}
