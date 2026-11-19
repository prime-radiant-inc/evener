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
