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
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io"
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

// Profile is one parsed coverage profile: every block it lists and whether
// the run reached it.
type Profile struct {
	stmts   map[blockKey]int
	covered map[blockKey]bool
}

// ParseProfile reads a Go coverage profile through covstmt, the repo's one
// profile parser, so dedup and any-hit union match every other coverage count.
func ParseProfile(r io.Reader) (Profile, error) {
	blocks, err := covstmt.BlocksReader(r)
	if err != nil {
		return Profile{}, err
	}
	p := Profile{stmts: make(map[blockKey]int, len(blocks)), covered: make(map[blockKey]bool)}
	for _, b := range blocks {
		k := blockKey{b.File, b.StartLine, b.StartCol, b.EndLine, b.EndCol}
		p.stmts[k] = b.StmtCount
		if b.Covered {
			p.covered[k] = true
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
func (p Profile) Total() int {
	n := 0
	for _, s := range p.stmts {
		n += s
	}
	return n
}

// Covered is the number of statements the run reached.
func (p Profile) Covered() int {
	n := 0
	for k := range p.covered {
		n += p.stmts[k]
	}
	return n
}

func (p Profile) ref(k blockKey) BlockRef {
	return BlockRef{File: k.file, StartLine: k.startLine, StartCol: k.startCol, EndLine: k.endLine, EndCol: k.endCol, Stmts: p.stmts[k]}
}

// coveredMinus lists the blocks p reached that none of others reached, in
// position order, with their statement total.
func (p Profile) coveredMinus(others ...Profile) ([]BlockRef, int) {
	var refs []BlockRef
	total := 0
	for k := range p.covered {
		if slices.ContainsFunc(others, func(o Profile) bool { return o.covered[k] }) {
			continue
		}
		refs = append(refs, p.ref(k))
		total += p.stmts[k]
	}
	sortRefs(refs)
	return refs, total
}

func sortRefs(refs []BlockRef) {
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.StartLine != b.StartLine {
			return a.StartLine < b.StartLine
		}
		return a.StartCol < b.StartCol
	})
}

// Unique is what one candidate test alone reaches in its package.
type Unique struct {
	Name   string
	Stmts  int
	Blocks []BlockRef
}

// UniqueCoverage reports, for each candidate, the statements it reaches that
// neither rest (a run of every other test in the package) nor any other
// candidate reaches. Rows come back fewest unique statements first, then by
// name: the head of the list is what can go without lowering coverage.
func UniqueCoverage(candidates map[string]Profile, rest Profile) []Unique {
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Unique, 0, len(names))
	for _, name := range names {
		others := []Profile{rest}
		for _, other := range names {
			if other != name {
				others = append(others, candidates[other])
			}
		}
		blocks, stmts := candidates[name].coveredMinus(others...)
		out = append(out, Unique{Name: name, Stmts: stmts, Blocks: blocks})
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
	names := make([]string, 0, len(cov))
	for name := range cov {
		names = append(names, name)
	}
	sort.Strings(names)

	// Blocks become bit positions so each pair costs a few hundred word ANDs.
	index := map[blockKey]int{}
	sets := make([][]uint64, len(names))
	for _, name := range names {
		for k := range cov[name].covered {
			if _, ok := index[k]; !ok {
				index[k] = len(index)
			}
		}
	}
	words := (len(index) + 63) / 64
	counts := make([]int, len(names))
	for i, name := range names {
		set := make([]uint64, words)
		for k := range cov[name].covered {
			bit := index[k]
			set[bit/64] |= 1 << (bit % 64)
		}
		sets[i] = set
		counts[i] = len(cov[name].covered)
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
			if counts[j] < counts[i] {
				small, big = j, i
			}
			pairs = append(pairs, Pair{
				Removable:        names[small],
				Keeper:           names[big],
				Containment:      float64(shared) / float64(counts[small]),
				AssertSimilarity: jaccard(asserts[names[i]], asserts[names[j]]),
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

func jaccard(a, b []string) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	inA := map[string]bool{}
	for _, s := range a {
		inA[s] = true
	}
	union := map[string]bool{}
	shared := 0
	for _, s := range b {
		if inA[s] && !union[s] {
			shared++
		}
		union[s] = true
	}
	for s := range inA {
		union[s] = true
	}
	return float64(shared) / float64(len(union))
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
			if sel, ok := call.Fun.(*ast.SelectorExpr); ok && failureCalls[sel.Sel.Name] {
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
