package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/mcphttp"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/toolname"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// Manager manages connections to external MCP servers.
type Manager struct {
	conns []conn
	// client is the shared *mcpsdk.Client every conn's sessions are connected
	// with, kept so RetryFailed can connect a server whose first connect failed
	// (its conn has no session, so the lazy reconnect in a registered tool's
	// closure cannot reach it).
	client *mcpsdk.Client

	// OnReconnect, if set, is called after a conn's lazy reconnect (Task 8)
	// successfully swaps in a healed session, with that server's configured
	// name. It is nil unless a caller wires it up (initMCP wires it to a
	// recovery diagnostic); most tests never set it, so every call site guards
	// it with a nil check. Exported (unlike conns) because it must be settable
	// from outside the package — initMCP lives in package agent.
	//
	// A first-connect recovery (RetryFailed) reports through its own return
	// value instead: its caller asked for the attempt and needs the tool names
	// it registered, and a dropped-connection notice would misdescribe a server
	// that never had a connection to drop.
	OnReconnect func(name string)
}

type conn struct {
	name string
	cfg  mcpconfig.ServerConfig
	dial func(context.Context) (mcpsdk.Transport, error) // re-dials a fresh transport; set for every conn, even ones whose initial connect failed, so a later reconnect (Task 8) always has something to call
	// client is the shared *mcpsdk.Client used to Connect this conn's
	// sessions — the initial connect, any later reconnect (Task 8), and a
	// retry of a conn whose initial connect failed (RetryFailed).
	client    *mcpsdk.Client
	session   *mcpsdk.ClientSession
	tools     []llm.ToolDefinition // namespaced: servername__toolname
	origNames map[string]string    // sanitized namespaced name → original MCP tool name
	status    string               // "connected" / "degraded" / "failed"
	lastErr   error
	lastErrAt time.Time

	// connectFailed marks a conn whose initial connect failed and that has not
	// connected since, so RetryFailed knows a dial is what heals it. A conn
	// that failRegister demoted stays false: its tools were discovered and
	// refused, and another dial would discover the same ones.
	connectFailed bool

	// mu guards session, status, lastErr, lastErrAt, closed, backoffUntil,
	// connectFailed, and reconnecting against concurrent access between Close(), a
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

	// now, when set, overrides time.Now() for backoff-window comparisons —
	// a test seam only (see newManagerWithClock in the tests). Production conns never set
	// it; clock() falls back to time.Now() so every existing conn literal
	// (including test fixtures that construct conn{} directly) keeps working
	// unchanged.
	now func() time.Time
}

// clock returns c.now() if the test seam is set, else time.Now(). Reads
// under c.mu are unaffected — clock() takes no lock of its own.
func (c *conn) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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
// still stored — and connectFailed records that a dial is what heals it — so
// RetryFailed can connect the server again later in the session.
func failedConn(cfg mcpconfig.ServerConfig, dial func(context.Context) (mcpsdk.Transport, error), err error) conn {
	return conn{
		name:          cfg.Name,
		cfg:           cfg,
		dial:          dial,
		status:        "failed",
		connectFailed: dial != nil,
		lastErr:       err,
		lastErrAt:     time.Now(),
	}
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
// closure (transportForConfigWithEnv). When provided (for testing, or a hermetic
// in-memory fake), dials[i] corresponds to configs[i]. Every conn stores the
// dial closure it used — including a conn whose initial connect failed — so
// a later reconnect (Task 8) can call it again.
//
// conns is preallocated to len(configs) and each goroutine writes exactly one
// index (mgr.conns[i], matching configs[i]), so the result is always in
// config order regardless of which server's connect finishes first — no
// mutex is needed on conns itself, since every index has exactly one writer
// and mgr.conns is only read after wg.Wait() establishes happens-before.
func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, dials []func(context.Context) (mcpsdk.Transport, error), opts ...Option) (*Manager, []ServerOutcome) {
	if len(configs) == 0 {
		return nil, nil
	}

	var o managerOptions
	for _, apply := range opts {
		apply(&o)
	}

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "evener",
		Version: "v1",
	}, nil)

	mgr := &Manager{conns: make([]conn, len(configs)), client: client}
	var wg sync.WaitGroup
	wg.Add(len(configs))
	for i, cfg := range configs {
		dial := productionDial(cfg, o.wrapper)
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

// Option configures NewManager. It is the non-breaking seam for threading a
// sandbox kernel wrapper into the stdio-transport spawn without disturbing the
// manager's existing callers.
type Option func(*managerOptions)

type managerOptions struct {
	wrapper *sandbox.Wrapper
}

// WithSandboxWrapper confines every stdio MCP server this manager spawns to the
// session's sandbox policy (kernel wrap + env floor + fd hygiene). A nil wrapper
// is a no-op, so a non-sandboxed session behaves exactly as before.
func WithSandboxWrapper(w *sandbox.Wrapper) Option {
	return func(o *managerOptions) { o.wrapper = w }
}

// productionDial returns the default dial factory for cfg: build a fresh
// transport via transportForConfigWithEnv on every call. NewManager uses this
// whenever the caller doesn't supply an explicit dials[i] — i.e., real,
// non-test use, and also real reconnects (Task 8), since transportForConfigWithEnv
// builds a brand-new transport each time it runs. When wrapper is non-nil, a
// stdio server's command is kernel-confined to the session sandbox before the
// transport hands it to the SDK.
func productionDial(cfg mcpconfig.ServerConfig, wrapper *sandbox.Wrapper) func(context.Context) (mcpsdk.Transport, error) {
	return productionDialWithEnv(cfg, wrapper, os.Environ)
}

// productionDialWithEnv is productionDial's deterministic construction seam.
// Production always passes os.Environ; package-local tests can supply a fixed
// environment while exercising the same transport and sandbox wiring without
// observing the developer machine's process environment.
func productionDialWithEnv(cfg mcpconfig.ServerConfig, wrapper *sandbox.Wrapper, environ func() []string) func(context.Context) (mcpsdk.Transport, error) {
	return func(ctx context.Context) (mcpsdk.Transport, error) {
		// MCP servers — stdio and remote (sse/http) alike — are trusted
		// infrastructure, so net=off does not sever them (kata 83pm). That holds
		// for config layers the model cannot write and for project servers the
		// launch authorized; a project stdio server a sandboxed delegate admitted
		// without authorization carries DenyNetworkException and is confined
		// ordinarily instead (see confineCommandUnderSandbox). Model-directed
		// egress (web_fetch/web_search) is unaffected and stays refused elsewhere.
		t, err := transportForConfigWithEnv(cfg, environ)
		if err != nil {
			return nil, err
		}
		if wrapper != nil {
			if ct, ok := t.(*mcpsdk.CommandTransport); ok && ct.Command != nil {
				// A sandbox whose host check has not answered starts no server.
				// A server starts once, at session start, so its start keeps
				// rechecking until the check answers or the connect's time
				// bound ends.
				if err := confirmSandboxForServer(ctx, wrapper); err != nil {
					return nil, err
				}
				confineCommandUnderSandbox(ct.Command, cfg, wrapper, environ)
			}
		}
		return t, nil
	}
}

// confirmSandboxForServer reruns the sandbox's host check until it answers or
// ctx ends, pausing between runs so a check that fails fast under load is not
// respawned back to back. It returns nil once the sandbox is confirmed, and the
// refusal otherwise: a definitive one at once, an unconfirmed one when ctx
// ends.
func confirmSandboxForServer(ctx context.Context, wrapper *sandbox.Wrapper) error {
	for {
		err := wrapper.Confirm(ctx)
		var refusal *sandbox.RefusalError
		if err == nil || !errors.As(err, &refusal) || refusal.Kind != sandbox.RefusalUnconfirmed {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(serverSandboxRecheckPause):
		}
	}
}

// serverSandboxRecheckPause spaces a server start's sandbox rechecks.
const serverSandboxRecheckPause = time.Second

// confineCommandUnderSandbox rewrites a stdio MCP server's *exec.Cmd so it runs
// inside the session sandbox: the bwrap invocation is prepended to the argv, the
// launcher's environment is rebuilt with secret hygiene + the sandbox floor, the
// server's configured cfg.Env is delivered to the CONFINED child alone, and
// ExtraFiles is emptied so the server inherits no evener fds beyond stdio.
//
// cmd.Env is the environment of the SANDBOX LAUNCHER, not of the server. bwrap
// and sandbox-exec are dynamically linked and read LD_PRELOAD, LD_LIBRARY_PATH,
// DYLD_INSERT_LIBRARIES and the rest from their own environment before any
// namespace, mount, or Seatbelt policy exists — and an MCP server's cfg.Env can
// come from a config layer a repository writes. It therefore never joins
// cmd.Env: the Confine*WithChildEnv methods apply it on the far side of the
// confinement (bwrap --setenv, or /usr/bin/env after sandbox-exec's --), which
// is the server's own loader environment and nothing else.
//
// The parent environment is scrubbed of credential-named vars (evener's own
// provider key et al.), so an ambient secret never reaches the confined server,
// while an explicitly configured cfg.Env var — even a secret-named one, which is
// deliberate server configuration — still arrives through the child-env channel.
// The floor (ssh-agent handle, cloud creds, TMPDIR, cache redirect) is applied to
// the launcher env last, and the same base is what the child env layers over, so
// the confined server's effective environment is unchanged by where the
// assignments are applied.
//
// Which wrapper method applies is cfg.DenyNetworkException's question, and it is
// the whole trusted-infrastructure decision:
//
//   - The default is ConfineTrustedInfraWithChildEnv: the server's config came
//     from a layer the model cannot write (global, CLI, plugin) or from a project
//     layer the launch authorized, so it is trusted infrastructure (kata 83pm)
//     and keeps egress even at net=off.
//   - A project server a sandboxed delegate admitted WITHOUT launch
//     authorization (mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv) sets
//     DenyNetworkException, and gets the ordinary confinement instead, so net=off
//     severs its network while local tools keep working.
//
// Both methods wrap the argv and set cmd.Dir for Seatbelt the same way, so the
// filesystem confinement, exec floor, fd hygiene, and child-env delivery are
// identical either way. environ must return the parent environment to scrub.
// productionDial passes os.Environ; deterministic construction tests pass a
// fixed slice.
func confineCommandUnderSandbox(cmd *exec.Cmd, cfg mcpconfig.ServerConfig, w *sandbox.Wrapper, environ func() []string) {
	// The floor must judge npm's project config from the directory the server
	// is confined to, not the session's launch dir.
	workingDir := w.Policy().Git.WorktreeRoot
	scrubbed := sandbox.ScrubSecretEnv(environ())
	// The launcher's environment holds only the scrubbed parent environment and
	// the floor. cfg.Env never joins it (see the doc comment above).
	cmd.Env = sandbox.ApplyEnvFloorInDir(scrubbed, w.Policy(), w.SessionTmp(), workingDir)
	// The child's configured environment is cfg.Env as the FLOOR leaves it, so
	// applying it behind the confinement changes nothing the server observes: a
	// floor-dropped credential (the ssh-agent handle) is still dropped from it,
	// and a floor-redirected cache still carries the floor's value.
	var childEnv map[string]string
	if len(cfg.Env) > 0 {
		childEnv = floorChildEnv(cfg.Env, sandbox.ApplyEnvFloorInDir(
			mergeEnvInto(scrubbed, cfg.Env), w.Policy(), w.SessionTmp(), workingDir))
	}
	// Confinement wraps the argv and, for Seatbelt, sets cmd.Dir to the worktree
	// so a macOS MCP server starts in the same directory a Linux (bwrap, via
	// --chdir) one does — sandbox-exec has no chdir flag.
	if cfg.DenyNetworkException {
		w.ConfineWithChildEnv(cmd, workingDir, childEnv)
	} else {
		w.ConfineTrustedInfraWithChildEnv(cmd, workingDir, childEnv)
	}
	cmd.ExtraFiles = nil
}

// floorChildEnv narrows a server's configured environment to the assignments
// the floor actually leaves for those NAMES, taken from env after the floor ran
// over the configured environment. A name the floor dropped is absent from the
// result, and a name the floor redirected carries the floor's value, so
// delivering these assignments inside the sandbox reproduces the environment
// the server received when they were merged into the launcher env instead.
func floorChildEnv(configured map[string]string, env []string) map[string]string {
	if len(configured) == 0 {
		return nil
	}
	out := make(map[string]string, len(configured))
	for name := range configured {
		if value, ok := envValue(env, name); ok {
			out[name] = value
		}
	}
	return out
}

// envValue returns the value env assigns name. It reads the LAST occurrence,
// matching exec's own rule for a duplicated name. An assigned-but-empty value
// is present, not absent.
func envValue(env []string, name string) (string, bool) {
	for _, entry := range slices.Backward(env) {
		if n, value, ok := strings.Cut(entry, "="); ok && n == name {
			return value, true
		}
	}
	return "", false
}

// ToolDefinitions returns namespaced tool definitions from servers whose
// tools are actually registered and callable (status "connected"). A server
// demoted to "failed" during RegisterTools contributes no definitions, so the
// system-prompt-visible tool surface always matches the callable set. Each
// conn's state is read under its mutex: RetryFailed can publish a new session
// and a new tool list for a conn after a failed first connect.
func (m *Manager) ToolDefinitions() []llm.ToolDefinition {
	if m == nil {
		return nil
	}
	var defs []llm.ToolDefinition
	for i := range m.conns {
		c := &m.conns[i]
		c.mu.Lock()
		if c.status != "connected" {
			c.mu.Unlock()
			continue
		}
		defs = append(defs, c.tools...)
		c.mu.Unlock()
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
		c.mu.Lock()
		connected := c.status == "connected"
		c.mu.Unlock()
		if !connected {
			continue
		}
		if outcome := m.registerConn(c, reg); outcome != nil {
			outcomes = append(outcomes, *outcome)
		}
	}
	return outcomes
}

// registerConn registers c's tools into reg through execution closures that
// dispatch against c's CURRENT session. A tool that fails name validation,
// collides, or cannot register demotes c to "failed" and is reported; only the
// names c itself added before that failure are rolled back (see failRegister).
// Registration always runs on the session's own turn driver — at init, or from
// RetryFailed before a turn's first round — so the collision check sees every
// name an earlier server already claimed; the registry itself self-synchronizes.
func (m *Manager) registerConn(c *conn, reg *tool.Registry) *ServerOutcome {
	c.mu.Lock()
	tools := c.tools
	origNames := c.origNames
	c.mu.Unlock()
	var added []string
	for _, td := range tools {
		// Validate the namespaced name (length, charset).
		if err := llm.ValidateToolName(td.Name); err != nil {
			outcome := failRegister(c, reg, added, fmt.Errorf("MCP tool %q: %w", td.Name, err))
			return &outcome
		}

		// Check for collision with existing tools (built-ins, or another
		// server's tools).
		if reg.Get(td.Name) != nil {
			outcome := failRegister(c, reg, added, fmt.Errorf("MCP tool %q collides with existing tool", td.Name))
			return &outcome
		}

		// Look up the original MCP tool name for CallTool.
		origName := origNames[td.Name]

		if err := reg.Register(tool.RegisteredTool{
			Definition: td,
			Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
				call := func(sess *mcpsdk.ClientSession) (*mcpsdk.CallToolResult, error) {
					return sess.CallTool(ctx, &mcpsdk.CallToolParams{
						Name:      origName,
						Arguments: args,
					})
				}

				// c.lockedSession(), not a session captured once at
				// registration time: Task 8 lets session change out from
				// under a long-lived conn via a reconnect swap, so every
				// call must read the CURRENT session under c.mu.
				result, err := call(c.lockedSession())
				if err != nil && isDroppedConn(err) {
					// Channel A: the transport itself dropped — the SDK's
					// ErrConnectionClosed, or the raw transport error a call
					// racing the teardown surfaces first (see isDroppedConn);
					// never a ctx cancellation, never a plain JSON-RPC error.
					// Demote the conn and, if reconnect actually swaps in a
					// healed session, retry this same call once against it.
					c.markDegraded(err)
					if newSess, ok := c.reconnect(ctx); ok {
						if m.OnReconnect != nil {
							m.OnReconnect(c.name)
						}
						result, err = call(newSess)
					}
				}

				if err != nil {
					// Final Channel-A error return: a ctx cancellation, a plain
					// JSON-RPC error, or an ErrConnectionClosed that reconnect
					// either didn't attempt or couldn't heal (in which case
					// markDegraded already stamped lastErr above; recording it
					// again here is harmless — it doesn't touch status).
					c.recordError(err)
					return nil, err
				}
				body := mcpResultToString(result)
				if result != nil && result.IsError {
					// Channel B: the server reported a tool-level error (e.g. an
					// upstream 4xx). Return it through the error path so the tool
					// result reaches the model as an error-typed tool_result and
					// renders red, instead of a green success carrying the error text.
					//
					// Record the actual body text as lastErr — NOT the sentinel
					// error returned below — so Servers() surfaces the real
					// upstream message (e.g. "upstream 400") rather than the
					// generic sentinel, which carries no diagnostic detail. status
					// stays whatever it already was (typically "connected"): the
					// server is reachable and responding, just reporting an
					// application-level error for this call.
					c.recordError(errors.New(body))
					return body, errors.New("MCP tool reported an error")
				}
				return body, nil
			},
		}); err != nil {
			outcome := failRegister(c, reg, added, fmt.Errorf("registering MCP tool %q: %w", td.Name, err))
			return &outcome
		}

		added = append(added, td.Name)
	}
	return nil
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
	c.mu.Lock()
	c.status = "failed"
	c.lastErr = err
	c.lastErrAt = time.Now()
	c.mu.Unlock()
	return ServerOutcome{Name: c.name, Stage: "register", Err: err}
}

// ServerRecovery reports a server that connected on a retry after its first
// connect failed, with the tools that attempt registered into the caller's
// registry. Tools are the namespaced names now callable.
type ServerRecovery struct {
	Name  string
	Tools []string
}

// RetryFailed dials every server whose first connect failed and has not
// connected since, and registers the tools of each one that now answers.
//
// This is the only path that can heal a server whose initial connect failed:
// the lazy reconnect in a registered tool's closure cannot run for a server
// that registered no tools. The caller runs it on its own turn's path, so the
// batch's total dial cost is ONE reconnectDialTimeout, not one per server: every
// eligible conn dials concurrently. That is also what keeps the retry
// converging — a serial batch long enough to outlast reconnectBackoff would find
// the earlier servers' windows already elapsed and pay the whole batch again on
// the next call. ctx ending stops the batch before further dials start. A conn
// that closed in the meantime is skipped, and its freshly-connected session is
// discarded.
//
// A failed attempt sets the same reconnectBackoff window a failed lazy reconnect
// does, so a caller that retries once per turn does not pay a dial timeout on
// every turn against a server that is still down. An attempt cut short by ctx
// ending is not a failed attempt and sets no window: the server was never shown
// to be down.
//
// Registration reuses registerConn, so a recovered server's tools obey the same
// collision rule as at init: a name another server (or a built-in) already owns
// fails that server's registration and is reported rather than replacing the
// existing tool. Registration therefore stays serial, in config order, and only
// the dialing parallelizes. The manager knows no tool-name policy: a caller that
// applied one at startup applies it to ServerRecovery.Tools, whose names are
// already registered by the time this returns.
func (m *Manager) RetryFailed(ctx context.Context, reg *tool.Registry) ([]ServerRecovery, []ServerOutcome) {
	if m == nil {
		return nil, nil
	}
	// Dial phase. Each goroutine writes its own index and wg.Wait establishes
	// happens-before, so these slices need no mutex (the same shape NewManager
	// uses for conns).
	attempted := make([]bool, len(m.conns))
	errs := make([]error, len(m.conns))
	var wg sync.WaitGroup
	for i := range m.conns {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			attempted[i], errs[i] = m.conns[i].connectIfRetryable(ctx, m.client)
		}(i)
	}
	wg.Wait()

	// Register phase: serial and in config order, because registerConn enforces
	// the collision rule against the shared registry.
	var recovered []ServerRecovery
	var outcomes []ServerOutcome
	for i := range m.conns {
		if !attempted[i] {
			continue
		}
		c := &m.conns[i]
		if err := errs[i]; err != nil {
			outcomes = append(outcomes, ServerOutcome{Name: c.name, Stage: "connect", Err: err})
			continue
		}
		if outcome := m.registerConn(c, reg); outcome != nil {
			outcomes = append(outcomes, *outcome)
			continue
		}
		recovered = append(recovered, ServerRecovery{Name: c.name, Tools: c.toolNames()})
	}
	return recovered, outcomes
}

// toolNames returns c's namespaced tool names.
func (c *conn) toolNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, len(c.tools))
	for i, td := range c.tools {
		names[i] = td.Name
	}
	return names
}

// connectIfRetryable dials c with the manager's shared client when c is a conn
// whose first connect failed and that has not connected since, replacing it
// with the freshly-connected conn on success. attempted is false when c has
// nothing to retry — it connected, a retry is already in flight, it was closed,
// or its last failed attempt's backoff window has not elapsed. A dial failure is
// recorded on c (so Servers() keeps naming the most recent cause), starts a
// backoff window, and is returned.
func (c *conn) connectIfRetryable(ctx context.Context, client *mcpsdk.Client) (attempted bool, err error) {
	c.mu.Lock()
	if !c.connectFailed || c.closed || c.reconnecting || c.clock().Before(c.backoffUntil) {
		c.mu.Unlock()
		return false, nil
	}
	c.reconnecting = true
	dial := c.dial
	cfg := c.cfg
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	dialCtx, cancel := context.WithTimeout(ctx, reconnectDialTimeout)
	defer cancel()

	connected := connectOne(dialCtx, client, cfg, dial)
	if connected.status != "connected" {
		c.mu.Lock()
		// A cancellation is not evidence the server is down, so it neither
		// rewrites the recorded cause nor starts a backoff window.
		if !c.closed && ctx.Err() == nil {
			c.lastErr = connected.lastErr
			c.lastErrAt = time.Now()
			c.backoffUntil = c.clock().Add(reconnectBackoff)
		}
		c.mu.Unlock()
		return true, connected.lastErr
	}
	c.commit(client, connected.session, connected.tools, connected.origNames)
	return true, nil
}

// commit installs the client, the freshly-connected session, and the tools
// discovered on it, unless c was closed while the dial was in flight — then the
// new session is closed instead of leaked, and c stays failed.
//
// The client is installed with the session because it is what a LATER reconnect
// needs: a conn built by failedConn has no client, so without this the lazy
// reconnect after a recovered connection drops would call Connect on a nil
// client.
func (c *conn) commit(client *mcpsdk.Client, session *mcpsdk.ClientSession, tools []llm.ToolDefinition, origNames map[string]string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if session != nil {
			_ = session.Close()
		}
		return
	}
	old := c.session
	c.client = client
	c.session = session
	c.tools = tools
	c.origNames = origNames
	c.status = "connected"
	c.connectFailed = false
	c.lastErr = nil
	c.backoffUntil = c.clock().Add(reconnectBackoff)
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// Servers returns per-server info including name, namespaced tool names,
// live status, and the last error of any kind (including a Channel-B
// application error that does not change status). It reads each conn's live
// state under its mutex rather than a construction-time snapshot, so a
// status or error change from a concurrent Close(), reconnect swap (Task 8),
// or exec closure (Task 13) is always reflected.
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
		lastErr := c.lastErr
		tools := make([]string, len(c.tools))
		for j, td := range c.tools {
			tools[j] = td.Name
		}
		c.mu.Unlock()
		out[i] = mcpconfig.ServerInfo{Name: name, Tools: tools, Status: status, Error: errString(lastErr)}
	}
	return out
}

// errString returns err's message, or "" if err is nil — a nil-safe
// conversion so a healthy server (lastErr never set) reports Error=="".
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
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

// isDroppedConn reports whether err means the transport itself dropped and a
// lazy reconnect should be attempted. The SDK normally surfaces a dropped
// session as mcpsdk.ErrConnectionClosed, but a call that races the transport
// teardown can surface the underlying transport error first — before the SDK
// marks the session closed and wraps it. The two ends of the transport fail
// differently: the WRITE side yields io.ErrClosedPipe (in-memory/stdio pipe),
// os.ErrClosed (a real subprocess pipe), or net.ErrClosed (an sse/http
// socket); the READ side yields io.EOF (the stream ended cleanly after the
// peer hung up) or io.ErrUnexpectedEOF (it ended mid-message). All of them
// mean the same thing — the peer is gone, so redial. A live CallTool never
// returns EOF, so an EOF here is unambiguously a drop. A ctx cancellation or a
// plain JSON-RPC application error is none of these, so it is not treated as a
// drop (see the discrimination cases in cov_reconnect_test.go — a handler's
// application error comes back as a result with IsError set, with a nil call
// error, so it never reaches this predicate at all).
func isDroppedConn(err error) bool {
	// A ctx cancellation is never a drop, even when the SDK joins it with an
	// underlying transport error: the mcp transport's cancellation path returns
	// errors.Join(ctx.Err(), ...), so that join can carry an io.EOF the checks
	// below would otherwise match. Excluding it first keeps a cancelled call
	// from being reclassified as a drop.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return errors.Is(err, mcpsdk.ErrConnectionClosed) ||
		errors.Is(err, io.ErrClosedPipe) ||
		errors.Is(err, os.ErrClosed) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// markDegraded records a dropped connection (see isDroppedConn) on c, demoting
// its status to "degraded". It does not touch session, dial, or
// backoffUntil: a subsequent call to reconnect (gated by backoffUntil)
// decides whether THIS call may trigger a redial.
func (c *conn) markDegraded(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status = "degraded"
	c.lastErr = err
	c.lastErrAt = time.Now()
}

// recordError records the last error observed on c — a Channel-A transport
// error or a Channel-B application error (the server itself reported a tool
// result as an error) — WITHOUT changing status. Unlike markDegraded, a
// Channel-B failure means the server is reachable and responding; it is only
// this call's result that failed, so status must stay whatever it already
// was (typically "connected"). Calling recordError after markDegraded already
// ran for the same failed call is harmless: it just overwrites lastErr with
// the same (or, after a failed retry, a newer) error.
func (c *conn) recordError(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
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
func (c *conn) reconnect(_ context.Context) (*mcpsdk.ClientSession, bool) {
	c.mu.Lock()
	if c.closed || c.reconnecting || c.clock().Before(c.backoffUntil) {
		c.mu.Unlock()
		return nil, false
	}
	c.reconnecting = true
	dial := c.dial
	client := c.client
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

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
	// error — instead of each re-paying a fresh dial timeout. The deferred
	// clear above handles reconnecting for this (and every other) exit path.
	c.mu.Lock()
	if !c.closed {
		c.lastErr = err
		c.lastErrAt = c.clock()
		c.backoffUntil = c.clock().Add(reconnectBackoff)
	}
	c.mu.Unlock()
	return nil, false
}

// swap commits newSess as c's live session, unless c was closed while the
// (unlocked) dial that produced newSess was in flight. On success, it
// returns the displaced (old) session for the caller to Close() OUTSIDE any
// lock, and resets backoffUntil so the next reconnect attempt waits
// reconnectBackoff. On failure (c.closed is true), it returns (nil, false);
// newSess is then the caller's responsibility to discard. swap no longer
// touches reconnecting — reconnect's own deferred clear owns that for every
// exit path, including swap's.
func (c *conn) swap(newSess *mcpsdk.ClientSession) (old *mcpsdk.ClientSession, committed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, false
	}
	old = c.session
	c.session = newSess
	c.status = "connected"
	c.connectFailed = false
	c.backoffUntil = time.Now().Add(reconnectBackoff)
	return old, true
}

func sanitizeToolName(name string) string {
	return toolname.SanitizeMCPToolName(name)
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

// transportForConfigWithEnv creates the appropriate MCP transport for a config.
// Production passes os.Environ; callers that need hermetic transport assembly
// can supply a fixed base environment.
func transportForConfigWithEnv(cfg mcpconfig.ServerConfig, environ func() []string) (mcpsdk.Transport, error) {
	switch cfg.Type {
	case "stdio", "":
		if cfg.Command == "" {
			return nil, errors.New("stdio transport requires a command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...) //nolint:noctx // MCP SDK's CommandTransport.Connect(ctx) owns the server process lifecycle
		// Always materialize the supplied parent environment. Besides keeping a
		// deterministic construction seam, this makes an empty configured map
		// mean "inherit this supplied environment", never the caller's ambient one.
		cmd.Env = mergeEnvInto(environ(), cfg.Env)
		return &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: commandTerminateDuration}, nil

	case "sse", "http":
		// The http and sse transports come from the builder the reachability
		// probe uses too (#2396): one policy for which SDK transport a
		// configured type means, its endpoint, header injection and the empty-URL
		// refusal. A nil client keeps the SDK's own, as the manager has always
		// done; the probe passes the client it pools.
		return mcphttp.BuildTransport(cfg.Type, cfg.URL, cfg.Headers, nil)

	default:
		return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
	}
}

// mergeEnvInto layers extra over the base env slice: base entries whose key is
// overridden by extra are dropped, then every extra pair is appended. It never
// mutates base.
func mergeEnvInto(base []string, extra map[string]string) []string {
	overrides := make(map[string]bool, len(extra))
	for k := range extra {
		overrides[k] = true
	}

	filtered := make([]string, 0, len(base)+len(extra))
	for _, e := range base {
		key, _, _ := strings.Cut(e, "=")
		if !overrides[key] {
			filtered = append(filtered, e)
		}
	}

	for k, v := range extra {
		filtered = append(filtered, k+"="+v)
	}
	return filtered
}
