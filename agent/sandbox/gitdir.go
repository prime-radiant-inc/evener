package sandbox

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/identifier"
)

var (
	gitWalkDir = filepath.WalkDir
	gitRel     = filepath.Rel
)

// WorkspaceKind classifies cwd's git layout. The distinction drives gitdir
// resolution: linked-worktree/submodule git dirs live outside the worktree and
// need a read grant + external config/hook protection, while a MainCheckout's
// .git sits inside it.
type WorkspaceKind int

const (
	// NonGit: cwd is not inside any git repository.
	NonGit WorkspaceKind = iota
	// MainCheckout: cwd's .git is a real directory (the primary checkout).
	MainCheckout
	// LinkedWorktree: cwd's .git is a pointer file into <main>/.git/worktrees/<id>.
	LinkedWorktree
	// Submodule: cwd's .git is a pointer file into <super>/.git/modules/<name>.
	Submodule
)

// String returns the kind's name for diagnostics.
func (k WorkspaceKind) String() string {
	switch k {
	case NonGit:
		return "non-git"
	case MainCheckout:
		return "main-checkout"
	case LinkedWorktree:
		return "linked-worktree"
	case Submodule:
		return "submodule"
	default:
		return fmt.Sprintf("WorkspaceKind(%d)", int(k))
	}
}

// GitLayout is the resolved git-surface map for a workspace: the writable
// metadata (so commit/add/checkout work), the write-PROTECTED config + hook
// surfaces (the persistence vectors), and — for layouts whose git dir lives
// outside the worktree — the paths that must still be READ-granted. All paths
// are absolute and symlink-resolved. A zero-value GitLayout (Kind NonGit, empty
// sets) is what an off-mode or non-repo session carries.
type GitLayout struct {
	Kind         WorkspaceKind
	WorktreeRoot string // active working-tree root (the writable content root)
	GitDir       string // this worktree's git dir (.git, .git/worktrees/<id>, or .git/modules/<name>)
	CommonDir    string // shared common git dir (main repo's .git; == GitDir for main/submodule)

	// WritablePaths are git-metadata paths writable in sandboxed writable modes:
	// objects, refs, index, logs, packed-refs (the spec's stated writable set),
	// rooted at the common dir and, for a linked worktree, the per-worktree dir.
	WritablePaths []string

	// ProtectedPaths are config + hook surfaces that stay WRITE-denied in every
	// writable mode (readable, never writable): .git/config, config.worktree,
	// .git/hooks, and submodule .git/modules/*/config + hooks. Because every
	// config file git reads is here (and $HOME configs are unwritable), a
	// core.hooksPath redirect can never be persisted.
	ProtectedPaths []string

	// ReadGrantPaths are paths outside WorktreeRoot that must still be readable
	// (a linked worktree / submodule reads its common/shared git dir, which lives
	// outside the worktree). Empty for a main checkout (its .git is inside).
	ReadGrantPaths []string
}

// gitWritableLeaves are the git-metadata entries granted writable in sandboxed
// writable modes so commit/add/checkout still function.
var gitWritableLeaves = []string{"objects", "refs", "index", "logs", "packed-refs"}

// gitProtectedLeaves are the per-git-dir config + hook surfaces kept write-denied,
// plus the two redirect files a linked-worktree git dir carries directly:
// commondir (points at the shared common dir — repointing it makes git read an
// attacker-controlled common dir's config, incl. core.hooksPath) and gitdir (the
// worktree back-pointer). Both are set once at `git worktree add` and never
// rewritten by a commit, so protecting them cannot regress the commit path.
var gitProtectedLeaves = []string{"config", "config.worktree", "hooks", "commondir", "gitdir"}

// resolveCleanPath is the package's symlink-resolving path normalizer, shared by
// the classifier and its tests so expected and actual paths normalize identically.
func resolveCleanPath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

// ClassifyWorkspace resolves cwd's git layout structurally (reading .git entries
// directly; it never forks the git binary) into a GitLayout. It returns an error
// only on an unreadable or unrecognized .git pointer shape — a fail-closed signal
// the resolver turns into a refusal rather than guessing a grant set.
func ClassifyWorkspace(cwd string) (GitLayout, error) {
	cwd = resolveCleanPath(cwd)

	holder, isDir, ok := findGitEntry(cwd)
	if !ok {
		return GitLayout{Kind: NonGit, WorktreeRoot: cwd}, nil
	}
	entry := filepath.Join(holder, ".git")

	if isDir {
		// Resolve a symlinked .git to its real target so protected surfaces point
		// at the real config/hooks (not the symlink) — otherwise a subtractive
		// backend would deny the symlink while the real config sits writable.
		gitDir := resolveCleanPath(entry)
		layout := newLayout(MainCheckout, holder, gitDir, gitDir)
		// A symlinked .git entry is itself a redirection surface: repointing it
		// swaps in an attacker-controlled git dir. Protect the symlink from writes.
		// (A real .git DIRECTORY must stay writable — its metadata lives inside.)
		if isSymlink(entry) {
			layout.ProtectedPaths = appendUnique(layout.ProtectedPaths, entry)
		}
		return withIncludeProtections(layout)
	}

	// .git is a pointer file: "gitdir: <path>".
	content, err := os.ReadFile(entry)
	if err != nil {
		return GitLayout{}, fmt.Errorf("reading .git pointer at %s: %w", holder, err)
	}
	pointer, ok := identifier.ParseGitdirPointer(string(content))
	if !ok {
		return GitLayout{}, fmt.Errorf("unrecognized .git pointer at %s", holder)
	}
	if !filepath.IsAbs(pointer) {
		pointer = filepath.Join(holder, pointer)
	}
	gitDir := resolveCleanPath(pointer)

	var layout GitLayout
	switch {
	case filepath.Base(filepath.Dir(gitDir)) == "worktrees":
		// <main>/.git/worktrees/<id> → common dir is <main>/.git.
		commonDir := filepath.Dir(filepath.Dir(gitDir))
		layout = newLayout(LinkedWorktree, holder, gitDir, commonDir)
	case gitDirUnderModules(gitDir):
		// <super>/.git/modules/<name> → the submodule's git dir is its own common
		// dir. The <name> segment is the submodule's path in the superproject and
		// can itself contain directories (a submodule added at "libs/foo" →
		// ".git/modules/libs/foo"), so the immediate parent of the git dir is not
		// necessarily "modules"; detect the ".git/modules" segment anywhere.
		layout = newLayout(Submodule, holder, gitDir, gitDir)
	default:
		return GitLayout{}, fmt.Errorf("unrecognized .git pointer shape %q at %s", gitDir, holder)
	}
	// The .git pointer file itself lives inside the writable worktree; rewriting
	// it ("gitdir: ./evil") redirects git to an attacker-planted dir. Protect it.
	layout.ProtectedPaths = appendUnique(layout.ProtectedPaths, entry)
	return withIncludeProtections(layout)
}

// gitDirUnderModules reports whether gitDir lies under a ".git/modules" segment
// — i.e. it is a submodule's git dir. A submodule at superproject path "libs/foo"
// resolves to "<super>/.git/modules/libs/foo", and a submodule-of-a-submodule to
// "<super>/.git/modules/<outer>/modules/<inner>", so the immediate parent of the
// git dir is not reliably "modules"; the ".git" + "modules" component pair may
// appear at any depth. Detect that pair anywhere rather than checking only the
// immediate parent.
func gitDirUnderModules(gitDir string) bool {
	parts := strings.Split(gitDir, string(filepath.Separator))
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == ".git" && parts[i+1] == "modules" {
			return true
		}
	}
	return false
}

// isSymlink reports whether path is a symbolic link (without following it).
func isSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&fs.ModeSymlink != 0
}

// findGitEntry walks up from cwd to the nearest directory containing a ".git"
// entry, returning that directory, whether the entry is a directory (vs a
// pointer file), and ok=false if none is found before the filesystem root.
func findGitEntry(cwd string) (holder string, isDir, ok bool) {
	dir := filepath.Clean(cwd)
	for {
		info, err := os.Lstat(filepath.Join(dir, ".git"))
		if err == nil {
			// A symlinked .git is followed via Stat to learn dir-vs-file.
			if info.Mode()&fs.ModeSymlink != 0 {
				if st, serr := os.Stat(filepath.Join(dir, ".git")); serr == nil {
					return dir, st.IsDir(), true
				}
			}
			return dir, info.IsDir(), true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, false
		}
		dir = parent
	}
}

// newLayout builds a GitLayout for a resolved (kind, worktreeRoot, gitDir,
// commonDir), computing the writable, protected, and read-grant sets.
func newLayout(kind WorkspaceKind, worktreeRoot, gitDir, commonDir string) GitLayout {
	l := GitLayout{
		Kind:         kind,
		WorktreeRoot: resolveCleanPath(worktreeRoot),
		GitDir:       gitDir,
		CommonDir:    commonDir,
	}

	// Writable metadata lives in the common dir (shared objects/refs/packed-refs/
	// logs). A commit writes lockfiles (packed-refs.lock) and loose refs UNDER
	// these leaves, so granting the leaf dirs suffices for the common dir; its
	// config/hooks stay protected (re-bound read-only over these grants).
	for _, leaf := range gitWritableLeaves {
		l.WritablePaths = appendUnique(l.WritablePaths, filepath.Join(commonDir, leaf))
	}
	// For a linked worktree the per-worktree dir is granted WHOLE: a commit writes
	// files DIRECTLY in it (index.lock beside index, COMMIT_EDITMSG, ORIG_HEAD,
	// HEAD, MERGE_MSG) — grants on individual leaves like `index` leave the sibling
	// `index.lock` on a read-only parent (the vmm6 failure). Its config.worktree +
	// config + hooks stay in ProtectedPaths and are re-bound read-only ON TOP of
	// this grant, mirroring ControlPolicy's writable-registry treatment.
	if gitDir != commonDir {
		l.WritablePaths = appendUnique(l.WritablePaths, gitDir)
	}

	// Protected config + hook surfaces on the common dir and the per-worktree dir,
	// each INCLUDING its worktrees/<id>/config.worktree (a per-worktree config can
	// carry core.hooksPath; a main checkout that owns linked worktrees must protect
	// those too, or a redirect planted there persists into a later unsandboxed op).
	for _, p := range gitDirProtectedSurfaces(commonDir) {
		l.ProtectedPaths = appendUnique(l.ProtectedPaths, p)
	}
	if gitDir != commonDir {
		for _, p := range gitDirProtectedSurfaces(gitDir) {
			l.ProtectedPaths = appendUnique(l.ProtectedPaths, p)
		}
	}
	// Submodule config/hook surfaces of nested modules (protect .git/modules/*/config).
	for _, mp := range moduleConfigProtections(commonDir) {
		l.ProtectedPaths = appendUnique(l.ProtectedPaths, mp)
	}

	// A git dir outside the worktree (linked worktree / submodule) must be
	// read-granted so git can read its common config/objects from the worktree.
	if kind == LinkedWorktree || kind == Submodule {
		l.ReadGrantPaths = appendUnique(l.ReadGrantPaths, commonDir)
	}

	// A protected surface must never be reachable through a writable grant, or a
	// write would slip past the config/hook denial. gitWritableLeaves and
	// gitProtectedLeaves are disjoint sibling names, so this holds by construction;
	// the classifier keeps the sets disjoint rather than relying on the caller.
	l.WritablePaths = removeProtectedFromWritable(l.WritablePaths, l.ProtectedPaths)
	return l
}

// gitDirProtectedSurfaces returns the config + hook surfaces of a single git dir
// that stay write-denied: its own config, config.worktree, and hooks, PLUS each
// per-worktree worktrees/<id>/config.worktree (and config), which can independently
// carry a core.hooksPath redirect. Paths are listed whether or not they exist yet
// (protecting a not-yet-created surface denies a plant).
func gitDirProtectedSurfaces(gitDir string) []string {
	out := make([]string, 0, len(gitProtectedLeaves)+2)
	for _, leaf := range gitProtectedLeaves {
		out = append(out, filepath.Join(gitDir, leaf))
	}
	worktreesRoot := filepath.Join(gitDir, "worktrees")
	entries, err := os.ReadDir(worktreesRoot)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			wtDir := filepath.Join(worktreesRoot, e.Name())
			out = append(out, filepath.Join(wtDir, "config.worktree"), filepath.Join(wtDir, "config"))
		}
	}
	return out
}

// moduleConfigProtections walks <commonDir>/modules (if present) and returns the
// config + hook surfaces of every nested submodule git dir, so classifying a
// superproject protects its submodules' persistence vectors too. It prunes the
// large content subtrees (objects/refs/logs) — which never hold a config surface —
// so the walk is a config-surface scan, not a full object-store traversal.
func moduleConfigProtections(commonDir string) []string {
	modulesRoot := filepath.Join(commonDir, "modules")
	info, err := os.Stat(modulesRoot)
	if err != nil || !info.IsDir() {
		return nil
	}
	var out []string
	// A submodule git dir is any directory under modules/ that holds a "config"
	// file; protect its full surface set (incl. its own per-worktree configs).
	_ = gitWalkDir(modulesRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip unreadable entries; never abort the walk
		}
		if d.IsDir() {
			switch d.Name() {
			case "objects", "refs", "logs", "worktrees":
				return fs.SkipDir // no config surface here; worktrees handled per git dir
			}
			return nil
		}
		if d.Name() == "config" {
			out = append(out, gitDirProtectedSurfaces(filepath.Dir(path))...)
		}
		return nil
	})
	return out
}

// gitConfigIncludeMaxDepth bounds recursion through chained config includes so a
// cyclic or pathological include graph cannot loop forever. Ten levels is far
// beyond any real git config's include nesting.
const gitConfigIncludeMaxDepth = 10

// withIncludeProtections closes the git-config include bypass. A protected config
// file (write-denied) can `include.path` / `includeIf.*.path` another file, and
// git reads that included file's directives too — so if it lives inside a writable
// root the model could write core.hooksPath into it and persist a hook past the
// sandbox. This reads each protected config surface, resolves its include targets
// (recursively, bounded), and write-protects any target that lands within a
// writable region (the worktree content root or the writable git-metadata paths).
// An include whose target cannot be resolved to a concrete file yet could reach a
// writable region (a glob path) fails closed with an error the resolver turns into
// a *RefusalError. Residual: `~`-anchored includes resolve into a home directory,
// which is never a writable root, so they are not followed.
func withIncludeProtections(l GitLayout) (GitLayout, error) {
	writable := append([]string{l.WorktreeRoot}, l.WritablePaths...)
	seen := map[string]bool{}
	var extra []string
	for _, p := range l.ProtectedPaths {
		if base := filepath.Base(p); base != "config" && base != "config.worktree" {
			continue // only config files carry include directives (skip hooks dir, .git pointer)
		}
		found, err := scanConfigIncludes(p, writable, seen, 0)
		if err != nil {
			return GitLayout{}, err
		}
		extra = append(extra, found...)
	}
	for _, e := range extra {
		l.ProtectedPaths = appendUnique(l.ProtectedPaths, e)
	}
	// Adding include targets can, in pathological cases, overlap a writable git-
	// metadata path; keep the two sets disjoint.
	l.WritablePaths = removeProtectedFromWritable(l.WritablePaths, l.ProtectedPaths)
	return l, nil
}

// scanConfigIncludes reads one git config file, resolves its include targets,
// returns those that land within a writable region, and recurses into every
// resolvable target (a non-writable include can itself include a writable file).
// A missing/unreadable file has no includes to follow. A glob include path that
// could reach a writable region is unresolvable → error (fail closed).
func scanConfigIncludes(configFile string, writable []string, seen map[string]bool, depth int) ([]string, error) {
	if depth > gitConfigIncludeMaxDepth {
		return nil, fmt.Errorf("git config include chain exceeds depth %d at %s", gitConfigIncludeMaxDepth, configFile)
	}
	if seen[configFile] {
		return nil, nil
	}
	seen[configFile] = true

	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, nil //nolint:nilerr // a not-yet-created/unreadable config has no includes to follow
	}
	configDir := filepath.Dir(configFile)

	var out []string
	for _, v := range parseIncludePaths(string(data)) {
		if strings.HasPrefix(v, "~") {
			continue // home-relative; a home directory is never a writable root
		}
		if strings.ContainsAny(v, "*?[") {
			if globCouldReachWritable(v, configDir, writable) {
				return nil, fmt.Errorf("git config %s has include path %q that could resolve into a writable root and cannot be evaluated safely", configFile, v)
			}
			continue // glob confined to a non-writable area
		}
		target := v
		if !filepath.IsAbs(target) {
			target = filepath.Join(configDir, target)
		}
		target = filepath.Clean(target)
		if underAnyRoot(target, writable) {
			out = append(out, target)
		}
		sub, err := scanConfigIncludes(target, writable, seen, depth+1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// parseIncludePaths extracts the `path` values of [include] and [includeIf "..."]
// sections from git config text. It ignores the includeIf CONDITION — we treat
// every include as active (fail closed) rather than trying to evaluate it — and
// returns only the raw, quote-trimmed path values. It is a deliberately small
// git-config reader: enough to find include directives, handling both the
// canonical multi-line form and a same-line "[include] path = x".
func parseIncludePaths(text string) []string {
	var out []string
	inInclude := false
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end < 0 {
				inInclude = isIncludeSectionHeader(line)
				continue
			}
			inInclude = isIncludeSectionHeader(line[:end+1])
			if rest := strings.TrimSpace(line[end+1:]); inInclude && rest != "" {
				if p := includePathValue(rest); p != "" {
					out = append(out, p)
				}
			}
			continue
		}
		if !inInclude {
			continue
		}
		if p := includePathValue(line); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// isIncludeSectionHeader reports whether a "[section ...]" header names the
// include or includeIf section (case-insensitive, matching git).
func isIncludeSectionHeader(header string) bool {
	h := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(header), "["), "]")
	h = strings.TrimSpace(h)
	if i := strings.IndexAny(h, " \t\""); i >= 0 {
		h = h[:i]
	}
	return strings.EqualFold(h, "include") || strings.EqualFold(h, "includeIf")
}

// includePathValue returns the path value of a "path = <value>" config line
// (empty if the line is not a path key). It strips surrounding double quotes and,
// for an unquoted value, an inline # or ; comment.
func includePathValue(line string) string {
	i := strings.Index(line, "=")
	if i < 0 {
		return ""
	}
	if !strings.EqualFold(strings.TrimSpace(line[:i]), "path") {
		return ""
	}
	val := strings.TrimSpace(line[i+1:])
	if len(val) >= 2 && val[0] == '"' {
		if parsed, ok := quotedIncludePathValue(val); ok {
			return parsed
		}
		return ""
	}
	if c := strings.IndexAny(val, "#;"); c >= 0 {
		val = strings.TrimSpace(val[:c])
	}
	return val
}

// quotedIncludePathValue returns the first complete double-quoted git-config
// value. Git permits escaped quotes and backslashes in a quoted value; scanning
// the closing quote rather than using strings.Index prevents an escaped quote in
// an include path from truncating the path and bypassing include protection.
func quotedIncludePathValue(value string) (string, bool) {
	var out strings.Builder
	for i := 1; i < len(value); i++ {
		switch value[i] {
		case '"':
			return out.String(), true
		case '\\':
			i++
			if i == len(value) {
				return "", false
			}
			switch value[i] {
			case '"', '\\':
				out.WriteByte(value[i])
			case 'n':
				out.WriteByte('\n')
			case 't':
				out.WriteByte('\t')
			case 'b':
				out.WriteByte('\b')
			default:
				return "", false
			}
		default:
			out.WriteByte(value[i])
		}
	}
	return "", false
}

// globCouldReachWritable reports whether a glob include path might match a file
// inside a writable region. It resolves the literal directory prefix before the
// first glob metacharacter and refuses (returns true) when that base directory
// is under a writable region OR a writable region is under it — either way a
// match could land somewhere writable. A glob confined to a clearly non-writable
// area (e.g. /etc/gitconfig.d/*.conf) returns false.
func globCouldReachWritable(v, configDir string, writable []string) bool {
	prefix := v
	if i := strings.IndexAny(v, "*?["); i >= 0 {
		prefix = v[:i]
	}
	dir := prefix
	if !strings.HasSuffix(prefix, "/") {
		dir = filepath.Dir(prefix)
	}
	switch {
	case dir == "" || dir == ".":
		dir = configDir
	case !filepath.IsAbs(dir):
		dir = filepath.Join(configDir, dir)
	}
	dir = filepath.Clean(dir)
	for _, w := range writable {
		if dir == w || pathUnder(dir, w) || pathUnder(w, dir) {
			return true
		}
	}
	return false
}

// underAnyRoot reports whether p is at or beneath any of roots.
func underAnyRoot(p string, roots []string) bool {
	for _, r := range roots {
		if p == r || pathUnder(p, r) {
			return true
		}
	}
	return false
}

func appendUnique(s []string, v string) []string {
	for _, e := range s {
		if e == v {
			return s
		}
	}
	return append(s, v)
}

// removeProtectedFromWritable drops any writable entry that equals or nests under
// a protected path — a defense-in-depth guard keeping the two sets disjoint even
// if the leaf constants were ever changed to overlap.
func removeProtectedFromWritable(writable, protected []string) []string {
	out := writable[:0:0]
	for _, w := range writable {
		blocked := false
		for _, p := range protected {
			if w == p || pathUnder(w, p) {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, w)
		}
	}
	return out
}

// pathUnder reports whether p is at or beneath dir.
func pathUnder(p, dir string) bool {
	rel, err := gitRel(dir, p)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !hasParentPrefix(rel)
}

func hasParentPrefix(rel string) bool {
	return rel == ".." || len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator
}
