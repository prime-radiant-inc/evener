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

func TestRemoteLegacyPageConversionLeavesRetainedPagingState(t *testing.T) {
	source := NewRemoteHubSource("remote", nil, nil)
	key := remoteItemPagingKey("remote", "thread-1")
	retainedHead := appwire.ThreadItemPosition{Entry: 2}
	retained := remoteItemPagingState{
		identity: appitempaging.CursorIdentity{ThreadRef: key, Incarnation: "retained-incarnation", ProjectionVersion: remoteHubItemCursorProjectionVersion},
		native:   "native-cursor",
		candidates: []appitempaging.TranscriptItemCandidate{{
			TurnID: "turn-1", Position: retainedHead,
			Item: appwire.ThreadItem{ID: "newest", Type: "agentMessage", TurnID: "turn-1", Position: &retainedHead, Version: 1},
		}},
		head: retainedHead, hasHead: true,
	}
	source.itemPaging.put(key, retained)

	olderPosition := appwire.ThreadItemPosition{Entry: 1}
	response := appwire.ThreadReadResponse{
		Thread: appwire.Thread{
			ID: "thread-1", SessionID: "thread-1", Evener: appwire.EvenerThread{Ref: "remote:thread-1"},
			Turns: []appwire.Turn{{
				ID: "turn-1", Status: appwire.TurnStatusCompleted, ItemsView: appwire.TurnItemsViewFragment,
				Items: []appwire.ThreadItem{{
					ID: "older", Type: "agentMessage", TurnID: "turn-1", TranscriptKey: "older",
					Position: &olderPosition, Version: 1,
				}},
			}},
		},
		Snapshot: &appwire.SnapshotIdentity{Incarnation: "snapshot-incarnation", Length: 2},
	}

	converted, err := source.ItemCandidatesFromLegacyPage(t.Context(), appwire.ThreadReadParams{Ref: "remote:thread-1", ThreadID: "thread-1"}, response)
	if err != nil {
		t.Fatalf("legacy conversion: %v", err)
	}
	if converted.Identity.Incarnation != "snapshot-incarnation" {
		t.Fatalf("legacy identity incarnation = %q, want the reread snapshot", converted.Identity.Incarnation)
	}
	state, ok := source.itemPaging.peek(key)
	if !ok || state.identity != retained.identity || state.native != retained.native || len(state.candidates) != len(retained.candidates) {
		t.Fatalf("legacy conversion changed retained paging state: %+v", state)
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
