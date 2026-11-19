package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// mcpHeaderRecorder is an MCP HTTP endpoint that records every Authorization
// header it is asked to serve and refuses the MCP handshake. Discovery does not
// need the server to work: the disclosure this test is about happens when the
// header is SENT, which is exactly what a refusal cannot undo.
type mcpHeaderRecorder struct {
	mu   sync.Mutex
	auth []string
}

func (r *mcpHeaderRecorder) record(auth string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.auth = append(r.auth, auth)
}

func (r *mcpHeaderRecorder) authorizations() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auth...)
}

func (r *mcpHeaderRecorder) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.record(req.Header.Get("Authorization"))
		http.Error(w, "not an MCP endpoint", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestSandboxedDelegateDoesNotExpandNewlyDiscoveredProjectMCPConfig is the
// end-to-end shape of the sandboxed-delegate MCP disclosure.
//
// A session starts outside Git under a workspace-write sandbox and spawns a
// shared-workspace delegate. Before that spawn a repository appears around the
// session with .evener/mcp.json naming a repository-chosen endpoint and
// carrying the host's credential by reference. The child's fresh root lookup
// discovers that layer — the parent never read it — and the child's sandbox
// withholds the credential from the child's own shell. Expanding the reference
// out of the host process environment and dialling the endpoint would hand the
// parent's credential to the repository just by spawning a delegate.
//
// Two halves must hold at once, and the test asserts both: the repo-controlled
// project layer must NOT reach its endpoint with the secret, and a global layer
// the USER authored must still expand the same reference, because the
// restriction is about who wrote the layer and not about the variable.
func TestSandboxedDelegateDoesNotExpandNewlyDiscoveredProjectMCPConfig(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	const secretName = "MCP_SANDBOXED_DELEGATE_SECRET"
	const secretValue = "host-credential-value"
	t.Setenv(secretName, secretValue)

	// The global layer is the user-authored one: it names its own endpoint and
	// references the same secret through an env var.
	globalRecorder := &mcpHeaderRecorder{}
	globalSrv := globalRecorder.server(t)
	globalPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "evener", "mcp.json")
	writeInfraFile(t, globalPath, `{"mcpServers":{"trusted_global":{"type":"http","url":"`+globalSrv.URL+`","headers":{"Authorization":"Bearer ${`+secretName+`}"}}}}`, 0o644)

	// The workspace is NOT a repository yet, which is what a session launched
	// outside Git looks like.
	workspace := realTempDirForTest(t)
	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(workspace)), withDir(workspace), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-mcp-delegate-*"),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	}))
	// The parent runs under an enforced workspace-write policy. The child inherits
	// it and shares the parent's environment, which is the shared-workspace shape
	// the finding names. Assigning the resolved policy directly provisions no
	// kernel wrapper, so no command in this test is sandboxed — the policy is the
	// fact under test, not the enforcement.
	sbxSetParentMode(t, s, infraSandboxHost(t), workspace, sandbox.ModeWorkspaceWrite)

	// The parent's startup read recorded no project layer, because there was none.
	startup := s.startupMCPProjectLayer()
	if startup == nil || startup.Path != "" {
		t.Fatalf("fixture is wrong: a launch outside Git recorded project evidence %+v, so the layer this test adds would not be a NEW discovery", startup)
	}

	// The repository, and its MCP config, appear around the running session.
	runInfraGit(t, workspace, "init", "-q")
	projectRecorder := &mcpHeaderRecorder{}
	projectSrv := projectRecorder.server(t)
	writeInfraFile(t, filepath.Join(workspace, ".evener", "mcp.json"),
		`{"mcpServers":{"repo_project":{"type":"http","url":"`+projectSrv.URL+`","headers":{"Authorization":"Bearer ${`+secretName+`}"}}}}`, 0o644)
	if got := mcpconfig.ProjectConfigPath(s.currentEnv()); got == "" {
		t.Fatal("the project MCP layer does not resolve after the repository appeared, so the child would never discover it and this test proves nothing")
	}

	// Spawn the shared-workspace delegate. Its own MCP initialization runs inside
	// createDelegate, so anything it discloses has been disclosed by the time this
	// returns.
	created := s.createDelegate(context.Background(), delegateArgs{
		Task:                "child task",
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}

	children := s.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked children = %d, want 1", len(children))
	}
	child := children[0]
	child.mu.Lock()
	childSandbox := child.cfg.Sandbox
	child.mu.Unlock()
	if childSandbox != sandbox.ModeWorkspaceWrite.String() {
		t.Fatalf("fixture is wrong: child sandbox = %q, want %q", childSandbox, sandbox.ModeWorkspaceWrite)
	}

	// The repo-controlled endpoint must not have been dialled with the secret.
	for _, auth := range projectRecorder.authorizations() {
		if strings.Contains(auth, secretValue) {
			t.Fatalf("the sandboxed delegate sent the host credential to the repository-selected MCP endpoint: Authorization %q", auth)
		}
	}
	if got := projectRecorder.authorizations(); len(got) != 0 {
		t.Fatalf("the sandboxed delegate dialled the repo-controlled MCP endpoint %d time(s) from a newly discovered project layer; headers = %q", len(got), got)
	}

	// The user-authored global layer still expands the same secret, so the
	// restriction is the layer's trust class and not the variable.
	globalAuthed := false
	for _, auth := range globalRecorder.authorizations() {
		if strings.Contains(auth, secretValue) {
			globalAuthed = true
			break
		}
	}
	if !globalAuthed {
		t.Fatalf("the trusted global layer's ${%s} did not expand (headers = %q), so this fixture could not show a selective restriction", secretName, globalRecorder.authorizations())
	}

	// And the child's own MCP manager holds the trusted server, not the project
	// one: the layer was skipped, not merely failed to connect.
	child.mu.Lock()
	mgr := child.mcpMgr
	child.mu.Unlock()
	if mgr == nil {
		t.Fatal("child MCP manager is nil; the trusted global layer should still have been discovered")
	}
	names := map[string]bool{}
	for _, info := range mgr.Servers() {
		names[info.Name] = true
	}
	if !names["trusted_global"] {
		t.Errorf("child MCP servers = %v, want the trusted global server discovered", names)
	}
	if names["repo_project"] {
		t.Errorf("child MCP servers = %v; a project layer that appeared after the parent's startup was discovered by a sandboxed delegate", names)
	}
}

// TestSandboxedDelegateKeepsProjectLayerReadAtStartup is the other half of the
// authorization: a project layer the PARENT read at its own startup is the
// layer the launch already trusted, so a sandboxed delegate still expands it.
// Without this case, the restriction could pass by disabling project MCP for
// every sandboxed delegate.
func TestSandboxedDelegateKeepsProjectLayerReadAtStartup(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	const secretName = "MCP_SANDBOXED_STARTUP_SECRET"
	const secretValue = "startup-credential-value"
	t.Setenv(secretName, secretValue)

	// The repository and its layer exist BEFORE the session starts, so the
	// session's own startup read is the authorization.
	workspace := realTempDirForTest(t)
	runInfraGit(t, workspace, "init", "-q")
	projectRecorder := &mcpHeaderRecorder{}
	projectSrv := projectRecorder.server(t)
	writeInfraFile(t, filepath.Join(workspace, ".evener", "mcp.json"),
		`{"mcpServers":{"startup_project":{"type":"http","url":"`+projectSrv.URL+`","headers":{"Authorization":"Bearer ${`+secretName+`}"}}}}`, 0o644)

	s := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(workspace)), withDir(workspace), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-mcp-startup-*"),
		MaxSubagentDepth: 1,
		testOnly: testConfig{
			skipGitSnapshot:     true,
			minimalSystemPrompt: true,
			noSyncJobStore:      true,
			sandboxProber:       bwrapCapableProber(workspace),
		},
	}))
	sbxSetParentMode(t, s, infraSandboxHost(t), workspace, sandbox.ModeWorkspaceWrite)

	startup := s.startupMCPProjectLayer()
	if startup == nil || startup.Path != filepath.Join(workspace, ".evener", "mcp.json") || startup.Digest == "" {
		t.Fatalf("fixture is wrong: startup project evidence = %+v, want the layer this session read", startup)
	}
	// The PARENT dials this endpoint at its own startup, because that is the
	// launch the layer was read under. Only the requests after this point belong
	// to the child, and only they can show what the child was authorized to do.
	parentDials := len(projectRecorder.authorizations())

	created := s.createDelegate(context.Background(), delegateArgs{
		Task:                "child task",
		DelegationAllowance: new(0),
	})
	if created.Err != nil {
		t.Fatalf("createDelegate: %v", created.Err)
	}

	childDials := projectRecorder.authorizations()[min(parentDials, len(projectRecorder.authorizations())):]
	if len(childDials) == 0 {
		t.Fatalf("the sandboxed delegate never dialled the project layer its launch already trusted, so this test could not show the authorization carrying over (parent dialled %d time(s))", parentDials)
	}
	authed := false
	for _, auth := range childDials {
		if strings.Contains(auth, secretValue) {
			authed = true
			break
		}
	}
	if !authed {
		t.Fatalf("the sandboxed delegate did not expand the project layer its launch already trusted, so a launch-trusted layer is being treated as untrusted; child headers = %q", childDials)
	}
}

// worktreeProjectMCPServer is a REAL MCP endpoint that also records every
// Authorization header it serves. A recorder that only refuses would prove the
// header was sent, not that the child connected; this one serves a working
// "echo" tool, so a test can show the delegate's MCP tool answered.
func worktreeProjectMCPServer(t *testing.T, recorder *mcpHeaderRecorder) *httptest.Server {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "worktree-project", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "echo",
		Description: "Echoes the input message back",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"message": map[string]any{"type": "string"},
			},
			"required": []string{"message"},
		},
	}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return nil, err
		}
		return &mcpsdk.CallToolResult{
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "echo: " + args.Message}},
		}, nil
	})
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server },
		&mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.record(r.Header.Get("Authorization"))
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// worktreeProjectMCPFixture is one sandboxed session rooted at a repository whose
// COMMITTED .evener/mcp.json authenticates with an environment variable, plus one
// worktree-isolated delegate created from it.
//
// The repository's config is committed before the session starts, so the parent's
// own startup read is the authorization, and the delegate's lane holds the same
// bytes at a different path — the shape the worktree branch exists for.
type worktreeProjectMCPFixture struct {
	t           *testing.T
	root        *Session
	recorder    *mcpHeaderRecorder
	secretName  string
	secretValue string
	workspace   string
	parentDials int
	created     delegateResult
}

func newWorktreeProjectMCPFixture(t *testing.T) *worktreeProjectMCPFixture {
	t.Helper()
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	f := &worktreeProjectMCPFixture{
		t:           t,
		recorder:    &mcpHeaderRecorder{},
		secretName:  "MCP_WORKTREE_PROJECT_SECRET",
		secretValue: "worktree-credential-value",
	}
	t.Setenv(f.secretName, f.secretValue)
	srv := worktreeProjectMCPServer(t, f.recorder)

	f.workspace = realTempDirForTest(t)
	runInfraGit(t, f.workspace, "init", "-q")
	runInfraGit(t, f.workspace, "config", "user.email", "test@example.com")
	runInfraGit(t, f.workspace, "config", "user.name", "Test")
	writeInfraFile(t, filepath.Join(f.workspace, ".evener", "mcp.json"),
		`{"mcpServers":{"worktree_project":{"type":"http","url":"`+srv.URL+
			`","headers":{"Authorization":"Bearer ${`+f.secretName+`}"}}}}`, 0o644)
	runInfraGit(t, f.workspace, "add", ".evener/mcp.json")
	runInfraGit(t, f.workspace, "commit", "-q", "-m", "committed project mcp config")

	f.root = newSession(t,
		withEnv(execenv.NewLocalExecutionEnvironment(f.workspace)),
		withDir(f.workspace),
		withConfig(SessionConfig{
			StateDir:         packageFixtureTempDir(t, "sbx-mcp-worktree-*"),
			MaxSubagentDepth: 1,
			testOnly: testConfig{
				skipGitSnapshot:     true,
				minimalSystemPrompt: true,
				noSyncJobStore:      true,
				sandboxProber:       bwrapCapableProber(f.workspace),
			},
		}))
	sbxSetParentMode(t, f.root, infraSandboxHost(t), f.workspace, sandbox.ModeWorkspaceWrite)
	f.root.mu.Lock()
	f.root.worktreeGitVersionOK = true
	f.root.mu.Unlock()

	startup := f.root.startupMCPProjectLayer()
	if startup == nil || startup.Path == "" || startup.RepoRoot == "" {
		t.Fatalf("fixture is wrong: the session's startup project evidence = %+v, want the committed layer with its repository identity", startup)
	}
	// The parent dials this endpoint at its own startup: that read is the launch
	// authorization. Everything after this count belongs to a child.
	f.parentDials = len(f.recorder.authorizations())

	f.created = f.root.createDelegate(context.Background(), delegateArgs{
		Task:                "child task in a worktree",
		Isolation:           "worktree",
		DelegationAllowance: new(0),
	})
	if f.created.Err != nil {
		t.Fatalf("createDelegate: %v", f.created.Err)
	}
	return f
}

// childDials returns the Authorization headers presented after the parent's own
// startup read.
func (f *worktreeProjectMCPFixture) childDials() []string {
	f.t.Helper()
	all := f.recorder.authorizations()
	return all[min(f.parentDials, len(all)):]
}

// requireChildAuthed fails unless a child has presented the secret.
func (f *worktreeProjectMCPFixture) requireChildAuthed(label string) {
	f.t.Helper()
	dials := f.childDials()
	if len(dials) == 0 {
		f.t.Fatalf("%s: the delegate never dialled the committed project layer, so nothing was authorized (parent dialled %d time(s))", label, f.parentDials)
	}
	for _, auth := range dials {
		if strings.Contains(auth, f.secretValue) {
			return
		}
	}
	f.t.Fatalf("%s: the delegate did not expand the committed project layer's ${%s}; headers = %q", label, f.secretName, dials)
}

// requireEchoTool fails unless sess can call the project server's echo tool.
func (f *worktreeProjectMCPFixture) requireEchoTool(sess *Session, label string) {
	f.t.Helper()
	const toolName = "worktree_project__echo"
	if sess.reg.Get(toolName) == nil {
		servers := map[string]string{}
		sess.mu.Lock()
		mgr := sess.mcpMgr
		sess.mu.Unlock()
		if mgr != nil {
			for _, info := range mgr.Servers() {
				servers[info.Name] = info.Status
			}
		}
		f.t.Fatalf("%s: %q is not registered, so the project layer was skipped or failed to connect; MCP servers = %v", label, toolName, servers)
	}
	res := sess.reg.ExecuteCall(context.Background(), sess.currentEnv(), llm.ToolCallData{
		ID:        "call_project_echo",
		Name:      toolName,
		Arguments: json.RawMessage(`{"message":"from-the-worktree-delegate"}`),
	})
	if res.IsError {
		f.t.Fatalf("%s: the project MCP tool failed: %s", label, res.Output)
	}
	if res.Output != "echo: from-the-worktree-delegate" {
		f.t.Fatalf("%s: project MCP tool output = %q", label, res.Output)
	}
}

// TestSandboxedWorktreeDelegateKeepsCommittedProjectMCPConfig is the Medium
// finding's end-to-end shape at WORKTREE CREATION.
//
// The user starts a sandboxed delegate with isolation "worktree" from a checkout
// whose committed .evener/mcp.json authenticates with ${SECRET}. The lane holds
// the exact bytes the parent's startup read parsed, at a different absolute path.
// Treating that path difference as a change skipped the whole project layer: every
// server in the repository's committed config disappeared from the delegate,
// including the servers that need no expansion at all.
func TestSandboxedWorktreeDelegateKeepsCommittedProjectMCPConfig(t *testing.T) {
	f := newWorktreeProjectMCPFixture(t)

	children := f.root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked children = %d, want 1", len(children))
	}
	child := children[0]
	lane := child.currentEnv().WorkingDirectory()
	if filepath.Clean(lane) == filepath.Clean(f.workspace) {
		t.Fatalf("fixture is wrong: the delegate runs in the parent's checkout %q, not a worktree", lane)
	}
	// The lane really holds the committed bytes, which is the premise: the same
	// file, checked out at a second path.
	committed, err := os.ReadFile(filepath.Join(f.workspace, ".evener", "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	inLane, err := os.ReadFile(filepath.Join(lane, ".evener", "mcp.json"))
	if err != nil {
		t.Fatalf("the worktree lane does not hold the committed project config: %v", err)
	}
	if !bytes.Equal(inLane, committed) {
		t.Fatalf("the worktree lane holds different bytes than the launch read:\nlane: %s\nmain: %s", inLane, committed)
	}

	f.requireChildAuthed("worktree creation")
	f.requireEchoTool(child, "worktree creation")
}

// TestColdRestoredWorktreeDelegateKeepsCommittedProjectMCPConfig is the second
// half: a cold restore rebuilds the child from its committed descriptor, and the
// authorization must survive that rebuild. A restore path that dropped the
// parent's startup evidence, or re-derived the layer against the lane alone,
// would silently strip the repository's committed MCP servers from every resumed
// delegate.
func TestColdRestoredWorktreeDelegateKeepsCommittedProjectMCPConfig(t *testing.T) {
	f := newWorktreeProjectMCPFixture(t)

	childID := f.created.ChildSessionID
	child := f.root.getSub(childID)
	if child == nil || child.sess == nil {
		t.Fatal("fixture is wrong: the created delegate has no live runtime to tear down")
	}
	lane := child.sess.currentEnv().WorkingDirectory()
	// Creation-time authorization is the other test's claim. What matters here is
	// the RESTORE's own initialization, so the count is taken and the requirement
	// applied only after the child has been rebuilt.
	beforeRestore := len(f.childDials())

	// Settle the creation turn, then reclaim the runtime, which is the production
	// path that turns the next send into a cold restore.
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
		// TRIPWIRE: the fixture's provider answers in-process in milliseconds; this
		// bound catches a wedged child, not a slow one.
	case <-time.After(30 * time.Second):
		t.Fatal("the created delegate never finished its first turn")
	}
	f.root.delegateController.mu.Lock()
	f.root.delegateController.maxRetainedTerminal = 1
	f.root.delegateController.mu.Unlock()
	if err := f.root.reclaimDelegateRuntimeCapacity(1); err != nil {
		t.Fatalf("reclaim the delegate runtime: %v", err)
	}
	if f.root.getSub(childID) != nil {
		t.Fatal("the delegate runtime was not reclaimed, so the next send would reuse it and never cold restore")
	}

	outcome := (delegateRuntime{owner: f.root}).send(context.Background(), f.created.DelegateID, "resume in the worktree", 0)
	if outcome.result.Err != nil {
		t.Fatalf("send to the cold delegate: %v", outcome.result.Err)
	}
	restored := f.root.getSub(childID)
	if restored == nil || restored.sess == nil {
		t.Fatal("the send did not restore the cold delegate")
	}
	if got := restored.sess.currentEnv().WorkingDirectory(); filepath.Clean(got) != filepath.Clean(lane) {
		t.Fatalf("the restored delegate runs in %q, want its worktree lane %q", got, lane)
	}
	// The restore's own MCP initialization is part of constructing the child, so
	// its dial has already happened by the time the runtime is installed.
	if got := len(f.childDials()); got <= beforeRestore {
		t.Fatalf("the restored delegate never dialled the committed project layer (%d dial(s) before the restore, %d after)", beforeRestore, got)
	}
	f.requireChildAuthed("cold restore")
	f.requireEchoTool(restored.sess, "cold restore")
}

// TestNestedSandboxedDelegateDoesNotEscalateFallbackProjectLayer is High #2's
// end-to-end proof: launch authorization must not be promoted from DISCOVERY.
//
// The shape a depth-2 escalation needs:
//
//  1. A session starts outside Git under a sandbox, so its startup read recorded
//     NO project layer, and spawns a depth-1 shared-workspace delegate.
//  2. A repository appears around the tree with a .evener/mcp.json whose server
//     authenticates as ${SECRET:-fallback}. The depth-1 delegate's fresh root
//     lookup discovers it — registering tools is the feature — but it may only
//     expand the FALLBACK, because the host environment is withheld from it.
//  3. The depth-1 delegate spawns a depth-2 delegate.
//
// If step 2's discovery were recorded as the depth-1 session's launch
// authorization, step 3 would hand the depth-2 delegate a record that names that
// exact file and entry, and the depth-2 process would match the digest and
// expand ${SECRET} out of the HOST environment. The fallback would have bought a
// real credential one level down. Step 3 must therefore see the same restricted
// expansion the depth-1 delegate did.
func TestNestedSandboxedDelegateDoesNotEscalateFallbackProjectLayer(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)

	const secretName = "MCP_NESTED_ESCALATION_SECRET"
	const secretValue = "host-credential-that-must-not-escape"
	const fallbackValue = "repo-chosen-fallback"
	t.Setenv(secretName, secretValue)

	// The workspace starts as no repository at all.
	workspace := realTempDirForTest(t)
	root := newSession(t, withEnv(execenv.NewLocalExecutionEnvironment(workspace)), withDir(workspace), withConfig(SessionConfig{
		StateDir:         packageFixtureTempDir(t, "sbx-mcp-nested-*"),
		MaxSubagentDepth: 2,
		testOnly: testConfig{
			skipGitSnapshot:            true,
			minimalSystemPrompt:        true,
			noSyncJobStore:             true,
			sandboxProber:              bwrapCapableProber(workspace),
			disableDelegateIdleRelease: true,
		},
	}), withSteps(
		// The root never runs a turn: its only job is to start depth 1. Every
		// step below therefore belongs to the first delegate that asks.
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(llm.ToolCallData{
				ID:        "spawn-nested",
				Name:      "delegate",
				Arguments: json.RawMessage(`{"prompt":"nested leaf task"}`),
				Type:      "function",
			})
		},
		func(llm.Request) llm.Response { return agenttest.FinalResponse("depth 1 done") },
		func(llm.Request) llm.Response { return agenttest.FinalResponse("nested leaf done") },
	))
	sbxSetParentMode(t, root, infraSandboxHost(t), workspace, sandbox.ModeReadOnly)

	// The root's own launch read found no project layer: there was no repository.
	rootStartup := root.startupMCPProjectLayer()
	if rootStartup == nil || rootStartup.Path != "" {
		t.Fatalf("fixture is wrong: a launch outside Git recorded project evidence %+v", rootStartup)
	}

	// The repository and its fallback-authenticated config appear around the
	// running tree.
	runInfraGit(t, workspace, "init", "-q")
	recorder := &mcpHeaderRecorder{}
	srv := recorder.server(t)
	writeInfraFile(t, filepath.Join(workspace, ".evener", "mcp.json"),
		`{"mcpServers":{"repo_project":{"type":"http","url":"`+srv.URL+
			`","headers":{"Authorization":"Bearer ${`+secretName+`:-`+fallbackValue+`}"}}}}`, 0o644)
	if got := mcpconfig.ProjectConfigPath(root.currentEnv()); got == "" {
		t.Fatal("the project layer does not resolve after the repository appeared, so no level below could ever discover it")
	}

	// Depth 1: the delegate discovers the layer and expands only the fallback.
	depth1 := root.createDelegate(context.Background(), delegateArgs{
		Task:                "spawn a nested delegate",
		DelegationAllowance: new(1),
	})
	if depth1.Err != nil {
		t.Fatalf("createDelegate (depth 1): %v", depth1.Err)
	}
	children := root.subagents.sessions()
	if len(children) != 1 {
		t.Fatalf("tracked depth-1 children = %d, want 1", len(children))
	}
	depth1Sess := children[0]

	// The premise that makes this test about High #2 and not about the first
	// round: depth 1 DID discover the layer and DID register its server. Only
	// the credential stayed withheld.
	depth1Sess.mu.Lock()
	manager := depth1Sess.mcpMgr
	depth1Sess.mu.Unlock()
	if manager == nil {
		t.Fatal("fixture is wrong: the depth-1 delegate registered no MCP manager, so the layer was skipped rather than partially expanded")
	}
	discovered := false
	for _, info := range manager.Servers() {
		if info.Name == "repo_project" {
			discovered = true
		}
	}
	if !discovered {
		t.Fatalf("fixture is wrong: the depth-1 delegate did not discover the new project layer (servers = %v), so a nested escalation has nothing to promote", manager.Servers())
	}
	for _, auth := range recorder.authorizations() {
		if strings.Contains(auth, secretValue) {
			t.Fatalf("the depth-1 delegate already expanded the HOST credential; Authorization %q", auth)
		}
	}

	// Depth 1 spawns depth 2 inside its own first turn (the scripted delegate
	// tool call), because a delegate may only create a child while it holds its
	// lease. Wait for that turn to settle before inspecting the tree.
	settled := root.getSub(depth1.ChildSessionID)
	if settled == nil {
		t.Fatal("the depth-1 delegate runtime is not tracked")
	}
	settled.mu.Lock()
	depth1Done := settled.done
	settled.mu.Unlock()
	select {
	case <-depth1Done:
		// TRIPWIRE: the fixture's provider answers in-process in milliseconds.
	case <-time.After(30 * time.Second):
		t.Fatal("the depth-1 delegate never finished its first turn")
	}

	// Depth 2: spawned by depth 1 from inside that turn. It must reach the same
	// layer through the same restricted expansion.
	nested := depth1Sess.subagents.sessions()
	if len(nested) != 1 {
		t.Fatalf("tracked depth-2 children = %d, want 1; the depth-1 delegate's delegate tool call did not spawn a nested delegate, so no escalation could be attempted", len(nested))
	}
	depth2Sess := nested[0]

	// Wait, bounded, until both delegates have dialled the endpoint. Both levels
	// connect during construction, so this is normally already true.
	waitForDials(t, recorder, 2)

	// The outcome assertion, first because it is the one that matters: no level
	// may present the HOST credential for a layer the tree only reached through
	// a ${SECRET:-fallback} expansion.
	fallbackDials := 0
	for _, auth := range recorder.authorizations() {
		if strings.Contains(auth, secretValue) {
			t.Fatalf("a nested level expanded the HOST credential from a layer its parent only reached through a ${%s:-%s} fallback; Authorization %q", secretName, fallbackValue, auth)
		}
		if strings.Contains(auth, fallbackValue) {
			fallbackDials++
		}
	}
	if fallbackDials < 2 {
		t.Fatalf("only %d level(s) dialled the layer with the %q fallback, so this test did not exercise the fallback expansion at both depths it claims to test", fallbackDials, fallbackValue)
	}

	// The cause, second: what each level recorded as launch authorization for
	// its own children must NOT be its own discovery. Anything else promotes a
	// fallback-expanded layer into a launch-authorized one, and a nested
	// delegate would then expand the real credential out of it.
	depth1Recorded := depth1Sess.startupMCPProjectLayer()
	if depth1Recorded == nil {
		t.Fatal("depth 1 recorded no evidence value at all")
	}
	if depth1Recorded.Path != "" || len(depth1Recorded.ServerDigests) != 0 {
		t.Fatalf("the sandboxed delegate recorded its OWN discovery as launch authorization for its children: %+v; a nested delegate would then expand the host credential out of a config this level could only reach by fallback", depth1Recorded)
	}

	// The premise again: depth 2 DID discover the layer from its own fresh
	// lookup. Only the credential stayed withheld.
	depth2Sess.mu.Lock()
	nestedManager := depth2Sess.mcpMgr
	depth2Sess.mu.Unlock()
	if nestedManager == nil {
		t.Fatal("fixture is wrong: the depth-2 delegate registered no MCP manager, so the layer was skipped rather than restricted")
	}
	nestedDiscovered := false
	for _, info := range nestedManager.Servers() {
		if info.Name == "repo_project" {
			nestedDiscovered = true
		}
	}
	if !nestedDiscovered {
		t.Fatalf("fixture is wrong: the depth-2 delegate did not discover the project layer (servers = %v), so nothing was withheld from it", nestedManager.Servers())
	}

	nestedRecorded := depth2Sess.startupMCPProjectLayer()
	if nestedRecorded == nil {
		t.Fatal("depth 2 recorded no evidence value at all")
	}
	if nestedRecorded.Path != "" || len(nestedRecorded.ServerDigests) != 0 {
		t.Fatalf("the depth-2 delegate treated a layer it only reached through a ${%s:-%s} fallback as launch authorization for its own children: %+v", secretName, fallbackValue, nestedRecorded)
	}
}

// waitForDials blocks until the recorder has seen at least n Authorization
// headers, or fails the test when a bound elapses. MCP servers connect in the
// background, so a level's first dial can trail the create call that started it.
func waitForDials(t *testing.T, r *mcpHeaderRecorder, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if len(r.authorizations()) >= n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d Authorization header(s) were presented within the bound, want at least %d: %q", len(r.authorizations()), n, r.authorizations())
}
