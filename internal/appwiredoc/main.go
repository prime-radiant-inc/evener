// Command appwiredoc generates the AppWire protocol reference
// (docs/appwire-protocol.md) from the declarative catalog in package appwire
// (appwire.Methods and appwire.Notifications). It is run via `go generate` on
// the appwire package; the committed doc is verified up-to-date in CI
// (`make lint-generated`), so the catalog in code is the single source of
// truth and the doc cannot drift.
//
// It never invents content: method/notification names, scopes, params/result
// types and their JSON fields are reflected from the catalog and the Go types.
// Prose (transport, lifecycle, keepalive, error model) lives in the embedded
// template.
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"text/template"

	"primeradiant.com/evener/appwire"
)

//go:embed protocol.md.tmpl
var tmplText string
var (
	exitProcess     = os.Exit
	executeTemplate = func(t *template.Template, buf *strings.Builder, data docData) error {
		return t.Execute(buf, data)
	}
)

type fieldView struct {
	JSON      string
	GoType    string
	Omitempty bool
	Embedded  bool
}

type typeView struct {
	Name   string
	Fields []fieldView
	Kind   string
}

type methodView struct {
	Name       string
	Scope      string
	Summary    string
	ParamsType string
	ResultType string
}

type notificationView struct {
	Name        string
	PayloadType string
	Summary     string
}

type docData struct {
	Methods       []methodView
	Notifications []notificationView
	Types         []typeView
}

func main() {
	exitProcess(run(os.Args[1:], os.Stderr, os.WriteFile))
}

func run(args []string, stderr *os.File, writeFile func(string, []byte, os.FileMode) error) int {
	flags := flag.NewFlagSet("appwiredoc", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "output markdown path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		_, _ = fmt.Fprintln(stderr, "appwiredoc: -out is required")
		return 2
	}

	data := build()
	tmpl := template.Must(template.New("protocol").Funcs(template.FuncMap{
		"typeLink": typeLinker(data.Types),
		"yesno": func(b bool) string {
			if b {
				return "yes"
			}
			return ""
		},
		// tableCell escapes the one character a markdown table cannot carry
		// unescaped: GFM splits a row on `|` even inside inline code, so a union
		// result type joined with a pipe (or any prose that carries one) would
		// silently add a column and shift the row.
		"tableCell": func(text string) string {
			return strings.ReplaceAll(text, "|", `\|`)
		},
	}).Parse(tmplText))

	var buf strings.Builder
	if err := executeTemplate(tmpl, &buf, data); err != nil {
		_, _ = fmt.Fprintln(stderr, "appwiredoc: render:", err)
		return 1
	}
	if err := writeFile(*out, []byte(buf.String()), 0o644); err != nil {
		_, _ = fmt.Fprintln(stderr, "appwiredoc: write:", err)
		return 1
	}
	return 0
}

func build() docData {
	d := docData{}
	typeNames := map[string]typeView{}

	register := func(v any) string {
		return registerType(typeNames, v)
	}
	for _, v := range appwire.AllJobActivityTypes {
		register(v)
	}
	// Supplemental roots carried outside typed catalog params/results.
	register(appwire.EvenerDelegateInfo{})
	register(appwire.JobOutputPrunedErrorData{})

	for _, m := range appwire.Methods {
		d.Methods = append(d.Methods, methodView{
			Name:       m.Name,
			Scope:      string(m.Scope),
			Summary:    m.Summary,
			ParamsType: register(m.Params),
			ResultType: resultType(register, m),
		})
	}
	for _, n := range appwire.Notifications {
		d.Notifications = append(d.Notifications, notificationView{
			Name:        n.Name,
			PayloadType: register(n.Payload),
			Summary:     n.Summary,
		})
	}

	for _, tv := range typeNames {
		d.Types = append(d.Types, tv)
	}
	sort.Slice(d.Types, func(i, j int) bool { return d.Types[i].Name < d.Types[j].Name })
	return d
}

// resultType renders one method's result type. An ordinary method is its single
// result struct; a method registered as a union (appwire.MethodResultArms) is
// the union over its arm names, with every arm registered so each gets its own
// field table — the union type itself carries no fields of its own to document.
func resultType(register func(any) string, m appwire.MethodSpec) string {
	arms, union := appwire.MethodResultArms[m.Name]
	if !union {
		return register(m.Result)
	}
	names := make([]string, 0, len(arms))
	for _, arm := range arms {
		names = append(names, register(arm))
	}
	return strings.Join(names, " | ")
}

func registerType(typeNames map[string]typeView, v any) string {
	return registerReflectType(typeNames, reflect.TypeOf(v))
}

func registerReflectType(typeNames map[string]typeView, t reflect.Type) string {
	if t == nil {
		return "(inline)"
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	name := t.Name()
	if name == "" {
		name = t.String()
	}
	if _, seen := typeNames[name]; !seen {
		typeNames[name] = typeView{Name: name, Fields: fieldsOf(t), Kind: t.Kind().String()}
		registerNestedTypes(typeNames, t)
	}
	return name
}

// Follow only JSON-visible fields. External types remain leaves: walking their
// implementation structs (for example time.Time) would invent a wire schema.
func registerNestedTypes(types map[string]typeView, t reflect.Type) {
	visit := func(nested reflect.Type) {
		if nested.PkgPath() == reflect.TypeFor[appwire.Thread]().PkgPath() {
			registerReflectType(types, nested)
		} else if nested.PkgPath() == "" {
			registerNestedTypes(types, nested)
		}
	}
	switch t.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Array:
		visit(t.Elem())
	case reflect.Map:
		visit(t.Key())
		visit(t.Elem())
	case reflect.Struct:
		for f := range t.Fields() {
			if f.IsExported() && strings.Split(f.Tag.Get("json"), ",")[0] != "-" {
				visit(f.Type)
			}
		}
	}
}

var typeToken = regexp.MustCompile(`(?:[A-Za-z_][A-Za-z0-9_]*\.)?[A-Za-z_][A-Za-z0-9_]*`)

func typeLinker(types []typeView) func(string) string {
	known := make(map[string]bool, len(types))
	for _, typ := range types {
		known[typ.Name] = true
	}
	return func(text string) string {
		text = strings.ReplaceAll(text, "*", `\*`)
		return typeToken.ReplaceAllStringFunc(text, func(token string) string {
			name := strings.TrimPrefix(token, "appwire.")
			if known[name] {
				return "[" + token + "](#" + strings.ToLower(name) + ")"
			}
			return token
		})
	}
}

func fieldsOf(t reflect.Type) []fieldView {
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []fieldView
	for f := range t.Fields() {
		if !f.IsExported() {
			continue
		}
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out = append(out, fieldView{
			JSON:      name,
			GoType:    f.Type.String(),
			Omitempty: strings.Contains(opts, "omitempty"),
			Embedded:  f.Anonymous,
		})
	}
	return out
}
