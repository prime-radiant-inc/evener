package copyonly

import (
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// testFunc is a top-level Go test function name.
var testFunc = regexp.MustCompile(`^Test\w+$`)

// copyComparers are the strings functions whose second argument is the text
// they look for.
var copyComparers = map[string]bool{"Contains": true, "HasPrefix": true, "HasSuffix": true, "EqualFold": true, "Index": true}

// diagnostics are the testing.T methods that report a failure or log; their
// arguments are messages, not inputs to the code under test.
var diagnostics = map[string]bool{
	"Fatal": true, "Fatalf": true, "Error": true, "Errorf": true,
	"Log": true, "Logf": true, "Skip": true, "Skipf": true,
}

func (s *scanner) scanGo(path, src string) ([]Target, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var targets []Target
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !testFunc.MatchString(fn.Name.Name) ||
			!copyPurposeName.MatchString(fn.Name.Name) {
			continue
		}
		checks := goChecks(fn.Body)
		if len(checks) == 0 {
			continue
		}
		supplied := suppliedText(fset, src, file, fn.Body, checks)
		allCopy := true
		for _, c := range checks {
			if !s.goCopyClaim(c, supplied) {
				allCopy = false
				break
			}
		}
		if allCopy {
			targets = append(targets, Target{File: path, Test: fn.Name.Name, Line: fset.Position(fn.Pos()).Line})
		}
	}
	return targets, nil
}

// goChecks collects the test's claims: every `if` condition and every
// require/assert call, closures included. `if err != nil` (with or without an
// initializer) is a setup guard after a call, not a claim; expecting an error
// (`err == nil`) is a claim.
func goChecks(body *ast.BlockStmt) []ast.Expr {
	var checks []ast.Expr
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.IfStmt:
			if !isErrNotNil(n.Cond) {
				checks = append(checks, n.Cond)
			}
		case *ast.CallExpr:
			if pkg, _ := selector(n.Fun); pkg == "require" || pkg == "assert" {
				checks = append(checks, n)
			}
		}
		return true
	})
	return checks
}

func isErrNotNil(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	return ok && b.Op == token.NEQ && isIdent(b.X, "err") && isIdent(b.Y, "nil")
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

// selector splits `x.Name` into ("x", "Name"); anything else gives "".
func selector(e ast.Expr) (string, string) {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return "", ""
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", ""
	}
	return x.Name, sel.Sel.Name
}

// suppliedText is the test body minus what carries no input to the code under
// test: the checks' conditions, comments, failure diagnostics, and subtest
// names. `if`
// initializers stay: `if got := render("x"); ...` supplies "x".
func suppliedText(fset *token.FileSet, src string, file *ast.File, body *ast.BlockStmt, checks []ast.Expr) string {
	type span struct{ lo, hi int }
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	var cut []span
	for _, c := range checks {
		cut = append(cut, span{off(c.Pos()), off(c.End())})
	}
	for _, cg := range file.Comments {
		if cg.Pos() > body.Lbrace && cg.End() < body.Rbrace {
			cut = append(cut, span{off(cg.Pos()), off(cg.End())})
		}
	}
	ast.Inspect(body, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok {
			recv, method := selector(call.Fun)
			if recv == "t" && diagnostics[method] {
				cut = append(cut, span{off(call.Pos()), off(call.End())})
				return false
			}
			// A subtest's name, like the test's own, supplies nothing.
			if recv == "t" && method == "Run" && len(call.Args) > 0 {
				cut = append(cut, span{off(call.Args[0].Pos()), off(call.Args[0].End())})
			}
		}
		return true
	})
	sort.Slice(cut, func(i, j int) bool { return cut[i].lo < cut[j].lo })
	var b strings.Builder
	at := off(body.Lbrace) + 1
	for _, c := range cut {
		if c.lo > at {
			b.WriteString(src[at:c.lo])
			b.WriteByte('\n')
		}
		at = max(at, c.hi)
	}
	b.WriteString(src[at:off(body.Rbrace)])
	return b.String()
}

// goCopyClaim reports a check that claims only that some copy appears: every
// && / || term compares text with a non-contract literal, nothing in it checks
// behavior, none of its literals is text the test supplied, and at least one
// literal is production copy.
func (s *scanner) goCopyClaim(cond ast.Expr, supplied string) bool {
	if checksBehavior(cond) {
		return false
	}
	for _, term := range conditionTerms(cond) {
		if !isCopyTerm(term) {
			return false
		}
	}
	isSupplied := false
	ast.Inspect(cond, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			// The raw source between the quotes, compared with raw source.
			raw := lit.Value[1 : len(lit.Value)-1]
			if utf8.RuneCountInString(raw) >= 3 && strings.Contains(supplied, raw) {
				isSupplied = true
			}
		}
		return !isSupplied
	})
	if isSupplied {
		return false
	}
	for _, lit := range copyLiterals(cond) {
		if !contractShaped(lit) && s.isGoCopy(lit) {
			return true
		}
	}
	return false
}

// isGoCopy reports a literal found in production Go source, or a phrase found
// in a production text asset.
func (s *scanner) isGoCopy(lit string) bool {
	if s.goProd.contains(lit) {
		return true
	}
	return utf8.RuneCountInString(lit) >= 8 && strings.Contains(lit, " ") && s.assets.contains(lit)
}

// checksBehavior reports a condition that checks something besides wording
// anywhere inside it: an expected error (`err == nil`), a length, a numeric
// comparison, or any other comparison that doesn't compare text with copy.
func checksBehavior(cond ast.Expr) bool {
	found := false
	ast.Inspect(cond, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			switch n.Op {
			case token.EQL, token.NEQ, token.LSS, token.GTR, token.LEQ, token.GEQ:
				// A comparison that is not a copy comparison checks behavior,
				// however deeply it is nested: `err == nil`, `status ==
				// expected`, a comparison with a number. So does one with a
				// number even when copy sits inside it
				// (`strings.Index(out, "Usage") == 0`).
				found = found || !isCopyTerm(n) || isNumber(n.X) || isNumber(n.Y)
			}
		case *ast.CallExpr:
			found = found || isIdent(n.Fun, "len")
		}
		return !found
	})
	return found
}

func isNumber(e ast.Expr) bool {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.SUB {
		e = u.X
	}
	lit, ok := ast.Unparen(e).(*ast.BasicLit)
	return ok && (lit.Kind == token.INT || lit.Kind == token.FLOAT)
}

// conditionTerms splits a condition at its && and || operators, looking
// through parentheses and negation: by De Morgan, !(a && b) is !a || !b, so
// it has the same terms as a && b, and a negated term asks about the same
// thing as the term itself.
func conditionTerms(cond ast.Expr) []ast.Expr {
	cond = ast.Unparen(cond)
	if u, ok := cond.(*ast.UnaryExpr); ok && u.Op == token.NOT {
		if terms := conditionTerms(u.X); len(terms) > 1 {
			return terms
		}
	}
	if b, ok := cond.(*ast.BinaryExpr); ok && (b.Op == token.LAND || b.Op == token.LOR) {
		return append(conditionTerms(b.X), conditionTerms(b.Y)...)
	}
	return []ast.Expr{cond}
}

// isCopyTerm reports a term that asks only about text: a text comparison
// against a literal that isn't a machine-contract token (`status !=
// "running"` checks state, not wording).
func isCopyTerm(term ast.Expr) bool {
	for _, lit := range copyLiterals(term) {
		if !contractShaped(lit) {
			return true
		}
	}
	return false
}

// copyLiterals returns the literals the expression compares text with:
// strings.Contains and its kin, == and != against a string, and
// regexp.MustCompile(...).MatchString (the regex's literal core).
func copyLiterals(e ast.Expr) []string {
	var lits []string
	ast.Inspect(e, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if pkg, fn := selector(n.Fun); pkg == "strings" && copyComparers[fn] && len(n.Args) == 2 {
				if lit, ok := stringLit(n.Args[1]); ok {
					lits = append(lits, strings.TrimSpace(lit))
				}
			}
			if sel, ok := n.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "MatchString" {
				if compile, ok := sel.X.(*ast.CallExpr); ok && len(compile.Args) == 1 {
					if pkg, fn := selector(compile.Fun); pkg == "regexp" && fn == "MustCompile" {
						if lit, ok := stringLit(compile.Args[0]); ok {
							lits = append(lits, strings.TrimSpace(regexCore(lit)))
						}
					}
				}
			}
		case *ast.BinaryExpr:
			if n.Op == token.EQL || n.Op == token.NEQ {
				for _, side := range []ast.Expr{n.X, n.Y} {
					if lit, ok := stringLit(side); ok {
						lits = append(lits, strings.TrimSpace(lit))
					}
				}
			}
		}
		return true
	})
	return lits
}

func stringLit(e ast.Expr) (string, bool) {
	lit, ok := ast.Unparen(e).(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	s, err := strconv.Unquote(lit.Value)
	return s, err == nil
}
