package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
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
	evenerName = regexp.MustCompile(`^EVENER_[A-Z0-9_]+$`)
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
// the directories that read them: through the envvars identifier, from the
// registry's own functions, or by name in the provider catalog.
func envRows(root string, sources []goSource) ([]envRow, error) {
	registry, err := parseRegistry(root)
	if err != nil {
		return nil, err
	}
	byIdent := map[string]map[string]bool{}
	for _, src := range sources {
		refs, err := scanRefs(src)
		if err != nil {
			return nil, err
		}
		if src.dir() != "envvars" {
			for ident := range refs.registry {
				addTo(byIdent, ident, src.dir())
			}
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
		for d := range byIdent[e.ident] {
			// A test-support package that clears a variable to isolate a
			// test refers to it without reading it.
			if !isTestSupport(d) {
				dirs[d] = true
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

var (
	variableName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	expansion    = regexp.MustCompile(`\$\{?([A-Z][A-Z0-9_]*)\b`)
)

// quotedOrExpanded matches a variable name written as a quoted string or as a
// $NAME / ${NAME} expansion, the two ways the registry's data and code name one.
var quotedOrExpanded = regexp.MustCompile(`(?:"|\$\{?)([A-Z][A-Z0-9_]*)\b`)

// registryNames is the set of variable names the provider registry reads: those
// the embedded models.dev catalog, the overlay beside it and the registry's own
// Go sources name. Provider variables have no Go identifier at their reader, so
// appearing here is how the registry consumes one.
func registryNames(root string) (map[string]bool, error) {
	names := map[string]bool{}
	addQuoted := func(text []byte) {
		for _, m := range quotedOrExpanded.FindAllSubmatch(text, -1) {
			names[string(m[1])] = true
		}
	}
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
		switch {
		case strings.HasSuffix(p, ".go"):
			// String literals only: a comment naming a variable reads nothing.
			file, err := parser.ParseFile(token.NewFileSet(), p, data, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					addQuoted([]byte(lit.Value))
				}
				return true
			})
		case strings.HasSuffix(p, ".gz"):
			zr, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			if data, err = io.ReadAll(zr); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			addQuoted(data)
		case strings.HasSuffix(p, ".toml"):
			var doc map[string]any
			if _, err := toml.Decode(string(data), &doc); err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			addTomlNames(doc, names)
		}
		return nil
	})
	return names, err
}

// addTomlNames adds every variable name a decoded TOML value holds: a string
// that is a name, or a $NAME / ${NAME} expansion inside one. Decoding drops
// comments, so only values count.
func addTomlNames(v any, names map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		for _, e := range t {
			addTomlNames(e, names)
		}
	case []map[string]any:
		for _, e := range t {
			addTomlNames(e, names)
		}
	case []any:
		for _, e := range t {
			addTomlNames(e, names)
		}
	case string:
		if variableName.MatchString(t) {
			names[t] = true
		}
		for _, m := range expansion.FindAllStringSubmatch(t, -1) {
			names[m[1]] = true
		}
	}
}

// fileRefs are the environment references one Go file makes.
type fileRefs struct {
	registry map[string]bool // envvars identifiers used other than to set or clear the variable
	declared []string        // string constants and variables: a name a tool declares for itself
	getenvs  []string        // names passed literally to os.Getenv / os.LookupEnv
}

// setterMethods only write the variable, so a file that merely sets it for a
// child process or a test does not read it.
var setterMethods = map[string]bool{"Setenv": true, "Unsetenv": true, "Assignment": true}

// isSetterCall reports whether call is to a function named like a setter
// (Setenv, setEnv, unsetEnv, set...), whose variable-name arguments are being
// written.
func isSetterCall(call *ast.CallExpr) bool {
	var name string
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		name = fn.Name
	case *ast.SelectorExpr:
		name = fn.Sel.Name
	}
	name = strings.ToLower(name)
	return strings.HasPrefix(name, "set") || strings.HasPrefix(name, "unset")
}

// markWrites marks the expressions a statement or call writes through: the
// map key of an assignment target (overlay[envvars.X.Name] = v), the key of a
// map literal, and every argument of a setter call.
func markWrites(n ast.Node, writes map[ast.Node]bool) {
	switch v := n.(type) {
	case *ast.AssignStmt:
		for _, lhs := range v.Lhs {
			if idx, ok := lhs.(*ast.IndexExpr); ok {
				writes[idx.Index] = true
			}
		}
	case *ast.KeyValueExpr:
		// An environment map built in one expression: map[string]string{envvars.X.Name: "1"}.
		writes[v.Key] = true
	case *ast.CallExpr:
		if isSetterCall(v) {
			for _, arg := range v.Args {
				writes[arg] = true
			}
		}
	}
}

// scanRefs finds a file's environment references in its syntax tree, so
// comments and strings that mention an identifier are not references.
func scanRefs(src goSource) (fileRefs, error) {
	refs := fileRefs{registry: map[string]bool{}}
	if !strings.Contains(src.text, "envvars.") && !strings.Contains(src.text, `"EVENER_`) && !strings.Contains(src.text, "os.Getenv") && !strings.Contains(src.text, "os.LookupEnv") {
		return refs, nil
	}
	file, err := parser.ParseFile(token.NewFileSet(), src.path, src.text, 0)
	if err != nil {
		return refs, err
	}
	skip := map[ast.Node]bool{}   // envvars.X selectors already classified through their method
	writes := map[ast.Node]bool{} // expressions that only name a variable to write it
	ast.Inspect(file, func(n ast.Node) bool {
		markWrites(n, writes)
		switch v := n.(type) {
		case *ast.SelectorExpr:
			if inner, ok := v.X.(*ast.SelectorExpr); ok && isIdent(inner.X, "envvars") {
				skip[inner] = true
				if !setterMethods[v.Sel.Name] && !writes[v] {
					refs.registry[inner.Sel.Name] = true
				}
			} else if isIdent(v.X, "envvars") && !skip[v] {
				refs.registry[v.Sel.Name] = true
			}
		case *ast.ValueSpec:
			for _, value := range v.Values {
				if lit, ok := value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if name, err := strconv.Unquote(lit.Value); err == nil {
						refs.declared = append(refs.declared, name)
					}
				}
			}
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok && isIdent(sel.X, "os") && (sel.Sel.Name == "Getenv" || sel.Sel.Name == "LookupEnv") && len(v.Args) == 1 {
				if lit, ok := v.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if name, err := strconv.Unquote(lit.Value); err == nil {
						refs.getenvs = append(refs.getenvs, name)
					}
				}
			}
		}
		return true
	})
	return refs, nil
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
		refs, err := scanRefs(src)
		if err != nil {
			return nil, err
		}
		for _, name := range append(refs.getenvs, refs.declared...) {
			if !known[name] && (evenerName.MatchString(name) || slices.Contains(refs.getenvs, name)) {
				addTo(byName, name, src.dir())
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
// is the hub's own type, so only package main's files can. The package is
// type-checked without resolving its imports: every selector on a value of the
// local Config type resolves, and one on an imported type (hubcore.WebConfig,
// whose fields share names with Config's) stays unresolved and does not count.
// A plain assignment to a field is a write, not a read.
func configReads(sources []goSource) (map[string]map[string]bool, error) {
	fset := token.NewFileSet()
	var files []*ast.File
	for _, src := range sources {
		if src.dir() != "cmd/evener-hub" {
			continue
		}
		file, err := parser.ParseFile(fset, src.path, src.text, 0)
		if err != nil {
			return nil, err
		}
		files = append(files, file)
	}
	info := &types.Info{Selections: map[*ast.SelectorExpr]*types.Selection{}}
	conf := types.Config{
		Importer: unresolvedImporter{},
		Error:    func(error) {}, // unresolved imports are expected; checking continues past them
	}
	pkg, _ := conf.Check("main", fset, files, info)

	reads := map[string]map[string]bool{}
	for _, file := range files {
		name := fset.Position(file.Pos()).Filename
		if name == "cmd/evener-hub/config.go" {
			continue
		}
		assigned := map[ast.Node]bool{}
		ast.Inspect(file, func(n ast.Node) bool {
			switch v := n.(type) {
			case *ast.AssignStmt:
				if v.Tok == token.ASSIGN {
					for _, lhs := range v.Lhs {
						assigned[lhs] = true
					}
				}
			case *ast.SelectorExpr:
				if sel := info.Selections[v]; sel != nil && sel.Kind() == types.FieldVal && isLocalConfig(sel.Recv(), pkg) && !assigned[v] {
					addTo(reads, v.Sel.Name, name)
				}
			}
			return true
		})
	}
	return reads, nil
}

// isLocalConfig reports whether t is the hub's Config (or a pointer to it).
func isLocalConfig(t types.Type, pkg *types.Package) bool {
	if ptr, ok := t.(*types.Pointer); ok {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	return ok && named.Obj().Pkg() == pkg && named.Obj().Name() == "Config"
}

// unresolvedImporter declines every import, so type-checking sees only the
// package's own declarations.
type unresolvedImporter struct{}

func (unresolvedImporter) Import(path string) (*types.Package, error) {
	return nil, fmt.Errorf("import %s is not loaded", path)
}

var tomlTag = regexp.MustCompile(`toml:"([^",]*)`)

// tomlKey is the field's hub.toml key, or "" for an unexported or untagged
// field and for a field the decoder skips.
func tomlKey(f *ast.Field) string {
	if f.Tag == nil || len(f.Names) != 1 || !f.Names[0].IsExported() {
		return ""
	}
	m := tomlTag.FindStringSubmatch(f.Tag.Value)
	if len(m) < 2 || m[1] == "-" {
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
