package execenv

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"primeradiant.com/evener/agent/internal/globpattern"
)

func expandSearchPattern(pattern string) ([]string, error) {
	expanded, err := globpattern.Expand(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid glob pattern: %w", err)
	}
	return expanded, nil
}

// cancelFS wraps an fs.FS so a walk over it observes ctx. doublestar has no
// context-aware entry point and reaches the filesystem only through Open,
// ReadDir and Stat, so failing those three the moment ctx is done is what
// makes an in-flight glob abortable — without it a `**` walk over a large
// tree runs to completion no matter who asks it to stop.
type cancelFS struct {
	ctx  context.Context
	fsys fs.FS
}

func (c cancelFS) Open(name string) (fs.File, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return c.fsys.Open(name)
}

func (c cancelFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return fs.ReadDir(c.fsys, name)
}

func (c cancelFS) Stat(name string) (fs.FileInfo, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	return fs.Stat(c.fsys, name)
}

// maxGlobDirListings bounds how many directory listings one glob call may
// make, across ignore discovery and every brace-expanded pattern in it. It
// counts directories listed rather than capping depth because a symlink cycle
// costs unbounded *work*, not merely unbounded depth: one /proc/<pid>/root hop
// re-enters the entire tree, so a shallow depth cap would still admit a
// combinatorial re-walk while a deep one would truncate real results. It
// bounds every listing, not only ones on a filesystem without file identity:
// even where the cycle check works, an unbounded `**` over a huge tree (`/`)
// still costs unbounded work.
//
// The value itself is a WORK bound, not a memory bound — the match cap and the
// O(depth) ancestor chain already hold memory to a small constant, so this
// only has to be large enough that a legitimate call doesn't pay for it. One
// call can spend this budget several times over: once on ignore discovery,
// then again on each brace-expanded pattern (up to
// globpattern.MaxExpansions). A 60,000-directory monorepo globbed with a
// 5-way brace expansion costs on the order of 360,000 listings and must not
// error; `/` on a developer's machine is 500,000 to several million
// directories and must. 1,000,000 sits between the two.
var maxGlobDirListings = 1_000_000

// maxGlobMatches bounds how many matches one glob call may accumulate.
var maxGlobMatches = 10_000

// globBudget bounds the total work and memory one glob call may spend, shared
// by every brace-expanded pattern the call walks.
type globBudget struct {
	listings  int
	matches   int
	truncated bool
}

// listing charges one more directory listing to the budget regardless of
// cycleSafe: an unbounded `**` over a huge tree costs unbounded work and
// memory even where the cycle check works, so the bound has to apply whether
// or not this filesystem can tell directories apart. cycleSafe only picks
// which explanation the refusal gives once the bound trips — true when the
// walk that hit the bound could have detected a symlink cycle on its own
// (the pattern walk can, only where the filesystem reports file identity;
// ignore discovery never follows symlinks and so always can), false when it
// could not.
func (b *globBudget) listing(cycleSafe bool) error {
	b.listings++
	if b.listings <= maxGlobDirListings {
		return nil
	}
	return &globBudgetError{listings: b.listings, budget: maxGlobDirListings, cycleSafe: cycleSafe}
}

// globBudgetError reports that one glob call ran past its directory-listing
// budget. cycleSafe records whether the walk that gave up could have detected
// a symlink cycle on its own — the pattern walk can tell only where the
// filesystem reports file identity, while ignore discovery never follows
// symlinks and so always can — which picks which of Error's two explanations
// applies; listings and budget are the values a caller can act on without
// parsing either sentence.
type globBudgetError struct {
	listings  int
	budget    int
	cycleSafe bool
}

func (e *globBudgetError) Error() string {
	if !e.cycleSafe {
		return fmt.Sprintf("glob walk made %d directory listings on a filesystem that reports no file identity, so a symlink cycle cannot be detected: refusing to keep walking past the budget of %d; narrow the pattern or its base directory", e.listings, e.budget)
	}
	return fmt.Sprintf("glob walk made %d directory listings, past the budget of %d for one glob call: narrow the pattern or its base directory", e.listings, e.budget)
}

// match reports whether the walk may keep the next match, so the caller can
// end the walk itself rather than letting it run to completion and truncating
// the result afterward — the cap has to bound the work a glob call spends,
// not only the length of the answer it hands back.
func (b *globBudget) match() bool {
	if b.matches >= maxGlobMatches {
		b.truncated = true
		return false
	}
	b.matches++
	return true
}

func (b *globBudget) full() bool {
	return b.truncated
}

// truncatedAt reports the match cap that cut a glob call short, or 0 when
// every match was reported, so a caller can hand the cap value to the model
// without duplicating the truncated check at every call site that needs it.
func (b *globBudget) truncatedAt() int {
	if b.truncated {
		return maxGlobMatches
	}
	return 0
}

// listedDir is one entry on globWalkFS.chain: the identity of a directory on
// the path currently being walked.
type listedDir struct {
	name string
	info fs.FileInfo
}

// globWalkFS is the view of the filesystem a single doublestar walk runs over.
// It supplies the two properties doublestar itself cannot:
//
// Termination. It refuses to list a directory that is the same file as one of
// its own ancestors on this walk. That is the shape /proc/<pid>/root has —
// following the link re-enters the tree it lives in — and it is why a `**`
// glob rooted at / never returned (#369). Everything under such a directory is
// already reachable at a shorter path, so refusing costs no matches. A
// directory symlink pointing anywhere other than at its own ancestors
// (node_modules/lib -> ../packages/lib, or /etc -> /private/etc on macOS) is
// walked normally and its files match under both names. The check
// deliberately admits one case: two sibling names for the same directory,
// neither an ancestor of the other (a bind mount, or two symlinks pointing at
// one shared target), are both listed and so walked twice. That case has no
// unbounded recursion to catch — a sibling can't be its own ancestor — so it
// is left to cost two listings against the budget rather than being detected
// and refused.
//
// Abort. doublestar propagates a filesystem error only under
// WithFailOnIOErrors; without it a cancelled walk keeps grinding through every
// sibling its parent frames already listed. So the walk runs with that option
// and this wrapper reports every failure other than the cancellation as
// fs.ErrNotExist — the "this entry simply isn't there" answer doublestar
// already skips over — which leaves cancellation as the only error that can
// end a walk. An unreadable directory still does not fail the glob.
type globWalkFS struct {
	fs.FS
	ctx context.Context
	// chain holds the identity of every directory on the path currently being
	// walked, root first, so the ancestor check in admit can compare against
	// them without retaining one FileInfo for every directory the walk has
	// ever listed. See push for how it stays pruned to that path.
	chain []listedDir
	// budget bounds the directory listings and matches this walk may spend,
	// shared with the rest of the glob call's brace-expanded patterns.
	budget *globBudget
}

func (w *globWalkFS) Stat(name string) (fs.FileInfo, error) {
	info, err := fs.Stat(w.FS, name)
	if err != nil {
		return nil, w.hide("stat", name)
	}
	return info, nil
}

func (w *globWalkFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := w.admit(name); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(w.FS, name)
	if err != nil {
		return nil, w.hide("readdir", name)
	}
	return entries, nil
}

// admit reports whether the walk may list name, recording its identity so the
// directories below it can be checked against it.
func (w *globWalkFS) admit(name string) error {
	info, err := fs.Stat(w.FS, name)
	if err != nil {
		return w.hide("stat", name)
	}
	identified := hasFileIdentity(info)
	if err := w.budget.listing(identified); err != nil {
		return err
	}
	if !identified {
		return nil
	}
	// The walk root is skipped: it has no ancestors to cycle back to, and
	// path.Dir(".") is "." — scanning from there would find the root's own
	// entry and refuse the second listing doublestar makes of every directory.
	if name != "." {
		for dir := path.Dir(name); ; dir = path.Dir(dir) {
			if ancestor, ok := w.identity(dir); ok && os.SameFile(ancestor, info) {
				return w.hide("readdir", name)
			}
			if dir == "." {
				break
			}
		}
	}
	w.push(name, info)
	return nil
}

// identity answers dir's FileInfo from the chain of directories currently
// being walked, falling back to a fresh stat on a miss. The chain is only a
// cache of the path being walked — push prunes it down to that path on every
// listing — so a walk that jumps between subtrees still compares admit's
// candidate against every real ancestor by re-stat'ing it, and pruning the
// chain can never let a cycle slip past the check that costs nothing extra.
func (w *globWalkFS) identity(dir string) (fs.FileInfo, bool) {
	for _, d := range w.chain {
		if d.name == dir {
			return d.info, true
		}
	}
	info, err := fs.Stat(w.FS, dir)
	if err != nil {
		return nil, false
	}
	return info, true
}

// push records name's identity as the innermost entry on the chain, first
// dropping every entry that is not a proper ancestor of name. Only name's own
// ancestors are ever consulted by the cycle check, so pruning everything else
// loses nothing: the chain stays sized to the depth of the path currently
// being walked rather than growing with the number of directories the walk
// has listed in total.
func (w *globWalkFS) push(name string, info fs.FileInfo) {
	kept := 0
	for _, d := range w.chain {
		if d.name != name && (d.name == "." || strings.HasPrefix(name, d.name+"/")) {
			w.chain[kept] = d
			kept++
		}
	}
	w.chain = append(w.chain[:kept], listedDir{name: name, info: info})
}

// hide answers with the walk's cancellation when there is one — the walk runs
// under WithFailOnIOErrors so that ends it immediately — and otherwise with
// "not there", which doublestar skips past. See the type comment.
func (w *globWalkFS) hide(op, name string) error {
	if cerr := w.ctx.Err(); cerr != nil {
		return cerr
	}
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrNotExist}
}

// hasFileIdentity reports whether info carries the (device, inode) identity
// os.SameFile compares. os.SameFile answers false for any FileInfo that did
// not come from the os package — an fstest.MapFS entry, say — so comparing an
// info against itself is how to tell "a different file" from "cannot tell".
func hasFileIdentity(info fs.FileInfo) bool {
	return os.SameFile(info, info)
}

// errGlobMatchesFull signals the GlobWalk callback's own cap trip back out to
// globMatches: doublestar has no way to end a walk early other than the
// callback returning an error, so the cap has to speak in that vocabulary and
// then be told apart from a real failure once GlobWalk returns.
var errGlobMatchesFull = errors.New("glob match cap reached")

// globMatches runs one expanded glob pattern against fsys, which must already
// observe ctx (see cancelFS), through a fresh globWalkFS — fresh per pattern,
// because the directories one pattern listed say nothing about whether another
// pattern's walk is re-entering itself. budget is shared across every pattern
// in the call: it is checked before the walk starts, so a pattern that runs
// after the cap has already tripped does not re-walk the tree just to
// discover it can keep nothing, and it is checked on every match the walk
// finds, so a `**` with millions of hits stops there instead of accumulating
// all of them before the caller gets a chance to see any.
//
// GlobWalk rather than Glob: it ends the walk the moment the callback returns
// an error, so a cancellation — or the match cap tripping — that lands
// between two filesystem calls stops the walk at the next match instead of
// being noticed only after the tree runs out.
func globMatches(ctx context.Context, fsys fs.FS, pattern string, budget *globBudget) ([]string, error) {
	if budget.full() {
		// A cancelled walk must not come back as a plausible-looking short
		// list: a cap that is already tripped when a cancellation lands must
		// still answer with the cancellation, not with the truncated result
		// the cap alone would produce.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return nil, nil
	}
	walk := &globWalkFS{FS: fsys, ctx: ctx, budget: budget}
	var matches []string
	err := doublestar.GlobWalk(walk, pattern, func(p string, _ fs.DirEntry) error {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !budget.match() {
			return errGlobMatchesFull
		}
		matches = append(matches, p)
		return nil
	}, doublestar.WithFailOnIOErrors())
	// A cancelled walk must not come back as a plausible-looking short list,
	// so answer with the cancellation however the walk happened to unwind.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	// The match cap ending the walk is not a failure: it is the mechanism by
	// which the cap bounds the work rather than only the result, so the
	// matches collected before it tripped are the answer, not an error.
	if errors.Is(err, errGlobMatchesFull) {
		return matches, nil
	}
	if err != nil {
		return nil, err
	}
	return matches, nil
}

func expandGrepFilter(filter string) ([]string, error) {
	if strings.TrimSpace(filter) == "" {
		return nil, nil
	}
	return expandSearchPattern(filter)
}

func matchesAnyGrepFilter(name string, filters []string) (bool, error) {
	for _, filter := range filters {
		matched, err := filepath.Match(filter, name)
		if err != nil {
			// Preserve the existing grep behavior for malformed [] patterns:
			// brace syntax is validated by Expand, while filepath.Match errors
			// simply mean that this filter cannot match this filename.
			continue
		}
		if matched {
			return true, nil
		}
	}
	return false, nil
}
