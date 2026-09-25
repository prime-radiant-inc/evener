//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// These tests drive Session.initMCP against a REAL stdio MCP server subprocess
// (agent/testdata/intgmcpserver), compiled in-test. They exercise the initMCP
// arms that only run when a server is actually configured: connect via
// mcp.NewManager, register the discovered tools into the session registry, and
// merge plugin-provided MCP configs. The server exits on stdin EOF, so the
// session's Close tears it down deterministically without a terminate timeout.

var (
	intgMCPServerOnce sync.Once
	intgMCPServerPath string
	errIntgMCPServer  error
)

// intg_buildMCPServer compiles the testdata stdio MCP server once per package
// run and returns the binary path. The path has no spaces so it survives the
// whitespace-split of an inline MCP spec.
func intg_buildMCPServer(t *testing.T) string {
	t.Helper()
	intgMCPServerOnce.Do(func() {
		// The same placement rule as the worktree base repo: a package
		// fixture must outlive the test that first builds it, so it never
		// lands inside an isolated test's own t.TempDir.
		intgMCPServerDir = packageFixtureTempDir(t, "evener-intgmcpserver-*")
		intgMCPServerPath = filepath.Join(intgMCPServerDir, "intgmcpserver")
		cmd := exec.Command("go", "build", "-o", intgMCPServerPath, "./testdata/intgmcpserver")
		out, err := cmd.CombinedOutput()
		if err != nil {
			errIntgMCPServer = fmt.Errorf("building test MCP server: %w\n%s", err, out)
		}
	})
	if errIntgMCPServer != nil {
		t.Fatal(errIntgMCPServer)
	}
	return intgMCPServerPath
}

// intg_mcpEcho drives the named registered tool with a message and returns its
// output, failing the test on any error.
func intg_mcpEcho(t *testing.T, sess *Session, toolName, message string) string {
	t.Helper()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        "call_mcp",
		Name:      toolName,
		Arguments: json.RawMessage(`{"message":"` + message + `"}`),
	})
	if res.IsError {
		t.Fatalf("MCP tool %q errored: %s", toolName, res.Output)
	}
	return res.Output
}

func TestIntg_InitMCP_InlineServer(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"intgsvc:" + bin}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	if sess.mcpMgr == nil {
		t.Fatal("mcpMgr is nil after initMCP with a configured server")
	}
	// The discovered tool must appear both in the MCP tool-definition list and
	// the executable registry, namespaced by server name.
	const want = "intgsvc__echo"
	if !intg_hasToolDef(sess.mcpTools, want) {
		t.Errorf("mcpTools missing %q; got %v", want, intg_toolDefNames(sess.mcpTools))
	}
	if sess.reg.Get(want) == nil {
		t.Fatalf("registry missing MCP tool %q", want)
	}
	if out := intg_mcpEcho(t, sess, want, "inline-hello"); out != "echo: inline-hello" {
		t.Errorf("tool output = %q, want %q", out, "echo: inline-hello")
	}
}

func TestIntg_InitMCP_PluginProvidedServerMerges(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	// A plugin whose .mcp.json contributes an MCP server exercises the
	// plugin-config merge layer of initMCP (pluginMCPConfigs merged under the
	// discovered configs) in addition to the connect+register path.
	dir := makePluginDir(t, "mcpplug")
	mcpJSON, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"svc": map[string]any{"command": bin},
		},
	})
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// Plugin MCP servers are namespaced "plugin_<plugin>_<server>".
	const want = "plugin_mcpplug_svc__echo"
	if sess.mcpMgr == nil {
		t.Fatal("mcpMgr is nil after initMCP with a plugin-provided server")
	}
	if sess.reg.Get(want) == nil {
		t.Fatalf("registry missing plugin MCP tool %q; have MCP tools %v", want, intg_toolDefNames(sess.mcpTools))
	}
	if out := intg_mcpEcho(t, sess, want, "plugin-hello"); out != "echo: plugin-hello" {
		t.Errorf("tool output = %q, want %q", out, "echo: plugin-hello")
	}
}

// TestIntg_InitMCP_PluginBadInlineMCPServersSurvives covers Task 12: a plugin
// whose inline mcpServers map has an entry that fails mcpconfig.ParseServerMap
// (here, an empty server name) used to abort plugin.Load/LoadAll entirely,
// taking the whole session down with it. It must now degrade to a
// plugin-level warning: NewSession succeeds, the bad plugin contributes no
// MCP server, and a WARNING event names the plugin.
func TestIntg_InitMCP_PluginBadInlineMCPServersSurvives(t *testing.T) {
	t.Parallel()
	dir := makePluginDir(t, "badmcpplug")
	metaDir := filepath.Join(dir, ".claude-plugin")
	manifest := `{"name": "badmcpplug", "mcpServers": {"": {"command": "somecmd"}}}`
	if err := os.WriteFile(filepath.Join(metaDir, "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	client := llm.NewClient()
	cfg := SessionConfig{PluginDirs: []string{dir}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession must survive a plugin with a bad inline mcpServers entry, got: %v", err)
	}

	var sawWarning bool
	for _, w := range drainWarnings(t, sess) {
		if strings.Contains(w.Message, "badmcpplug") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Fatal("expected a WARNING event naming the plugin with the bad inline mcpServers entry")
	}
}

func TestIntg_InitMCP_RegisterToolsError(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)

	// A 60-char server name pushes the namespaced tool name ("<name>__echo")
	// past the 64-char provider limit, so RegisterTools fails validation after a
	// successful connect+discover. The server is demoted to failed and its tool
	// dropped, but NewSession now survives instead of reporting the error.
	longName := strings.Repeat("a", 60)
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{longName + ":" + bin}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession must survive an MCP tool name exceeding the length limit, got: %v", err)
	}
	// Registration failure demotes the connection but leaves its live
	// ClientSession owned by the Manager; take ownership before any assertion so
	// fatal paths close the Manager, ClientSession, and subprocess too.
	t.Cleanup(sess.Close)
	if want := longName + "__echo"; sess.reg.Get(want) != nil {
		t.Error("a failed server must contribute no callable tool")
	}
	// The pending warning is flushed onto the event stream at SESSION_START
	// (session_events.go's emitSessionStartEnvelope flushes pendingMCPWarnings
	// and resets it to nil), so it no longer sits on the field by the time
	// NewSession returns: check the stream instead.
	var sawWarning bool
	for _, w := range drainWarnings(t, sess) {
		if strings.Contains(w.Message, longName) {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Fatal("expected a WARNING event for the register failure")
	}
}

func TestIntg_InitMCP_ConnectError(t *testing.T) {
	t.Parallel()
	// `true` exits immediately without speaking MCP, so its stdout closes before
	// the initialize handshake completes: mcp.NewManager's Connect fails. initMCP
	// now folds that failure into a pending warning instead of aborting the
	// session, so NewSession succeeds with the dead server's tool absent.
	truePath, err := exec.LookPath("true")
	if err != nil {
		t.Skipf("`true` not found: %v", err)
	}
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"deadsvc:" + truePath}}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession must survive a dead MCP server, got: %v", err)
	}
	if sess.reg.Get("deadsvc__echo") != nil {
		t.Error("a failed server must contribute no callable tool")
	}
	// The pending warning is flushed onto the event stream at SESSION_START
	// (session_events.go's emitSessionStartEnvelope flushes pendingMCPWarnings
	// and resets it to nil), so it no longer sits on the field by the time
	// NewSession returns: check the stream instead.
	var sawWarning bool
	for _, w := range drainWarnings(t, sess) {
		if strings.Contains(w.Message, "deadsvc") {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Fatal("expected a WARNING event for the dead server")
	}
}

func TestIntg_InitMCP_DiscoverError(t *testing.T) {
	t.Parallel()
	// A malformed inline spec (no colon) fails mcpconfig.Discover, so initMCP
	// returns before spawning anything and NewSession reports the error.
	client := llm.NewClient()
	cfg := SessionConfig{MCPInline: []string{"missing-colon-spec"}}
	_, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		t.Fatal("expected NewSession to fail on a malformed inline MCP spec")
	}
	if !strings.Contains(err.Error(), "MCP") {
		t.Errorf("error %q does not mention MCP", err.Error())
	}
}

// TestIntg_InitMCP_GlobalConfigParseErrorSurvives is the non-fatal counterpart
// to TestIntg_InitMCP_DiscoverError: a malformed *global* mcp.json (layer 1,
// not CLI-supplied) must not abort session construction. mcpconfig.Discover
// folds that layer's parse failure into a warning instead of an error, and
// initMCP folds the warning into pendingMCPWarnings, so NewSession succeeds
// with zero MCP servers.
//
// This test cannot run in parallel with its siblings: it points
// XDG_CONFIG_HOME at a temp dir via t.Setenv, and every other test in this
// file that constructs a Session calls t.Parallel(). Go's test driver runs
// all non-parallel top-level tests to completion (Setenv's restore included)
// before any parallel test body executes, so omitting t.Parallel() here is
// what keeps this safe rather than racy.
func TestIntg_InitMCP_GlobalConfigParseErrorSurvives(t *testing.T) {
	globalDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	evenerDir := filepath.Join(globalDir, "evener")
	if err := os.MkdirAll(evenerDir, 0755); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(evenerDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatalf("NewSession must survive a malformed global MCP config, got: %v", err)
	}

	var sawWarning bool
	for _, w := range drainWarnings(t, sess) {
		if strings.Contains(w.Message, mcpPath) {
			sawWarning = true
		}
	}
	if !sawWarning {
		t.Fatal("expected a WARNING event naming the malformed global MCP config path")
	}
}

// TestIntg_NewSession_LateErrorClosesMCPManager covers the Task-4b fix: when
// NewSession runs initSessionState through to a successful initMCP (mcpMgr is
// set, backed by a genuinely connected server) but then fails later — here on
// an unrecognized ContextStrategy, in selectStrategy — the MCP manager must
// still be closed before the error is returned. Otherwise the connected
// server's subprocess is orphaned: NewSession returns (nil, err), so the
// caller never gets a handle to close it.
//
// Detecting the close requires a real subprocess (agent-level tests cannot
// inject an mcpsdk.Transport spy into NewSession's real initMCP path), so this
// relies on the intgmcpserver exit marker: Manager.Close's session.Close call
// blocks on the child's Cmd.Wait, so by the time NewSession has returned, a
// server that was actually closed has already written its marker file.
func TestIntg_NewSession_LateErrorClosesMCPManager(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)
	marker := filepath.Join(t.TempDir(), "exited.marker")

	client := llm.NewClient()
	cfg := SessionConfig{
		MCPInline:       []string{"intgsvc:" + bin + " " + marker},
		ContextStrategy: "bogus-nonexistent-strategy",
	}
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err == nil {
		sess.Close()
		t.Fatal("expected NewSession to fail on an unknown context strategy")
	}
	if !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("err = %v, want unknown context strategy error", err)
	}
	if sess != nil {
		t.Fatal("expected a nil session on error")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("MCP server exit marker %s missing after synchronous cleanup: %v", marker, statErr)
	}
}

// TestIntg_RestoreSession_LateErrorClosesMCPManager is the restore-path
// counterpart of TestIntg_NewSession_LateErrorClosesMCPManager: the same
// unknown-context-strategy failure, reached via RestoreSessionFromMetaWithConfig
// after a successful initMCP, must also close the connected MCP manager
// instead of orphaning its subprocess.
func TestIntg_RestoreSession_LateErrorClosesMCPManager(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)
	marker := filepath.Join(t.TempDir(), "exited.marker")
	stateDir := t.TempDir()

	snap := SessionConfig{
		MCPInline:       []string{"intgsvc:" + bin + " " + marker},
		ContextStrategy: "bogus-nonexistent-strategy",
	}.toSnapshot()
	meta := schema.SessionMeta{ID: "01TASK4BRESTOREMCPCLOSE01", ProfileID: "openai", Model: "gpt-5.2", Config: snap}

	sess, err := RestoreSessionFromMetaWithConfig(
		w3init_restoreClient(), NewOpenAIProfile("gpt-5.2"),
		execenv.NewLocalExecutionEnvironment(t.TempDir()), meta,
		RestoreSessionConfig{StateDir: stateDir},
	)
	if err == nil {
		sess.Close()
		t.Fatal("expected RestoreSessionFromMetaWithConfig to fail on an unknown context strategy")
	}
	if !strings.Contains(err.Error(), "unknown context strategy") {
		t.Fatalf("err = %v, want unknown context strategy error", err)
	}
	if sess != nil {
		t.Fatal("expected a nil session on error")
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("MCP server exit marker %s missing after synchronous cleanup: %v", marker, statErr)
	}
}

// TestIntg_DelegateIdleReleasesStdioMCPServer pins the idle runtime release
// contract: a stable delegate whose generation finalized must release its
// resident runtime — which kills its stdio MCP server subprocess — while it
// sits idle, and must stay resumable via the cold restore path.
//
// Before the fix, a finalized delegate was retained as a live session (a
// terminal record kept warm for resume), so its plugin-provided stdio MCP
// server process stayed alive as long as its daemon did — effectively forever
// for a hub-spawned per-thread daemon (fleet evidence: 715 leaked chrome MCP
// server processes across 55 daemons at diagnosis time). The exit marker
// proves the child's server actually exited: the parent session's own server
// (same plugin config, so the same marker path) stays connected for the whole
// test, so a marker can only have been written by the released child's
// server.
func TestIntg_DelegateIdleReleasesStdioMCPServer(t *testing.T) {
	t.Parallel()
	bin := intg_buildMCPServer(t)
	marker := filepath.Join(t.TempDir(), "released.marker")

	dir := makePluginDir(t, "mcpplug")
	mcpJSON, err := json.Marshal(map[string]any{
		"mcpServers": map[string]any{
			"svc": map[string]any{"command": bin, "args": []string{marker}},
		},
	})
	if err != nil {
		t.Fatalf("marshal .mcp.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".mcp.json"), mcpJSON, 0644); err != nil {
		t.Fatalf("write .mcp.json: %v", err)
	}

	parentClient := llm.NewClient()
	parentClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
		return agenttest.FinalResponse("parent")
	}})

	var factoryCalls atomic.Int64
	factory := func() *llm.Client {
		factoryCalls.Add(1)
		c := llm.NewClient()
		c.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(llm.Request) llm.Response {
			return agenttest.FinalResponse("delegate done")
		}})
		registerTestSessionNamer(c)
		return c
	}

	cfg := SessionConfig{
		StateDir:         t.TempDir(),
		PluginDirs:       []string{dir},
		MaxSubagentDepth: 1,
	}
	cfg.testOnly.childClientFactory = factory
	// Shrink the follow-up grace so the scheduled release fires within the
	// poll window below; production keeps the default grace.
	shortGrace := 100 * time.Millisecond
	cfg.testOnly.delegateIdleReleaseDelay = &shortGrace

	sess, err := NewSession(parentClient, withTestSessionNamer(parentClient, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(t.TempDir()), cfg)
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		for range sess.Events() {
		}
		close(drainDone)
	}()
	defer func() {
		sess.Close()
		<-drainDone
	}()

	// TRIPWIRE: scripted adapters plus an in-process MCP subprocess; the
	// markers and completion normally settle in well under a second. 30s only
	// fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res := sess.createDelegate(ctx, delegateArgs{Task: "run once", DelegationAllowance: new(0)})
	if res.Err != nil {
		t.Fatalf("createDelegate: %v (status=%s reason=%s)", res.Err, res.Status, res.Reason)
	}
	delegateID := res.DelegateID
	childID := res.ChildSessionID
	if delegateID == "" || childID == "" {
		t.Fatalf("createDelegate returned empty ids: %+v", res)
	}
	child := sess.subagents.get(childID)
	if child == nil {
		t.Fatalf("subagent %s not found", childID)
	}
	child.mu.Lock()
	done := child.done
	child.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatalf("delegate run did not finish: %v", ctx.Err())
	}

	// Bracket continuity, part one: the parent's own server shares the
	// child's plugin config and therefore its marker path, so the marker
	// alone proves only that SOME server exited. A parent echo alive on both
	// sides of the wait attributes the exit to the child; the parent has no
	// shutdown path in this test, and its echo after the wait closes the
	// bracket.
	if out := intg_mcpEcho(t, sess, "plugin_mcpplug_svc__echo", "parent-alive-before"); out != "echo: parent-alive-before" {
		t.Fatalf("parent MCP echo before the release wait = %q, want %q", out, "echo: parent-alive-before")
	}

	// The release runs on a timer after the (here tiny) follow-up grace, so
	// poll for the child's MCP server to exit rather than assuming ordering.
	// TRIPWIRE: the release normally fires well inside a second of the 100ms
	// grace; 15s only bounds a genuine hang.
	waitForCondition(t, 15*time.Second, "idle release of delegate "+delegateID+" to stop its stdio MCP server (marker "+marker+")", func() bool {
		_, statErr := os.Stat(marker)
		return statErr == nil
	})

	// Scope: the parent's own plugin MCP server stays connected and callable.
	if out := intg_mcpEcho(t, sess, "plugin_mcpplug_svc__echo", "parent-alive"); out != "echo: parent-alive" {
		t.Errorf("parent MCP echo = %q, want %q", out, "echo: parent-alive")
	}

	// Resumability: a send to the idle delegate must restore it cold (a fresh
	// child client) and complete another generation.
	// A fresh bound for the restore phase: the release-wait context's 30s is
	// mostly spent by the marker poll, and the send must not inherit a
	// nearly-expired context under load.
	// TRIPWIRE: the scripted send and restore complete in well under a
	// second; 30s only bounds a genuine hang.
	restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer restoreCancel()
	send := (delegateRuntime{owner: sess}).send(restoreCtx, delegateID, "run again", 0).result
	if send.Err != nil {
		t.Fatalf("delegate_send after idle release: %+v", send)
	}
	var restored *subagent
	// TRIPWIRE: the cold restore normally appears within milliseconds of the
	// send; 15s only bounds a genuine hang.
	waitForCondition(t, 15*time.Second, "cold-restored record for delegate "+delegateID, func() bool {
		restored = sess.subagents.get(childID)
		return restored != nil && restored != child
	})
	restored.mu.Lock()
	rdone := restored.done
	restored.mu.Unlock()
	select {
	case <-rdone:
	case <-restoreCtx.Done():
		t.Fatalf("restored run did not finish: %v", restoreCtx.Err())
	}
	// The cold restore reuses the restoring parent's client — the same
	// binding a post-restart restore gets — so the spawn factory must have
	// been called exactly once; the new record for the same child session ID
	// completing a run is the cold-path proof.
	if got := factoryCalls.Load(); got != 1 {
		t.Errorf("childClientFactory calls = %d, want 1 (cold restore reuses the parent client)", got)
	}
}

func intg_hasToolDef(defs []llm.ToolDefinition, name string) bool {
	for _, d := range defs {
		if d.Name == name {
			return true
		}
	}
	return false
}

func intg_toolDefNames(defs []llm.ToolDefinition) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}
