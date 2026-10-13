package execenv

import (
	"context"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// MaxListDirDepth caps the recursion depth a budgeted list_dir walk honors.
// Depth is model-supplied and was previously unbounded; the walk's
// entry-visit cap below already bounds the work a pathological depth can
// spend, so this clamp exists for the deep-but-sparse chain that stays under
// it: a listing meant to show project structure rarely wants more than a
// dozen levels, and the tool's own advice for a big tree is to narrow the
// path, not deepen the walk. Callers with a trusted, deeper need (the memory
// page walker) use the unbudgeted legacy walk.
const MaxListDirDepth = 16

// maxListDirWalkEntries caps how many entries one budgeted list_dir walk may
// collect before it stops and reports partial results — the page-shaped
// counterpart of glob's maxGlobMatches, sized at the same order: a default
// list_dir page is 1000 entries with a 16k-char rendering budget that
// usually binds first, so 10,000 collected entries is about ten pages of
// headroom, while the whole-machine walk the issue cites (1-5M entries) must
// stop. At roughly 120 bytes a DirEntry the accumulated slice stays near
// 1.2 MB, where the unbounded walk collected hundreds.
var maxListDirWalkEntries = 10_000

// DirListBudget bounds the work one list_dir page's walk may spend. It wraps
// the glob budget machinery — every listing it makes is chunked, charged and
// cancellation-checked through the same *GlobBudget the glob and native grep
// walkers use — and adds the page need: how many entries this page's walk
// must collect before it can stop, which is what keeps a small page from
// enumerating a whole subtree (#2375).
//
// A budget belongs to one call: like GlobBudget its counters are
// unsynchronized, so it must not be shared across goroutines or reused.
type DirListBudget struct {
	glob *GlobBudget
	// need is how many entries this walk collects before it stops: the
	// page's offset+limit, plus one so a walk that reaches need can report
	// that more entries exist rather than guessing, clamped to
	// maxListDirWalkEntries.
	need int
	// collected counts the entries appended so far.
	collected int
	// walkStopped marks that the need cap, not the subtree's end, stopped
	// the walk, so the listing is partial and its totals are lower bounds.
	walkStopped bool
	// listings counts the directories the walk has listed. Honest
	// accounting for tests to read through the package boundary; the work
	// bound on listings is glob's, charged through b.glob.
	listings int
	// onListing fires after each directory listing completes. Nil in
	// production; a test-only seam (docs/developing-evener/testing.md,
	// "Prove a Wait with a Signal, Not a Window") that lets a test cancel
	// the walk at a chosen listing, because the confined arm has no
	// filesystem seam to interpose a counting wrapper on.
	onListing func()
}

// NewDirListBudget constructs the budget for one page's walk from the page
// parameters the list_dir tool parsed: offset entries already shown, limit
// entries wanted. The walk collects offset+limit+1 entries at most — the +1
// distinguishes "exactly offset+limit entries exist" from "more exist" —
// clamped to maxListDirWalkEntries so a huge offset or limit cannot turn a
// page request into an unbounded walk. Normalization (negative offset to 0,
// limit below 1 to 1) mirrors the tool's argument parsing.
func NewDirListBudget(offset, limit int) *DirListBudget {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 1
	}
	need := maxListDirWalkEntries
	if offset < maxListDirWalkEntries && limit < maxListDirWalkEntries-offset {
		need = offset + limit + 1
	}
	return &DirListBudget{glob: newGlobBudget("list_dir"), need: need}
}

// TruncatedAt reports the entry-visit cap that stopped the walk short of the
// whole requested subtree, or 0 when the walk enumerated everything — the
// same convention glob's budget reports through, so a caller can surface the
// value to the model without duplicating the truncated check at every call
// site.
func (b *DirListBudget) TruncatedAt() int {
	if b.walkStopped {
		return b.need
	}
	return 0
}

// room reports whether the walk may collect one more entry. When it returns
// false the walk is over: the page it was collecting for can be answered.
func (b *DirListBudget) room() bool {
	return b.collected < b.need
}

// collect charges one collected entry against the page need.
func (b *DirListBudget) collect() {
	b.collected++
}

// stopWalk records that the need cap, not the subtree's end, ended the walk,
// so TruncatedAt reports it and the caller knows the totals are partial.
func (b *DirListBudget) stopWalk() {
	b.walkStopped = true
}

// listDirListingName names a directory listing for the budget's live-entry
// accounting: the walk root comes through with an empty prefix, which the
// ancestor reasoning reads as ".", the convention the glob walkers charge
// under (isGlobWalkAncestor treats "." as an ancestor of everything).
func listDirListingName(relPrefix string) string {
	if relPrefix == "" {
		return "."
	}
	return relPrefix
}

// listDirBaseFS opens the filesystem the off-sandbox list_dir walk lists,
// rooted at the resolved base directory — the same shape, and for the same
// reason, as globBaseFS: a variable so tests can drive the exported entry
// point over a filesystem they control, while every listing that reaches the
// walk is chunked and charged through the glob budget machinery no matter
// what observes it from above.
var listDirBaseFS = func(ctx context.Context, dir string, budget *GlobBudget) fs.FS {
	return boundedDirFS{FS: os.DirFS(dir), budget: budget, ctx: ctx}
}

// listDirWalk collects DirEntry values beneath fsys — a cancelFS wrapping a
// boundedDirFS on the off-sandbox arm, so every listing is read in chunks and
// charged to the budget by the shared reader — recursing up to depth levels
// in the legacy walk's order: each directory's entries sorted by name, a
// subdirectory's descendants following it immediately (preorder DFS). rel is
// the fs path of the directory being listed; prefix is the caller-facing
// relative path its entries are named with ("." and "" at the root).
//
// The walk stops — unwinding without error — the moment the budget's page
// need is met, which is the fix for #2375's unbounded walk: a small page
// costs work proportional to the page plus one entry, not the subtree.
// Cancellation outranks the cap, matching glob's convention, and every entry
// passes the glob budget's per-directory and live-entry bounds on its way in.
func listDirWalk(ctx context.Context, fsys fs.FS, rel, prefix string, depth int, out *[]DirEntry, budget *DirListBudget) error {
	if berr := budget.glob.listing(true); berr != nil {
		return berr
	}
	ents, err := fs.ReadDir(fsys, rel)
	if err != nil {
		return err
	}
	budget.listings++
	if budget.onListing != nil {
		budget.onListing()
	}
	for _, ent := range ents {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if !budget.room() {
			budget.stopWalk()
			return nil
		}
		name := ent.Name()
		relName := name
		if prefix != "" {
			relName = filepath.Join(prefix, name)
		}
		de := DirEntry{Name: relName, IsDir: ent.IsDir()}
		if ent.Type()&os.ModeSymlink != 0 {
			de.IsSymlink = true
		}
		if !ent.IsDir() {
			if info, ierr := ent.Info(); ierr == nil {
				de.Size = info.Size()
				de.ModTime = info.ModTime()
				de.IsRegular = info.Mode().IsRegular()
				if info.Mode()&0o111 != 0 {
					de.IsExec = true
				}
			}
		}
		*out = append(*out, de)
		budget.collect()
		if ent.IsDir() && depth > 1 {
			if err := listDirWalk(ctx, fsys, path.Join(rel, name), relName, depth-1, out, budget); err != nil {
				return err
			}
		}
	}
	return nil
}
