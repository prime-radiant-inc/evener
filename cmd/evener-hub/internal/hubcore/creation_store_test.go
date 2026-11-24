package hubcore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

func testCreationParams() appwire.ThreadStartParams {
	return appwire.ThreadStartParams{
		CWD:   "/tmp/project",
		Model: "openai/gpt-5",
		Input: []appwire.InputItem{{Type: "text", Text: "hello"}},
	}
}

func TestCreationStoreRoundTripAllFourStatesAndStripsSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewCreationStore(root)
	if err != nil {
		t.Fatalf("NewCreationStore: %v", err)
	}
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatalf("CreationPayloadHash: %v", err)
	}

	// 1. spawning + rollback
	res, err := store.Acquire(t.Context(), "m-rollback", hash, true, "turn-m-0")
	if err != nil {
		t.Fatalf("Acquire m-rollback: %v", err)
	}
	if res.Owner == nil || res.Response != nil {
		t.Fatalf("Acquire unseen = %+v, want owner and nil response", res)
	}
	dirInfo, err := os.Stat(filepath.Join(root, creationStateSubdir))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("creations dir mode = %o, want 0700", perm)
	}
	fileInfo, err := os.Stat(creationStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("creations state file mode = %o, want 0600", perm)
	}
	if err := store.RollbackSpawning("m-rollback"); err != nil {
		t.Fatalf("RollbackSpawning: %v", err)
	}
	store.ReleaseOwner(res.Owner, appwire.ThreadStartResponse{}, errors.New("rolled back"))
	reacquire, err := store.Acquire(t.Context(), "m-rollback", hash, true, "turn-m-0")
	if err != nil || reacquire.Owner == nil {
		t.Fatalf("re-acquire after rollback = %+v, %v; want fresh unseen owner", reacquire, err)
	}
	_ = store.RollbackSpawning("m-rollback")
	store.ReleaseOwner(reacquire.Owner, appwire.ThreadStartResponse{}, nil)

	// 2. spawned persisted across reload
	spawnedRes, err := store.Acquire(t.Context(), "m-spawned", hash, true, "turn-m-1")
	if err != nil || spawnedRes.Owner == nil {
		t.Fatalf("Acquire m-spawned: %+v, %v", spawnedRes, err)
	}
	if err := store.CommitSpawned("m-spawned", "local:sess-1", "sess-1", "inst-1"); err != nil {
		t.Fatalf("CommitSpawned: %v", err)
	}
	store.ReleaseOwner(spawnedRes.Owner, appwire.ThreadStartResponse{}, errors.New("partial turn failure"))

	// 3. completed persisted with stripped turns & turn items
	compRes, err := store.Acquire(t.Context(), "m-completed", hash, true, "turn-m-2")
	if err != nil || compRes.Owner == nil {
		t.Fatalf("Acquire m-completed: %+v, %v", compRes, err)
	}
	if err := store.CommitSpawned("m-completed", "local:sess-2", "sess-2", "inst-2"); err != nil {
		t.Fatalf("CommitSpawned completed: %v", err)
	}
	fullResp := appwire.ThreadStartResponse{
		Thread: appwire.Thread{
			ID:            "sess-2",
			SessionID:     "sess-2",
			Name:          "Keep Metadata",
			ModelProvider: "openai",
			CWD:           "/tmp/project",
			Source:        "local",
			Evener: appwire.EvenerThread{
				Ref:                        "local:sess-2",
				InstanceID:                 "inst-2",
				Capabilities:               appwire.ThreadCapabilities{Send: true},
				MutationStateAuthoritative: true,
				PendingMutations: []appwire.PendingMutation{{
					ClientMutationID: "turn-m-2",
					Method:           appwire.MethodTurnStart,
					ExecutionState:   "accepted",
					Input: []appwire.InputItem{
						{Type: "text", Text: "PENDING_MUTATION_SECRET_PROMPT"},
						{Type: "image", URL: "data:image/png;base64,PENDING_DATA_URL_SECRET", Data: []byte("PENDING_RAW_IMAGE_BYTES")},
					},
					ProjectionState: appwire.MutationProjectionPending,
				}},
				Queue: appwire.QueueState{Depth: 1, Revision: 3, Preview: []string{"QUEUED_PREVIEW_SECRET"}, Texts: []string{"QUEUED_FULL_TEXT_SECRET"}, IDs: []string{"q1"}, ClientMutationIDs: []string{"qm1"}},
			},
			Turns: []appwire.Turn{{
				ID:     "turn_1",
				Status: "inProgress",
				Items: []appwire.ThreadItem{{
					Type:   "userMessage",
					ID:     "item_1",
					Text:   "SUPER_SECRET_PROMPT_TEXT",
					Images: []appwire.InputItem{{Type: "image", Data: []byte("RAW_IMAGE_BYTES_SENTINEL")}},
				}},
			}},
		},
		Turn: appwire.Turn{
			ID:     "turn_1",
			Status: "inProgress",
			Items:  []appwire.ThreadItem{{Type: "userMessage", ID: "item_1", Text: "SUPER_SECRET_PROMPT_TEXT"}},
		},
		ClientMutationID: "m-completed",
		Disposition:      appwire.MutationDispositionApplied,
	}
	if err := store.CommitCompleted("m-completed", fullResp); err != nil {
		t.Fatalf("CommitCompleted: %v", err)
	}
	store.ReleaseOwner(compRes.Owner, fullResp, nil)

	// 3b. completed without Name must strip prompt-derived Preview, LastMessage, and PendingAsk
	prevRes, err := store.Acquire(t.Context(), "m-completed-preview", hash, true, "turn-m-prev")
	if err != nil || prevRes.Owner == nil {
		t.Fatalf("Acquire m-completed-preview: %+v, %v", prevRes, err)
	}
	previewResp := appwire.ThreadStartResponse{
		Thread: appwire.Thread{
			ID:        "sess-preview",
			SessionID: "sess-preview",
			Preview:   "SECRET_PREVIEW_PROMPT",
			CWD:       "/tmp/project",
			Source:    "local",
			Evener: appwire.EvenerThread{
				Ref:         "local:sess-preview",
				InstanceID:  "inst-prev",
				LastMessage: "SECRET_LAST_MESSAGE_PROMPT",
				PendingAsk:  &appwire.PendingAskState{Version: 1, InstanceID: "inst-prev", Calls: []appwire.PendingAskCall{{TurnID: "turn_1", CallID: "call_1"}}},
			},
		},
	}
	if err := store.CommitCompleted("m-completed-preview", previewResp); err != nil {
		t.Fatalf("CommitCompleted preview: %v", err)
	}
	store.ReleaseOwner(prevRes.Owner, previewResp, nil)

	// 4. uncertain persisted
	uncRes, err := store.Acquire(t.Context(), "m-uncertain", hash, false, "")
	if err != nil || uncRes.Owner == nil {
		t.Fatalf("Acquire m-uncertain: %+v, %v", uncRes, err)
	}
	if err := store.MarkUncertain("m-uncertain"); err != nil {
		t.Fatalf("MarkUncertain: %v", err)
	}
	store.ReleaseOwner(uncRes.Owner, appwire.ThreadStartResponse{}, errors.New("uncertain"))
	if len(fullResp.Thread.Evener.PendingMutations) != 1 || fullResp.Thread.Evener.Queue.Depth != 1 || !fullResp.Thread.Evener.MutationStateAuthoritative {
		t.Fatal("CommitCompleted mutated caller ThreadStartResponse")
	}

	rawBytes, err := os.ReadFile(creationStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range [][]byte{
		[]byte("PENDING_MUTATION_SECRET_PROMPT"),
		[]byte("PENDING_DATA_URL_SECRET"),
		[]byte("PENDING_RAW_IMAGE_BYTES"),
		[]byte("UEVORElOR19SQVdfSU1BR0VfQllURVM"),
		[]byte("QUEUED_PREVIEW_SECRET"),
		[]byte("QUEUED_FULL_TEXT_SECRET"),
		[]byte("SECRET_PREVIEW_PROMPT"),
		[]byte("SECRET_LAST_MESSAGE_PROMPT"),
	} {
		if bytes.Contains(rawBytes, forbidden) {
			t.Fatalf("state.json leaked pending/queued prompt or image bytes (%s): %s", forbidden, rawBytes)
		}
	}
	if bytes.Contains(rawBytes, []byte("SUPER_SECRET_PROMPT_TEXT")) ||
		bytes.Contains(rawBytes, []byte("RAW_IMAGE_BYTES_SENTINEL")) ||
		bytes.Contains(rawBytes, []byte("UkFXX0lNQUdFX0JZVEVTX1NFTlRJTkVM")) {
		t.Fatalf("state.json must not persist prompt text or image bytes: %s", rawBytes)
	}

	reloaded, err := NewCreationStore(root)
	if err != nil {
		t.Fatalf("reload NewCreationStore: %v", err)
	}

	// Verify spawned reload
	gotSpawned, err := reloaded.Acquire(t.Context(), "m-spawned", hash, true, "ignored")
	if err != nil {
		t.Fatalf("Acquire reloaded m-spawned: %v", err)
	}
	if gotSpawned.Owner == nil || gotSpawned.Record == nil || gotSpawned.Response != nil {
		t.Fatalf("reloaded spawned = %+v", gotSpawned)
	}
	if gotSpawned.Record.State != CreationStateSpawned ||
		gotSpawned.Record.CreatedRef != "local:sess-1" ||
		gotSpawned.Record.CreatedSessionID != "sess-1" ||
		gotSpawned.Record.CreatedInstanceID != "inst-1" ||
		!gotSpawned.Record.HasInitialInput ||
		gotSpawned.Record.InitialTurnMutationID != "turn-m-1" {
		t.Fatalf("unexpected reloaded spawned record: %+v", gotSpawned.Record)
	}
	reloaded.ReleaseOwner(gotSpawned.Owner, appwire.ThreadStartResponse{}, nil)

	// Verify completed reload
	gotComp, err := reloaded.Acquire(t.Context(), "m-completed", hash, true, "ignored")
	if err != nil {
		t.Fatalf("Acquire reloaded m-completed: %v", err)
	}
	if gotComp.Response == nil || gotComp.Owner != nil {
		t.Fatalf("reloaded completed = %+v, want Response and nil Owner", gotComp)
	}
	if gotComp.Response.Disposition != appwire.MutationDispositionReplayed ||
		gotComp.Response.ClientMutationID != "m-completed" ||
		gotComp.Response.Thread.ID != "sess-2" ||
		gotComp.Response.Thread.Name != "Keep Metadata" ||
		gotComp.Response.Thread.ModelProvider != "openai" ||
		gotComp.Response.Thread.CWD != "/tmp/project" ||
		!gotComp.Response.Thread.Evener.Capabilities.Send ||
		gotComp.Response.Thread.Turns != nil ||
		gotComp.Response.Thread.Evener.PendingMutations != nil ||
		!reflect.DeepEqual(gotComp.Response.Thread.Evener.Queue, appwire.QueueState{}) ||
		gotComp.Response.Thread.Evener.MutationStateAuthoritative ||
		!reflect.DeepEqual(gotComp.Response.Turn, appwire.Turn{ID: "turn_1", Status: "inProgress"}) {
		t.Fatalf("unexpected replayed response: %+v", gotComp.Response)
	}

	gotPrev, err := reloaded.Acquire(t.Context(), "m-completed-preview", hash, true, "ignored")
	if err != nil {
		t.Fatalf("Acquire reloaded m-completed-preview: %v", err)
	}
	if gotPrev.Response == nil ||
		gotPrev.Response.Thread.Preview != "sess-preview" ||
		gotPrev.Response.Thread.Evener.LastMessage != "" ||
		gotPrev.Response.Thread.Evener.PendingAsk != nil {
		t.Fatalf("replayed preview snapshot leaked prompt/ask state: %+v", gotPrev.Response)
	}
	_ = previewResp

	// Verify uncertain reload
	_, err = reloaded.Acquire(t.Context(), "m-uncertain", hash, false, "")
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("uncertain Acquire err = %T (%v), want WireError", err, err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorMutationOutcomeUnknown ||
		data.ClientMutationID != "m-uncertain" ||
		data.MutationOutcome != appwire.MutationOutcomeUnknown ||
		data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("unexpected uncertain error data: %+v", data)
	}
}

func TestCreationStoreInMemoryMode(t *testing.T) {
	t.Parallel()
	store, err := NewCreationStore("")
	if err != nil {
		t.Fatalf("NewCreationStore empty root: %v", err)
	}
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Acquire(t.Context(), "mem-1", hash, false, "")
	if err != nil || res.Owner == nil {
		t.Fatalf("Acquire: %+v, %v", res, err)
	}
	resp := appwire.ThreadStartResponse{
		Thread: appwire.Thread{ID: "t1", SessionID: "t1", Evener: appwire.EvenerThread{Ref: "local:t1", InstanceID: "t1"}},
	}
	if err := store.CommitCompleted("mem-1", resp); err != nil {
		t.Fatalf("CommitCompleted: %v", err)
	}
	store.ReleaseOwner(res.Owner, resp, nil)
	replay, err := store.Acquire(t.Context(), "mem-1", hash, false, "")
	if err != nil || replay.Response == nil || replay.Response.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
}

func TestCreationPayloadHashAndConflict(t *testing.T) {
	t.Parallel()
	base := testCreationParams()
	hEmpty, err := CreationPayloadHash("local", base)
	if err != nil {
		t.Fatal(err)
	}
	withEvener := base
	withEvener.Harness = "evener"
	hEvener, err := CreationPayloadHash("local", withEvener)
	if err != nil {
		t.Fatal(err)
	}
	if hEmpty != hEvener {
		t.Fatalf("empty harness hash %q != evener harness hash %q", hEmpty, hEvener)
	}

	emptySlice := base
	emptySlice.Input = []appwire.InputItem{}
	nilSlice := base
	nilSlice.Input = nil
	hEmptySlice, _ := CreationPayloadHash("local", emptySlice)
	hNilSlice, _ := CreationPayloadHash("local", nilSlice)
	if hEmptySlice != hNilSlice {
		t.Fatalf("empty slice %q != nil slice %q", hEmptySlice, hNilSlice)
	}

	diffSource, _ := CreationPayloadHash("remote-a", base)
	diffModel := base
	diffModel.Model = "openai/gpt-5-mini"
	hDiffModel, _ := CreationPayloadHash("local", diffModel)
	if diffSource == hEmpty || hDiffModel == hEmpty {
		t.Fatal("distinct payloads produced identical hashes")
	}

	store, err := NewCreationStore("")
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Acquire(t.Context(), "m-conflict", hEmpty, true, "tm1")
	if err != nil {
		t.Fatal(err)
	}

	// Conflict while in-flight owner active
	_, err = store.Acquire(t.Context(), "m-conflict", hDiffModel, true, "tm2")
	assertConflictingMutationID(t, err, "m-conflict")

	// Conflict when completed
	resp := appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "s1", Evener: appwire.EvenerThread{Ref: "local:s1"}}}
	if err := store.CommitCompleted("m-conflict", resp); err != nil {
		t.Fatal(err)
	}
	store.ReleaseOwner(res.Owner, resp, nil)
	_, err = store.Acquire(t.Context(), "m-conflict", hDiffModel, true, "tm2")
	assertConflictingMutationID(t, err, "m-conflict")

	// Conflict when uncertain
	res2, err := store.Acquire(t.Context(), "m-unc-conflict", hEmpty, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUncertain("m-unc-conflict"); err != nil {
		t.Fatal(err)
	}
	store.ReleaseOwner(res2.Owner, appwire.ThreadStartResponse{}, errors.New("uncertain"))
	_, err = store.Acquire(t.Context(), "m-unc-conflict", hDiffModel, false, "")
	assertConflictingMutationID(t, err, "m-unc-conflict")
}

func assertConflictingMutationID(t *testing.T, err error, id string) {
	t.Helper()
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T (%v), want WireError", err, err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.EvenerErrorInfo != appwire.ErrorConflictingMutationID ||
		data.ClientMutationID != id ||
		data.MutationOutcome != appwire.MutationOutcomeNotAccepted ||
		data.RetryDisposition != appwire.RetryDispositionNone {
		t.Fatalf("unexpected conflict error data: %+v", data)
	}
}

func TestCreationStoreStrictDecodeAndCorruptFileUntouched(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
	}{
		{name: "invalid json", content: "{not json"},
		{name: "unknown field", content: `{"version":1,"records":[],"extra":true}`},
		{name: "trailing json", content: `{"version":1,"records":[]}{}`},
		{name: "unsupported version", content: `{"version":99,"records":[]}`},
		{name: "invalid state", content: `{"version":1,"records":[{"client_mutation_id":"m1","payload_hash":"h","state":"bogus","updated_at":"2026-11-24T00:00:00Z"}]}`},
		{name: "empty id", content: `{"version":1,"records":[{"client_mutation_id":"","payload_hash":"h","state":"completed","updated_at":"2026-11-24T00:00:00Z"}]}`},
		{name: "duplicate id", content: `{"version":1,"records":[{"client_mutation_id":"m1","payload_hash":"h","state":"completed","updated_at":"2026-11-24T00:00:00Z"},{"client_mutation_id":"m1","payload_hash":"h","state":"completed","updated_at":"2026-11-24T00:00:00Z"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := creationStatePath(root)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err := NewCreationStore(root)
			if err == nil || store != nil {
				t.Fatalf("NewCreationStore = (%v, %v), want (nil, error)", store, err)
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(after) != tc.content {
				t.Fatalf("corrupt state file was modified: got %q, want %q", after, tc.content)
			}
		})
	}
}

func TestCreationStoreStartupNormalizesSpawningToUncertain(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := NewCreationStore(root)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatal(err)
	}
	res, err := store.Acquire(t.Context(), "m-crash-spawning", hash, false, "")
	if err != nil || res.Owner == nil {
		t.Fatalf("Acquire: %+v, %v", res, err)
	}
	// Simulate crash before any completion or ReleaseOwner by reopening from disk.
	reopened, err := NewCreationStore(root)
	if err != nil {
		t.Fatalf("reopened NewCreationStore: %v", err)
	}
	_, err = reopened.Acquire(t.Context(), "m-crash-spawning", hash, false, "")
	var wire appwire.WireError
	if !errors.As(err, &wire) {
		t.Fatalf("err = %T (%v), want WireError", err, err)
	}
	data, ok := appwire.DecodedErrorData(wire.Data)
	if !ok || data.MutationOutcome != appwire.MutationOutcomeUnknown || data.RetryDisposition != appwire.RetryDispositionBlocked {
		t.Fatalf("unexpected error data after spawning crash normalization: %+v", data)
	}
}

func TestCreationStoreAtomicWriteFaults(t *testing.T) {
	t.Parallel()
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatal(err)
	}
	wantFault := errors.New("injected rename fault")

	t.Run("BeforeRename leaves prior state intact", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		failCommit := false
		store, err := newCreationStoreFS(afero.NewOsFs(), root, creationStoreFaults{
			BeforeRename: func() error {
				if failCommit {
					return wantFault
				}
				return nil
			},
		}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		res, err := store.Acquire(t.Context(), "m-before", hash, false, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := store.CommitSpawned("m-before", "local:s1", "s1", "inst-1"); err != nil {
			t.Fatal(err)
		}
		failCommit = true
		err = store.CommitCompleted("m-before", appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "s1"}})
		if !errors.Is(err, wantFault) {
			t.Fatalf("CommitCompleted = %v, want %v", err, wantFault)
		}
		store.ReleaseOwner(res.Owner, appwire.ThreadStartResponse{}, err)

		// In-memory and on-disk state must still be spawned.
		next, err := store.Acquire(t.Context(), "m-before", hash, false, "")
		if err != nil || next.Record == nil || next.Record.State != CreationStateSpawned {
			t.Fatalf("in-memory state after BeforeRename = %+v, %v; want spawned", next, err)
		}
		store.ReleaseOwner(next.Owner, appwire.ThreadStartResponse{}, nil)

		reloaded, err := NewCreationStore(root)
		if err != nil {
			t.Fatal(err)
		}
		diskNext, err := reloaded.Acquire(t.Context(), "m-before", hash, false, "")
		if err != nil || diskNext.Record == nil || diskNext.Record.State != CreationStateSpawned {
			t.Fatalf("disk state after BeforeRename = %+v, %v; want spawned", diskNext, err)
		}
	})

	t.Run("AfterRename updates in-memory state", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		failCommit := false
		store, err := newCreationStoreFS(afero.NewOsFs(), root, creationStoreFaults{
			AfterRename: func() error {
				if failCommit {
					return wantFault
				}
				return nil
			},
		}, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		res, err := store.Acquire(t.Context(), "m-after", hash, false, "")
		if err != nil {
			t.Fatal(err)
		}
		failCommit = true
		resp := appwire.ThreadStartResponse{
			Thread: appwire.Thread{ID: "s2", SessionID: "s2", Evener: appwire.EvenerThread{Ref: "local:s2", InstanceID: "inst-2"}},
		}
		err = store.CommitCompleted("m-after", resp)
		if !errors.Is(err, wantFault) {
			t.Fatalf("CommitCompleted = %v, want %v", err, wantFault)
		}
		store.ReleaseOwner(res.Owner, resp, nil)

		// In-memory state must reflect the renamed completed record.
		replay, err := store.Acquire(t.Context(), "m-after", hash, false, "")
		if err != nil || replay.Response == nil || replay.Response.Thread.ID != "s2" {
			t.Fatalf("in-memory state after AfterRename = %+v, %v; want completed replay", replay, err)
		}
	})
}

func TestCreationStorePruningSevenDaysAndMax200Terminal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	store, err := newCreationStoreFS(afero.NewOsFs(), root, creationStoreFaults{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatal(err)
	}

	// Old non-terminal spawning and spawned records must survive age pruning and cap.
	spawningOld, err := store.Acquire(t.Context(), "keep-spawning", hash, false, "")
	if err != nil {
		t.Fatal(err)
	}
	spawnedOld, err := store.Acquire(t.Context(), "keep-spawned", hash, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitSpawned("keep-spawned", "local:keep", "keep", "keep"); err != nil {
		t.Fatal(err)
	}
	store.ReleaseOwner(spawnedOld.Owner, appwire.ThreadStartResponse{}, nil)

	// Old terminal records (> 7 days)
	resOldComp, _ := store.Acquire(t.Context(), "old-completed", hash, false, "")
	_ = store.CommitCompleted("old-completed", appwire.ThreadStartResponse{Thread: appwire.Thread{ID: "old-c"}})
	store.ReleaseOwner(resOldComp.Owner, appwire.ThreadStartResponse{}, nil)

	resOldUnc, _ := store.Acquire(t.Context(), "old-uncertain", hash, false, "")
	_ = store.MarkUncertain("old-uncertain")
	store.ReleaseOwner(resOldUnc.Owner, appwire.ThreadStartResponse{}, nil)

	// Advance 8 days; write 205 fresh terminal records at distinct timestamps.
	now = now.Add(8 * 24 * time.Hour)
	for i := range 205 {
		now = now.Add(time.Second)
		id := fmt.Sprintf("term-%03d", i)
		res, err := store.Acquire(t.Context(), id, hash, false, "")
		if err != nil {
			t.Fatalf("Acquire %s: %v", id, err)
		}
		if i%2 == 0 {
			err = store.CommitCompleted(id, appwire.ThreadStartResponse{Thread: appwire.Thread{ID: id}})
		} else {
			err = store.MarkUncertain(id)
		}
		if err != nil {
			t.Fatalf("terminal commit %s: %v", id, err)
		}
		store.ReleaseOwner(res.Owner, appwire.ThreadStartResponse{}, nil)
	}
	_ = spawningOld

	raw, err := os.ReadFile(creationStatePath(root))
	if err != nil {
		t.Fatal(err)
	}
	var snap creationSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	present := map[string]CreationRecord{}
	terminalCount := 0
	for _, rec := range snap.Records {
		present[rec.ClientMutationID] = rec
		if rec.State == CreationStateCompleted || rec.State == CreationStateUncertain {
			terminalCount++
		}
	}
	if terminalCount != creationMaxTerminalRecords {
		t.Fatalf("terminalCount = %d, want %d", terminalCount, creationMaxTerminalRecords)
	}
	if _, ok := present["keep-spawning"]; !ok {
		t.Fatal("spawning record was pruned")
	}
	if _, ok := present["keep-spawned"]; !ok {
		t.Fatal("spawned record was pruned")
	}
	if _, ok := present["old-completed"]; ok {
		t.Fatal("old completed record was not pruned")
	}
	if _, ok := present["old-uncertain"]; ok {
		t.Fatal("old uncertain record was not pruned")
	}
	for i := range 5 {
		if _, ok := present[fmt.Sprintf("term-%03d", i)]; ok {
			t.Fatalf("oldest terminal record term-%03d should have been evicted", i)
		}
	}
	if _, ok := present["term-005"]; !ok {
		t.Fatal("term-005 should have been retained")
	}
}

func TestCreationStoreConcurrentAcquireCoalescingAndWaiterCancellation(t *testing.T) {
	t.Parallel()
	waitRegistered := make(chan struct{}, 4)
	store, err := newCreationStoreFS(
		afero.NewOsFs(),
		t.TempDir(),
		creationStoreFaults{
			OnWait: func() { waitRegistered <- struct{}{} },
		},
		time.Now,
	)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := CreationPayloadHash("local", testCreationParams())
	if err != nil {
		t.Fatal(err)
	}
	ownerRes, err := store.Acquire(t.Context(), "m-coal", hash, true, "turn-m-coal")
	if err != nil || ownerRes.Owner == nil {
		t.Fatalf("owner Acquire: %+v, %v", ownerRes, err)
	}

	// Waiter 1 cancels while owner is still working.
	waiterCancelCtx, cancelWaiter := context.WithCancel(t.Context())
	waiter1Err := make(chan error, 1)
	go func() {
		_, err := store.Acquire(waiterCancelCtx, "m-coal", hash, true, "turn-m-coal")
		waiter1Err <- err
	}()
	// Waiter 2 waits for completion.
	waiter2Res := make(chan CreationAcquireResult, 1)
	waiter2Err := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		res, err := store.Acquire(t.Context(), "m-coal", hash, true, "turn-m-coal")
		waiter2Res <- res
		waiter2Err <- err
	})

	<-waitRegistered
	<-waitRegistered
	cancelWaiter()
	if err := <-waiter1Err; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err = %v, want context.Canceled", err)
	}

	// Owner finishes and releases.
	resp := appwire.ThreadStartResponse{
		Thread:           appwire.Thread{ID: "sess-coal", SessionID: "sess-coal", Evener: appwire.EvenerThread{Ref: "local:sess-coal"}},
		Turn:             appwire.Turn{ID: "turn_1", Status: "inProgress"},
		ClientMutationID: "m-coal",
		Disposition:      appwire.MutationDispositionApplied,
	}
	if err := store.CommitCompleted("m-coal", resp); err != nil {
		t.Fatal(err)
	}
	store.ReleaseOwner(ownerRes.Owner, resp, nil)
	wg.Wait()

	if err := <-waiter2Err; err != nil {
		t.Fatalf("waiter2 err = %v", err)
	}
	got2 := <-waiter2Res
	if got2.Response == nil || got2.Response.Disposition != appwire.MutationDispositionReplayed || got2.Response.ClientMutationID != "m-coal" || got2.Response.Thread.ID != "sess-coal" {
		t.Fatalf("waiter2 response = %+v", got2.Response)
	}

	// Owner failure propagates to coalesced waiter and clears owner slot.
	owner2, err := store.Acquire(t.Context(), "m-coal-fail", hash, false, "")
	if err != nil {
		t.Fatal(err)
	}
	waitErrCh := make(chan error, 1)
	go func() {
		_, err := store.Acquire(t.Context(), "m-coal-fail", hash, false, "")
		waitErrCh <- err
	}()
	<-waitRegistered
	wantErr := appwire.Unavailable("transient failure")
	_ = store.RollbackSpawning("m-coal-fail")
	store.ReleaseOwner(owner2.Owner, appwire.ThreadStartResponse{}, wantErr)
	if err := <-waitErrCh; !errors.Is(err, wantErr) {
		t.Fatalf("coalesced failure err = %v, want %v", err, wantErr)
	}
}
