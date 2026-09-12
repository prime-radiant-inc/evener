package appsource

import (
	"reflect"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appitempaging"
)

func TestItemSnapshotStateFingerprintTailIsFixedAndBounded(t *testing.T) {
	stateType := reflect.TypeFor[itemSnapshotState]()
	tail, ok := stateType.FieldByName("FingerprintTail")
	if !ok {
		t.Fatal("retained state has no fixed FingerprintTail")
	}
	if tail.Type.Kind() != reflect.Array || tail.Type.Len() != appwire.TranscriptItemPageLimit {
		t.Fatalf("FingerprintTail type = %v, want fixed [%d] array", tail.Type, appwire.TranscriptItemPageLimit)
	}
	for field := range stateType.Fields() {
		kind := field.Type.Kind()
		if kind == reflect.Map || kind == reflect.Slice {
			t.Fatalf("retained state field %q is unbounded %v", field.Name, kind)
		}
	}
}

func TestItemSnapshotStateTypeContainsNoTranscriptPayloadTypes(t *testing.T) {
	stateType := reflect.TypeFor[itemSnapshotState]()
	for _, forbidden := range []reflect.Type{
		reflect.TypeFor[appitempaging.TranscriptItemCandidate](),
		reflect.TypeFor[appwire.Turn](),
		reflect.TypeFor[appwire.ThreadItem](),
	} {
		if typeContains(stateType, forbidden, make(map[reflect.Type]bool)) {
			t.Fatalf("retained %v contains forbidden transcript payload type %v", stateType, forbidden)
		}
	}
}

func typeContains(current, target reflect.Type, seen map[reflect.Type]bool) bool {
	if current == target {
		return true
	}
	if seen[current] {
		return false
	}
	seen[current] = true
	switch current.Kind() {
	case reflect.Array, reflect.Pointer, reflect.Slice:
		return typeContains(current.Elem(), target, seen)
	case reflect.Map:
		return typeContains(current.Key(), target, seen) || typeContains(current.Elem(), target, seen)
	case reflect.Struct:
		for field := range current.Fields() {
			if typeContains(field.Type, target, seen) {
				return true
			}
		}
	}
	return false
}

func retainedPagingStateCount(t *testing.T, storage any) int {
	t.Helper()
	value := reflect.ValueOf(storage)
	if value.Kind() == reflect.Map {
		return value.Len()
	}
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		t.Fatalf("retained paging state storage = %T, want map or struct pointer", storage)
	}
	entries := value.FieldByName("entries")
	if !entries.IsValid() || entries.Kind() != reflect.Map {
		t.Fatalf("retained paging state storage = %T, want entries map", storage)
	}
	return entries.Len()
}

func retainedItemSnapshotStates(cache *itemSnapshotStateCache) []itemSnapshotState {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	states := make([]itemSnapshotState, 0, len(cache.entries))
	for _, element := range cache.entries {
		states = append(states, element.Value.(itemSnapshotStateEntry).state)
	}
	return states
}
