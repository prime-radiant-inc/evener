package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/testwait"
)

func TestFilesSearchValidationAndSpawnCWD(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	repo := filepath.Join(home, "proj")
	mustWriteFileForSearchTest(t, filepath.Join(repo, "cmd", "main.go"), "package main\n")
	mustWriteFileForSearchTest(t, filepath.Join(repo, "README.md"), "# hi\n")
	mustWriteFileForSearchTest(t, filepath.Join(repo, ".env"), "KEY=1\n")

	plainFile := filepath.Join(home, "not-a-dir.txt")
	mustWriteFileForSearchTest(t, plainFile, "file\n")

	srv := newHubRPCTestServer(t, hubcore.WebConfig{})
	defer srv.Close()
	client := dialHubRPCInitialized(t, srv)

	for _, tc := range []struct {
		name   string
		params appwire.FilesSearchParams
	}{
		{name: "neither ref nor cwd", params: appwire.FilesSearchParams{Query: "a"}},
		{name: "both ref and cwd", params: appwire.FilesSearchParams{Ref: "local:0123", CWD: repo, Query: "a"}},
		{name: "invalid ref", params: appwire.FilesSearchParams{Ref: "not-a-ref", Query: "a"}},
		{name: "query exceeds 256 runes", params: appwire.FilesSearchParams{CWD: repo, Query: strings.Repeat("a", 257)}},
		{name: "relative cwd", params: appwire.FilesSearchParams{CWD: "relative/dir", Query: "a"}},
		{name: "file path cwd", params: appwire.FilesSearchParams{CWD: plainFile, Query: "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var resp appwire.FilesSearchResponse
			err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, tc.params, &resp)
			assertWireCode(t, err, appwire.CodeInvalidParams)
		})
	}

	t.Run("expands tilde and strips ASCII control chars", func(t *testing.T) {
		var resp appwire.FilesSearchResponse
		err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			CWD:   "~/proj",
			Query: "main\x00\x1f",
		}, &resp)
		if err != nil {
			t.Fatalf("search ~/proj: %v", err)
		}
		if len(resp.Items) == 0 || resp.Items[0].Path != "cmd/main.go" {
			t.Fatalf("items = %+v, want cmd/main.go first", resp.Items)
		}
	})

	t.Run("missing spawn directory encodes non-nil empty items array", func(t *testing.T) {
		var raw json.RawMessage
		err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			CWD:   filepath.Join(home, "not-created-yet"),
			Query: "main",
		}, &raw)
		if err != nil {
			t.Fatalf("missing spawn cwd errored: %v", err)
		}
		if string(raw) != `{"items":[]}` {
			t.Fatalf("raw JSON = %s, want {\"items\":[]}", raw)
		}
	})

	t.Run("clamps limit default 25 and max 50", func(t *testing.T) {
		bigRepo := t.TempDir()
		for i := range 60 {
			mustWriteFileForSearchTest(t, filepath.Join(bigRepo, "file_"+string(rune('a'+i/26))+string(rune('a'+i%26))+".go"), "package p\n")
		}

		var defResp appwire.FilesSearchResponse
		if err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			CWD:   bigRepo,
			Query: "",
			Limit: 0,
		}, &defResp); err != nil {
			t.Fatalf("default limit search: %v", err)
		}
		if len(defResp.Items) != 25 {
			t.Fatalf("default limit returned %d items, want 25", len(defResp.Items))
		}

		var maxResp appwire.FilesSearchResponse
		if err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			CWD:   bigRepo,
			Query: "",
			Limit: 200,
		}, &maxResp); err != nil {
			t.Fatalf("over-cap limit search: %v", err)
		}
		if len(maxResp.Items) != 50 {
			t.Fatalf("over-cap limit returned %d items, want 50", len(maxResp.Items))
		}

		var smallResp appwire.FilesSearchResponse
		if err := client.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			CWD:   bigRepo,
			Query: "",
			Limit: 3,
		}, &smallResp); err != nil {
			t.Fatalf("explicit limit=3 search: %v", err)
		}
		if len(smallResp.Items) != 3 {
			t.Fatalf("limit=3 returned %d items, want 3", len(smallResp.Items))
		}
	})
}

func TestFilesSearchSessionCWDWorktreeSwitchAndCache(t *testing.T) {
	worktreeA := t.TempDir()
	worktreeB := t.TempDir()
	mustWriteFileForSearchTest(t, filepath.Join(worktreeA, "alpha_only.go"), "package a\n")
	mustWriteFileForSearchTest(t, filepath.Join(worktreeB, "beta_only.go"), "package b\n")

	sessionID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
	ref := localAppRef(sessionID)
	src := &scriptedAppSource{
		id: appwire.LocalSourceID,
		thread: appwire.Thread{
			ID:     sessionID,
			CWD:    worktreeA,
			Evener: appwire.EvenerThread{Ref: ref},
		},
	}
	sources := appsource.NewRegistry()
	sources.Add(src)
	roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
		SessionID: sessionID,
		Status:    appwire.ThreadStatusIdle,
	})
	cfg := hubcore.WebConfig{Roster: roster}

	var nowMu sync.Mutex
	curTime := time.Unix(1_700_000_000, 0)
	var walkCount atomic.Int32

	svc := newFileSearchService(cfg, sources)
	svc.now = func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return curTime
	}
	origWalk := svc.walk
	svc.walk = func(ctx context.Context, root string, opts execenv.ProjectFilesOptions) (execenv.ProjectFilesResult, error) {
		walkCount.Add(1)
		return origWalk(ctx, root, opts)
	}

	// First search warms the cache for worktreeA.
	respA1, err := svc.Search(t.Context(), appwire.FilesSearchParams{Ref: ref, Query: "only"})
	if err != nil {
		t.Fatalf("search worktreeA: %v", err)
	}
	if len(respA1.Items) != 1 || respA1.Items[0].Path != "alpha_only.go" {
		t.Fatalf("worktreeA items = %+v, want [alpha_only.go]", respA1.Items)
	}
	if got := walkCount.Load(); got != 1 {
		t.Fatalf("walks after cold search = %d, want 1", got)
	}

	// Repeated query on same canonical root hits the 5s cache.
	respA2, err := svc.Search(t.Context(), appwire.FilesSearchParams{Ref: ref, Query: "alpha"})
	if err != nil {
		t.Fatalf("cached search worktreeA: %v", err)
	}
	if len(respA2.Items) != 1 || respA2.Items[0].Path != "alpha_only.go" {
		t.Fatalf("cached worktreeA items = %+v", respA2.Items)
	}
	if got := walkCount.Load(); got != 1 {
		t.Fatalf("walks after cached search = %d, want 1", got)
	}

	// Mid-session worktree switch: daemon now reports worktreeB even though
	// worktreeA's 5s cache entry is still fresh.
	src.thread.CWD = worktreeB
	respB, err := svc.Search(t.Context(), appwire.FilesSearchParams{Ref: ref, Query: "only"})
	if err != nil {
		t.Fatalf("search after worktree switch: %v", err)
	}
	if len(respB.Items) != 1 || respB.Items[0].Path != "beta_only.go" {
		t.Fatalf("worktreeB items = %+v, want [beta_only.go]", respB.Items)
	}
	if got := walkCount.Load(); got != 2 {
		t.Fatalf("walks after worktree switch = %d, want 2", got)
	}

	// Advance clock past the 5s TTL and verify worktreeB cache entry expires.
	nowMu.Lock()
	curTime = curTime.Add(5 * time.Second)
	nowMu.Unlock()
	if _, err := svc.Search(t.Context(), appwire.FilesSearchParams{Ref: ref, Query: "beta"}); err != nil {
		t.Fatalf("post-expiry search: %v", err)
	}
	if got := walkCount.Load(); got != 3 {
		t.Fatalf("walks after 5s expiry = %d, want 3", got)
	}

	t.Run("archived session reload, pruned worktree, and deletion fence", func(t *testing.T) {
		root := t.TempDir()
		archivedCWD := t.TempDir()
		mustWriteFileForSearchTest(t, filepath.Join(archivedCWD, "archived_file.go"), "package arch\n")

		proj := filepath.Join(root, "projects", "project-arch-0000000000")
		if err := os.MkdirAll(filepath.Join(proj, "sessions"), 0o755); err != nil {
			t.Fatal(err)
		}
		archID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
		if err := schema.SaveSessionMeta(proj, schema.SessionMeta{
			ID: archID, UpdatedAt: time.Now(), OriginalPrompt: "archived",
			EnvInfo: schema.EnvironmentInfo{WorkingDir: archivedCWD},
		}); err != nil {
			t.Fatal(err)
		}
		past := newRebuiltPastIndex(t, filepath.Join(root, "projects", "*"))
		store, err := hubcore.NewDeletionStore(root)
		if err != nil {
			t.Fatal(err)
		}
		archCfg := hubcore.WebConfig{Past: past, DeletionStore: store, ResumeLocks: hubcore.NewResumeLocks()}
		srv := newHubRPCTestServer(t, archCfg)
		defer srv.Close()
		rpc := dialHubRPCInitialized(t, srv)

		var archResp appwire.FilesSearchResponse
		if err := rpc.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			Ref:   localAppRef(archID),
			Query: "archived",
		}, &archResp); err != nil {
			t.Fatalf("archived session search: %v", err)
		}
		if len(archResp.Items) != 1 || archResp.Items[0].Path != "archived_file.go" {
			t.Fatalf("archived items = %+v, want [archived_file.go]", archResp.Items)
		}

		// Pruned worktree (directory deleted on disk) returns non-nil empty [].
		if err := os.RemoveAll(archivedCWD); err != nil {
			t.Fatal(err)
		}
		var raw json.RawMessage
		if err := rpc.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			Ref:   localAppRef(archID),
			Query: "archived",
		}, &raw); err != nil {
			t.Fatalf("pruned worktree search errored: %v", err)
		}
		if string(raw) != `{"items":[]}` {
			t.Fatalf("pruned worktree raw JSON = %s, want {\"items\":[]}", raw)
		}

		// Active deletion fence refuses the session search with TargetDeleted.
		if _, err := store.Begin("project-arch-0000000000", []hubcore.DeletionTarget{{
			Ref:      localAppRef(archID),
			ThreadID: archID,
		}}); err != nil {
			t.Fatal(err)
		}
		var fencedResp appwire.FilesSearchResponse
		ferr := rpc.Request(t.Context(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{
			Ref:   localAppRef(archID),
			Query: "archived",
		}, &fencedResp)
		var wireErr appwire.WireError
		if !errors.As(ferr, &wireErr) {
			t.Fatalf("deletion-fenced error = %T %v, want WireError", ferr, ferr)
		}
		if data, ok := wireErr.Data.(map[string]any); !ok || data["mutationOutcome"] != string(appwire.MutationOutcomeTargetDeleted) {
			t.Fatalf("deletion-fenced wire error data = %#v, want TargetDeleted", wireErr.Data)
		}
	})
}

func TestFilesSearchRemoteSingleHopAndProxyGuards(t *testing.T) {
	t.Run("forwards remote session ref single-hop rewritten to local and propagates MethodNotFound", func(t *testing.T) {
		var sawMethodNotFound bool
		remoteClient, calls, _ := scriptedAdminRemote(t, func(method string, params json.RawMessage) hostAdminReply {
			if method != appwire.MethodEvenerFilesSearch {
				return hostAdminReply{wireErr: &appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "unexpected"}}
			}
			var got appwire.FilesSearchParams
			_ = json.Unmarshal(params, &got)
			if got.Query == "older-hub" || sawMethodNotFound {
				return hostAdminReply{wireErr: &appwire.WireError{Code: appwire.CodeMethodNotFound, Message: "method not found"}}
			}
			return hostAdminReply{result: appwire.FilesSearchResponse{
				Items: []appwire.FileSearchItem{{Path: "remote/file.go", Kind: "file"}},
			}}
		}, false)

		remoteSource := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
			return remoteClient, nil
		})
		remoteSource.SetHostOnline(func() bool { return true })

		sources := appsource.NewRegistry()
		sources.Add(remoteSource)
		hosts, err := hostreg.New([]hostreg.Host{{Name: "h1", SSH: "h1.example"}})
		if err != nil {
			t.Fatal(err)
		}
		svc := newFileSearchService(hubcore.WebConfig{RemoteHostRegistry: hosts}, sources)

		sessionID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
		resp, err := svc.Search(t.Context(), appwire.FilesSearchParams{
			Ref:   "h1:" + sessionID,
			Query: "file",
			Limit: 10,
		})
		if err != nil {
			t.Fatalf("remote search: %v", err)
		}
		if len(resp.Items) != 1 || resp.Items[0].Path != "remote/file.go" {
			t.Fatalf("remote items = %+v, want [remote/file.go]", resp.Items)
		}

		recorded := calls()
		idx := slices.IndexFunc(recorded, func(c hostAdminCall) bool { return c.method == appwire.MethodEvenerFilesSearch })
		if idx < 0 {
			t.Fatalf("remote SearchFiles was not forwarded: %+v", recorded)
		}
		var forwarded appwire.FilesSearchParams
		if err := json.Unmarshal(recorded[idx].params, &forwarded); err != nil {
			t.Fatal(err)
		}
		if forwarded.Ref != appwire.LocalRef(sessionID).String() {
			t.Fatalf("forwarded Ref = %q, want %q", forwarded.Ref, appwire.LocalRef(sessionID).String())
		}

		// Older remote hub returning CodeMethodNotFound propagates verbatim.
		sawMethodNotFound = true
		_, mnfErr := svc.Search(t.Context(), appwire.FilesSearchParams{
			Ref:   "h1:" + sessionID,
			Query: "older-hub",
		})
		assertWireCode(t, mnfErr, appwire.CodeMethodNotFound)

		// Remote-originated call carrying a non-local ref must be refused single-hop.
		remoteOriginCtx := withHostRoutingOrigin(t.Context(), hostRoutingOriginBridge)
		_, hopErr := svc.Search(remoteOriginCtx, appwire.FilesSearchParams{
			Ref:   "h1:" + sessionID,
			Query: "file",
		})
		assertWireCode(t, hopErr, appwire.CodeInvalidParams)
	})

	t.Run("unattached remote host fails closed without dialing or local fallback", func(t *testing.T) {
		localDir := t.TempDir()
		mustWriteFileForSearchTest(t, filepath.Join(localDir, "local_trap.go"), "package trap\n")

		var dialed atomic.Bool
		remoteSource := appsource.NewRemoteHubSource("h1", nil, func(context.Context, string) (*appwire.Client, error) {
			dialed.Store(true)
			return nil, errors.New("should not dial")
		})
		remoteSource.SetHostOnline(func() bool { return false })

		sources := appsource.NewRegistry()
		sources.Add(remoteSource)
		svc := newFileSearchService(hubcore.WebConfig{}, sources)

		sessionID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
		_, err := svc.Search(t.Context(), appwire.FilesSearchParams{
			Ref:   "h1:" + sessionID,
			Query: "trap",
		})
		assertWireCode(t, err, appwire.CodeUnavailable)
		if dialed.Load() {
			t.Fatal("unattached host dialed instead of failing closed")
		}
	})

	t.Run("evener/host/request rejects evener/files/search carrying ref", func(t *testing.T) {
		controller, _, calls := scriptedHostAdmin(t, true, func(string, json.RawMessage) hostAdminReply {
			return okReply()
		})
		paramsJSON, _ := json.Marshal(appwire.FilesSearchParams{Ref: "local:0123", Query: "a"})
		_, err := controller.Request(t.Context(), appwire.HostRequestParams{
			Host:   "m4",
			Method: appwire.MethodEvenerFilesSearch,
			Params: paramsJSON,
		})
		assertWireCode(t, err, appwire.CodeInvalidParams)
		if slices.ContainsFunc(calls(), func(c hostAdminCall) bool { return c.method != appwire.MethodInitialize }) {
			t.Fatalf("proxy forwarded files/search with ref (%+v), want local InvalidParams refusal", calls())
		}
	})
}

type blockingFileSearchSource struct {
	*scriptedAppSource
	gate    <-chan struct{}
	started func()
}

func (b *blockingFileSearchSource) ReadThread(ctx context.Context, params appwire.ThreadReadParams) (appwire.ThreadReadResponse, error) {
	if !params.IncludeTurns {
		b.started()
		select {
		case <-b.gate:
		case <-ctx.Done():
			return appwire.ThreadReadResponse{}, ctx.Err()
		}
	}
	return b.scriptedAppSource.ReadThread(ctx, params)
}

func (b *blockingFileSearchSource) InterruptTurn(context.Context, appwire.TurnInterruptParams) (appwire.TurnInterruptResponse, error) {
	return appwire.TurnInterruptResponse{}, nil
}

func TestFilesSearchLocalDoesNotHeadOfLineBlockConnection(t *testing.T) {
	t.Run("stalled local ref search does not block same-connection requests", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileForSearchTest(t, filepath.Join(dir, "main.go"), "package main\n")

		sessionID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
		ref := localAppRef(sessionID)
		gate := make(chan struct{})
		openGate := sync.OnceFunc(func() { close(gate) })

		var started atomic.Bool
		src := &blockingFileSearchSource{
			scriptedAppSource: &scriptedAppSource{
				id: appwire.LocalSourceID,
				thread: appwire.Thread{
					ID:     sessionID,
					CWD:    dir,
					Evener: appwire.EvenerThread{Ref: ref},
				},
			},
			gate:    gate,
			started: func() { started.Store(true) },
		}
		sources := appsource.NewRegistry()
		sources.Add(src)
		roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			SessionID: sessionID,
			Status:    appwire.ThreadStatusIdle,
		})
		server := newHubAppServer(testWebConfig(t, hubcore.WebConfig{Roster: roster}), sources)
		wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
		t.Cleanup(wire.Close)
		t.Cleanup(openGate)
		client := dialHubRPC(t, wire)
		t.Cleanup(func() { _ = client.Close() })
		mustInitializeHubClient(t, client)

		searchDone := make(chan error, 1)
		go func() {
			var resp appwire.FilesSearchResponse
			searchDone <- client.Request(context.Background(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{Ref: ref, Query: "main"}, &resp)
		}()
		testwait.Until(t, "local evener/files/search to reach ReadThread", started.Load)

		validateDone := make(chan error, 1)
		go func() {
			var out appwire.PathValidateResponse
			validateDone <- client.Request(context.Background(), appwire.MethodEvenerPathValidate, appwire.PathValidateParams{Path: dir}, &out)
		}()
		select {
		case err := <-validateDone:
			if err != nil {
				t.Fatalf("same-connection path/validate failed: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("same-connection request waited behind stalled local evener/files/search")
		}

		if _, err := client.ThreadRead(context.Background(), appwire.ThreadReadParams{Ref: ref, IncludeTurns: true}); err != nil {
			t.Fatalf("same-connection thread/read failed: %v", err)
		}
		select {
		case err := <-searchDone:
			t.Fatalf("stalled local evener/files/search answered before gate opened: %v", err)
		default:
		}
		openGate()
		if err := <-searchDone; err != nil {
			t.Fatalf("unblocked local evener/files/search failed: %v", err)
		}
	})

	t.Run("stalled explicit cwd CanonicalizeDir does not block interrupt or remote pool and bounds local pool", func(t *testing.T) {
		dir := t.TempDir()
		mustWriteFileForSearchTest(t, filepath.Join(dir, "main.go"), "package main\n")
		sessionID := identifier.EncodeUUID(uuid.Must(uuid.NewV7()))
		ref := localAppRef(sessionID)

		gate := make(chan struct{})
		openGate := sync.OnceFunc(func() { close(gate) })
		var blockedCount atomic.Int32

		origCanonicalize := defaultSearchCanonicalizeDir
		defaultSearchCanonicalizeDir = func(p string) (string, error) {
			blockedCount.Add(1)
			<-gate
			return origCanonicalize(p)
		}
		t.Cleanup(func() { defaultSearchCanonicalizeDir = origCanonicalize })

		remoteClient, remoteCalls, _ := scriptedAdminRemote(t, func(method string, _ json.RawMessage) hostAdminReply {
			if method == appwire.MethodEvenerFilesSearch {
				return hostAdminReply{result: appwire.FilesSearchResponse{
					Items: []appwire.FileSearchItem{{Path: "remote/main.go", Kind: "file"}},
				}}
			}
			return okReply()
		}, true)
		remoteSource := appsource.NewRemoteHubSource("m4", nil, func(context.Context, string) (*appwire.Client, error) {
			return remoteClient, nil
		})
		remoteSource.SetHostOnline(func() bool { return true })
		localSrc := &blockingFileSearchSource{
			scriptedAppSource: &scriptedAppSource{
				id: appwire.LocalSourceID,
				thread: appwire.Thread{
					ID:     sessionID,
					CWD:    dir,
					Evener: appwire.EvenerThread{Ref: ref},
				},
			},
			gate:    gate,
			started: func() {},
		}
		sources := appsource.NewRegistry()
		sources.Add(localSrc)
		sources.Add(remoteSource)
		roster := hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			SessionID: sessionID,
			Status:    appwire.ThreadStatusActive,
		})
		hosts, err := hostreg.New([]hostreg.Host{{Name: "m4", SSH: "m4.example"}})
		if err != nil {
			t.Fatal(err)
		}
		server := newHubAppServer(testWebConfig(t, hubcore.WebConfig{
			Roster:             roster,
			RemoteHostRegistry: hosts,
		}), sources)
		wire := httptest.NewServer(http.HandlerFunc(server.ServeWebSocket))
		t.Cleanup(wire.Close)
		t.Cleanup(openGate)
		client := dialHubRPC(t, wire)
		t.Cleanup(func() { _ = client.Close() })
		mustInitializeHubClient(t, client)

		cwdSearchDone := make(chan error, 1)
		go func() {
			var resp appwire.FilesSearchResponse
			cwdSearchDone <- client.Request(context.Background(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{CWD: dir, Query: "main"}, &resp)
		}()
		testwait.Until(t, "cwd evener/files/search to reach CanonicalizeDir", func() bool {
			return blockedCount.Load() == 1
		})

		// Inline turn/interrupt on the serial worker must complete immediately.
		interruptDone := make(chan error, 1)
		go func() {
			var out appwire.TurnInterruptResponse
			ierr := client.Request(context.Background(), appwire.MethodTurnInterrupt, appwire.TurnInterruptParams{
				Ref:                ref,
				ClientMutationID:   "interrupt-1",
				ExpectedInstanceID: sessionID,
			}, &out)
			interruptDone <- ierr
		}()
		select {
		case ierr := <-interruptDone:
			if ierr != nil {
				t.Fatalf("same-connection turn/interrupt failed: %v", ierr)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("same-connection turn/interrupt waited behind stalled cwd evener/files/search")
		}

		// Saturate the local pool and confirm excess local searches fail Unavailable
		// without blocking serial worker requests or remote host searches.
		var held []<-chan error
		for i := 2; ; i++ {
			if i > 1000 {
				t.Fatal("local concurrent request pool never saturated")
			}
			ch := make(chan error, 1)
			go func() {
				var resp appwire.FilesSearchResponse
				ch <- client.Request(context.Background(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{CWD: dir, Query: "main"}, &resp)
			}()
			testwait.Until(t, "local search to block or be refused", func() bool {
				return len(ch) > 0 || int(blockedCount.Load()) == i
			})
			if len(ch) == 0 {
				held = append(held, ch)
				continue
			}
			var wireErr appwire.WireError
			if err := <-ch; !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeUnavailable {
				t.Fatalf("excess local search = %v, want CodeUnavailable", err)
			}
			break
		}

		var remoteResp appwire.FilesSearchResponse
		if err := client.Request(context.Background(), appwire.MethodEvenerFilesSearch, appwire.FilesSearchParams{Ref: "m4:" + sessionID, Query: "main"}, &remoteResp); err != nil {
			t.Fatalf("remote search while local pool full failed: %v", err)
		}
		if len(remoteResp.Items) != 1 || remoteResp.Items[0].Path != "remote/main.go" || len(remoteCalls()) == 0 {
			t.Fatalf("unexpected remote search response: %+v", remoteResp)
		}

		openGate()
		if err := <-cwdSearchDone; err != nil {
			t.Fatalf("unblocked cwd evener/files/search failed: %v", err)
		}
		for _, ch := range held {
			if err := <-ch; err != nil {
				t.Fatalf("held local evener/files/search failed: %v", err)
			}
		}
	})
}

func mustWriteFileForSearchTest(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
