package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"primeradiant.com/evener/agent/execenv"
)

// applyPatchSequential applies a patch by handing each parsed op to its own
// apply, the reference this file's coalescing is measured against: each
// operation loads its file, transforms it, and writes it back on its own
// success.
func applyPatchSequential(ctx context.Context, fm execenv.FileMutator, patch string) (string, error) {
	ops, err := parseV4APatch(patch)
	if err != nil {
		return "", err
	}
	var touched []string
	for _, op := range ops {
		paths, err := op.apply(ctx, fm)
		if err != nil {
			return "", err
		}
		touched = append(touched, paths...)
	}
	if len(touched) == 0 {
		return "no changes", nil
	}
	return "applied patch to:\n" + strings.Join(touched, "\n"), nil
}

// TestApplyPatchCoalescedUpdatesMatchSequentialApply is the oracle for issue
// #6430's coalescing: for every patch, applying it coalesced must produce the
// same output string, the same error text, and the same file tree as applying
// each operation on its own. Failure cases matter most, because a mismatch at a
// later section must leave exactly what the earlier sections wrote.
func TestApplyPatchCoalescedUpdatesMatchSequentialApply(t *testing.T) {
	const base = "alpha\nbeta\ngamma\ndelta\nepsilon\n"
	cases := []struct {
		name  string
		base  string
		patch string
	}{
		{
			name:  "two update sections one file",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-epsilon\n+EPSILON\n*** End Patch\n",
		},
		{
			name:  "three update sections one file",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+BETA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** Update File: f.txt\n@@\n-delta\n+DELTA\n*** End Patch\n",
		},
		{
			name:  "later section matches an earlier section's added line",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+inserted\n*** Update File: f.txt\n@@\n-inserted\n+rewritten\n*** End Patch\n",
		},
		{
			name:  "later section matches a line the earlier one shifted",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n alpha\n+new line\n*** Update File: f.txt\n@@\n-epsilon\n+EPSILON\n*** End Patch\n",
		},
		{
			name:  "later section matches above the earlier section's edit",
			base:  "aaa\nbbb\nccc\nddd\neee\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-ddd\n+DDD\n*** Update File: f.txt\n@@\n-aaa\n+AAA\n*** End Patch\n",
		},
		{
			name:  "sections in reverse file order",
			base:  "aaa\nbbb\nccc\nddd\neee\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-eee\n+EEE\n*** Update File: f.txt\n@@\n-bbb\n+BBB\n*** End Patch\n",
		},
		{
			name:  "later section re-matches a line the earlier one already changed",
			base:  "aaa\nbbb\nccc\nddd\neee\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-ccc\n+CCC\n*** Update File: f.txt\n@@\n-ccc\n+ccc again\n*** End Patch\n",
		},
		{
			name:  "second section mismatches",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-not present\n+X\n*** End Patch\n",
		},
		{
			name:  "third section mismatches",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** Update File: f.txt\n@@\n-not present\n+X\n*** End Patch\n",
		},
		{
			name:  "first section mismatches",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-not present\n+X\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** End Patch\n",
		},
		{
			name:  "run interrupted by another path",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: g.txt\n@@\n+added to g\n*** Update File: f.txt\n@@\n-epsilon\n+EPSILON\n*** End Patch\n",
		},
		{
			name:  "run interrupted by an add of the same path",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Add File: f.txt\n+replaced whole file\n*** Update File: f.txt\n@@\n-replaced whole file\n+and again\n*** End Patch\n",
		},
		{
			name:  "first section carries Move to, next names the old path",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n*** Move to: moved.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-beta\n+BETA\n*** End Patch\n",
		},
		{
			name:  "line ending in a stray carriage return across sections",
			base:  "alpha\r\r\nbeta\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+BETA\n*** Update File: f.txt\n@@\n+tail\n*** End Patch\n",
		},
		{
			name:  "hunk adds a line ending in a carriage return, then another section",
			base:  "alpha\nbeta\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\r\n*** Update File: f.txt\n@@\n-beta\n+BETA\n*** End Patch\n",
		},
		{
			name:  "later section of a moved file",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n*** Move to: moved.txt\n@@\n-epsilon\n+EPSILON\n*** Update File: moved.txt\n@@\n-BETA\n+beta\n*** End Patch\n",
		},
		{
			name:  "no final newline preserved across sections",
			base:  "alpha\nbeta\ngamma",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** End Patch\n",
		},
		{
			name:  "pure add sections",
			base:  base,
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n+prepended\n*** Update File: f.txt\n@@\n+appended tail\n*** End Patch\n",
		},
		{
			name:  "CRLF file normalized once across sections",
			base:  "alpha\r\nbeta\r\ngamma\r\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** End Patch\n",
		},
		{
			name:  "section empties the file, next anchors on its blank line",
			base:  "only\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-only\n*** Update File: f.txt\n@@\n\n+after\n*** End Patch\n",
		},
		{
			name:  "trailing added blank line with no final newline",
			base:  "alpha\nbeta",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** End Patch\n",
		},
		{
			name:  "trailing added blank line then an append",
			base:  "alpha\nbeta",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+\n*** Update File: f.txt\n@@\n alpha\n+appended\n*** End Patch\n",
		},
		{
			name:  "blank context lines across sections",
			base:  "a\n\nb\n\nc\n",
			patch: "*** Begin Patch\n*** Update File: f.txt\n@@\n a\n\n+between\n*** Update File: f.txt\n@@\n b\n\n+after b\n*** End Patch\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := runPatchOnFixture(t, tc.base, tc.patch, applyPatchSequential)
			got := runPatchOnFixture(t, tc.base, tc.patch, ApplyPatch)
			if got.outcome != want.outcome {
				t.Errorf("outcome = %q, want %q", got.outcome, want.outcome)
			}
			if !apatch_treeEqual(got.tree, want.tree) {
				t.Errorf("file tree = %v, want %v", got.tree, want.tree)
			}
		})
	}
}

// FuzzApatchCoalescedMatchesSequential drives the same differential oracle as
// TestApplyPatchCoalescedUpdatesMatchSequentialApply over arbitrary patch text
// and base contents: every patch must coalesce to exactly what applying its
// operations one at a time produces — the same output string, error text, and
// file tree — and must not panic.
func FuzzApatchCoalescedMatchesSequential(f *testing.F) {
	for _, s := range apatch_seeds() {
		f.Add(s.patch, s.base)
	}
	// The shared seeds all name file.txt, which this harness does not create, so
	// they never reach a successful update. These name f.txt and carry
	// consecutive sections on it, the case coalescing exists for.
	for _, s := range []struct{ patch, base string }{
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+BETA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** Update File: f.txt\n@@\n-delta\n+DELTA\n*** End Patch\n", "alpha\nbeta\ngamma\ndelta\n"},
		// A later section matching a line the earlier one added.
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-beta\n+inserted\n*** Update File: f.txt\n@@\n-inserted\n+rewritten\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
		// A later section matching above the earlier one's edit.
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
		// A later section that cannot match, so the earlier ones write back.
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-not present\n+X\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
		// A run ended by a Move to, and a run ended by another path.
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n*** Move to: g.txt\n@@\n-g one\n+G ONE\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
		{"*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: g.txt\n@@\n-g one\n+G ONE\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** End Patch\n", "alpha\nbeta\ngamma\n"},
	} {
		f.Add(s.patch, s.base)
	}
	f.Fuzz(func(t *testing.T, patch, base string) {
		want := runPatchOnFixture(t, base, patch, applyPatchSequential)
		got := runPatchOnFixture(t, base, patch, ApplyPatch)
		if got.outcome != want.outcome {
			t.Fatalf("coalesced apply diverges from sequential apply\n  coalesced:  %q\n  sequential: %q\n  patch=%q base=%q", got.outcome, want.outcome, patch, base)
		}
		if !apatch_treeEqual(got.tree, want.tree) {
			t.Fatalf("coalesced apply produced a different file tree\n  coalesced:  %v\n  sequential: %v\n  patch=%q base=%q", got.tree, want.tree, patch, base)
		}
	})
}

// TestApplyPatchCoalescedWriteBackFailureMatchesSequential pins the error a
// coalesced run reports when it cannot write back the sections that completed:
// applying one section at a time wrote each before the next ran, so the write
// failure came first and must still come first here, not the later section's
// hunk mismatch.
func TestApplyPatchCoalescedWriteBackFailureMatchesSequential(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-not present\n+X\n*** End Patch\n"
	readOnlyMutator := func(t *testing.T) execenv.FileMutator {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return execenv.NewLocalExecutionEnvironment(dir).SetFs(afero.NewReadOnlyFs(afero.NewOsFs()))
	}
	_, wantErr := applyPatchSequential(context.Background(), readOnlyMutator(t), patch)
	_, gotErr := ApplyPatch(context.Background(), readOnlyMutator(t), patch)
	if wantErr == nil || gotErr == nil {
		t.Fatalf("expected both appliers to fail, got sequential=%v coalesced=%v", wantErr, gotErr)
	}
	if gotErr.Error() != wantErr.Error() {
		t.Errorf("error = %q, want %q", gotErr, wantErr)
	}
}

// cancellingMutator cancels its caller's context as the first read returns, so
// a run of sections sees the cancellation before its second section.
type cancellingMutator struct {
	execenv.FileMutator
	cancel context.CancelFunc
	reads  int
}

func (m *cancellingMutator) ReadFileRawCtx(ctx context.Context, path string) ([]byte, error) {
	m.reads++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := m.ReadFileRaw(path)
	if m.reads == 1 {
		m.cancel()
	}
	return b, err
}

// TestApplyPatchCoalescedCancelMatchesSequential pins that a cancellation
// arriving between two sections is observed, not swallowed by the run's single
// read: applied one at a time, the second section's read reports it, so the
// second section's work must not reach the disk here either.
func TestApplyPatchCoalescedCancelMatchesSequential(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: f.txt\n@@\n-alpha\n+ALPHA\n*** Update File: f.txt\n@@\n-gamma\n+GAMMA\n*** End Patch\n"
	run := func(apply func(context.Context, execenv.FileMutator, string) (string, error)) (patchFixtureOutcome, int, error) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("alpha\nbeta\ngamma\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		fm := &cancellingMutator{FileMutator: testMutator(dir), cancel: cancel}
		out, err := apply(ctx, fm, patch)
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		return patchFixtureOutcome{outcome: out + "\x00" + errText, tree: apatch_snapshotTree(t, dir)}, fm.reads, err
	}
	want, wantReads, wantErr := run(applyPatchSequential)
	got, gotReads, gotErr := run(ApplyPatch)
	if got.outcome != want.outcome {
		t.Errorf("outcome = %q, want %q", got.outcome, want.outcome)
	}
	if !apatch_treeEqual(got.tree, want.tree) {
		t.Errorf("file tree = %v, want %v", got.tree, want.tree)
	}
	if wantReads != 2 {
		t.Errorf("sequential reads = %d, want 2", wantReads)
	}
	if !errors.Is(wantErr, context.Canceled) {
		t.Errorf("sequential error = %v, want %v", wantErr, context.Canceled)
	}
	if !errors.Is(gotErr, context.Canceled) {
		t.Errorf("coalesced error = %v, want %v", gotErr, context.Canceled)
	}
	if gotReads != 1 {
		t.Errorf("coalesced reads = %d, want 1", gotReads)
	}
}

// TestPatchFileStateRereadMatchesARead pins reread's invariant: the state a
// coalesced run carries between sections must be the state a section that wrote
// and re-read the file itself would have started from. Each state writes
// itself, is read back the way loadPatchFileState reads, and must agree with
// reread.
func TestPatchFileStateRereadMatchesARead(t *testing.T) {
	// Every state over these lines and both final-newline settings, so the
	// empty-line, CR, and no-final-newline forms are all covered.
	elements := []string{"", "a", "\r", "a\r", "\r\r"}
	var states []patchFileState
	for n := 0; n <= 3; n++ {
		var build func(prefix []string)
		build = func(prefix []string) {
			if len(prefix) == n {
				for _, finalNL := range []bool{false, true} {
					states = append(states, patchFileState{lines: append([]string(nil), prefix...), finalNL: finalNL})
				}
				return
			}
			for _, element := range elements {
				build(append(prefix, element))
			}
		}
		build(nil)
	}
	for _, state := range states {
		text := state.text()
		got := state.reread()
		path := filepath.Join(t.TempDir(), "f.txt")
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		read, err := loadPatchFileState(context.Background(), testMutator(filepath.Dir(path)), "f.txt")
		if err != nil {
			t.Fatalf("load after writing %q: %v", text, err)
		}
		if strings.Join(got.lines, "\x00") != strings.Join(read.lines, "\x00") || got.finalNL != read.finalNL {
			t.Errorf("reread(%q) = %q finalNL=%v, but a read gives %q finalNL=%v",
				text, got.lines, got.finalNL, read.lines, read.finalNL)
		}
	}
}

// patchFixtureOutcome is everything one applier's run is compared on: its
// output string with the error text, and the resulting file tree.
type patchFixtureOutcome struct {
	outcome string
	tree    map[string]string
}

// runPatchOnFixture seeds a fresh confined fixture with base, applies patch with
// apply, and returns the outcome to compare. The mutator is confined because the
// patch text is fuzzed, so an absolute or ".." path is refused instead of
// touching the filesystem outside the fixture. The root is named "workspace"
// because a sandbox denial quotes filepath.Base(root): the two runs compare
// error text, so the basename must not be the borrowed t.TempDir() counter.
func runPatchOnFixture(t *testing.T, base, patch string, apply func(context.Context, execenv.FileMutator, string) (string, error)) patchFixtureOutcome {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "g.txt"), []byte("g one\ng two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := apply(context.Background(), confinedPatchMutator(t, dir), patch)
	errText := ""
	if err != nil {
		// Each run gets its own temp dir, and a diagnostic embeds the absolute
		// path it opened; compare the message SHAPE, as the apatch fuzz
		// harness does, not the incidental fixture path.
		errText = strings.ReplaceAll(err.Error(), dir, "<root>")
	}
	return patchFixtureOutcome{outcome: out + "\x00" + errText, tree: apatch_snapshotTree(t, dir)}
}
