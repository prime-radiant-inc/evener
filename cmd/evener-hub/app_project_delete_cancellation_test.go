package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// Unlike the retry regression, this request has no committed deletion record.
// Cancellation while reserving B must not turn A into a partial deletion.
func TestHubRPCFreshProjectDeleteCanceledBeforeCommit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root, workDir, hubRoot := t.TempDir(), t.TempDir(), t.TempDir()
		project, err := identifier.ResolveProject(workDir)
		if err != nil {
			t.Fatal(err)
		}
		stateDir := filepath.Join(root, "projects", project.ID)
		first, second := projectDeleteCanonicalSessionIDs[0], projectDeleteCanonicalSessionIDs[1]
		artifacts := make(map[string][]byte)
		for _, id := range []string{first, second} {
			buildRPCSessionWithWorkingDir(t, stateDir, id, project.CanonicalPath)
			for _, suffix := range []string{".meta.json", ".transcript.jsonl"} {
				path := filepath.Join(stateDir, "sessions", id+suffix)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				artifacts[path] = data
			}
		}
		past := hubcore.NewPastIndex(filepath.Join(root, "projects", "*"))
		if _, err := past.Rebuild(); err != nil {
			t.Fatal(err)
		}
		if entries := past.All(); len(entries) != 2 {
			t.Fatalf("fresh project fixture did not index both sessions: %+v", entries)
		}
		locks := hubcore.NewResumeLocks()
		web := NewWebServer(hubcore.WebConfig{
			StateDir: root, HubStateRoot: hubRoot, LaunchConfigRoot: t.TempDir(), PluginRoot: t.TempDir(),
			Past: past, ResumeLocks: locks, CredsStore: newTestCredentialsStore(t),
		})
		if records := web.cfg.DeletionStore.Deleting(); len(records) != 0 {
			t.Fatalf("fresh fixture already has deletion records: %+v", records)
		}
		for _, id := range []string{first, second} {
			if state, exists := web.cfg.DeletionStore.TargetState("local:"+id, id); exists {
				t.Fatalf("fresh fixture already fences %s: %s", id, state)
			}
		}

		// This probes the real external file reservation, not an internal seam.
		reserveFirst := func() error {
			owner, err := llm.NewSessionAPILogger(stateDir)
			if err != nil {
				return err
			}
			defer func() { _ = owner.Close() }()
			return owner.ReserveSession(first)
		}
		blocked := locks.For(second)
		blocked.Lock()
		release := sync.OnceFunc(blocked.Unlock)
		defer release()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		params, err := json.Marshal(appwire.ProjectDeleteParams{Key: project.ID, WorkingDir: project.CanonicalPath})
		if err != nil {
			t.Fatal(err)
		}

		// The broken path logs canceled navigation after committing/removing A.
		// Capture stderr and assert silence; unexpected diagnostics remain part
		// of the failure report rather than being discarded.
		stderr, err := os.CreateTemp(t.TempDir(), "fresh-delete-stderr-")
		if err != nil {
			t.Fatal(err)
		}
		previousStderr := os.Stderr
		os.Stderr = stderr
		defer func() {
			cancel()
			release()
			synctest.Wait()
			os.Stderr = previousStderr
			_ = stderr.Close()
		}()
		type result struct {
			response any
			err      error
		}
		completed := make(chan result, 1)
		go func() {
			response, err := web.appRPC.Router().Dispatch(ctx, appwire.Request{Method: appwire.MethodEvenerProjectDelete, Params: params})
			completed <- result{response, err}
		}()
		synctest.Wait()
		if locks.For(first).TryLock() {
			locks.For(first).Unlock()
			t.Fatal("fresh deletion did not acquire A before waiting for B")
		}
		if err := reserveFirst(); !errors.Is(err, llm.ErrAPILogTargetLocked) {
			t.Fatalf("fresh deletion did not reserve A's API log before waiting for B: %v", err)
		}
		select {
		case got := <-completed:
			t.Fatalf("fresh deletion returned before cancellation with B held: %+v", got)
		default:
		}

		cancel()
		synctest.Wait()
		select {
		case got := <-completed:
			if got.err == nil {
				t.Errorf("canceled fresh deletion reported success: %+v", got.response)
			}
		default:
			t.Error("canceled fresh deletion still waits for B")
		}
		if !locks.For(first).TryLock() {
			t.Error("canceled fresh deletion retained A's alias")
		} else {
			locks.For(first).Unlock()
		}
		if err := reserveFirst(); err != nil {
			t.Errorf("canceled fresh deletion retained A's API-log reservation: %v", err)
		}
		if blocked.TryLock() {
			blocked.Unlock()
			t.Error("canceled fresh deletion released B's other owner")
		}
		for path, before := range artifacts {
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, before) {
				t.Errorf("canceled fresh deletion changed saved artifact %s: read error=%v", filepath.Base(path), err)
			}
		}
		// Reopen the actual durable store so a committed tombstone cannot hide
		// behind stale in-memory state or successful artifact cleanup.
		store, err := hubcore.NewDeletionStore(hubRoot)
		if err != nil {
			t.Fatal(err)
		}
		if records := store.Deleting(); len(records) != 0 {
			t.Errorf("canceled fresh deletion committed a deletion record: %+v", records)
		}
		for _, id := range []string{first, second} {
			if state, exists := store.TargetState("local:"+id, id); exists {
				t.Errorf("canceled fresh deletion committed target tombstone %s: %s", id, state)
			}
		}
		diagnostic, err := os.ReadFile(stderr.Name())
		if err != nil {
			t.Fatal(err)
		}
		if len(diagnostic) != 0 {
			t.Errorf("canceled fresh deletion emitted unexpected diagnostics: %s", diagnostic)
		}
		// Every assertion above happens while B is still owned. Deferred
		// release also drains the handler when a precondition fails.
	})
}
