package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// envRow is one variable in the envvars registry.
type envRow struct {
	Name       string
	Visibility string
	DocRow     string
	Consumers  []string
}

// directRow is an environment variable read by name outside the registry.
type directRow struct {
	Name      string
	DocRow    string
	Consumers []string
}

// hubRow is one scalar hub.toml key.
type hubRow struct {
	Key       string
	Default   string
	DocRow    string
	Consumers []string
}

var (
	qualifiedRef = regexp.MustCompile(`\benvvars\.([A-Za-z0-9_]+)`)
	evenerName   = regexp.MustCompile(`"(EVENER_[A-Z0-9_]+)"`)
	getenvName   = regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z][A-Z0-9_]*)"\)`)
)

// registryEntry is a Var declared in envvars/envvars.go.
type registryEntry struct {
	ident, name, visibility string
}

func parseRegistry(root string) ([]registryEntry, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "envvars", "envvars.go"), nil, 0)
	if err != nil {
		return nil, err
	}
	var out []registryEntry
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if len(vs.Values) != 1 {
				continue
			}
			lit, ok := vs.Values[0].(*ast.CompositeLit)
			if !ok || !isIdent(lit.Type, "Var") {
				continue
			}
			entry := registryEntry{ident: vs.Names[0].Name}
			for _, elt := range lit.Elts {
				kv := elt.(*ast.KeyValueExpr)
				switch kv.Key.(*ast.Ident).Name {
				case "Name":
					entry.name, _ = strconv.Unquote(kv.Value.(*ast.BasicLit).Value)
				case "Visibility":
					entry.visibility = strings.ToLower(kv.Value.(*ast.Ident).Name)
				}
			}
			out = append(out, entry)
		}
	}
	return out, nil
}

// envRows lists the supported variables (those with a Visibility; the one
// retained only to diagnose a removed name has none) with their doc row and
// the directories that read them, by registry identifier or by name.
func envRows(root string, sources []goSource) ([]envRow, error) {
	registry, err := parseRegistry(root)
	if err != nil {
		return nil, err
	}
	byIdent := map[string]map[string]bool{}
	byName := map[string]map[string]bool{}
	for _, src := range sources {
		if !strings.HasPrefix(src.path, "envvars/") {
			for _, m := range qualifiedRef.FindAllStringSubmatch(src.text, -1) {
				addTo(byIdent, m[1], src.dir())
			}
		}
		if src.path == "envvars/envvars.go" {
			continue
		}
		for _, m := range evenerName.FindAllStringSubmatch(src.text, -1) {
			addTo(byName, m[1], src.dir())
		}
	}
	catalog, err := registryNames(root)
	if err != nil {
		return nil, err
	}
	internalReads, err := registryFuncReads(root)
	if err != nil {
		return nil, err
	}
	var rows []envRow
	for _, e := range registry {
		if e.visibility == "" {
			continue
		}
		dirs := map[string]bool{}
		for _, index := range []map[string]bool{byIdent[e.ident], byName[e.name]} {
			for d := range index {
				// A test-support package that clears a variable to isolate a
				// test refers to it without reading it.
				if !isTestSupport(d) {
					dirs[d] = true
				}
			}
		}
		if internalReads[e.ident] {
			dirs["envvars"] = true
		}
		if catalog[e.name] {
			dirs["llm/registry"] = true
		}
		rows = append(rows, envRow{
			Name:       e.name,
			Visibility: e.visibility,
			DocRow:     docMention(root, mentionsBackticked(e.name)),
			Consumers:  sorted(dirs),
		})
	}
	return rows, nil
}

// isTestSupport reports whether dir is a package that exists only to support
// tests (oaitest, sandboxtest, hubtestenv).
func isTestSupport(dir string) bool {
	base := path.Base(dir)
	return strings.HasSuffix(base, "test") || strings.HasSuffix(base, "testenv")
}

// quotedOrExpanded matches a variable name written as a quoted string or as a
// $NAME / ${NAME} expansion, the two ways the registry's data and code name one.
var quotedOrExpanded = regexp.MustCompile(`(?:"|\$\{?)([A-Z][A-Z0-9_]*)\b`)

// registryNames is the set of variable names the provider registry reads: those
// the embedded models.dev catalog, the overlay beside it and the registry's own
// Go sources name. Provider variables have no Go identifier at their reader, so
// appearing here is how the registry consumes one.
func registryNames(root string) (map[string]bool, error) {
	var text bytes.Buffer
	dir := filepath.Join(root, "llm", "registry")
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".gz") {
			zr, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			if data, err = io.ReadAll(zr); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
		}
		text.Write(data)
		return nil
	})
	names := map[string]bool{}
	for _, m := range quotedOrExpanded.FindAllSubmatch(text.Bytes(), -1) {
		names[string(m[1])] = true
	}
	return names, err
}

// registryFuncReads is the set of registry identifiers that envvars.go's own
// function bodies refer to, as the recorder master switch does. Declarations,
// the allVars list, comments and strings are not reads.
func registryFuncReads(root string) (map[string]bool, error) {
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, "envvars", "envvars.go"), nil, 0)
	if err != nil {
		return nil, err
	}
	reads := map[string]bool{}
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok {
					reads[id.Name] = true
				}
				return true
			})
		}
	}
	return reads, nil
}

func mentionsBackticked(name string) func(string) bool {
	return func(text string) bool { return strings.Contains(text, "`"+name+"`") }
}

// directEnvRows lists variables read by name outside the registry: the
// EVENER_* constants that dev tooling and test rigs declare themselves, and
// literal os.Getenv names.
func directEnvRows(root string, sources []goSource) ([]directRow, error) {
	registry, err := parseRegistry(root)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, e := range registry {
		known[e.name] = true
	}
	byName := map[string]map[string]bool{}
	for _, src := range sources {
		if src.path == "envvars/envvars.go" {
			continue
		}
		for _, re := range []*regexp.Regexp{evenerName, getenvName} {
			for _, m := range re.FindAllStringSubmatch(src.text, -1) {
				if !known[m[1]] {
					addTo(byName, m[1], src.dir())
				}
			}
		}
	}
	var rows []directRow
	for _, name := range sorted(keysOf(byName)) {
		rows = append(rows, directRow{
			Name:      name,
			DocRow:    docMention(root, mentionsBackticked(name)),
			Consumers: sorted(byName[name]),
		})
	}
	return rows, nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func keysOf(m map[string]map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// hubRows reads cmd/evener-hub/config.go: the scalar toml keys of Config, the
// default DefaultConfig gives each, the doc that mentions it, and the hub
// files that read its field. It also returns removedKnobs, the retired keys a
// hub.toml may still set.
func hubRows(root string, sources []goSource) ([]hubRow, []string, error) {
	path := filepath.Join(root, "cmd", "evener-hub", "config.go")
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	file, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
	if err != nil {
		return nil, nil, err
	}
	text := func(n ast.Node) string {
		return string(src[n.Pos()-file.FileStart : n.End()-file.FileStart])
	}

	consts := map[string]string{}
	defaults := map[string]ast.Expr{}
	var removed []string
	var fields []*ast.Field
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					if d.Tok == token.CONST && len(s.Values) == 1 {
						consts[s.Names[0].Name] = text(s.Values[0])
					}
					if d.Tok == token.VAR && s.Names[0].Name == "removedKnobs" {
						for _, elt := range s.Values[0].(*ast.CompositeLit).Elts {
							key, _ := strconv.Unquote(elt.(*ast.BasicLit).Value)
							removed = append(removed, key)
						}
					}
				case *ast.TypeSpec:
					if st, ok := s.Type.(*ast.StructType); ok && s.Name.Name == "Config" {
						fields = st.Fields.List
					}
				}
			}
		case *ast.FuncDecl:
			if d.Name.Name == "DefaultConfig" {
				ast.Inspect(d.Body, func(n ast.Node) bool {
					if lit, ok := n.(*ast.CompositeLit); ok && isIdent(lit.Type, "Config") {
						for _, elt := range lit.Elts {
							kv := elt.(*ast.KeyValueExpr)
							defaults[kv.Key.(*ast.Ident).Name] = kv.Value
						}
					}
					return true
				})
			}
		}
	}
	if fields == nil {
		return nil, nil, fmt.Errorf("%s: no Config struct", path)
	}

	reads, err := configReads(sources)
	if err != nil {
		return nil, nil, err
	}
	var rows []hubRow
	for _, f := range fields {
		key := tomlKey(f)
		if key == "" || !isScalarKnob(f.Type) {
			continue
		}
		name := f.Names[0].Name
		row := hubRow{Key: key, Default: "unset"}
		if expr, ok := defaults[name]; ok {
			row.Default = defaultText(expr, consts, text)
		}
		row.DocRow = docMention(root, mentionsKey(key))
		row.Consumers = sorted(reads[name])
		rows = append(rows, row)
	}
	return rows, removed, nil
}

// configReads maps each Config field name to the hub files that read it. Config
// is the hub's own type, so only package main's files can; a read is a selector
// on the configuration the hub passes around (cfg, config, or a cfg/Cfg field),
// which keeps a same-named field of another struct from counting.
func configReads(sources []goSource) (map[string]map[string]bool, error) {
	reads := map[string]map[string]bool{}
	fset := token.NewFileSet()
	for _, src := range sources {
		if src.dir() != "cmd/evener-hub" || src.path == "cmd/evener-hub/config.go" {
			continue
		}
		file, err := parser.ParseFile(fset, src.path, src.text, 0)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok && isConfigExpr(sel.X) {
				addTo(reads, sel.Sel.Name, src.path)
			}
			return true
		})
	}
	return reads, nil
}

func isConfigExpr(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name == "cfg" || v.Name == "config"
	case *ast.SelectorExpr:
		return v.Sel.Name == "cfg" || v.Sel.Name == "Cfg"
	}
	return false
}

var tomlTag = regexp.MustCompile(`toml:"([^",]*)`)

// tomlKey is the field's hub.toml key, or "" for an unexported or untagged
// field and for a field the decoder skips.
func tomlKey(f *ast.Field) string {
	if f.Tag == nil || len(f.Names) != 1 || !f.Names[0].IsExported() {
		return ""
	}
	m := tomlTag.FindStringSubmatch(f.Tag.Value)
	if m == nil || m[1] == "-" {
		return ""
	}
	return m[1]
}

// isScalarKnob excludes the machine-managed record tables (maps) and the
// [[hosts]] and [[providers]] arrays of tables; strings, numbers, booleans,
// durations and string lists are operator knobs.
func isScalarKnob(t ast.Expr) bool {
	switch v := t.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return true
	case *ast.ArrayType:
		id, ok := v.Elt.(*ast.Ident)
		return ok && id.Name == "string"
	}
	return false
}

func mentionsKey(key string) func(string) bool {
	assigned := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + ` =`)
	return func(text string) bool {
		return strings.Contains(text, "`"+key+"`") || assigned.MatchString(text)
	}
}

// defaultText renders a DefaultConfig value: a literal or an expression over
// package constants prints as written (a constant name resolves to its value);
// anything that calls a function is computed at startup.
func defaultText(expr ast.Expr, consts map[string]string, text func(ast.Node) string) string {
	computed := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			computed = true
		case *ast.SelectorExpr:
			// time.Second and friends are constants of the standard library.
			if isIdent(v.X, "time") {
				return false
			}
		case *ast.Ident:
			if _, isConst := consts[v.Name]; !isConst && v.Name != "true" && v.Name != "false" {
				computed = true
			}
		}
		return true
	})
	if computed {
		return "computed at startup"
	}
	out := text(expr)
	if id, ok := expr.(*ast.Ident); ok && consts[id.Name] != "" {
		out = consts[id.Name]
	}
	return out
}
