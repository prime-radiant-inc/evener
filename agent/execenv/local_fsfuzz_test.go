package execenv

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"primeradiant.com/serf/fuzz/oracle"
)

// This file fuzzes the two non-subprocess filesystem helpers in local.go that
// read the real OS directly rather than the injectable afero.Fs seam:
//
//   - ListDirectory      — sorted, depth-limited recursive directory listing.
//   - injectLocalVenvPath — prepends a project's local virtualenv bin dir(s)
//                           onto PATH, probing the disk for their existence.
//
// Both are exercised over a per-run t.TempDir sandbox (never real disk outside
// the sandbox, never a subprocess). All identifiers are prefixed exfs_ to avoid
// colliding with the other filesystem fuzzers in this package (local_fuzz_test.go).

// ---------------------------------------------------------------------------
// ListDirectory
// ---------------------------------------------------------------------------

// exfs_FuzzListDirectory builds an arbitrary directory tree under a t.TempDir
// from the fuzzed spec, then drives ListDirectory over it. Oracles, checked
// after every call:
//
//   - never panic (the fuzz engine catches any panic as a failure);
//   - DETERMINISM: two listings of the same tree at the same depth are equal;
//   - GROUND TRUTH per entry: every returned entry resolves to a real on-disk
//     path whose Lstat metadata (IsDir / IsSymlink / IsExec / Size) matches the
//     reported fields, no entry is reported twice, and no entry sits deeper than
//     the requested depth;
//   - COMPLETENESS: the SET of returned names equals the set an independent
//     filepath.WalkDir traversal (a different traversal engine, so not a mirror
//     of the os.ReadDir recursion under test) collects under the same depth
//     bound — nothing dropped, nothing invented;
//   - ORDERING: the returned order equals a preorder DFS with lexicographically
//     sorted siblings, derived independently by sorting the returned names on
//     their path-component key.
//
// It also confirms the top-level error path: listing a path that does not exist
// returns an error and a nil slice.
//
// SAFETY: a fresh t.TempDir per run; no disk outside it, no subprocess.
func FuzzExfsListDirectory(f *testing.F) {
	f.Add("a.txt\nb/c.txt\nd/\n", 2, false, false)
	f.Add("z.sh\na/b/c/deep.txt\n", 3, true, false)
	f.Add("one\ntwo\nthree\n", 1, false, true)
	f.Add("dir/\ndir/nested/\ndir/nested/leaf", 5, true, true)
	f.Add("", 1, false, false)
	f.Add("../escape\n/abs/path\n./rel", 2, false, false)
	f.Add("same\nsame\nsame/child", 4, false, false)

	f.Fuzz(func(t *testing.T, spec string, depth int, mkExec, mkSym bool) {
		// Bound the spec so a pathological tree can't turn a correct O(entries)
		// listing into a multi-second exec; the logic is fully exercised by
		// small trees.
		if len(spec) > 4096 {
			return
		}
		if depth > 64 {
			depth = 64
		}
		if depth < -8 {
			depth = -8
		}

		root := t.TempDir()
		exfs_buildTree(t, root, spec, mkExec, mkSym)

		env := NewLocalExecutionEnvironment(root)

		// DETERMINISM (and the never-panic / never-error-on-a-real-dir guard).
		list := func(struct{}) []DirEntry {
			out, err := env.ListDirectory(".", depth)
			if err != nil {
				t.Fatalf("ListDirectory(%q, %d) on an existing root errored: %v", ".", depth, err)
			}
			return out
		}
		oracle.Deterministic(t, list, struct{}{}, exfs_dirEntriesEqual)

		out, _ := env.ListDirectory(".", depth)

		effDepth := depth
		if effDepth <= 0 {
			effDepth = 1
		}

		// GROUND TRUTH per entry + no duplicates + depth bound.
		seen := map[string]bool{}
		for _, e := range out {
			if seen[e.Name] {
				t.Fatalf("ListDirectory returned duplicate entry %q", e.Name)
			}
			seen[e.Name] = true

			comps := strings.Count(e.Name, string(filepath.Separator)) + 1
			if comps > effDepth {
				t.Fatalf("entry %q is %d levels deep, exceeds depth %d", e.Name, comps, effDepth)
			}

			abs := filepath.Join(root, filepath.FromSlash(e.Name))
			info, err := os.Lstat(abs)
			if err != nil {
				t.Fatalf("returned entry %q does not exist on disk: %v", e.Name, err)
			}
			isSym := info.Mode()&os.ModeSymlink != 0
			if e.IsSymlink != isSym {
				t.Fatalf("entry %q IsSymlink=%v but disk says %v", e.Name, e.IsSymlink, isSym)
			}
			if e.IsDir != info.IsDir() {
				t.Fatalf("entry %q IsDir=%v but disk says %v", e.Name, e.IsDir, info.IsDir())
			}
			if e.IsDir {
				continue
			}
			// ListDirectory populates Size and IsExec from the same Lstat-based
			// FileInfo that ent.Info() returns for non-directory entries.
			if e.Size != info.Size() {
				t.Fatalf("entry %q Size=%d but disk says %d", e.Name, e.Size, info.Size())
			}
			wantExec := info.Mode()&0o111 != 0
			if e.IsExec != wantExec {
				t.Fatalf("entry %q IsExec=%v but disk mode %v says %v", e.Name, e.IsExec, info.Mode(), wantExec)
			}
		}

		// COMPLETENESS: the returned name set equals an independent walk's set.
		want := exfs_walkNames(root, effDepth)
		if len(want) != len(seen) {
			t.Fatalf("entry count mismatch: ListDirectory=%d independent-walk=%d\n got =%v\n want=%v",
				len(seen), len(want), exfs_sortedKeys(seen), exfs_sortedKeys(want))
		}
		for name := range want {
			if !seen[name] {
				t.Fatalf("ListDirectory dropped entry %q present in independent walk", name)
			}
		}

		// ORDERING: returned order is a preorder DFS with sorted siblings.
		names := make([]string, len(out))
		for i, e := range out {
			names[i] = e.Name
		}
		wantOrder := append([]string(nil), names...)
		sort.SliceStable(wantOrder, func(i, j int) bool { return exfs_dfsLess(wantOrder[i], wantOrder[j]) })
		for i := range names {
			if names[i] != wantOrder[i] {
				t.Fatalf("ordering not preorder-DFS-sorted at %d\n got =%v\n want=%v", i, names, wantOrder)
			}
		}

		// Top-level error path: a nonexistent directory must error with a nil slice.
		absent := "exfs_no_such_dir_xyzzy/leaf"
		if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(absent))); err != nil {
			got, err := env.ListDirectory(absent, effDepth)
			if err == nil {
				t.Fatalf("ListDirectory(%q) on a missing path returned no error", absent)
			}
			if got != nil {
				t.Fatalf("ListDirectory(%q) on a missing path returned entries: %v", absent, got)
			}
		}
	})
}

// exfs_buildTree materializes the fuzzed spec as files and directories under
// root. Each non-empty line is one relative path; a trailing '/' makes it a
// directory, otherwise it is a file whose contents (hence size) are its own
// path. Paths that are absolute or escape root are skipped, so nothing lands
// outside the sandbox. When mkExec is set the first regular file is made
// executable; when mkSym is set a symlink is added — this is how the IsExec /
// IsSymlink branches of ListDirectory get exercised. Build failures (e.g. a
// file path whose parent is already a file) are ignored: every oracle reads the
// resulting state back from disk, so the on-disk truth is what matters.
func exfs_buildTree(t *testing.T, root, spec string, mkExec, mkSym bool) {
	t.Helper()
	var firstFile string
	count := 0
	for _, ln := range strings.Split(spec, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		isDir := strings.HasSuffix(ln, "/")
		rel := filepath.Clean(strings.Trim(ln, "/"))
		if rel == "." || rel == ".." || filepath.IsAbs(rel) || exfs_escapes(rel) {
			continue
		}
		count++
		if count > 64 {
			break
		}
		abs := filepath.Join(root, rel)
		if isDir {
			_ = os.MkdirAll(abs, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			continue
		}
		// Don't clobber an existing directory with a file.
		if fi, err := os.Lstat(abs); err == nil && fi.IsDir() {
			continue
		}
		if err := os.WriteFile(abs, []byte(rel), 0o644); err != nil {
			continue
		}
		if firstFile == "" {
			firstFile = abs
		}
	}
	if mkExec && firstFile != "" {
		_ = os.Chmod(firstFile, 0o755)
	}
	if mkSym {
		link := filepath.Join(root, "exfs_link")
		if _, err := os.Lstat(link); err != nil {
			target := firstFile
			if target == "" {
				target = root
			}
			_ = os.Symlink(target, link)
		}
	}
}

// exfs_escapes reports whether a cleaned relative path steps outside its root
// via a leading "..".
func exfs_escapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// exfs_walkNames independently collects the set of entry names ListDirectory
// should return for root at the given (already >=1) depth, using filepath.WalkDir
// rather than the os.ReadDir recursion under test. Like ListDirectory, WalkDir
// does not descend into symlinked directories, so the two agree on which entries
// exist within the depth bound.
func exfs_walkNames(root string, depth int) map[string]bool {
	res := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // mirror ListDirectory's best-effort tolerance of unreadable dirs
		}
		if p == root {
			return nil
		}
		// p is always root or a descendant of it, so trimming the root prefix
		// yields the same relative path filepath.Rel would, without the error arm.
		rel := strings.TrimPrefix(p, root+string(filepath.Separator))
		comps := strings.Count(rel, string(filepath.Separator)) + 1
		if comps > depth {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		res[rel] = true
		return nil
	})
	return res
}

// exfs_dfsLess orders two slash-or-separator paths by their components: a parent
// sorts before its descendants, and siblings sort lexicographically. This is the
// preorder-DFS-with-sorted-siblings order ListDirectory emits.
func exfs_dfsLess(a, b string) bool {
	ca := strings.Split(a, string(filepath.Separator))
	cb := strings.Split(b, string(filepath.Separator))
	for i := 0; i < len(ca) && i < len(cb); i++ {
		if ca[i] != cb[i] {
			return ca[i] < cb[i]
		}
	}
	return len(ca) < len(cb)
}

// exfs_dirEntriesEqual compares two DirEntry slices field-for-field in order.
func exfs_dirEntriesEqual(a, b []DirEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// exfs_sortedKeys returns the keys of a set, sorted, for stable failure output.
func exfs_sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// injectLocalVenvPath
// ---------------------------------------------------------------------------

// exfs_FuzzInjectLocalVenvPath drives injectLocalVenvPath over a fuzzed
// environment slice and a fixed pair of roots under a t.TempDir, with the
// presence of each candidate ".venv"/"venv" bin directory selected by venvBits
// so the disk-probe branches are all reachable. Oracles:
//
//   - never panic;
//   - DETERMINISM: two calls on identical inputs return equal slices (the
//     function's map usage must not leak iteration order into the result);
//   - IDEMPOTENCE: injecting into an already-injected environment is a no-op,
//     because every prepended dir is now already on PATH and gets filtered out;
//   - GROUND TRUTH: every directory the function prepends onto PATH is a real
//     on-disk "<root>/{.venv,venv}/{bin,Scripts}" directory (it never invents a
//     path), the prepended dirs are distinct, and the original PATH value is
//     preserved verbatim as a suffix;
//   - IDENTITY: when no candidate venv bin dir exists, the environment is
//     returned unchanged.
//
// SAFETY: a fresh t.TempDir per run; no disk outside it, no subprocess.
func FuzzExfsInjectLocalVenvPath(f *testing.F) {
	f.Add("PATH=/usr/bin:/bin\nHOME=/home/x", uint8(0))
	f.Add("PATH=/usr/bin", uint8(0b0001))
	f.Add("FOO=bar", uint8(0b1010))
	f.Add("", uint8(0b1111))
	f.Add("PATH=", uint8(0b0101))
	f.Add("PATH=/a:/b:/c\nX=1\nY=2", uint8(0b1111))
	f.Add("PATH=/a:: /b", uint8(0b1111)) // empty + whitespace-only PATH segments

	f.Fuzz(func(t *testing.T, envSpec string, venvBits uint8) {
		if len(envSpec) > 4096 {
			return
		}
		root := t.TempDir()
		r0 := filepath.Join(root, "root0")
		r1 := filepath.Join(root, "root1")
		if err := os.MkdirAll(r0, 0o755); err != nil {
			t.Fatalf("mkdir r0: %v", err)
		}
		if err := os.MkdirAll(r1, 0o755); err != nil {
			t.Fatalf("mkdir r1: %v", err)
		}

		binDir := "bin"
		if runtime.GOOS == "windows" {
			binDir = "Scripts"
		}
		mk := func(cond bool, base, kind string) {
			if cond {
				_ = os.MkdirAll(filepath.Join(base, kind, binDir), 0o755)
			}
		}
		mk(venvBits&1 != 0, r0, ".venv")
		mk(venvBits&2 != 0, r0, "venv")
		mk(venvBits&4 != 0, r1, ".venv")
		mk(venvBits&8 != 0, r1, "venv")

		env := exfs_parseEnv(envSpec)
		// Roots deliberately carry whitespace, a duplicate, and an empty entry
		// to exercise the trim/dedup/skip logic.
		roots := []string{" " + r0 + " ", r0, r1, "", r1}

		det := func(struct{}) []string {
			return injectLocalVenvPath(exfs_cloneStrs(env), append([]string(nil), roots...))
		}
		oracle.Deterministic(t, det, struct{}{}, exfs_strsEqual)

		got := injectLocalVenvPath(exfs_cloneStrs(env), append([]string(nil), roots...))

		// IDEMPOTENCE.
		got2 := injectLocalVenvPath(exfs_cloneStrs(got), append([]string(nil), roots...))
		if !exfs_strsEqual(got, got2) {
			t.Fatalf("injectLocalVenvPath not idempotent\n once =%v\n twice=%v", got, got2)
		}

		exfs_checkInject(t, env, got, r0, r1, binDir)
	})
}

// exfs_checkInject verifies the ground-truth PATH invariants of an
// injectLocalVenvPath result against the on-disk state.
func exfs_checkInject(t *testing.T, env, got []string, r0, r1, binDir string) {
	t.Helper()

	// The candidate venv bin dirs that actually exist on disk, in the order the
	// function considers them: root order (r0 before r1), .venv before venv.
	var diskVenv []string
	for _, root := range []string{r0, r1} {
		for _, kind := range []string{".venv", "venv"} {
			cand := filepath.Join(root, kind, binDir)
			if info, err := os.Stat(cand); err == nil && info.IsDir() {
				diskVenv = append(diskVenv, cand)
			}
		}
	}
	diskVenvSet := map[string]bool{}
	for _, d := range diskVenv {
		diskVenvSet[d] = true
	}

	origIdx, origPath, hadPath := exfs_findPath(env)

	// IDENTITY: with no candidate venv dirs (or an empty env), nothing changes.
	if len(env) == 0 || len(diskVenv) == 0 {
		if !exfs_strsEqual(got, env) {
			t.Fatalf("no venv dirs / empty env but result changed\n env=%v\n got=%v", env, got)
		}
		return
	}

	// Non-PATH entries must be preserved identically and in order.
	gotIdx, gotPath, gotHasPath := exfs_findPath(got)
	if !gotHasPath {
		t.Fatalf("result has no PATH entry\n env=%v\n got=%v", env, got)
	}
	if err := exfs_sameExceptPath(env, got); err != nil {
		t.Fatalf("non-PATH entries altered: %v\n env=%v\n got=%v", err, env, got)
	}
	if hadPath && gotIdx != origIdx {
		t.Fatalf("PATH entry moved from index %d to %d", origIdx, gotIdx)
	}

	// The original PATH value must survive verbatim as a suffix, and the dirs
	// prepended ahead of it must all be real venv bin dirs the disk agrees on.
	var prepended []string
	if hadPath && origPath != "" {
		if !strings.HasSuffix(gotPath, origPath) {
			t.Fatalf("original PATH not preserved as suffix\n orig=%q\n got =%q", origPath, gotPath)
		}
		pre := strings.TrimSuffix(gotPath, origPath)
		pre = strings.TrimSuffix(pre, string(os.PathListSeparator))
		if pre != "" {
			prepended = strings.Split(pre, string(os.PathListSeparator))
		}
	} else if gotPath != "" {
		// No PATH entry, or an empty one: the whole value is the prepend.
		prepended = strings.Split(gotPath, string(os.PathListSeparator))
	}

	seen := map[string]bool{}
	for _, d := range prepended {
		if !diskVenvSet[d] {
			t.Fatalf("prepended PATH segment %q is not a real venv bin dir on disk\n disk=%v", d, diskVenv)
		}
		if seen[d] {
			t.Fatalf("prepended PATH segment %q appears more than once", d)
		}
		seen[d] = true
	}

	// If a candidate venv dir exists that was NOT already on the original PATH,
	// the function must have prepended at least one dir.
	origSet := exfs_pathSegSet(origPath)
	newFound := false
	for _, d := range diskVenv {
		if !origSet[d] {
			newFound = true
			break
		}
	}
	if newFound && len(prepended) == 0 {
		t.Fatalf("venv dir exists off-PATH but nothing was prepended\n disk=%v\n orig=%q", diskVenv, origPath)
	}
}

// exfs_parseEnv turns a newline-separated spec into an environment slice, one
// entry per non-empty line, verbatim (entries need not contain '=').
func exfs_parseEnv(spec string) []string {
	var out []string
	for _, ln := range strings.Split(spec, "\n") {
		if ln == "" {
			continue
		}
		out = append(out, ln)
	}
	return out
}

// exfs_findPath returns the index and value of the PATH= entry in env, and
// whether one was present.
func exfs_findPath(env []string) (int, string, bool) {
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			return i, strings.TrimPrefix(kv, "PATH="), true
		}
	}
	return -1, "", false
}

// exfs_sameExceptPath checks that env and got have identical entries in identical
// order apart from PATH= entries, whose values may differ.
func exfs_sameExceptPath(env, got []string) error {
	strip := func(s []string) []string {
		out := make([]string, 0, len(s))
		for _, kv := range s {
			if strings.HasPrefix(kv, "PATH=") {
				continue
			}
			out = append(out, kv)
		}
		return out
	}
	a, b := strip(env), strip(got)
	if !exfs_strsEqual(a, b) {
		return fmt.Errorf("non-PATH entries differ: %v vs %v", a, b)
	}
	return nil
}

// exfs_pathSegSet returns the set of non-empty, trimmed segments of a PATH value.
func exfs_pathSegSet(path string) map[string]bool {
	set := map[string]bool{}
	if path == "" {
		return set
	}
	for _, p := range strings.Split(path, string(os.PathListSeparator)) {
		p = strings.TrimSpace(p)
		if p != "" {
			set[p] = true
		}
	}
	return set
}

// exfs_cloneStrs returns a fresh copy so an in-place PATH rewrite by
// injectLocalVenvPath cannot bleed across the determinism/idempotence calls.
func exfs_cloneStrs(s []string) []string { return append([]string(nil), s...) }

// exfs_strsEqual reports element-wise equality of two string slices.
func exfs_strsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
