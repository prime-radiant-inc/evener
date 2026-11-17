package execenv

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	gitignore "github.com/sabhiram/go-gitignore"
)

// TestIgnoreSet_Matches_NilSet covers the nil-set path (line 99-100).
func TestIgnoreSet_Matches_NilSet(t *testing.T) {
	t.Parallel()
	var s *ignoreSet
	if s.matches("foo.txt", false) {
		t.Fatal("expected false for nil set")
	}
	if s.matches("foo/", true) {
		t.Fatal("expected false for nil set (dir)")
	}
}

// TestIgnoreSet_Matches_DotDir covers matching with rel == "." (line 106-107).
func TestIgnoreSet_Matches_DotDir(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, ".", "*.tmp")
	if !set.matches("file.tmp", false) {
		t.Fatal("expected file.tmp to be ignored")
	}
	if set.matches("file.go", false) {
		t.Fatal("expected file.go NOT to be ignored")
	}
}

// TestIgnoreSet_Matches_PathEqualsDir covers relPath == id.rel (line 108-109).
func TestIgnoreSet_Matches_PathEqualsDir(t *testing.T) {
	t.Parallel()
	content := "*.log\n"
	fsys := fstest.MapFS{
		"sub":            {Mode: os.ModeDir | 0o755},
		"sub/.gitignore": {Data: []byte(content)},
	}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if err != nil {
		t.Fatal(err)
	}
	// "sub" == id.rel, so rel becomes "." — "*.log" won't match "."
	if set.matches("sub", false) {
		t.Fatal("expected sub (file) NOT to be ignored")
	}
	// sub/file.log should be ignored because rel becomes "file.log"
	if !set.matches("sub/file.log", false) {
		t.Fatal("expected sub/file.log to be ignored")
	}
}

// TestIgnoreSet_Matches_PrefixMatch covers the prefix match path (line 110-111).
func TestIgnoreSet_Matches_PrefixMatch(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, "sub", "build/")
	if !set.matches("sub/build/", true) {
		t.Fatal("expected sub/build/ to be ignored (dir)")
	}
	if !set.matches("sub/build/out.txt", false) {
		t.Fatal("expected sub/build/out.txt to be ignored")
	}
}

// TestIgnoreSet_Matches_DefaultContinue covers the default-continue path
// (line 112-113) where relPath doesn't match any dir prefix.
func TestIgnoreSet_Matches_DefaultContinue(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, "sub", "*.log")
	if set.matches("other/file.txt", false) {
		t.Fatal("expected other/file.txt NOT to be ignored")
	}
}

// TestIgnoreSet_Matches_IsDir covers the isDir trailing-slash append (line 115-116).
func TestIgnoreSet_Matches_IsDir(t *testing.T) {
	t.Parallel()
	set := loadIgnoreSetFromLines(t, ".", "node_modules/")
	if !set.matches("node_modules", true) {
		t.Fatal("expected node_modules dir to be ignored")
	}
}

// TestIsDotPath covers the IsDotPath function (lines 139-146).
func TestIsDotPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want bool
	}{
		{"foo.txt", false},
		{".git", true},
		{"foo/bar.txt", false},
		{"foo/.hidden", true},
		{".claude/worktrees", true},
		{"./foo", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := IsDotPath(tc.path); got != tc.want {
			t.Errorf("IsDotPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestLoadIgnoreSet_SkipFile covers the skip path for a file (line 62-63).
func TestLoadIgnoreSet_SkipFile(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		".gitignore":      {Data: []byte("*.tmp\n")},
		"skipme":          {Data: []byte("data\n")},
		"skipme/file.txt": {Data: []byte("data\n")},
	}
	set, err := loadIgnoreSet(t.Context(), fsys, func(relPath string) bool {
		return relPath == "skipme"
	}, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if err != nil {
		t.Fatal(err)
	}
	// The .gitignore at root should still be loaded.
	if !set.matches("test.tmp", false) {
		t.Fatal("expected test.tmp to be ignored by root .gitignore")
	}
}

// TestGlobMatchIsDir covers the globMatchIsDir function (lines 130-133).
func TestGlobMatchIsDir(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"file.txt": {Data: []byte("hello")},
		"subdir":   {Data: []byte(""), Mode: os.ModeDir | 0o755},
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "file.txt"); isDir {
		t.Fatal("expected file.txt NOT to be a directory")
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "subdir"); !isDir {
		t.Fatal("expected subdir to be a directory")
	}
	if isDir, _ := globMatchIsDir(context.Background(), fsys, "nonexistent"); isDir {
		t.Fatal("expected nonexistent NOT to be a directory")
	}
}

// loadIgnoreSetFromLines builds an in-memory filesystem with a .gitignore
// in the given directory and returns the loaded ignoreSet.
func loadIgnoreSetFromLines(t *testing.T, dir string, lines ...string) *ignoreSet {
	t.Helper()
	var sb strings.Builder
	for _, l := range lines {
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	content := sb.String()
	path := ".gitignore"
	if dir != "." {
		path = dir + "/.gitignore"
	}
	fsys := fstest.MapFS{
		path: {Data: []byte(content)},
	}
	set, err := loadIgnoreSet(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// countCompiles swaps the matcher compile seam for the duration of a test and
// returns a pointer to the number of compiles it has seen.
func countCompiles(t *testing.T) *int {
	t.Helper()
	compiles := new(int)
	orig := compileIgnoreLines
	compileIgnoreLines = func(lines ...string) *gitignore.GitIgnore {
		*compiles++
		return orig(lines...)
	}
	t.Cleanup(func() { compileIgnoreLines = orig })
	return compiles
}

// TestIgnoreMatcherCacheReusesACompiledMatcher covers the lifetime change the
// cache exists for: an operation over a tree another operation already read
// compiles no rules file again, and the reused matchers answer exactly as the
// freshly compiled ones did.
func TestIgnoreMatcherCacheReusesACompiledMatcher(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore":     {Data: []byte("*.log\n!keep.log\n")},
		"sub/.gitignore": {Data: []byte("*.tmp\n")},
		"sub/a.log":      {Data: []byte("x")},
		"sub/a.tmp":      {Data: []byte("x")},
		"keep.log":       {Data: []byte("x")},
		"a.go":           {Data: []byte("x")},
	}
	compiles := countCompiles(t)
	cache := newIgnoreMatcherCache()

	first, err := loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
	if err != nil {
		t.Fatal(err)
	}
	if *compiles != 2 {
		t.Fatalf("compiles = %d after the first call, want 2: one per rules file", *compiles)
	}

	second, err := loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
	if err != nil {
		t.Fatal(err)
	}
	if *compiles != 2 {
		t.Fatalf("compiles = %d after the second call, want 2 unchanged: the cache recompiled a rules file whose size and mtime had not changed", *compiles)
	}

	probes := []struct {
		path  string
		isDir bool
	}{
		{"sub/a.log", false},
		{"sub/a.tmp", false},
		{"keep.log", false},
		{"a.go", false},
		{"sub", true},
	}
	for _, p := range probes {
		if got, want := second.matches(p.path, p.isDir), first.matches(p.path, p.isDir); got != want {
			t.Fatalf("cached matches(%q, dir=%v) = %v, want %v: the reused matcher answered differently from the one it replaced", p.path, p.isDir, got, want)
		}
	}
	if !second.matches("sub/a.log", false) {
		t.Fatal("the cached set does not ignore sub/a.log, so reusing it lost the rules rather than the compile")
	}
	if second.matches("keep.log", false) {
		t.Fatal("the cached set ignores keep.log, so reusing it dropped the negation")
	}
}

// TestIgnoreMatcherCacheRecompilesOnAChangedRulesFile pins the staleness bound:
// a rules file that gained a rule, or was rewritten to the same byte count at a
// newer mtime, is recompiled rather than served from the cache.
func TestIgnoreMatcherCacheRecompilesOnAChangedRulesFile(t *testing.T) {
	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		data    string
		modTime time.Time
	}{
		{name: "appended rule", data: "*.log\n*.tmp\n", modTime: base},
		{name: "rewritten at the same size", data: "*.bak\n", modTime: base.Add(2 * time.Second)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			old := fstest.MapFS{
				".gitignore": {Data: []byte("*.log\n"), ModTime: base},
				"a.log":      {Data: []byte("x")},
				"a.tmp":      {Data: []byte("x")},
				"a.bak":      {Data: []byte("x")},
			}
			compiles := countCompiles(t)
			cache := newIgnoreMatcherCache()
			set, err := loadIgnoreSetCached(t.Context(), old, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
			if err != nil {
				t.Fatal(err)
			}
			if !set.matches("a.log", false) {
				t.Fatal("the first pass does not ignore a.log, so the rules file was not read")
			}

			changed := fstest.MapFS{
				".gitignore": {Data: []byte(tc.data), ModTime: tc.modTime},
				"a.log":      {Data: []byte("x")},
				"a.tmp":      {Data: []byte("x")},
				"a.bak":      {Data: []byte("x")},
			}
			set, err = loadIgnoreSetCached(t.Context(), changed, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
			if err != nil {
				t.Fatal(err)
			}
			if *compiles != 2 {
				t.Fatalf("compiles = %d after the rules file changed, want 2: the cache served a matcher built from the old bytes", *compiles)
			}
			if tc.name == "appended rule" {
				if !set.matches("a.tmp", false) {
					t.Fatal("a.tmp is not ignored after the rules file gained *.tmp, so the appended rule was lost")
				}
			} else {
				if !set.matches("a.bak", false) {
					t.Fatal("a.bak is not ignored after the rules file was rewritten, so the same-size rewrite was not recompiled")
				}
				if set.matches("a.log", false) {
					t.Fatal("a.log is still ignored after the rules file dropped *.log, so the old matcher was reused")
				}
			}
		})
	}
}

// TestIgnoreMatcherCacheKeepsIgnoreSemanticsIdentical compares a cached pass
// with an uncached one over the shapes that make ignore handling subtle: a
// nested rules file that overrides its parent, a negation inside one file, a
// directory-only rule, and a path outside every rules file's reach.
func TestIgnoreMatcherCacheKeepsIgnoreSemanticsIdentical(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore":        {Data: []byte("*.log\n!important.log\nbuild/\n")},
		"sub/.gitignore":    {Data: []byte("!keep.log\n*.gen.go\n")},
		"sub/a.log":         {Data: []byte("x")},
		"sub/keep.log":      {Data: []byte("x")},
		"sub/a.gen.go":      {Data: []byte("x")},
		"sub/a.go":          {Data: []byte("x")},
		"build/out.bin":     {Data: []byte("x")},
		"important.log":     {Data: []byte("x")},
		"other/untouched.y": {Data: []byte("x")},
	}
	probes := []struct {
		path  string
		isDir bool
	}{
		{"sub/a.log", false},
		{"sub/keep.log", false},
		{"sub/a.gen.go", false},
		{"sub/a.go", false},
		{"build", true},
		{"build/out.bin", false},
		{"important.log", false},
		{"other/untouched.y", false},
	}
	cache := newIgnoreMatcherCache()
	uncached := make([]bool, len(probes))
	for pass := range 2 {
		set, err := func() (*ignoreSet, error) {
			if pass == 0 {
				return loadIgnoreSet(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope())
			}
			return loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
		}()
		if err != nil {
			t.Fatal(err)
		}
		for i, p := range probes {
			got := set.matches(p.path, p.isDir)
			if pass == 0 {
				uncached[i] = got
				continue
			}
			if got != uncached[i] {
				t.Fatalf("cached matches(%q, dir=%v) = %v, want %v from the uncached pass: the cache changed ignore semantics", p.path, p.isDir, got, uncached[i])
			}
		}
	}
	if !slices.Contains(uncached, true) || !slices.Contains(uncached, false) {
		t.Fatalf("uncached answers = %v, want both true and false so the comparison exercises ignoring and keeping", uncached)
	}
}

// TestIgnoreMatcherCacheChargesAHitWhatTheReadPaid pins the budget interaction:
// a cache hit is charged exactly what the read that populated it paid, so a
// repeated operation spends the same rules budget as the first one.
func TestIgnoreMatcherCacheChargesAHitWhatTheReadPaid(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore":     {Data: []byte("# a comment\n*.log\n\n*.tmp\n")},
		"sub/.gitignore": {Data: []byte("build/\n")},
		"a.log":          {Data: []byte("x")},
	}
	cache := newIgnoreMatcherCache()
	firstBudget := newGlobBudget("glob")
	if _, err := loadIgnoreSetCached(t.Context(), fsys, nil, firstBudget, wholeBaseIgnoreScope(), "", cache); err != nil {
		t.Fatal(err)
	}
	hitBudget := newGlobBudget("glob")
	if _, err := loadIgnoreSetCached(t.Context(), fsys, nil, hitBudget, wholeBaseIgnoreScope(), "", cache); err != nil {
		t.Fatal(err)
	}
	if firstBudget.ruleBytes == 0 {
		t.Fatal("ruleBytes = 0 after reading two rules files, want a charge")
	}
	if hitBudget.ruleBytes != firstBudget.ruleBytes {
		t.Fatalf("ruleBytes = %d on a cache hit, want %d as the reading call paid: the hit skipped part of the charge, so repeating an operation could buy rules the budget refuses", hitBudget.ruleBytes, firstBudget.ruleBytes)
	}
}

// TestIgnoreMatcherCacheStillRefusesAHitWhenTheBudgetIsSpent pins the other
// half of that interaction: a hit whose charge would cross the call-wide rules
// budget is refused, not served. Without it a session could re-read a huge
// rules file any number of times for free.
func TestIgnoreMatcherCacheStillRefusesAHitWhenTheBudgetIsSpent(t *testing.T) {
	fsys := fstest.MapFS{
		".gitignore": {Data: []byte("*.log\n")},
		"a.log":      {Data: []byte("x")},
	}
	cache := newIgnoreMatcherCache()
	if _, err := loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache); err != nil {
		t.Fatal(err)
	}
	compiles := countCompiles(t)
	stubMaxGlobIgnoreTotalBytes(t, 1)
	set, err := loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", cache)
	if err == nil {
		t.Fatalf("no error with the rules budget at 1 byte and a warm cache, want a refusal: the hit bought the matcher for free (set has %d dirs)", len(set.dirs))
	}
	var budgetErr *globBudgetError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("error = %v, want a *globBudgetError", err)
	}
	if budgetErr.kind != budgetRulesTotal {
		t.Fatalf("globBudgetError.kind = %v, want budgetRulesTotal", budgetErr.kind)
	}
	if *compiles != 0 {
		t.Fatalf("compiles = %d while refusing a hit, want 0: the refusal should come from the charge, not from a recompile", *compiles)
	}
}

// TestIgnoreMatcherCacheIsNilSafe covers the environments built as struct
// literals, which have no cache to reach and must still read their rules.
func TestIgnoreMatcherCacheIsNilSafe(t *testing.T) {
	t.Parallel()
	if cache := (*LocalExecutionEnvironment)(nil).ignoreMatcherCacheFor(); cache != nil {
		t.Fatal("a nil environment answered a cache, want nil so its callers read rules uncached")
	}
	var c *ignoreMatcherCache
	c.store("", ".gitignore", ignoreMatcherEntry{})
	if _, ok := c.lookup("", ".gitignore", 0, time.Time{}); ok {
		t.Fatal("a nil cache answered a hit")
	}
	fsys := fstest.MapFS{
		".gitignore": {Data: []byte("*.log\n")},
	}
	if _, err := loadIgnoreSetCached(t.Context(), fsys, nil, newGlobBudget("glob"), wholeBaseIgnoreScope(), "", nil); err != nil {
		t.Fatalf("loadIgnoreSetCached with a nil cache: %v", err)
	}
}

// countEntryPointCompiles swaps the matcher compile seam for the duration of a
// test, returning a function that reports how many compiles have happened so
// far and a reset for measuring one call at a time.
func countEntryPointCompiles(t *testing.T) (count func() int, reset func()) {
	t.Helper()
	n := 0
	orig := compileIgnoreLines
	compileIgnoreLines = func(lines ...string) *gitignore.GitIgnore {
		n++
		return orig(lines...)
	}
	t.Cleanup(func() { compileIgnoreLines = orig })
	return func() int { return n }, func() { n = 0 }
}

// writeEntryPointIgnoreTree lays down a tree with a root rules file, a nested
// one carrying a negation, and files the rules select and spare.
func writeEntryPointIgnoreTree(t *testing.T) (root string) {
	t.Helper()
	root = t.TempDir()
	dirs := []string{"sub", "build"}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		".gitignore":     "*.log\n!important.log\nbuild/\n",
		"sub/.gitignore": "*.tmp\n",
		"a.go":           "package a\n",
		"a.log":          "log\n",
		"important.log":  "keep\n",
		"sub/b.go":       "package b\n",
		"sub/b.log":      "log\n",
		"sub/b.tmp":      "tmp\n",
		"build/out.bin":  "bin\n",
		"keep.txt":       "keep\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestGlobReusesIgnoreMatchersAcrossCalls is the test the review asked for: two
// identical env.Glob calls on one environment, the second of which must compile
// nothing. It goes through the exported entry point rather than the helper,
// because the helper is handed a cache directly and therefore passed while no
// production call site passed one.
func TestGlobReusesIgnoreMatchersAcrossCalls(t *testing.T) {
	t.Parallel()
	root := writeEntryPointIgnoreTree(t)
	env := NewLocalExecutionEnvironment(root)
	count, reset := countEntryPointCompiles(t)

	glob := func() []string {
		matches, err := env.Glob(t.Context(), "**/*", "")
		if err != nil {
			t.Fatalf("Glob: %v", err)
		}
		return matches
	}
	first := glob()
	if firstCompiles := count(); firstCompiles == 0 {
		t.Fatal("the first env.Glob compiled no rules file, so the tree has no rules to cache and this test proves nothing")
	}
	reset()
	second := glob()
	if got := count(); got != 0 {
		t.Fatalf("the second env.Glob compiled %d rules file(s), want 0: the environment's cache is not reaching Glob's ignore load", got)
	}
	if !slices.Equal(first, second) {
		t.Fatalf("the two calls returned different results, so the cached matchers changed what is matched:\nfirst  = %v\nsecond = %v", first, second)
	}
	if slices.ContainsFunc(second, func(p string) bool { return filepath.Base(p) == "a.log" }) {
		t.Fatalf("an ignored file survived the cached pass: %v", second)
	}
	if !slices.ContainsFunc(second, func(p string) bool { return filepath.Base(p) == "important.log" }) {
		t.Fatalf("the negated file is missing from the cached pass: %v", second)
	}
}

// TestGrepReusesIgnoreMatchersAcrossCalls is the same assertion for grep, through
// the exported env.Grep. grep hands the search to ripgrep when ripgrep is
// installed, and that subprocess does its own ignore handling, so the test makes
// rg "absent" through the execLookPath seam to force the native walk — the arm
// that reads rules through grepTreeWalk. It is not parallel for that reason.
func TestGrepReusesIgnoreMatchersAcrossCalls(t *testing.T) {
	root := writeEntryPointIgnoreTree(t)
	env := NewLocalExecutionEnvironment(root)
	count, reset := countEntryPointCompiles(t)
	origLookPath := execLookPath
	execLookPath = func(string) (string, error) { return "", errors.New("no rg: force the native walk") }
	t.Cleanup(func() { execLookPath = origLookPath })

	grep := func() string {
		out, err := env.Grep(t.Context(), "log", root, "", false, 100, "files_with_matches")
		if err != nil {
			t.Fatalf("Grep: %v", err)
		}
		return out
	}
	first := grep()
	if firstCompiles := count(); firstCompiles == 0 {
		t.Fatal("the first env.Grep compiled no rules file, so this test proves nothing")
	}
	reset()
	second := grep()
	if got := count(); got != 0 {
		t.Fatalf("the second env.Grep compiled %d rules file(s), want 0: the environment's cache is not reaching grep's ignore load", got)
	}
	if first != second {
		t.Fatalf("the two grep calls disagreed:\nfirst  = %q\nsecond = %q", first, second)
	}
}

// TestGrepExclusionCountReusesIgnoreMatchers covers the second grep entry point,
// countNativeExcluded, which the rg arm calls to disclose what the default rules
// skipped. It builds its own grepTreeWalk, so it needs its own wiring.
func TestGrepExclusionCountReusesIgnoreMatchers(t *testing.T) {
	t.Parallel()
	root := writeEntryPointIgnoreTree(t)
	env := NewLocalExecutionEnvironment(root)
	count, reset := countEntryPointCompiles(t)

	excluded := func() int {
		n, err := env.countNativeExcluded(t.Context(), root, nil)
		if err != nil {
			t.Fatalf("countNativeExcluded: %v", err)
		}
		return n
	}
	first := excluded()
	if firstCompiles := count(); firstCompiles == 0 {
		t.Fatal("the first countNativeExcluded compiled no rules file, so this test proves nothing")
	}
	reset()
	second := excluded()
	if got := count(); got != 0 {
		t.Fatalf("the second countNativeExcluded compiled %d rules file(s), want 0: the environment's cache is not reaching the exclusion count's ignore load", got)
	}
	if first != second {
		t.Fatalf("the two exclusion counts disagreed: %d then %d", first, second)
	}
	if first == 0 {
		t.Fatal("countNativeExcluded reported nothing excluded, so the tree gave it nothing to count and this test proves nothing")
	}
}

// TestIgnoreMatcherCacheSeparatesTwoBasesWithIdenticalTimestamps pins the key:
// one environment, two bases, each with a .gitignore of the SAME size and the
// same modification time but different rules. Each call must get its own
// directory's rules. A cache keyed on the base-relative path serves the first
// directory's matcher to the second call and silently hides the wrong files.
func TestIgnoreMatcherCacheSeparatesTwoBasesWithIdenticalTimestamps(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	stamp := timeStamp(t)
	// Same byte count, same mtime, different rules.
	const firstRules = "*.aaa\n"
	const secondRules = "*.bbb\n"
	if len(firstRules) != len(secondRules) {
		t.Fatalf("the two rule files differ in length (%d vs %d), so this test cannot prove the key distinguishes them by path alone", len(firstRules), len(secondRules))
	}
	for _, d := range []struct{ name, rules string }{{"alpha", firstRules}, {"beta", secondRules}} {
		dir := filepath.Join(root, d.name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gi := filepath.Join(dir, ".gitignore")
		if err := os.WriteFile(gi, []byte(d.rules), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(gi, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"x.aaa", "x.bbb", "x.go"} {
			if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	env := NewLocalExecutionEnvironment(root)
	names := func(base string) []string {
		matches, _, err := env.GlobWithBudget(t.Context(), "**/*", base, false, NewGlobBudget())
		if err != nil {
			t.Fatalf("GlobWithBudget(%s): %v", base, err)
		}
		out := make([]string, 0, len(matches))
		for _, m := range matches {
			out = append(out, filepath.Base(m))
		}
		return out
	}
	alphaFirst := names(filepath.Join(root, "alpha"))
	if slices.Contains(alphaFirst, "x.aaa") {
		t.Fatalf("the alpha base did not apply its own *.aaa rule: %v", alphaFirst)
	}
	beta := names(filepath.Join(root, "beta"))
	if slices.Contains(beta, "x.bbb") {
		t.Fatalf("the beta base applied *.bbb, so its own rules were not read: %v", beta)
	}
	// beta's rules are *.bbb, so aaa must be VISIBLE there. Its absence is what
	// a leaked matcher looks like: alpha's *.aaa rule applied to beta's files.
	if !slices.Contains(beta, "x.aaa") {
		t.Fatalf("the beta base did not report x.aaa, so it applied alpha's *.aaa rule: the cache served alpha's matcher to beta, whose .gitignore has the same size and mtime but different rules: %v", beta)
	}
}

// timeStamp returns one modification time both rules files are set to.
func timeStamp(t *testing.T) (stamp time.Time) {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "stamp")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(probe)
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().Truncate(time.Second)
}
