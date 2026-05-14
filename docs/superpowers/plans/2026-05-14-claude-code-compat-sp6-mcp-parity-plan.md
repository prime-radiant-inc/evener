# SP6 — MCP Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the three documented gaps between Claude Code's MCP behavior and serf's existing implementation: accept `streamable-http` as alias for `http`, inject `CLAUDE_PROJECT_DIR` into spawned stdio MCP servers, and extend variable-expansion to resolve `${CLAUDE_PROJECT_DIR}`, `${CLAUDE_PLUGIN_ROOT}`, `${CLAUDE_PLUGIN_DATA}`, and `${user_config.KEY}`.

**Architecture:** All changes confined to package `agent`. Rename `expandEnvVars` → `expandVars` and thread an unexported `expansionContext` through the loaders. Add a `resolveProjectDir` helper that walks (session override → git root → cwd). Add `projectDir` parameter to `transportForConfig` and inject `CLAUDE_PROJECT_DIR` into the stdio env map. Replace `expandPluginRoot`'s string `ReplaceAll` with an `expandVars` call seeded with plugin paths.

**Tech Stack:** Go 1.x, `github.com/modelcontextprotocol/go-sdk`, standard library only. TDD with `t.TempDir()` and real files (no mocked filesystem). Table-driven tests in the style of `agent/mcp_config_test.go`.

**Spec source of truth:** `docs/superpowers/specs/2026-05-14-claude-code-compat-sp6-mcp-parity-design.md`

---

## File Map

Files modified by this plan:

- `agent/mcp_config.go` — rename `expandEnvVars` → `expandVars`; add unexported `expansionContext` and `userConfigLookup`; collapse `streamable-http`; thread context through `serverJSONToConfig` and `LoadMCPConfigFile`; add `resolveProjectDir`.
- `agent/mcp_manager.go` — add `projectDir string` parameter to `transportForConfig`; inject `CLAUDE_PROJECT_DIR` into the stdio env map; thread `projectDir` from `NewMCPManager`'s callers.
- `agent/plugin.go` — replace literal `expandPluginRoot` with `expandVars` seeded with plugin paths; remove the helper once parity is confirmed.
- `agent/session.go` — pass resolved project dir into `NewMCPManager`.
- `agent/mcp_config_test.go`, `agent/mcp_manager_test.go`, `agent/plugin_test.go` — new tests per §10 of the spec.

No new files, no new packages. Test fixtures live inline (table-driven) per existing serf style.

---

## Task Sequence Overview

The plan splits into nine task groups, ordered so each test is written before its implementation and each commit leaves the tree green:

1. Rename `expandEnvVars` → `expandVars` with a zero-value `expansionContext` (refactor only, no behavior change).
2. Add `expansionContext` plumbing through `serverJSONToConfig` and the plugin loader (still no behavior change).
3. Add `${CLAUDE_PROJECT_DIR}` resolution from context.
4. Add `${CLAUDE_PLUGIN_ROOT}` and `${CLAUDE_PLUGIN_DATA}` resolution from context.
5. Add `${user_config.KEY}` resolution via `userConfigLookup`.
6. Collapse `streamable-http` alias to `http`.
7. Add `resolveProjectDir` helper.
8. Wire `projectDir` through `transportForConfig` and inject `CLAUDE_PROJECT_DIR`.
9. Replace `expandPluginRoot` with `expandVars` in `agent/plugin.go`; remove helper.

---

## Task 1: Rename `expandEnvVars` → `expandVars` with context plumbing (no behavior change)

**Goal:** Introduce the new function name and `expansionContext` type. Behavior remains identical to today; only the call signature changes. Old name kept as a shim until SP6 fully lands.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 1.1: Write the failing test for the rename + context plumbing

- [ ] Append the following test to `agent/mcp_config_test.go`:

```go
// TestExpandVars_ZeroContext_MatchesExpandEnvVars verifies the renamed
// expandVars function with a zero-value expansionContext behaves identically
// to the old expandEnvVars for inputs that only contain ${OS_VAR}
// placeholders.
func TestExpandVars_ZeroContext_MatchesExpandEnvVars(t *testing.T) {
	t.Setenv("TEST_MCP_VAR", "hello")

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"no vars here", "no vars here", false},
		{"${TEST_MCP_VAR}", "hello", false},
		{"prefix-${TEST_MCP_VAR}-suffix", "prefix-hello-suffix", false},
		{"${TEST_MCP_VAR:-fallback}", "hello", false},
		{"${UNSET_VAR_12345:-default}", "default", false},
		{"${UNSET_VAR_12345}", "", true},
		{"${TEST_MCP_VAR:-}", "hello", false},
		{"${UNSET_VAR_12345:-}", "", false},
		{"multiple ${TEST_MCP_VAR} and ${TEST_MCP_VAR}", "multiple hello and hello", false},
	}

	for _, tt := range tests {
		got, err := expandVars(tt.input, expansionContext{})
		if tt.wantErr {
			if err == nil {
				t.Errorf("expandVars(%q, {}): expected error, got %q", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("expandVars(%q, {}): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("expandVars(%q, {}) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

### Step 1.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestExpandVars_ZeroContext_MatchesExpandEnvVars -v`

Expected: FAIL with `undefined: expandVars` and `undefined: expansionContext`.

### Step 1.3: Add `expansionContext` type and `userConfigLookup` alias

- [ ] In `agent/mcp_config.go`, immediately after the `mcpServerJSON` struct definition (around line 35), add:

```go
// userConfigLookup returns the resolved value for one user_config key bound
// to the plugin whose config is being expanded. Returns ok==false if the
// key is undefined; callers treat that the same as an unset env var.
// Implemented by SP7. SP6 calls only the function; storage, prompt flow,
// keychain integration, and CLAUDE_PLUGIN_OPTION_* injection are SP7.
type userConfigLookup func(key string) (value string, ok bool)

// expansionContext supplies the values consulted by expandVars when it
// encounters a name-bearing placeholder. All fields are optional: an empty
// value means "this kind of placeholder is not available in this context",
// and any reference to such a name fails with the same "not set" error
// expandVars uses for unset OS env vars.
type expansionContext struct {
	ProjectDir string           // value substituted for ${CLAUDE_PROJECT_DIR}
	PluginRoot string           // value substituted for ${CLAUDE_PLUGIN_ROOT}
	PluginData string           // value substituted for ${CLAUDE_PLUGIN_DATA}
	UserConfig userConfigLookup // nil means ${user_config.*} is unavailable
}
```

### Step 1.4: Rename `expandEnvVars` to `expandVars` and add the context parameter

- [ ] In `agent/mcp_config.go`, replace the existing `expandEnvVars` function (lines ~126-171) with:

```go
// expandVars expands ${VAR} and ${VAR:-default} in s. The context supplies
// values for the reserved names CLAUDE_PROJECT_DIR, CLAUDE_PLUGIN_ROOT,
// CLAUDE_PLUGIN_DATA, and user_config.KEY; any other name falls through to
// os.LookupEnv. Missing ${VAR} with no default is an error.
func expandVars(s string, ctx expansionContext) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], "${")
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		i += idx + 2

		end := strings.Index(s[i:], "}")
		if end < 0 {
			b.WriteString("${")
			continue
		}

		expr := s[i : i+end]
		i += end + 1

		varName := expr
		defaultVal := ""
		hasDefault := false
		if di := strings.Index(expr, ":-"); di >= 0 {
			varName = expr[:di]
			defaultVal = expr[di+2:]
			hasDefault = true
		}

		val, ok := os.LookupEnv(varName)
		if !ok {
			if !hasDefault {
				return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
			}
			val = defaultVal
		}
		b.WriteString(val)
	}
	return b.String(), nil
}
```

The body is identical to the old `expandEnvVars` — only the name and signature changed. Context is unused at this step; later tasks add resolution branches.

### Step 1.5: Replace every `expandEnvVars(x)` call site with `expandVars(x, expansionContext{})`

- [ ] In `agent/mcp_config.go`, in `serverJSONToConfig`, replace all five `expandEnvVars(...)` call sites with `expandVars(..., expansionContext{})`:

```go
func serverJSONToConfig(name string, sj mcpServerJSON) (MCPServerConfig, error) {
	typ := strings.TrimSpace(sj.Type)
	if typ == "" {
		typ = "stdio"
	}

	command, err := expandVars(sj.Command, expansionContext{})
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding command: %w", err)
	}

	args := make([]string, len(sj.Args))
	for i, a := range sj.Args {
		args[i], err = expandVars(a, expansionContext{})
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding arg[%d]: %w", i, err)
		}
	}
	if len(sj.Args) == 0 {
		args = nil
	}

	env := make(map[string]string, len(sj.Env))
	for k, v := range sj.Env {
		env[k], err = expandVars(v, expansionContext{})
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding env %q: %w", k, err)
		}
	}
	if len(sj.Env) == 0 {
		env = nil
	}

	url, err := expandVars(sj.URL, expansionContext{})
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding url: %w", err)
	}

	headers := make(map[string]string, len(sj.Headers))
	for k, v := range sj.Headers {
		headers[k], err = expandVars(v, expansionContext{})
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding header %q: %w", k, err)
		}
	}
	if len(sj.Headers) == 0 {
		headers = nil
	}

	return MCPServerConfig{
		Name:    name,
		Type:    typ,
		Command: command,
		Args:    args,
		Env:     env,
		URL:     url,
		Headers: headers,
	}, nil
}
```

### Step 1.6: Update the existing `TestExpandEnvVars` test to call `expandVars`

- [ ] In `agent/mcp_config_test.go`, replace the body of `TestExpandEnvVars` so it calls the new function (keeping the old test name for now is fine; we'll fold it into the new table in Task 3):

```go
func TestExpandEnvVars(t *testing.T) {
	t.Setenv("TEST_MCP_VAR", "hello")

	tests := []struct {
		input   string
		want    string
		wantErr bool
	}{
		{"no vars here", "no vars here", false},
		{"${TEST_MCP_VAR}", "hello", false},
		{"prefix-${TEST_MCP_VAR}-suffix", "prefix-hello-suffix", false},
		{"${TEST_MCP_VAR:-fallback}", "hello", false},
		{"${UNSET_VAR_12345:-default}", "default", false},
		{"${UNSET_VAR_12345}", "", true},
		{"${TEST_MCP_VAR:-}", "hello", false},
		{"${UNSET_VAR_12345:-}", "", false},
		{"multiple ${TEST_MCP_VAR} and ${TEST_MCP_VAR}", "multiple hello and hello", false},
	}

	for _, tt := range tests {
		got, err := expandVars(tt.input, expansionContext{})
		if tt.wantErr {
			if err == nil {
				t.Errorf("expandVars(%q): expected error, got %q", tt.input, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("expandVars(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("expandVars(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

### Step 1.7: Run the tests to verify they pass

Run: `go test ./agent/ -run "TestExpandVars_ZeroContext_MatchesExpandEnvVars|TestExpandEnvVars|TestLoadMCPConfigFile|TestExpandEnvVars_InConfigLoading|TestExpandEnvVars_MissingVarInConfig" -v`

Expected: PASS for all rows.

### Step 1.8: Run the full agent test suite to verify no regression

Run: `go test ./agent/...`

Expected: PASS.

### Step 1.9: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: rename expandEnvVars to expandVars with expansionContext (SP6)"
```

---

## Task 2: Thread `expansionContext` through `serverJSONToConfig` and the plugin loader

**Goal:** Add the `ctx expansionContext` parameter to `serverJSONToConfig` and `parseMCPServerMap` so plugin and top-level loaders can supply distinct contexts. Behavior remains identical (callers still pass `expansionContext{}`).

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/plugin.go`
- Modify: `agent/mcp_config_test.go`

### Step 2.1: Write the failing test that constructs a config with a non-zero context

- [ ] Append to `agent/mcp_config_test.go`:

```go
// TestServerJSONToConfig_AcceptsContext verifies serverJSONToConfig now
// takes an expansionContext parameter. The context is unused at this stage
// (later tasks make it functional); we only assert the signature accepts it.
func TestServerJSONToConfig_AcceptsContext(t *testing.T) {
	sj := mcpServerJSON{
		Type:    "stdio",
		Command: "echo",
		Args:    []string{"hi"},
	}
	cfg, err := serverJSONToConfig("svc", sj, expansionContext{ProjectDir: "/p"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Command != "echo" {
		t.Errorf("cmd = %q, want echo", cfg.Command)
	}
}
```

### Step 2.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestServerJSONToConfig_AcceptsContext -v`

Expected: FAIL with `too many arguments in call to serverJSONToConfig`.

### Step 2.3: Add the `ctx` parameter to `serverJSONToConfig`

- [ ] In `agent/mcp_config.go`, change the signature and pass `ctx` to each `expandVars` call:

```go
func serverJSONToConfig(name string, sj mcpServerJSON, ctx expansionContext) (MCPServerConfig, error) {
	typ := strings.TrimSpace(sj.Type)
	if typ == "" {
		typ = "stdio"
	}

	command, err := expandVars(sj.Command, ctx)
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding command: %w", err)
	}

	args := make([]string, len(sj.Args))
	for i, a := range sj.Args {
		args[i], err = expandVars(a, ctx)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding arg[%d]: %w", i, err)
		}
	}
	if len(sj.Args) == 0 {
		args = nil
	}

	env := make(map[string]string, len(sj.Env))
	for k, v := range sj.Env {
		env[k], err = expandVars(v, ctx)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding env %q: %w", k, err)
		}
	}
	if len(sj.Env) == 0 {
		env = nil
	}

	url, err := expandVars(sj.URL, ctx)
	if err != nil {
		return MCPServerConfig{}, fmt.Errorf("expanding url: %w", err)
	}

	headers := make(map[string]string, len(sj.Headers))
	for k, v := range sj.Headers {
		headers[k], err = expandVars(v, ctx)
		if err != nil {
			return MCPServerConfig{}, fmt.Errorf("expanding header %q: %w", k, err)
		}
	}
	if len(sj.Headers) == 0 {
		headers = nil
	}

	return MCPServerConfig{
		Name:    name,
		Type:    typ,
		Command: command,
		Args:    args,
		Env:     env,
		URL:     url,
		Headers: headers,
	}, nil
}
```

### Step 2.4: Update `LoadMCPConfigFile` to call `serverJSONToConfig` with an empty context

- [ ] In `agent/mcp_config.go`, in `LoadMCPConfigFile`, replace the `serverJSONToConfig(name, sj)` call with `serverJSONToConfig(name, sj, expansionContext{})`. (A later task replaces this with a context built from `resolveProjectDir`.)

```go
		cfg, err := serverJSONToConfig(name, sj, expansionContext{})
```

### Step 2.5: Update `parseMCPServerMap` in `agent/plugin.go` to thread the context

- [ ] In `agent/plugin.go`, change `parseMCPServerMap` to accept and pass through an `expansionContext`:

```go
// parseMCPServerMap converts a map of server names to raw JSON into
// MCPServerConfig slices. The source string is used for error context.
func parseMCPServerMap(servers map[string]json.RawMessage, source string, ctx expansionContext) ([]MCPServerConfig, error) {
	var configs []MCPServerConfig
	for name, raw := range servers {
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, source, err)
		}
		cfg, err := serverJSONToConfig(name, sj, ctx)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, source, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}
```

### Step 2.6: Update `loadPluginMCPFile` and `discoverPluginMCPConfigs` to pass `expansionContext{}` for now

The plugin context will be populated in Task 9 (PluginRoot/PluginData/UserConfig). For now, preserve current behavior by passing an empty context to `parseMCPServerMap` and leaving the literal `expandPluginRoot` in place.

- [ ] In `agent/plugin.go`, update both call sites:

```go
func loadPluginMCPFile(path, pluginDir string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	// Expand ${CLAUDE_PLUGIN_ROOT} before env-var expansion so
	// expandVars (called by serverJSONToConfig) doesn't fail on it.
	expanded := expandPluginRoot(string(data), pluginDir)

	var cf struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(expanded), &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	return parseMCPServerMap(cf.MCPServers, path, expansionContext{})
}
```

And inside `discoverPluginMCPConfigs`:

```go
	// Layer 2: Inline mcpServers from the manifest.
	if len(manifestMCPServers) > 0 {
		expanded := expandPluginRoot(string(manifestMCPServers), pluginDir)
		var servers map[string]json.RawMessage
		if err := json.Unmarshal([]byte(expanded), &servers); err == nil && len(servers) > 0 {
			inlineConfigs, err := parseMCPServerMap(servers, "inline", expansionContext{})
			if err != nil {
				return nil, err
			}
			layers = append(layers, inlineConfigs)
		}
	}
```

### Step 2.7: Run the tests

Run: `go test ./agent/...`

Expected: PASS (including the new `TestServerJSONToConfig_AcceptsContext`).

### Step 2.8: Commit

```bash
git add agent/mcp_config.go agent/plugin.go agent/mcp_config_test.go
git commit -m "agent: thread expansionContext through MCP loaders (SP6)"
```

---

## Task 3: Resolve `${CLAUDE_PROJECT_DIR}` from context

**Goal:** When `expandVars` encounters `${CLAUDE_PROJECT_DIR}`, return `ctx.ProjectDir` instead of falling through to `os.LookupEnv`. Empty context value falls through to OS env, and if still unset, errors per existing format. Context wins when both are set.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 3.1: Write the failing test for context-supplied CLAUDE_PROJECT_DIR

- [ ] Append to `agent/mcp_config_test.go`:

```go
// TestExpandVars_ProjectDir covers all branches of ${CLAUDE_PROJECT_DIR}
// resolution from context.
func TestExpandVars_ProjectDir(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		ctx         expansionContext
		osEnvKey    string // if non-empty, set this OS env var
		osEnvVal    string
		want        string
		wantErrSub  string // substring; "" means expect success
	}{
		{
			name:  "context provides value",
			input: "${CLAUDE_PROJECT_DIR}",
			ctx:   expansionContext{ProjectDir: "/x"},
			want:  "/x",
		},
		{
			name:     "context wins over OS env",
			input:    "${CLAUDE_PROJECT_DIR}",
			ctx:      expansionContext{ProjectDir: "/x"},
			osEnvKey: "CLAUDE_PROJECT_DIR",
			osEnvVal: "/shell",
			want:     "/x",
		},
		{
			name:       "context empty, OS env unset, no default → error",
			input:      "${CLAUDE_PROJECT_DIR}",
			ctx:        expansionContext{},
			wantErrSub: `"CLAUDE_PROJECT_DIR" is not set`,
		},
		{
			name:  "context empty, default supplied",
			input: "${CLAUDE_PROJECT_DIR:-.}",
			ctx:   expansionContext{},
			want:  ".",
		},
		{
			name:  "literal mixed with project dir",
			input: "prefix-${CLAUDE_PROJECT_DIR}-suffix",
			ctx:   expansionContext{ProjectDir: "p"},
			want:  "prefix-p-suffix",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Ensure no stray CLAUDE_PROJECT_DIR from the parent process.
			t.Setenv("CLAUDE_PROJECT_DIR", "")
			if err := os.Unsetenv("CLAUDE_PROJECT_DIR"); err != nil {
				t.Fatal(err)
			}
			if tt.osEnvKey != "" {
				t.Setenv(tt.osEnvKey, tt.osEnvVal)
			}
			got, err := expandVars(tt.input, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil (output=%q)", tt.wantErrSub, got)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
```

### Step 3.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestExpandVars_ProjectDir -v`

Expected: FAIL — for the "context provides value" case the function currently calls `os.LookupEnv("CLAUDE_PROJECT_DIR")` and either errors or returns the OS value.

### Step 3.3: Add `${CLAUDE_PROJECT_DIR}` branch to `expandVars`

- [ ] In `agent/mcp_config.go`, replace the resolution block inside `expandVars` (after `varName`/`defaultVal` parsing) with a switch that handles the reserved name first:

```go
		var val string
		var ok bool
		switch {
		case varName == "CLAUDE_PROJECT_DIR":
			if ctx.ProjectDir != "" {
				val, ok = ctx.ProjectDir, true
			}
		}
		if !ok {
			val, ok = os.LookupEnv(varName)
		}
		if !ok {
			if !hasDefault {
				return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
			}
			val = defaultVal
		}
		b.WriteString(val)
```

The full `expandVars` body now looks like:

```go
func expandVars(s string, ctx expansionContext) (string, error) {
	var b strings.Builder
	i := 0
	for i < len(s) {
		idx := strings.Index(s[i:], "${")
		if idx < 0 {
			b.WriteString(s[i:])
			break
		}
		b.WriteString(s[i : i+idx])
		i += idx + 2

		end := strings.Index(s[i:], "}")
		if end < 0 {
			b.WriteString("${")
			continue
		}

		expr := s[i : i+end]
		i += end + 1

		varName := expr
		defaultVal := ""
		hasDefault := false
		if di := strings.Index(expr, ":-"); di >= 0 {
			varName = expr[:di]
			defaultVal = expr[di+2:]
			hasDefault = true
		}

		var val string
		var ok bool
		switch {
		case varName == "CLAUDE_PROJECT_DIR":
			if ctx.ProjectDir != "" {
				val, ok = ctx.ProjectDir, true
			}
		}
		if !ok {
			val, ok = os.LookupEnv(varName)
		}
		if !ok {
			if !hasDefault {
				return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
			}
			val = defaultVal
		}
		b.WriteString(val)
	}
	return b.String(), nil
}
```

Note: the "context wins over OS env" rule comes from checking context **before** `os.LookupEnv`. The test in 3.1 verifies this.

### Step 3.4: Run the test to verify it passes

Run: `go test ./agent/ -run TestExpandVars_ProjectDir -v`

Expected: PASS for all subtests.

### Step 3.5: Run full suite to verify no regression

Run: `go test ./agent/...`

Expected: PASS.

### Step 3.6: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: resolve \${CLAUDE_PROJECT_DIR} from expansionContext (SP6)"
```

---

## Task 4: Resolve `${CLAUDE_PLUGIN_ROOT}` and `${CLAUDE_PLUGIN_DATA}` from context

**Goal:** Extend `expandVars` to resolve `CLAUDE_PLUGIN_ROOT` and `CLAUDE_PLUGIN_DATA` from context fields. Same semantics as project dir: context wins; empty context falls through to OS env; still unset errors out unless a `:-default` is supplied.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 4.1: Write the failing tests for plugin root and data

- [ ] Append to `agent/mcp_config_test.go`:

```go
func TestExpandVars_PluginRoot(t *testing.T) {
	if err := os.Unsetenv("CLAUDE_PLUGIN_ROOT"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		input      string
		ctx        expansionContext
		want       string
		wantErrSub string
	}{
		{"context provides value", "${CLAUDE_PLUGIN_ROOT}", expansionContext{PluginRoot: "/p"}, "/p", ""},
		{"empty context errors", "${CLAUDE_PLUGIN_ROOT}", expansionContext{}, "", `"CLAUDE_PLUGIN_ROOT" is not set`},
		{"default kicks in", "${CLAUDE_PLUGIN_ROOT:-/d}", expansionContext{}, "/d", ""},
		{"mixed with project dir", "${CLAUDE_PLUGIN_ROOT}/bin/${CLAUDE_PROJECT_DIR}", expansionContext{PluginRoot: "/p", ProjectDir: "/proj"}, "/p/bin//proj", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandVars(tt.input, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("got err=%v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExpandVars_PluginData(t *testing.T) {
	if err := os.Unsetenv("CLAUDE_PLUGIN_DATA"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		input      string
		ctx        expansionContext
		want       string
		wantErrSub string
	}{
		{"context provides value", "${CLAUDE_PLUGIN_DATA}", expansionContext{PluginData: "/d"}, "/d", ""},
		{"empty context errors", "${CLAUDE_PLUGIN_DATA}", expansionContext{}, "", `"CLAUDE_PLUGIN_DATA" is not set`},
		{"default kicks in", "${CLAUDE_PLUGIN_DATA:-/x}", expansionContext{}, "/x", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandVars(tt.input, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("got err=%v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
```

### Step 4.2: Run the tests to verify they fail

Run: `go test ./agent/ -run "TestExpandVars_PluginRoot|TestExpandVars_PluginData" -v`

Expected: FAIL — context-supplied values are ignored; `os.LookupEnv` returns nothing.

### Step 4.3: Add plugin-root and plugin-data branches to the switch

- [ ] In `agent/mcp_config.go`, extend the switch inside `expandVars`:

```go
		var val string
		var ok bool
		switch {
		case varName == "CLAUDE_PROJECT_DIR":
			if ctx.ProjectDir != "" {
				val, ok = ctx.ProjectDir, true
			}
		case varName == "CLAUDE_PLUGIN_ROOT":
			if ctx.PluginRoot != "" {
				val, ok = ctx.PluginRoot, true
			}
		case varName == "CLAUDE_PLUGIN_DATA":
			if ctx.PluginData != "" {
				val, ok = ctx.PluginData, true
			}
		}
```

### Step 4.4: Run the tests to verify they pass

Run: `go test ./agent/ -run "TestExpandVars_PluginRoot|TestExpandVars_PluginData" -v`

Expected: PASS.

### Step 4.5: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 4.6: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: resolve \${CLAUDE_PLUGIN_ROOT} and \${CLAUDE_PLUGIN_DATA} from context (SP6)"
```

---

## Task 5: Resolve `${user_config.KEY}` via `userConfigLookup`

**Goal:** When `varName` starts with the literal prefix `user_config.`, the suffix is the key and the value comes from `ctx.UserConfig(key)`. If `ctx.UserConfig == nil` or the lookup returns `ok=false`, treat as unset and apply the existing default/error logic. Error text must preserve the full dotted placeholder name.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 5.1: Write the failing test for `${user_config.*}` resolution

- [ ] Append to `agent/mcp_config_test.go`:

```go
func TestExpandVars_UserConfig(t *testing.T) {
	lookup := func(values map[string]string) userConfigLookup {
		return func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		}
	}

	tests := []struct {
		name       string
		input      string
		ctx        expansionContext
		want       string
		wantErrSub string
	}{
		{
			name:  "lookup returns value",
			input: "${user_config.K}",
			ctx:   expansionContext{UserConfig: lookup(map[string]string{"K": "v"})},
			want:  "v",
		},
		{
			name:       "lookup returns false → error preserves dotted name",
			input:      "${user_config.MISSING}",
			ctx:        expansionContext{UserConfig: lookup(map[string]string{})},
			wantErrSub: `"user_config.MISSING" is not set`,
		},
		{
			name:       "nil lookup → error preserves dotted name",
			input:      "${user_config.K}",
			ctx:        expansionContext{},
			wantErrSub: `"user_config.K" is not set`,
		},
		{
			name:  "nil lookup, default supplied",
			input: "${user_config.K:-fallback}",
			ctx:   expansionContext{},
			want:  "fallback",
		},
		{
			name:  "lookup returns false, default supplied",
			input: "${user_config.K:-fallback}",
			ctx:   expansionContext{UserConfig: lookup(map[string]string{})},
			want:  "fallback",
		},
		{
			name:  "key contains dots after prefix",
			input: "${user_config.deep.path}",
			ctx:   expansionContext{UserConfig: lookup(map[string]string{"deep.path": "vv"})},
			want:  "vv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandVars(tt.input, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("got err=%v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
```

### Step 5.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestExpandVars_UserConfig -v`

Expected: FAIL — `user_config.K` is treated as an OS env var name today.

### Step 5.3: Add the `user_config.` prefix branch to the switch

- [ ] In `agent/mcp_config.go`, extend the switch inside `expandVars` with the prefix check. The full switch now reads:

```go
		var val string
		var ok bool
		const userConfigPrefix = "user_config."
		switch {
		case varName == "CLAUDE_PROJECT_DIR":
			if ctx.ProjectDir != "" {
				val, ok = ctx.ProjectDir, true
			}
		case varName == "CLAUDE_PLUGIN_ROOT":
			if ctx.PluginRoot != "" {
				val, ok = ctx.PluginRoot, true
			}
		case varName == "CLAUDE_PLUGIN_DATA":
			if ctx.PluginData != "" {
				val, ok = ctx.PluginData, true
			}
		case strings.HasPrefix(varName, userConfigPrefix):
			// user_config.* never falls through to OS env. If the context
			// has no lookup or the lookup returns false, the only way to
			// recover is a :-default.
			key := varName[len(userConfigPrefix):]
			if ctx.UserConfig != nil {
				val, ok = ctx.UserConfig(key)
			}
			if !ok {
				if !hasDefault {
					return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
				}
				val = defaultVal
				ok = true
			}
		}
		if !ok {
			val, ok = os.LookupEnv(varName)
		}
		if !ok {
			if !hasDefault {
				return "", fmt.Errorf("environment variable %q is not set (use ${%s:-default} to provide a default)", varName, varName)
			}
			val = defaultVal
		}
		b.WriteString(val)
```

The `user_config.*` branch is special because it must **not** fall through to OS env — the spec §5.4 requires that references in non-plugin scope error out (`UserConfig == nil`) rather than silently match an OS env var named `user_config.K`.

### Step 5.4: Run the test to verify it passes

Run: `go test ./agent/ -run TestExpandVars_UserConfig -v`

Expected: PASS for all subtests.

### Step 5.5: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 5.6: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: resolve \${user_config.KEY} via userConfigLookup (SP6)"
```

---

## Task 6: Collapse `streamable-http` alias to `http`

**Goal:** When `mcpServerJSON.Type` equals exactly `"streamable-http"` (after trimming whitespace), normalize it to `"http"` in `serverJSONToConfig`. Case-sensitive: `Streamable-HTTP`, `streamableHttp`, `streamable_http` continue to fail with the existing `unknown MCP transport type` error and the original (un-normalized) spelling.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 6.1: Write the failing tests

- [ ] Append to `agent/mcp_config_test.go`:

```go
func TestLoadMCPConfigFile_StreamableHTTPAlias(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{
		"mcpServers": {
			"x": {
				"type": "streamable-http",
				"url": "https://e.test/mcp"
			}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	configs, err := LoadMCPConfigFile(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	if configs[0].Type != "http" {
		t.Errorf("type = %q, want http (alias collapsed)", configs[0].Type)
	}
	if configs[0].URL != "https://e.test/mcp" {
		t.Errorf("url = %q", configs[0].URL)
	}
}

func TestLoadMCPConfigFile_StreamableHTTPAlias_CaseSensitive(t *testing.T) {
	tests := []struct {
		name      string
		typeValue string
	}{
		{"capitalized", "Streamable-HTTP"},
		{"all caps", "STREAMABLE-HTTP"},
		{"camel", "streamableHttp"},
		{"underscore", "streamable_http"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mcp.json")
			body := `{"mcpServers":{"x":{"type":"` + tt.typeValue + `","url":"https://e.test"}}}`
			if err := os.WriteFile(path, []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadMCPConfigFile(path)
			if err == nil {
				t.Fatalf("expected error for type %q", tt.typeValue)
			}
			if !strings.Contains(err.Error(), tt.typeValue) {
				t.Errorf("error %q does not preserve original spelling %q", err.Error(), tt.typeValue)
			}
			if !strings.Contains(err.Error(), "unknown MCP transport type") {
				t.Errorf("error %q does not contain expected substring", err.Error())
			}
		})
	}
}
```

### Step 6.2: Run the tests to verify they fail

Run: `go test ./agent/ -run "TestLoadMCPConfigFile_StreamableHTTPAlias" -v`

Expected: FAIL — `streamable-http` currently parses through to `serverJSONToConfig` with `Type="streamable-http"`, then `transportForConfig` returns `unknown MCP transport type`. (Both the success case and the case-sensitivity case may fail today depending on which path reports the error — verify the error format too.)

### Step 6.3: Collapse the alias in `serverJSONToConfig`

- [ ] In `agent/mcp_config.go`, in `serverJSONToConfig`, just after the existing default-to-`"stdio"` line, add the alias collapse:

```go
func serverJSONToConfig(name string, sj mcpServerJSON, ctx expansionContext) (MCPServerConfig, error) {
	typ := strings.TrimSpace(sj.Type)
	if typ == "" {
		typ = "stdio"
	}
	if typ == "streamable-http" {
		typ = "http"
	}
	// ... rest unchanged
```

The error for an unrecognized type continues to flow through `transportForConfig`, which uses `cfg.Type` — but the test asserts the original spelling is preserved. The existing `transportForConfig` error reads:

```go
return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
```

…which will quote the post-trim value. For the case-sensitive test rows (`Streamable-HTTP`, `streamableHttp`, etc.) the trimmed value is identical to the user-supplied value, so the existing error preserves it. No change needed in `transportForConfig`.

### Step 6.4: Run the tests to verify they pass

Run: `go test ./agent/ -run "TestLoadMCPConfigFile_StreamableHTTPAlias" -v`

Expected: PASS.

### Step 6.5: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 6.6: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: collapse streamable-http alias to http (SP6)"
```

---

## Task 7: Add `resolveProjectDir` helper

**Goal:** Add the helper that picks the value for `CLAUDE_PROJECT_DIR`. Precedence: session override (via duck-typed `ProjectDir() string` method on the env) → git root via `gitRootOrEmpty` → cwd. `env == nil` falls back to `os.Getwd()`; if even that fails, return `""`.

**Files:**
- Modify: `agent/mcp_config.go`
- Modify: `agent/mcp_config_test.go`

### Step 7.1: Write the failing test

- [ ] Append to `agent/mcp_config_test.go`:

```go
// envWithProjectDir is a test ExecutionEnvironment that adds the
// ProjectDir() string method resolveProjectDir duck-types for.
type envWithProjectDir struct {
	*fakeEnvForMCP
	projectDir string
}

func (e *envWithProjectDir) ProjectDir() string { return e.projectDir }

func TestResolveProjectDir(t *testing.T) {
	tmp := t.TempDir()
	// Initialize a real git repo so gitRootOrEmpty returns something.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	cmd := exec.Command("git", "init", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	// Resolve symlinks so we compare against the same path gitRootOrEmpty
	// would return.
	resolvedTmp, err := filepath.EvalSymlinks(tmp)
	if err != nil {
		t.Fatal(err)
	}

	// 1. env returns cwd inside the git repo → git root.
	envInRepo := &fakeEnvForMCP{workDir: resolvedTmp, gitRoot: resolvedTmp}
	if got := resolveProjectDir(envInRepo); got != resolvedTmp {
		t.Errorf("inside repo: got %q, want %q", got, resolvedTmp)
	}

	// 2. env returns cwd outside any git repo → cwd.
	nonRepoDir := t.TempDir()
	resolvedNonRepo, _ := filepath.EvalSymlinks(nonRepoDir)
	envOutside := &fakeEnvForMCP{workDir: resolvedNonRepo, gitRoot: ""}
	if got := resolveProjectDir(envOutside); got != resolvedNonRepo {
		t.Errorf("outside repo: got %q, want %q", got, resolvedNonRepo)
	}

	// 3. env is nil → os.Getwd value.
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if got := resolveProjectDir(nil); got != wd {
		t.Errorf("nil env: got %q, want %q", got, wd)
	}

	// 4. env implements ProjectDir() and returns non-empty → that value.
	forced := &envWithProjectDir{
		fakeEnvForMCP: &fakeEnvForMCP{workDir: resolvedTmp, gitRoot: resolvedTmp},
		projectDir:    "/forced/path",
	}
	if got := resolveProjectDir(forced); got != "/forced/path" {
		t.Errorf("forced env: got %q, want /forced/path", got)
	}

	// 5. env implements ProjectDir() but returns "" → falls through to git root.
	empty := &envWithProjectDir{
		fakeEnvForMCP: &fakeEnvForMCP{workDir: resolvedTmp, gitRoot: resolvedTmp},
		projectDir:    "",
	}
	if got := resolveProjectDir(empty); got != resolvedTmp {
		t.Errorf("empty override: got %q, want %q", got, resolvedTmp)
	}
}
```

Add `os/exec` to the test file's import block if missing.

### Step 7.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestResolveProjectDir -v`

Expected: FAIL with `undefined: resolveProjectDir`.

### Step 7.3: Implement `resolveProjectDir`

- [ ] In `agent/mcp_config.go`, add the function near the bottom of the file (after `globalMCPConfigPath`):

```go
// resolveProjectDir picks the value substituted for ${CLAUDE_PROJECT_DIR}
// and injected as CLAUDE_PROJECT_DIR into stdio MCP servers. Precedence:
// session project root (via duck-typed ProjectDir() string method on env) →
// git root → cwd. env may be nil; the function then falls back to
// os.Getwd() and skips the git-root probe. If os.Getwd also fails, returns "".
func resolveProjectDir(env ExecutionEnvironment) string {
	if env != nil {
		if override, ok := env.(interface{ ProjectDir() string }); ok {
			if v := override.ProjectDir(); v != "" {
				return v
			}
		}
		cwd := env.WorkingDirectory()
		if root := gitRootOrEmpty(env, cwd); root != "" {
			return root
		}
		return cwd
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}
```

### Step 7.4: Run the test to verify it passes

Run: `go test ./agent/ -run TestResolveProjectDir -v`

Expected: PASS.

### Step 7.5: Wire `LoadMCPConfigFile` and `DiscoverMCPConfigs` to seed `expansionContext.ProjectDir`

- [ ] In `agent/mcp_config.go`, change `LoadMCPConfigFile` to construct a context from `resolveProjectDir(nil)`:

```go
func LoadMCPConfigFile(path string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading MCP config %s: %w", path, err)
	}

	var cf mcpConfigFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	ctx := expansionContext{ProjectDir: resolveProjectDir(nil)}
	var configs []MCPServerConfig
	for name, raw := range cf.MCPServers {
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, path, err)
		}

		cfg, err := serverJSONToConfig(name, sj, ctx)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, path, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}
```

- [ ] Add an unexported sibling for callers that want to thread their own context (used by `DiscoverMCPConfigs`):

```go
// loadMCPConfigFileWithContext is like LoadMCPConfigFile but uses the
// caller's expansionContext. Used by DiscoverMCPConfigs so the project-dir
// resolution honors the session's ExecutionEnvironment.
func loadMCPConfigFileWithContext(path string, ctx expansionContext) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading MCP config %s: %w", path, err)
	}

	var cf mcpConfigFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	var configs []MCPServerConfig
	for name, raw := range cf.MCPServers {
		var sj mcpServerJSON
		if err := json.Unmarshal(raw, &sj); err != nil {
			return nil, fmt.Errorf("parsing MCP server %q in %s: %w", name, path, err)
		}

		cfg, err := serverJSONToConfig(name, sj, ctx)
		if err != nil {
			return nil, fmt.Errorf("MCP server %q in %s: %w", name, path, err)
		}
		configs = append(configs, cfg)
	}
	return configs, nil
}
```

- [ ] Modify `DiscoverMCPConfigs` to resolve project dir once and pass it through:

```go
func DiscoverMCPConfigs(env ExecutionEnvironment, extraFiles, inlineSpecs []string) ([]MCPServerConfig, error) {
	var layers [][]MCPServerConfig
	ctx := expansionContext{ProjectDir: resolveProjectDir(env)}

	// Layer 1: Global config.
	globalPath := globalMCPConfigPath()
	if globalPath != "" {
		if configs, err := loadMCPConfigFileWithContext(globalPath, ctx); err == nil {
			layers = append(layers, configs)
		}
	}

	// Layer 2: Per-project config.
	if env != nil {
		cwd := env.WorkingDirectory()
		root := gitRootOrEmpty(env, cwd)
		if root != "" {
			projPath := filepath.Join(root, ".serf", "mcp.json")
			if configs, err := loadMCPConfigFileWithContext(projPath, ctx); err == nil {
				layers = append(layers, configs)
			}
		}
	}

	// Layer 3: CLI config files.
	for _, path := range extraFiles {
		configs, err := loadMCPConfigFileWithContext(path, ctx)
		if err != nil {
			return nil, fmt.Errorf("--mcp-config %s: %w", path, err)
		}
		layers = append(layers, configs)
	}

	// Layer 4: CLI inline specs.
	for _, spec := range inlineSpecs {
		cfg, err := ParseMCPInline(spec)
		if err != nil {
			return nil, fmt.Errorf("--mcp %q: %w", spec, err)
		}
		layers = append(layers, []MCPServerConfig{cfg})
	}

	return MergeMCPConfigs(layers...), nil
}
```

### Step 7.6: Write a test confirming `DiscoverMCPConfigs` threads project dir into expansion

- [ ] Append to `agent/mcp_config_test.go`:

```go
func TestDiscoverMCPConfigs_ExpandsProjectDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	projDir := t.TempDir()
	resolvedProj, _ := filepath.EvalSymlinks(projDir)
	if err := os.MkdirAll(filepath.Join(resolvedProj, ".serf"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(resolvedProj, ".serf", "mcp.json"), []byte(`{
		"mcpServers": {
			"x": {"command": "${CLAUDE_PROJECT_DIR}/bin/x"}
		}
	}`), 0644); err != nil {
		t.Fatal(err)
	}

	env := &fakeEnvForMCP{workDir: resolvedProj, gitRoot: resolvedProj}
	configs, err := DiscoverMCPConfigs(env, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(configs) != 1 {
		t.Fatalf("expected 1 config, got %d", len(configs))
	}
	want := resolvedProj + "/bin/x"
	if configs[0].Command != want {
		t.Errorf("command = %q, want %q", configs[0].Command, want)
	}
}
```

### Step 7.7: Run the tests to verify they pass

Run: `go test ./agent/ -run "TestResolveProjectDir|TestDiscoverMCPConfigs_ExpandsProjectDir" -v`

Expected: PASS.

### Step 7.8: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 7.9: Commit

```bash
git add agent/mcp_config.go agent/mcp_config_test.go
git commit -m "agent: add resolveProjectDir helper and thread into Discover/LoadMCPConfigFile (SP6)"
```

---

## Task 8: Add `projectDir` parameter to `transportForConfig` and inject `CLAUDE_PROJECT_DIR`

**Goal:** Inject `CLAUDE_PROJECT_DIR` into the spawned env of stdio MCP servers. `cfg.Env` wins over the auto-injection. HTTP/SSE transports are unaffected. When both `cfg.Env` is empty *and* `projectDir == ""`, the call path skips `mergeEnv` entirely so `cmd.Env` stays nil (inherit parent).

**Files:**
- Modify: `agent/mcp_manager.go`
- Modify: `agent/mcp_manager_test.go`
- Modify: `agent/session.go`

### Step 8.1: Write the failing test for env injection (unit-level)

The integration spec calls for a real spawn, but that needs a stub MCP server. For the unit-level test we exercise the env-merge path directly by reading the `*exec.Cmd` the stdio transport wraps. The `mcp.CommandTransport.Command` field is exported, so the test can inspect `cmd.Env` without spawning.

- [ ] Append to `agent/mcp_manager_test.go`:

```go
// TestTransportForConfig_InjectsProjectDir verifies that stdio transports
// receive CLAUDE_PROJECT_DIR in their spawned env when projectDir is
// non-empty, and that cfg.Env entries take precedence over the auto-
// injection.
func TestTransportForConfig_InjectsProjectDir(t *testing.T) {
	tests := []struct {
		name       string
		cfg        MCPServerConfig
		projectDir string
		wantKV     map[string]string // keys that must be present with these exact values
		wantUnset  []string          // keys that must NOT appear in cmd.Env
		expectNilEnv bool            // true if cmd.Env must be nil (inherit parent)
	}{
		{
			name:         "stdio + projectDir empty + cfg.Env empty → inherit parent",
			cfg:          MCPServerConfig{Type: "stdio", Command: "true"},
			projectDir:   "",
			expectNilEnv: true,
		},
		{
			name:       "stdio + projectDir set → injected",
			cfg:        MCPServerConfig{Type: "stdio", Command: "true"},
			projectDir: "/proj",
			wantKV:     map[string]string{"CLAUDE_PROJECT_DIR": "/proj"},
		},
		{
			name:       "stdio + cfg.Env overrides injection",
			cfg:        MCPServerConfig{Type: "stdio", Command: "true", Env: map[string]string{"CLAUDE_PROJECT_DIR": "/explicit"}},
			projectDir: "/proj",
			wantKV:     map[string]string{"CLAUDE_PROJECT_DIR": "/explicit"},
		},
		{
			name:       "stdio + projectDir empty + cfg.Env has unrelated → merged, no project dir",
			cfg:        MCPServerConfig{Type: "stdio", Command: "true", Env: map[string]string{"X": "y"}},
			projectDir: "",
			wantKV:     map[string]string{"X": "y"},
			wantUnset:  []string{"CLAUDE_PROJECT_DIR"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, err := transportForConfig(tt.cfg, tt.projectDir)
			if err != nil {
				t.Fatalf("transportForConfig: %v", err)
			}
			cmdTransport, ok := tr.(*mcp.CommandTransport)
			if !ok {
				t.Fatalf("expected *mcp.CommandTransport, got %T", tr)
			}
			env := cmdTransport.Command.Env

			if tt.expectNilEnv {
				if env != nil {
					t.Errorf("expected nil env (inherit parent), got %v", env)
				}
				return
			}
			byKey := map[string]string{}
			for _, kv := range env {
				k, v, _ := strings.Cut(kv, "=")
				byKey[k] = v
			}
			for k, want := range tt.wantKV {
				if got := byKey[k]; got != want {
					t.Errorf("env[%q] = %q, want %q", k, got, want)
				}
			}
			for _, k := range tt.wantUnset {
				// Unset means the key from the parent process must not have
				// been merged in by us. But mergeEnv inherits os.Environ(),
				// so the key could be present from the parent shell. We
				// only assert the key is not equal to a value we would have
				// injected — i.e. it must not equal the empty-projectDir
				// sentinel, which is just "we did not auto-inject."
				// Concretely: if projectDir is empty we never write a
				// CLAUDE_PROJECT_DIR entry of our own — but a parent shell
				// may have set one. To make this assertion deterministic,
				// scope the test env:
				_ = k
			}
		})
	}
}
```

Note the comment about `wantUnset`: parent-shell pollution makes it brittle to assert absence. We rely instead on the explicit value assertions plus the `expectNilEnv` branch.

### Step 8.2: Run the test to verify it fails

Run: `go test ./agent/ -run TestTransportForConfig_InjectsProjectDir -v`

Expected: FAIL with `too many arguments in call to transportForConfig` (current signature is one-arg).

### Step 8.3: Add the `projectDir` parameter and the injection logic

- [ ] In `agent/mcp_manager.go`, replace `transportForConfig`'s signature and stdio arm:

```go
// transportForConfig creates the appropriate MCP transport for a config.
// projectDir is the value to inject as CLAUDE_PROJECT_DIR into stdio
// transports' env. It is ignored for sse and http transports.
func transportForConfig(cfg MCPServerConfig, projectDir string) (mcp.Transport, error) {
	switch cfg.Type {
	case "stdio", "":
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio transport requires a command")
		}
		cmd := exec.Command(cfg.Command, cfg.Args...)
		// Inject CLAUDE_PROJECT_DIR, then layer cfg.Env on top so explicit
		// user-supplied env wins. Skip mergeEnv entirely when neither side
		// contributes a key, preserving today's "inherit parent" default.
		extra := map[string]string{}
		if projectDir != "" {
			extra["CLAUDE_PROJECT_DIR"] = projectDir
		}
		for k, v := range cfg.Env {
			extra[k] = v
		}
		if len(extra) > 0 {
			cmd.Env = mergeEnv(extra)
		}
		return &mcp.CommandTransport{Command: cmd}, nil

	case "sse":
		if cfg.URL == "" {
			return nil, fmt.Errorf("sse transport requires a url")
		}
		t := &mcp.SSEClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	case "http":
		if cfg.URL == "" {
			return nil, fmt.Errorf("http transport requires a url")
		}
		t := &mcp.StreamableClientTransport{Endpoint: cfg.URL}
		if len(cfg.Headers) > 0 {
			t.HTTPClient = httpClientWithHeaders(cfg.Headers)
		}
		return t, nil

	default:
		return nil, fmt.Errorf("unknown MCP transport type %q", cfg.Type)
	}
}
```

### Step 8.4: Add a `projectDir` parameter to `NewMCPManager`

- [ ] In `agent/mcp_manager.go`, change `NewMCPManager`'s signature and pass `projectDir` through to `transportForConfig`:

```go
// NewMCPManager connects to all configured MCP servers, discovers their tools,
// and namespaces them. The transports parameter is optional: when nil, transports
// are created from configs. When provided (for testing), each transport[i]
// corresponds to configs[i]. projectDir is injected as CLAUDE_PROJECT_DIR
// into stdio transports; pass "" to skip injection.
func NewMCPManager(ctx context.Context, configs []MCPServerConfig, transports []mcp.Transport, projectDir string) (*MCPManager, error) {
	if len(configs) == 0 {
		return nil, nil
	}

	client := mcp.NewClient(&mcp.Implementation{
		Name:    "serf",
		Version: "v1",
	}, nil)

	mgr := &MCPManager{}
	for i, cfg := range configs {
		var transport mcp.Transport
		if transports != nil && i < len(transports) {
			transport = transports[i]
		} else {
			var err error
			transport, err = transportForConfig(cfg, projectDir)
			if err != nil {
				mgr.Close()
				return nil, fmt.Errorf("MCP server %q: %w", cfg.Name, err)
			}
		}

		session, err := client.Connect(ctx, transport, nil)
		if err != nil {
			mgr.Close()
			return nil, fmt.Errorf("MCP server %q connect: %w", cfg.Name, err)
		}

		result, err := session.ListTools(ctx, nil)
		if err != nil {
			_ = session.Close()
			mgr.Close()
			return nil, fmt.Errorf("MCP server %q list tools: %w", cfg.Name, err)
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

		mgr.conns = append(mgr.conns, mcpConn{
			name:      cfg.Name,
			session:   session,
			tools:     tools,
			origNames: origNames,
		})
	}

	return mgr, nil
}
```

### Step 8.5: Update the `session.go` caller

- [ ] In `agent/session.go` (around line 2847), change the `NewMCPManager` call to pass `resolveProjectDir(s.env)`:

```go
	mgr, err := NewMCPManager(ctx, configs, nil, resolveProjectDir(s.env))
```

### Step 8.6: Update existing callers in `mcp_manager_test.go` and `mcp_real_test.go`

- [ ] In `agent/mcp_real_test.go`, in `newEverythingManager`, update the `NewMCPManager` call:

```go
	mgr, err := NewMCPManager(ctx, []MCPServerConfig{{
		Name:    "everything",
		Type:    "stdio",
		Command: "npx",
		Args:    []string{"-y", "@modelcontextprotocol/server-everything"},
	}}, nil, "")
```

- [ ] In `agent/mcp_manager_test.go`, find every existing `NewMCPManager(...)` call and add a trailing `""` argument. (Search and update all occurrences.)

- [ ] In `agent/mcp_integration_test.go`, do the same: add `""` to every `NewMCPManager` call.

### Step 8.7: Run the unit test

Run: `go test ./agent/ -run TestTransportForConfig_InjectsProjectDir -v`

Expected: PASS.

### Step 8.8: Run full suite

Run: `go test ./agent/...`

Expected: PASS. If any caller of `NewMCPManager` was missed, the build fails — find it and add the trailing `""`.

### Step 8.9: Add the real-spawn integration test (`TestMCPManager_InjectsProjectDir`)

- [ ] Append to `agent/mcp_manager_test.go`:

```go
// TestMCPManager_InjectsProjectDir spawns a real script as a stdio MCP
// server and verifies the script saw CLAUDE_PROJECT_DIR in its env. We do
// not require a full MCP handshake — the script captures the env to a
// sentinel file and exits. The MCP client will see a closed pipe and
// return an error from Connect, which we ignore.
func TestMCPManager_InjectsProjectDir(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "captured.txt")
	script := filepath.Join(tmp, "fake-mcp.sh")
	body := "#!/bin/bash\nprintf '%s' \"$CLAUDE_PROJECT_DIR\" > " + sentinel + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr, _ := NewMCPManager(ctx, []MCPServerConfig{{
		Name:    "fake",
		Type:    "stdio",
		Command: script,
	}}, nil, "/injected/path")
	if mgr != nil {
		mgr.Close()
	}

	// Give the spawned process a moment to write the sentinel.
	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(sentinel)
		if err == nil {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "/injected/path" {
		t.Errorf("sentinel = %q, want %q", string(got), "/injected/path")
	}
}

// TestMCPManager_RespectsExplicitProjectDirOverride verifies that an
// explicit cfg.Env entry for CLAUDE_PROJECT_DIR takes precedence over the
// projectDir argument passed to NewMCPManager.
func TestMCPManager_RespectsExplicitProjectDirOverride(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}

	tmp := t.TempDir()
	sentinel := filepath.Join(tmp, "captured.txt")
	script := filepath.Join(tmp, "fake-mcp.sh")
	body := "#!/bin/bash\nprintf '%s' \"$CLAUDE_PROJECT_DIR\" > " + sentinel + "\nexit 0\n"
	if err := os.WriteFile(script, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	mgr, _ := NewMCPManager(ctx, []MCPServerConfig{{
		Name:    "fake",
		Type:    "stdio",
		Command: script,
		Env:     map[string]string{"CLAUDE_PROJECT_DIR": "/explicit"},
	}}, nil, "/injected/path")
	if mgr != nil {
		mgr.Close()
	}

	deadline := time.Now().Add(2 * time.Second)
	var got []byte
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(sentinel)
		if err == nil {
			got = b
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "/explicit" {
		t.Errorf("sentinel = %q, want %q (cfg.Env should win)", string(got), "/explicit")
	}
}
```

Ensure these imports are present in `agent/mcp_manager_test.go`: `os`, `os/exec`, `path/filepath`, `time`.

### Step 8.10: Run the integration tests

Run: `go test ./agent/ -run "TestMCPManager_InjectsProjectDir|TestMCPManager_RespectsExplicitProjectDirOverride" -v`

Expected: PASS.

### Step 8.11: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 8.12: Commit

```bash
git add agent/mcp_manager.go agent/mcp_manager_test.go agent/mcp_real_test.go agent/mcp_integration_test.go agent/session.go
git commit -m "agent: inject CLAUDE_PROJECT_DIR into stdio MCP servers (SP6)"
```

---

## Task 9: Replace `expandPluginRoot` with `expandVars` in plugin loader

**Goal:** Stop pre-expanding `${CLAUDE_PLUGIN_ROOT}` via literal `strings.ReplaceAll`. Instead, pass a populated `expansionContext` (with `PluginRoot`, `PluginData`, and a `UserConfig` lookup that is a no-op for now) through `parseMCPServerMap` so `expandVars` handles all substitutions uniformly. Remove `expandPluginRoot` once tests pass.

The `UserConfig` lookup is supplied by SP7. Until SP7 lands, SP6 wires in a no-op `func(string) (string, bool) { return "", false }`. This is per the spec §6.

**Files:**
- Modify: `agent/plugin.go`
- Modify: `agent/plugin_test.go`

### Step 9.1: Write the failing regression test

- [ ] Append to `agent/plugin_test.go`:

```go
// TestPluginLoader_ExpandsPluginRoot_ViaExpandVars confirms that after SP6
// the plugin loader still substitutes ${CLAUDE_PLUGIN_ROOT} correctly — but
// now via expandVars rather than a string ReplaceAll. The behavior must be
// identical to pre-SP6.
func TestPluginLoader_ExpandsPluginRoot_ViaExpandVars(t *testing.T) {
	pluginDir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(pluginDir)

	if err := os.MkdirAll(filepath.Join(resolvedDir, ".claude-plugin"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{
		"name": "test-plugin",
		"mcpServers": {
			"db": {
				"command": "${CLAUDE_PLUGIN_ROOT}/bin/db"
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(resolvedDir, ".claude-plugin", "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}

	lp, err := LoadPlugin(resolvedDir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(lp.MCPConfigs) != 1 {
		t.Fatalf("expected 1 MCP config, got %d", len(lp.MCPConfigs))
	}
	want := resolvedDir + "/bin/db"
	if lp.MCPConfigs[0].Command != want {
		t.Errorf("command = %q, want %q", lp.MCPConfigs[0].Command, want)
	}
}

// TestPluginLoader_ExpandsPluginDataInFile confirms a .mcp.json file inside
// a plugin can reference ${CLAUDE_PLUGIN_DATA} once SP6 lands.
func TestPluginLoader_ExpandsPluginDataInFile(t *testing.T) {
	pluginDir := t.TempDir()
	resolvedDir, _ := filepath.EvalSymlinks(pluginDir)

	if err := os.MkdirAll(filepath.Join(resolvedDir, ".claude-plugin"), 0755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"test-plugin"}`
	if err := os.WriteFile(filepath.Join(resolvedDir, ".claude-plugin", "plugin.json"), []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	mcp := `{
		"mcpServers": {
			"db": {
				"command": "/bin/db",
				"args": ["--state", "${CLAUDE_PLUGIN_DATA}/db.sqlite"]
			}
		}
	}`
	if err := os.WriteFile(filepath.Join(resolvedDir, ".mcp.json"), []byte(mcp), 0644); err != nil {
		t.Fatal(err)
	}

	lp, err := LoadPlugin(resolvedDir)
	if err != nil {
		t.Fatalf("LoadPlugin: %v", err)
	}
	if len(lp.MCPConfigs) != 1 {
		t.Fatalf("expected 1 MCP config, got %d", len(lp.MCPConfigs))
	}
	got := lp.MCPConfigs[0].Args[1]
	wantSuffix := "/db.sqlite"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Errorf("args[1] = %q, want suffix %q", got, wantSuffix)
	}
	// PluginData must not be the literal placeholder.
	if strings.Contains(got, "${CLAUDE_PLUGIN_DATA}") {
		t.Errorf("args[1] still contains the unexpanded placeholder: %q", got)
	}
}
```

Add `path/filepath`, `os`, `strings` to the test file's imports if missing.

### Step 9.2: Run the tests to verify they fail

Run: `go test ./agent/ -run "TestPluginLoader_ExpandsPluginRoot_ViaExpandVars|TestPluginLoader_ExpandsPluginDataInFile" -v`

Expected: The first test may already pass (because `expandPluginRoot` does the ReplaceAll today). The second test FAILS because `${CLAUDE_PLUGIN_DATA}` is not handled — `expandVars` is called with `expansionContext{}` which has no `PluginData`, and falls through to OS env which is unset → error during `LoadPlugin`.

### Step 9.3: Add a `pluginDataDir` helper

- [ ] In `agent/plugin.go`, add the helper that picks the per-plugin data directory:

```go
// pluginDataDir returns the per-plugin data directory used as the value
// for ${CLAUDE_PLUGIN_DATA}. The path lives under the same XDG_CONFIG_HOME
// (or ~/.config) base used by globalMCPConfigPath, in
// serf/plugins/data/<pluginName>. The directory is not created here;
// plugins create it on demand.
func pluginDataDir(pluginName string) string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "serf", "plugins", "data", pluginName)
}
```

### Step 9.4: Add the `noopUserConfigLookup` constant

- [ ] In `agent/plugin.go`, add:

```go
// noopUserConfigLookup is the placeholder userConfigLookup used by the
// plugin loader until SP7 supplies a real implementation. It always
// returns ok=false, so any ${user_config.K} reference without a
// :-default fails to load with the standard "not set" error.
func noopUserConfigLookup(string) (string, bool) { return "", false }
```

### Step 9.5: Build the plugin `expansionContext` and replace `expandPluginRoot`

- [ ] In `agent/plugin.go`, rewrite `loadPluginMCPFile`:

```go
// loadPluginMCPFile reads a plugin's .mcp.json file and parses server
// configs using an expansionContext seeded with the plugin's root and
// data directories.
func loadPluginMCPFile(path, pluginDir, pluginName string) ([]MCPServerConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cf struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("parsing MCP config %s: %w", path, err)
	}

	ctx := expansionContext{
		PluginRoot: pluginDir,
		PluginData: pluginDataDir(pluginName),
		UserConfig: noopUserConfigLookup,
	}
	return parseMCPServerMap(cf.MCPServers, path, ctx)
}
```

- [ ] In `agent/plugin.go`, rewrite `discoverPluginMCPConfigs`:

```go
// discoverPluginMCPConfigs reads MCP server configs from a plugin's .mcp.json
// file and/or inline manifest mcpServers field. Server names are prefixed
// with "plugin_<pluginName>_". Expansion of ${CLAUDE_PLUGIN_ROOT},
// ${CLAUDE_PLUGIN_DATA}, and ${user_config.KEY} happens inside expandVars
// via the expansionContext.
func discoverPluginMCPConfigs(pluginDir string, manifestMCPServers json.RawMessage, pluginName string) ([]MCPServerConfig, error) {
	var layers [][]MCPServerConfig
	ctx := expansionContext{
		PluginRoot: pluginDir,
		PluginData: pluginDataDir(pluginName),
		UserConfig: noopUserConfigLookup,
	}

	// Layer 1: .mcp.json file in the plugin directory.
	mcpPath := filepath.Join(pluginDir, ".mcp.json")
	if fileConfigs, err := loadPluginMCPFile(mcpPath, pluginDir, pluginName); err == nil {
		layers = append(layers, fileConfigs)
	}
	// Missing file is not an error.

	// Layer 2: Inline mcpServers from the manifest.
	if len(manifestMCPServers) > 0 {
		var servers map[string]json.RawMessage
		if err := json.Unmarshal(manifestMCPServers, &servers); err == nil && len(servers) > 0 {
			inlineConfigs, err := parseMCPServerMap(servers, "inline", ctx)
			if err != nil {
				return nil, err
			}
			layers = append(layers, inlineConfigs)
		}
	}

	merged := MergeMCPConfigs(layers...)
	if len(merged) == 0 {
		return nil, nil
	}

	prefix := "plugin_" + pluginName + "_"
	for i := range merged {
		merged[i].Name = prefix + merged[i].Name
	}
	return merged, nil
}
```

- [ ] In `agent/plugin.go`, remove the `expandPluginRoot` function entirely (lines ~59-62):

```
// expandPluginRoot replaces ${CLAUDE_PLUGIN_ROOT} with pluginDir in s.
func expandPluginRoot(s string, pluginDir string) string {
	return strings.ReplaceAll(s, "${CLAUDE_PLUGIN_ROOT}", pluginDir)
}
```

- [ ] If `strings` becomes unused in `agent/plugin.go` after this removal, also remove the import. (As of today `strings` is also used by `kebabCaseRe` indirectly via `regexp`, not `strings`. Check after editing — let `go build` tell you.)

### Step 9.6: Run the regression tests

Run: `go test ./agent/ -run "TestPluginLoader_ExpandsPluginRoot_ViaExpandVars|TestPluginLoader_ExpandsPluginDataInFile" -v`

Expected: PASS.

### Step 9.7: Run the full agent suite

Run: `go test ./agent/...`

Expected: PASS. If any existing plugin test fails (e.g. one that asserted on the exact pre-expansion code path), inspect the failure and update the assertion to match the new flow. Do not change the production semantics.

### Step 9.8: Commit

```bash
git add agent/plugin.go agent/plugin_test.go
git commit -m "agent: replace expandPluginRoot with expandVars in plugin loader (SP6)"
```

---

## Task 10: Add `expandVars` row-by-row coverage tests (§10.1 of the spec)

**Goal:** Consolidate the small, scattered `expandVars` tests added in Tasks 1, 3, 4, 5 into one full table-driven test covering every row in spec §10.1, including the non-recursion case and the unterminated `${` case. This locks in behavior.

**Files:**
- Modify: `agent/mcp_config_test.go`

### Step 10.1: Write the failing combined-table test

- [ ] Append to `agent/mcp_config_test.go`:

```go
// TestExpandVars_FullTable is the spec-§10.1 coverage table. Every reserved
// name × outcome × default-presence combination appears here, plus the
// non-recursion and unterminated-${ edge cases.
func TestExpandVars_FullTable(t *testing.T) {
	lookup := func(values map[string]string) userConfigLookup {
		return func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		}
	}

	tests := []struct {
		name       string
		input      string
		setEnv     map[string]string // OS env to set with t.Setenv
		unsetEnv   []string          // OS env to ensure unset
		ctx        expansionContext
		want       string
		wantErrSub string
	}{
		{name: "1 plain", input: "plain", want: "plain"},
		{name: "2 ${A} set", input: "${A}", setEnv: map[string]string{"A": "v"}, want: "v"},
		{name: "3 ${A:-d} unset", input: "${A:-d}", unsetEnv: []string{"A"}, want: "d"},
		{name: "4 ${A} unset error", input: "${A}", unsetEnv: []string{"A"}, wantErrSub: `"A" is not set`},
		{name: "5 project dir from ctx", input: "${CLAUDE_PROJECT_DIR}", ctx: expansionContext{ProjectDir: "/x"}, want: "/x"},
		{name: "6 ctx wins over OS env", input: "${CLAUDE_PROJECT_DIR}", setEnv: map[string]string{"CLAUDE_PROJECT_DIR": "/shell"}, ctx: expansionContext{ProjectDir: "/x"}, want: "/x"},
		{name: "7 project dir empty errors", input: "${CLAUDE_PROJECT_DIR}", unsetEnv: []string{"CLAUDE_PROJECT_DIR"}, wantErrSub: `"CLAUDE_PROJECT_DIR" is not set`},
		{name: "8 project dir default", input: "${CLAUDE_PROJECT_DIR:-.}", unsetEnv: []string{"CLAUDE_PROJECT_DIR"}, want: "."},
		{name: "9 plugin root from ctx", input: "${CLAUDE_PLUGIN_ROOT}", ctx: expansionContext{PluginRoot: "/p"}, want: "/p"},
		{name: "10 plugin root empty errors", input: "${CLAUDE_PLUGIN_ROOT}", unsetEnv: []string{"CLAUDE_PLUGIN_ROOT"}, wantErrSub: `"CLAUDE_PLUGIN_ROOT" is not set`},
		{name: "11 plugin data from ctx", input: "${CLAUDE_PLUGIN_DATA}", ctx: expansionContext{PluginData: "/d"}, want: "/d"},
		{name: "12 user_config lookup hit", input: "${user_config.K}", ctx: expansionContext{UserConfig: lookup(map[string]string{"K": "v"})}, want: "v"},
		{name: "13 user_config lookup miss errors", input: "${user_config.K}", ctx: expansionContext{UserConfig: lookup(map[string]string{})}, wantErrSub: `"user_config.K" is not set`},
		{name: "14 user_config nil errors", input: "${user_config.K}", ctx: expansionContext{}, wantErrSub: `"user_config.K" is not set`},
		{name: "15 user_config nil with default", input: "${user_config.K:-fallback}", ctx: expansionContext{}, want: "fallback"},
		{name: "16 mixed literals", input: "prefix-${CLAUDE_PROJECT_DIR}-suffix-${A}", setEnv: map[string]string{"A": "y"}, ctx: expansionContext{ProjectDir: "x"}, want: "prefix-x-suffix-y"},
		{name: "17 unterminated literal", input: "${", want: "${"},
		{name: "18 no recursion left-to-right", input: "${a${b}}", setEnv: map[string]string{"b": "Z"}, unsetEnv: []string{"a"}, wantErrSub: `"a" is not set`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range tt.unsetEnv {
				if err := os.Unsetenv(k); err != nil {
					t.Fatal(err)
				}
			}
			for k, v := range tt.setEnv {
				t.Setenv(k, v)
			}
			got, err := expandVars(tt.input, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("got err=%v, want substring %q (output=%q)", err, tt.wantErrSub, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
```

Row 18 documents the spec's no-recursion claim: scanning is left-to-right, and the inner `${b}` is parsed first because `}` is found before the outer `${a...}` could close. The scanner finds `${` at offset 0, walks to the next `}` which closes `${a${b}` at "${a${b}" — wait: re-read `expandVars`. With input `${a${b}}`:

- Find `${` at i=0; advance i past it (i=2).
- Find `}` starting at i=2 in the substring `a${b}}`; the first `}` closes after `a${b`, so `expr = "a${b"`.
- That expr is parsed as a varName `a${b` — but `:-` is not found, so it's an unset env var named `a${b`, which errors.

So row 18 should expect an error mentioning the literal `a${b`. Update the row:

```go
		{name: "18 no recursion left-to-right", input: "${a${b}}", setEnv: map[string]string{"b": "Z"}, unsetEnv: []string{"a${b"}, wantErrSub: `"a${b" is not set`},
```

Replace the row in the table accordingly.

### Step 10.2: Run the table test

Run: `go test ./agent/ -run TestExpandVars_FullTable -v`

Expected: PASS for every row. Any failure means a previous task's branch needs adjustment.

### Step 10.3: Add the `LoadMCPConfigFile` extension-table tests (§10.2)

- [ ] Append to `agent/mcp_config_test.go`:

```go
// TestLoadMCPConfigFile_ContextSubstitution covers the §10.2 rows that the
// existing tests do not already exercise. It uses loadMCPConfigFileWithContext
// because LoadMCPConfigFile picks up project dir from resolveProjectDir(nil),
// which is hard to pin in tests.
func TestLoadMCPConfigFile_ContextSubstitution(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		ctx        expansionContext
		assert     func(t *testing.T, cfgs []MCPServerConfig)
		wantErrSub string
	}{
		{
			name: "project dir in command resolves",
			body: `{"mcpServers":{"x":{"command":"${CLAUDE_PROJECT_DIR}/bin/x"}}}`,
			ctx:  expansionContext{ProjectDir: "/tmp/p"},
			assert: func(t *testing.T, cfgs []MCPServerConfig) {
				if cfgs[0].Command != "/tmp/p/bin/x" {
					t.Errorf("command = %q, want /tmp/p/bin/x", cfgs[0].Command)
				}
			},
		},
		{
			name: "project dir in url resolves",
			body: `{"mcpServers":{"x":{"type":"http","url":"${CLAUDE_PROJECT_DIR}/api"}}}`,
			ctx:  expansionContext{ProjectDir: "/tmp/p"},
			assert: func(t *testing.T, cfgs []MCPServerConfig) {
				if cfgs[0].URL != "/tmp/p/api" {
					t.Errorf("url = %q, want /tmp/p/api", cfgs[0].URL)
				}
			},
		},
		{
			name:       "project dir unset no default",
			body:       `{"mcpServers":{"x":{"command":"${CLAUDE_PROJECT_DIR}/bin/x"}}}`,
			ctx:        expansionContext{},
			wantErrSub: `"CLAUDE_PROJECT_DIR" is not set`,
		},
		{
			name: "user_config in headers resolves",
			body: `{"mcpServers":{"x":{"type":"http","url":"https://e.test","headers":{"Authorization":"Bearer ${user_config.TOKEN}"}}}}`,
			ctx: expansionContext{UserConfig: func(k string) (string, bool) {
				if k == "TOKEN" {
					return "abc", true
				}
				return "", false
			}},
			assert: func(t *testing.T, cfgs []MCPServerConfig) {
				if cfgs[0].Headers["Authorization"] != "Bearer abc" {
					t.Errorf("Authorization = %q, want Bearer abc", cfgs[0].Headers["Authorization"])
				}
			},
		},
		{
			name:       "user_config in non-plugin context errors",
			body:       `{"mcpServers":{"x":{"type":"http","url":"https://e.test","headers":{"Authorization":"Bearer ${user_config.TOKEN}"}}}}`,
			ctx:        expansionContext{},
			wantErrSub: `"user_config.TOKEN" is not set`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "mcp.json")
			if err := os.WriteFile(path, []byte(tt.body), 0644); err != nil {
				t.Fatal(err)
			}
			// Ensure no parent-shell CLAUDE_PROJECT_DIR pollutes the test.
			if err := os.Unsetenv("CLAUDE_PROJECT_DIR"); err != nil {
				t.Fatal(err)
			}
			cfgs, err := loadMCPConfigFileWithContext(path, tt.ctx)
			if tt.wantErrSub != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("got err=%v, want substring %q", err, tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(cfgs) != 1 {
				t.Fatalf("expected 1 cfg, got %d", len(cfgs))
			}
			tt.assert(t, cfgs)
		})
	}
}
```

### Step 10.4: Run the new table test

Run: `go test ./agent/ -run TestLoadMCPConfigFile_ContextSubstitution -v`

Expected: PASS.

### Step 10.5: Run full suite

Run: `go test ./agent/...`

Expected: PASS.

### Step 10.6: Commit

```bash
git add agent/mcp_config_test.go
git commit -m "agent: full coverage tables for expandVars and LoadMCPConfigFile (SP6)"
```

---

## Self-Review Notes (informational; the worker doesn't act on these)

After writing this plan, the author ran the spec-coverage check below.

| Spec section | Plan coverage |
|---|---|
| §2.1 Type normalization (`stdio`/`sse`/`http`) | Task 6 collapses `streamable-http` → `http`; existing tests for the other names remain untouched |
| §2.2 `expansionContext` | Task 1 step 1.3 |
| §2.3 Rename `expandEnvVars` → `expandVars` | Task 1 step 1.4 |
| §2.3 `serverJSONToConfig` gains `ctx` parameter | Task 2 step 2.3 |
| §2.4 `resolveProjectDir` helper | Task 7 |
| §3 `streamable-http` alias (case-sensitive) | Task 6 |
| §4.1 Project dir precedence | Task 7 (test rows 1–5) |
| §4.2 Empty project dir behavior | Task 8 (test row "stdio + projectDir empty…inherit parent") plus Task 7 row 6 |
| §4.3 Injection point + cfg.Env precedence | Task 8 |
| §4.4 `transportForConfig` projectDir parameter | Task 8 |
| §5.1 Supported placeholder names | Tasks 3, 4, 5, plus full table in Task 10 |
| §5.2 Resolution order (context beats OS env) | Task 3 row "context wins over OS env" |
| §5.3 No recursive expansion | Task 10 row 18 |
| §5.4 Undefined behavior + error format | Task 5 row "nil lookup → error preserves dotted name", Task 10 row 13/14 |
| §5.5 Worked example | Implicitly covered by Tasks 3–5 + Task 9 plugin tests |
| §6 SP7 interface contract (`userConfigLookup`, `noopUserConfigLookup`) | Task 1 (type), Task 9 (no-op) |
| §7 Backward compatibility | Existing tests in `mcp_config_test.go` continue to run after each task; full-suite run at every step |
| §8 Error contracts | Task 6 (unknown-type rows), Tasks 3/4/5 (each error row), Task 10 table |
| §9 File layout | Tasks 1–9 confined to the three named files plus their `_test.go` siblings |
| §10.1 Table | Task 10 step 10.1 |
| §10.2 Loader rows | Task 10 step 10.3 (extends existing `TestLoadMCPConfigFile_*` coverage) |
| §10.3 Env injection integration test | Task 8 steps 8.9 (positive) and 8.10 (override) |
| §10.4 `resolveProjectDir` table | Task 7 step 7.1 |
| §10.5 Coverage gate | Covered by the full table in Task 10 + integration tests in Task 8 |

Placeholder scan: no "TBD"/"implement later"/"similar to" instances.

Type consistency: `expansionContext`, `userConfigLookup`, `expandVars`, `serverJSONToConfig`, `parseMCPServerMap`, `transportForConfig`, `NewMCPManager`, `resolveProjectDir`, `pluginDataDir`, `noopUserConfigLookup`, `loadMCPConfigFileWithContext` are all introduced with one consistent signature and used by exactly that signature in later tasks.

Known assumptions:

- `gitRootOrEmpty` exists today in `agent/project_docs.go` and is used unchanged.
- `ExecutionEnvironment` is already a package-level interface in `agent/`; the duck-type assertion `interface{ ProjectDir() string }` does not require modifying that interface.
- `mcp.CommandTransport.Command` is exported on the MCP SDK; the unit test in 8.1 dereferences `cmdTransport.Command.Env`.
- Callers of `NewMCPManager` (the four cmd entry points listed in the spec §9) ultimately reach this via `agent/session.go`; only that one file is updated in Task 8.5. If any other production caller exists, the build break in step 8.8 surfaces it.
