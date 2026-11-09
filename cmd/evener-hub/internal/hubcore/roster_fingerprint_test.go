package hubcore

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// structElem returns the struct type a field's value is made of, looking
// through pointers, slices and map values.
func structElem(t reflect.Type) (reflect.Type, bool) {
	for {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			t = t.Elem()
		case reflect.Struct:
			return t, true
		default:
			return nil, false
		}
	}
}

// Every field of LiveEntry, and of the structs under a field that is only
// partly shown, is classified as shown or not shown, so the next field added
// to LiveEntry or to an appwire type it carries cannot slip past the roster
// fingerprint unconsidered.
func TestRosterFingerprintCoversLiveEntry(t *testing.T) {
	listed := func(path string, paths []string) bool { return slices.Contains(paths, path) }
	hasListedBeneath := func(path string) bool {
		for _, p := range slices.Concat(rosterShownFields, rosterNotShownFields) {
			if strings.HasPrefix(p, path+".") {
				return true
			}
		}
		return false
	}
	var walk func(typ reflect.Type, prefix string)
	walk = func(typ reflect.Type, prefix string) {
		for field := range typ.Fields() {
			path := prefix + field.Name
			shown, notShown := listed(path, rosterShownFields), listed(path, rosterNotShownFields)
			switch {
			case shown && notShown:
				t.Errorf("%s is listed as both shown and not shown", path)
			case shown || notShown:
			case hasListedBeneath(path):
				elem, ok := structElem(field.Type)
				if !ok {
					t.Errorf("%s has listed fields beneath it but is not a struct", path)
					continue
				}
				walk(elem, path+".")
			default:
				t.Errorf("%s is in neither rosterShownFields nor rosterNotShownFields", path)
			}
		}
	}
	walk(reflect.TypeFor[LiveEntry](), "")
}

// A listed path that names no field is a leftover from a rename or removal.
func TestRosterFingerprintFieldListsNameRealFields(t *testing.T) {
	for _, path := range slices.Concat(rosterShownFields, rosterNotShownFields) {
		typ := reflect.TypeFor[LiveEntry]()
		for name := range strings.SplitSeq(path, ".") {
			elem, ok := structElem(typ)
			if !ok {
				elem = typ
			}
			field, found := elem.FieldByName(name)
			if !found {
				t.Fatalf("%s names no field", path)
			}
			typ = field.Type
		}
	}
}

func TestRosterFingerprintIgnoresListOrderAndLeavesInputsAlone(t *testing.T) {
	entry := func(flip bool) LiveEntry {
		e := LiveEntry{
			SessionID:          "s1",
			RunningSubagentIDs: []string{"a", "b"},
			RunningJobs:        []appwire.EvenerJobInfo{{JobID: "1"}, {JobID: "2"}},
			Watches:            []appwire.EvenerWatchInfo{{ID: "w1"}, {ID: "w2"}},
			ChildWatches:       map[string][]appwire.EvenerWatchInfo{"a": {{ID: "x"}, {ID: "y"}}},
		}
		if flip {
			e.RunningSubagentIDs = []string{"b", "a"}
			e.RunningJobs = []appwire.EvenerJobInfo{{JobID: "2"}, {JobID: "1"}}
			e.Watches = []appwire.EvenerWatchInfo{{ID: "w2"}, {ID: "w1"}}
			e.ChildWatches = map[string][]appwire.EvenerWatchInfo{"a": {{ID: "y"}, {ID: "x"}}}
		}
		return e
	}
	flipped := entry(true)
	if rosterFingerprint(map[string]LiveEntry{"s1": entry(false)}) != rosterFingerprint(map[string]LiveEntry{"s1": flipped}) {
		t.Fatal("listing the same ids, jobs or watches in another order moved the fingerprint")
	}
	if flipped.RunningSubagentIDs[0] != "b" || flipped.RunningJobs[0].JobID != "2" || flipped.Watches[0].ID != "w2" || flipped.ChildWatches["a"][0].ID != "y" {
		t.Fatal("fingerprinting reordered the caller's lists")
	}
}

func TestRosterFingerprintTreatsNoTaskStateAsEmptyList(t *testing.T) {
	absent := LiveEntry{SessionID: "s1"}
	empty := LiveEntry{SessionID: "s1", Tasks: &appwire.TaskAggregate{}}
	if rosterFingerprint(map[string]LiveEntry{"s1": absent}) != rosterFingerprint(map[string]LiveEntry{"s1": empty}) {
		t.Fatal("a daemon that cannot read its tasks fingerprints differently from an empty list")
	}
}
