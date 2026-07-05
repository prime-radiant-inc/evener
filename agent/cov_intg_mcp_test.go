//go:build !short

package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// These tests drive Session.initMCP against a REAL stdio MCP server subprocess
// (agent/testdata/intgmcpserver), compiled in-test. They exercise the initMCP
// arms that only run when a server is actually configured: connect via
// mcp.NewManager, register the discovered tools into the session registry, and
// merge plugin-provided MCP configs. The server exits on stdin EOF, so the
// session's Close tears it down deterministically without a terminate timeout.

// intg_buildMCPServer compiles the testdata stdio MCP server into a fresh temp
// dir and returns the binary path. The Go build cache makes repeated builds
// (e.g. under -count) cheap, and the path has no spaces so it survives the
// whitespace-split of an inline MCP spec.
func intg_buildMCPServer(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "intgmcpserver")
	cmd := exec.Command("go", "build", "-o", bin, "./testdata/intgmcpserver")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building test MCP server: %v\n%s", err, out)
	}
	return bin
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
	serfDir := filepath.Join(globalDir, "serf")
	if err := os.MkdirAll(serfDir, 0755); err != nil {
		t.Fatal(err)
	}
	mcpPath := filepath.Join(serfDir, "mcp.json")
	if err := os.WriteFile(mcpPath, []byte(`{invalid`), 0644); err != nil {
		t.Fatal(err)
	}

	client := llm.NewClient()
	sess, err := NewSession(client, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(t.TempDir()), SessionConfig{})
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
	intg_awaitMCPExitMarker(t, marker, 5*time.Second)
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
	intg_awaitMCPExitMarker(t, marker, 5*time.Second)
}

// intg_awaitMCPExitMarker polls for the exit-marker file testdata/intgmcpserver
// writes right before it exits (see main.go) and fails the test if it does not
// appear within timeout. Manager.Close blocks on the subprocess's Cmd.Wait, so
// a marker written by a Close'd server is already on disk by the time the
// caller under test has returned; the poll only guards against incidental
// scheduling jitter, not against a real close-vs-not-close race.
func intg_awaitMCPExitMarker(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("MCP server exit marker %s never appeared; the underlying subprocess was not closed", path)
		}
		time.Sleep(10 * time.Millisecond)
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
