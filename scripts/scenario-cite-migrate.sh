#!/usr/bin/env bash
# scenario-cite-migrate.sh — move scenario-card citations of Go code off bare
# line numbers and onto symbol anchors: `agent/tree_counter.go:12` becomes
# `agent/tree_counter.go#defaultMaxConcurrentDelegateTurns`.
#
# A line number is invalidated by any edit above it, silently — the citation
# still parses, still looks precise, and now points at an unrelated statement.
# Kata ypwb sampled four of the corpus's Go citations and found three already
# stale. `file.go#Symbol` is the spelling scripts/run-fuzz.sh already uses for
# the same idea ("adapter.go#decodeStream") and TestScenarioSourceCitationsResolve
# keeps it honest.
#
# It rewrites only what TWO independent witnesses agree on:
#   * the card already names, in backticks on the citation's line or the line
#     above it, exactly one identifier that go/ast reports as declared in the
#     cited file (which must resolve to exactly one Go file in the tree), AND
#   * the cited line range still overlaps that symbol's declaration.
# Everything else is printed and left alone. One witness is not enough: the
# card's own prose picked `cmd/serf/main.go#run` for a sentence about there
# being no `run` SUBCOMMAND, and picked `appwire/types.go#TurnSteerParams` for
# a citation that pointed at `MethodTurnSteer`. Resolving a line number to
# whatever symbol currently encloses it is worse still — it launders a stale
# number into a confident-looking anchor, which is the failure this convention
# exists to stop.
#
# Citations with only the card's witness are listed as UNCONFIRMED. Those are
# where the staleness actually lives, and they need a human to say which symbol
# the sentence meant; --include-unconfirmed migrates them for a reviewed pass.
#
# Usage:
#   scripts/scenario-cite-migrate.sh            # report only; writes nothing
#   scripts/scenario-cite-migrate.sh --apply    # rewrite the confirmed ones
#   scripts/scenario-cite-migrate.sh --skips    # also print every citation left
#                                               # alone, with the reason
#   scripts/scenario-cite-migrate.sh --include-unconfirmed --apply
#                                               # also rewrite the single-witness
#                                               # ones; REVIEW THE DIFF
#
# Exits 0 whether or not anything was rewritten; a corpus with no migratable
# citations left is the goal state, not an error.
set -uo pipefail

apply=0
skips=0
unconfirmed=0
for arg in "$@"; do
	case "$arg" in
	--apply) apply=1 ;;
	--skips) skips=1 ;;
	--include-unconfirmed) unconfirmed=1 ;;
	-h | --help)
		sed -n '2,40p' "$0"
		exit 0
		;;
	*)
		echo "scenario-cite-migrate.sh: unknown flag $arg (try --help)" >&2
		exit 2
		;;
	esac
done

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
if ! work="$(mktemp -d -t serf-cite-migrate.XXXXXX)"; then
	echo "scenario-cite-migrate.sh: unable to create temporary directory" >&2
	exit 1
fi
trap 'rm -f "$work"/*.go; rmdir "$work"' EXIT

prog="$work/migrate.go"
cat >"$prog" <<'GO'
// Command migrate rewrites corroborated `file.go:line` citations in the
// scenario corpus to `file.go#Symbol`. See scenario-cite-migrate.sh.
package main

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
)

// citation is a card pointing into Go code by a single line or a single line
// range: the shape this migration exists to retire. A comma list
// (`x_test.go:31,228,241,58`) is deliberately excluded — it enumerates several
// places at once and no single symbol can stand for all of them.
var citation = regexp.MustCompile("`([A-Za-z0-9._/-]+\\.go):([0-9]+(?:-[0-9]+)?)`")

// backtickedIdentifier is how a card names a Go symbol in prose — `stopNestedOrLocal`,
// `Session.spawnAgent`. It is the only place the migration will look for an
// anchor, because a symbol the card did not name is a symbol the card did not
// mean.
var backtickedIdentifier = regexp.MustCompile("`([A-Za-z_][A-Za-z0-9_]*(?:\\.[A-Za-z_][A-Za-z0-9_]*)*)`")

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: migrate <repo-root> <apply> <report-skips> <include-unconfirmed>")
		os.Exit(2)
	}
	root := os.Args[1]
	apply, reportSkips, includeUnconfirmed := os.Args[2] == "true", os.Args[3] == "true", os.Args[4] == "true"
	if err := run(root, apply, reportSkips, includeUnconfirmed); err != nil {
		fmt.Fprintln(os.Stderr, "scenario-cite-migrate:", err)
		os.Exit(1)
	}
}

func run(root string, apply, reportSkips, includeUnconfirmed bool) error {
	byBase, err := indexGoFiles(root)
	if err != nil {
		return err
	}
	cards, err := cardFiles(root)
	if err != nil {
		return err
	}
	declared := map[string]declarationTable{}
	rewrote, skipped, unconfirmed := 0, 0, 0
	for _, card := range cards {
		raw, err := os.ReadFile(card)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, card)
		lines := strings.Split(string(raw), "\n")
		out := append([]string(nil), lines...)
		for i, line := range lines {
			for _, m := range citation.FindAllStringSubmatch(line, -1) {
				whole, cited := m[0], m[1]
				where := fmt.Sprintf("%s:%d", rel, i+1)
				files := resolve(byBase, cited)
				if len(files) != 1 {
					skipped++
					if reportSkips {
						fmt.Printf("SKIP %s %s: path resolves to %d Go files, not 1\n", where, whole, len(files))
					}
					continue
				}
				names, err := declarationsIn(declared, filepath.Join(root, files[0]))
				if err != nil {
					return err
				}
				anchors := corroboratedAnchors(lines, i, names)
				if len(anchors) != 1 {
					skipped++
					if reportSkips {
						fmt.Printf("SKIP %s %s: card names %d symbols declared in %s, not 1 %v\n",
							where, whole, len(anchors), files[0], anchors)
					}
					continue
				}
				// The line range still landing inside the symbol the card
				// named is the second, independent witness. Without it the
				// card's prose is the only evidence, and the corpus proves
				// that alone is not enough.
				replacement := "`" + cited + "#" + anchors[0] + "`"
				if !lineRangeIn(names[anchors[0]], m[2]) {
					unconfirmed++
					fmt.Printf("UNCONFIRMED %s %s -> %s (line range no longer inside %s)\n",
						where, whole, replacement, anchors[0])
					if !includeUnconfirmed {
						continue
					}
				}
				out[i] = strings.ReplaceAll(out[i], whole, replacement)
				rewrote++
				fmt.Printf("REWRITE %s %s -> %s\n", where, whole, replacement)
			}
		}
		if !apply {
			continue
		}
		joined := strings.Join(out, "\n")
		if joined == string(raw) {
			continue
		}
		if err := os.WriteFile(card, []byte(joined), 0o644); err != nil {
			return err
		}
	}
	verb := "would rewrite"
	if apply {
		verb = "rewrote"
	}
	fmt.Printf("---- %s %d citation(s); %d unconfirmed; left %d alone\n", verb, rewrote, unconfirmed, skipped)
	return nil
}

// corroboratedAnchors returns the distinct identifiers the card names in
// backticks at or before the citation — its own line and the line above it,
// because prose wraps and puts "(`readOutputImageFile`,\n`output_images.go:289-299`)"
// on two lines. The line BELOW is deliberately out of the window: it is
// usually the next bullet, whose symbol has nothing to do with this citation.
func corroboratedAnchors(lines []string, i int, names declarationTable) []string {
	window := lines[i]
	if i > 0 {
		window = lines[i-1] + "\n" + window
	}
	var anchors []string
	seen := map[string]bool{}
	for _, m := range backtickedIdentifier.FindAllStringSubmatch(window, -1) {
		candidate := m[1]
		if strings.HasSuffix(candidate, ".go") {
			continue
		}
		if len(names[candidate]) == 0 || seen[candidate] {
			continue
		}
		seen[candidate] = true
		anchors = append(anchors, candidate)
	}
	sort.Strings(anchors)
	return anchors
}

// span is the inclusive line range a declaration occupies.
type span struct{ from, to int }

// declarationTable maps each name a file declares to every place it is
// declared; a method name can appear on more than one receiver.
type declarationTable map[string][]span

// declarationsIn returns every name a Go file declares — funcs and methods,
// types, package-level and grouped consts and vars, struct fields, interface
// methods — with the lines each occupies, memoized across citations into the
// same file.
func declarationsIn(cache map[string]declarationTable, path string) (declarationTable, error) {
	if names, ok := cache[path]; ok {
		return names, nil
	}
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	names := declarationTable{}
	record := func(name string, node ast.Node) {
		names[name] = append(names[name], span{
			from: fset.Position(node.Pos()).Line,
			to:   fset.Position(node.End()).Line,
		})
	}
	for _, decl := range parsed.Decls {
		switch decl := decl.(type) {
		case *ast.FuncDecl:
			record(decl.Name.Name, decl)
			if receiver := receiverName(decl.Recv); receiver != "" {
				record(receiver+"."+decl.Name.Name, decl)
			}
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch spec := spec.(type) {
				case *ast.TypeSpec:
					record(spec.Name.Name, spec)
					recordMembers(record, spec.Name.Name, spec.Type)
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						record(name.Name, spec)
					}
				}
			}
		}
	}
	cache[path] = names
	return names, nil
}

// recordMembers records the field names of a struct type and the method names
// of an interface type; cards anchor to both.
func recordMembers(record func(string, ast.Node), typeName string, typ ast.Expr) {
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
			record(name.Name, field)
			record(typeName+"."+name.Name, field)
		}
	}
}

// receiverName returns the declared receiver type name, including the base
// type inside pointer and generic receiver expressions.
func receiverName(fields *ast.FieldList) string {
	if fields == nil || len(fields.List) == 0 {
		return ""
	}
	var name func(ast.Expr) string
	name = func(expr ast.Expr) string {
		switch expr := expr.(type) {
		case *ast.Ident:
			return expr.Name
		case *ast.StarExpr:
			return name(expr.X)
		case *ast.IndexExpr:
			return name(expr.X)
		case *ast.IndexListExpr:
			return name(expr.X)
		case *ast.ParenExpr:
			return name(expr.X)
		default:
			return ""
		}
	}
	return name(fields.List[0].Type)
}

// lineRangeIn reports whether a cited line or line range overlaps any place the
// symbol is declared — the second witness that a rewrite is right.
func lineRangeIn(spans []span, cited string) bool {
	from, to := cited, cited
	if dash := strings.IndexByte(cited, '-'); dash >= 0 {
		from, to = cited[:dash], cited[dash+1:]
	}
	first, err := strconv.Atoi(from)
	if err != nil {
		return false
	}
	last, err := strconv.Atoi(to)
	if err != nil {
		return false
	}
	for _, s := range spans {
		if first <= s.to && last >= s.from {
			return true
		}
	}
	return false
}

// indexGoFiles maps every tracked Go file by base name, so a cited path suffix
// resolves without rewalking and generated or ignored files cannot become
// migration targets accidentally.
func indexGoFiles(root string) (map[string][]string, error) {
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

// resolve returns every Go file whose path is, or ends with, the cited path;
// cards abbreviate a path down to the part that reads well.
func resolve(byBase map[string][]string, cited string) []string {
	base := cited
	if i := strings.LastIndexByte(cited, '/'); i >= 0 {
		base = cited[i+1:]
	}
	var out []string
	for _, candidate := range byBase[base] {
		if candidate == cited || strings.HasSuffix(candidate, "/"+cited) {
			out = append(out, candidate)
		}
	}
	return out
}

// cardFiles is the corpus the scenario audits read: every card plus the
// practices doc they all defer to.
func cardFiles(root string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(root, "test", "scenarios"))
	if err != nil {
		return nil, err
	}
	var cards []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			cards = append(cards, filepath.Join(root, "test", "scenarios", e.Name()))
		}
	}
	cards = append(cards, filepath.Join(root, "docs", "agentic-testing.md"))
	sort.Strings(cards)
	return cards, nil
}
GO

applystr=false
skipstr=false
unconfirmedstr=false
if [ "$apply" -eq 1 ]; then applystr=true; fi
if [ "$skips" -eq 1 ]; then skipstr=true; fi
if [ "$unconfirmed" -eq 1 ]; then unconfirmedstr=true; fi

(cd "$repo_root" && go run "$prog" "$repo_root" "$applystr" "$skipstr" "$unconfirmedstr")
