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
	register(appwire.EvenerDelegateInfo{})
	register(appwire.HostEntry{})
	// InstanceEntry never appears as a method's own Params/Result - only
	// nested inside InstanceListResponse.Instances - so without this it
	// would never get a field table of its own, and a field documented on
	// its AuthStatusResponse twin (e.g. shadowedEnvVar) would silently go
	// undocumented here (PR #758 review).
	register(appwire.InstanceEntry{})
	// InstanceModelEntry likewise only nests inside InstanceEntry.models,
	// so it needs the same explicit registration for its own table.
	register(appwire.InstanceModelEntry{})
	// HostCredentialPushResult is the element type of
	// HostPushCredentialsResponse.Results, never a method's own Params/Result,
	// so it needs the same explicit registration: without it the push report's
	// instance/action/reason fields have no field table at all while
	// types.gen.ts still emits the interface (roborev review of the 07c push).
	register(appwire.HostCredentialPushResult{})
	// HostPlan and HostPlanStaleFacts nest inside the evener/host/plan union's
	// arms — HostPlanPlanned.Plan and HostPlanNoToken.StaleFacts — so without
	// these the reference would name both types with no field table of their own
	// while the TypeScript output emits them.
	register(appwire.HostPlan{})
	register(appwire.HostPlanStaleFacts{})
	// SearchResult is the element type of SearchResponse.Live/.Past/.InSessions
	// (S14), never a method's own Params/Result, so without this it would name
	// the array but document none of its fields (archived, hits, hitCount
	// included). SearchHit and SearchSnippetPart are in turn SearchResult.Hits'
	// and SearchHit.Snippet's own element types, so they need the same
	// explicit registration (roborev review of PR 25).
	register(appwire.SearchResult{})
	register(appwire.SearchHit{})
	register(appwire.SearchSnippetPart{})
	// HostRow and RemovedRow are the row shapes the registry mutations' union
	// arms carry — HostMutationCommitted.Host, the teardown-failure arms' Host,
	// HostMutationAmbiguous.ObservedRow, HostMutationCollisionDropped.Host and
	// .DroppedEntry — and they are also the shapes evener/host/list and
	// evener/host/status return, so the reference needs both field tables:
	// without them the row's own fields (`openRemnantId`, `escalationAgeSec`,
	// `retainedRows`, …) are named but never documented (roborev review of the
	// S12 union conversion).
	register(appwire.HostRow{})
	register(appwire.RemovedRow{})
	// HostTeardownAttestation only nests inside HostTeardownRecoverParams and
	// the recovery receipt record, so it needs the same explicit registration
	// for a field table of its own — the operator/statement/observedAt triple is
	// the audited recovery contract a reader has to see.
	register(appwire.HostTeardownAttestation{})
	// HubNotice is only ever the element type of NoticesListResponse.Notices
	// (evener/notices/list's result and evener/notices/changed's payload),
	// never a method's own Params/Result, so it needs the same explicit
	// registration for a field table of its own (RoboRev review of PR 22).
	register(appwire.HubNotice{})
	// OperationRecord is the element type of HostOperationsResponse.Operations —
	// the deploy pipeline's operations read (08b §§8, 10) — never a method's own
	// Params/Result, so it needs the same explicit registration: without it the
	// reference would name the record but document none of its fields (state,
	// result, and the progress entries a reader inspects).
	register(appwire.OperationRecord{})

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
