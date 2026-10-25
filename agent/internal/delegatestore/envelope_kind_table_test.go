package delegatestore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/fuzz/aliascheck"
)

// The EventKind enum and Event's payload fields must agree one-to-one: a
// payload field whose JSON name is x belongs to kind "delegate_x". The
// production table is the only place that pairs them, so it cannot be its own
// oracle. The helpers below derive the pairing from the package's EventKind
// declarations and Event's struct tags instead, independently of
// eventKindSpecs.

// eventPayloadFields returns Event's payload fields, in declaration order. A
// payload field is an Event field whose type is a pointer; Kind, Seq, TS and
// DelegateID are the only non-payload fields. Filtering by pointer type (not
// by whether the derived kind is declared) is what keeps a new payload field
// with no kind visible to the completeness check.
func eventPayloadFields(tb testing.TB) []reflect.StructField {
	tb.Helper()
	eventType := reflect.TypeOf(Event{})
	var fields []reflect.StructField
	for i := 0; i < eventType.NumField(); i++ {
		field := eventType.Field(i)
		if field.Type.Kind() != reflect.Pointer {
			continue
		}
		if jsonFieldName(field) == "" {
			tb.Fatalf("Event payload field %s has no JSON name", field.Name)
		}
		fields = append(fields, field)
	}
	if len(fields) == 0 {
		tb.Fatal("Event has no pointer payload fields")
	}
	return fields
}

func jsonFieldName(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	return name
}

func eventKindForField(tb testing.TB, field reflect.StructField) EventKind {
	tb.Helper()
	name := jsonFieldName(field)
	if name == "" {
		tb.Fatalf("Event field %s has no JSON name", field.Name)
	}
	return EventKind("delegate_" + name)
}

func eventPayloadField(tb testing.TB, kind EventKind) (reflect.StructField, bool) {
	tb.Helper()
	for _, field := range eventPayloadFields(tb) {
		if eventKindForField(tb, field) == kind {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// attachEventPayload attaches a fresh zero payload of field's type to event.
func attachEventPayload(tb testing.TB, event *Event, field reflect.StructField) reflect.Value {
	tb.Helper()
	payload := reflect.New(field.Type.Elem())
	reflect.ValueOf(event).Elem().FieldByIndex(field.Index).Set(payload)
	return payload
}

// declaredEventKinds collects every EventKind-typed string constant in the
// package's non-test sources. Scanning the whole package, not one named file,
// keeps a kind declared in a new event_*.go file visible.
func declaredEventKinds(tb testing.TB) []EventKind {
	tb.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		tb.Fatalf("read package directory: %v", err)
	}
	fileSet := token.NewFileSet()
	var kinds []EventKind
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fileSet, filepath.Join(".", name), nil, 0)
		if err != nil {
			tb.Fatalf("parse %s: %v", name, err)
		}
		kinds = append(kinds, eventKindConsts(tb, file)...)
	}
	slices.Sort(kinds)
	return kinds
}

func eventKindConsts(tb testing.TB, file *ast.File) []EventKind {
	tb.Helper()
	var kinds []EventKind
	for _, declaration := range file.Decls {
		genDecl, ok := declaration.(*ast.GenDecl)
		if !ok || genDecl.Tok != token.CONST {
			continue
		}
		// A spec that omits its type inherits the previous spec's type, so a
		// kind added without repeating EventKind in the same const block is
		// still collected.
		inEventKindBlock := false
		for _, spec := range genDecl.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch {
			case isEventKindIdent(valueSpec.Type):
				inEventKindBlock = true
			case valueSpec.Type != nil:
				inEventKindBlock = false
				continue
			case !inEventKindBlock:
				continue
			}
			for _, expression := range valueSpec.Values {
				literal, ok := expression.(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				kind, err := strconv.Unquote(literal.Value)
				if err != nil {
					tb.Fatalf("unquote EventKind literal %s: %v", literal.Value, err)
				}
				kinds = append(kinds, EventKind(kind))
			}
		}
	}
	return kinds
}

func isEventKindIdent(expression ast.Expr) bool {
	ident, ok := expression.(*ast.Ident)
	return ok && ident.Name == "EventKind"
}

// TestEventKindTableComplete is the independent net under eventKindSpecs. It
// reads the EventKind declarations and Event's payload fields rather than the
// table, so a kind added with no row, a payload field added with no kind, a
// row bound to another kind's payload, or a row missing apply/clone handling
// fails here.
func TestEventKindTableComplete(t *testing.T) {
	t.Parallel()
	declared := declaredEventKinds(t)
	fields := eventPayloadFields(t)

	declaredSet := make(map[EventKind]bool, len(declared))
	for _, kind := range declared {
		if declaredSet[kind] {
			t.Errorf("the package declares EventKind %q more than once", kind)
		}
		declaredSet[kind] = true
	}
	fieldKinds := make(map[EventKind]bool, len(fields))
	for _, field := range fields {
		kind := eventKindForField(t, field)
		if fieldKinds[kind] {
			t.Errorf("Event has more than one payload field for kind %q", kind)
		}
		fieldKinds[kind] = true
	}
	for kind := range declaredSet {
		if !fieldKinds[kind] {
			t.Errorf("EventKind %q has no pointer payload field (want JSON name %q)", kind, strings.TrimPrefix(string(kind), "delegate_"))
		}
	}
	for kind := range fieldKinds {
		if !declaredSet[kind] {
			t.Errorf("Event payload field names kind %q, which the package does not declare", kind)
		}
	}

	rowCount := make(map[EventKind]int, len(eventKindSpecs))
	rowByKind := make(map[EventKind]eventKindSpec, len(eventKindSpecs))
	for _, spec := range eventKindSpecs {
		rowCount[spec.kind]++
		rowByKind[spec.kind] = spec
		if !declaredSet[spec.kind] {
			t.Errorf("eventKindSpecs has a row for undeclared kind %q", spec.kind)
		}
	}

	applyNames := make(map[string]EventKind, len(eventKindSpecs))
	for _, kind := range declared {
		if rowCount[kind] != 1 {
			t.Errorf("EventKind %q has %d eventKindSpecs rows, want 1", kind, rowCount[kind])
			continue
		}
		spec := rowByKind[kind]
		field, ok := eventPayloadField(t, kind)
		if !ok {
			continue
		}
		event := Event{Kind: kind, DelegateID: "dlg_1"}
		if spec.present(&event) {
			t.Errorf("kind %q reports its payload present with no payload attached", kind)
		}
		attachEventPayload(t, &event, field)
		if !spec.present(&event) {
			t.Errorf("kind %q does not report its %s payload present", kind, field.Name)
		}
		for _, other := range eventKindSpecs {
			if other.kind != kind && other.present(&event) {
				t.Errorf("row %q reports kind %q's %s payload present", other.kind, kind, field.Name)
			}
		}
		if spec.clone == nil {
			t.Errorf("kind %q has no clone function", kind)
		}
		if spec.apply == nil {
			t.Errorf("kind %q has no apply function", kind)
			continue
		}
		name := runtime.FuncForPC(reflect.ValueOf(spec.apply).Pointer()).Name()
		if previous, duplicate := applyNames[name]; duplicate {
			t.Errorf("kinds %q and %q share the apply function %s", previous, kind, name)
		}
		applyNames[name] = kind
	}
}

// TestCloneEventIsolatesEveryKind drives one clone-isolation case per table
// row. The field it populates comes from the independently derived kind-to-
// field mapping, never from the row, so a row bound to the wrong payload
// cannot make the test agree with itself. aliascheck is the same structural
// oracle the sibling jobstore.cloneEvent test uses: it rejects a shared
// pointer, slice backing array or map at any depth, and needs no new case when
// a payload field is added.
func TestCloneEventIsolatesEveryKind(t *testing.T) {
	t.Parallel()
	for _, spec := range eventKindSpecs {
		t.Run(string(spec.kind), func(t *testing.T) {
			t.Parallel()
			field, ok := eventPayloadField(t, spec.kind)
			if !ok {
				t.Fatalf("kind %q has no pointer payload field", spec.kind)
			}
			original := Event{Kind: spec.kind, DelegateID: "dlg_1"}
			payload := attachEventPayload(t, &original, field)
			aliascheck.Populate(payload.Elem(), 8)
			clone := cloneEvent(original)
			if !reflect.DeepEqual(original, clone) {
				t.Fatalf("cloneEvent changed the %s payload:\noriginal: %#v\nclone:    %#v", field.Name, original, clone)
			}
			if shared := aliascheck.FindSharedStorage(reflect.ValueOf(original), reflect.ValueOf(clone), "Event"); shared != "" {
				t.Fatalf("%s: %s", spec.kind, shared)
			}
		})
	}
}
