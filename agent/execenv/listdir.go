package execenv

import (
	"context"
	"io/fs"
	"os"
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
// RETAIN — the page window — before it stops and reports partial results.
// It bounds memory, not work: the entries a deep page skips ahead of its
// window are charged and discarded, so an offset past this cap stays
// reachable (the prefix costs work, never memory). The page-shaped
// counterpart of glob's maxGlobMatches, sized at the same order: a default
// list_dir page is 1000 entries with a 16k-char rendering budget that
// usually binds first, so 10,000 retained entries is about ten pages of
// headroom, while the whole-machine walk the issue cites (1-5M entries) must
// stop. At roughly 120 bytes a DirEntry the retained window stays near
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
	// skip is how many entries the walk charges and discards before the
	// page window starts: the page's offset, so deep pages cost their prefix
	// as work but never as memory.
	skip int
	// window is how many entries the walk retains: the page's limit plus one
	// lookahead that proves more entries exist, clamped to
	// maxListDirWalkEntries.
	window int
	// charBudget bounds the rendered size of the retained window (0
	// disables the bound); the walk stops one retained entry past the point
	// the page fills it, which is exactly where the rendered page cuts.
	charBudget int
	// usedChars is the retained window's rendered size so far.
	usedChars int
	// charFull marks that the character bound's lookahead was retained.
	charFull bool
	// held counts the entries retained in the window so far.
	held int
	// visited counts every entry the walk charged, skipped or retained. For
	// a walk that enumerated the whole subtree it is the subtree's
	// exhaustive total; for one the need stopped early it is the visit
	// position where the walk stopped.
	visited int
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
// entries wanted, and the rendered character budget the page must fit (0
// disables the character bound). The walk retains limit+1 entries — the page
// plus one lookahead that distinguishes "exactly limit entries exist" from
// "more exist" — clamped to maxListDirWalkEntries; the offset prefix is
// charged, never held. Normalization (negative offset to 0, limit below 1 to
// 1) mirrors the tool's argument parsing.
func NewDirListBudget(offset, limit, charBudget int) *DirListBudget {
	if offset < 0 {
		offset = 0
	}
	if limit < 1 {
		limit = 1
	}
	window := maxListDirWalkEntries
	if limit < maxListDirWalkEntries {
		window = limit + 1
	}
	if charBudget < 0 {
		charBudget = 0
	}
	return &DirListBudget{glob: newGlobBudget("list_dir"), skip: offset, window: window, charBudget: charBudget}
}

// TruncatedAt reports the visit position where the walk stopped short of the
// whole requested subtree — skipped prefix plus retained window — or 0 when
// the walk enumerated everything: the same convention glob's budget reports
// through, so a caller can surface the value to the model without duplicating
// the truncated check at every call site.
func (b *DirListBudget) TruncatedAt() int {
	if b.walkStopped {
		return b.visited
	}
	return 0
}

// Visited reports how many entries the walk charged in total. For a walk
// that enumerated the whole subtree this is the subtree's exhaustive total,
// which is what the footer's "of N" owes the model; for one the need stopped
// early it is the same partial visit position TruncatedAt reports.
func (b *DirListBudget) Visited() int { return b.visited }

// room reports whether the walk may retain one more entry. When it returns
// false the walk is over: the page it was collecting for is complete and its
// lookahead is in hand.
func (b *DirListBudget) room() bool {
	return b.held < b.window && !b.charFull
}

// discard charges one visited entry and reports whether it belongs to the
// skipped prefix ahead of the page window: the offset entries before the
// page cost their work but never their memory.
func (b *DirListBudget) discard() bool {
	b.visited++
	return b.visited <= b.skip
}

// collect retains one entry in the page window and reports whether the
// window is now full: the page complete plus its lookahead, under whichever
// bound fills first — the entry window, or the rendered size the page may
// occupy. The entry that crosses the character bound is the lookahead: the
// rendered page ends before it, exactly where paginateDirEntries will cut,
// so the walk and the footer agree on what was shown.
func (b *DirListBudget) collect(de DirEntry) bool {
	b.held++
	if b.charBudget > 0 && !b.charFull {
		sz := DirEntryRenderSize(de)
		if b.held == 1 || b.usedChars+sz <= b.charBudget {
			b.usedChars += sz
		} else {
			b.charFull = true
		}
	}
	return b.held >= b.window || b.charFull
}

// stopWalk records that the page need, not the subtree's end, ended the
// walk, so TruncatedAt reports it and the caller knows the totals are
// partial.
func (b *DirListBudget) stopWalk() {
	b.walkStopped = true
}

// DirEntryRenderSize over-estimates an entry's rendered line length the way
// a list_dir page renders it — the name as OneLinePath writes it, an optional
// type sigil and tab-separated size, and a newline — so a budgeted walk can
// stop collecting at the same character bound the rendered page will
// enforce. The agent package's dirEntrySize delegates here so the collector
// and the renderer can never drift apart.
func DirEntryRenderSize(e DirEntry) int {
	return len(OneLinePath(e.Name)) + 16
}

// listDirListingName names a directory listing for the budget's live-entry
// accounting: the walk root comes through with an empty prefix, which the
// ancestor reasoning reads as ".", the convention the glob walkers charge
// under (isGlobWalkAncestor treats "." as an ancestor of everything). The
// name is slash-form because that is what the ancestor recognition matches
// on, so on Windows the walk's native prefixes cross through
// filepath.ToSlash here — for this key alone, never for the native paths
// that keep flowing to filesystem operations and the rendered entries.
func listDirListingName(relPrefix string) string {
	if relPrefix == "" {
		return "."
	}
	return filepath.ToSlash(relPrefix)
}

// listDirReadOS lists one directory in charged, cancellation-checked chunks
// through the shared chunked reader, over the raw descriptor the root open
// hands back. The walk reads OS paths instead of going through an fs.FS
// because a directory name that is not valid UTF-8 is a legal Linux filename
// that io/fs path validation rejects — a recursive listing over such a tree
// must traverse it the way the legacy walk's raw paths did, not error on the
// name and discard the page. dir names the listing for the budget's
// accounting (see listDirListingName); the descriptor's own name is the
// absolute path, so its entries' Info resolves against real paths.
//
// The context is checked before the open because a listing that never starts
// has no chunk for the reader's own checks to land in, and the open itself is
// where an unfenced one hangs: a readerless FIFO at the requested path blocks
// the open until a writer appears. The open is directory-fenced
// (listDirOpenDir), so a FIFO fails ENOTDIR promptly instead.
func listDirReadOS(ctx context.Context, absPath, dir string, budget *GlobBudget) ([]fs.DirEntry, error) {
	if cerr := ctx.Err(); cerr != nil {
		return nil, cerr
	}
	f, err := listDirOpenDir(absPath)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return readDirChunked(ctx, f, dir, budget)
}

// listDirRead is the seam over one directory's listing, a variable so tests
// can interpose on the walk — count its listings, or cancel at a chosen one.
var listDirRead = listDirReadOS

// listDirWalk collects DirEntry values beneath absDir, recursing up to depth
// levels in the legacy walk's order: each directory's entries sorted by name,
// a subdirectory's descendants following it immediately (preorder DFS). prefix
// is the caller-facing relative path its entries are named with ("" at the
// root).
//
// The walk stops — unwinding without error — the moment the budget's page
// need is met, which is the fix for #2375's unbounded walk: a small page
// costs work proportional to the page plus one entry, not the subtree — the
// entries an offset skips are charged and discarded rather than held, so
// deep pages stay reachable without paying memory for their prefix, and the
// subdirectory whose entry fills the need is never opened, so a huge one
// beyond the page cannot fail a call whose page is already valid.
// Cancellation outranks the cap, matching glob's convention, and every entry
// passes the glob budget's per-directory and live-entry bounds on its way in.
func listDirWalk(ctx context.Context, absDir, prefix string, depth int, out *[]DirEntry, budget *DirListBudget) error {
	if berr := budget.glob.listing(true); berr != nil {
		return berr
	}
	ents, err := listDirRead(ctx, absDir, listDirListingName(prefix), budget.glob)
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
		// A discarded entry is charged but never assembled or retained; a
		// discarded DIRECTORY still descends, because its descendants are part
		// of the DFS sequence the skip counts through.
		name := ent.Name()
		relName := name
		if prefix != "" {
			relName = filepath.Join(prefix, name)
		}
		if !budget.discard() {
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
			if budget.collect(de) {
				// The window holds the page plus its lookahead; nothing beyond
				// it can be shown, so stop before opening any child.
				budget.stopWalk()
				return nil
			}
		}
		if ent.IsDir() && depth > 1 {
			if err := listDirWalk(ctx, filepath.Join(absDir, name), relName, depth-1, out, budget); err != nil {
				return err
			}
		}
	}
	return nil
}
