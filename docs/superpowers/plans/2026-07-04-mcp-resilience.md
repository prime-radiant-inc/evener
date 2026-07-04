# MCP Resilience Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One misconfigured or dead MCP server can no longer kill a session — startup is non-fatal, parallel, and warned; server-reported tool errors render as errors; a call-driven lazy reconnect heals a dropped connection; and every surface (wire, TUI, settings probe) tells the honest truth about MCP health.

**Architecture:** `mcp.Manager` becomes a two-stage, per-server assembly: `NewManager` connects every server in parallel under per-server timeouts and records per-server connect outcomes; `RegisterTools` registers per-server and rolls back only a failed server's own tools; `initMCP` merges the outcomes into deferred `pendingMCPWarnings` and rebuilds `s.mcpTools` from the actually-registered set. A per-conn mutex + closed flag + redial transport factory make a `Manager` reconnect a single conn (only on `ErrConnectionClosed`) on the next call, closing the displaced session. Failures ride the existing deferred-diagnostics path with a new `SourceMCP` classifier; `Status`/`Error` thread the enumerated carrier chain to the TUI and (via a rebuilt `mcpprobe`) the settings pane.

**Tech Stack:** Go (`agent/internal/mcp`, `agent/mcpconfig`, `agent/plugin`, `agent/internal/diagnostic`, root-module `server` + `cmd/serf` + `cmd/serf-hub` + `cmd/serf-tui` + `appwire` + root `internal/diagnostic`, new `agent/mcpprobe`), `github.com/modelcontextprotocol/go-sdk` v1.6.1 (agent module), `llm` module for the severity pin, jstest (JSDOM) for the web error-render assertion.

**Working rules for every task:**
- Run Go tests per-module: `cd <module> && go test ./<pkg>/ -run <Name> -count=1` (modules: repo root `.`, `agent`, `llm`). Each task ends green on the touched module's lint slice (`cd <module> && golangci-lint run ./<pkg>/...`) and its `go test`.
- Full gates (Task 21 only): `make test-short`, `make test-race`, `make lint`, and `sh cmd/serf-hub/jstest/run-all.sh` from repo root.
- jstest: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh` (or `node test-<name>.js` for one file).
- Commit after every green task with the exact message given. NEVER use `git add -A`; add the named files only.
- Read the anchor code before editing it — the design turns on details at specific lines.

**Shared types & names (used consistently across tasks — introduce once, reuse verbatim):**
- `mcp.ServerOutcome struct { Name string; Stage string; Err error }` — `Stage` ∈ `"connect"` | `"register"`. Returned by `NewManager` (connect) and `RegisterTools` (register); merged by `initMCP` into warnings.
- `conn` gains: `cfg mcpconfig.ServerConfig`, `dial func(context.Context) (mcpsdk.Transport, error)` (redial factory), `mu sync.Mutex`, `status string` (`"connected"`/`"degraded"`/`"failed"`), `lastErr error`, `lastErrAt time.Time`, `closed bool`, `backoffUntil time.Time` (zero value = redial allowed now). A conn is retained for every configured server, healthy or not; a failed conn has `session==nil`, `status=="failed"`.
- `mcpconfig.ServerInfo` gains `Status string` and `Error string`.
- Status vocabulary everywhere: `connected` / `degraded` / `failed`.
- Diagnostic: `SourceMCP Source = "mcp"` and `mcpFailure()` in **both** `agent/internal/diagnostic` and root `internal/diagnostic`.
- `Session.pendingMCPWarnings []events.WarningData`.
- New package `agent/mcpprobe`: `func Probe(ctx context.Context, configs []mcpconfig.ServerConfig) []Result` with `Result struct { Name, Transport, Status, Error string }`.

---

## Task 1: Severity fix — Channel-B server errors render as errors (standalone bug fix)

Shippable independently of the manager rework. Channel B (`CallToolResult.IsError==true`, e.g. a Linear 400) is currently folded into a plain `"[MCP Error] …"` string with `IsError:false`, so `tool.Registry.ExecuteCall` hits its success fallback (`agent/internal/tool/registry.go:532-533`) and the result renders green. Make the MCP tool executor return the error body **through the error path** so `ExecResult.IsError` is true, the model gets an error-typed `tool_result`, and the web renders the error marker.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (the `RegisterTools` exec closure, ~line 137-148)
- Test (Go, agent): `agent/internal/mcp/cov_channelb_test.go` (new)
- Test (jstest): `cmd/serf-hub/jstest/test-mcp-error-marker.js` (new)

- [ ] **Step 1: Failing Go test — a Channel-B result is an error-typed `ExecResult`**

Create `agent/internal/mcp/cov_channelb_test.go` (`package mcp`; copy the import set + in-memory-server setup from `manager_test.go`'s `TestMCPManager_InMemory`). The server's tool returns `&mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom: upstream 400"}}}`:

```go
func TestMCPManager_ChannelBError_IsErrorTypedResult(t *testing.T) {
	ctx := context.Background()
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "s", Version: "v1"}, nil)
	server.AddTool(&mcpsdk.Tool{
		Name:        "fail",
		Description: "Always reports a server-side error",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	}, func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		return &mcpsdk.CallToolResult{
			IsError: true,
			Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "boom: upstream 400"}},
		}, nil
	})
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	mgr, err := NewManager(ctx, []mcpconfig.ServerConfig{{Name: "s", Type: "stdio"}}, []mcpsdk.Transport{ct})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer mgr.Close()
	reg := tool.NewRegistry()
	if err := mgr.RegisterTools(reg); err != nil {
		t.Fatalf("RegisterTools: %v", err)
	}
	res := reg.ExecuteCall(ctx, &agenttest.FakeEnv{WorkDir: t.TempDir()}, llm.ToolCallData{
		ID: "c", Name: "s__fail", Arguments: json.RawMessage(`{}`),
	})
	if !res.IsError {
		t.Fatalf("Channel-B error rendered as success: IsError=false, output=%q", res.Output)
	}
	if !strings.Contains(res.Output, "boom: upstream 400") {
		t.Errorf("error body missing from result: %q", res.Output)
	}
}
```

- [ ] **Step 2: Run — fails** (`res.IsError` is false today)

Run: `cd agent && go test ./internal/mcp/ -run TestMCPManager_ChannelBError_IsErrorTypedResult -count=1`
Expected: FAIL — `Channel-B error rendered as success: IsError=false`.

- [ ] **Step 3: Implement — return Channel B through the error path**

In `agent/internal/mcp/manager.go`, the exec closure currently ends:

```go
			if err != nil {
				return nil, err
			}
			return mcpResultToString(result), nil
```

Replace the trailing return with a Channel-B discrimination (keep Channel A — transport `err` — untouched):

```go
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
```

`tool.Registry.ExecuteCall` builds `full` from the returned value when the error's value is non-empty (`registry.go:491-498`), so the model sees the server's error body (already `[MCP Error] …`-prefixed by `mcpResultToString`), not the sentinel. `errors` is already imported in manager.go.

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ -run TestMCPManager_ChannelBError_IsErrorTypedResult -count=1`
Expected: PASS

- [ ] **Step 5: Failing jstest — an MCP-namespaced tool result renders the error marker**

Create `cmd/serf-hub/jstest/test-mcp-error-marker.js` (copy the JSDOM bootstrap + `SerfRendererInternal` load from `test-appwire-replay-tool-pairs.js`). Drive the tool renderer for an MCP-namespaced name (`linear__search_issues`) with a `TOOL_CALL_END` carrying `error` (the wire shape `session_tools.go:445-448` produces when `IsError`), and assert the default renderer classifies it as an error:

```js
const { toolRendererFor } = window.SerfRendererInternal || require... // use the same accessor the sibling tests use
const r = toolRendererFor("linear__search_issues");           // MCP names fall through to __default__
const marker = r.result({ error: "[MCP Error] upstream 400" }, "");
assert.strictEqual(marker, "error", "MCP-namespaced tool with error must render the error marker");
const ok = r.result({}, "done");
assert.strictEqual(ok, "ok", "MCP-namespaced tool without error still renders ok");
```

(If `toolRendererFor` is not already exported to `SerfRendererInternal`, follow the sibling test's pattern for reaching it — match whatever `test-appwire-replay-tool-pairs.js` uses; do not add a new export unless the sibling tests require one.)

- [ ] **Step 6: Run — pass** (this pins existing behavior against regression; the default renderer already keys on `data.error`)

Run: `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-mcp-error-marker.js`
Expected: PASS (exit 0). If the accessor differs, fix the harness lines until it passes — the assertion contract is fixed.

- [ ] **Step 7: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_channelb_test.go cmd/serf-hub/jstest/test-mcp-error-marker.js`
- `git commit -m "fix(mcp): surface server-reported tool errors as errors (Channel B)"`

_~55 loc._

---

## Task 2: Two-stage non-fatal assembly — `NewManager` records per-server connect outcomes

`NewManager` no longer aborts the whole batch on one server's transport-build/connect/discover failure. It builds a `conn` for every server (failed ones with `status:"failed"`, `session:nil`) and returns `[]ServerOutcome` for the connect stage. The three `NewManager` contract tests flip here. `initMCP` keeps fatal semantics via a temporary scaffold so the two `InitMCP` agent tests stay green until Task 4.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (`conn` struct, `NewManager`, `Close`, `Servers`)
- Modify: `agent/session_init.go` (`initMCP` — scaffold)
- Modify (flip): `agent/internal/mcp/cov_intg_manager_test.go` (the 3 NewManager tests)
- Test: `agent/internal/mcp/cov_twostage_test.go` (new — healthy-survive-sibling spy)

- [ ] **Step 1: Flip the three NewManager contract tests + add the sibling-survive test**

In `cov_intg_manager_test.go`, rewrite `TestIntgMCP_NewManager_TransportBuildError`, `TestIntgMCP_NewManager_ConnectError`, `TestIntgMCP_NewManager_ListToolsError` so each asserts the manager is **returned** and the offending server is recorded as a connect outcome — no top-level error, no nil manager. E.g. for ConnectError:

```go
func TestIntgMCP_NewManager_ConnectError(t *testing.T) {
	sentinel := errors.New("intg: connect refused")
	mgr, outcomes := NewManager(context.Background(),
		[]mcpconfig.ServerConfig{{Name: "unreachable", Type: "stdio"}},
		[]mcpsdk.Transport{intg_failConnectTransport{err: sentinel}})
	if mgr == nil {
		t.Fatal("expected a non-nil manager even when a server fails to connect")
	}
	defer mgr.Close()
	if len(outcomes) != 1 || outcomes[0].Name != "unreachable" || outcomes[0].Stage != "connect" {
		t.Fatalf("want one connect outcome for %q, got %+v", "unreachable", outcomes)
	}
	if !errors.Is(outcomes[0].Err, sentinel) {
		t.Errorf("outcome error %v does not wrap the sentinel", outcomes[0].Err)
	}
	if got := mgr.Servers(); len(got) != 1 || got[0].Status != "failed" {
		t.Errorf("failed server must still appear in Servers() as failed, got %+v", got)
	}
}
```

Do the same for TransportBuildError (`Stage:"connect"`, error mentions the missing command) and ListToolsError (`Stage:"connect"`; the connected-but-list-failed server is `failed`). Add the sibling-survive spy in `cov_twostage_test.go`: two servers via in-memory transports where server A's `tools/list` is rejected (reuse the `AddReceivingMiddleware` pattern from ListToolsError) and server B is healthy; assert B's tool is discovered and registrable while A is `failed`.

- [ ] **Step 2: Run — fails to compile** (`NewManager` returns two values, not `(*Manager, error)`)

Run: `cd agent && go test ./internal/mcp/ -run 'TestIntgMCP_NewManager|Sibling' -count=1`
Expected: FAIL to compile — `NewManager` arity / undefined `ServerOutcome` / `ServerInfo.Status`.

- [ ] **Step 3: Implement the two-stage `NewManager`**

Add near the top of `manager.go`:

```go
// ServerOutcome reports a per-server failure at one assembly stage. Stage is
// "connect" (transport build, connect handshake, or tools/list) or "register".
type ServerOutcome struct {
	Name  string
	Stage string
	Err   error
}
```

Extend `conn` with `cfg mcpconfig.ServerConfig`, `status string`, `lastErr error`, `lastErrAt time.Time` (the mutex/closed/dial/backoff fields land in later tasks — add only what this task uses). Change `NewManager` to `func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, transports []mcpsdk.Transport) (*Manager, []ServerOutcome)`. For each config, on transport-build/connect/list failure, append a `conn{name, cfg, status: "failed", lastErr: err, lastErrAt: now}` and a `ServerOutcome{Name, Stage: "connect", Err: err}`, and **continue** (never `mgr.Close()` mid-loop). On success append the healthy `conn{..., status: "connected"}`. Return `(mgr, outcomes)`; `len(configs)==0` still returns `(nil, nil)`. `Close` must skip conns with `session==nil`. `Servers()` sets `Status: c.status` and (empty for now) `Error` on each `ServerInfo`.

- [ ] **Step 4: Scaffold `initMCP` to stay fatal (removed in Task 4)**

In `agent/session_init.go`, adapt the changed call site and preserve current behavior:

```go
	mgr, connectOutcomes := mcp.NewManager(ctx, configs, nil)
	if len(connectOutcomes) > 0 {
		mgr.Close()
		return fmt.Errorf("MCP server %q connect: %w", connectOutcomes[0].Name, connectOutcomes[0].Err)
	}
	if err := mgr.RegisterTools(s.reg); err != nil {
		mgr.Close()
		return err
	}
```

This keeps `TestIntg_InitMCP_ConnectError` fatal (green) until Task 4 flips it.

- [ ] **Step 5: Run — pass**

Run: `cd agent && go test ./internal/mcp/ ./ -run 'TestIntgMCP_NewManager|Sibling|TestIntg_InitMCP' -count=1`
Expected: PASS (both InitMCP tests still fatal; NewManager tests flipped).

- [ ] **Step 6: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/session_init.go agent/internal/mcp/cov_intg_manager_test.go agent/internal/mcp/cov_twostage_test.go`
- `git commit -m "refactor(mcp): non-fatal per-server connect outcomes in NewManager"`

_~75 loc._

---

## Task 3: `RegisterTools` per-server outcomes + rollback-spares-the-collision-winner + definitions rebuilt from the registered set

`RegisterTools` continues past a failing server, marks it `failed`, and rolls back **only the names that server itself successfully registered** — never the colliding name (which belongs to the winner). `ToolDefinitions()` returns tools only from servers that registered, so the system-prompt surface matches the callable set. The two RegisterTools contract tests flip.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (`RegisterTools`, `ToolDefinitions`)
- Modify: `agent/session_init.go` (scaffold adapts to `[]ServerOutcome`)
- Modify (flip): `agent/internal/mcp/manager_test.go` (`TestMCPManager_BuiltinCollision`, `TestMCPManager_ToolNameTooLong`)
- Test: `agent/internal/mcp/cov_rollback_test.go` (new — I3 + I1)

- [ ] **Step 1: Flip the two RegisterTools tests + add rollback/definitions tests**

In `manager_test.go`, change both tests: `RegisterTools` now returns `[]ServerOutcome`; assert the outcome names the server with `Stage:"register"` and the manager keeps going. For collision:

```go
	outcomes := mgr.RegisterTools(reg)
	if len(outcomes) != 1 || outcomes[0].Stage != "register" {
		t.Fatalf("want one register outcome, got %+v", outcomes)
	}
```

Add `cov_rollback_test.go`:
- **Rollback spares the winner (I3):** two servers whose sanitized names collide — server `foo_bar` (registers `foo_bar__x` then `foo_bar__y`) and server `foo-bar` (would produce `foo_bar__y` too). Config order puts `foo_bar` first. Assert after `RegisterTools`: `reg.Get("foo_bar__x") != nil` and `reg.Get("foo_bar__y") != nil` (the winner's survive), the loser is `failed`, and the loser contributed **no** surviving tool. Construct the collision so the loser registers one non-colliding tool first, then hits the collision, and assert that loser's own first tool was rolled back while the winner's colliding tool remains.
- **Definitions rebuilt from registered set (I1):** the `ToolNameTooLong` server (whose one tool fails validation) contributes **zero** entries to `mgr.ToolDefinitions()`, while a healthy sibling's tool is present.

- [ ] **Step 2: Run — fails to compile** (`RegisterTools` returns a value)

Run: `cd agent && go test ./internal/mcp/ -run 'BuiltinCollision|ToolNameTooLong|Rollback|DefinitionsRebuilt' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

Change `RegisterTools` to `func (m *Manager) RegisterTools(reg *tool.Registry) []ServerOutcome`. Iterate conns with `status=="connected"` only. Per conn, track `added []string`; on the first validation/collision failure for that conn, roll back with `for _, n := range added { reg.Remove(n) }`, set `conn.status="failed"`, `conn.lastErr=err`, append `ServerOutcome{Name, Stage:"register", Err:err}`, and move to the next conn. Only append to `added` after a successful `reg.Register`. `ToolDefinitions()` returns tools only from conns with `status=="connected"` (a conn demoted to `failed` during register is skipped). Update the `initMCP` scaffold:

```go
	regOutcomes := mgr.RegisterTools(s.reg)
	if len(connectOutcomes)+len(regOutcomes) > 0 {
		mgr.Close()
		all := append(connectOutcomes, regOutcomes...)
		return fmt.Errorf("MCP server %q %s: %w", all[0].Name, all[0].Stage, all[0].Err)
	}
```

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ ./ -run 'BuiltinCollision|ToolNameTooLong|Rollback|DefinitionsRebuilt|TestIntg_InitMCP' -count=1`
Expected: PASS (InitMCP tests still fatal).

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/session_init.go agent/internal/mcp/manager_test.go agent/internal/mcp/cov_rollback_test.go`
- `git commit -m "refactor(mcp): per-server register outcomes with winner-sparing rollback"`

_~75 loc._

---

## Task 4: `initMCP` merges outcomes non-fatally, collects `pendingMCPWarnings`, rebuilds `s.mcpTools`

Remove the fatal scaffold. A session constructs even when every MCP server fails; connect+register outcomes become `pendingMCPWarnings` (unflushed until Task 9). `s.mcpTools` is rebuilt from `ToolDefinitions()` (registered set). The two `InitMCP` contract tests flip; `TestIntg_InitMCP_DiscoverError` stays fatal (config parse is not touched here).

**Files:**
- Modify: `agent/session.go` (add `pendingMCPWarnings []events.WarningData`)
- Modify: `agent/session_init.go` (`initMCP`)
- Modify (flip): `agent/cov_intg_mcp_test.go` (`TestIntg_InitMCP_ConnectError`, `TestIntg_InitMCP_RegisterToolsError`)

- [ ] **Step 1: Flip the two InitMCP tests (white-box — `package agent`)**

Rewrite both so `NewSession` **succeeds**, the failed server's tool is absent, and a warning was collected. For ConnectError:

```go
func TestIntg_InitMCP_ConnectError(t *testing.T) {
	t.Parallel()
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
	defer sess.Close()
	if len(sess.pendingMCPWarnings) == 0 {
		t.Fatal("expected a pending MCP warning for the dead server")
	}
	if sess.reg.Get("deadsvc__echo") != nil {
		t.Error("a failed server must contribute no callable tool")
	}
}
```

Rewrite `RegisterToolsError` the same way (long server name → the tool is dropped, `NewSession` succeeds, a register warning is collected). Leave `TestIntg_InitMCP_DiscoverError` unchanged.

- [ ] **Step 2: Run — fails** (`NewSession` still returns the scaffolded error)

Run: `cd agent && go test ./ -run 'TestIntg_InitMCP_(Connect|RegisterTools|Discover)Error' -count=1`
Expected: FAIL — ConnectError/RegisterToolsError still fatal; DiscoverError passes.

- [ ] **Step 3: Implement the non-fatal merge**

Add `pendingMCPWarnings []events.WarningData` to `Session` (next to `pendingHookWarnings`, `session.go`). Replace the scaffold in `initMCP`:

```go
	mgr, connectOutcomes := mcp.NewManager(ctx, configs, nil)
	regOutcomes := mgr.RegisterTools(s.reg)
	for _, o := range append(connectOutcomes, regOutcomes...) {
		s.pendingMCPWarnings = append(s.pendingMCPWarnings, events.WarningData{
			Source:  string(diagnostic.SourceMCP),
			Title:   "MCP server unavailable",
			Message: fmt.Sprintf("MCP server %q failed to %s: %v", o.Name, o.Stage, o.Err),
		})
	}
	s.mcpMgr = mgr
	s.mcpTools = mgr.ToolDefinitions()
	return nil
```

(`diagnostic` = `agent/internal/diagnostic`; `SourceMCP` is added in Task 8 — until then use the literal `"mcp"` and switch to the constant in Task 8, or land Task 8 first. Prefer the constant: sequence Task 8 before this if executing strictly; the plan orders Task 8 in the warning-path block. To keep this task self-contained and green, use `Source: "mcp"` here and let Task 8's classifier recognize it.)

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./ -run 'TestIntg_InitMCP' -count=1`
Expected: PASS (Discover still fatal; Connect/RegisterTools now survive).

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./... && go test ./ -run 'TestIntg_InitMCP|MCP' -count=1`
Commit:
- `git add agent/session.go agent/session_init.go agent/cov_intg_mcp_test.go`
- `git commit -m "feat(mcp): non-fatal MCP init — a bad server no longer kills the session"`

_~45 loc._

---

## Task 5: Parallel connects, per-server 10s timeouts, config-order determinism

`NewManager` connects all servers concurrently, each under its own 10s timeout derived from the parent (`initMCP`'s 30s), then re-sorts results to config order before they become conns — so tool registration order and `Servers()` order are deterministic regardless of connect completion order.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (`NewManager` loop → parallel)
- Test: `agent/internal/mcp/cov_parallel_test.go` (new)

- [ ] **Step 1: Failing tests — determinism + concurrency**

`cov_parallel_test.go`:
- **Config-order determinism:** three in-memory servers whose connects complete out of order (wrap two transports so their `Connect` sleeps in reverse config order); assert `mgr.Servers()` names are in config order and `ToolDefinitions()` groups by config order.
- **Parallel bound:** two transports that each block ~400ms in `Connect`; assert wall-clock `NewManager` < 700ms (serial would be ~800ms). Use a small margin; keep the sleeps modest to stay CI-robust.

- [ ] **Step 2: Run — fails** (serial today; order depends on completion, timing exceeds bound)

Run: `cd agent && go test ./internal/mcp/ -run 'ConfigOrder|ParallelBound' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Rewrite the `NewManager` body: spawn one goroutine per config, each doing transport-build → `client.Connect(perServerCtx, …)` → `ListTools`, where `perServerCtx, cancel := context.WithTimeout(ctx, 10*time.Second)`. Collect into a `results []connectResult` slice **indexed by config position** (each goroutine writes `results[i]`), `sync.WaitGroup.Wait()`, then iterate `results` in index order building conns + outcomes. This preserves config order deterministically. Keep `client` construction shared (it is safe to `Connect` concurrently — one `mcpsdk.Client` dials many sessions).

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ -run 'ConfigOrder|ParallelBound|TestMCPManager|Sibling|Rollback' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -race -run 'ConfigOrder|ParallelBound' -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_parallel_test.go`
- `git commit -m "perf(mcp): parallel per-server connects with 10s timeouts and config-order determinism"`

_~65 loc._

---

## Task 6: Per-conn mutex + closed flag + live-state `Servers()` + serialized `Close`

Add the per-conn concurrency primitives the reconnect swap needs: `conn.mu`, `conn.closed`. `Close()` takes each conn's mutex and sets `closed`, so it serializes with a future swap. `Servers()` reads `status`/`lastErr` under the mutex (live state, not a startup snapshot). No behavior flip yet — this is the mutex scaffold that Tasks 7-8 build on.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (`conn` fields, `Close`, `Servers`)
- Test: `agent/internal/mcp/cov_convmutex_test.go` (new — `-race` Close/Servers concurrency)

- [ ] **Step 1: Failing/racing test**

`cov_convmutex_test.go`: build a manager with one healthy in-memory server; from two goroutines call `mgr.Servers()` and `mgr.Close()` concurrently under `-race`; assert no race and that post-`Close` `Servers()` still returns the server row (status may read `connected` or `failed`, but no panic / no torn read).

- [ ] **Step 2: Run under -race — fails** (unsynchronized `c.session`/`c.status` access)

Run: `cd agent && go test ./internal/mcp/ -race -run ConnMutex -count=1`
Expected: FAIL (data race) — or compile error for the new fields.

- [ ] **Step 3: Implement**

Add `mu sync.Mutex` and `closed bool` to `conn`. `Close()`: for each conn, `c.mu.Lock(); c.closed = true; sess := c.session; c.session = nil; c.mu.Unlock()`, then `sess.Close()` outside the lock. `Servers()`: per conn, `c.mu.Lock()`, snapshot `name/status/tools/lastErr`, `c.mu.Unlock()`, build the `ServerInfo` from the snapshot. (Tools are immutable per conn, but read them under the lock too for a clean snapshot.)

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ -race -run 'ConnMutex|TestMCPManager' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_convmutex_test.go`
- `git commit -m "refactor(mcp): per-conn mutex and closed flag; Servers() reads live state"`

_~70 loc._

---

## Task 7: Redial transport-factory seam

Replace the one-shot positional transport injection with a per-conn dial factory, so a conn can re-dial a fresh transport — the seam that makes reconnect constructible for both production (`transportForConfig(cfg)`) and hermetic in-memory fakes. Mechanical: all transport-passing tests convert their single transport to a one-shot factory.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (`NewManager` third param → `dials []func(context.Context) (mcpsdk.Transport, error)`; store `conn.dial`)
- Modify: `agent/internal/mcp/manager_test.go`, `cov_intg_manager_test.go`, `cov_twostage_test.go`, `cov_channelb_test.go`, `cov_parallel_test.go`, `cov_rollback_test.go`, `cov_convmutex_test.go`, `real_test.go`, `cov_s5_headers_test.go`, `cov_w2tail_manager_test.go` (call-site updates + a `staticDial` helper)
- Modify: `agent/session_init.go` (`initMCP` passes `nil`)

- [ ] **Step 1: Add the failing seam test**

Add to `cov_convmutex_test.go` (or a new `cov_redial_test.go`): a test that constructs a manager via a factory slice and asserts `NewManager` calls each factory exactly once for the initial connect (spy counter). This forces the signature change.

- [ ] **Step 2: Run — fails to compile**

Run: `cd agent && go test ./internal/mcp/ -run Redial -count=1`
Expected: FAIL to compile (`NewManager` still takes `[]mcpsdk.Transport`).

- [ ] **Step 3: Implement + migrate call sites**

Change the signature to `func NewManager(ctx context.Context, configs []mcpconfig.ServerConfig, dials []func(context.Context) (mcpsdk.Transport, error)) (*Manager, []ServerOutcome)`. Per config `i`: `dial := dials[i]` when `dials != nil && i < len(dials)`, else the production closure `func(ctx context.Context) (mcpsdk.Transport, error) { return transportForConfig(cfg) }`. The initial connect calls `dial(perServerCtx)`; store `dial` on the conn. Add a test helper in `manager_test.go`:

```go
func staticDial(t mcpsdk.Transport) func(context.Context) (mcpsdk.Transport, error) {
	return func(context.Context) (mcpsdk.Transport, error) { return t, nil }
}
```

Migrate every `[]mcpsdk.Transport{ct}` call to `[]func(context.Context) (mcpsdk.Transport, error){staticDial(ct)}`; `intg_failConnectTransport` cases pass a factory returning the sentinel error. `initMCP` passes `nil`.

- [ ] **Step 4: Run — pass (whole package)**

Run: `cd agent && go test ./internal/mcp/ -race -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -count=1`
Commit:
- `git add agent/internal/mcp/ agent/session_init.go`
- `git commit -m "refactor(mcp): per-conn redial transport factory seam"`

_~55 loc._

---

## Task 8: Lazy reconnect — `ErrConnectionClosed`-only, zero-init backoff, one post-swap retry, displaced-session close

The MCP exec closure captures the `conn` (not a session pointer) and reads `c.session` under `c.mu`. On a `CallTool` error that `errors.Is(err, mcpsdk.ErrConnectionClosed)` (SDK taxonomy, `transport.go` `call`: `ErrClientClosing`/`ErrServerClosing` only — **not** `ctx.Err()`, **not** JSON-RPC errors), the conn goes `degraded` and, if `backoffUntil` has passed (zero value = immediately) and not `closed`, re-dials via `conn.dial` under the 10s bound; on success it swaps in the new session, closes the **displaced** session, resets `backoffUntil` to `now+30s`, and **retries the triggering call once** against the new session. Recovery emits an `emitDiagnosticWarning` line. Swap and `Close` serialize on `c.mu`; `closed` is re-checked under the lock after every dial.

**Files:**
- Modify: `agent/internal/mcp/manager.go` (exec closure → capture conn; add `reconnect`/`swap` helpers; `conn` gains `backoffUntil time.Time`; a recovery-callback hook `Manager.onReconnect func(name string)`)
- Modify: `agent/session_init.go` (wire `mgr.onReconnect` to `emitDiagnosticWarning` recovery line)
- Test: `agent/internal/mcp/cov_reconnect_test.go` (new)

- [ ] **Step 1: Failing tests (the reconnect matrix)**

`cov_reconnect_test.go` uses the dial factory to hand out a fresh in-memory server each dial. Cases:
- **Closure reaches the NEW session + zero-init backoff:** dial #1 server's tool works; force the session into `ErrConnectionClosed` (close the server side so the next `CallTool` returns `ErrConnectionClosed`); the **very next** call redials (backoff zero) and succeeds against dial #2 — asserted by dial #2 returning a distinct payload. The triggering call returns success (retry-once), not an error.
- **Discrimination (J1/Decision 4):** a `CallTool` that returns a plain JSON-RPC error and one that returns `ctx.Err()` (cancel the call ctx) each leave `conn.status=="connected"` and trigger **no** redial (dial factory call-count unchanged).
- **Displaced-session leak (J7):** the dial #1 session is closed after the swap — assert via a session whose `Close` flips a spy flag; after reconnect the flag is set exactly once.
- **Close vs swap (`-race`, I6):** concurrently trigger a reconnect and call `mgr.Close()`; assert no race and no panic; a dial that finishes after `closed` discards+closes its session.

- [ ] **Step 2: Run — fails**

Run: `cd agent && go test ./internal/mcp/ -race -run Reconnect -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Rework the exec closure to close over `c *conn` (and `origName`). On call: read `sess := c.lockedSession()` (under `c.mu`); `result, err := sess.CallTool(...)`; if `errors.Is(err, mcpsdk.ErrConnectionClosed)` → `c.markDegraded(err)` then `newSess, ok := c.reconnect(ctx)`; if `ok`, retry once: `result, err = newSess.CallTool(...)`. `reconnect`: under `c.mu`, bail if `c.closed` or `time.Now().Before(c.backoffUntil)`; capture `old := c.session`; unlock for the dial; `transport, derr := c.dial(dialCtx)` (10s bound) + `client.Connect`; re-lock, if `c.closed` → close the freshly dialed session and return false; else `c.session = newSess`, `c.status = "connected"`, `c.backoffUntil = time.Now().Add(30*time.Second)`; unlock; `old.Close()` (displaced); fire `m.onReconnect(c.name)`; return `(newSess, true)`. Startup-`failed` conns have `session==nil` and no tools → never invoked (assert in a case). Then in `initMCP`, set `mgr.onReconnect = func(name string) { s.emitDiagnosticWarning(events.WarningData{Source: "mcp", Title: "MCP server reconnected", Message: fmt.Sprintf("MCP server %q reconnected after a dropped connection", name)}) }`.

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ -race -run 'Reconnect|TestMCPManager' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/mcp/... && go test ./internal/mcp/ -race -count=1`
Commit:
- `git add agent/internal/mcp/manager.go agent/internal/mcp/cov_reconnect_test.go agent/session_init.go`
- `git commit -m "feat(mcp): lazy call-driven reconnect on connection-closed with one retry"`

_~145 loc._

---

## Task 9: Flush `pendingMCPWarnings` after SESSION_START (NewSession + restore)

The collected MCP warnings ride the deferred-diagnostics path: flushed in `emitSessionStartEnvelope` via `emitDiagnosticWarning` (rendered everywhere, never fires the Notification hook). Because `emitSessionStartEnvelope` is called by both `NewSession` (`session_init.go:238`) and restore (`:500`), restored sessions re-emit deliberately.

**Files:**
- Modify: `agent/session_events.go` (`emitSessionStartEnvelope` — flush loop)
- Test: `agent/cov_mcp_warning_flush_test.go` (new)

- [ ] **Step 1: Failing test**

`cov_mcp_warning_flush_test.go` (`package agent`): start a session with a dead inline MCP server (`deadsvc:$(true)`), drain `sess.Events()`, and assert a `WARNING` event whose **Message** names the dead server and `"failed to"` arrives **after** `SESSION_START` (key on Message, not Source — enrichment rewrites an unrecognized `"mcp"` Source until Task 10 lands, but never touches Message), and that no Notification hook fired (mirror the assertion style in `session_events`/recursion tests — a session with no Notification hook simply must not panic/emit extra). Add a second case: a restored session (via `RestoreSessionFromMetaWithConfig` with the same dead server) re-emits the warning. (The enriched `Source=="mcp"` value is pinned in Task 10, which adds the classifier branch enrichment routes through.)

- [ ] **Step 2: Run — fails** (warnings collected but never emitted)

Run: `cd agent && go test ./ -run 'MCPWarningFlush' -count=1`
Expected: FAIL — no MCP warning on the stream.

- [ ] **Step 3: Implement**

In `emitSessionStartEnvelope`, after the `pendingHookWarnings` loop, add:

```go
	for _, w := range s.pendingMCPWarnings {
		s.emitDiagnosticWarning(w)
	}
	s.pendingMCPWarnings = nil
```

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./ -run 'MCPWarningFlush|TestIntg_InitMCP' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./... && go test ./ -run 'MCP' -count=1`
Commit:
- `git add agent/session_events.go agent/cov_mcp_warning_flush_test.go`
- `git commit -m "feat(mcp): flush MCP startup warnings after SESSION_START (new + restore)"`

_~40 loc._

---

## Task 10: `SourceMCP` diagnostics classifier — both packages, with the 401-stays-provider regression pin

Add a `SourceMCP` branch to the diagnostic classifier so an MCP warning gets actionable hints (command-not-found / connection-refused / auth / bad-handshake) and an MCP 401 no longer matches the **provider-credentials** matcher. This is needed in **both** copies: `agent/internal/diagnostic` (agent-side enrichment at emit, `diagnostics.go:66`) **and** root `internal/diagnostic` (re-classified in the projector, `internal/appprojector/appwire_projection.go:404`). The existing `TestClassifyProviderHTTPFailureAsProvider` in each package (a provider 401 → `SourceProvider`) is the regression pin and must keep passing.

**Files:**
- Modify: `agent/internal/diagnostic/diagnostic.go` (+ `SourceMCP`, `mcpFailure`, `normalizeSource`, `defaultForSource`)
- Modify: `internal/diagnostic/diagnostic.go` (same)
- Test: `agent/internal/diagnostic/diagnostic_test.go`, `internal/diagnostic/diagnostic_test.go` (append MCP cases)

- [ ] **Step 1: Failing tests (both packages)**

Append to each `diagnostic_test.go`:

```go
func TestFromFields_MCPSource_GetsMCPHints(t *testing.T) {
	// A connection-refused MCP failure classifies as MCP, not the generic serf hint.
	got := FromFields("mcp", "", "", "MCP server \"linear\" failed to connect: connection refused")
	if got.Source != SourceMCP {
		t.Fatalf("Source=%q, want %q", got.Source, SourceMCP)
	}
}

func TestFromFields_MCP401_DoesNotMatchProvider(t *testing.T) {
	// An MCP auth failure carrying "unauthorized" must NOT read as a provider-credential error.
	got := FromFields("mcp", "", "", "MCP server \"linear\" failed to connect: 401 unauthorized")
	if got.Source != SourceMCP {
		t.Fatalf("MCP 401 misclassified: Source=%q, want %q", got.Source, SourceMCP)
	}
}
```

Confirm `TestClassifyProviderHTTPFailureAsProvider` (provider 401 → `SourceProvider`) already exists in each file and leave it unchanged — it is the pin proving we did not break provider classification.

- [ ] **Step 2: Run — fails** (`undefined: SourceMCP`)

Run: `cd agent && go test ./internal/diagnostic/ -run 'MCP|Provider' -count=1` and `cd . && go test ./internal/diagnostic/ -run 'MCP|Provider' -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement in both files**

Add `SourceMCP Source = "mcp"` to the const block; add:

```go
func mcpFailure() Info {
	return Info{
		Source: SourceMCP,
		Title:  "MCP server error",
		Hint:   "An MCP server failed to connect, authenticate, or complete a tool call. Check the command is on PATH (stdio), the URL/headers and auth token (http/sse), and that the server speaks MCP. The session runs without it; other tools are unaffected.",
	}
}
```

Add `case SourceMCP: return SourceMCP` to `normalizeSource`, and `case SourceMCP: return mcpFailure()` to `defaultForSource`. (No change to `isProviderFailure` — the provider matcher stays; MCP warnings never reach it because they carry `Source:"mcp"`, so `FromFields` short-circuits via `defaultForSource`.)

- [ ] **Step 4: Run — pass (both)**

Run: `cd agent && go test ./internal/diagnostic/ -count=1 && cd .. && go test ./internal/diagnostic/ -count=1` (adjust dirs to repo layout)
Expected: PASS in both modules.

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./internal/diagnostic/... && cd /Users/jesse/prime-radiant/toil-suite/serf && golangci-lint run ./internal/diagnostic/...`
Commit:
- `git add agent/internal/diagnostic/diagnostic.go agent/internal/diagnostic/diagnostic_test.go internal/diagnostic/diagnostic.go internal/diagnostic/diagnostic_test.go`
- `git commit -m "feat(diagnostic): SourceMCP classifier so MCP failures get MCP hints, not provider"`

_~75 loc._

---

## Task 11: Config-load honesty — `Discover` surfaces swallowed global/project layer errors

A global/project `mcp.json` that fails to parse or expand its `${VAR}` today has its **entire layer silently dropped** (`config.go:256,268` swallow every non-nil error, not just missing-file). Make `Discover` return per-layer warnings naming the file + error; the layer is still skipped (non-fatal). `initMCP` folds those into `pendingMCPWarnings`. CLI `--mcp-config` / `--mcp` stay fatal — `TestIntg_InitMCP_DiscoverError` must stay green.

**Files:**
- Modify: `agent/mcpconfig/config.go` (`Discover` returns warnings; distinguish not-exist from parse/expand)
- Modify: `agent/session_init.go` (`initMCP` folds Discover warnings)
- Test: `agent/mcpconfig/config_test.go` (new cases), `agent/cov_intg_mcp_test.go` (a global-layer parse-failure survives)

- [ ] **Step 1: Failing tests**

In `config_test.go`: point `XDG_CONFIG_HOME` at a temp dir containing `serf/mcp.json` with malformed JSON; assert `Discover` returns the parsed configs it could (none here) **plus** a warning naming the path, and **no** fatal error. Add a second case: valid JSON but an unset `${NOPE}` → warning, non-fatal. Add a not-exist case: missing global file → no warning, no error.

- [ ] **Step 2: Run — fails to compile** (`Discover` returns two or three values)

Run: `cd agent && go test ./mcpconfig/ -run 'DiscoverWarn|DiscoverMissing' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Change `Discover` to `func Discover(env execenv.ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]ServerConfig, []string, error)`. For layers 1-2, replace `if configs, err := LoadFile(p); err == nil` with: on `err != nil`, skip if `errors.Is(err, os.ErrNotExist)` (silent, as before), else append `fmt.Sprintf("MCP config %s: %v", p, err)` to warnings and skip. Layers 3-4 (CLI) keep returning the fatal error. Update the sole caller `initMCP`:

```go
	configs, cfgWarnings, err := mcpconfig.Discover(s.currentEnv(), s.cfg.MCPConfigFiles, s.cfg.MCPInline)
	if err != nil {
		return err
	}
	for _, w := range cfgWarnings {
		s.pendingMCPWarnings = append(s.pendingMCPWarnings, events.WarningData{
			Source: "mcp", Title: "MCP config error", Message: w,
		})
	}
```

- [ ] **Step 4: Run — pass** (incl. the DiscoverError pin still fatal)

Run: `cd agent && go test ./mcpconfig/ ./ -run 'Discover|TestIntg_InitMCP' -count=1`
Expected: PASS — `TestIntg_InitMCP_DiscoverError` (CLI inline) still fatal; global-layer parse failure now warns + survives.

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./mcpconfig/... ./... && go test ./mcpconfig/ -count=1`
Commit:
- `git add agent/mcpconfig/config.go agent/mcpconfig/config_test.go agent/session_init.go agent/cov_intg_mcp_test.go`
- `git commit -m "fix(mcpconfig): surface swallowed global/project MCP config-layer errors as warnings"`

_~55 loc._

---

## Task 12: Plugin config honesty — inline `mcpServers` `ParseServerMap` failure degrades to a plugin-level diagnostic

The one fatal plugin MCP path is a well-formed inline `mcpServers` map whose entries fail `ParseServerMap` (empty name, unset `${VAR}`; `plugin.go:131-134` → `Load`/`LoadAll` abort). Per-server rows are unbuildable from a parse failure, so degrade to a **plugin-level** `failed(config)` diagnostic: the plugin's MCP layer is skipped, the warning names the plugin + error, and its hooks/skills/agents load normally. The already-swallowed gaps (`.mcp.json` **file** parse error `plugin.go:121`, malformed inline JSON `plugin.go:130`) get the same visible warning.

**Files:**
- Modify: `agent/plugin/plugin.go` (`Instance` gains `MCPConfigWarnings []string`; `discoverPluginMCPConfigs` collects instead of aborting; `loadPluginMCPFile` non-notexist errors warned)
- Modify: `agent/session_init.go` (`initPlugins` folds `p.MCPConfigWarnings` into `pendingMCPWarnings`)
- Test: `agent/plugin/plugin_test.go` (new cases), `agent/cov_intg_mcp_test.go` (session survives a bad plugin inline map)

- [ ] **Step 1: Failing tests**

In `agent/plugin/plugin_test.go`: a plugin whose manifest `mcpServers` is a valid JSON object but has an entry with an empty name (or `${UNSET}`) → `Load` returns a **non-error** `Instance` whose `MCPConfigWarnings` names the plugin + error and whose `MCPConfigs` is empty, while `Skills`/`Agents`/`Hooks` still populate. Add a `.mcp.json`-file-parse-error case and a malformed-inline-JSON case → each yields a warning. In `cov_intg_mcp_test.go`: a `NewSession` with such a plugin **succeeds** and emits an MCP warning.

- [ ] **Step 2: Run — fails** (`Load` aborts; `MCPConfigWarnings` undefined)

Run: `cd agent && go test ./plugin/ -run 'MCPConfigWarn|InlineParse' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add `MCPConfigWarnings []string` to `Instance`. Change `discoverPluginMCPConfigs` to return `([]mcpconfig.ServerConfig, []string, error)` where the `[]string` is warnings and `error` is reserved for truly unexpected failures (none of the config-parse paths). On `loadPluginMCPFile` error that is not `os.ErrNotExist`, append a warning (was silently swallowed at `:121`). On inline JSON unmarshal error (`:130`), append a warning (was swallowed). On `ParseServerMap` failure (`:131`), append `fmt.Sprintf("plugin %q: MCP config failed: %v", pluginName, err)` and skip the inline layer (was fatal). `Load` sets `lp.MCPConfigWarnings = warnings` instead of returning the error. `initPlugins` (`session_init.go:773` area): after `s.pluginMCPConfigs = append(...)`, add:

```go
		for _, w := range p.MCPConfigWarnings {
			s.pendingMCPWarnings = append(s.pendingMCPWarnings, events.WarningData{
				Source: "mcp", Title: "MCP server unavailable", Message: w, PluginName: p.Manifest.Name,
			})
		}
```

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./plugin/ ./ -run 'MCPConfigWarn|InlineParse|TestIntg_InitMCP' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./plugin/... ./... && go test ./plugin/ -count=1`
Commit:
- `git add agent/plugin/plugin.go agent/plugin/plugin_test.go agent/session_init.go agent/cov_intg_mcp_test.go`
- `git commit -m "fix(plugin): degrade fatal inline mcpServers parse failure to a plugin-level warning"`

_~60 loc._

---

## Task 13: `Status`/`Error` on `mcpconfig.ServerInfo` + live `Servers()` read + Channel-B stamps `Error`

Populate the health surface at the source. `ServerInfo` carries `Status` and `Error`; `Servers()` reports each conn's live `status` and `lastErr` under the mutex. `Error` carries the **last failure of any kind, including a Channel-B application error on a `connected` server** (J4): the exec closure records the error on the conn (under `c.mu`) without changing `status` — so a Linear-400-on-every-call server reads `connected · last error: … (2m ago)`, not fully healthy.

**Files:**
- Modify: `agent/mcpconfig/config.go` (`ServerInfo` + `Status`/`Error`)
- Modify: `agent/internal/mcp/manager.go` (`Servers()` fills them; closure stamps `lastErr` on Channel-B and Channel-A errors)
- Test: `agent/internal/mcp/cov_channelb_test.go` (extend), `agent/internal/mcp/manager_test.go`

- [ ] **Step 1: Failing test — Channel-B stamps Error, status unchanged**

Extend `cov_channelb_test.go`: after the Channel-B call from Task 1, assert `mgr.Servers()[0].Status == "connected"` and `mgr.Servers()[0].Error` contains `"upstream 400"`. Add a healthy-server case asserting `Status=="connected"`, `Error==""`.

- [ ] **Step 2: Run — fails** (`ServerInfo.Status`/`.Error` undefined; not stamped)

Run: `cd agent && go test ./internal/mcp/ -run 'ChannelB|ServersStatus' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add `Status string` and `Error string` to `mcpconfig.ServerInfo`. In the exec closure, on **any** returned error (Channel A transport error, Channel B `result.IsError`), do `c.recordError(err)` = under `c.mu` set `c.lastErr=err; c.lastErrAt=time.Now()` (do **not** change `status` for Channel B; the reconnect path already sets `degraded` for `ErrConnectionClosed`). `Servers()` sets `Status: c.status` and `Error: errString(c.lastErr)` from the locked snapshot. (Keep the `lastErrAt` for the renderer's "(2m ago)"; expose it via `ServerInfo` only if the renderer needs it — otherwise fold the age into the `Error` string at render time in Task 15. Simplest: `Servers()` leaves `Error` as the raw message; the TUI/settings render the age from a separate field only if added. To avoid scope creep, carry just `Error` string; drop the age or format it into `Error`.)

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./internal/mcp/ -race -run 'ChannelB|ServersStatus|TestMCPManager' -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./mcpconfig/... ./internal/mcp/... && go test ./internal/mcp/ ./mcpconfig/ -count=1`
Commit:
- `git add agent/mcpconfig/config.go agent/internal/mcp/manager.go agent/internal/mcp/cov_channelb_test.go agent/internal/mcp/manager_test.go`
- `git commit -m "feat(mcp): ServerInfo carries live Status/Error; Channel-B stamps Error without demotion"`

_~55 loc._

---

## Task 14: Thread `Status`/`Error` through the carrier chain + protocol doc/golden regen

Enumerated carrier chain: `mcpconfig.ServerInfo` → `agent.DetailedStatus.MCP` (already `[]mcpconfig.ServerInfo`) → `server.MCPServerInfo` → `appwire.SerfMCPServerInfo`, with both converters (`cmd/serf/serve.go:570`, `server/appwire_runtime.go:515`). Regenerate the protocol doc + appwire golden.

**Files:**
- Modify: `server/server.go` (`MCPServerInfo` + `Status`/`Error`)
- Modify: `appwire/types.go` (`SerfMCPServerInfo` + `Status`/`Error`)
- Modify: `cmd/serf/serve.go` (converter), `server/appwire_runtime.go` (converter)
- Regen: `docs/appwire-protocol.md`, `appwire` goldens
- Test: `server/appwire_runtime_test.go` (or nearest converter test) — Status/Error survive the hop

- [ ] **Step 1: Failing converter test**

In the converter test nearest `appDiagnosticsFromDetailedStatus`, feed a `DetailedStatus` whose `MCP[0]` has `Status:"degraded", Error:"boom"` and assert the produced `SerfMCPServerInfo` carries both. Add the analogous assertion for `agentToServerDetailedStatus` in `cmd/serf/serve_test.go` (nearest existing test).

- [ ] **Step 2: Run — fails to compile** (fields undefined on `server.MCPServerInfo` / `SerfMCPServerInfo`)

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./server/ ./cmd/serf/ -run MCP -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement + converters + regen**

Add `Status string json:"status,omitempty"` and `Error string json:"error,omitempty"` to `server.MCPServerInfo` and `appwire.SerfMCPServerInfo`. Update the two converters:
- `serve.go:570`: `server.MCPServerInfo{Name: m.Name, Tools: m.Tools, Status: m.Status, Error: m.Error}`
- `appwire_runtime.go:515`: `appwire.SerfMCPServerInfo{Name: srv.Name, Tools: append([]string(nil), srv.Tools...), Status: srv.Status, Error: srv.Error}`

Regenerate: `make generate` (updates `docs/appwire-protocol.md` if the reflected type table changes). If `appwire/golden_test.go` fails, refresh with `go test ./appwire -run '^Test.*Golden$' -update-goldens` and re-run.

- [ ] **Step 4: Run — pass**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./server/ ./cmd/serf/ ./appwire/ -count=1 && make lint-generated`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && golangci-lint run ./server/... ./cmd/serf/... ./appwire/...`
Commit:
- `git add server/server.go appwire/types.go cmd/serf/serve.go server/appwire_runtime.go docs/appwire-protocol.md appwire/testdata server/appwire_runtime_test.go cmd/serf/serve_test.go`
- `git commit -m "feat(wire): thread MCP Status/Error through the status carrier chain"`

_~55 loc. (Add only the golden files that actually changed under `appwire/testdata`.)_

---

## Task 15: TUI renderers surface MCP status/error (both renderers)

The two TUI MCP-server renderers print only `name (N tools)`. Show `status` and, when present, `error`. `details_drawer.go` reads `appwire.SerfMCPServerInfo`; `hub_status.go` reads `server.MCPServerInfo` — both now carry the fields.

**Files:**
- Modify: `cmd/serf-tui/details_drawer.go` (~line 136-138)
- Modify: `cmd/serf-tui/hub_status.go` (~line 128-130)
- Test: `cmd/serf-tui/details_drawer_test.go` / `hub_status_test.go` (nearest existing render tests)

- [ ] **Step 1: Failing render test**

In the nearest existing TUI render test, build a status with one `connected` server (no error) and one `degraded` server with `Error:"connection refused"`; assert the rendered string contains `degraded` and `connection refused` and `connected`.

- [ ] **Step 2: Run — fails** (renderer omits status/error)

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./cmd/serf-tui/ -run 'MCP|Details|HubStatus' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

In both renderers, extend the per-server line to append status and error, e.g.:

```go
	line := fmt.Sprintf("\n  %s (%d tools) — %s", srv.Name, len(srv.Tools), srv.Status)
	if srv.Error != "" {
		line += " — last error: " + srv.Error
	}
	fmt.Fprint(b, line)
```

Guard for an empty `Status` (older daemons / no MCP) so the em-dash suffix is omitted when `Status==""`.

- [ ] **Step 4: Run — pass**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./cmd/serf-tui/ -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && golangci-lint run ./cmd/serf-tui/...`
Commit:
- `git add cmd/serf-tui/details_drawer.go cmd/serf-tui/hub_status.go cmd/serf-tui/details_drawer_test.go cmd/serf-tui/hub_status_test.go`
- `git commit -m "feat(tui): render MCP server status and last error"`

_~40 loc._

---

## Task 16: Update `docs/subagent-runtime-contracts.md` (fail-fast paragraph + silent-gap notes)

Named deliverable (I5). The doc's "fail-fast / no partial-availability" paragraph (`:63-65`) and the "malformed inline `mcpServers` block is swallowed" silent-gap note (`:94-95`) describe the exact behavior this workstream inverts. Invoke the `maintaining-documentation` skill.

**Files:**
- Modify: `docs/subagent-runtime-contracts.md`

- [ ] **Step 1: Rewrite the fail-fast paragraph (`:63-65`)**

Replace "MCP *server* unavailability, by contrast, is fail-fast: any server that fails to connect or list its tools fails session init … there is no partial-availability mode." with the new contract: MCP startup is **non-fatal and parallel** — a server that fails to build its transport, connect, list tools, or register is skipped with a deferred `SourceMCP` warning after `SESSION_START`; the session constructs with the healthy servers (zero healthy servers is still a healthy session). Note the call-driven lazy reconnect (one attempt per dropped connection, `ErrConnectionClosed` only) and that CLI `--mcp-config`/`--mcp` config-parse errors stay fatal.

- [ ] **Step 2: Update the silent-gap note (`:94-95`)**

Change "a **malformed inline `mcpServers` block is swallowed** (yields zero servers, no error)" to: an inline `mcpServers` parse failure now degrades to a **plugin-level MCP warning** (the plugin's MCP layer is skipped, other contributions load); the `.mcp.json` file-parse and malformed-inline-JSON gaps are likewise warned. Leave the `skills`/`tasks` value-validation gap as-is.

- [ ] **Step 3: Verify + commit**

Run the `maintaining-documentation` skill's check that the anchors named (`initMCP`, `NewManager`) still exist. Then:
- `git add docs/subagent-runtime-contracts.md`
- `git commit -m "docs(contracts): MCP init is non-fatal/parallel; inline mcpServers failure is warned"`

_~20 loc (prose)._

---

## Task 17: `agent/mcpprobe` package — honest, limit-stated probe

New public package replacing the orphaned `mcpstatus`. Parallel, 3s cap, per-row env errors, a **real `initialize` handshake** for http/sse (a 200 that fails `initialize` is not "available"), command-present labeling for stdio, and its limits stated on the surface: initialize-OK cannot certify per-tool success (the 400-ing-Linear case shows `available` while calls fail — the `Error` field is what tells that truth), and stdio command-present cannot certify connectability. Results are "as probed from the hub" with the daemon-env divergence noted (in the package doc + the settings caption, Task 18).

**Files:**
- New: `agent/mcpprobe/mcpprobe.go`
- Test: `agent/mcpprobe/mcpprobe_test.go`

- [ ] **Step 1: Failing tests**

`mcpprobe_test.go`: 
- http server returning a valid MCP `initialize` response → `Status:"available"`.
- http server returning 200 but a non-MCP body (initialize fails) → `Status:"unreachable"` (this is the status-code-only bug `mcpstatus` had — a 400/garbage reads unreachable now).
- refused URL → `unreachable`.
- stdio with a command on PATH → `available` (labeled command-present), missing command → `missing`.
- a config whose `${VAR}` is unset (env error surfaced by the config layer) → per-row `Error` populated.
- parallel: N slow http probes finish within ~3s, not N×3s.

- [ ] **Step 2: Run — fails** (package does not exist)

Run: `cd agent && go test ./mcpprobe/ -count=1`
Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`func Probe(ctx context.Context, configs []mcpconfig.ServerConfig) []Result` with `Result{Name, Transport, Status, Error string}`. One goroutine per config under `context.WithTimeout(ctx, 3*time.Second)`, results indexed by position (config order). stdio: `exec.LookPath` → `available`/`missing`. http/sse: build the transport via the same `transportForConfig`-equivalent (reuse `agent/internal/mcp` transport construction is internal — so `mcpprobe` constructs `mcpsdk.StreamableClientTransport`/`SSEClientTransport` directly with the config's URL+headers) and run `mcpsdk.NewClient(...).Connect(ctx, transport, nil)` → success = `available`, else `unreachable` with `Error` set. Package doc comment states the limits verbatim.

- [ ] **Step 4: Run — pass**

Run: `cd agent && go test ./mcpprobe/ -count=1`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd agent && golangci-lint run ./mcpprobe/...`
Commit:
- `git add agent/mcpprobe/`
- `git commit -m "feat(mcpprobe): honest parallel MCP probe with real initialize and stated limits"`

_~110 loc._

---

## Task 18: Settings pane renders the discovered server list + status column

`discoverMCPsForSettings` computes `[]mcpDisplay` but it is **computed-and-discarded** — no template renders `settingsData.Mcps`. Wire it to `mcpprobe` (status-code-checked) and render a status column (name, transport, probe status, last error) in the MCP settings partial, captioned "as probed from the hub". Source-layer column dropped (global-file-only discovery).

**Files:**
- Modify: `cmd/serf-hub/web_settings.go` (`discoverMCPsForSettings` → `mcpprobe.Probe`; `mcpDisplay` gains `Transport`/`Error`)
- Modify: `cmd/serf-hub/web_types.go` (`mcpDisplay` fields)
- Modify: `cmd/serf-hub/templates/partials/settings/mcp.html` (render the status table)
- Test: `cmd/serf-hub/web_settings_test.go` (nearest); jstest `cmd/serf-hub/jstest/test-settings-mcp-status.js` (new)

- [ ] **Step 1: Failing tests**

Go: assert `discoverMCPsForSettings` for a config file with one stdio (present command) and one http (refused URL) returns rows with `Status` `available`/`unreachable` and a populated `Error` on the refused row. jstest: render the MCP settings partial with a seeded status list (or assert the template contains the status/error columns and the "as probed from the hub" caption).

- [ ] **Step 2: Run — fails**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./cmd/serf-hub/ -run 'MCPStatus|DiscoverMCP' -count=1`
Expected: FAIL.

- [ ] **Step 3: Implement**

`discoverMCPsForSettings`: build rows from `mcpprobe.Probe(ctx, configs)` (transport label + status + error) instead of `mcpstatus.ProbeMCPStatus`. Add `Transport` and `Error` to `mcpDisplay`. In `mcp.html`, add a rendered table/list of the discovered servers with columns name / transport / status / last error and the caption. Keep the existing editable config-files + inline-servers sections; add the status list as a read-only pane fed by `settingsData.Mcps` (server-rendered) or via the existing settings data endpoint — match how the partial gets its data (this partial is client-JS-driven off `launchconfig`; add a small fetch of the probe rows or render them server-side into the partial). Remove the `mcpstatus` import.

- [ ] **Step 4: Run — pass**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && go test ./cmd/serf-hub/ -count=1 && cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules node test-settings-mcp-status.js`
Expected: PASS

- [ ] **Step 5: Lint + commit**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && golangci-lint run ./cmd/serf-hub/...`
Commit:
- `git add cmd/serf-hub/web_settings.go cmd/serf-hub/web_types.go cmd/serf-hub/templates/partials/settings/mcp.html cmd/serf-hub/web_settings_test.go cmd/serf-hub/jstest/test-settings-mcp-status.js`
- `git commit -m "feat(settings): render discovered MCP servers with a probed status column"`

_~80 loc._

---

## Task 19: Delete the `mcpstatus` package

Now orphaned (its sole consumer, `discoverMCPsForSettings`, moved to `mcpprobe` in Task 18).

**Files:**
- Delete: `cmd/serf-hub/internal/mcpstatus/`

- [ ] **Step 1: Confirm no references remain**

Run: `cd /Users/jesse/prime-radiant/toil-suite/serf && grep -rn "mcpstatus" --include="*.go" .`
Expected: no matches.

- [ ] **Step 2: Delete + build**

Run: `git rm -r cmd/serf-hub/internal/mcpstatus && cd /Users/jesse/prime-radiant/toil-suite/serf && go build ./... && go test ./cmd/serf-hub/ -count=1`
Expected: builds clean, tests pass.

- [ ] **Step 3: Commit**

- `git commit -m "chore(mcp): remove orphaned mcpstatus package (superseded by mcpprobe)"`

_~15 loc (deletion)._

---

## Task 20: Three-server end-to-end scenario card

Invoke the `e2e-scenario-testing` skill. One server cannot be both startup-failed and rendering-red (J3), so the card uses **three** servers against a freshly built hub+daemon:

**Files:**
- New: `docs/superpowers/proofs/2026-07-04-mcp-resilience-e2e.md` (scenario card + captured evidence)

- [ ] **Step 1: Author the scenario card** with falsifiable assertions:
  - **Server A (good):** a healthy stdio echo server (reuse `agent/testdata/intgmcpserver`) → session constructs; its tool is callable; settings probe `available`; `Servers()`/wire `connected`.
  - **Server B (startup-failed):** an inline `deadsvc:$(command -v true)` → warning emitted after `SESSION_START` with `Source=mcp`; no B tool registered; B stays terminal (no reconnect trigger); settings probe `unreachable`/`missing`.
  - **Server C (connected but Channel-B-erroring):** an http/stdio server that connects but returns `IsError:true` on every `tools/call` → the tool result renders **red** in web + TUI (error marker / error body); `Servers()` shows C `connected` with `Error` populated; status is still `connected` (Decision 3).
  - **Reconnect:** kill + restart Server A's process, then issue the next call → it reconnects **immediately** (zero-init backoff) and succeeds; a reconnect diagnostic line renders.

- [ ] **Step 2: Build + run** a live `serf-hub` + `serf serve` (per `reference_serf_live_run`: `go build -o /tmp/serf ./cmd/serf`; source the repo `.env`; use a real `--model oai-work/<model>`). Drive the web UI (and TUI where cheap) capturing screenshots/DOM + event-stream excerpts proving each assertion. Record any assertion that fails as a defect, not a pass.

- [ ] **Step 3: Commit** the proof:
- `git add docs/superpowers/proofs/2026-07-04-mcp-resilience-e2e.md`
- `git commit -m "test(mcp): three-server e2e proof — good, startup-failed, Channel-B-erroring, reconnect"`

_~55 loc (mostly prose + captured evidence)._

---

## Task 21: Full-repo gates

- [ ] **Step 1: Run every gate green**

Run, from repo root:
- `make test-short`
- `make test-race`
- `make lint`
- `cd cmd/serf-hub/jstest && NODE_PATH=/tmp/serf-jstest-jsdom/node_modules sh run-all.sh`

Expected: all pass. Fix any failure at root cause (a gate failure is in scope). If a full-suite MCP test outside the ones touched here now fails, it is a regression from this branch — fix it, do not skip it.

- [ ] **Step 2: Commit any gate fixes** with a descriptive message; otherwise no commit.

_~5 loc._

---

## Self-review — Review-log fold → task map (every fold has a test)

| Fold | Task | Pinning test |
|---|---|---|
| I1 definitions rebuilt from registered set | 3 | `DefinitionsRebuilt` (failed-register server absent from `ToolDefinitions`) |
| I2/J2 plugin fold retargeted to inline `ParseServerMap`, plugin-level, gaps warned | 12 | `InlineParse` + file/JSON-gap cases |
| I3 rollback spares the collision winner | 3 | `Rollback` (winner's colliding tool survives) |
| I4/J8 `Discover` layer-swallow → warnings | 11 | `DiscoverWarn` + `TestIntg_InitMCP_DiscoverError` stays fatal |
| I5 contracts doc deliverable | 16 | doc rewrite (fail-fast + silent-gap) |
| I6 one mutex + closed flag (close-vs-swap race) | 6 + 8 | `ConnMutex` (`-race`) + reconnect close-vs-swap (`-race`) |
| I7 recovery line via diagnostic path | 8 | reconnect fires `onReconnect` → `emitDiagnosticWarning` |
| J1 `ErrConnectionClosed`-only discrimination | 8 | discrimination case (ctx-cancel + JSON-RPC do not break) |
| J3 three-server e2e | 20 | scenario card |
| J4 Channel-B stamps `Error`; probe limits stated | 13 + 17 | `ChannelB` (Error set, status unchanged) + probe limit doc/tests |
| J5 per-conn redial factory seam | 7 | `Redial` (factory called per connect) |
| J6 `SourceMCP` classifier | 10 | `TestFromFields_MCP401_DoesNotMatchProvider` (both pkgs) |
| J7 displaced session closed | 8 | displaced-session leak spy |
| J9 zero-init backoff + one post-swap retry | 8 | closure-reaches-new-session (immediate redial, single retry) |

Contract tests that flip, each inside the task that changes their behavior: NewManager Transport/Connect/ListTools → Task 2; `RegisterTools` BuiltinCollision/ToolNameTooLong → Task 3; `InitMCP` Connect/RegisterTools → Task 4. `TestIntg_InitMCP_DiscoverError` stays fatal-pinned (asserted green in Tasks 4 and 11). Severity’s two new pins (turn-loop error-typed result + jstest error-marker) → Task 1.
