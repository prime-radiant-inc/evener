package mcp

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/sandbox"
	"primeradiant.com/serf/llm"
)

const (
	mcpProgramCommandPath = "/synthetic/mcp-program"
	mcpProgramBwrapPath   = "/synthetic/bwrap"
)

// FuzzMCPManagerProgram drives Manager through its real in-memory MCP boundary.
// It deliberately keeps every transport in-process: no stdio command, HTTP
// client, listener, or wall-clock retry is involved. The program covers the
// manager contracts that matter after connection setup:
//
//   - a startup failure remains visible without suppressing a healthy sibling;
//   - discovered names are sanitized, registered, and routed to the owning MCP
//     server through Registry.ExecuteCall;
//   - a server-level (Channel B) error is an error-typed tool result but leaves
//     a reachable connection connected;
//   - a dropped first session reconnects through a scripted second transport
//     exactly once and retries the triggering call on the replacement session;
//   - register-stage rollback removes only the losing server's own names; and
//   - Close clears the live session and is idempotent.
//
// The fuzz bytes vary the request payload and the namespacing spelling. The
// oracles compare the observable registry result and Manager.Server state, not
// implementation-private transport details.
func FuzzMCPManagerProgram(f *testing.F) {
	for _, seed := range []struct {
		payload string
		variant uint8
	}{
		{payload: "hello", variant: 0},
		{payload: "spaces and punctuation !?", variant: 7},
		{payload: "", variant: 19},
		{payload: "unicode \U0001f642", variant: 25},
	} {
		f.Add(seed.payload, seed.variant)
	}

	f.Fuzz(func(t *testing.T, rawPayload string, variant uint8) {
		if len(rawPayload) > 512 {
			return
		}
		payload := mcpProgramPayload(rawPayload)
		mcpProgramLifecycle(t, payload, variant)
		mcpProgramRollback(t, payload)
		mcpProgramConstructionCases(t)
		mcpProgramManagerEdges(t)
	})
}

func mcpProgramLifecycle(t *testing.T, payload string, variant uint8) {
	t.Helper()
	ctx := context.Background()
	configName := fmt.Sprintf("alpha-%c", 'a'+rune(variant%26))
	toolPrefix := sanitizeToolName(configName) + "__"

	firstServer, firstTransport := mcpProgramServer(t, "first:")
	secondServer, secondTransport := mcpProgramServer(t, "second:")
	defer func() { _ = firstServer.Close() }()
	defer func() { _ = secondServer.Close() }()

	var dialCalls int32
	dial := func(context.Context) (mcpsdk.Transport, error) {
		switch atomic.AddInt32(&dialCalls, 1) {
		case 1:
			return firstTransport, nil
		case 2:
			return secondTransport, nil
		default:
			return nil, errors.New("unexpected MCP redial")
		}
	}
	failedDial := func(context.Context) (mcpsdk.Transport, error) {
		return nil, errors.New("scripted startup failure")
	}

	now := time.Unix(1_700_000_000, 0)
	mgr, outcomes := NewManagerWithClock(ctx,
		[]mcpconfig.ServerConfig{
			{Name: configName, Type: "stdio"},
			{Name: "offline", Type: "stdio"},
		},
		[]func(context.Context) (mcpsdk.Transport, error){dial, failedDial},
		func() time.Time { return now },
	)
	if mgr == nil {
		t.Fatal("NewManagerWithClock returned nil for non-empty configs")
	}
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Name != "offline" || outcomes[0].Stage != "connect" || outcomes[0].Err == nil {
		t.Fatalf("startup outcomes = %+v, want one offline connect failure", outcomes)
	}
	if got := mgr.conns[0].clock(); !got.Equal(now) {
		t.Fatalf("manager clock = %v, want fake clock %v", got, now)
	}

	var reconnects int32
	mgr.OnReconnect = func(name string) {
		if name != configName {
			t.Errorf("OnReconnect name = %q, want %q", name, configName)
		}
		atomic.AddInt32(&reconnects, 1)
	}

	defs := mgr.ToolDefinitions()
	if len(defs) != 2 || !mcpProgramHasDefinition(defs, toolPrefix+"echo") || !mcpProgramHasDefinition(defs, toolPrefix+"report_error") {
		t.Fatalf("ToolDefinitions = %#v, want namespaced echo and report_error", defs)
	}

	reg := tool.NewRegistry()
	if got := mgr.RegisterTools(reg); len(got) != 0 {
		t.Fatalf("RegisterTools outcomes = %+v", got)
	}
	if reg.Get(toolPrefix+"echo") == nil || reg.Get(toolPrefix+"report_error") == nil {
		t.Fatalf("registered tools missing from registry: %v", reg.Names())
	}

	env := &agenttest.DenyEnv{WorkDir: t.TempDir(), Seed: uint64(variant)}
	first := mcpProgramExecute(t, ctx, reg, env, toolPrefix+"echo", payload)
	if first.IsError || first.Output != "first:"+payload {
		t.Fatalf("first routed call = %+v, want first server reply", first)
	}

	channelB := mcpProgramExecute(t, ctx, reg, env, toolPrefix+"report_error", payload)
	if !channelB.IsError || channelB.Err == nil || channelB.Output != "[MCP Error] upstream:"+payload {
		t.Fatalf("Channel-B result = %+v, want typed upstream error", channelB)
	}
	beforeDrop := mcpProgramServerByName(t, mgr.Servers(), configName)
	if beforeDrop.Status != "connected" || beforeDrop.Error != "[MCP Error] upstream:"+payload {
		t.Fatalf("server after Channel-B error = %+v, want connected with upstream detail", beforeDrop)
	}

	if err := firstServer.Close(); err != nil {
		t.Fatalf("close first in-memory server: %v", err)
	}
	retried := mcpProgramExecute(t, ctx, reg, env, toolPrefix+"echo", payload)
	if retried.IsError || retried.Output != "second:"+payload {
		t.Fatalf("reconnect retry = %+v, want second server reply", retried)
	}
	if got := atomic.LoadInt32(&dialCalls); got != 2 {
		t.Fatalf("dial calls = %d, want initial connection plus one redial", got)
	}
	if got := atomic.LoadInt32(&reconnects); got != 1 {
		t.Fatalf("OnReconnect calls = %d, want 1", got)
	}
	afterDrop := mcpProgramServerByName(t, mgr.Servers(), configName)
	if afterDrop.Status != "connected" {
		t.Fatalf("server after successful reconnect = %+v, want connected", afterDrop)
	}

	mgr.Close()
	mgr.Close()
	if sess := mgr.conns[0].lockedSession(); sess != nil {
		t.Fatal("Close left a live MCP session behind")
	}
}

func mcpProgramRollback(t *testing.T, payload string) {
	t.Helper()
	ctx := context.Background()
	winnerServer, winnerTransport := mcpProgramServerWithTools(t, map[string]string{
		"keep": "winner:" + payload,
		"same": "winner:" + payload,
	})
	loserServer, loserTransport := mcpProgramServerWithTools(t, map[string]string{
		"rollback": "loser:" + payload,
		"same":     "loser:" + payload,
	})
	defer func() { _ = winnerServer.Close() }()
	defer func() { _ = loserServer.Close() }()

	mgr, outcomes := NewManager(ctx,
		[]mcpconfig.ServerConfig{{Name: "foo_bar", Type: "stdio"}, {Name: "foo-bar", Type: "stdio"}},
		[]func(context.Context) (mcpsdk.Transport, error){staticDial(winnerTransport), staticDial(loserTransport)},
	)
	if len(outcomes) != 0 {
		t.Fatalf("rollback manager startup outcomes = %+v", outcomes)
	}
	defer mgr.Close()

	reg := tool.NewRegistry()
	got := mgr.RegisterTools(reg)
	if len(got) != 1 || got[0].Name != "foo-bar" || got[0].Stage != "register" || got[0].Err == nil {
		t.Fatalf("rollback outcomes = %+v, want foo-bar register failure", got)
	}
	if reg.Get("foo_bar__keep") == nil || reg.Get("foo_bar__same") == nil {
		t.Fatalf("winner tools were not retained: %v", reg.Names())
	}
	if reg.Get("foo_bar__rollback") != nil {
		t.Fatalf("loser-owned registration survived rollback: %v", reg.Names())
	}
	if got := mcpProgramServerByName(t, mgr.Servers(), "foo-bar"); got.Status != "failed" {
		t.Fatalf("loser server state = %+v, want failed", got)
	}
	if got := mcpProgramServerByName(t, mgr.Servers(), "foo_bar"); got.Status != "connected" {
		t.Fatalf("winner server state = %+v, want connected", got)
	}
}

func mcpProgramServer(t *testing.T, prefix string) (*mcpsdk.ServerSession, mcpsdk.Transport) {
	t.Helper()
	return mcpProgramServerWithTools(t, map[string]string{
		"echo":         prefix,
		"report-error": "upstream:",
	})
}

func mcpProgramServerWithTools(t *testing.T, replies map[string]string) (*mcpsdk.ServerSession, mcpsdk.Transport) {
	t.Helper()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "fuzz-manager", Version: "v1"}, nil)
	for name, prefix := range replies {
		name, prefix := name, prefix
		server.AddTool(&mcpsdk.Tool{
			Name:        name,
			Description: "fuzz manager " + name,
			InputSchema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]any{"message": map[string]any{"type": "string"}},
				"required":             []string{"message"},
			},
		}, func(_ context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
			var args struct {
				Message string `json:"message"`
			}
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, err
			}
			return &mcpsdk.CallToolResult{
				Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: prefix + args.Message}},
				IsError: name == "report-error",
			}, nil
		})
	}
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	session, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("connect in-memory MCP server: %v", err)
	}
	return session, clientTransport
}

func mcpProgramExecute(t *testing.T, ctx context.Context, reg *tool.Registry, env *agenttest.DenyEnv, name, message string) tool.ExecResult {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"message": message, "purpose": "fuzz manager routing"})
	if err != nil {
		t.Fatalf("marshal MCP args: %v", err)
	}
	return reg.ExecuteCall(ctx, env, llm.ToolCallData{ID: "mcp-program", Name: name, Arguments: raw})
}

func mcpProgramHasDefinition(defs []llm.ToolDefinition, want string) bool {
	for _, def := range defs {
		if def.Name == want {
			return true
		}
	}
	return false
}

func mcpProgramServerByName(t *testing.T, servers []mcpconfig.ServerInfo, want string) mcpconfig.ServerInfo {
	t.Helper()
	for _, server := range servers {
		if server.Name == want {
			return server
		}
	}
	t.Fatalf("server %q missing from %+v", want, servers)
	return mcpconfig.ServerInfo{}
}

func mcpProgramPayload(raw string) string {
	if len(raw) > 96 {
		raw = raw[:96]
	}
	return hex.EncodeToString([]byte(raw))
}

// mcpProgramConstructionCases covers the manager's transport-construction
// boundary without dialing a real server. Command, SSE, and streamable HTTP
// transports are only allocated and inspected; the one header path uses an
// in-memory RoundTripper, so no listener or network request is involved.
func mcpProgramConstructionCases(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	if mgr, outcomes := NewManager(ctx, nil, nil); mgr != nil || outcomes != nil {
		t.Fatalf("empty NewManager = %#v, %#v", mgr, outcomes)
	}
	if mgr, outcomes := NewManagerWithClock(ctx, nil, nil, func() time.Time { return time.Unix(0, 0) }); mgr != nil || outcomes != nil {
		t.Fatalf("empty NewManagerWithClock = %#v, %#v", mgr, outcomes)
	}

	cases := []struct {
		cfg      mcpconfig.ServerConfig
		wantType string
		wantErr  bool
	}{
		{cfg: mcpconfig.ServerConfig{Type: "stdio"}, wantErr: true},
		{cfg: mcpconfig.ServerConfig{Type: "stdio", Command: mcpProgramCommandPath, Args: []string{"--probe"}}, wantType: "command"},
		{cfg: mcpconfig.ServerConfig{Type: "sse"}, wantErr: true},
		{cfg: mcpconfig.ServerConfig{Type: "sse", URL: "https://invalid.example/mcp"}, wantType: "sse"},
		{cfg: mcpconfig.ServerConfig{Type: "sse", URL: "https://invalid.example/mcp", Headers: map[string]string{"X-Program": "sse"}}, wantType: "sse-header"},
		{cfg: mcpconfig.ServerConfig{Type: "http"}, wantErr: true},
		{cfg: mcpconfig.ServerConfig{Type: "http", URL: "https://invalid.example/mcp"}, wantType: "http"},
		{cfg: mcpconfig.ServerConfig{Type: "http", URL: "https://invalid.example/mcp", Headers: map[string]string{"X-Program": "http"}}, wantType: "http-header"},
		{cfg: mcpconfig.ServerConfig{Type: "unknown"}, wantErr: true},
	}
	for _, tc := range cases {
		transport, err := transportForConfig(tc.cfg)
		if tc.wantErr {
			if err == nil || transport != nil {
				t.Fatalf("transportForConfig(%+v) = %T, %v; want error", tc.cfg, transport, err)
			}
			continue
		}
		if err != nil || transport == nil {
			t.Fatalf("transportForConfig(%+v): %T, %v", tc.cfg, transport, err)
		}
		switch tc.wantType {
		case "command":
			if command, ok := transport.(*mcpsdk.CommandTransport); !ok || command.Command == nil || command.Command.Args[0] != mcpProgramCommandPath || command.Command.Path != mcpProgramCommandPath {
				t.Fatalf("stdio transport = %#v", transport)
			}
		case "sse", "sse-header":
			sse, ok := transport.(*mcpsdk.SSEClientTransport)
			if !ok || (tc.wantType == "sse-header" && sse.HTTPClient == nil) {
				t.Fatalf("SSE transport = %#v", transport)
			}
		case "http", "http-header":
			stream, ok := transport.(*mcpsdk.StreamableClientTransport)
			if !ok || (tc.wantType == "http-header" && stream.HTTPClient == nil) {
				t.Fatalf("HTTP transport = %#v", transport)
			}
		}
	}

	plainDial := productionDial(mcpconfig.ServerConfig{Type: "stdio", Command: mcpProgramCommandPath}, nil)
	plain, err := plainDial(ctx)
	if err != nil {
		t.Fatalf("unsandboxed production dial: %v", err)
	}
	if command, ok := plain.(*mcpsdk.CommandTransport); !ok || command.Command.Path != mcpProgramCommandPath {
		t.Fatalf("unsandboxed production transport = %#v", plain)
	}

	wrapped := mcpProgramSandboxWrapper(t, true)
	var options managerOptions
	WithSandboxWrapper(wrapped)(&options)
	if options.wrapper != wrapped {
		t.Fatal("WithSandboxWrapper did not retain wrapper")
	}
	confinedDial := productionDialWithEnv(mcpconfig.ServerConfig{
		Name:    "program",
		Type:    "stdio",
		Command: mcpProgramCommandPath,
		Args:    []string{"--probe"},
		Env:     map[string]string{"PROGRAM_VALUE": "kept", "MCP_SERVER_TOKEN": "configured"},
	}, wrapped, mcpProgramFixedEnvironment)
	confined, err := confinedDial(ctx)
	if err != nil {
		t.Fatalf("confined production dial: %v", err)
	}
	confinedCommand, ok := confined.(*mcpsdk.CommandTransport)
	if !ok || confinedCommand.Command == nil {
		t.Fatalf("confined command = %#v", confined)
	}
	joinedEnv := strings.Join(confinedCommand.Command.Env, "\n")
	if confinedCommand.Command.Path != mcpProgramBwrapPath || !strings.Contains(joinedEnv, "PROGRAM_VALUE=kept") || !strings.Contains(joinedEnv, "MCP_SERVER_TOKEN=configured") || strings.Contains(joinedEnv, "SERF_AMBIENT_API_KEY=") || strings.Contains(joinedEnv, "SSH_AUTH_SOCK=") || confinedCommand.Command.ExtraFiles != nil {
		t.Fatalf("confined command = %#v", confined)
	}

	netOff := mcpProgramSandboxWrapper(t, false)
	for _, typ := range []string{"sse", "http"} {
		if _, err := productionDialWithEnv(mcpconfig.ServerConfig{Name: "remote", Type: typ, URL: "https://invalid.example/mcp"}, netOff, mcpProgramFixedEnvironment)(ctx); err == nil || !strings.Contains(err.Error(), "network egress is disabled") {
			t.Fatalf("net-off %s dial error = %v", typ, err)
		}
	}

	base := &mcpProgramRoundTripper{}
	request, err := http.NewRequest(http.MethodGet, "https://invalid.example/headers", nil)
	if err != nil {
		t.Fatalf("new in-memory request: %v", err)
	}
	if _, err := (&headerRoundTripper{base: base, headers: map[string]string{"X-Program": "value"}}).RoundTrip(request); err != nil || base.got == nil || base.got.Header.Get("X-Program") != "value" {
		t.Fatalf("header round trip = req=%#v err=%v", base.got, err)
	}
	if client := httpClientWithHeaders(map[string]string{"X-Program": "value"}); client == nil || client.Transport == nil {
		t.Fatal("httpClientWithHeaders returned an unusable client")
	}

	merged := mergeEnvInto([]string{"A=old", "B=keep", "MALFORMED"}, map[string]string{"A": "new", "C": "add"})
	if mcpProgramEnvValue(merged, "A") != "new" || mcpProgramEnvValue(merged, "B") != "keep" || mcpProgramEnvValue(merged, "C") != "add" {
		t.Fatalf("mergeEnvInto = %v", merged)
	}
	if got := errString(nil); got != "" {
		t.Fatalf("nil error string = %q", got)
	}
	if got := errString(errors.New("detail")); got != "detail" {
		t.Fatalf("non-nil error string = %q", got)
	}
}

func mcpProgramManagerEdges(t *testing.T) {
	t.Helper()
	ctx := context.Background()

	var nilManager *Manager
	if nilManager.ToolDefinitions() != nil || nilManager.RegisterTools(nil) != nil || nilManager.Servers() != nil {
		t.Fatal("nil manager did not preserve nil receiver contract")
	}
	nilManager.Close()

	failed := failedConn(mcpconfig.ServerConfig{Name: "failed"}, nil, errors.New("startup"))
	if failed.status != "failed" || failed.lastErr == nil || failed.dial != nil {
		t.Fatalf("failedConn = %s", mcpProgramConnSummary(&failed))
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "program", Version: "v1"}, nil)
	connectFail := connectOne(ctx, client, mcpconfig.ServerConfig{Name: "connect-fail"}, func(context.Context) (mcpsdk.Transport, error) {
		return mcpProgramFailTransport{err: errors.New("connect failure")}, nil
	})
	if connectFail.status != "failed" || connectFail.lastErr == nil {
		t.Fatalf("connect failure conn = %s", mcpProgramConnSummary(&connectFail))
	}

	listServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "list-fail", Version: "v1"}, nil)
	listServer.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			if method == "tools/list" {
				return nil, errors.New("scripted tools/list failure")
			}
			return next(ctx, method, req)
		}
	})
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := listServer.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("connect list-fail server: %v", err)
	}
	defer func() { _ = serverSession.Close() }()
	listFail := connectOne(ctx, client, mcpconfig.ServerConfig{Name: "list-fail"}, staticDial(clientTransport))
	if listFail.status != "failed" || listFail.lastErr == nil || listFail.session != nil {
		t.Fatalf("list-tools failure conn = %s", mcpProgramConnSummary(&listFail))
	}

	badNameManager := &Manager{conns: []conn{{
		name:         "bad-name",
		status:       "connected",
		tools:        []llm.ToolDefinition{{Name: strings.Repeat("x", 65), Parameters: map[string]any{"type": "object"}}},
		origNames:    map[string]string{},
		lastErrAt:    time.Unix(0, 0),
		backoffUntil: time.Time{},
	}}}
	if outcomes := badNameManager.RegisterTools(tool.NewRegistry()); len(outcomes) != 1 || outcomes[0].Stage != "register" || badNameManager.conns[0].status != "failed" {
		t.Fatalf("invalid tool registration = %+v / %s", outcomes, mcpProgramConnSummary(&badNameManager.conns[0]))
	}

	badSchemaManager := &Manager{conns: []conn{{
		name:      "bad-schema",
		status:    "connected",
		tools:     []llm.ToolDefinition{{Name: "bad_schema", Description: "bad schema", Parameters: map[string]any{"type": "array"}}},
		origNames: map[string]string{"bad_schema": "bad_schema"},
	}}}
	if outcomes := badSchemaManager.RegisterTools(tool.NewRegistry()); len(outcomes) != 1 || outcomes[0].Err == nil || badSchemaManager.conns[0].status != "failed" {
		t.Fatalf("schema registration = %+v / %s", outcomes, mcpProgramConnSummary(&badSchemaManager.conns[0]))
	}
	if defs := badSchemaManager.ToolDefinitions(); len(defs) != 0 {
		t.Fatalf("failed registration still advertised definitions: %#v", defs)
	}

	now := time.Unix(100, 0)
	var redials int32
	backoffConn := &conn{
		client: client,
		dial: func(context.Context) (mcpsdk.Transport, error) {
			atomic.AddInt32(&redials, 1)
			return nil, errors.New("redial fail")
		},
		now: func() time.Time { return now },
	}
	if session, ok := backoffConn.reconnect(ctx); session != nil || ok || atomic.LoadInt32(&redials) != 1 || !backoffConn.backoffUntil.After(now) || backoffConn.reconnecting {
		t.Fatalf("failed reconnect state: session=%v ok=%v calls=%d conn=%s", session, ok, redials, mcpProgramConnSummary(backoffConn))
	}
	if session, ok := backoffConn.reconnect(ctx); session != nil || ok || atomic.LoadInt32(&redials) != 1 {
		t.Fatalf("backoff did not suppress repeat reconnect: session=%v ok=%v calls=%d", session, ok, redials)
	}
	for _, c := range []*conn{
		{closed: true, client: client, dial: backoffConn.dial},
		{reconnecting: true, client: client, dial: backoffConn.dial},
	} {
		if session, ok := c.reconnect(ctx); session != nil || ok {
			t.Fatalf("gated reconnect = %v, %v", session, ok)
		}
	}
	closed := &conn{closed: true}
	if old, committed := closed.swap(nil); old != nil || committed {
		t.Fatalf("closed swap = %v, %v", old, committed)
	}

	for _, tc := range []struct {
		err  error
		want bool
	}{
		{mcpsdk.ErrConnectionClosed, true},
		{io.ErrClosedPipe, true},
		{os.ErrClosed, true},
		{net.ErrClosed, true},
		{io.EOF, true},
		{io.ErrUnexpectedEOF, true},
		{context.Canceled, false},
		{errors.Join(context.DeadlineExceeded, io.EOF), false},
		{errors.New("application error"), false},
	} {
		if got := isDroppedConn(tc.err); got != tc.want {
			t.Fatalf("isDroppedConn(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
	for _, schema := range []any{nil, map[string]any{"type": "object"}, mcpProgramSchema{Type: "object"}, make(chan int), []string{"not", "a", "map"}} {
		params := mcpSchemaToParams(schema)
		if params == nil || params["type"] == nil {
			t.Fatalf("mcpSchemaToParams(%T) = %#v", schema, params)
		}
	}
	result := &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{
		&mcpsdk.TextContent{Text: "text"},
		&mcpsdk.ImageContent{MIMEType: "image/png", Data: []byte("x")},
	}}
	if got := mcpResultToString(result); !strings.HasPrefix(got, "[MCP Error] ") || !strings.Contains(got, "text") {
		t.Fatalf("mixed MCP result = %q", got)
	}
	if mcpResultToString(nil) != "" || sanitizeToolName("a-b-c") != "a_b_c" || sanitizeToolName("plain") != "plain" {
		t.Fatal("MCP helper normalization contract failed")
	}
}

type mcpProgramFailTransport struct{ err error }

type mcpProgramSchema struct {
	Type string `json:"type"`
}

func (t mcpProgramFailTransport) Connect(context.Context) (mcpsdk.Connection, error) {
	return nil, t.err
}

type mcpProgramRoundTripper struct{ got *http.Request }

func (r *mcpProgramRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.got = req.Clone(req.Context())
	return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func mcpProgramEnvValue(env []string, key string) string {
	for _, entry := range env {
		if gotKey, value, ok := strings.Cut(entry, "="); ok && gotKey == key {
			return value
		}
	}
	return ""
}

// mcpProgramSandboxWrapper injects the minimal enforcing policy needed to
// inspect command confinement. It deliberately does not call sandbox.Resolve:
// Resolve structurally classifies a cwd and could walk a hostile TMPDIR ancestor
// containing .git. This explicit NonGit layout stays entirely inside the test
// root, and the synthetic bwrap path is never executed.
func mcpProgramSandboxWrapper(t *testing.T, network bool) *sandbox.Wrapper {
	t.Helper()
	root := t.TempDir()
	policy := sandbox.ResolvedPolicy{
		Mode:          sandbox.ModeWorkspaceWrite,
		Network:       network,
		Backend:       sandbox.BackendBwrap,
		CacheStrategy: sandbox.CacheSessionPrivate,
		SessionTmp:    true,
		FileTool: sandbox.AccessScope{
			Read:       sandbox.ReadAnywhere,
			WriteRoots: []string{root},
		},
		Spawned: sandbox.AccessScope{
			Read:       sandbox.ReadAnywhere,
			WriteRoots: []string{root},
		},
		Git: sandbox.GitLayout{Kind: sandbox.NonGit, WorktreeRoot: root},
	}
	wrapper, err := sandbox.NewWrapper(policy, mcpProgramBwrapPath, filepath.Join(root, "session-tmp"))
	if err != nil {
		t.Fatalf("new no-Git sandbox wrapper: %v", err)
	}
	return wrapper
}

func mcpProgramFixedEnvironment() []string {
	return []string{
		"PATH=/synthetic/bin",
		"SERF_AMBIENT_API_KEY=must-not-leak",
		"SSH_AUTH_SOCK=/synthetic/agent.sock",
		"TMPDIR=/synthetic/ambient-tmp",
		"GOCACHE=/synthetic/ambient-cache",
	}
}

func mcpProgramConnSummary(c *conn) string {
	if c == nil {
		return "<nil>"
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("name=%q status=%q closed=%v reconnecting=%v session=%v err=%v", c.name, c.status, c.closed, c.reconnecting, c.session != nil, c.lastErr)
}
