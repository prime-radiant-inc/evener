// Package testredundancy finds tests a package can lose without losing
// behavior coverage, and assembles the proof docs/developing-evener/testing.md
// ("Deleting and Consolidating Tests") asks a deletion PR to carry. It is the
// library behind `evener dev test-redundancy`.
//
// Profiles name files by import path (`primeradiant.com/evener/agent/x.go`),
// whatever -trimpath does to the binary, so blocks are compared by those names
// and mapped to package-relative files only for display and mutation.
package testredundancy

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
	"maps"
	"math/bits"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"primeradiant.com/evener/internal/devtool/covstmt"
)

// BlockRef is one coverage block: its position and statement count.
type BlockRef struct {
	File                                 string
	StartLine, StartCol, EndLine, EndCol int
	Stmts                                int
}

func (b BlockRef) String() string {
	return fmt.Sprintf("%s:%d.%d-%d.%d (%d)", b.File, b.StartLine, b.StartCol, b.EndLine, b.EndCol, b.Stmts)
}

// blockKey is a block's identity: its position without the statement count.
type blockKey struct {
	file                                 string
	startLine, startCol, endLine, endCol int
}

// Profile is one parsed coverage profile: the blocks the run reached, with
// their statement counts, and the statement total of every block it lists.
// Only reached blocks are kept, so a thousand single-test profiles of a large
// package stay small.
type Profile struct {
	covered map[blockKey]int
	total   int
}

// ParseProfile reads a Go coverage profile through covstmt, the repo's one
// profile parser, so dedup and any-hit union match every other coverage count.
func ParseProfile(r io.Reader) (Profile, error) {
	blocks, err := covstmt.BlocksReader(r)
	if err != nil {
		return Profile{}, err
	}
	p := Profile{covered: map[blockKey]int{}}
	for _, b := range blocks {
		p.total += b.StmtCount
		if b.Covered {
			p.covered[blockKey{b.File, b.StartLine, b.StartCol, b.EndLine, b.EndCol}] = b.StmtCount
		}
	}
	return p, nil
}

// LoadProfile parses the profile at path.
func LoadProfile(path string) (Profile, error) {
	f, err := os.Open(path)
	if err != nil {
		return Profile{}, err
	}
	defer func() { _ = f.Close() }()
	p, err := ParseProfile(f)
	if err != nil {
		return Profile{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

// Total is the number of statements the profile lists.
func (p Profile) Total() int { return p.total }

// Covered is the number of statements the run reached.
func (p Profile) Covered() int {
	n := 0
	for _, stmts := range p.covered {
		n += stmts
	}
	return n
}

// coveredWhere lists, in position order, the reached blocks keep accepts, with
// their statement total.
func (p Profile) coveredWhere(keep func(blockKey) bool) ([]BlockRef, int) {
	var refs []BlockRef
	total := 0
	for k, stmts := range p.covered {
		if !keep(k) {
			continue
		}
		refs = append(refs, BlockRef{File: k.file, StartLine: k.startLine, StartCol: k.startCol, EndLine: k.endLine, EndCol: k.endCol, Stmts: stmts})
		total += stmts
	}
	slices.SortFunc(refs, func(a, b BlockRef) int {
		return cmp.Or(
			strings.Compare(a.File, b.File),
			cmp.Compare(a.StartLine, b.StartLine),
			cmp.Compare(a.StartCol, b.StartCol),
			cmp.Compare(a.EndLine, b.EndLine),
			cmp.Compare(a.EndCol, b.EndCol),
		)
	})
	return refs, total
}

// coveredMinus lists the blocks p reached and other did not.
func (p Profile) coveredMinus(other Profile) ([]BlockRef, int) {
	return p.coveredWhere(func(k blockKey) bool {
		_, hit := other.covered[k]
		return !hit
	})
}

// Unique is what one candidate test alone reaches in its package. SharedStmts
// counts the statements it reaches that only other candidates also reach:
// deleting every candidate loses those too. Assertions counts its assertions
// (see Assertions).
type Unique struct {
	Name        string
	Stmts       int
	Blocks      []BlockRef
	SharedStmts int
	Assertions  int
}

// UniqueCoverage reports, for each candidate, the statements it reaches that
// neither rest (a run of every other test in the package) nor any other
// candidate reaches, and the statements it shares only with other candidates.
// Rows come back fewest unique statements first, then by name.
func UniqueCoverage(candidates map[string]Profile, rest Profile) []Unique {
	reachedBy := map[blockKey]int{}
	for _, p := range candidates {
		for k := range p.covered {
			reachedBy[k]++
		}
	}
	names := slices.Sorted(maps.Keys(candidates))
	out := make([]Unique, 0, len(names))
	for _, name := range names {
		onlyCandidates := func(want func(int) bool) func(blockKey) bool {
			return func(k blockKey) bool {
				_, inRest := rest.covered[k]
				return !inRest && want(reachedBy[k])
			}
		}
		blocks, stmts := candidates[name].coveredWhere(onlyCandidates(func(n int) bool { return n == 1 }))
		_, shared := candidates[name].coveredWhere(onlyCandidates(func(n int) bool { return n > 1 }))
		out = append(out, Unique{Name: name, Stmts: stmts, Blocks: blocks, SharedStmts: shared})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Stmts < out[j].Stmts })
	return out
}

// Pair is two candidates whose coverage overlaps. Containment is the share of
// Removable's covered blocks that Keeper also covers; AssertSimilarity is the
// Jaccard similarity of their normalized assertions.
type Pair struct {
	Removable, Keeper string
	Containment       float64
	AssertSimilarity  float64
}

// RankPairs compares every two candidates that share a covered block and
// orients each pair so Removable is the test the other contains more of.
// Pairs are ordered by containment, then assertion similarity: a pair at 1 and
// 1 runs the same code and checks the same things.
func RankPairs(cov map[string]Profile, asserts map[string][]string) []Pair {
	names := slices.Sorted(maps.Keys(cov))

	// Blocks become bit positions so each pair costs a few hundred word ANDs.
	index := map[blockKey]int{}
	for _, name := range names {
		for k := range cov[name].covered {
			if _, ok := index[k]; !ok {
				index[k] = len(index)
			}
		}
	}
	words := (len(index) + 63) / 64
	sets := make([][]uint64, len(names))
	assertSets := make([]map[string]bool, len(names))
	for i, name := range names {
		sets[i] = make([]uint64, words)
		for k := range cov[name].covered {
			bit := index[k]
			sets[i][bit/64] |= 1 << (bit % 64)
		}
		assertSets[i] = map[string]bool{}
		for _, a := range asserts[name] {
			assertSets[i][a] = true
		}
	}

	var pairs []Pair
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			shared := 0
			for w := range words {
				shared += bits.OnesCount64(sets[i][w] & sets[j][w])
			}
			if shared == 0 {
				continue
			}
			small, big := i, j
			if len(cov[names[j]].covered) < len(cov[names[i]].covered) {
				small, big = j, i
			}
			pairs = append(pairs, Pair{
				Removable:        names[small],
				Keeper:           names[big],
				Containment:      float64(shared) / float64(len(cov[names[small]].covered)),
				AssertSimilarity: jaccard(assertSets[i], assertSets[j]),
			})
		}
	}
	sort.SliceStable(pairs, func(a, b int) bool {
		if pairs[a].Containment != pairs[b].Containment {
			return pairs[a].Containment > pairs[b].Containment
		}
		return pairs[a].AssertSimilarity > pairs[b].AssertSimilarity
	})
	return pairs
}

func jaccard(a, b map[string]bool) float64 {
	shared := 0
	for s := range a {
		if b[s] {
			shared++
		}
	}
	union := len(a) + len(b) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// failureCalls are the testing.TB methods that fail a test.
var failureCalls = map[string]bool{"Error": true, "Errorf": true, "Fatal": true, "Fatalf": true, "Fail": true, "FailNow": true}

// Assertions reads the top-level Test functions in dir's _test.go files and
// returns each one's assertions: the condition of every if statement whose
// body fails the test, and every call that passes the test's *testing.T to a
// helper. Literals are normalized to _ so checks against different values
// compare equal.
func Assertions(dir string) (map[string][]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := map[string][]string{}
	for _, path := range files {
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
				continue
			}
			out[fn.Name.Name] = funcAssertions(fset, fn)
		}
	}
	return out, nil
}

func funcAssertions(fset *token.FileSet, fn *ast.FuncDecl) []string {
	// Normalize literals in place: this AST is parsed for this read only.
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok {
			lit.Value = "_"
		}
		return true
	})
	tbNames := map[string]bool{}
	for _, field := range fn.Type.Params.List {
		for _, name := range field.Names {
			tbNames[name.Name] = true
		}
	}
	var out []string
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt:
			if failsTest(n.Body) {
				out = append(out, "if "+render(fset, n.Cond))
			}
		case *ast.CallExpr:
			for _, arg := range n.Args {
				if id, ok := arg.(*ast.Ident); ok && tbNames[id.Name] {
					out = append(out, render(fset, n))
					break
				}
			}
		}
		return true
	})
	return out
}

func failsTest(body *ast.BlockStmt) bool {
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			// A zero-argument Error is error.Error(), not testing.TB.Error.
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && failureCalls[sel.Sel.Name] && (sel.Sel.Name != "Error" || len(call.Args) > 0) {
				found = true
			}
		}
		return !found
	})
	return found
}

func render(fset *token.FileSet, n ast.Node) string {
	var b strings.Builder
	_ = printer.Fprint(&b, fset, n)
	return b.String()
}
