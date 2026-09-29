package hubcore

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
)

// Adding a SessionMeta field fails this test until it is placed on
// shownMetaFields or notShownMetaFields, so the fingerprint cannot silently
// miss a field a row shows.
func TestShownMetaFieldsCoverSessionMeta(t *testing.T) {
	covered := map[string]bool{}
	for _, path := range shownMetaFields {
		covered[path] = true
	}
	for _, path := range notShownMetaFields {
		if covered[path] {
			t.Errorf("%s is on both the shown and not-shown lists", path)
		}
		covered[path] = true
	}
	var walk func(prefix string, typ reflect.Type)
	walk = func(prefix string, typ reflect.Type) {
		for f := range typ.Fields() {
			path := prefix + f.Name
			if covered[path] {
				continue
			}
			// A struct field is covered by listing its leaves instead.
			if f.Type.Kind() == reflect.Struct && f.Type != reflect.TypeFor[time.Time]() && hasListedLeaf(path) {
				walk(path+".", f.Type)
				continue
			}
			t.Errorf("SessionMeta field %s is on neither shownMetaFields nor notShownMetaFields: decide whether a navigation row shows it", path)
		}
	}
	walk("", reflect.TypeFor[schema.SessionMeta]())

	// The lists name real fields, so a removed field cannot leave a stale entry.
	for _, path := range append(slices.Clone(shownMetaFields), notShownMetaFields...) {
		if _, ok := metaFieldIndex(path); !ok {
			t.Errorf("listed field %s does not exist on SessionMeta", path)
		}
	}
}

func hasListedLeaf(prefix string) bool {
	for _, path := range append(slices.Clone(shownMetaFields), notShownMetaFields...) {
		if strings.HasPrefix(path, prefix+".") {
			return true
		}
	}
	return false
}

// Every shown field moves the fingerprint, so a listed field is really hashed.
func TestRootFingerprintMovesForEveryShownField(t *testing.T) {
	entry := func(m schema.SessionMeta) []PastEntry { return []PastEntry{{ID: "s1", Meta: m}} }
	base := rootFingerprint(entry(schema.SessionMeta{ID: "s1"}))
	for _, path := range shownMetaFields {
		var meta schema.SessionMeta
		index, _ := metaFieldIndex(path)
		v := reflect.ValueOf(&meta).Elem().FieldByIndex(index)
		switch v.Kind() {
		case reflect.String:
			v.SetString("x")
		case reflect.Bool:
			v.SetBool(true)
		case reflect.Int, reflect.Int64:
			v.SetInt(7)
		case reflect.Uint64:
			v.SetUint(7)
		case reflect.Slice:
			v.Set(reflect.ValueOf([]string{"x"}))
		case reflect.Struct:
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0)))
		default:
			t.Fatalf("shown field %s has unsupported kind %s", path, v.Kind())
		}
		if meta.ID == "" {
			meta.ID = "s1"
		}
		if rootFingerprint(entry(meta)) == base {
			t.Errorf("changing shown field %s does not move the root fingerprint", path)
		}
	}
}

// A subagent contributes only its identity: nothing else about it, and not its
// position among the entries, moves the fingerprint.
func TestRootFingerprintSubagentContributesIdentityOnly(t *testing.T) {
	root := PastEntry{ID: "r", Meta: schema.SessionMeta{ID: "r", Name: "root"}}
	sub := func(name string, at int64) PastEntry {
		return PastEntry{ID: "s", Meta: schema.SessionMeta{ID: "s", Name: name, IsSubagent: true, ParentSessionID: "r", UpdatedAt: time.Unix(at, 0)}}
	}
	before := rootFingerprint([]PastEntry{root, sub("a", 1)})
	if got := rootFingerprint([]PastEntry{sub("b", 2), root}); got != before {
		t.Fatal("a subagent's title, UpdatedAt or position moved the root fingerprint")
	}
	if got := rootFingerprint([]PastEntry{root}); got == before {
		t.Fatal("removing a subagent did not move the root fingerprint")
	}
}

// A field on notShownMetaFields never moves the fingerprint, so a write that
// touches only such fields cannot rebuild navigation.
func TestRootFingerprintIgnoresNotShownFields(t *testing.T) {
	entry := func(m schema.SessionMeta) []PastEntry { return []PastEntry{{ID: "s1", Meta: m}} }
	base := rootFingerprint(entry(schema.SessionMeta{ID: "s1"}))
	for _, path := range notShownMetaFields {
		index, ok := metaFieldIndex(path)
		if !ok {
			t.Fatalf("not-shown field %s does not exist", path)
		}
		meta := schema.SessionMeta{ID: "s1"}
		fillNonZero(reflect.ValueOf(&meta).Elem().FieldByIndex(index))
		if rootFingerprint(entry(meta)) != base {
			t.Errorf("changing not-shown field %s moves the root fingerprint", path)
		}
	}
}

// fillNonZero sets v, and everything reachable from it, to non-zero values.
func fillNonZero(v reflect.Value) {
	switch v.Kind() {
	case reflect.String:
		v.SetString("x")
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(7)
	case reflect.Float32, reflect.Float64:
		v.SetFloat(7)
	case reflect.Struct:
		if v.Type() == reflect.TypeFor[time.Time]() {
			v.Set(reflect.ValueOf(time.Unix(1_700_000_000, 0)))
			return
		}
		for _, field := range v.Fields() {
			if field.CanSet() {
				fillNonZero(field)
			}
		}
	case reflect.Slice:
		v.Set(reflect.MakeSlice(v.Type(), 1, 1))
		fillNonZero(v.Index(0))
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fillNonZero(v.Elem())
	}
}
