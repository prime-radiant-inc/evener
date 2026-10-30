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
	eventType := reflect.TypeFor[Event]()
	var fields []reflect.StructField
	for field := range eventType.Fields() {
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

// eventKindForField derives a payload field's kind from its JSON name. Callers
// pass fields from eventPayloadFields, which already rejects a field without a
// JSON name.
func eventKindForField(field reflect.StructField) EventKind {
	return EventKind("delegate_" + jsonFieldName(field))
}

func eventPayloadField(tb testing.TB, kind EventKind) (reflect.StructField, bool) {
	tb.Helper()
	for _, field := range eventPayloadFields(tb) {
		if eventKindForField(field) == kind {
			return field, true
		}
	}
	return reflect.StructField{}, false
}

// attachEventPayload attaches a fresh zero payload of field's type to event.
func attachEventPayload(tb testing.TB, event *Event, field reflect.StructField) reflect.Value {
	tb.Helper()
	return attachEventPayloadField(event, field)
}

// attachEventPayloadField is attachEventPayload without testing.TB. A fuzz
// target's callback may not call *testing.F methods, so it uses this directly.
func attachEventPayloadField(event *Event, field reflect.StructField) reflect.Value {
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
			case !inEventKindBlock && !declaresEventKindByConversion(valueSpec):
				continue
			}
			for _, expression := range valueSpec.Values {
				if kind, ok := eventKindLiteral(tb, expression); ok {
					kinds = append(kinds, kind)
				}
			}
		}
	}
	return kinds
}

// eventKindLiteral reads an EventKind string literal, including the conversion
// spelling EventKind("delegate_x"). Any other value in an EventKind
// declaration is a form this scan cannot read, so it fails loudly rather than
// letting a kind escape the completeness check.
func eventKindLiteral(tb testing.TB, expression ast.Expr) (EventKind, bool) {
	tb.Helper()
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			tb.Fatalf("EventKind value %s is not a string literal", value.Value)
		}
		kind, err := strconv.Unquote(value.Value)
		if err != nil {
			tb.Fatalf("unquote EventKind literal %s: %v", value.Value, err)
		}
		return EventKind(kind), true
	case *ast.CallExpr:
		if !isEventKindIdent(value.Fun) || len(value.Args) != 1 {
			tb.Fatalf("EventKind value is not an EventKind(\"...\") conversion")
		}
		return eventKindLiteral(tb, value.Args[0])
	default:
		tb.Fatalf("EventKind value %T is not a string literal or EventKind conversion", expression)
		return "", false
	}
}

// declaresEventKindByConversion reports whether an untyped spec's value is the
// EventKind("...") spelling, which declares a kind outside a typed const block.
func declaresEventKindByConversion(spec *ast.ValueSpec) bool {
	for _, expression := range spec.Values {
		if call, ok := expression.(*ast.CallExpr); ok && isEventKindIdent(call.Fun) {
			return true
		}
	}
	return false
}

func isEventKindIdent(expression ast.Expr) bool {
	if parenthesized, ok := expression.(*ast.ParenExpr); ok {
		return isEventKindIdent(parenthesized.X)
	}
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
		kind := eventKindForField(field)
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
