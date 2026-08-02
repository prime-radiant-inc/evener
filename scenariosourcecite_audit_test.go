package serf_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// scenarioGoCitation matches a card pointing at a place inside a Go file: a
// backticked path carrying an anchor, either the `#symbol` this convention
// prefers or the `:line` hint it replaces.
//
// The anchor is what makes it a citation. A backticked path on its own is a
// mention — "relocated from the deleted `security.go`", "the package was
// extracted out of `cmd/serf-tui/pending.go`" — and naming a file that is gone
// is the whole point of those sentences, so they are not checked.
var scenarioGoCitation = regexp.MustCompile("`([A-Za-z0-9._/-]+\\.go)(?:#([A-Za-z0-9_.]+)|:([0-9][0-9,-]*))`")

// TestScenarioSourceCitationsResolve keeps a card's pointer into Go code
// attached to a file that still exists. Kata 2mzk's audit deliberately exempts
// source paths from the no-line-numbers rule, because code has no headings to
// anchor to; that exemption said nothing about whether the paths were right.
// Kata ypwb sampled four and found three stale, and a whole-file rename or
// package move breaks every citation into it at once while every existing
// audit stays green.
//
// Cards abbreviate a path down to the part that reads well
// (`internal/hubcore/tree.go` for `cmd/serf-hub/internal/hubcore/tree.go`), so
// resolution is by path SUFFIX against the tree. That still catches the failure
// that matters — the file moved out from under the citation, or was renamed,
// or never existed under that name.
func TestScenarioSourceCitationsResolve(t *testing.T) {
	byBase := scenarioGoFilesByBase(t)
	var findings []string
	resolved := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioGoCitation.FindAllStringSubmatch(line, -1) {
				cited := m[1]
				if len(scenarioResolveGoPath(byBase, cited)) == 0 {
					findings = append(findings, path+":"+strconv.Itoa(i+1)+
						": `"+cited+"` names no Go file in the tree: "+strings.TrimSpace(line))
					continue
				}
				resolved++
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. Cards cite Go code by the hundred today, so zero resolutions means
	// the citation needle broke, not that the citations left.
	if resolved == 0 {
		t.Fatalf("the Go-citation needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card that points into a Go file must name a file "+
			"that is still there — a package move or a rename silently turns "+
			"every citation into it into a pointer at nothing, and no other "+
			"audit sees it (kata ypwb). Repoint at the file that carries the "+
			"code now, or drop the anchor if the sentence is deliberately "+
			"naming something deleted:\n%s", strings.Join(findings, "\n"))
	}
}

// TestScenarioSourceSymbolsAreDeclared keeps the `#symbol` half of a Go
// citation resolvable. `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`
// survives every edit to that file — which is the entire reason kata ypwb moved
// the corpus off `:12` — but only until the symbol is renamed or moved to
// another file, and nothing else in the suite would notice that.
//
// A symbol is anything a card legitimately anchors to: a func or method, a
// type, a package-level or grouped const or var, a struct field, an interface
// method. Receiver-qualified names such as `Type.Method` are preserved; an
// unqualified alias remains available only when that name is unique in the
// file, so a collision cannot make a citation appear to resolve by accident.
func TestScenarioSourceSymbolsAreDeclared(t *testing.T) {
	byBase := scenarioGoFilesByBase(t)
	declared := map[string]map[string]bool{}
	var findings []string
	checked := 0
	for _, path := range scenarioCardFiles(t) {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			for _, m := range scenarioGoCitation.FindAllStringSubmatch(line, -1) {
				cited, symbol := m[1], m[2]
				if symbol == "" {
					continue
				}
				checked++
				found := false
				for _, file := range scenarioResolveGoPath(byBase, cited) {
					names, err := scenarioDeclarationsIn(declared, file)
					if err != nil {
						t.Fatalf("parsing %s cited by %s: %v", file, path, err)
					}
					if names[symbol] {
						found = true
						break
					}
				}
				if !found {
					findings = append(findings, path+":"+strconv.Itoa(i+1)+
						": `"+cited+"` declares no "+symbol+": "+strings.TrimSpace(line))
				}
			}
		}
	}
	// A corpus audit is green either because the corpus is clean or because its
	// needle stopped matching anything; only a floor on matches tells the two
	// apart. The corpus carries symbol anchors today, so zero checks means the
	// `#symbol` needle broke, not that the anchors left.
	if checked == 0 {
		t.Fatalf("the `#symbol` needle matched nothing across the corpus — " +
			"the detector is dead and this audit is checking nothing")
	}
	if len(findings) > 0 {
		sort.Strings(findings)
		t.Fatalf("a scenario card's `file.go#symbol` anchor must name something "+
			"that file still declares — a rename or a move to another file "+
			"leaves the anchor parsing fine and pointing at nothing, which is "+
			"the failure the line numbers it replaced had by construction "+
			"(kata ypwb). Repoint at the file and symbol that carry the code "+
			"now:\n%s", strings.Join(findings, "\n"))
	}
}

func TestScenarioDeclarationsPreserveReceiverQualification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "methods.go")
	const source = `package fixture

type Alpha struct {
	Field int
}
type Beta struct {
	Field int
}
type Contract interface {
	Do()
}

func (Alpha) Run() {}
func (*Beta) Run() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	names, err := scenarioDeclarationsIn(map[string]map[string]bool{}, path)
	if err != nil {
		t.Fatalf("scenarioDeclarationsIn: %v", err)
	}
	for _, want := range []string{"Alpha.Run", "Beta.Run", "Alpha.Field", "Contract.Do"} {
		if !names[want] {
			t.Fatalf("declarations missing qualified method %q: %v", want, names)
		}
	}
	for _, ambiguous := range []string{"Run", "Field"} {
		if names[ambiguous] {
			t.Fatalf("ambiguous unqualified declaration %q was accepted: %v", ambiguous, names)
		}
	}
	if !names["Do"] {
		t.Fatalf("unique unqualified interface method was rejected: %v", names)
	}
}

func TestScenarioTrackedGoFilesByBaseIgnoresUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"tracked.go", "deleted.go", "untracked.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "tracked.go", "deleted.go"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatalf("remove deleted fixture: %v", err)
	}

	byBase, err := scenarioTrackedGoFilesByBase(root)
	if err != nil {
		t.Fatalf("scenarioTrackedGoFilesByBase: %v", err)
	}
	if got := byBase["tracked.go"]; len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("tracked Go files = %v, want tracked.go only", got)
	}
	if _, ok := byBase["untracked.go"]; ok {
		t.Fatalf("untracked Go file appeared in tracked index: %v", byBase["untracked.go"])
	}
	if _, ok := byBase["deleted.go"]; ok {
		t.Fatalf("deleted Go file appeared in checkout index: %v", byBase["deleted.go"])
	}
}

// scenarioDeclarationsIn returns every name a Go file declares — funcs and
// methods, types, package-level and grouped consts and vars, struct fields,
// interface methods — memoized across citations into the same file.
func scenarioDeclarationsIn(cache map[string]map[string]bool, path string) (map[string]bool, error) {
	if names, ok := cache[path]; ok {
		return names, nil
	}
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	unqualifiedCounts := map[string]int{}
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			unqualifiedCounts[decl.Name.Name]++
			if receiver := scenarioReceiverName(decl.Recv); receiver != "" {
				names[receiver+"."+decl.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					unqualifiedCounts[spec.Name.Name]++
					scenarioAddMemberNames(names, unqualifiedCounts, spec.Name.Name, spec.Type)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						unqualifiedCounts[name.Name]++
					}
				}
			}
		}
	}
	for name, count := range unqualifiedCounts {
		if count == 1 {
			names[name] = true
		}
	}
	cache[path] = names
	return names, nil
}

// scenarioAddMemberNames records a struct type's field names and an interface
// type's method names; cards anchor to both. It records the qualified name
// immediately and defers the unqualified alias until its file-wide count is
// known, so same-named members cannot mask one another.
func scenarioAddMemberNames(names map[string]bool, unqualifiedCounts map[string]int, typeName string, typ ast.Expr) {
	var fields *ast.FieldList
	switch typ := typ.(type) {
	case *ast.StructType:
		fields = typ.Fields
	case *ast.InterfaceType:
		fields = typ.Methods
	default:
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			unqualifiedCounts[name.Name]++
			names[typeName+"."+name.Name] = true
		}
	}
}

// scenarioReceiverName returns the declared receiver type name, including the
// base type inside pointer and generic receiver expressions.
func scenarioReceiverName(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var receiverName func(ast.Expr) string
	receiverName = func(expr ast.Expr) string {
		switch expr := expr.(type) {
		case *ast.Ident:
			return expr.Name
		case *ast.StarExpr:
			return receiverName(expr.X)
		case *ast.IndexExpr:
			return receiverName(expr.X)
		case *ast.IndexListExpr:
			return receiverName(expr.X)
		case *ast.ParenExpr:
			return receiverName(expr.X)
		default:
			return ""
		}
	}
	return receiverName(fields.List[0].Type)
}

// scenarioTrackedGoFilesByBase indexes tracked Go files by base name, so a
// cited path suffix can be resolved without rewalking the tree per citation.
func scenarioTrackedGoFilesByBase(root string) (map[string][]string, error) {
	cmd := exec.Command("git", "-C", root, "ls-files", "-z", "--", "*.go")
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	byBase := map[string][]string{}
	for _, path := range strings.Split(string(raw), "\x00") {
		if path == "" {
			continue
		}
		path = filepath.ToSlash(path)
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat tracked Go file %s: %w", path, err)
		}
		byBase[filepath.Base(path)] = append(byBase[filepath.Base(path)], path)
	}
	return byBase, nil
}

// scenarioGoFilesByBase indexes tracked Go files in the worktree by base name.
func scenarioGoFilesByBase(t *testing.T) map[string][]string {
	t.Helper()
	byBase, err := scenarioTrackedGoFilesByBase(".")
	if err != nil {
		t.Fatalf("listing tracked Go files: %v", err)
	}
	return byBase
}

// scenarioResolveGoPath returns every Go file whose path is, or ends with, the
// cited path. Cards abbreviate, so `internal/hubcore/tree.go` must find
// `cmd/serf-hub/internal/hubcore/tree.go`; a bare `main.go` legitimately finds
// many, which is imprecise but not stale.
func scenarioResolveGoPath(byBase map[string][]string, cited string) []string {
	var out []string
	for _, candidate := range byBase[scenarioCitedBaseName(cited)] {
		if candidate == cited || strings.HasSuffix(candidate, "/"+cited) {
			out = append(out, candidate)
		}
	}
	return out
}

// scenarioCitedBaseName is the file name at the end of a citation path.
func scenarioCitedBaseName(cited string) string {
	if i := strings.LastIndexByte(cited, '/'); i >= 0 {
		return cited[i+1:]
	}
	return cited
}
