package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/llm"
)

// Manager manages connections to external MCP servers.
type Manager struct {
	conns []conn

	// OnReconnect, if set, is called after a conn's lazy reconnect (Task 8)
	// successfully swaps in a healed session, with that server's configured
	// name. It is nil unless a caller wires it up (initMCP wires it to a
	// recovery diagnostic); most tests never set it, so every call site
	// guards it with a nil check. Exported (unlike conns) because it must be
	// settable from outside the package — initMCP lives in package agent.
	OnReconnect func(name string)
}

type conn struct {
	name string
	cfg  mcpconfig.ServerConfig
	dial func(context.Context) (mcpsdk.Transport, error) // re-dials a fresh transport; set for every conn, even ones whose initial connect failed, so a later reconnect (Task 8) always has something to call
	// client is the shared *mcpsdk.Client used to Connect this conn's
	// sessions — both the initial connect and any later reconnect (Task 8).
	// It is only populated once a conn has actually reached "connected":
	// a conn that never leaves "failed" never gets a registered exec
	// closure, so it can never call reconnect and never needs one.
	client    *mcpsdk.Client
	session   *mcpsdk.ClientSession
	tools     []llm.ToolDefinition // namespaced: servername__toolname
	origNames map[string]string    // sanitized namespaced name → original MCP tool name
	status    string               // "connected" / "degraded" / "failed"
	lastErr   error
	lastErrAt time.Time

	// mu guards session, status, lastErr, lastErrAt, closed, backoffUntil,
	// and reconnecting against concurrent access between Close(), a
	// reconnect swap (Task 8), and Servers(). Because conn embeds a mutex, it
	// must never be copied by value once it lives in Manager.conns — every
	// access goes through a pointer (&mgr.conns[i]), never a `range
	// mgr.conns` value copy.
	mu     sync.Mutex
	closed bool
	// backoffUntil gates lazy reconnect attempts (Task 8): its zero value
	// means "a reconnect may be attempted immediately" (every conn starts
	// this way). reconnect resets it to time.Now().Add(reconnectBackoff)
	// after every attempt it makes, success or failure, so a burst of calls
	// against a still-flaky server doesn't redial on every single one.
	backoffUntil time.Time
	// reconnecting marks that a dial is currently in flight for this conn. It
	// is set under c.mu before the lock is released for the (lock-free) dial,
	// and cleared under c.mu once that attempt completes, success or
	// failure. Without it, two calls that hit ErrConnectionClosed at nearly
	// the same moment can both pass the closed/backoffUntil gate —
	// backoffUntil is only updated once a dial finishes, so a second
	// concurrent caller sees the same gate state the first one did — and
	// both dial and swap: the second commit then displaces and closes the
	// first's freshly-committed session, silently invalidating a session the
	// first caller was already told (ok=true) to retry against. reconnecting
	// makes a second concurrent caller bail out immediately instead of
	// racing its own redundant dial.
	reconnecting bool
}

// ServerOutcome reports a per-server failure at one assembly stage. Stage is
// "connect" (transport build, connect handshake, or tools/list) or "register".
type ServerOutcome struct {
	Name  string
	Stage string
	Err   error
}

// failedConn builds a conn recording a connect-stage failure for cfg. session,
// tools, and origNames stay at their zero values: a failed conn has no live
// session and contributes no tools to ToolDefinitions/RegisterTools. dial is
// still stored so a later task could in principle retry the failed server.
func failedConn(cfg mcpconfig.ServerConfig, dial func(context.Context) (mcpsdk.Transport, error), err error) conn {
	return conn{name: cfg.Name, cfg: cfg, dial: dial, status: "failed", lastErr: err, lastErrAt: time.Now()}
}

// NewManager connects to all configured MCP servers concurrently and
// discovers their tools, namespacing them. Each server's transport build,
// connect, and tools/list run in their own goroutine under their own 10s
// timeout (derived from ctx), so one slow or hung server cannot delay or
// starve the others. It does not abort the batch when one server's transport
// build, connect, or tools/list fails: a conn is retained for every config
// (failed ones with status "failed" and no session), and each such failure is
// reported in the returned []ServerOutcome with Stage "connect". The manager
// is nil only when configs is empty. dials is optional: when nil, or when
// dials[i] is absent, config i dials its transport via the production
// closure (transportForConfig). When provided (for testing, or a hermetic
// in-memory fake), dials[i] corresponds to configs[i]. Every conn stores the
// dial closure it used — including a conn whose initial connect failed — so
// a later reconnect (Task 8) can call it again.
//
// conns is preallocated to len(configs) and each goroutine writes exactly one
// index (mgr.conns[i], matching configs[i]), so the result is always in
// config order regardless of which server's connect finishes first — no
// mutex is needed on conns itself, since every index has exactly one writer
// and mgr.conns is only read after wg.Wait() establishes happens-before.
func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, dials []func(context.Context) (mcpsdk.Transport, error)) (*Manager, []ServerOutcome) {
	if len(configs) == 0 {
		return nil, nil
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "serf",
		Version: "v1",
	}, nil)

	mgr := &Manager{conns: make([]conn, len(configs))}
	var wg sync.WaitGroup
	wg.Add(len(configs))
	for i, cfg := range configs {
		dial := productionDial(cfg)
		if dials != nil && i < len(dials) {
			dial = dials[i]
		}
		go func(i int, cfg mcpconfig.ServerConfig, dial func(context.Context) (mcpsdk.Transport, error)) {
			defer wg.Done()
			mgr.conns[i] = connectOne(ctx, client, cfg, dial)
		}(i, cfg, dial)
	}
	wg.Wait()

	var outcomes []ServerOutcome
	for i := range mgr.conns {
		c := &mgr.conns[i]
		if c.status != "connected" {
			outcomes = append(outcomes, ServerOutcome{Name: c.name, Stage: "connect", Err: c.lastErr})
		}
	}

	return mgr, outcomes
}

// connectOne dials a transport via dial, connects to a single MCP server, and
// discovers its tools — all bounded by a 10s timeout derived from ctx. Any
// failure dialing, connecting, or listing tools yields a "failed" conn
// recording the error; NewManager turns that into a connect-stage
// ServerOutcome. dial is stored on the returned conn regardless of outcome.
func connectOne(ctx context.Context, client *mcpsdk.Client, cfg mcpconfig.ServerConfig, dial func(context.Context) (mcpsdk.Transport, error)) conn {
	perServerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	transport, err := dial(perServerCtx)
	if err != nil {
		return failedConn(cfg, dial, err)
	}

	session, err := client.Connect(perServerCtx, transport, nil)
	if err != nil {
		return failedConn(cfg, dial, err)
	}

	// Discover tools from the server.
	result, err := session.ListTools(perServerCtx, nil)
	if err != nil {
		_ = session.Close()
		return failedConn(cfg, dial, err)
	}

	var tools []llm.ToolDefinition
	origNames := make(map[string]string, len(result.Tools))
	for _, t := range result.Tools {
		namespacedName := sanitizeToolName(cfg.Name + "__" + t.Name)
		origNames[namespacedName] = t.Name
		params := mcpSchemaToParams(t.InputSchema)
		tools = append(tools, llm.ToolDefinition{
			Name:        namespacedName,
			Description: t.Description,
			Parameters:  params,
		})
	}

	return conn{
		name:      cfg.Name,
		cfg:       cfg,
		dial:      dial,
		client:    client,
		session:   session,
		tools:     tools,
		origNames: origNames,
		status:    "connected",
	}
}

// productionDial returns the default dial factory for cfg: build a fresh
// transport via transportForConfig on every call. NewManager uses this
// whenever the caller doesn't supply an explicit dials[i] — i.e., real,
// non-test use, and also real reconnects (Task 8), since transportForConfig
// builds a brand-new transport each time it runs.
func productionDial(cfg mcpconfig.ServerConfig) func(context.Context) (mcpsdk.Transport, error) {
	return func(context.Context) (mcpsdk.Transport, error) { return transportForConfig(cfg) }
}

// ToolDefinitions returns namespaced tool definitions from servers whose
// tools are actually registered and callable (status "connected"). A server
// demoted to "failed" during RegisterTools contributes no definitions, so the
// system-prompt-visible tool surface always matches the callable set.
func (m *Manager) ToolDefinitions() []llm.ToolDefinition {
	if m == nil {
		return nil
	}
	var defs []llm.ToolDefinition
	for i := range m.conns {
		c := &m.conns[i]
		if c.status != "connected" {
			continue
		}
		defs = append(defs, c.tools...)
	}
	return defs
}

// RegisterTools registers execution closures for all MCP tools from
// connected servers into the given tool.Registry. It does not abort the
// whole batch when one server's tools fail name validation or collide with
// an existing registration: that server is demoted to status "failed" and
// reported in the returned []ServerOutcome with Stage "register", and the
// next server is attempted. Only the namespaced names THAT SERVER itself
// successfully registered before the failure are rolled back — a name that
// already belongs to an earlier winner (or a built-in tool) is never
// touched, because it was never added to the failing server's own list.
func (m *Manager) RegisterTools(reg *tool.Registry) []ServerOutcome {
	if m == nil {
		return nil
	}
	var outcomes []ServerOutcome
	for i := range m.conns {
		c := &m.conns[i]
		if c.status != "connected" {
			continue
		}

		var added []string
		for _, td := range c.tools {
			// Validate the namespaced name (length, charset).
			if err := llm.ValidateToolName(td.Name); err != nil {
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("MCP tool %q: %w", td.Name, err)))
				break
			}

			// Check for collision with existing tools (built-ins, or an
			// earlier server's tools within this same call). This is safe
			// because RegisterTools is called only during single-threaded
			// session init, after registerCoreTools has already completed.
			if reg.Get(td.Name) != nil {
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("MCP tool %q collides with existing tool", td.Name)))
				break
			}

			// Look up the original MCP tool name for CallTool.
			origName := c.origNames[td.Name]

			if err := reg.Register(tool.RegisteredTool{
				Tool: llm.Tool{Definition: td},
				Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
					call := func(sess *mcpsdk.ClientSession) (*mcpsdk.CallToolResult, error) {
						return sess.CallTool(ctx, &mcpsdk.CallToolParams{
							Name:      origName,
							Arguments: args,
						})
					}

					// c.lockedSession(), not a session captured once at
					// RegisterTools time: Task 8 lets session change out from
					// under a long-lived conn via a reconnect swap, so every
					// call must read the CURRENT session under c.mu.
					result, err := call(c.lockedSession())
					if err != nil && errors.Is(err, mcpsdk.ErrConnectionClosed) {
						// Channel A: the transport itself dropped (SDK taxonomy:
						// ErrClientClosing/ErrServerClosing only — never a ctx
						// cancellation, never a plain JSON-RPC error). Demote the
						// conn and, if reconnect actually swaps in a healed
						// session, retry this same call once against it.
						c.markDegraded(err)
						if newSess, ok := c.reconnect(ctx); ok {
							if m.OnReconnect != nil {
								m.OnReconnect(c.name)
							}
							result, err = call(newSess)
						}
					}
					if err != nil {
						return nil, err
					}
					body := mcpResultToString(result)
					if result != nil && result.IsError {
						// Channel B: the server reported a tool-level error (e.g. an
						// upstream 4xx). Return it through the error path so the tool
						// result reaches the model as an error-typed tool_result and
						// renders red, instead of a green success carrying the error text.
						return body, errors.New("MCP tool reported an error")
					}
					return body, nil
				},
			}); err != nil {
				outcomes = append(outcomes, failRegister(c, reg, added, fmt.Errorf("registering MCP tool %q: %w", td.Name, err)))
				break
			}

			added = append(added, td.Name)
		}
	}
	return outcomes
}

// failRegister rolls back exactly the names c itself added to reg (added)
// before hitting err, demotes c to status "failed", and returns the
// ServerOutcome to report. It must never be passed a name that belongs to a
// different conn: added tracks only names the CALLER appended after its own
// successful reg.Register calls, so a collision with an earlier winner's
// name — which never entered this conn's added list — leaves that name alone.
func failRegister(c *conn, reg *tool.Registry, added []string, err error) ServerOutcome {
	for _, n := range added {
		reg.Remove(n)
	}
	c.status = "failed"
	c.lastErr = err
	c.lastErrAt = time.Now()
	return ServerOutcome{Name: c.name, Stage: "register", Err: err}
}

// Servers returns per-server info including name and namespaced tool names.
// It reads each conn's live state under its mutex rather than a
// construction-time snapshot, so a status change from a concurrent Close()
// or (in a later task) a reconnect swap is always reflected.
func (m *Manager) Servers() []mcpconfig.ServerInfo {
	if m == nil {
		return nil
	}
	out := make([]mcpconfig.ServerInfo, len(m.conns))
	for i := range m.conns {
		c := &m.conns[i]
		c.mu.Lock()
		name := c.name
		status := c.status
		tools := make([]string, len(c.tools))
		for j, td := range c.tools {
			tools[j] = td.Name
		}
		c.mu.Unlock()
		out[i] = mcpconfig.ServerInfo{Name: name, Tools: tools, Status: status}
	}
	return out
}

// Close shuts down all MCP server connections. Each conn's mutex serializes
// Close with a future reconnect swap (Tasks 7-8): closed is set and the
// session reference cleared under the lock, then the session itself is
// closed outside the lock so a slow server shutdown cannot block other
// conns' Close or a concurrent Servers() call.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	for i := range m.conns {
		c := &m.conns[i]
		c.mu.Lock()
		c.closed = true
		sess := c.session
		c.session = nil
		c.mu.Unlock()
		if sess != nil {
			_ = sess.Close()
		}
	}
}

// lockedSession returns c's current session under c.mu, so a concurrent
// reconnect swap or Close is never observed mid-update. The exec closure
// calls this on every invocation — rather than capturing session once, at
// RegisterTools time, the way it did before Task 8 — because a reconnect
// swap can now change session out from under a long-lived conn.
func (c *conn) lockedSession() *mcpsdk.ClientSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.session
}

// markDegraded records a dropped connection (mcpsdk.ErrConnectionClosed) on
// c, demoting its status to "degraded". It does not touch session, dial, or
// backoffUntil: a subsequent call to reconnect (gated by backoffUntil)
// decides whether THIS call may trigger a redial.
func (c *conn) markDegraded(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = "degraded"
	c.lastErr = err
	c.lastErrAt = time.Now()
}

const (
	// reconnectDialTimeout bounds a lazy reconnect's redial (see reconnect),
	// independent of the triggering call's own context — see reconnect's doc
	// comment for why.
	reconnectDialTimeout = 10 * time.Second
	// reconnectBackoff is how long a conn waits before the NEXT lazy
	// reconnect attempt, whether the attempt that set it succeeded or
	// failed. On success, it stops a burst of calls against a
	// freshly-reconnected server from redialing needlessly. On failure, it
	// stops a burst of calls against a still-dead server from each
	// re-paying the full dial timeout.
	reconnectBackoff = 30 * time.Second
)

// reconnect attempts to heal c after one of its calls reported
// mcpsdk.ErrConnectionClosed. It bails out immediately, without dialing, if c
// is already closed, a reconnect is already in flight for c (reconnecting),
// or a previous reconnect attempt's backoff window hasn't elapsed yet —
// backoffUntil's zero value means "try immediately", which is what every conn
// starts with. reconnecting is set under c.mu before the lock is released
// for the dial, so a second concurrent caller sees it immediately upon
// acquiring the lock, before doing any dialing of its own — see the field's
// own doc comment for why this matters.
//
// The dial itself is bounded by its own reconnectDialTimeout, derived from
// context.Background() rather than from ctx: a reconnect heals the conn for
// every future caller, not just the one whose call happened to hit the drop,
// so it deliberately does not inherit one triggering call's deadline or
// cancellation — the discrimination tests establish that ctx expiry and a
// dropped connection are independent signals in the first place, so there is
// no correctness reason to couple them. ctx is accepted only because the exec
// closure naturally has one to hand; it is otherwise unused here. (Flagged
// for review: this is a judgment call, not something the plan spelled out.)
//
// c.mu is released for the dial (which can take up to reconnectDialTimeout)
// so a concurrent Close() is never blocked behind a slow or hung server, then
// re-acquired (via swap) to commit or discard the result. A dial that
// completes after Close() has already run is discarded: its
// freshly-connected session is closed immediately and reconnect reports
// failure, exactly as if the dial had never happened — see swap.
//
// On success, reconnect returns the new session and true; the exec closure
// retries the triggering call once against it. On any failure (bailed out,
// dial/Connect error, or Close() won the race), it returns (nil, false) and
// the exec closure surfaces the original triggering error.
func (c *conn) reconnect(ctx context.Context) (*mcpsdk.ClientSession, bool) {
	c.mu.Lock()
	if c.closed || c.reconnecting || time.Now().Before(c.backoffUntil) {
		c.mu.Unlock()
		return nil, false
	}
	c.reconnecting = true
	dial := c.dial
	client := c.client
	c.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(context.Background(), reconnectDialTimeout)
	defer cancel()

	transport, err := dial(dialCtx)
	if err == nil {
		var newSess *mcpsdk.ClientSession
		newSess, err = client.Connect(dialCtx, transport, nil)
		if err == nil {
			old, committed := c.swap(newSess)
			if !committed {
				// Close() won the race while we were dialing: discard the
				// freshly-connected session rather than leak or publish it.
				_ = newSess.Close()
				return nil, false
			}
			if old != nil {
				_ = old.Close() // displaced session; closed outside the lock per swap's contract
			}
			return newSess, true
		}
	}

	// The redial itself failed (dial or Connect error). Record the failure
	// and apply the same backoff as a success, so a burst of calls against a
	// still-dead server fails fast — returning the ORIGINAL triggering
	// error — instead of each re-paying a fresh dial timeout. Clear
	// reconnecting either way: this attempt is over, so a future call is
	// once again free to try (subject to the backoff just set).
	c.mu.Lock()
	c.reconnecting = false
	if !c.closed {
		c.lastErr = err
		c.lastErrAt = time.Now()
		c.backoffUntil = time.Now().Add(reconnectBackoff)
	}
	c.mu.Unlock()
	return nil, false
}

// swap commits newSess as c's live session, unless c was closed while the
// (unlocked) dial that produced newSess was in flight. On success, it
// returns the displaced (old) session for the caller to Close() OUTSIDE any
// lock, and resets backoffUntil so the next reconnect attempt waits
// reconnectBackoff. On failure (c.closed is true), it returns (nil, false);
// newSess is then the caller's responsibility to discard. Either way, it
// clears reconnecting: the in-flight attempt that called swap is now
// complete, so a future caller (there cannot be a concurrent one right now —
// reconnecting excluded it) is once again free to try.
func (c *conn) swap(newSess *mcpsdk.ClientSession) (old *mcpsdk.ClientSession, committed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reconnecting = false
	if c.closed {
		return nil, false
	}
	old = c.session
	c.session = newSess
	c.status = "connected"
	c.backoffUntil = time.Now().Add(reconnectBackoff)
	return old, true
}

// sanitizeToolName replaces characters invalid in LLM tool names (e.g. hyphens)
// with underscores. MCP servers often use hyphens in tool names, but LLM
// providers only accept [a-zA-Z0-9_].
func sanitizeToolName(name string) string {
	return strings.ReplaceAll(name, "-", "_")
}

// mcpSchemaToParams converts an MCP tool's InputSchema (any) to our
// map[string]any format used by llm.ToolDefinition.Parameters.
func mcpSchemaToParams(schema any) map[string]any {
	emptySchema := func() map[string]any {
		return map[string]any{"type": "object", "properties": map[string]any{}}
	}
	if schema == nil {
		return emptySchema()
	}
	// The MCP SDK returns InputSchema as a map[string]any from JSON unmarshaling.
	if m, ok := schema.(map[string]any); ok {
		return m
	}
	// Fallback: re-marshal and unmarshal.
	b, err := json.Marshal(schema)
	if err != nil {
		return emptySchema()
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return emptySchema()
	}
	return m
}

// mcpResultToString converts an MCP CallToolResult to a string suitable
// for returning as a tool result.
func mcpResultToString(result *mcpsdk.CallToolResult) string {
	if result == nil {
		return ""
	}

	var parts []string
	for _, c := range result.Content {
		switch ct := c.(type) {
		case *mcpsdk.TextContent:
			parts = append(parts, ct.Text)
		default:
			// For non-text content, marshal to JSON as best-effort.
			b, err := json.Marshal(c)
			if err != nil {
				parts = append(parts, fmt.Sprintf("[%T]", c))
			} else {
				parts = append(parts, string(b))
			}
		}
	}

	if result.IsError {
		return "[MCP Error] " + strings.Join(parts, "\n")
	}
	return strings.Join(parts, "\n")
}

// commandTerminateDuration bounds how long a stdio MCP transport's Close waits
// for the server to exit (after closing its stdin) before escalating to SIGTERM.
// Zero means the SDK default (5s). It is the dominant cost of tearing down a
// stdio server that does not exit promptly on stdin-EOF, so tests shrink it to
// avoid paying ~5s per server on cleanup.
var commandTerminateDuration time.Duration

// transportForConfig creates the appropriate MCP transport for a config.
func transportForConfig(cfg mcpconfig.ServerConfig) (mcpsdk.Transport, error) {
	switch cfg.Type {
	case "stdio", "":
		if cfg.Command == "" {
			return nil, errors.New("stdio transport requires a command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...) //nolint:noctx // MCP SDK's CommandTransport.Connect(ctx) owns the server process lifecycle
		// Merge process env with configured env vars.
		if len(cfg.Env) > 0 {
			cmd.Env = mergeEnv(cfg.Env)
		}
		return &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: commandTerminateDuration}, nil

	case "sse":
		if cfg.URL == "" {
			return nil, errors.New("sse transport requires a url")
		}
		t := &mcpsdk.SSEClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	case "http":
		if cfg.URL == "" {
			return nil, errors.New("http transport requires a url")
		}
		t := &mcpsdk.StreamableClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	default:
		return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
	}
}

// mergeEnv creates a combined environment from os.Environ() with extra vars
// merged in. Existing keys are replaced rather than duplicated.
func mergeEnv(extra map[string]string) []string {
	env := os.Environ()

	// Build a set of extra keys for quick lookup.
	overrides := make(map[string]bool, len(extra))
	for k := range extra {
		overrides[k] = true
	}

	// Filter out existing entries that will be overridden.
	filtered := make([]string, 0, len(env)+len(extra))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		if !overrides[key] {
			filtered = append(filtered, e)
		}
	}

	// Append the overrides.
	for k, v := range extra {
		filtered = append(filtered, k+"="+v)
	}
	return filtered
}

// headerRoundTripper wraps an http.RoundTripper to inject headers into requests.
type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (h *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	return h.base.RoundTrip(req)
}

// httpClientWithHeaders returns an *http.Client that injects the given headers.
func httpClientWithHeaders(headers map[string]string) *http.Client {
	return &http.Client{
		Transport: &headerRoundTripper{
			base:    http.DefaultTransport,
			headers: headers,
		},
	}
}
