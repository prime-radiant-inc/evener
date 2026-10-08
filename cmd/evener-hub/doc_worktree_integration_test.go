package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/rendezvous"
	daemonserver "primeradiant.com/evener/server"
)

type worktreeDocumentFixture struct {
	session         *agent.Session
	hubClient       *appwire.Client
	hubURL          string
	documentURL     string
	ref             string
	sessionID       string
	root            string
	step            func(t *testing.T, operation, name string) string
	localRead       func(t *testing.T, path string) (int, []byte)
	remoteRead      func(t *testing.T, path string) (int, []byte)
	localImageRead  func(t *testing.T, path string) (int, []byte)
	remoteImageRead func(t *testing.T, path string) (int, []byte)
}

var (
	worktreeImageA = encodeWorktreePNG(color.NRGBA{R: 0xd1, G: 0x49, B: 0x5b, A: 0xff})
	worktreeImageB = encodeWorktreePNG(color.NRGBA{R: 0x28, G: 0x77, B: 0xb8, A: 0xff})
)

func encodeWorktreePNG(pixel color.NRGBA) []byte {
	fixture := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	for y := range 2 {
		for x := range 2 {
			fixture.SetNRGBA(x, y, pixel)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, fixture); err != nil {
		panic(err)
	}
	return encoded.Bytes()
}

// worktreeDocumentAdapter scripts only the provider boundary. Each queued step
// selects one real manage_worktree call; the next provider round ends the turn.
type worktreeDocumentAdapter struct {
	steps chan map[string]any
	mu    sync.Mutex
	end   bool
}

func (*worktreeDocumentAdapter) Name() string { return "openai" }

func (*worktreeDocumentAdapter) Stream(context.Context, llm.Request) (llm.Stream, error) {
	return nil, llm.ErrStreamUnsupported
}

func (a *worktreeDocumentAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.ResponseFormat != nil {
		return llm.Response{Message: llm.Assistant(`{"name":"worktree document fixture"}`)}, nil
	}

	a.mu.Lock()
	if a.end {
		a.end = false
		a.mu.Unlock()
		return activityRelayTool("communicate", map[string]any{
			"message":  "worktree step complete",
			"end_turn": true,
			"output": map[string]any{
				"message":   "",
				"data":      map[string]any{},
				"artifacts": []string{},
			},
		}), nil
	}
	a.mu.Unlock()

	select {
	case args := <-a.steps:
		a.mu.Lock()
		a.end = true
		a.mu.Unlock()
		return activityRelayTool("manage_worktree", args), nil
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
}

func newWorktreeDocumentFixture(t *testing.T) *worktreeDocumentFixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	t.Cleanup(cancel)

	root := t.TempDir()
	runWorktreeDocumentGit(t, root, "init")
	runWorktreeDocumentGit(t, root, "config", "user.name", "Evener Test")
	runWorktreeDocumentGit(t, root, "config", "user.email", "evener-test@example.invalid")
	for name, contents := range map[string]string{
		"seed.txt":    "initial commit\n",
		"captured.md": "captured launch A",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "captured.png"), worktreeImageB, 0o600); err != nil {
		t.Fatal(err)
	}
	runWorktreeDocumentGit(t, root, "add", "seed.txt", "captured.md", "captured.png")
	runWorktreeDocumentGit(t, root, "commit", "-m", "initial fixture")

	project, err := identifier.ResolveProject(root)
	if err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	stateDir := filepath.Join(stateRoot, "projects", project.ID)
	adapter := &worktreeDocumentAdapter{steps: make(chan map[string]any, 1)}
	llmClient := llm.NewClient()
	llmClient.Register(adapter)
	sess, err := agent.NewSession(
		llmClient,
		provider.NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(root),
		agent.SessionConfig{StateDir: stateDir, Sandbox: "off"},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.Close)

	sessionID := sess.ID()
	ref := "local:" + sessionID
	daemon := daemonserver.NewServer(daemonserver.ServerConfig{})
	// Its thread history projects into the transcript's index directory
	// until closed, which must happen before stateDir is removed. The
	// session closes first, so the history sees everything it records.
	t.Cleanup(func() {
		sess.Close()
		daemon.Close()
	})
	prepared, err := daemonserver.PrepareAppIdentityForRef("local", sessionID, ref, sess.TranscriptPath())
	if err != nil {
		t.Fatal(err)
	}
	daemon.ReplaceAppIdentity(prepared, nil)
	daemon.SetStatus(daemonserver.StatusInfo{SessionID: sessionID, State: appwire.ThreadStatusIdle, WorkingDir: root})
	daemon.WireTranscriptHistory(sess)
	sess.ConsumeEventsLossless(func(event events.SessionEvent) {
		daemonserver.BridgeEvent(daemon, event, nil)
	}, func() {})
	daemonHTTP := httptest.NewServer(http.HandlerFunc(daemon.AppServer().ServeWebSocket))
	t.Cleanup(daemonHTTP.Close)

	entry := rendezvous.Entry{
		Protocol:     appwire.ProtocolVersion,
		Endpoint:     daemonHTTP.URL,
		SourceID:     "local",
		ThreadID:     sessionID,
		SessionID:    sessionID,
		WorkspaceRef: ref,
		WorkingDir:   root,
		StateDir:     stateDir,
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: sessionID}}
	}, daemonHTTP.Client())
	sources := appsource.NewRegistry()
	sources.Add(source)

	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if cached, ok := past.Find(sessionID); !ok || cached.Meta.EnvInfo.WorkingDir != root {
		t.Fatalf("warm past fixture = %+v, want launch root %q", cached, root)
	}
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(),
		Past:         past,
		Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{
			Entry: entry, SessionID: sessionID, Status: appwire.ThreadStatusIdle,
		}),
	}

	hubServer := newHubAppServer(cfg, sources)
	hubHTTP := httptest.NewServer(http.HandlerFunc(hubServer.ServeWebSocket))
	t.Cleanup(hubHTTP.Close)
	hubClient := dialHubRPC(t, hubHTTP)
	if _, err := hubClient.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hubClient.Close() })
	proxyHostClient := dialHubRPC(t, hubHTTP)
	if _, err := proxyHostClient.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxyHostClient.Close() })

	daemonClient := dialHubRPC(t, daemonHTTP)
	if _, err := daemonClient.Initialize(ctx, appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = daemonClient.Close() })

	initial, err := hubClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if initial.Thread.CWD != root {
		t.Fatalf("initial hub hydration cwd = %q, want launch root %q", initial.Thread.CWD, root)
	}
	if got := hubServer.SubscriberCount(ref); got != 1 {
		t.Fatalf("hub subscribers for %q = %d, want 1", ref, got)
	}
	lastHydrated := initial.Thread.CWD

	web := NewWebServer(cfg)
	web.sources = sources
	localHTTP := httptest.NewServer(web.Handler())
	t.Cleanup(localHTTP.Close)
	controllerHTTP := controllerOverHost(t, hubcore.WebConfig{}, proxyHostClient)
	t.Cleanup(controllerHTTP.Close)

	fixture := &worktreeDocumentFixture{
		session:     sess,
		hubClient:   hubClient,
		hubURL:      hubHTTP.URL,
		documentURL: localHTTP.URL + "/doc/file",
		ref:         ref,
		sessionID:   sessionID,
		root:        root,
	}
	fixture.localRead = func(t *testing.T, path string) (int, []byte) {
		t.Helper()
		return readWorktreeDocumentHTTP(t, fixture.documentURL, sessionID, path)
	}
	fixture.remoteRead = func(t *testing.T, path string) (int, []byte) {
		t.Helper()
		return readWorktreeDocumentHTTP(t, controllerHTTP.URL+"/doc/file", "h1:"+sessionID, path)
	}
	fixture.localImageRead = func(t *testing.T, path string) (int, []byte) {
		t.Helper()
		return readWorktreeImageHTTP(t, localHTTP.URL+"/doc/image", sessionID, path)
	}
	fixture.remoteImageRead = func(t *testing.T, path string) (int, []byte) {
		t.Helper()
		return readWorktreeImageHTTP(t, controllerHTTP.URL+"/doc/image", "h1:"+sessionID, path)
	}

	created := map[string]string{}
	fixture.step = func(t *testing.T, operation, name string) string {
		t.Helper()
		args := map[string]any{"operation": operation}
		if name != "" {
			args["name"] = name
		}
		adapter.steps <- args
		if _, err := sess.ProcessInput(ctx, operation+" "+name, nil); err != nil {
			t.Fatalf("ProcessInput(%s, %s): %v", operation, name, err)
		}

		installed := strings.TrimSpace(sess.Meta().EnvInfo.WorkingDir)
		meta, err := schema.LoadSessionMeta(stateDir, sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if installed == "" {
			t.Fatalf("%s %q installed an empty cwd", operation, name)
		}
		if persisted := strings.TrimSpace(meta.EnvInfo.WorkingDir); persisted != installed {
			t.Fatalf("%s %q cwd: session=%q persisted=%q", operation, name, installed, persisted)
		}
		switch operation {
		case "create":
			if installed == root || installed == lastHydrated {
				transcript, _ := os.ReadFile(sess.TranscriptPath())
				t.Fatalf("create %q left cwd at %q; transcript:\n%s", name, installed, transcript)
			}
			created[name] = installed
		case "switch":
			if installed != created[name] {
				t.Fatalf("switch %q installed %q, want %q", name, installed, created[name])
			}
		case "exit":
			if installed != root {
				t.Fatalf("exit installed %q, want launch root %q", installed, root)
			}
		}

		// Do not consume the A→B resync yet. The hydrated client still carries A,
		// while document authority must already be B and reject its captured A
		// absolute target rather than rebasing it onto B.
		if operation == "create" && name == "docs-b" {
			if lastHydrated != root {
				t.Fatalf("held hydration = %q, want captured launch root %q", lastHydrated, root)
			}
			capturedA := filepath.Join(root, "captured.md")
			for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
				status, body := read(t, capturedA)
				if status != http.StatusForbidden || bytes.Contains(body, []byte("captured launch A")) {
					t.Fatalf("captured A target during held hydration = %d %q, want 403 without A bytes", status, body)
				}
			}
			capturedImageA := filepath.Join(root, "captured.png")
			for name, check := range map[string]struct {
				read       func(*testing.T, string) (int, []byte)
				wantStatus int
			}{
				"local":  {read: fixture.localImageRead, wantStatus: http.StatusForbidden},
				"remote": {read: fixture.remoteImageRead, wantStatus: http.StatusBadRequest},
			} {
				status, body := check.read(t, capturedImageA)
				if status != check.wantStatus || bytes.Contains(body, worktreeImageA) || bytes.Contains(body, worktreeImageB) {
					t.Fatalf("%s captured A image during held hydration = %d %q, want %d without A or B bytes", name, status, body, check.wantStatus)
				}
			}
		}

		awaitWorktreeDocumentResync(ctx, t, hubClient.Notifications(), sessionID, ref)
		daemonRead, err := daemonClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref})
		if err != nil {
			t.Fatalf("daemon ThreadRead after %s %q: %v", operation, name, err)
		}
		hydrated, err := hubClient.ThreadRead(ctx, appwire.ThreadReadParams{Ref: ref, Subscribe: true})
		if err != nil {
			t.Fatalf("hub ThreadRead after %s %q: %v", operation, name, err)
		}
		lastHydrated = hydrated.Thread.CWD
		if daemonRead.Thread.CWD != installed || hydrated.Thread.CWD != installed {
			t.Fatalf("%s %q cwd: session=%q daemon=%q hub=%q", operation, name, installed, daemonRead.Thread.CWD, hydrated.Thread.CWD)
		}
		return installed
	}
	return fixture
}

func TestDocumentWorktree_ProducerToLocalAndRemoteReads(t *testing.T) {
	fixture := newWorktreeDocumentFixture(t)

	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := read(t, "captured.md")
		if status != http.StatusOK || string(body) != "captured launch A" {
			t.Fatalf("initial read = %d %q, want launch A", status, body)
		}
	}

	rootB := fixture.step(t, "create", "docs-b")
	if err := os.WriteFile(filepath.Join(fixture.root, "plan.md"), []byte("launch A"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "plan.md"), []byte("worktree B"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b-only.md"), []byte("only B"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := read(t, "plan.md")
		if status != http.StatusOK || string(body) != "worktree B" {
			t.Fatalf("read = %d %q, want worktree B", status, body)
		}
		status, body = read(t, "b-only.md")
		if status != http.StatusOK || string(body) != "only B" {
			t.Fatalf("B-only read = %d %q, want only B", status, body)
		}
		status, body = read(t, filepath.Join(fixture.root, "plan.md"))
		if status != http.StatusForbidden {
			t.Fatalf("old absolute target returned %d %q", status, body)
		}
	}

	rootC := fixture.step(t, "create", "docs-c")
	if err := os.WriteFile(filepath.Join(rootC, "plan.md"), []byte("worktree C"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootC, "c-only.md"), []byte("only C"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := read(t, "plan.md")
		if status != http.StatusOK || string(body) != "worktree C" {
			t.Fatalf("C read = %d %q, want worktree C", status, body)
		}
		status, body = read(t, "c-only.md")
		if status != http.StatusOK || string(body) != "only C" {
			t.Fatalf("C-only read = %d %q, want only C", status, body)
		}
	}

	if got := fixture.step(t, "switch", "docs-b"); got != rootB {
		t.Fatalf("switch returned %q, want B %q", got, rootB)
	}
	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := read(t, "plan.md")
		if status != http.StatusOK || string(body) != "worktree B" {
			t.Fatalf("switched B read = %d %q, want worktree B", status, body)
		}
		status, body = read(t, filepath.Join(rootC, "plan.md"))
		if status != http.StatusForbidden {
			t.Fatalf("old C absolute target returned %d %q", status, body)
		}
	}

	reconnected := dialWorktreeDocumentClient(t, fixture.hubURL)
	defer reconnected.Close() //nolint:errcheck // test cleanup
	read, err := reconnected.ThreadRead(t.Context(), appwire.ThreadReadParams{Ref: fixture.ref, Subscribe: true})
	if err != nil {
		t.Fatal(err)
	}
	if read.Thread.CWD != rootB {
		t.Fatalf("reconnected hydration cwd = %q, want B %q", read.Thread.CWD, rootB)
	}
	for _, readFile := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := readFile(t, filepath.Join(rootB, "plan.md"))
		if status != http.StatusOK || string(body) != "worktree B" {
			t.Fatalf("absolute B after reconnect = %d %q, want worktree B", status, body)
		}
	}

	if got := fixture.step(t, "exit", ""); got != fixture.root {
		t.Fatalf("exit returned %q, want A %q", got, fixture.root)
	}
	for _, readFile := range []func(*testing.T, string) (int, []byte){fixture.localRead, fixture.remoteRead} {
		status, body := readFile(t, "plan.md")
		if status != http.StatusOK || string(body) != "launch A" {
			t.Fatalf("exit read = %d %q, want launch A", status, body)
		}
		status, body = readFile(t, filepath.Join(rootB, "plan.md"))
		if status != http.StatusForbidden {
			t.Fatalf("B absolute target after exit returned %d %q", status, body)
		}
	}
}

func TestDocumentWorktree_ImageFixturesDecode(t *testing.T) {
	t.Parallel()

	fixtures := []struct {
		name      string
		data      []byte
		wantPixel color.NRGBA
	}{
		{name: "launch A", data: worktreeImageA, wantPixel: color.NRGBA{R: 0xd1, G: 0x49, B: 0x5b, A: 0xff}},
		{name: "worktree B", data: worktreeImageB, wantPixel: color.NRGBA{R: 0x28, G: 0x77, B: 0xb8, A: 0xff}},
	}

	decodedPixels := make([]color.NRGBA, 0, len(fixtures))
	for _, fixture := range fixtures {
		decoded, err := png.Decode(bytes.NewReader(fixture.data))
		if err != nil {
			t.Errorf("decode %s fixture: %v", fixture.name, err)
			continue
		}
		if bounds := decoded.Bounds(); bounds.Dx() != 2 || bounds.Dy() != 2 {
			t.Errorf("%s fixture dimensions = %v, want 2x2", fixture.name, bounds)
		}
		gotPixel := color.NRGBAModel.Convert(decoded.At(decoded.Bounds().Min.X, decoded.Bounds().Min.Y)).(color.NRGBA)
		if gotPixel != fixture.wantPixel {
			t.Errorf("%s fixture pixel = %#v, want %#v", fixture.name, gotPixel, fixture.wantPixel)
		}
		decodedPixels = append(decodedPixels, gotPixel)
	}
	if bytes.Equal(worktreeImageA, worktreeImageB) {
		t.Error("launch A and worktree B fixture bytes are equal")
	}
	if len(decodedPixels) == len(fixtures) && decodedPixels[0] == decodedPixels[1] {
		t.Errorf("launch A and worktree B fixture pixels are both %#v, want distinct pixels", decodedPixels[0])
	}
}

func TestDocumentWorktree_ImageProducerToLocalAndRemoteReads(t *testing.T) {
	fixture := newWorktreeDocumentFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.root, "captured.png"), worktreeImageA, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localImageRead, fixture.remoteImageRead} {
		status, body := read(t, "captured.png")
		if status != http.StatusOK || !bytes.Equal(body, worktreeImageA) {
			t.Fatalf("initial relative image read = %d %q, want launch A bytes", status, body)
		}
	}

	rootB := fixture.step(t, "create", "docs-b")
	for _, read := range []func(*testing.T, string) (int, []byte){fixture.localImageRead, fixture.remoteImageRead} {
		for name, path := range map[string]string{
			"relative":         "captured.png",
			"current absolute": filepath.Join(rootB, "captured.png"),
		} {
			status, body := read(t, path)
			if status != http.StatusOK || !bytes.Equal(body, worktreeImageB) {
				t.Fatalf("B %s image read = %d %q, want worktree B bytes", name, status, body)
			}
		}
	}
}

func TestDocumentWorktree_LiveSourceUnavailableDoesNotReadPast(t *testing.T) {
	rootA := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "plan.md"), []byte("stale launch A"), 0o600); err != nil {
		t.Fatal(err)
	}
	stateRoot := t.TempDir()
	stateDir := filepath.Join(stateRoot, "projects", "unavailable-0000000000")
	sessionID := "02wMz5Txv1C3Hut0M8GCeB"
	if err := schema.SaveSessionMeta(stateDir, schema.SessionMeta{
		ID: sessionID, UpdatedAt: time.Now(), EnvInfo: schema.EnvironmentInfo{WorkingDir: rootA},
	}); err != nil {
		t.Fatal(err)
	}
	past := hubcore.NewPastIndex(filepath.Join(stateRoot, "projects", "*"))
	if _, err := past.Rebuild(); err != nil {
		t.Fatal(err)
	}
	if _, ok := past.Find(sessionID); !ok {
		t.Fatal("stale past fixture was not indexed")
	}

	deadDaemon := httptest.NewServer(http.NotFoundHandler())
	endpoint := deadDaemon.URL
	httpClient := deadDaemon.Client()
	deadDaemon.Close()
	entry := rendezvous.Entry{
		Protocol: appwire.ProtocolVersion, Endpoint: endpoint, SourceID: "local",
		ThreadID: sessionID, SessionID: sessionID, WorkspaceRef: "local:" + sessionID,
		WorkingDir: rootA, StateDir: stateDir,
	}
	source := appsource.NewLocalDaemonSourceWithEntries("local", func() []appsource.LocalDaemonEntry {
		return []appsource.LocalDaemonEntry{{Entry: entry, SessionID: sessionID}}
	}, httpClient)
	sources := appsource.NewRegistry()
	sources.Add(source)
	cfg := hubcore.WebConfig{
		HubStateRoot: t.TempDir(), Past: past,
		Roster: hubcore.NewRosterWithEntries(hubcore.LiveEntry{Entry: entry, SessionID: sessionID, Status: appwire.ThreadStatusIdle}),
	}

	hostApp := newHubAppServer(cfg, sources)
	hostHTTP := httptest.NewServer(http.HandlerFunc(hostApp.ServeWebSocket))
	defer hostHTTP.Close()
	if response, err := requestSessionDocument(t, hostHTTP, appwire.SessionDocumentParams{SessionID: sessionID, Path: "plan.md"}); err == nil {
		t.Fatalf("direct host read returned %q, want transient unavailable", response.Data)
	} else if got := sessionImageErrorInfo(t, err); got != string(appwire.ErrorSessionUnavailable) {
		t.Fatalf("direct host error = %q (%v), want %q", got, err, appwire.ErrorSessionUnavailable)
	}

	hostClient := dialHubRPC(t, hostHTTP)
	defer hostClient.Close() //nolint:errcheck // test cleanup
	if _, err := hostClient.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		t.Fatal(err)
	}
	web := NewWebServer(cfg)
	web.sources = sources
	localHTTP := httptest.NewServer(web.Handler())
	defer localHTTP.Close()
	controllerHTTP := controllerOverHost(t, hubcore.WebConfig{}, hostClient)
	defer controllerHTTP.Close()

	for name, endpoint := range map[string]string{
		"local":  localHTTP.URL + "/doc/file",
		"remote": controllerHTTP.URL + "/doc/file",
	} {
		session := sessionID
		if name == "remote" {
			session = "h1:" + sessionID
		}
		status, body := readWorktreeDocumentHTTP(t, endpoint, session, "plan.md")
		if status != http.StatusServiceUnavailable || bytes.Contains(body, []byte("stale launch A")) {
			t.Fatalf("%s unavailable read = %d %q, want 503 without stale A bytes", name, status, body)
		}
	}
}

func runWorktreeDocumentGit(t *testing.T, root string, args ...string) {
	t.Helper()
	argv := append([]string{"-C", root}, args...)
	cmd := exec.CommandContext(t.Context(), "git", argv...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(argv, " "), err, output)
	}
}

func readWorktreeDocumentHTTP(t *testing.T, endpoint, session, path string) (int, []byte) {
	t.Helper()
	requestURL := endpoint + "?format=raw&session=" + url.QueryEscape(session) + "&path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", requestURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func readWorktreeImageHTTP(t *testing.T, endpoint, session, path string) (int, []byte) {
	t.Helper()
	requestURL := endpoint + "?session=" + url.QueryEscape(session) + "&path=" + url.QueryEscape(path)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, requestURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", requestURL, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func awaitWorktreeDocumentResync(ctx context.Context, t *testing.T, notifications <-chan appwire.Notification, sessionID, ref string) {
	t.Helper()
	var observed []string
	for {
		select {
		case notification, ok := <-notifications:
			if !ok {
				t.Fatal("hub notification stream closed before cwd resync")
			}
			if notification.Method != appwire.NotifyEvenerThreadResync {
				observed = append(observed, notification.Method)
				continue
			}
			var params appwire.ThreadResyncParams
			if err := json.Unmarshal(notification.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ThreadID != sessionID || params.Ref != ref {
				t.Fatalf("resync = %+v, want thread %q ref %q", params, sessionID, ref)
			}
			return
		case <-ctx.Done():
			t.Fatalf("waiting for cwd resync after notifications %q: %v", observed, ctx.Err())
		}
	}
}

func dialWorktreeDocumentClient(t *testing.T, hubURL string) *appwire.Client {
	t.Helper()
	transport, err := appwire.DialWebSocket(t.Context(), "ws"+strings.TrimPrefix(hubURL, "http")+"/rpc", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := appwire.NewClient(transport)
	client.Start(t.Context())
	if _, err := client.Initialize(t.Context(), appwire.InitializeParams{ProtocolVersion: appwire.ProtocolVersion}); err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	return client
}
