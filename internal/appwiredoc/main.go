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
	"sort"
	"strings"
	"text/template"

	"primeradiant.com/serf/appwire"
)

//go:embed protocol.md.tmpl
var tmplText string

type fieldView struct {
	JSON      string
	GoType    string
	Omitempty bool
	Embedded  bool
}

type typeView struct {
	Name   string
	Fields []fieldView
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
	out := flag.String("out", "", "output markdown path")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "appwiredoc: -out is required")
		os.Exit(2)
	}

	data := build()
	tmpl := template.Must(template.New("protocol").Funcs(template.FuncMap{
		"yesno": func(b bool) string {
			if b {
				return "yes"
			}
			return ""
		},
	}).Parse(tmplText))

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		fmt.Fprintln(os.Stderr, "appwiredoc: render:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(buf.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "appwiredoc: write:", err)
		os.Exit(1)
	}
}

func build() docData {
	d := docData{}
	typeNames := map[string]typeView{}

	register := func(v any) string {
		t := reflect.TypeOf(v)
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
			typeNames[name] = typeView{Name: name, Fields: fieldsOf(t)}
		}
		return name
	}

	for _, m := range appwire.Methods {
		d.Methods = append(d.Methods, methodView{
			Name:       m.Name,
			Scope:      string(m.Scope),
			Summary:    m.Summary,
			ParamsType: register(m.Params),
			ResultType: register(m.Result),
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

func fieldsOf(t reflect.Type) []fieldView {
	if t == nil || t.Kind() != reflect.Struct {
		return nil
	}
	var out []fieldView
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
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
