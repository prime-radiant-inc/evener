package execenv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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
// make, across every brace-expanded pattern in it. It counts directories
// listed rather than capping depth because a symlink cycle costs unbounded
// *work*, not merely unbounded depth: one /proc/<pid>/root hop re-enters the
// entire tree, so a shallow depth cap would still admit a combinatorial
// re-walk while a deep one would truncate real results. It bounds every
// listing, not only ones on a filesystem without file identity: even where
// the cycle check works, an unbounded `**` over a huge tree (`/`) still costs
// unbounded work.
//
// The value itself is a WORK bound rather than a memory bound. Memory is held
// down by three other things: the match cap, the O(depth) ancestor chain, and
// the per-directory entry cap. What those leave is maxGlobDirEntries times the
// walk's depth, which is bounded but not small (see that constant), and
// raising this number does not make it larger. So this one only has to be big
// enough that a legitimate call never pays for it: a 60,000-directory
// monorepo globbed with a 5-way brace expansion costs on the order of 300,000
// listings and must not error; `/` on a developer's machine is 500,000 to
// several million directories and must. 1,000,000 sits between the two.
var maxGlobDirListings = 1_000_000

// globDirChunk is how many entries one bounded listing pulls per syscall. A
// variable, not a constant: it is the unit the per-listing entry bound is
// enforced in, so a test has to be able to shrink it below a fixture's size
// to see a listing stop after a few chunks instead of needing a directory too
// large to build in a test.
var globDirChunk = 4096

// maxGlobDirEntries bounds how many entries a SINGLE directory listing may
// materialize. It is per listing rather than cumulative because it bounds
// peak memory — but the peak one listing being small does not mean the
// walk's peak is: both fs.WalkDir and doublestar hold a directory's entry
// slice live for as long as they are recursing into one of its children, so a
// walk currently N levels deep is holding up to N listings at once, one per
// ancestor on the path, and only frees a sibling's slice once it moves on to
// the next one. The real peak is this cap times the walk's depth. An
// os.dirEntry is roughly 105 bytes including its name, so one listing at the
// cap costs on the order of 21 to 25 MB, which has to stay small enough to
// multiply by a plausible depth without exhausting memory on its own.
var maxGlobDirEntries = 200_000

// maxGlobLiveEntries bounds how many directory entries one call may hold live
// at once, summed over every listing its walk still has open. maxGlobDirEntries
// bounds a single listing, which does not bound the walk: fs.WalkDir and
// doublestar both keep an ancestor's entry slice alive while they read a
// child, so a walk N levels deep holds up to N listings at once. This is the
// aggregate ceiling that makes that sum finite. At roughly 105 bytes per
// os.dirEntry, 500,000 entries is about 52 MB — a couple of maximal
// directories' worth, far above any real tree (a deep repository holds a few
// thousand live entries) and far below what would threaten the process.
var maxGlobLiveEntries = 500_000

// maxGlobMatches bounds how many matches one glob call may accumulate.
var maxGlobMatches = 10_000

// SetMaxGlobMatchesForTesting overrides maxGlobMatches for the duration of a
// test, returning a restore func. maxGlobMatches is unexported, so callers
// outside this package (which cannot reassign it directly the way this
// package's own tests do) use this to shrink it enough to exercise real
// truncation without building a tree large enough to trip the production cap.
func SetMaxGlobMatchesForTesting(n int) (restore func()) {
	orig := maxGlobMatches
	maxGlobMatches = n
	return func() { maxGlobMatches = orig }
}

// GlobBudget bounds the total work and memory one glob call may spend, shared
// by every brace-expanded pattern the call walks and, through GlobBudgeter, by
// every caller that wants to see a truncated listing rather than being
// refused one. Its fields stay unexported: a caller reads what the call had
// to cut off it through TruncatedAt rather than inspecting the accounting
// directly.
type GlobBudget struct {
	listings  int
	matches   int
	truncated bool
	op        string
	// peakDirEntries is the largest number of entries tooManyEntries has ever
	// been asked to charge to a single listing. The per-listing bound exists
	// to cap peak memory, not merely to end in a refusal eventually, so this
	// is what lets a test tell a listing that stopped after a few chunks
	// apart from one that read a whole huge directory and only then reported
	// the refusal — a result-only assertion cannot see the difference, but the
	// OOM only the former avoids.
	peakDirEntries int
	// live holds one entry per directory listing the walk currently has open,
	// pruned by holdEntries down to just the ancestors of whichever directory
	// is being listed now.
	live []liveDir
	// peakLiveEntries is the largest live total holdEntries has ever computed
	// across b.live, recorded on every call rather than only the one that
	// trips, so a test can see how far a walk actually got.
	peakLiveEntries int
}

// liveDir is one directory whose listing the walk is still holding.
type liveDir struct {
	name string
	n    int
}

// newGlobBudget constructs a GlobBudget for op, so every internal call site
// names the operation its budget belongs to rather than leaving the field
// unset. NewGlobBudget is the entry point for callers outside this package,
// which always name the operation "glob".
func newGlobBudget(op string) *GlobBudget {
	return &GlobBudget{op: op}
}

// NewGlobBudget constructs a GlobBudget for a glob call, for a caller that
// wants to supply its own budget to GlobBudgeter.GlobWithBudget rather than
// go through GlobExcluder.GlobWithExclusions, which creates one internally
// and has nowhere to report a truncation it finds.
func NewGlobBudget() *GlobBudget {
	return newGlobBudget("glob")
}

// listing charges one more directory listing to the budget regardless of
// cycleSafe: an unbounded `**` over a huge tree costs unbounded work even
// where the cycle check works, so the bound has to apply whether or not this
// filesystem can tell directories apart. cycleSafe only picks which
// explanation the refusal gives once the bound trips: true when the walk
// that hit the bound could have detected a symlink cycle on its own (only
// where the filesystem reports file identity), false when it could not.
func (b *GlobBudget) listing(cycleSafe bool) error {
	b.listings++
	if b.listings <= maxGlobDirListings {
		return nil
	}
	return &globBudgetError{count: b.listings, budget: maxGlobDirListings, cycleSafe: cycleSafe, op: b.op, kind: budgetListings}
}

// tooManyEntries reports the refusal for a listing that has read more entries
// than one directory may materialize, or nil while it is still under. dir is
// the directory being listed, named in the refusal so a caller knows which
// one to act on. It records read against peakDirEntries on every call, not
// only the one that finally trips, so peakDirEntries reflects how far a
// listing actually got even on the chunks that stay under the bound.
func (b *GlobBudget) tooManyEntries(dir string, read int) error {
	if read > b.peakDirEntries {
		b.peakDirEntries = read
	}
	if read <= maxGlobDirEntries {
		return nil
	}
	return &globBudgetError{count: read, dir: dir, budget: maxGlobDirEntries, cycleSafe: true, op: b.op, kind: budgetEntries}
}

// isGlobWalkAncestor reports whether dir is a proper ancestor of name in the
// slash-separated paths a walk uses.
func isGlobWalkAncestor(dir, name string) bool {
	return dir != name && (dir == "." || strings.HasPrefix(name, dir+"/"))
}

// holdEntries records dir's listing of n entries as live and releases every
// directory that is not one of dir's ancestors: to be listing dir at all the
// walk must have left them, the same reasoning globWalkFS.push uses for
// identity. It refuses once the live total crosses maxGlobLiveEntries.
func (b *GlobBudget) holdEntries(dir string, n int) error {
	kept := 0
	total := n
	for _, d := range b.live {
		if isGlobWalkAncestor(d.name, dir) {
			b.live[kept] = d
			kept++
			total += d.n
		}
	}
	b.live = append(b.live[:kept], liveDir{name: dir, n: n})
	if total > b.peakLiveEntries {
		b.peakLiveEntries = total
	}
	if total > maxGlobLiveEntries {
		return &globBudgetError{count: total, budget: maxGlobLiveEntries, cycleSafe: true, op: b.op, kind: budgetLiveEntries}
	}
	return nil
}

// resetLive clears the record of directory listings this budget currently
// holds live, so a traversal beginning now starts from none held open: the
// total holdEntries computes is meant to reflect only what THAT walk has open
// at the time, and this budget is shared across more than one traversal in a
// call (ignore discovery, then each brace-expanded pattern's own walk) with
// nothing else marking where one ends and the next begins. The cumulative
// counters — listings, matches, peakDirEntries, peakLiveEntries — are left
// alone: those are meant to span the whole call, not any one traversal in it.
func (b *GlobBudget) resetLive() {
	b.live = nil
}

// globBudgetKind tells apart the three things a globBudgetError can report:
// too many directory listings across a whole call, too many entries
// materialized by a single one of them, or too many entries held live at once
// across the listings a walk still has open.
type globBudgetKind int

const (
	budgetListings globBudgetKind = iota
	budgetEntries
	budgetLiveEntries
)

// globBudgetError reports that one glob call ran past one of its bounds: its
// directory-listing budget, the per-directory entry budget for a single
// listing within it, or the call-wide ceiling on entries held live at once.
// cycleSafe records whether the walk that gave up could have detected a
// symlink cycle on its own — true only where the filesystem reports file
// identity — which picks which of the listings kind's two explanations
// applies; neither a single oversized directory nor an aggregate live total
// says anything about cycle detection, so those kinds are always cycleSafe.
// count is the whole call's directory-listing tally, the entry count of the
// one oversized directory, or the live total, according to kind; dir names
// the oversized directory and is empty for the other two kinds, which have no
// single directory to blame. count, dir and budget are the values a caller
// can act on without parsing any of the sentences.
type globBudgetError struct {
	count     int
	dir       string
	budget    int
	cycleSafe bool
	op        string
	kind      globBudgetKind
}

// advice names the lever that makes a whole call's listing count smaller.
func (e *globBudgetError) advice() string {
	if e.op == "grep" {
		return "narrow the base directory"
	}
	return "narrow the pattern or its base directory"
}

// entryAdvice names the lever that gets a caller past one oversized
// directory, the entries kind's counterpart to advice.
func (e *globBudgetError) entryAdvice() string {
	if e.op == "grep" {
		return "that directory is too large to list, so point the base at a smaller directory that does not contain it"
	}
	return "that directory is too large to list, so match inside it more specifically or point the base elsewhere"
}

// where names the oversized directory the way a caller can act on it. The
// walk's own root comes through as ".", which tells a model nothing, so it is
// reported as the base it passed in instead of echoed back as a bare dot.
func (e *globBudgetError) where() string {
	if e.dir == "" || e.dir == "." {
		return "the base directory"
	}
	return "directory " + e.dir
}

func (e *globBudgetError) Error() string {
	if e.kind == budgetEntries {
		return fmt.Sprintf("%s walk read %d entries from %s, past the per-directory budget of %d: %s", e.op, e.count, e.where(), e.budget, e.entryAdvice())
	}
	if e.kind == budgetLiveEntries {
		return fmt.Sprintf("%s walk is holding %d directory entries live across the listings it has open, past the call-wide budget of %d: %s", e.op, e.count, e.budget, e.advice())
	}
	if !e.cycleSafe {
		return fmt.Sprintf("%s walk made %d directory listings on a filesystem that reports no file identity, so a symlink cycle cannot be detected: refusing to keep walking past the budget of %d; %s", e.op, e.count, e.budget, e.advice())
	}
	return fmt.Sprintf("%s walk made %d directory listings, past the budget of %d for one call: %s", e.op, e.count, e.budget, e.advice())
}

// match reports whether the walk may keep the next match, so the caller can
// end the walk itself rather than letting it run to completion and truncating
// the result afterward — the cap has to bound the work a glob call spends,
// not only the length of the answer it hands back.
func (b *GlobBudget) match() bool {
	if b.matches >= maxGlobMatches {
		b.truncated = true
		return false
	}
	b.matches++
	return true
}

func (b *GlobBudget) full() bool {
	return b.truncated
}

// TruncatedAt reports the match cap that cut a glob call short, or 0 when
// every match was reported, so a caller can hand the cap value to the model
// without duplicating the truncated check at every call site that needs it.
func (b *GlobBudget) TruncatedAt() int {
	if b.truncated {
		return maxGlobMatches
	}
	return 0
}

// boundedDirFS lists a directory in chunks instead of materializing it whole,
// charging what it reads to budget after every chunk so one pathological
// directory cannot exhaust memory before the listing budget or match cap ever
// get a say — os.DirFS's own ReadDir, like the raw secureDirFS one it mirrors
// on the sandboxed arm, reads a directory's entire contents before handing
// any of it back, which is exactly the shape that lets a single huge
// directory OOM the walk regardless of how tightly the other bounds are set.
// It has to sit at the bottom of the plain path's stack, under any test
// wrapper such as countingFS, so every listing that reaches the walk is
// charged no matter what observes it from above.
type boundedDirFS struct {
	fs.FS
	// ctx reaches readDirChunked, which checks it between chunks. A chunk
	// loop reading from an already-open directory handle is invisible to
	// cancelFS's Open/ReadDir/Stat checks: those see a listing starting, not
	// the chunks inside one, so without this a cancelled call keeps reading a
	// directory nobody is waiting for.
	ctx    context.Context
	budget *GlobBudget
}

// Stat forwards to the wrapped fs.FS's own Stat rather than letting fs.Stat
// fall back to Open plus File.Stat. boundedDirFS embeds fs.FS as a bare
// interface value, which exposes only Open, so without this forward
// os.DirFS's fs.StatFS fast path is invisible here and a file that can be
// stat'ed but not opened (mode 0000) fails a call that used to succeed.
// fs.ReadFile and fs.Sub lose the same fast path and are deliberately not
// forwarded: their fallbacks (Open plus ReadAll, and a path-prefixing
// wrapper) are functionally identical to the fast path, and ReadFile's
// fallback fails on exactly the unreadable file its fast path would have
// failed on too, so only Stat's fallback actually changes behavior.
func (b boundedDirFS) Stat(name string) (fs.FileInfo, error) {
	return fs.Stat(b.FS, name)
}

// ReadDir lists name through readDirChunked once Open hands back an
// fs.ReadDirFile, falling back to reading it whole only for a filesystem that
// cannot list itself in pieces (a test fake, say). That fallback charges the
// per-directory entry budget (tooManyEntries) against what it reads, but not
// the call-wide live-entry ceiling (holdEntries): only readDirChunked folds a
// listing into live tracking, and this path is unreachable with os.DirFS, the
// only fs.FS boundedDirFS wraps in production.
func (b boundedDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	f, err := b.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	rdf, ok := f.(fs.ReadDirFile)
	if !ok {
		entries, err := fs.ReadDir(b.FS, name)
		if err != nil {
			return nil, err
		}
		if berr := b.budget.tooManyEntries(name, len(entries)); berr != nil {
			return nil, berr
		}
		return entries, nil
	}
	return readDirChunked(b.ctx, rdf, name, b.budget)
}

// readDirChunked lists dir by pulling entries from rdf in globDirChunk-sized
// pieces, charging the running total to budget after each one and returning
// its refusal the moment it trips rather than waiting for the rest of the
// directory to come back. A single unchunked ReadDir(-1) call — the shape
// both os.DirFS's ReadDir and a raw *os.File's ReadDir take — materializes a
// directory's entire contents before the entry budget or match cap ever get
// a say, which is exactly what lets one huge directory OOM the walk no
// matter how tightly the other bounds are set. The pieces are sorted by name
// only once the whole read is done, not per chunk: a listing that stays
// under the bound has to come back byte-for-byte what os.ReadDir would have
// produced, because the match cap downstream truncates to a deterministic,
// lexically-first prefix of it — sorting mid-stream would let a chunk
// boundary that fell in a different place change which entries end up in
// that prefix, and the raw fd-backed listing secureDirFS builds this on top
// of has no ordering guarantee of its own to begin with.
//
// ctx is checked between chunks because cancelFS cannot help here: it guards
// Open, ReadDir and Stat at the filesystem level, so it tests the context
// once as a listing begins and never again while this loop holds the
// directory handle. See boundedDirFS.ctx and secureDirFS.ctx for how it is
// threaded this far down.
func readDirChunked(ctx context.Context, rdf fs.ReadDirFile, dir string, budget *GlobBudget) ([]fs.DirEntry, error) {
	var entries []fs.DirEntry
	for {
		// cancelFS guards Open, ReadDir and Stat, so it checks ctx once as a
		// listing starts and never again; this loop then holds the directory
		// handle for as many chunks as the entry cap allows. Checking here is
		// what keeps a cancelled call from reading the rest of a huge
		// directory nobody is waiting for any more, and it answers with the
		// same cancellation the rest of the walk returns.
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		chunk, err := rdf.ReadDir(globDirChunk)
		entries = append(entries, chunk...)
		if berr := budget.tooManyEntries(dir, len(entries)); berr != nil {
			return nil, berr
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}
		if len(chunk) == 0 {
			// Defensive: a well-behaved ReadDirFile signals the end with
			// io.EOF, but nothing here should trust that every fs.FS does, and
			// a chunk that reads nothing without an error would otherwise spin
			// forever.
			break
		}
	}
	// A cancellation that landed on the final chunk must not come back as a
	// complete listing, so the answer is the cancellation however the loop
	// happened to leave it.
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	slices.SortFunc(entries, func(x, y fs.DirEntry) int { return strings.Compare(x.Name(), y.Name()) })
	if berr := budget.holdEntries(dir, len(entries)); berr != nil {
		return nil, berr
	}
	return entries, nil
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
	budget *GlobBudget
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
		// A per-directory entry-budget refusal has to reach the caller
		// looking like itself, the same as the listing-count refusal admit
		// already returns unhidden: folding it into the generic fs.ErrNotExist
		// below would make doublestar quietly skip the very directory that
		// tripped the bound instead of ending the walk with the reason why.
		if _, refused := errors.AsType[*globBudgetError](err); refused {
			return nil, err
		}
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
		if isGlobWalkAncestor(d.name, name) {
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
// all of them before the caller gets a chance to see any. It also resets the
// budget's live-entry tracking right before this walk starts (see
// GlobBudget.resetLive): this is a fresh traversal over fsys, so it must not
// inherit directories an earlier traversal sharing this budget — ignore
// discovery, or an earlier pattern in this same call — recorded as held open
// before it finished with them.
//
// GlobWalk rather than Glob: it ends the walk the moment the callback returns
// an error, so a cancellation — or the match cap tripping — that lands
// between two filesystem calls stops the walk at the next match instead of
// being noticed only after the tree runs out.
func globMatches(ctx context.Context, fsys fs.FS, pattern string, budget *GlobBudget) ([]string, error) {
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
	budget.resetLive()
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
