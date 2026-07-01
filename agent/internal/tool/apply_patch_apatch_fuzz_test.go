package tool

// Fuzz harness (lane token: apatch_) for the V4A apply_patch parser + applier
// in apply_patch.go. Two surfaces:
//
//   - FuzzApatchParseV4APatchLines drives parseV4APatchLines directly over
//     fuzzed patch text. Oracle: the parse is DETERMINISTIC — parsing the same
//     lines twice yields identical ops + identical error text. Plus never-panic.
//
//   - FuzzApatchApplyPatch drives ApplyPatch (parse + the three patchOp.apply
//     methods) over a fuzzed patch AND a fuzzed base file, inside an isolated
//     t.TempDir sandbox. Oracles: never-panic; and a full CONSISTENCY oracle —
//     applying the identical patch to two freshly-built identical sandboxes
//     produces the identical result string AND the identical resulting file
//     tree (ApplyPatch is a pure function of (rootDir contents, patch text), so
//     any divergence is a real nondeterminism bug).
//
// apply_patch.go reaches the filesystem through the os package directly (not an
// afero/execenv seam), so fuzz/fault cannot inject FS faults here; the honest
// sandbox is a real t.TempDir, which fully contains every os.* call because
// safeJoin rejects absolute paths and ".." traversal outside rootDir.

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// apatch_seed is a (patch, base) pair for the apply fuzzer; the parse fuzzer
// uses only the patch field.
type apatch_seed struct {
	patch string
	base  string
}

// apatch_seeds are shared seeds exercising the interesting parser + applier
// branches: add/delete/update ops, move-to, @@ hints, multiple hunks, End of
// File markers, context/add/remove lines, and a spread of malformed inputs that
// hit the error branches.
func apatch_seeds() []apatch_seed {
	const base3 = "line1\nline2\nline3\n"
	return []apatch_seed{
		{"", ""},
		{"*** Begin Patch\n*** End Patch\n", ""},
		{"  *** Begin Patch  \n*** End Patch\n", ""},
		{"not a patch\n", ""},
		{"*** Begin Patch\n", ""}, // missing End Patch
		// Add File
		{"*** Begin Patch\n*** Add File: new.txt\n+hello\n+world\n*** End Patch\n", ""},
		{"*** Begin Patch\n*** Add File: dir/nested.txt\n+a\n*** End Patch\n", ""},
		{"*** Begin Patch\n*** Add File: bad.txt\nnot-a-plus-line\n*** End Patch\n", ""}, // '+' error branch
		{"*** Begin Patch\n*** Add File: empty.txt\n*** End Patch\n", ""},
		// Delete File
		{"*** Begin Patch\n*** Delete File: file.txt\n*** End Patch\n", base3},
		// Update File — successful context+remove+add
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n line1\n-line2\n+CHANGED\n line3\n*** End Patch\n", base3},
		// Update with @@ hint text
		{"*** Begin Patch\n*** Update File: file.txt\n@@ line1\n line1\n+inserted\n line2\n*** End Patch\n", base3},
		// Update with Move to
		{"*** Begin Patch\n*** Update File: file.txt\n*** Move to: moved.txt\n@@\n-line1\n+one\n*** End Patch\n", base3},
		// Update, multiple hunks separated by @@
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n line1\n+x\n@@\n line3\n+y\n*** End Patch\n", base3},
		// Update, pure-add hunk (no old lines)
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n+prepended\n*** End Patch\n", base3},
		// Update targeting a missing file (ReadFile error branch)
		{"*** Begin Patch\n*** Update File: missing.txt\n@@\n-x\n+y\n*** End Patch\n", ""},
		// Update whose old lines don't match (mismatch diagnostic branch)
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n-nope\n+z\n*** End Patch\n", base3},
		// End of File marker
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n line1\n*** End of File\n*** End Patch\n", base3},
		// Path-traversal + absolute-path rejections
		{"*** Begin Patch\n*** Add File: ../escape.txt\n+x\n*** End Patch\n", ""},
		{"*** Begin Patch\n*** Add File: /abs/escape.txt\n+x\n*** End Patch\n", ""},
		{"*** Begin Patch\n*** Add File: \n+x\n*** End Patch\n", ""}, // empty path
		// Unexpected line inside patch body
		{"*** Begin Patch\ngarbage line\n*** End Patch\n", ""},
		// CRLF line endings
		{"*** Begin Patch\r\n*** Add File: crlf.txt\r\n+hi\r\n*** End Patch\r\n", ""},
		// Unicode-punctuation fuzzy match: base has curly quote, patch has straight.
		{"*** Begin Patch\n*** Update File: file.txt\n@@\n-say ‘hi’\n+bye\n*** End Patch\n", "say 'hi'\n"},
	}
}

// FuzzApatchParseV4APatchLines fuzzes the pure line parser. Oracle: determinism
// — parsing identical lines twice must agree exactly (ops + error text).
func FuzzApatchParseV4APatchLines(f *testing.F) {
	for _, s := range apatch_seeds() {
		f.Add(s.patch)
	}
	f.Fuzz(func(t *testing.T, patch string) {
		lines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
		parse := func(in []string) apatch_parseResult {
			ops, err := parseV4APatchLines(in)
			return apatch_newParseResult(ops, err)
		}
		oracle.Deterministic(t, parse, lines, apatch_parseResultEqual)
	})
}

// FuzzApatchApplyPatch fuzzes the full parse+apply pipeline against a fuzzed
// base file. Oracle: applying to two identical fresh sandboxes yields identical
// output (result string, error text, and resulting file tree).
func FuzzApatchApplyPatch(f *testing.F) {
	for _, s := range apatch_seeds() {
		f.Add(s.patch, s.base)
	}
	f.Fuzz(func(t *testing.T, patch, base string) {
		res1, tree1 := apatch_runOnce(t, patch, base)
		res2, tree2 := apatch_runOnce(t, patch, base)

		if res1.out != res2.out || res1.errText != res2.errText {
			t.Fatalf("ApplyPatch nondeterministic:\n  run1 out=%q err=%q\n  run2 out=%q err=%q\n  patch=%q base=%q",
				res1.out, res1.errText, res2.out, res2.errText, patch, base)
		}
		if !apatch_treeEqual(tree1, tree2) {
			t.Fatalf("ApplyPatch produced divergent file trees:\n  run1=%v\n  run2=%v\n  patch=%q base=%q",
				tree1, tree2, patch, base)
		}
	})
}

// apatch_applyResult is the observable output of one ApplyPatch call.
type apatch_applyResult struct {
	out     string
	errText string
}

// apatch_runOnce builds a fresh sandbox seeded with base at "file.txt", runs
// ApplyPatch, and snapshots the resulting tree. Any panic in ApplyPatch aborts
// the fuzz test (the never-panic oracle).
func apatch_runOnce(t *testing.T, patch, base string) (apatch_applyResult, map[string]string) {
	t.Helper()
	root := t.TempDir()
	// Seed a known file so update/delete ops that reference it hit their
	// success branches instead of always failing at ReadFile.
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte(base), 0o644); err != nil {
		t.Fatalf("seed file.txt: %v", err)
	}
	out, err := ApplyPatch(root, patch)
	errText := ""
	if err != nil {
		// Error messages can embed the absolute sandbox path (each run gets a
		// distinct t.TempDir); replace it with a sentinel so the consistency
		// oracle compares the message SHAPE, not the incidental temp path.
		errText = strings.ReplaceAll(err.Error(), root, "<root>")
	}
	return apatch_applyResult{out: out, errText: errText}, apatch_snapshotTree(t, root)
}

// apatch_snapshotTree returns a rel-path -> contents map for every regular file
// under root, so two sandboxes can be compared for structural equality.
func apatch_snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	tree := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		tree[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk sandbox: %v", err)
	}
	return tree
}

func apatch_treeEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if bv, ok := b[k]; !ok || bv != a[k] {
			return false
		}
	}
	return true
}

// apatch_parseResult captures parseV4APatchLines output for equality checks.
// ops carries the parsed patchOps (comparable via reflect through the eq func),
// errText carries the error message.
type apatch_parseResult struct {
	ops     []patchOp
	errText string
}

func apatch_newParseResult(ops []patchOp, err error) apatch_parseResult {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	return apatch_parseResult{ops: ops, errText: errText}
}

func apatch_parseResultEqual(a, b apatch_parseResult) bool {
	return a.errText == b.errText && oracle.DeepEqual(a.ops, b.ops)
}
