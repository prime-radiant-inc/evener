package hub

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/hubapi"
)

// TestNavigationValueRecordFixtureNamesEveryWireField keeps the shared fixture
// testdata/navigation/value-records.json complete: it names every JSON field of
// every navigation value record, recursively, and nothing else. The client
// codec's test decodes the same file and fails if the codec drops any of it
// (appwire-client/typescript/state/navigation/codec.test.ts), so a field added
// to a type here without its codec entry fails there, where the tolerant codec
// would otherwise drop it without a sound.
func TestNavigationValueRecordFixtureNamesEveryWireField(t *testing.T) {
	raw, err := os.ReadFile("testdata/navigation/value-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	records := map[string]reflect.Type{
		"session":     reflect.TypeFor[hubapi.NavigationSessionSummary](),
		"project":     reflect.TypeFor[hubapi.NavigationProjectSummary](),
		"pin_section": reflect.TypeFor[hubapi.NavigationPinSectionDescriptor](),
		"manifest":    reflect.TypeFor[hubapi.NavigationManifest](),
		"location":    reflect.TypeFor[navigationLocationMetadata](),
	}
	if got, want := slices.Sorted(maps.Keys(fixture)), slices.Sorted(maps.Keys(records)); !slices.Equal(got, want) {
		t.Fatalf("fixture records = %v, want %v", got, want)
	}
	for name, typ := range records {
		assertFixtureNamesEveryField(t, name, typ, fixture[name])
	}
	// The hub's own validators accept the fixture, so the codec test decodes a
	// value the hub could really produce.
	var session hubapi.NavigationSessionSummary
	if err := strictNavigationDecode(fixture["session"], []string{"ref"}, &session); err != nil || !navigationSessionValueValid(session) {
		t.Fatalf("fixture session is not a valid hub summary (decode error %v)", err)
	}
	var manifest hubapi.NavigationManifest
	if err := json.Unmarshal(fixture["manifest"], &manifest); err != nil || !validateNavigationManifestRaw(fixture["manifest"]) || !navigationManifestValuesValid(manifest) {
		t.Fatalf("fixture manifest is not a valid hub manifest (decode error %v)", err)
	}
}

// assertFixtureNamesEveryField checks that object carries exactly typ's JSON
// field names, and recurses into every field whose type is itself a record: a
// struct, a pointer to one, or a list of them. A list must hold at least one
// element so its element type is checked too. A session's children stay empty
// (the summary schema requires it), so that list is not recursed into.
func assertFixtureNamesEveryField(t *testing.T, path string, typ reflect.Type, object json.RawMessage) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(object, &fields); err != nil {
		t.Fatalf("%s: not an object: %v", path, err)
	}
	var want []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		want = append(want, name)
		raw, present := fields[name]
		if !present || name == "children" {
			continue
		}
		elem, list := field.Type, false
		for elem.Kind() == reflect.Pointer || elem.Kind() == reflect.Slice {
			list = list || elem.Kind() == reflect.Slice
			elem = elem.Elem()
		}
		if elem.Kind() != reflect.Struct || elem == reflect.TypeFor[time.Time]() {
			continue
		}
		if !list {
			assertFixtureNamesEveryField(t, path+"."+name, elem, raw)
			continue
		}
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil || len(items) == 0 {
			t.Fatalf("%s.%s: want a non-empty list so its element type is checked", path, name)
		}
		for index, item := range items {
			assertFixtureNamesEveryField(t, fmt.Sprintf("%s.%s[%d]", path, name, index), elem, item)
		}
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(fields)); !slices.Equal(got, want) {
		t.Fatalf("%s fields = %v, want every wire field of %s: %v", path, got, typ, want)
	}
}
