package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/sandbox"
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
