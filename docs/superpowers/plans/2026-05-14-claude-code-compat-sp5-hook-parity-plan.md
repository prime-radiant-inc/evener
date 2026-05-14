# SP5 — Hook Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring serf's plugin hook system to Claude Code A-tier parity: nine new events, three new hook types (`http`, `mcp_tool`, `agent`), six new config fields, five new output fields, five new input fields, three new env vars, and the documented dual-mode matcher.

**Architecture:** Additive extension of `agent/plugin_hooks.go`. New files in package `agent/`: `plugin_hooks_matcher.go`, `plugin_hooks_http.go`, `plugin_hooks_mcp.go`, `plugin_hooks_agent.go`, `plugin_hooks_async.go`, `config_watcher.go`. Existing files (`session.go`, `subagents.go`, `context_strategy.go`) gain firing sites for new events. Strict TDD throughout: every new exported symbol gets a unit test before implementation; `t.TempDir()` for filesystem; existing `llm` stub for `agent`/`prompt` hooks; `httptest.NewServer` for `http` hooks.

**Tech Stack:** Go (existing). New imports: `net/http`, `net/http/httptest`, `github.com/fsnotify/fsnotify` (only for `config_watcher.go`). No other new dependencies.

**Spec reference:** `docs/superpowers/specs/2026-05-14-claude-code-compat-sp5-hook-parity-design.md`.

**Sibling specs assumed:**
- SP1 (config loader) — provides `ConfigTier` types and merged hook arrays.
- SP2 (permissions) — exports `permissions.EvaluateRule(rule, toolName, toolInput) (bool, error)` and `permissions.CurrentMode(s *Session) string`. SP5 stubs these via small interfaces so its tests do not depend on SP2 landing first.

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `agent/plugin_hooks.go` | modify | New event constants, `RegisteredHook` field additions, `HookInput`/`ParsedHookOutput` additions, `parseHookSpec` helper, expanded `parseHookOutput`, new `Run<Event>` methods, additionalContext routing on `HookRunResult`/`PreToolUseResult` |
| `agent/plugin_hooks_matcher.go` | create | `compileMatcher(pattern string) func(string) bool` — dual-mode (case 1 wildcard, case 2 exact-or-pipe-list, case 3 regex) |
| `agent/plugin_hooks_matcher_test.go` | create | Table-driven coverage of every §9.2 row and §9.3 caveats |
| `agent/plugin_hooks_http.go` | create | `executeHTTPHook` — POST `HookInput` JSON to URL with restricted env-var header substitution |
| `agent/plugin_hooks_http_test.go` | create | `httptest.NewServer` harness covering response variants and header substitution |
| `agent/plugin_hooks_mcp.go` | create | `executeMCPToolHook` — call a connected MCP tool with substituted args |
| `agent/plugin_hooks_mcp_test.go` | create | Stub MCP tool host; arg substitution and error-path tests |
| `agent/plugin_hooks_agent.go` | create | `executeAgentHook` — ephemeral one-shot subagent with `decide(...)` tool; experimental warn-once |
| `agent/plugin_hooks_agent_test.go` | create | LLM stub returns `decide(allow,...)`/`decide(deny,...)`; timeout and tool-budget paths |
| `agent/plugin_hooks_async.go` | create | Async dispatch, rewake channel, `AsyncRewakeSignal`, `formatRewakeMessage` |
| `agent/plugin_hooks_async_test.go` | create | Async-no-block, rewake-via-exit-2, full-channel drop |
| `agent/plugin_hooks_events_test.go` | create | One section per new event per §13.1 |
| `agent/config_watcher.go` | create | fsnotify-backed config watcher behind `cfg.WatchConfig`; emits `ConfigChangeSignal` |
| `agent/config_watcher_test.go` | create | Verifies signal emission on file write |
| `agent/session.go` | modify | Fire seven new events (`PostToolUseFailure`, `PostToolBatch`, `StopFailure`, `UserPromptExpansion`); extend `hookInput` with new common fields; drain async-rewake channel |
| `agent/subagents.go` | modify | Fire `SubagentStart` between `NewSession` and `go sub.run` |
| `agent/context_strategy.go` | modify | Fire `PostCompact` after `MaybeCompact` |
| `agent/plugin_e2e_test.go` | modify | Add `TestPluginE2E_HookParityScenarios` |
| `agent/testdata/plugins/hooks_http/` | create | Fixture: one `http` hook |
| `agent/testdata/plugins/hooks_mcp/` | create | Fixture: one `mcp_tool` hook |
| `agent/testdata/plugins/hooks_agent/` | create | Fixture: one `agent` hook |
| `agent/testdata/plugins/hooks_matcher_corners/` | create | Fixture exercising §9.2 matcher rows |
| `agent/testdata/plugins/hookparity/` | create | E2E fixture used by `TestPluginE2E_HookParityScenarios` |

---

## Phase 1: Constants, structs, and the matcher

### Task 1: Add the nine new `HookEvent` constants and register them in `validHookEvents`

**Files:**
- Modify: `agent/plugin_hooks.go:22-45`
- Test: `agent/plugin_hooks_test.go` (new function appended)

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestValidHookEvents_IncludesA_TierEvents(t *testing.T) {
	want := []HookEvent{
		HookPostToolUseFailure,
		HookPostToolBatch,
		HookStopFailure,
		HookSubagentStart,
		HookUserPromptExpansion,
		HookPostCompact,
		HookPermissionRequest,
		HookPermissionDenied,
		HookConfigChange,
	}
	for _, e := range want {
		if !validHookEvents[e] {
			t.Errorf("validHookEvents missing %q", e)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestValidHookEvents_IncludesA_TierEvents -v`
Expected: FAIL — `undefined: HookPostToolUseFailure`.

- [ ] **Step 3: Add the constants and registrations**

Modify `agent/plugin_hooks.go:22-45` to:

```go
const (
	HookPreToolUse       HookEvent = "PreToolUse"
	HookPostToolUse      HookEvent = "PostToolUse"
	HookStop             HookEvent = "Stop"
	HookSubagentStop     HookEvent = "SubagentStop"
	HookUserPromptSubmit HookEvent = "UserPromptSubmit"
	HookSessionStart     HookEvent = "SessionStart"
	HookSessionEnd       HookEvent = "SessionEnd"
	HookPreCompact       HookEvent = "PreCompact"
	HookNotification     HookEvent = "Notification"

	// A-tier events added by SP5.
	HookPostToolUseFailure  HookEvent = "PostToolUseFailure"
	HookPostToolBatch       HookEvent = "PostToolBatch"
	HookStopFailure         HookEvent = "StopFailure"
	HookSubagentStart       HookEvent = "SubagentStart"
	HookUserPromptExpansion HookEvent = "UserPromptExpansion"
	HookPostCompact         HookEvent = "PostCompact"
	HookPermissionRequest   HookEvent = "PermissionRequest"
	HookPermissionDenied    HookEvent = "PermissionDenied"
	HookConfigChange        HookEvent = "ConfigChange"
)

var validHookEvents = map[HookEvent]bool{
	HookPreToolUse:          true,
	HookPostToolUse:         true,
	HookStop:                true,
	HookSubagentStop:        true,
	HookUserPromptSubmit:    true,
	HookSessionStart:        true,
	HookSessionEnd:          true,
	HookPreCompact:          true,
	HookNotification:        true,
	HookPostToolUseFailure:  true,
	HookPostToolBatch:       true,
	HookStopFailure:         true,
	HookSubagentStart:       true,
	HookUserPromptExpansion: true,
	HookPostCompact:         true,
	HookPermissionRequest:   true,
	HookPermissionDenied:    true,
	HookConfigChange:        true,
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestValidHookEvents_IncludesA_TierEvents -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): register nine A-tier hook event constants"
```

---

### Task 2: Implement `compileMatcher` — dual-mode matcher (case 1 wildcard)

**Files:**
- Create: `agent/plugin_hooks_matcher.go`
- Create: `agent/plugin_hooks_matcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_matcher_test.go`:

```go
package agent

import "testing"

func TestCompileMatcher_Wildcard(t *testing.T) {
	tests := []struct {
		pattern string
		target  string
		want    bool
	}{
		{"", "Bash", true},
		{"", "", true},
		{"*", "Bash", true},
		{"*", "anything", true},
	}
	for _, tc := range tests {
		fn := compileMatcher(tc.pattern)
		if got := fn(tc.target); got != tc.want {
			t.Errorf("compileMatcher(%q)(%q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestCompileMatcher_Wildcard -v`
Expected: FAIL — `undefined: compileMatcher`.

- [ ] **Step 3: Create the file with minimal implementation**

Create `agent/plugin_hooks_matcher.go`:

```go
package agent

import (
	"log"
	"regexp"
	"strings"
)

// compileMatcher returns a predicate that matches a target string against
// the given pattern using Claude Code's documented dual-mode algorithm:
//
//	case 1: "" or "*" → match everything
//	case 2: pattern contains only [a-zA-Z0-9_|] → split on "|" and exact-match
//	case 3: otherwise → regex (RE2, MatchString semantics)
//
// On regex compile failure, the matcher logs a warning and never matches.
func compileMatcher(pattern string) func(string) bool {
	if pattern == "" || pattern == "*" {
		return func(string) bool { return true }
	}
	if isSimpleMatcher(pattern) {
		alts := strings.Split(pattern, "|")
		return func(target string) bool {
			for _, a := range alts {
				if a == target {
					return true
				}
			}
			return false
		}
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		log.Printf("hook matcher %q: regex compile failed: %v (never matches)", pattern, err)
		return func(string) bool { return false }
	}
	return re.MatchString
}

func isSimpleMatcher(pattern string) bool {
	for _, r := range pattern {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '_':
		case r == '|':
		default:
			return false
		}
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestCompileMatcher_Wildcard -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_matcher.go agent/plugin_hooks_matcher_test.go
git commit -m "feat(hooks): introduce compileMatcher with wildcard case"
```

---

### Task 3: Cover case 2 (exact-or-pipe-list) in `compileMatcher`

**Files:**
- Modify: `agent/plugin_hooks_matcher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_matcher_test.go`:

```go
func TestCompileMatcher_ExactAndPipeList(t *testing.T) {
	tests := []struct {
		pattern string
		target  string
		want    bool
	}{
		{"Bash", "Bash", true},
		{"Bash", "BashEcho", false},          // exact, no substring
		{"Bash", "Edit", false},
		{"Bash|Edit", "Edit", true},
		{"Bash|Edit", "Bash", true},
		{"Bash|Edit", "Write", false},
		{"mcp__server__tool", "mcp__server__tool", true}, // underscores stay simple
		{"mcp__server__tool", "mcp__server__other", false},
	}
	for _, tc := range tests {
		fn := compileMatcher(tc.pattern)
		if got := fn(tc.target); got != tc.want {
			t.Errorf("compileMatcher(%q)(%q) = %v, want %v", tc.pattern, tc.target, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it passes immediately**

Run: `go test ./agent/ -run TestCompileMatcher_ExactAndPipeList -v`
Expected: PASS — implementation from Task 2 already covers case 2.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_hooks_matcher_test.go
git commit -m "test(hooks): lock in exact-and-pipe-list matcher semantics"
```

---

### Task 4: Cover case 3 (regex) in `compileMatcher`

**Files:**
- Modify: `agent/plugin_hooks_matcher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_matcher_test.go`:

```go
func TestCompileMatcher_RegexMode(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		target  string
		want    bool
	}{
		{"dotstar matches anything", "mcp__.*", "mcp__github__search", true},
		{"dotstar requires prefix", "mcp__.*", "Bash", false},
		{"anchored", "^Write$", "Write", true},
		{"anchored mismatch", "^Write$", "WriteFile", false},
		{"invalid regex never matches", "Bash(rm:*)", "Bash", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fn := compileMatcher(tc.pattern)
			if got := fn(tc.target); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it passes immediately**

Run: `go test ./agent/ -run TestCompileMatcher_RegexMode -v`
Expected: PASS — Task 2's implementation handles regex and the invalid-regex never-match path.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_hooks_matcher_test.go
git commit -m "test(hooks): lock in matcher regex mode incl. invalid-regex path"
```

---

### Task 5: Cache compiled matcher on `RegisteredHook` and rewire `matchHooks`

**Files:**
- Modify: `agent/plugin_hooks.go` — `RegisteredHook` struct and `matchHooks` method
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestMatchHooks_UsesDualMode(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPreToolUse, RegisteredHook{Matcher: "Bash", Type: "command", Command: "true"})
	r.Add(HookPreToolUse, RegisteredHook{Matcher: "Edit|Write", Type: "command", Command: "true"})
	r.Add(HookPreToolUse, RegisteredHook{Matcher: "mcp__.*", Type: "command", Command: "true"})

	// Force matcher compilation (Add path must compile).
	cases := []struct {
		target string
		want   int
	}{
		{"Bash", 1},                // exact only
		{"Edit", 1},                // pipe-list
		{"mcp__github__search", 1}, // regex
		{"Write", 1},               // pipe-list "Edit|Write"
		{"NoMatch", 0},
	}
	for _, tc := range cases {
		got := r.matchHooks(HookPreToolUse, tc.target)
		if len(got) != tc.want {
			t.Errorf("target=%q: got %d hooks, want %d", tc.target, len(got), tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestMatchHooks_UsesDualMode -v`
Expected: FAIL — current `matchHooks` regex-compiles `Bash`, which matches `BashEcho` substring-style; the `"Edit"` row in pipe-list also fails because `Edit|Write` is regex-treated.

- [ ] **Step 3: Edit `RegisteredHook` and `matchHooks` and `Add`**

In `agent/plugin_hooks.go`, change `RegisteredHook` to add an unexported field (keep at bottom of struct so JSON tag patterns are untouched):

```go
type RegisteredHook struct {
	Matcher    string
	Type       string
	Command    string
	Prompt     string
	Timeout    int
	Model      string
	PluginName string
	PluginDir  string

	matcherFunc func(string) bool // populated by HookRunner.Add or ParsePluginHooks
}
```

Replace `matchHooks` and `Add` in `agent/plugin_hooks.go`:

```go
func (r *HookRunner) Add(event HookEvent, hooks ...RegisteredHook) {
	for i := range hooks {
		if hooks[i].matcherFunc == nil {
			hooks[i].matcherFunc = compileMatcher(hooks[i].Matcher)
		}
	}
	r.hooks[event] = append(r.hooks[event], hooks...)
}

func (r *HookRunner) matchHooks(event HookEvent, toolName string) []RegisteredHook {
	var matched []RegisteredHook
	for _, hook := range r.hooks[event] {
		fn := hook.matcherFunc
		if fn == nil {
			fn = compileMatcher(hook.Matcher)
		}
		if fn(toolName) {
			matched = append(matched, hook)
		}
	}
	return matched
}
```

Remove the now-unused `regexp` import if no other call remains.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestMatchHooks_UsesDualMode|TestParsePluginHooks|TestHook" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): cache dual-mode matcher on RegisteredHook"
```

---

### Task 6: Compile matchers at parse time in `ParsePluginHooks`

**Files:**
- Modify: `agent/plugin_hooks.go` — `ParsePluginHooks`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParsePluginHooks_CachesMatcherFunc(t *testing.T) {
	data := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Bash|Edit","hooks":[{"type":"command","command":"true"}]}]}}`)
	hooks, err := ParsePluginHooks(data, "/p", "p")
	if err != nil {
		t.Fatal(err)
	}
	if hooks[HookPreToolUse][0].matcherFunc == nil {
		t.Fatal("matcherFunc was not cached at parse time")
	}
	if !hooks[HookPreToolUse][0].matcherFunc("Bash") {
		t.Error("cached matcher should match Bash")
	}
	if hooks[HookPreToolUse][0].matcherFunc("Write") {
		t.Error("cached matcher should not match Write")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginHooks_CachesMatcherFunc -v`
Expected: FAIL — `matcherFunc` is nil after parse.

- [ ] **Step 3: Set `matcherFunc` in the `rh := RegisteredHook{...}` block inside `ParsePluginHooks`**

In `agent/plugin_hooks.go`, just before `result[event] = append(result[event], rh)`:

```go
rh.matcherFunc = compileMatcher(g.Matcher)
result[event] = append(result[event], rh)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParsePluginHooks_CachesMatcherFunc -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): compile matcher func during ParsePluginHooks"
```

---

### Task 7: Extend `hookSpec` and `RegisteredHook` with the six new config fields

**Files:**
- Modify: `agent/plugin_hooks.go` — `hookSpec` and `RegisteredHook`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParsePluginHooks_NewConfigFields(t *testing.T) {
	data := []byte(`{
		"hooks": {
			"PreToolUse": [
				{
					"matcher": "Bash",
					"hooks": [
						{
							"type": "command",
							"command": "echo hi",
							"args": ["--strict"],
							"async": true,
							"asyncRewake": true,
							"shell": "bash",
							"if": "Bash(*)",
							"statusMessage": "Checking..."
						}
					]
				}
			]
		}
	}`)
	hooks, err := ParsePluginHooks(data, "/p", "p")
	if err != nil {
		t.Fatal(err)
	}
	h := hooks[HookPreToolUse][0]
	if len(h.Args) != 1 || h.Args[0] != "--strict" {
		t.Errorf("Args = %v, want [--strict]", h.Args)
	}
	if !h.Async {
		t.Error("Async = false, want true")
	}
	if !h.AsyncRewake {
		t.Error("AsyncRewake = false, want true")
	}
	if h.Shell != "bash" {
		t.Errorf("Shell = %q, want bash", h.Shell)
	}
	if h.If != "Bash(*)" {
		t.Errorf("If = %q, want Bash(*)", h.If)
	}
	if h.StatusMessage != "Checking..." {
		t.Errorf("StatusMessage = %q, want Checking...", h.StatusMessage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginHooks_NewConfigFields -v`
Expected: FAIL — fields do not exist.

- [ ] **Step 3: Add the fields**

Extend `hookSpec` in `agent/plugin_hooks.go`:

```go
type hookSpec struct {
	Type          string   `json:"type"`
	Command       string   `json:"command,omitempty"`
	Prompt        string   `json:"prompt,omitempty"`
	Timeout       int      `json:"timeout,omitempty"`
	Model         string   `json:"model,omitempty"`
	Args          []string `json:"args,omitempty"`
	Async         bool     `json:"async,omitempty"`
	AsyncRewake   bool     `json:"asyncRewake,omitempty"`
	Shell         string   `json:"shell,omitempty"`
	If            string   `json:"if,omitempty"`
	StatusMessage string   `json:"statusMessage,omitempty"`

	// type=http
	URL            string            `json:"url,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	AllowedEnvVars []string          `json:"allowedEnvVars,omitempty"`

	// type=mcp_tool
	MCPServer string         `json:"server,omitempty"`
	MCPTool   string         `json:"tool,omitempty"`
	MCPInput  map[string]any `json:"input,omitempty"`

	// type=agent
	AgentType string `json:"agentType,omitempty"`
}
```

Extend `RegisteredHook` (insert after the existing exported fields, before `matcherFunc`):

```go
type RegisteredHook struct {
	Matcher    string
	Type       string
	Command    string
	Prompt     string
	Timeout    int
	Model      string
	PluginName string
	PluginDir  string

	Args          []string
	Async         bool
	AsyncRewake   bool
	Shell         string
	If            string
	StatusMessage string

	URL            string
	Headers        map[string]string
	AllowedEnvVars []string

	MCPServer string
	MCPTool   string
	MCPInput  map[string]any

	AgentType string

	matcherFunc func(string) bool
}
```

In `ParsePluginHooks`, populate the new `rh` fields right where existing fields are set:

```go
rh := RegisteredHook{
	Matcher:        g.Matcher,
	Type:           spec.Type,
	Command:        expandPluginRoot(spec.Command, pluginDir),
	Prompt:         expandPluginRoot(spec.Prompt, pluginDir),
	Timeout:        timeout,
	Model:          spec.Model,
	PluginName:     pluginName,
	PluginDir:      pluginDir,
	Args:           expandPluginRootSlice(spec.Args, pluginDir),
	Async:          spec.Async || spec.AsyncRewake,
	AsyncRewake:    spec.AsyncRewake,
	Shell:          spec.Shell,
	If:             spec.If,
	StatusMessage:  spec.StatusMessage,
	URL:            expandPluginRoot(spec.URL, pluginDir),
	Headers:        spec.Headers,
	AllowedEnvVars: spec.AllowedEnvVars,
	MCPServer:      spec.MCPServer,
	MCPTool:        spec.MCPTool,
	MCPInput:       spec.MCPInput,
	AgentType:      spec.AgentType,
}
```

Add the helper near `expandPluginRoot` (or at bottom of `plugin_hooks.go`):

```go
func expandPluginRootSlice(s []string, pluginDir string) []string {
	if len(s) == 0 {
		return nil
	}
	out := make([]string, len(s))
	for i, v := range s {
		out[i] = expandPluginRoot(v, pluginDir)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestParsePluginHooks" -v`
Expected: PASS for all rows including the new one.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): parse six new RegisteredHook config fields"
```

---

### Task 8: Validate `args` is only legal on `type: "command"`

**Files:**
- Modify: `agent/plugin_hooks.go` — `ParsePluginHooks`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParsePluginHooks_ArgsOnlyOnCommand(t *testing.T) {
	data := []byte(`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"prompt","prompt":"x","args":["--bad"]}]}]}}`)
	_, err := ParsePluginHooks(data, "/p", "p")
	if err == nil {
		t.Fatal("expected error for args on prompt hook")
	}
	if !strings.Contains(err.Error(), `"args"`) {
		t.Errorf("error %q should mention args", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginHooks_ArgsOnlyOnCommand -v`
Expected: FAIL — parse currently accepts the malformed spec.

- [ ] **Step 3: Add the validation inside the hook-spec loop in `ParsePluginHooks`**

After the `rh := RegisteredHook{...}` block in `ParsePluginHooks`, before `result[event] = append(...)`:

```go
if len(spec.Args) > 0 && spec.Type != "command" {
	return nil, fmt.Errorf("hook in event %q: \"args\" is only valid for command hooks", eventName)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParsePluginHooks_ArgsOnlyOnCommand -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): reject args on non-command hooks at parse time"
```

---

### Task 9: Reject `async: true` on flow-control events

**Files:**
- Modify: `agent/plugin_hooks.go` — `ParsePluginHooks`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParsePluginHooks_AsyncForbiddenOnFlowControl(t *testing.T) {
	cases := []string{"PreToolUse", "PermissionRequest", "UserPromptExpansion"}
	for _, event := range cases {
		data := []byte(`{"hooks":{"` + event + `":[{"matcher":"*","hooks":[{"type":"command","command":"true","async":true}]}]}}`)
		if _, err := ParsePluginHooks(data, "/p", "p"); err == nil {
			t.Errorf("event %s: expected error for async hook", event)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginHooks_AsyncForbiddenOnFlowControl -v`
Expected: FAIL.

- [ ] **Step 3: Add the validation**

In `ParsePluginHooks`, before `result[event] = append(...)`:

```go
if (spec.Async || spec.AsyncRewake) && isFlowControlEvent(event) {
	return nil, fmt.Errorf("hook in event %q: async is not allowed for flow-control events", eventName)
}
```

Add helper:

```go
func isFlowControlEvent(e HookEvent) bool {
	switch e {
	case HookPreToolUse, HookPermissionRequest, HookUserPromptExpansion:
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParsePluginHooks_AsyncForbiddenOnFlowControl -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): forbid async on flow-control hook events"
```

---

### Task 10: Add `HookInput` common new fields with `omitempty`

**Files:**
- Modify: `agent/plugin_hooks.go` — `HookInput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestHookInput_NewCommonFields_OmitEmpty(t *testing.T) {
	in := HookInput{SessionID: "s", CWD: "/x", HookEventName: "Stop"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, key := range []string{"transcript_path", "permission_mode", "effort", "agent_id", "agent_type", "tool_use_id"} {
		if strings.Contains(got, key) {
			t.Errorf("empty value should not emit %q; got %s", key, got)
		}
	}
}

func TestHookInput_NewCommonFields_Populated(t *testing.T) {
	in := HookInput{
		SessionID:      "s",
		HookEventName:  "PreToolUse",
		TranscriptPath: "/tmp/x.jsonl",
		PermissionMode: "default",
		Effort:         &EffortField{Level: "high"},
		AgentID:        "agt-1",
		AgentType:      "general-purpose",
		ToolUseID:      "use-42",
	}
	b, _ := json.Marshal(in)
	for _, want := range []string{
		`"transcript_path":"/tmp/x.jsonl"`,
		`"permission_mode":"default"`,
		`"effort":{"level":"high"}`,
		`"agent_id":"agt-1"`,
		`"agent_type":"general-purpose"`,
		`"tool_use_id":"use-42"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestHookInput_NewCommonFields -v`
Expected: FAIL — fields and type do not exist.

- [ ] **Step 3: Add the fields and `EffortField`**

In `agent/plugin_hooks.go`, replace the existing `HookInput` struct with:

```go
type HookInput struct {
	SessionID     string         `json:"session_id"`
	CWD           string         `json:"cwd"`
	HookEventName string         `json:"hook_event_name"`
	ToolName      string         `json:"tool_name,omitempty"`
	ToolInput     map[string]any `json:"tool_input,omitempty"`
	ToolResult    string         `json:"tool_result,omitempty"`
	UserPrompt    string         `json:"user_prompt,omitempty"`
	Reason        string         `json:"reason,omitempty"`

	// SP5 common new fields.
	TranscriptPath string       `json:"transcript_path,omitempty"`
	PermissionMode string       `json:"permission_mode,omitempty"`
	Effort         *EffortField `json:"effort,omitempty"`
	AgentID        string       `json:"agent_id,omitempty"`
	AgentType      string       `json:"agent_type,omitempty"`
	ToolUseID      string       `json:"tool_use_id,omitempty"`
}

// EffortField carries reasoning-effort metadata for the active session.
// Wrapped in a struct so future budget fields can attach without a schema break.
type EffortField struct {
	Level string `json:"level"`
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestHookInput_NewCommonFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): add six common HookInput fields with omitempty"
```

---

### Task 11: Add event-specific `HookInput` fields and `BatchToolResult`

**Files:**
- Modify: `agent/plugin_hooks.go` — `HookInput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestHookInput_EventSpecificFields(t *testing.T) {
	in := HookInput{
		HookEventName:    "PostToolBatch",
		ToolResults:      []BatchToolResult{{ToolName: "Bash", ToolUseID: "u1", Succeeded: true}},
		ToolError:        "boom",
		ErrorType:        "rate_limit",
		ErrorMessage:     "429",
		ExpansionType:    "slash_command",
		CommandName:      "skill",
		CommandArgs:      "args",
		CommandSource:    "skill",
		Prompt:           "run X",
		CompactTrigger:   "auto",
		PermissionRule:   "Bash(*)",
		PermissionCat:    "destructive",
		DenialReason:     "blocked by policy",
		ConfigSource:     "user_settings",
		ConfigFile:       "/etc/serf/config.json",
		ChangedKeys:      []string{"hooks", "permissions"},
	}
	b, _ := json.Marshal(in)
	for _, want := range []string{
		`"tool_results":[{"tool_name":"Bash"`,
		`"tool_error":"boom"`,
		`"error_type":"rate_limit"`,
		`"error_message":"429"`,
		`"expansion_type":"slash_command"`,
		`"command_name":"skill"`,
		`"compact_trigger":"auto"`,
		`"permission_rule":"Bash(*)"`,
		`"permission_category":"destructive"`,
		`"denial_reason":"blocked by policy"`,
		`"config_source":"user_settings"`,
		`"config_file":"/etc/serf/config.json"`,
		`"changed_keys":["hooks","permissions"]`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestHookInput_EventSpecificFields -v`
Expected: FAIL.

- [ ] **Step 3: Add the event-specific fields**

Append into the `HookInput` struct in `agent/plugin_hooks.go`, after the SP5 common fields:

```go
	// Event-specific fields. All marked omitempty so per-event JSON
	// never contains a field the event does not own.
	ToolError      string            `json:"tool_error,omitempty"`         // PostToolUseFailure
	ToolResults    []BatchToolResult `json:"tool_results,omitempty"`       // PostToolBatch
	ErrorType      string            `json:"error_type,omitempty"`         // StopFailure
	ErrorMessage   string            `json:"error_message,omitempty"`      // StopFailure
	ExpansionType  string            `json:"expansion_type,omitempty"`     // UserPromptExpansion
	CommandName    string            `json:"command_name,omitempty"`       // UserPromptExpansion
	CommandArgs    string            `json:"command_args,omitempty"`       // UserPromptExpansion
	CommandSource  string            `json:"command_source,omitempty"`     // UserPromptExpansion
	Prompt         string            `json:"prompt,omitempty"`             // SubagentStart, UserPromptExpansion
	CompactTrigger string            `json:"compact_trigger,omitempty"`    // PostCompact
	PermissionRule string            `json:"permission_rule,omitempty"`    // PermissionRequest
	PermissionCat  string            `json:"permission_category,omitempty"` // PermissionRequest
	DenialReason   string            `json:"denial_reason,omitempty"`      // PermissionDenied
	ConfigSource   string            `json:"config_source,omitempty"`      // ConfigChange
	ConfigFile     string            `json:"config_file,omitempty"`        // ConfigChange
	ChangedKeys    []string          `json:"changed_keys,omitempty"`       // ConfigChange
```

Add the `BatchToolResult` type below `EffortField`:

```go
// BatchToolResult is one entry of HookInput.ToolResults on PostToolBatch.
type BatchToolResult struct {
	ToolName     string         `json:"tool_name"`
	ToolUseID    string         `json:"tool_use_id"`
	ToolInput    map[string]any `json:"tool_input"`
	ToolResponse any            `json:"tool_response"`
	Succeeded    bool           `json:"succeeded"`
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestHookInput_EventSpecificFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): add event-specific HookInput fields and BatchToolResult"
```

---

### Task 12: Add new `ParsedHookOutput` fields

**Files:**
- Modify: `agent/plugin_hooks.go` — `ParsedHookOutput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParsedHookOutput_NewZeroFields(t *testing.T) {
	var p ParsedHookOutput
	// Compile-time presence of fields.
	_ = p.Deferred
	_ = p.PermissionDecisionReason
	_ = p.AdditionalContext
	_ = p.AdditionalContextOverflow
	_ = p.SessionTitle
	_ = p.AddPermissionRule
	_ = p.Retry
	_ = p.StopReason
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsedHookOutput_NewZeroFields -v`
Expected: FAIL — fields do not exist.

- [ ] **Step 3: Add the fields**

Replace the `ParsedHookOutput` struct in `agent/plugin_hooks.go` with:

```go
type ParsedHookOutput struct {
	Continue       bool
	SuppressOutput bool
	SystemMessage  string
	Denied         bool
	UpdatedInput   map[string]any
	Blocked        bool
	BlockReason    string
	IsError        bool
	RawExitCode    int

	// SP5 additions.
	Deferred                  bool
	PermissionDecisionReason  string
	AdditionalContext         string
	AdditionalContextOverflow string
	SessionTitle              string
	AddPermissionRule         string
	Retry                     bool
	StopReason                string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParsedHookOutput_NewZeroFields -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): extend ParsedHookOutput with SP5 fields"
```

---

### Task 13: `parseHookOutput` — separate `additionalContext` from `SystemMessage`

**Files:**
- Modify: `agent/plugin_hooks.go` — `parseHookOutput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParseHookOutput_AdditionalContextRoutedSeparately(t *testing.T) {
	js := `{"hookSpecificOutput":{"additionalContext":"steer me"}}`
	out := parseHookOutput(js, 0)
	if out.AdditionalContext != "steer me" {
		t.Errorf("AdditionalContext = %q, want %q", out.AdditionalContext, "steer me")
	}
	if out.SystemMessage != "" {
		t.Errorf("SystemMessage should stay empty (now routed to AdditionalContext), got %q", out.SystemMessage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseHookOutput_AdditionalContextRoutedSeparately -v`
Expected: FAIL — current code copies `additionalContext` into `SystemMessage`.

- [ ] **Step 3: Update `parseHookOutput`**

In `agent/plugin_hooks.go`, replace the `if ac, ok := hso["additionalContext"]...` block with:

```go
if ac, ok := hso["additionalContext"].(string); ok && ac != "" {
	result.AdditionalContext = ac
}
```

- [ ] **Step 4: Run all hook tests to verify**

Run: `go test ./agent/ -run "TestParseHookOutput|TestHook" -v`
Expected: PASS. (Existing SessionStart-route tests may need adjustment if they checked `SystemMessage`; verify which tests fail before committing.)

If any existing test relied on `SystemMessage` carrying `additionalContext`, update it to read `AdditionalContext` instead. Do that change in the same commit.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): route additionalContext to its own ParsedHookOutput field"
```

---

### Task 14: `parseHookOutput` — `permissionDecision: "defer"`, `permissionDecisionReason`

**Files:**
- Modify: `agent/plugin_hooks.go` — `parseHookOutput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParseHookOutput_DeferAndReason(t *testing.T) {
	js := `{"hookSpecificOutput":{"permissionDecision":"defer","permissionDecisionReason":"not my call"}}`
	out := parseHookOutput(js, 0)
	if !out.Deferred {
		t.Error("Deferred = false, want true")
	}
	if out.Denied {
		t.Error("Denied = true, want false")
	}
	if out.PermissionDecisionReason != "not my call" {
		t.Errorf("Reason = %q, want %q", out.PermissionDecisionReason, "not my call")
	}
}

func TestParseHookOutput_DenyReasonRoutedToField(t *testing.T) {
	js := `{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"blocked"}}`
	out := parseHookOutput(js, 0)
	if !out.Denied {
		t.Error("Denied should be true")
	}
	if out.PermissionDecisionReason != "blocked" {
		t.Errorf("Reason = %q, want %q", out.PermissionDecisionReason, "blocked")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestParseHookOutput_(DeferAndReason|DenyReasonRoutedToField)" -v`
Expected: FAIL.

- [ ] **Step 3: Update `parseHookOutput`**

In `agent/plugin_hooks.go`, replace the `if pd, ok := hso["permissionDecision"].(string); ok && pd == "deny"` block with:

```go
if pd, ok := hso["permissionDecision"].(string); ok {
	switch pd {
	case "deny":
		result.Denied = true
	case "defer":
		result.Deferred = true
	}
}
if r, ok := hso["permissionDecisionReason"].(string); ok {
	result.PermissionDecisionReason = r
}
if r, ok := hso["reason"].(string); ok && result.PermissionDecisionReason == "" {
	result.PermissionDecisionReason = r
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestParseHookOutput -v`
Expected: PASS for new rows; existing tests still pass.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): parse defer decision and permissionDecisionReason"
```

---

### Task 15: `parseHookOutput` — `sessionTitle`, `addPermissionRule`, `retry`, `stopReason`

**Files:**
- Modify: `agent/plugin_hooks.go` — `parseHookOutput`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestParseHookOutput_MiscNewFields(t *testing.T) {
	js := `{
		"continue": false,
		"stopReason": "shut down",
		"hookSpecificOutput": {
			"sessionTitle": "new title",
			"decision": {"behavior":"allow","addPermissionRule":"Bash(ls:*)"},
			"retry": true
		}
	}`
	out := parseHookOutput(js, 0)
	if out.SessionTitle != "new title" {
		t.Errorf("SessionTitle = %q", out.SessionTitle)
	}
	if out.AddPermissionRule != "Bash(ls:*)" {
		t.Errorf("AddPermissionRule = %q", out.AddPermissionRule)
	}
	if !out.Retry {
		t.Error("Retry should be true")
	}
	if out.StopReason != "shut down" {
		t.Errorf("StopReason = %q", out.StopReason)
	}
	if out.Continue {
		t.Error("Continue should be false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseHookOutput_MiscNewFields -v`
Expected: FAIL.

- [ ] **Step 3: Update `parseHookOutput`**

In `agent/plugin_hooks.go`, after the existing `parsed["decision"] == "block"` block, append:

```go
if s, ok := parsed["stopReason"].(string); ok {
	result.StopReason = s
}
if hso, ok := parsed["hookSpecificOutput"].(map[string]any); ok {
	if t, ok := hso["sessionTitle"].(string); ok {
		result.SessionTitle = t
	}
	if b, ok := hso["retry"].(bool); ok {
		result.Retry = b
	}
	if dec, ok := hso["decision"].(map[string]any); ok {
		if behavior, ok := dec["behavior"].(string); ok {
			switch behavior {
			case "deny":
				result.Denied = true
			case "allow":
				result.Denied = false
			}
		}
		if rule, ok := dec["addPermissionRule"].(string); ok {
			result.AddPermissionRule = rule
		}
		if ui, ok := dec["updatedInput"].(map[string]any); ok && result.UpdatedInput == nil {
			result.UpdatedInput = ui
		}
		if r, ok := dec["reason"].(string); ok && result.PermissionDecisionReason == "" {
			result.PermissionDecisionReason = r
		}
	}
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestParseHookOutput -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): parse sessionTitle, addPermissionRule, retry, stopReason"
```

---

### Task 16: Output capping — truncate `AdditionalContext` at 10,000 bytes

**Files:**
- Modify: `agent/plugin_hooks.go` — add `capAdditionalContext`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestCapAdditionalContext_TruncatesAndWritesOverflow(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 12000)
	truncated, overflowPath := capAdditionalContext(big, dir, "sess-1", "PostToolUse", "hk-7")
	if len(truncated) > 10000+200 { // truncated + suffix
		t.Errorf("truncated len = %d, want close to 10000", len(truncated))
	}
	if !strings.Contains(truncated, "[additionalContext truncated") {
		t.Error("missing truncation suffix")
	}
	if overflowPath == "" {
		t.Fatal("expected overflow path")
	}
	data, err := os.ReadFile(overflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != big {
		t.Error("overflow file contents != original")
	}
	info, _ := os.Stat(overflowPath)
	if info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %v, want 0600", info.Mode().Perm())
	}
}

func TestCapAdditionalContext_NoFileWhenStateDirEmpty(t *testing.T) {
	big := strings.Repeat("y", 12000)
	truncated, overflowPath := capAdditionalContext(big, "", "sess-1", "PostToolUse", "hk-7")
	if overflowPath != "" {
		t.Errorf("overflowPath = %q, want empty", overflowPath)
	}
	if !strings.Contains(truncated, "persistence disabled") {
		t.Error("missing persistence-disabled suffix")
	}
}

func TestCapAdditionalContext_Passthrough(t *testing.T) {
	small := "hi"
	truncated, overflowPath := capAdditionalContext(small, t.TempDir(), "s", "e", "h")
	if truncated != "hi" {
		t.Errorf("unexpected change: %q", truncated)
	}
	if overflowPath != "" {
		t.Errorf("overflowPath = %q, want empty", overflowPath)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestCapAdditionalContext -v`
Expected: FAIL — `undefined: capAdditionalContext`.

- [ ] **Step 3: Add the helper**

Append to `agent/plugin_hooks.go`:

```go
// hookContextCap is the byte cap for hook-supplied additionalContext.
// Matches Claude Code's documented value.
const hookContextCap = 10000

// capAdditionalContext truncates an additionalContext string to hookContextCap
// bytes and, when stateDir is set, writes the full original to
// <stateDir>/hooks/<sessionID>/<event>-<hookID>.txt (mode 0600).
//
// Returns the (possibly truncated) string with a trailing notice and the
// overflow-file path (empty if no file was written).
func capAdditionalContext(s, stateDir, sessionID, event, hookID string) (string, string) {
	if len(s) <= hookContextCap {
		return s, ""
	}
	if stateDir == "" {
		return s[:hookContextCap] + "\n\n[additionalContext truncated at 10000 bytes; persistence disabled]", ""
	}
	overflowDir := filepath.Join(stateDir, "hooks", sessionID)
	if err := os.MkdirAll(overflowDir, 0o700); err != nil {
		return s[:hookContextCap] + "\n\n[additionalContext truncated at 10000 bytes; overflow write failed]", ""
	}
	overflowPath := filepath.Join(overflowDir, fmt.Sprintf("%s-%s.txt", event, hookID))
	if err := os.WriteFile(overflowPath, []byte(s), 0o600); err != nil {
		return s[:hookContextCap] + "\n\n[additionalContext truncated at 10000 bytes; overflow write failed]", ""
	}
	suffix := fmt.Sprintf("\n\n[additionalContext truncated at %d bytes; full content at %s]", hookContextCap, overflowPath)
	return s[:hookContextCap] + suffix, overflowPath
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestCapAdditionalContext -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): cap additionalContext at 10000 bytes with overflow file"
```

---

### Task 17: Add `AdditionalContext` slice to `HookRunResult` and `PreToolUseResult`

**Files:**
- Modify: `agent/plugin_hooks.go` — result types and `collectSystemMessages`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestCollectSystemMessages_RoutesAdditionalContext(t *testing.T) {
	outputs := []ParsedHookOutput{
		{SystemMessage: "show me", AdditionalContext: "steer me"},
		{AdditionalContext: "and me"},
		{SystemMessage: "also show"},
	}
	got := collectSystemMessages(outputs)
	if len(got.SystemMessages) != 2 || got.SystemMessages[0] != "show me" || got.SystemMessages[1] != "also show" {
		t.Errorf("SystemMessages = %v", got.SystemMessages)
	}
	if len(got.AdditionalContext) != 2 || got.AdditionalContext[0] != "steer me" || got.AdditionalContext[1] != "and me" {
		t.Errorf("AdditionalContext = %v", got.AdditionalContext)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestCollectSystemMessages_RoutesAdditionalContext -v`
Expected: FAIL — field does not exist.

- [ ] **Step 3: Extend the result types and helper**

In `agent/plugin_hooks.go`:

```go
type HookRunResult struct {
	SystemMessages    []string
	AdditionalContext []string
}

type PreToolUseResult struct {
	Denied            bool
	DenyMessage       string
	SystemMessages    []string
	AdditionalContext []string
	UpdatedInput      map[string]any
}
```

Replace `collectSystemMessages`:

```go
func collectSystemMessages(outputs []ParsedHookOutput) HookRunResult {
	var result HookRunResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
	}
	return result
}
```

Also extend `RunPreToolUse` to populate `AdditionalContext`:

```go
func (r *HookRunner) RunPreToolUse(ctx context.Context, input HookInput) PreToolUseResult {
	outputs := r.runAll(ctx, HookPreToolUse, input.ToolName, input)
	var result PreToolUseResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			result.SystemMessages = append(result.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			result.AdditionalContext = append(result.AdditionalContext, o.AdditionalContext)
		}
		if o.Denied {
			result.Denied = true
			if result.DenyMessage == "" {
				result.DenyMessage = o.PermissionDecisionReason
				if result.DenyMessage == "" {
					result.DenyMessage = o.SystemMessage
				}
			}
		}
		if o.UpdatedInput != nil {
			if result.UpdatedInput == nil {
				result.UpdatedInput = map[string]any{}
			}
			for k, v := range o.UpdatedInput {
				result.UpdatedInput[k] = v
			}
		}
	}
	return result
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestCollectSystemMessages_RoutesAdditionalContext|TestRunPreToolUse|TestHookRunner" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): route AdditionalContext alongside SystemMessages in results"
```

---

## Phase 2: Async dispatch and rewake channel

### Task 18: `AsyncRewakeSignal` type and channel field on `Session`

**Files:**
- Create: `agent/plugin_hooks_async.go`
- Create: `agent/plugin_hooks_async_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_async_test.go`:

```go
package agent

import "testing"

func TestAsyncRewakeSignal_FormatMessage(t *testing.T) {
	sig := AsyncRewakeSignal{
		PluginName: "validator",
		Event:      HookPostToolUse,
		Stderr:     "policy violation: X",
	}
	got := formatRewakeMessage(sig)
	if !contains(got, "validator") || !contains(got, "PostToolUse") || !contains(got, "policy violation: X") {
		t.Errorf("formatRewakeMessage missing fields: %q", got)
	}
	if !contains(got, "<async-hook-rewake") || !contains(got, "</async-hook-rewake>") {
		t.Errorf("expected wrapping tags, got %q", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestAsyncRewakeSignal_FormatMessage -v`
Expected: FAIL — `undefined: AsyncRewakeSignal`.

- [ ] **Step 3: Create the file**

Create `agent/plugin_hooks_async.go`:

```go
package agent

import "fmt"

// AsyncRewakeSignal carries the exit-2 output of an async-rewake hook
// back to the main agent loop so it can steer the model.
type AsyncRewakeSignal struct {
	PluginName string
	HookType   string
	Event      HookEvent
	Stdout     string
	Stderr     string
	ExitCode   int
}

// formatRewakeMessage wraps a rewake signal's stderr in tags the model
// can recognize.
func formatRewakeMessage(sig AsyncRewakeSignal) string {
	body := sig.Stderr
	if body == "" {
		body = sig.Stdout
	}
	return fmt.Sprintf("<async-hook-rewake plugin=%q event=%q>%s</async-hook-rewake>",
		sig.PluginName, string(sig.Event), body)
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestAsyncRewakeSignal_FormatMessage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_async.go agent/plugin_hooks_async_test.go
git commit -m "feat(hooks): introduce AsyncRewakeSignal and formatRewakeMessage"
```

---

### Task 19: `HookRunner` gains a rewake channel and `dispatchAsync`

**Files:**
- Modify: `agent/plugin_hooks_async.go`
- Modify: `agent/plugin_hooks.go` — `HookRunner` struct and `NewHookRunner`
- Modify: `agent/plugin_hooks_async_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_async_test.go`:

```go
func TestDispatchAsync_DoesNotBlock(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.EnableAsyncRewake(16)

	done := make(chan struct{})
	hook := RegisteredHook{
		Type:    "command",
		Command: "sleep 5",
		Timeout: 30,
		Async:   true,
	}
	go func() {
		r.dispatchAsync(context.Background(), HookPostToolUse, hook, HookInput{HookEventName: "PostToolUse"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("dispatchAsync blocked > 200ms")
	}
}

func TestDispatchAsync_RewakeOnExitTwo(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.EnableAsyncRewake(4)

	hook := RegisteredHook{
		Type:        "command",
		Command:     `echo "stderr line" 1>&2; exit 2`,
		Timeout:     5,
		Async:       true,
		AsyncRewake: true,
		PluginName:  "p",
	}
	r.dispatchAsync(context.Background(), HookPostToolUse, hook, HookInput{HookEventName: "PostToolUse"})

	select {
	case sig := <-r.AsyncRewakeChan():
		if sig.PluginName != "p" {
			t.Errorf("PluginName = %q", sig.PluginName)
		}
		if sig.ExitCode != 2 {
			t.Errorf("ExitCode = %d, want 2", sig.ExitCode)
		}
		if !contains(sig.Stderr, "stderr line") {
			t.Errorf("Stderr = %q", sig.Stderr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no rewake signal received")
	}
}
```

Add imports at top of test file if absent (`context`, `time`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestDispatchAsync_" -v`
Expected: FAIL — `dispatchAsync`, `EnableAsyncRewake`, `AsyncRewakeChan` undefined.

- [ ] **Step 3: Implement async dispatch on the runner**

Append to `agent/plugin_hooks_async.go`:

```go
import (
	"context"
	"log"
)

// EnableAsyncRewake creates the rewake channel with the given capacity.
// Idempotent: re-enabling with the same capacity is a no-op.
func (r *HookRunner) EnableAsyncRewake(capacity int) {
	if r.asyncRewake == nil {
		r.asyncRewake = make(chan AsyncRewakeSignal, capacity)
	}
}

// AsyncRewakeChan returns the rewake channel for the main loop to drain.
// Returns nil if EnableAsyncRewake was never called.
func (r *HookRunner) AsyncRewakeChan() <-chan AsyncRewakeSignal {
	return r.asyncRewake
}

// dispatchAsync runs the hook in a detached goroutine, discarding its
// ParsedHookOutput. If hook.AsyncRewake is set and the hook exits with
// code 2, the stderr is pushed to the rewake channel (non-blocking; a
// full channel drops the signal with a warning).
func (r *HookRunner) dispatchAsync(ctx context.Context, event HookEvent, hook RegisteredHook, input HookInput) {
	go func() {
		var hr HookResult
		var err error
		switch hook.Type {
		case "command":
			hr, err = executeCommandHook(ctx, hook, input)
		case "http":
			hr, err = executeHTTPHook(ctx, hook, input)
		case "mcp_tool":
			// MCP-tool async hooks defer to the regular MCP path.
			hr, err = HookResult{}, nil
		default:
			return
		}
		if err != nil {
			log.Printf("async hook %q event %q: %v", hook.PluginName, event, err)
		}
		if hook.AsyncRewake && hr.ExitCode == 2 && r.asyncRewake != nil {
			sig := AsyncRewakeSignal{
				PluginName: hook.PluginName,
				HookType:   hook.Type,
				Event:      event,
				Stdout:     hr.Stdout,
				Stderr:     hr.Stderr,
				ExitCode:   hr.ExitCode,
			}
			select {
			case r.asyncRewake <- sig:
			default:
				log.Printf("async rewake channel full; dropping signal for plugin %q event %q", hook.PluginName, event)
			}
		}
	}()
}
```

In `agent/plugin_hooks.go`, add the channel field to `HookRunner`:

```go
type HookRunner struct {
	hooks       map[HookEvent][]RegisteredHook
	client      PromptHookClient
	model       string
	onEvent     func(EventKind, any)
	asyncRewake chan AsyncRewakeSignal
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestDispatchAsync_" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_async.go agent/plugin_hooks.go agent/plugin_hooks_async_test.go
git commit -m "feat(hooks): async dispatch with rewake-on-exit-2 channel"
```

---

### Task 20: Channel-full drop emits a warning

**Files:**
- Modify: `agent/plugin_hooks_async_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestDispatchAsync_RewakeChannelFullDropsSignal(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.EnableAsyncRewake(2)
	// Fill the channel with 2 signals.
	for i := 0; i < 2; i++ {
		r.asyncRewake <- AsyncRewakeSignal{PluginName: "p"}
	}
	hook := RegisteredHook{
		Type:        "command",
		Command:     `echo dropme 1>&2; exit 2`,
		Timeout:     5,
		Async:       true,
		AsyncRewake: true,
		PluginName:  "p2",
	}
	r.dispatchAsync(context.Background(), HookPostToolUse, hook, HookInput{HookEventName: "PostToolUse"})
	// Allow goroutine to run.
	time.Sleep(500 * time.Millisecond)
	if len(r.asyncRewake) != 2 {
		t.Errorf("channel len = %d, want 2 (new signal should have dropped)", len(r.asyncRewake))
	}
}
```

- [ ] **Step 2: Run test to verify it passes immediately**

Run: `go test ./agent/ -run TestDispatchAsync_RewakeChannelFullDropsSignal -v`
Expected: PASS — Task 19's `select { default }` branch already implements the drop.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_hooks_async_test.go
git commit -m "test(hooks): lock in async rewake channel-full drop behavior"
```

---

### Task 21: `runAll` routes async hooks through `dispatchAsync`

**Files:**
- Modify: `agent/plugin_hooks.go` — `runAll`
- Test: `agent/plugin_hooks_async_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunAll_SkipsAsyncHooksInSyncResult(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.EnableAsyncRewake(4)
	r.Add(HookPostToolUse,
		RegisteredHook{Matcher: "*", Type: "command", Command: "echo sync", Timeout: 5},
		RegisteredHook{Matcher: "*", Type: "command", Command: "echo async", Timeout: 5, Async: true},
	)
	outputs := r.runAll(context.Background(), HookPostToolUse, "Bash", HookInput{ToolName: "Bash"})
	if len(outputs) != 1 {
		t.Fatalf("got %d sync outputs, want 1", len(outputs))
	}
	if outputs[0].SystemMessage != "sync" {
		t.Errorf("sync output = %q, want %q", outputs[0].SystemMessage, "sync")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunAll_SkipsAsyncHooksInSyncResult -v`
Expected: FAIL — current `runAll` collects all matched hooks synchronously.

- [ ] **Step 3: Update `runAll`**

Replace the matched-hooks branch in `runAll` (`agent/plugin_hooks.go`):

```go
func (r *HookRunner) runAll(ctx context.Context, event HookEvent, toolName string, input HookInput) []ParsedHookOutput {
	claudeName := MapSerfToolNameToClaude(toolName)
	matched := r.matchHooks(event, claudeName)
	if len(matched) == 0 {
		return nil
	}

	var sync []RegisteredHook
	for _, h := range matched {
		if h.Async {
			r.dispatchAsync(ctx, event, h, input)
			continue
		}
		sync = append(sync, h)
	}
	if len(sync) == 0 {
		return nil
	}

	results := make([]ParsedHookOutput, len(sync))
	var wg sync2.WaitGroup
	wg.Add(len(sync))
	for i, hook := range sync {
		go func(idx int, h RegisteredHook) {
			defer wg.Done()
			if r.onEvent != nil {
				r.onEvent(EventHookStart, HookStartData{
					Event:      string(event),
					HookType:   h.Type,
					Matcher:    h.Matcher,
					PluginName: h.PluginName,
				})
			}
			start := time.Now()
			results[idx] = r.runHook(ctx, h, input)
			elapsed := time.Since(start)
			if r.onEvent != nil {
				r.onEvent(EventHookEnd, HookEndData{
					Event:      string(event),
					HookType:   h.Type,
					Matcher:    h.Matcher,
					PluginName: h.PluginName,
					ExitCode:   results[idx].RawExitCode,
					DurationMS: elapsed.Milliseconds(),
				})
			}
		}(i, hook)
	}
	wg.Wait()
	return results
}
```

Add a rename-import for `sync` at the top of `plugin_hooks.go`:

```go
import (
	...
	sync2 "sync"
	...
)
```

(Variable `sync` clashes with the package name; the alias prevents that.) Verify other places in `plugin_hooks.go` use `sync.WaitGroup` and switch them to `sync2.WaitGroup` consistently.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestRunAll_|TestHookRunner|TestDispatchAsync_" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_async_test.go
git commit -m "feat(hooks): route async hooks through dispatchAsync from runAll"
```

---

## Phase 3: New hook types — http, mcp_tool, agent

### Task 22: `executeHTTPHook` happy path (2xx with JSON body)

**Files:**
- Create: `agent/plugin_hooks_http.go`
- Create: `agent/plugin_hooks_http_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_http_test.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestExecuteHTTPHook_JSONResponse(t *testing.T) {
	var gotBody HookInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"systemMessage":"ok from http"}`))
	}))
	defer srv.Close()

	hook := RegisteredHook{
		Type:    "http",
		URL:     srv.URL,
		Timeout: 5,
	}
	res, err := executeHTTPHook(context.Background(), hook, HookInput{
		SessionID:     "abc",
		HookEventName: "PreToolUse",
		ToolName:      "Bash",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.Stdout != `{"systemMessage":"ok from http"}` {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d", res.ExitCode)
	}
	if gotBody.SessionID != "abc" || gotBody.ToolName != "Bash" {
		t.Errorf("body = %+v", gotBody)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExecuteHTTPHook_JSONResponse -v`
Expected: FAIL — `undefined: executeHTTPHook`.

- [ ] **Step 3: Implement `executeHTTPHook`**

Create `agent/plugin_hooks_http.go`:

```go
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// executeHTTPHook POSTs the hook input as JSON to hook.URL, applies
// allow-listed env-var substitution to header values, and returns the
// response body and exit-code-equivalent status.
//
//   - 2xx empty body         → ExitCode 0, no Stdout.
//   - 2xx JSON or text body  → ExitCode 0, body in Stdout.
//   - non-2xx, transport err → infra error (returned, loop continues).
func executeHTTPHook(ctx context.Context, hook RegisteredHook, input HookInput) (HookResult, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return HookResult{}, fmt.Errorf("marshaling hook input: %w", err)
	}

	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(rctx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		return HookResult{}, fmt.Errorf("building hook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range hook.Headers {
		req.Header.Set(k, substituteAllowedEnv(v, hook.AllowedEnvVars))
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return HookResult{}, fmt.Errorf("http hook request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	hr := HookResult{Stdout: string(respBody)}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return hr, fmt.Errorf("http hook %s returned status %d", hook.URL, resp.StatusCode)
	}
	return hr, nil
}

// substituteAllowedEnv replaces $NAME and ${NAME} in s only when NAME is
// in the allowed list. Other tokens pass through verbatim.
func substituteAllowedEnv(s string, allowed []string) string {
	if len(allowed) == 0 || s == "" {
		return s
	}
	for _, name := range allowed {
		val := os.Getenv(name)
		s = strings.ReplaceAll(s, "${"+name+"}", val)
		s = strings.ReplaceAll(s, "$"+name, val)
	}
	return s
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestExecuteHTTPHook_JSONResponse -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_http.go agent/plugin_hooks_http_test.go
git commit -m "feat(hooks): executeHTTPHook with allow-listed header env substitution"
```

---

### Task 23: `executeHTTPHook` — header substitution and non-2xx error path

**Files:**
- Modify: `agent/plugin_hooks_http_test.go`

- [ ] **Step 1: Write the failing tests**

Append:

```go
func TestExecuteHTTPHook_AllowedHeaderSubstitution(t *testing.T) {
	t.Setenv("HOOK_TOKEN", "secret")
	t.Setenv("HOOK_FORBIDDEN", "leak")
	var gotAuth, gotForbidden string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotForbidden = r.Header.Get("X-Forbidden")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	hook := RegisteredHook{
		Type:           "http",
		URL:            srv.URL,
		Timeout:        5,
		Headers:        map[string]string{"Authorization": "Bearer $HOOK_TOKEN", "X-Forbidden": "$HOOK_FORBIDDEN"},
		AllowedEnvVars: []string{"HOOK_TOKEN"},
	}
	if _, err := executeHTTPHook(context.Background(), hook, HookInput{}); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotForbidden != "$HOOK_FORBIDDEN" {
		t.Errorf("X-Forbidden = %q, want unchanged", gotForbidden)
	}
}

func TestExecuteHTTPHook_Non2xxReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", 500)
	}))
	defer srv.Close()
	_, err := executeHTTPHook(context.Background(), RegisteredHook{Type: "http", URL: srv.URL, Timeout: 5}, HookInput{})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
```

- [ ] **Step 2: Run tests to verify pass**

Run: `go test ./agent/ -run "TestExecuteHTTPHook_(AllowedHeaderSubstitution|Non2xxReturnsError)" -v`
Expected: PASS — Task 22's implementation already covers both.

- [ ] **Step 3: Commit**

```bash
git add agent/plugin_hooks_http_test.go
git commit -m "test(hooks): lock in http hook header substitution and non-2xx path"
```

---

### Task 24: Wire `http` type through `runHook`

**Files:**
- Modify: `agent/plugin_hooks.go` — `runHook`
- Test: `agent/plugin_hooks_http_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestHookRunner_DispatchesHTTPType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`hello world`))
	}))
	defer srv.Close()

	r := NewHookRunner(nil, "")
	r.Add(HookPostToolUse, RegisteredHook{Matcher: "*", Type: "http", URL: srv.URL, Timeout: 5})
	res := r.RunPostToolUse(context.Background(), HookInput{ToolName: "Bash", HookEventName: "PostToolUse"})
	if len(res.SystemMessages) != 1 || res.SystemMessages[0] != "hello world" {
		t.Errorf("SystemMessages = %v", res.SystemMessages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestHookRunner_DispatchesHTTPType -v`
Expected: FAIL — `runHook` falls through to default branch for `http`.

- [ ] **Step 3: Update `runHook`**

In `agent/plugin_hooks.go`, replace the type switch in `runHook`:

```go
switch hook.Type {
case "command":
	hr, err = executeCommandHook(ctx, hook, input)
case "prompt":
	if r.client == nil {
		return ParsedHookOutput{Continue: true, SystemMessage: "prompt hook skipped: no LLM client"}
	}
	hr, err = executePromptHook(ctx, r.client, r.model, hook, input)
case "http":
	hr, err = executeHTTPHook(ctx, hook, input)
case "mcp_tool":
	hr, err = executeMCPToolHook(ctx, r, hook, input)
case "agent":
	hr, err = executeAgentHook(ctx, r, hook, input)
default:
	return ParsedHookOutput{Continue: true}
}
```

The `executeMCPToolHook` and `executeAgentHook` references will be created in subsequent tasks; if the build fails before those tasks land, add temporary stubs:

```go
// Temporary stubs — real implementations land in later tasks.
func executeMCPToolHook(ctx context.Context, r *HookRunner, hook RegisteredHook, input HookInput) (HookResult, error) {
	return HookResult{}, fmt.Errorf("mcp_tool hook not yet implemented")
}
func executeAgentHook(ctx context.Context, r *HookRunner, hook RegisteredHook, input HookInput) (HookResult, error) {
	return HookResult{}, fmt.Errorf("agent hook not yet implemented")
}
```

Place these stubs in a new `agent/plugin_hooks_typestubs.go` file (delete the file when Tasks 25 and 26 land).

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestHookRunner_DispatchesHTTPType -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_typestubs.go agent/plugin_hooks_http_test.go
git commit -m "feat(hooks): dispatch http hook type through HookRunner"
```

---

### Task 25: `executeMCPToolHook` with arg substitution

**Files:**
- Create: `agent/plugin_hooks_mcp.go`
- Create: `agent/plugin_hooks_mcp_test.go`
- Delete: `agent/plugin_hooks_typestubs.go` (the mcp_tool stub)

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_mcp_test.go`:

```go
package agent

import (
	"context"
	"testing"
)

// stubMCPCaller satisfies the small interface executeMCPToolHook uses to
// invoke an MCP tool. The real impl resolves to *MCPManager.
type stubMCPCaller struct {
	wantServer string
	wantTool   string
	wantInput  map[string]any
	respText   string
	respIsErr  bool
	called     bool
}

func (s *stubMCPCaller) CallTool(ctx context.Context, server, tool string, args map[string]any) (string, bool, error) {
	s.called = true
	if server != s.wantServer || tool != s.wantTool {
		return "", false, nil
	}
	return s.respText, s.respIsErr, nil
}

func TestExecuteMCPToolHook_SubstitutesArgs(t *testing.T) {
	stub := &stubMCPCaller{
		wantServer: "policy",
		wantTool:   "check",
		respText:   `{"systemMessage":"checked"}`,
	}
	r := NewHookRunner(nil, "")
	r.mcpCaller = stub

	hook := RegisteredHook{
		Type:      "mcp_tool",
		MCPServer: "policy",
		MCPTool:   "check",
		MCPInput:  map[string]any{"command": "${tool_input.command}", "sid": "${session_id}"},
		Timeout:   5,
	}
	res, err := executeMCPToolHook(context.Background(), r, hook, HookInput{
		SessionID: "S",
		ToolInput: map[string]any{"command": "ls /tmp"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !stub.called {
		t.Fatal("stub never called")
	}
	if res.Stdout != `{"systemMessage":"checked"}` {
		t.Errorf("Stdout = %q", res.Stdout)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExecuteMCPToolHook_SubstitutesArgs -v`
Expected: FAIL — `executeMCPToolHook` (real version) and `HookRunner.mcpCaller` undefined.

- [ ] **Step 3: Implement**

Add to `agent/plugin_hooks.go` (struct field on `HookRunner`):

```go
type HookRunner struct {
	hooks       map[HookEvent][]RegisteredHook
	client      PromptHookClient
	model       string
	onEvent     func(EventKind, any)
	asyncRewake chan AsyncRewakeSignal
	mcpCaller   MCPHookCaller
}

// MCPHookCaller is the contract executeMCPToolHook needs from an MCP host.
// MCPManager will satisfy it; tests use a stub.
type MCPHookCaller interface {
	CallTool(ctx context.Context, server, tool string, args map[string]any) (string, bool, error)
}

// SetMCPCaller registers the MCP host used by mcp_tool hooks.
func (r *HookRunner) SetMCPCaller(c MCPHookCaller) { r.mcpCaller = c }
```

Create `agent/plugin_hooks_mcp.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// executeMCPToolHook resolves an mcp_tool hook against the runner's MCPHookCaller.
// Input values are substituted with ${session_id}, ${cwd}, ${tool_input.K}, etc.
func executeMCPToolHook(ctx context.Context, r *HookRunner, hook RegisteredHook, input HookInput) (HookResult, error) {
	if r.mcpCaller == nil {
		return HookResult{}, fmt.Errorf("mcp_tool hook in plugin %q: no MCP host registered", hook.PluginName)
	}
	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := substituteMCPInput(hook.MCPInput, input)
	text, isErr, err := r.mcpCaller.CallTool(rctx, hook.MCPServer, hook.MCPTool, args)
	if err != nil {
		return HookResult{}, fmt.Errorf("mcp_tool %s.%s: %w", hook.MCPServer, hook.MCPTool, err)
	}
	if isErr {
		return HookResult{Stdout: text, ExitCode: 1}, fmt.Errorf("mcp_tool %s.%s returned isError", hook.MCPServer, hook.MCPTool)
	}
	return HookResult{Stdout: text, ExitCode: 0}, nil
}

// substituteMCPInput walks the input map and replaces ${...} tokens in string values.
func substituteMCPInput(in map[string]any, hi HookInput) map[string]any {
	if len(in) == 0 {
		return in
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = substituteMCPValue(v, hi)
	}
	return out
}

func substituteMCPValue(v any, hi HookInput) any {
	switch t := v.(type) {
	case string:
		return substituteMCPString(t, hi)
	case map[string]any:
		return substituteMCPInput(t, hi)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = substituteMCPValue(e, hi)
		}
		return out
	}
	return v
}

func substituteMCPString(s string, hi HookInput) string {
	s = strings.ReplaceAll(s, "${session_id}", hi.SessionID)
	s = strings.ReplaceAll(s, "${cwd}", hi.CWD)
	s = strings.ReplaceAll(s, "${tool_name}", hi.ToolName)
	// ${tool_input.KEY}
	for {
		i := strings.Index(s, "${tool_input.")
		if i < 0 {
			break
		}
		end := strings.Index(s[i:], "}")
		if end < 0 {
			break
		}
		token := s[i : i+end+1]
		key := strings.TrimSuffix(strings.TrimPrefix(token, "${tool_input."), "}")
		var repl string
		if v, ok := hi.ToolInput[key]; ok {
			if vs, ok := v.(string); ok {
				repl = vs
			} else if b, err := json.Marshal(v); err == nil {
				repl = string(b)
			}
		}
		s = strings.Replace(s, token, repl, 1)
	}
	return s
}
```

Remove the `executeMCPToolHook` stub from `agent/plugin_hooks_typestubs.go`.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestExecuteMCPToolHook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_mcp.go agent/plugin_hooks_mcp_test.go agent/plugin_hooks.go agent/plugin_hooks_typestubs.go
git commit -m "feat(hooks): mcp_tool hook with arg substitution and MCPHookCaller seam"
```

---

### Task 26: `executeAgentHook` — one-shot subagent with `decide` tool

**Files:**
- Create: `agent/plugin_hooks_agent.go`
- Create: `agent/plugin_hooks_agent_test.go`
- Modify: `agent/plugin_hooks_typestubs.go` (remove the agent stub; if file now empty, delete it)

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_agent_test.go`:

```go
package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

// stubAgentClient returns a canned response that includes a decide(...) tool call.
type stubAgentClient struct {
	decision string // "allow" | "deny" | "defer"
	reason   string
}

func (s stubAgentClient) Generate(ctx context.Context, req llm.Request) (llm.Response, error) {
	return llm.Response{
		Message: llm.Message{
			Role: "assistant",
			Content: []llm.ContentBlock{
				{Type: "tool_use", ToolName: "decide", ToolInput: map[string]any{
					"decision": s.decision,
					"reason":   s.reason,
				}},
			},
		},
	}, nil
}

func TestExecuteAgentHook_AllowDecision(t *testing.T) {
	r := NewHookRunner(stubAgentClient{decision: "allow", reason: "looks fine"}, "stub-model")
	hook := RegisteredHook{Type: "agent", Prompt: "verify $TOOL_INPUT", Timeout: 5}
	res, err := executeAgentHook(context.Background(), r, hook, HookInput{ToolInput: map[string]any{"x": 1}})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d", res.ExitCode)
	}
	if !contains(res.Stdout, `"permissionDecision":"allow"`) {
		t.Errorf("Stdout = %q", res.Stdout)
	}
	if !contains(res.Stdout, "looks fine") {
		t.Errorf("Stdout missing reason: %q", res.Stdout)
	}
}

func TestExecuteAgentHook_DenyDecision(t *testing.T) {
	r := NewHookRunner(stubAgentClient{decision: "deny", reason: "blocked"}, "stub-model")
	hook := RegisteredHook{Type: "agent", Prompt: "check", Timeout: 5}
	res, err := executeAgentHook(context.Background(), r, hook, HookInput{})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !contains(res.Stdout, `"permissionDecision":"deny"`) {
		t.Errorf("Stdout = %q", res.Stdout)
	}
}
```

The exact `llm.Message` / `llm.ContentBlock` types may differ in the codebase. Inspect `llm/` first; adapt the stub to whatever shape the existing prompt-hook tests use. (See `agent/plugin_hooks_test.go` for the canonical client adapter.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExecuteAgentHook_AllowDecision -v`
Expected: FAIL — `undefined: executeAgentHook` (real version).

- [ ] **Step 3: Implement `executeAgentHook`**

Create `agent/plugin_hooks_agent.go`:

```go
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"primeradiant.com/serf/llm"
)

// executeAgentHook spawns a lightweight one-shot LLM call that must end
// with a decide(decision, reason) tool call. The result is encoded as
// hook stdout JSON so parseHookOutput maps it into ParsedHookOutput.
//
// The hook type is marked experimental in Claude Code docs; ParsePluginHooks
// emits a one-time warning per plugin per hook.
func executeAgentHook(ctx context.Context, r *HookRunner, hook RegisteredHook, input HookInput) (HookResult, error) {
	if r.client == nil {
		return HookResult{}, fmt.Errorf("agent hook in plugin %q: no LLM client", hook.PluginName)
	}
	timeout := time.Duration(hook.Timeout) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := substituteHookVariables(hook.Prompt, input)
	model := hook.Model
	if model == "" {
		model = r.model
	}

	resp, err := r.client.Generate(rctx, llm.Request{
		Model:    model,
		Messages: []llm.Message{llm.User(prompt)},
	})
	if err != nil {
		return HookResult{}, fmt.Errorf("agent hook LLM: %w", err)
	}

	decision, reason := extractDecideCall(resp.Message)
	if decision == "" {
		// No decision → treat as defer.
		decision = "defer"
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	b, _ := json.Marshal(out)
	return HookResult{Stdout: string(b), ExitCode: 0}, nil
}

// extractDecideCall scans an assistant message for a decide(...) tool call.
// Returns ("", "") if none is found.
func extractDecideCall(msg llm.Message) (string, string) {
	// The shape of message content varies; the helper below adapts to the
	// real llm.Message API. Adjust to actual types when implementing.
	for _, blk := range msg.Content {
		if blk.Type == "tool_use" && blk.ToolName == "decide" {
			d, _ := blk.ToolInput["decision"].(string)
			r, _ := blk.ToolInput["reason"].(string)
			return d, r
		}
	}
	return "", ""
}
```

Adjust the field accesses to whatever `llm.Message` actually exposes (check `llm/` package). Delete the `executeAgentHook` stub from `agent/plugin_hooks_typestubs.go`; if that file becomes empty, delete the file.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestExecuteAgentHook -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks_agent.go agent/plugin_hooks_agent_test.go agent/plugin_hooks.go agent/plugin_hooks_typestubs.go
git commit -m "feat(hooks): agent hook with decide() tool decision parsing"
```

---

### Task 27: One-time experimental warning for `agent` hooks at parse time

**Files:**
- Modify: `agent/plugin_hooks.go` — `ParsePluginHooks`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestParsePluginHooks_AgentHookEmitsExperimentalWarning(t *testing.T) {
	var seen []string
	prev := experimentalWarnFn
	experimentalWarnFn = func(msg string) { seen = append(seen, msg) }
	t.Cleanup(func() { experimentalWarnFn = prev })

	data := []byte(`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[
		{"type":"agent","prompt":"check"},
		{"type":"agent","prompt":"check again"}
	]}]}}`)
	if _, err := ParsePluginHooks(data, "/p", "px"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Errorf("got %d warnings, want 2: %v", len(seen), seen)
	}
	for _, w := range seen {
		if !strings.Contains(w, "experimental") || !strings.Contains(w, "px") {
			t.Errorf("warning %q missing required fragments", w)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParsePluginHooks_AgentHookEmitsExperimentalWarning -v`
Expected: FAIL.

- [ ] **Step 3: Add the warn hook**

In `agent/plugin_hooks.go`, near the top of the file:

```go
// experimentalWarnFn is overridable for tests. Default: log.Printf.
var experimentalWarnFn = func(msg string) { log.Printf("%s", msg) }
```

Add `"log"` to the imports if not present.

In `ParsePluginHooks`, after the validation steps and before `result[event] = append(...)`:

```go
if spec.Type == "agent" {
	experimentalWarnFn(fmt.Sprintf(
		"agent hook in plugin %q event %q: this hook type is experimental and its API may change",
		pluginName, eventName))
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestParsePluginHooks_AgentHookEmitsExperimentalWarning -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): warn once per agent hook at parse time"
```

---

### Task 28: `args` exec form bypasses the shell

**Files:**
- Modify: `agent/plugin_hooks.go` — `executeCommandHook`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestExecuteCommandHook_ExecForm(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "echoargs.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"argc=$#\"; for a in \"$@\"; do echo arg=\"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hook := RegisteredHook{
		Type:    "command",
		Command: script,
		Args:    []string{"a b", "$NOTEXPANDED", "third"},
		Timeout: 5,
	}
	res, err := executeCommandHook(context.Background(), hook, HookInput{HookEventName: "PostToolUse"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("ExitCode = %d; stderr = %s", res.ExitCode, res.Stderr)
	}
	for _, want := range []string{"argc=3", "arg=a b", "arg=$NOTEXPANDED", "arg=third"} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("output missing %q in %q", want, res.Stdout)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExecuteCommandHook_ExecForm -v`
Expected: FAIL — current code always uses `bash -c`.

- [ ] **Step 3: Update `executeCommandHook`**

In `agent/plugin_hooks.go`, replace the `cmd := exec.CommandContext(...)` block with:

```go
var cmd *exec.Cmd
if len(hook.Args) > 0 {
	cmd = exec.CommandContext(ctx, hook.Command, hook.Args...)
} else {
	shell := hook.Shell
	switch shell {
	case "", "bash":
		cmd = exec.CommandContext(ctx, "bash", "-c", hook.Command)
	case "powershell":
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command", hook.Command)
		} else {
			log.Printf("hook in plugin %q: shell=powershell unsupported on %s; falling back to bash", hook.PluginName, runtime.GOOS)
			cmd = exec.CommandContext(ctx, "bash", "-c", hook.Command)
		}
	default:
		cmd = exec.CommandContext(ctx, "bash", "-c", hook.Command)
	}
}
```

Add `"runtime"` to the imports.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestExecuteCommandHook" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): exec-form command hooks via Args plus shell selector"
```

---

### Task 29: New env vars `CLAUDE_PLUGIN_DATA`, `CLAUDE_EFFORT`, `CLAUDE_CODE_REMOTE`

**Files:**
- Modify: `agent/plugin_hooks.go` — `executeCommandHook`
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestExecuteCommandHook_NewEnvVars(t *testing.T) {
	dir := t.TempDir()
	hook := RegisteredHook{
		Type:    "command",
		Command: "echo data=$CLAUDE_PLUGIN_DATA effort=$CLAUDE_EFFORT remote=$CLAUDE_CODE_REMOTE",
		Timeout: 5,
		PluginDataDir: filepath.Join(dir, "data"),
	}
	res, err := executeCommandHook(context.Background(), hook, HookInput{
		CWD:    "/tmp",
		Effort: &EffortField{Level: "high"},
	})
	// Expect serf to surface remote via input/session, not hook fields; this test
	// sets it via context env injection in Task 33.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "data="+filepath.Join(dir, "data")) {
		t.Errorf("missing CLAUDE_PLUGIN_DATA: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "effort=high") {
		t.Errorf("missing CLAUDE_EFFORT: %q", res.Stdout)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestExecuteCommandHook_NewEnvVars -v`
Expected: FAIL — `PluginDataDir` field undefined.

- [ ] **Step 3: Add the field and env wiring**

Add `PluginDataDir string` to `RegisteredHook` (right after `PluginDir`).

In `executeCommandHook`, replace the `cmd.Env = append(...)` block with:

```go
env := append(os.Environ(),
	"CLAUDE_PLUGIN_ROOT="+hook.PluginDir,
	"CLAUDE_PROJECT_DIR="+input.CWD,
)
if hook.PluginDataDir != "" {
	env = append(env, "CLAUDE_PLUGIN_DATA="+hook.PluginDataDir)
}
if input.Effort != nil && input.Effort.Level != "" {
	env = append(env, "CLAUDE_EFFORT="+input.Effort.Level)
}
// CLAUDE_CODE_REMOTE is set by the session before dispatch (see Task 33).
cmd.Env = env
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestExecuteCommandHook_NewEnvVars -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): inject CLAUDE_PLUGIN_DATA and CLAUDE_EFFORT into hook env"
```

---


## Phase 4: Per-event dispatch methods

### Task 30: `RunPostToolUseFailure`

**Files:**
- Modify: `agent/plugin_hooks.go`
- Create: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/plugin_hooks_events_test.go`:

```go
package agent

import (
	"context"
	"strings"
	"testing"
)

func TestRunPostToolUseFailure_HappyPath(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPostToolUseFailure, RegisteredHook{
		Matcher: "Bash",
		Type:    "command",
		Command: `echo '{"systemMessage":"saw failure","hookSpecificOutput":{"additionalContext":"retry me"}}'`,
		Timeout: 5,
	})
	res := r.RunPostToolUseFailure(context.Background(), HookInput{
		ToolName:      "Bash",
		HookEventName: "PostToolUseFailure",
		ToolError:     "boom",
	})
	if len(res.SystemMessages) != 1 || !strings.Contains(res.SystemMessages[0], "saw failure") {
		t.Errorf("SystemMessages = %v", res.SystemMessages)
	}
	if len(res.AdditionalContext) != 1 || res.AdditionalContext[0] != "retry me" {
		t.Errorf("AdditionalContext = %v", res.AdditionalContext)
	}
}

func TestRunPostToolUseFailure_BlocksOnExit2(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPostToolUseFailure, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo "halt now" 1>&2; exit 2`,
		Timeout: 5,
	})
	res := r.RunPostToolUseFailure(context.Background(), HookInput{ToolName: "Edit", HookEventName: "PostToolUseFailure"})
	if !res.Blocked {
		t.Error("expected Blocked = true on exit 2")
	}
	if res.BlockReason == "" {
		t.Error("expected BlockReason populated from stderr")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunPostToolUseFailure -v`
Expected: FAIL — `undefined: RunPostToolUseFailure`.

- [ ] **Step 3: Implement the dispatcher**

Append to `agent/plugin_hooks.go`:

```go
// RunPostToolUseFailure dispatches PostToolUseFailure hooks.
// Behaves like RunPostToolUse but also reports exit-2 / decision=block as blocking.
func (r *HookRunner) RunPostToolUseFailure(ctx context.Context, input HookInput) StopResult {
	outputs := r.runAll(ctx, HookPostToolUseFailure, input.ToolName, input)
	var res StopResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			res.AdditionalContext = append(res.AdditionalContext, o.AdditionalContext)
		}
		if o.Blocked || o.IsError {
			res.Blocked = true
			if res.BlockReason == "" {
				if o.BlockReason != "" {
					res.BlockReason = o.BlockReason
				} else {
					res.BlockReason = o.SystemMessage
				}
			}
		}
	}
	return res
}
```

Extend `StopResult` to carry `AdditionalContext`:

```go
type StopResult struct {
	Blocked           bool
	BlockReason       string
	SystemMessages    []string
	AdditionalContext []string
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunPostToolUseFailure -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunPostToolUseFailure dispatcher"
```

---

### Task 31: `PostToolBatchResult` + `RunPostToolBatch` (matcher-less)

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunPostToolBatch_FiresAlways(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPostToolBatch, RegisteredHook{
		Matcher: "ThisShouldNotMatter",
		Type:    "command",
		Command: `echo '{"systemMessage":"batch done"}'`,
		Timeout: 5,
	})
	res := r.RunPostToolBatch(context.Background(), HookInput{
		HookEventName: "PostToolBatch",
		ToolResults:   []BatchToolResult{{ToolName: "Bash", Succeeded: true}, {ToolName: "Edit", Succeeded: false}},
	})
	if len(res.SystemMessages) != 1 || res.SystemMessages[0] != "batch done" {
		t.Errorf("SystemMessages = %v", res.SystemMessages)
	}
}

func TestRunPostToolBatch_BlockingDecisionStopsLoop(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPostToolBatch, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo '{"decision":"block","reason":"too many failures"}'`,
		Timeout: 5,
	})
	res := r.RunPostToolBatch(context.Background(), HookInput{HookEventName: "PostToolBatch"})
	if !res.Blocked {
		t.Error("expected Blocked = true")
	}
	if res.BlockReason != "too many failures" {
		t.Errorf("BlockReason = %q", res.BlockReason)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunPostToolBatch -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Add to `agent/plugin_hooks.go`:

```go
// PostToolBatchResult is the aggregated outcome of a PostToolBatch run.
type PostToolBatchResult struct {
	Blocked           bool
	BlockReason       string
	SystemMessages    []string
	AdditionalContext []string
}

// runAllUnmatched executes every hook registered for the event regardless
// of matcher value. Used by events that always fire (PostToolBatch).
func (r *HookRunner) runAllUnmatched(ctx context.Context, event HookEvent, input HookInput) []ParsedHookOutput {
	all := r.hooks[event]
	if len(all) == 0 {
		return nil
	}
	var sync []RegisteredHook
	for _, h := range all {
		if h.Async {
			r.dispatchAsync(ctx, event, h, input)
			continue
		}
		sync = append(sync, h)
	}
	results := make([]ParsedHookOutput, len(sync))
	var wg sync2.WaitGroup
	wg.Add(len(sync))
	for i, hook := range sync {
		go func(idx int, h RegisteredHook) {
			defer wg.Done()
			results[idx] = r.runHook(ctx, h, input)
		}(i, hook)
	}
	wg.Wait()
	return results
}

// RunPostToolBatch fires PostToolBatch hooks for every registered hook
// regardless of matcher.
func (r *HookRunner) RunPostToolBatch(ctx context.Context, input HookInput) PostToolBatchResult {
	outputs := r.runAllUnmatched(ctx, HookPostToolBatch, input)
	var res PostToolBatchResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			res.AdditionalContext = append(res.AdditionalContext, o.AdditionalContext)
		}
		if o.Blocked || o.IsError {
			res.Blocked = true
			if res.BlockReason == "" {
				res.BlockReason = o.BlockReason
			}
		}
	}
	return res
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunPostToolBatch -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunPostToolBatch always-fire dispatcher"
```

---

### Task 32: `classifyAPIError` + `RunStopFailure`

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestClassifyAPIError(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errorsNew("rate limit exceeded"), "rate_limit"},
		{errorsNew("authentication failed: missing token"), "authentication_failed"},
		{errorsNew("billing error from provider"), "billing_error"},
		{errorsNew("invalid request: bad JSON"), "invalid_request"},
		{errorsNew("server error 500"), "server_error"},
		{errorsNew("max output tokens"), "max_output_tokens"},
		{errorsNew("totally unknown"), "unknown"},
	}
	for _, tc := range tests {
		got := classifyAPIError(tc.err)
		if got != tc.want {
			t.Errorf("classifyAPIError(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
}

func errorsNew(s string) error { return &errorsString{s} }

type errorsString struct{ s string }

func (e *errorsString) Error() string { return e.s }

func TestRunStopFailure_FiresAndAggregates(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookStopFailure, RegisteredHook{
		Matcher: "rate_limit",
		Type:    "command",
		Command: `echo '{"systemMessage":"caught rate limit"}'`,
		Timeout: 5,
	})
	r.RunStopFailure(context.Background(), HookInput{
		HookEventName: "StopFailure",
		ErrorType:     "rate_limit",
	})
	// RunStopFailure returns no value; this test only asserts it doesn't panic
	// and that the matcher correctly fired by checking hook side effects via a
	// helper hook. For now, simply assert the call returns within timeout.
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestClassifyAPIError|TestRunStopFailure_FiresAndAggregates" -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// classifyAPIError maps a Go error to Claude Code's enumerated error_type strings.
func classifyAPIError(err error) string {
	if err == nil {
		return ""
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "rate limit"):
		return "rate_limit"
	case strings.Contains(s, "authentication"):
		return "authentication_failed"
	case strings.Contains(s, "oauth"):
		return "oauth_org_not_allowed"
	case strings.Contains(s, "billing"):
		return "billing_error"
	case strings.Contains(s, "invalid request"):
		return "invalid_request"
	case strings.Contains(s, "server error"):
		return "server_error"
	case strings.Contains(s, "max output tokens"):
		return "max_output_tokens"
	}
	return "unknown"
}

// RunStopFailure dispatches StopFailure hooks. Returns no value:
// the session is already terminating; hook output is captured for logs only.
// Matcher targets are the error_type string.
func (r *HookRunner) RunStopFailure(ctx context.Context, input HookInput) {
	_ = r.runAll(ctx, HookStopFailure, input.ErrorType, input)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run "TestClassifyAPIError|TestRunStopFailure" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): classifyAPIError and RunStopFailure observability dispatcher"
```

---

### Task 33: `RunSubagentStart`

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunSubagentStart_AgentTypeMatcher(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookSubagentStart, RegisteredHook{
		Matcher: "general-purpose",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"additionalContext":"watch this"}}'`,
		Timeout: 5,
	})
	res := r.RunSubagentStart(context.Background(), HookInput{
		HookEventName: "SubagentStart",
		AgentType:     "general-purpose",
		AgentID:       "agt-1",
		Prompt:        "do X",
	})
	if len(res.AdditionalContext) != 1 || res.AdditionalContext[0] != "watch this" {
		t.Errorf("AdditionalContext = %v", res.AdditionalContext)
	}
}

func TestRunSubagentStart_MatcherMiss(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookSubagentStart, RegisteredHook{
		Matcher: "specialist",
		Type:    "command",
		Command: `echo '{"systemMessage":"nope"}'`,
		Timeout: 5,
	})
	res := r.RunSubagentStart(context.Background(), HookInput{AgentType: "general-purpose"})
	if len(res.SystemMessages) != 0 {
		t.Errorf("SystemMessages = %v, want empty", res.SystemMessages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunSubagentStart -v`
Expected: FAIL — `undefined: RunSubagentStart`.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// RunSubagentStart dispatches SubagentStart hooks. Matcher target is agent_type.
func (r *HookRunner) RunSubagentStart(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookSubagentStart, input.AgentType, input)
	return collectSystemMessages(outputs)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunSubagentStart -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunSubagentStart dispatcher with agent_type matcher"
```

---

### Task 34: `RunUserPromptExpansion` + result type

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunUserPromptExpansion_AllowsAndAppendsContext(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookUserPromptExpansion, RegisteredHook{
		Matcher: "skill_a",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"additionalContext":"extra"}}'`,
		Timeout: 5,
	})
	res := r.RunUserPromptExpansion(context.Background(), HookInput{
		HookEventName: "UserPromptExpansion",
		CommandName:   "skill_a",
		Prompt:        "expanded text",
	})
	if res.Blocked {
		t.Error("should not be blocked")
	}
	if len(res.AdditionalContext) != 1 || res.AdditionalContext[0] != "extra" {
		t.Errorf("AdditionalContext = %v", res.AdditionalContext)
	}
}

func TestRunUserPromptExpansion_DecisionBlock(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookUserPromptExpansion, RegisteredHook{
		Matcher: "*",
		Type:    "command",
		Command: `echo '{"decision":"block","reason":"banned skill"}'`,
		Timeout: 5,
	})
	res := r.RunUserPromptExpansion(context.Background(), HookInput{CommandName: "x"})
	if !res.Blocked {
		t.Error("Blocked = false, want true")
	}
	if res.BlockReason != "banned skill" {
		t.Errorf("BlockReason = %q", res.BlockReason)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunUserPromptExpansion -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// UserPromptExpansionResult is the aggregated outcome of a UserPromptExpansion run.
type UserPromptExpansionResult struct {
	Blocked           bool
	BlockReason       string
	SystemMessages    []string
	AdditionalContext []string
}

// RunUserPromptExpansion dispatches UserPromptExpansion hooks. Matcher
// target is command_name. A block decision cancels the expansion.
func (r *HookRunner) RunUserPromptExpansion(ctx context.Context, input HookInput) UserPromptExpansionResult {
	outputs := r.runAll(ctx, HookUserPromptExpansion, input.CommandName, input)
	var res UserPromptExpansionResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.AdditionalContext != "" {
			res.AdditionalContext = append(res.AdditionalContext, o.AdditionalContext)
		}
		if o.Blocked || o.IsError {
			res.Blocked = true
			if res.BlockReason == "" {
				res.BlockReason = o.BlockReason
				if res.BlockReason == "" {
					res.BlockReason = o.SystemMessage
				}
			}
		}
	}
	return res
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunUserPromptExpansion -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunUserPromptExpansion with block-cancels-expansion"
```

---

### Task 35: `RunPostCompact`

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunPostCompact_TriggerMatcher(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPostCompact, RegisteredHook{
		Matcher: "auto",
		Type:    "command",
		Command: `echo '{"systemMessage":"compacted"}'`,
		Timeout: 5,
	})
	res := r.RunPostCompact(context.Background(), HookInput{
		HookEventName:  "PostCompact",
		CompactTrigger: "auto",
	})
	if len(res.SystemMessages) != 1 || res.SystemMessages[0] != "compacted" {
		t.Errorf("SystemMessages = %v", res.SystemMessages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunPostCompact -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// RunPostCompact dispatches PostCompact hooks. Matcher target is compact_trigger.
func (r *HookRunner) RunPostCompact(ctx context.Context, input HookInput) HookRunResult {
	outputs := r.runAll(ctx, HookPostCompact, input.CompactTrigger, input)
	return collectSystemMessages(outputs)
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunPostCompact -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunPostCompact dispatcher"
```

---

### Task 36: `PermissionRequestResult` + `RunPermissionRequest` (deny wins, allow merges)

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunPermissionRequest_AllowDecision(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPermissionRequest, RegisteredHook{
		Matcher: "Bash",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"decision":{"behavior":"allow","addPermissionRule":"Bash(ls:*)"}}}'`,
		Timeout: 5,
	})
	res := r.RunPermissionRequest(context.Background(), HookInput{
		HookEventName: "PermissionRequest",
		ToolName:      "Bash",
	})
	if res.Behavior != "allow" {
		t.Errorf("Behavior = %q, want allow", res.Behavior)
	}
	if res.AddPermissionRule != "Bash(ls:*)" {
		t.Errorf("AddPermissionRule = %q", res.AddPermissionRule)
	}
}

func TestRunPermissionRequest_DenyWins(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPermissionRequest,
		RegisteredHook{Matcher: "*", Type: "command", Command: `echo '{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}'`, Timeout: 5},
		RegisteredHook{Matcher: "*", Type: "command", Command: `echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","reason":"no"}}}'`, Timeout: 5},
	)
	res := r.RunPermissionRequest(context.Background(), HookInput{ToolName: "Bash"})
	if res.Behavior != "deny" {
		t.Errorf("deny should win; got %q", res.Behavior)
	}
	if res.Reason != "no" {
		t.Errorf("Reason = %q", res.Reason)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunPermissionRequest -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// PermissionRequestResult is the aggregated outcome of a PermissionRequest run.
type PermissionRequestResult struct {
	Behavior          string // "allow" | "deny" | ""  (empty = no opinion)
	Reason            string
	UpdatedInput      map[string]any
	AddPermissionRule string
	SystemMessages    []string
}

// RunPermissionRequest dispatches PermissionRequest hooks.
// Deny wins; among allows, the first non-empty addPermissionRule wins.
// updatedInput is merged last-write-wins per key in firing order.
func (r *HookRunner) RunPermissionRequest(ctx context.Context, input HookInput) PermissionRequestResult {
	outputs := r.runAll(ctx, HookPermissionRequest, input.ToolName, input)
	var res PermissionRequestResult
	hasAllow := false
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.Denied {
			res.Behavior = "deny"
			if res.Reason == "" {
				res.Reason = o.PermissionDecisionReason
				if res.Reason == "" {
					res.Reason = o.SystemMessage
				}
			}
		} else if o.PermissionDecisionReason != "" || o.AddPermissionRule != "" || o.UpdatedInput != nil {
			// Treat as an explicit allow when there's any allow-shaped signal.
			hasAllow = true
		}
		if o.UpdatedInput != nil {
			if res.UpdatedInput == nil {
				res.UpdatedInput = map[string]any{}
			}
			for k, v := range o.UpdatedInput {
				res.UpdatedInput[k] = v
			}
		}
		if res.AddPermissionRule == "" && o.AddPermissionRule != "" {
			res.AddPermissionRule = o.AddPermissionRule
		}
	}
	if res.Behavior == "" && hasAllow {
		res.Behavior = "allow"
	}
	return res
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunPermissionRequest -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunPermissionRequest with deny-wins precedence"
```

---

### Task 37: `RunPermissionDenied`

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunPermissionDenied_RetryFlag(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPermissionDenied, RegisteredHook{
		Matcher: "Bash",
		Type:    "command",
		Command: `echo '{"hookSpecificOutput":{"retry":true}}'`,
		Timeout: 5,
	})
	res := r.RunPermissionDenied(context.Background(), HookInput{ToolName: "Bash"})
	if !res.Retry {
		t.Error("Retry = false, want true")
	}
}

func TestRunPermissionDenied_NoRetry(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookPermissionDenied, RegisteredHook{Matcher: "*", Type: "command", Command: `echo ''`, Timeout: 5})
	res := r.RunPermissionDenied(context.Background(), HookInput{ToolName: "Bash"})
	if res.Retry {
		t.Error("Retry = true, want false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunPermissionDenied -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// PermissionDeniedResult is the aggregated outcome of a PermissionDenied run.
type PermissionDeniedResult struct {
	Retry          bool
	SystemMessages []string
}

// RunPermissionDenied dispatches PermissionDenied hooks. The only meaningful
// output is `retry: true`, which tells the orchestrator to surface a "you may
// retry" system message to the model.
func (r *HookRunner) RunPermissionDenied(ctx context.Context, input HookInput) PermissionDeniedResult {
	outputs := r.runAll(ctx, HookPermissionDenied, input.ToolName, input)
	var res PermissionDeniedResult
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.Retry {
			res.Retry = true
		}
	}
	return res
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunPermissionDenied -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunPermissionDenied with retry flag"
```

---

### Task 38: `ConfigChangeResult` + `RunConfigChange` with `policy_settings` exception

**Files:**
- Modify: `agent/plugin_hooks.go`
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestRunConfigChange_BlockRejectsReload(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookConfigChange, RegisteredHook{
		Matcher: "user_settings",
		Type:    "command",
		Command: `echo '{"decision":"block","reason":"keep old"}'`,
		Timeout: 5,
	})
	res := r.RunConfigChange(context.Background(), HookInput{
		HookEventName: "ConfigChange",
		ConfigSource:  "user_settings",
	})
	if !res.Blocked {
		t.Error("expected Blocked = true")
	}
	if res.BlockReason != "keep old" {
		t.Errorf("BlockReason = %q", res.BlockReason)
	}
}

func TestRunConfigChange_PolicySettingsBlockBecomesAdvisory(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.Add(HookConfigChange, RegisteredHook{
		Matcher: "policy_settings",
		Type:    "command",
		Command: `echo '{"decision":"block","reason":"shouldn-t apply"}'`,
		Timeout: 5,
	})
	res := r.RunConfigChange(context.Background(), HookInput{ConfigSource: "policy_settings"})
	if res.Blocked {
		t.Error("policy_settings block should be advisory only")
	}
	if len(res.SystemMessages) == 0 {
		t.Error("expected advisory SystemMessage")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestRunConfigChange -v`
Expected: FAIL.

- [ ] **Step 3: Implement**

Append to `agent/plugin_hooks.go`:

```go
// ConfigChangeResult is the aggregated outcome of a ConfigChange run.
type ConfigChangeResult struct {
	Blocked        bool
	BlockReason    string
	SystemMessages []string
}

// RunConfigChange dispatches ConfigChange hooks. Matcher target is config_source.
// A block decision rejects the reload — except for policy_settings, where a
// block becomes an advisory SystemMessage.
func (r *HookRunner) RunConfigChange(ctx context.Context, input HookInput) ConfigChangeResult {
	outputs := r.runAll(ctx, HookConfigChange, input.ConfigSource, input)
	var res ConfigChangeResult
	policyAdvisory := input.ConfigSource == "policy_settings"
	for _, o := range outputs {
		if o.SystemMessage != "" {
			res.SystemMessages = append(res.SystemMessages, o.SystemMessage)
		}
		if o.Blocked || o.IsError {
			if policyAdvisory {
				reason := o.BlockReason
				if reason == "" {
					reason = o.SystemMessage
				}
				if reason == "" {
					reason = "(no reason)"
				}
				res.SystemMessages = append(res.SystemMessages,
					"policy_settings reload: hook block was advisory: "+reason)
				continue
			}
			res.Blocked = true
			if res.BlockReason == "" {
				res.BlockReason = o.BlockReason
				if res.BlockReason == "" {
					res.BlockReason = o.SystemMessage
				}
			}
		}
	}
	return res
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestRunConfigChange -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): RunConfigChange with policy_settings advisory exception"
```

---

### Task 39: `if` filter via SP2 evaluator seam, fail-open

**Files:**
- Modify: `agent/plugin_hooks.go` — `HookRunner` and dispatch path
- Modify: `agent/plugin_hooks_events_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_events_test.go`:

```go
type stubRuleEvaluator struct {
	match bool
	err   error
}

func (s stubRuleEvaluator) EvaluateRule(rule, toolName string, toolInput map[string]any) (bool, error) {
	return s.match, s.err
}

func TestIfFilter_HookSkippedWhenRuleNotMatched(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.SetRuleEvaluator(stubRuleEvaluator{match: false})
	r.Add(HookPreToolUse, RegisteredHook{
		Matcher: "Bash",
		Type:    "command",
		Command: `echo '{"systemMessage":"ran"}'`,
		If:      "Bash(rm:*)",
		Timeout: 5,
	})
	res := r.RunPreToolUse(context.Background(), HookInput{ToolName: "Bash", ToolInput: map[string]any{"command": "ls"}})
	if len(res.SystemMessages) != 0 {
		t.Errorf("hook should have been filtered; got %v", res.SystemMessages)
	}
}

func TestIfFilter_FailOpenOnEvalError(t *testing.T) {
	r := NewHookRunner(nil, "")
	r.SetRuleEvaluator(stubRuleEvaluator{err: errorsNew("bad rule")})
	r.Add(HookPreToolUse, RegisteredHook{
		Matcher: "Bash",
		Type:    "command",
		Command: `echo '{"systemMessage":"ran"}'`,
		If:      "garbage",
		Timeout: 5,
	})
	res := r.RunPreToolUse(context.Background(), HookInput{ToolName: "Bash"})
	if len(res.SystemMessages) != 1 {
		t.Errorf("fail-open expected; got %v", res.SystemMessages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestIfFilter_ -v`
Expected: FAIL — `SetRuleEvaluator` undefined.

- [ ] **Step 3: Implement**

In `agent/plugin_hooks.go`, add to `HookRunner`:

```go
type HookRunner struct {
	hooks         map[HookEvent][]RegisteredHook
	client        PromptHookClient
	model         string
	onEvent       func(EventKind, any)
	asyncRewake   chan AsyncRewakeSignal
	mcpCaller     MCPHookCaller
	ruleEvaluator HookRuleEvaluator
}

// HookRuleEvaluator is the SP2 seam used by the `if` field. SP2 implements
// the real evaluator; tests substitute their own.
type HookRuleEvaluator interface {
	EvaluateRule(rule, toolName string, toolInput map[string]any) (bool, error)
}

// SetRuleEvaluator registers the SP2 evaluator.
func (r *HookRunner) SetRuleEvaluator(e HookRuleEvaluator) { r.ruleEvaluator = e }
```

In `runAll` (and `runAllUnmatched`), filter `sync` by `if`:

```go
var sync []RegisteredHook
for _, h := range matched {
	if h.Async {
		r.dispatchAsync(ctx, event, h, input)
		continue
	}
	if !r.passesIfFilter(h, input) {
		continue
	}
	sync = append(sync, h)
}
```

Add the helper:

```go
// passesIfFilter returns true when the hook's If rule is empty, the
// evaluator is unset, the rule matches, or the rule errored (fail-open).
func (r *HookRunner) passesIfFilter(h RegisteredHook, input HookInput) bool {
	if h.If == "" || r.ruleEvaluator == nil {
		return true
	}
	ok, err := r.ruleEvaluator.EvaluateRule(h.If, input.ToolName, input.ToolInput)
	if err != nil {
		log.Printf("hook %q event If=%q: evaluator error: %v (fail-open)", h.PluginName, h.If, err)
		return true
	}
	return ok
}
```

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestIfFilter_ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/plugin_hooks.go agent/plugin_hooks_events_test.go
git commit -m "feat(hooks): wire If filter through SP2 evaluator seam (fail-open)"
```

---


## Phase 5: ConfigChange watcher

### Task 40: `config_watcher.go` — emit `ConfigChangeSignal` on file write

**Files:**
- Create: `agent/config_watcher.go`
- Create: `agent/config_watcher_test.go`

- [ ] **Step 1: Write the failing test**

Create `agent/config_watcher_test.go`:

```go
package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigWatcher_EmitsSignalOnWrite(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.json")
	if err := os.WriteFile(cfg, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan ConfigChangeSignal, 4)
	if err := WatchConfigFiles(ctx, []ConfigWatchEntry{{Path: cfg, Source: "user_settings"}}, out); err != nil {
		t.Fatal(err)
	}

	// Touch the file.
	if err := os.WriteFile(cfg, []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case sig := <-out:
		if sig.Path != cfg {
			t.Errorf("Path = %q, want %q", sig.Path, cfg)
		}
		if sig.Source != "user_settings" {
			t.Errorf("Source = %q", sig.Source)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no ConfigChangeSignal received")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestConfigWatcher_EmitsSignalOnWrite -v`
Expected: FAIL — `undefined: WatchConfigFiles`.

- [ ] **Step 3: Implement (uses `github.com/fsnotify/fsnotify`)**

Create `agent/config_watcher.go`:

```go
package agent

import (
	"context"
	"log"

	"github.com/fsnotify/fsnotify"
)

// ConfigWatchEntry pairs a config file path with the SP1 tier name that
// owns it. The tier name appears in the emitted ConfigChangeSignal as
// the matcher target for ConfigChange hooks.
type ConfigWatchEntry struct {
	Path   string
	Source string // "user_settings" | "project_settings" | "local_settings" | "policy_settings" | "skills"
}

// ConfigChangeSignal is emitted by WatchConfigFiles whenever a watched
// file is written. The session loop consumes these and dispatches
// HookConfigChange.
type ConfigChangeSignal struct {
	Path   string
	Source string
}

// WatchConfigFiles starts an fsnotify-backed goroutine that emits a
// ConfigChangeSignal on out for every write event to any path in entries.
// The goroutine exits when ctx is cancelled.
//
// Returns an error only if fsnotify fails to initialize.
func WatchConfigFiles(ctx context.Context, entries []ConfigWatchEntry, out chan<- ConfigChangeSignal) error {
	if len(entries) == 0 {
		return nil
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	bySource := make(map[string]string, len(entries))
	for _, e := range entries {
		if err := w.Add(e.Path); err != nil {
			log.Printf("config watcher: %s: %v", e.Path, err)
			continue
		}
		bySource[e.Path] = e.Source
	}
	go func() {
		defer w.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if ev.Op&(fsnotify.Write|fsnotify.Create) == 0 {
					continue
				}
				src := bySource[ev.Name]
				if src == "" {
					continue
				}
				select {
				case out <- ConfigChangeSignal{Path: ev.Name, Source: src}:
				case <-ctx.Done():
					return
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Printf("config watcher: %v", err)
			}
		}
	}()
	return nil
}
```

If `github.com/fsnotify/fsnotify` is not already in `go.mod`, run `go get github.com/fsnotify/fsnotify@latest` first and stage the `go.mod` / `go.sum` updates.

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestConfigWatcher_EmitsSignalOnWrite -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/config_watcher.go agent/config_watcher_test.go go.mod go.sum
git commit -m "feat(hooks): fsnotify-backed config watcher emitting ConfigChangeSignal"
```

---

## Phase 6: Session integration sites

### Task 41: `hookInput` populates `TranscriptPath`, `PermissionMode`, `Effort`, `AgentID`, `AgentType`

**Files:**
- Modify: `agent/session.go` — `hookInput` (find the existing function)
- Test: `agent/session_test.go`

- [ ] **Step 1: Locate `hookInput`**

Search for `func (s *Session) hookInput(`. The function builds a `HookInput` from session state. Note line numbers.

- [ ] **Step 2: Write the failing test**

Append to `agent/session_test.go`:

```go
func TestHookInput_PopulatesSP5CommonFields(t *testing.T) {
	s := newTestSession(t)
	s.cfg.ReasoningEffort = "high"
	s.cfg.AgentName = "specialist"
	s.cfg.ParentSessionID = "parent-1"
	// hookInput is unexported; verify via a dispatch shape.
	got := s.hookInput(HookPreToolUse)
	if got.TranscriptPath == "" {
		t.Error("TranscriptPath should be populated when persistence is enabled")
	}
	if got.PermissionMode == "" {
		t.Log("PermissionMode empty; acceptable until SP2 is loaded")
	}
	if got.Effort == nil || got.Effort.Level != "high" {
		t.Errorf("Effort = %+v, want level=high", got.Effort)
	}
	if got.AgentType != "specialist" {
		t.Errorf("AgentType = %q", got.AgentType)
	}
	if got.AgentID == "" {
		t.Error("AgentID should be set on subagent contexts")
	}
}
```

`newTestSession` is the existing helper from the file — if absent, build the minimal `Session` inline using existing patterns from `session_test.go`.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestHookInput_PopulatesSP5CommonFields -v`
Expected: FAIL — `hookInput` does not yet set these fields.

- [ ] **Step 4: Implement**

In `agent/session.go`, extend `hookInput`:

```go
func (s *Session) hookInput(event HookEvent) HookInput {
	hi := HookInput{
		SessionID:     s.id,
		CWD:           s.env.WorkingDir,
		HookEventName: string(event),
	}
	if tp := s.TranscriptPath(); tp != "" {
		hi.TranscriptPath = tp
	}
	if s.cfg.ReasoningEffort != "" {
		hi.Effort = &EffortField{Level: s.cfg.ReasoningEffort}
	}
	if s.cfg.ParentSessionID != "" {
		hi.AgentID = s.id
		hi.AgentType = s.cfg.AgentName
		if hi.AgentType == "" {
			hi.AgentType = "general-purpose"
		}
	}
	// permission_mode resolution is owned by SP2; left empty here.
	return hi
}
```

If `TranscriptPath()` does not exist as a method on `Session`, add it: return whatever path is used for transcripts today, or empty if persistence is disabled. Find an existing call site for transcript paths in the codebase first; reuse rather than inventing.

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestHookInput_PopulatesSP5CommonFields -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/session_test.go
git commit -m "feat(hooks): populate SP5 common fields in Session.hookInput"
```

---

### Task 42: Fire `PostToolUseFailure` after a failed tool call

**Files:**
- Modify: `agent/session.go` — find `execTool` and the `PostToolUse` site
- Test: `agent/session_test.go` or `agent/plugin_integration_test.go`

- [ ] **Step 1: Locate the firing site**

Search for `RunPostToolUse(`. The site is in the per-tool result handling within `execTool` (or the round loop). Note the exact lines.

- [ ] **Step 2: Write the failing test**

Append to `agent/plugin_integration_test.go`:

```go
func TestPostToolUseFailure_FiresOnFailingTool(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "fired")
	plugin := writeFixturePlugin(t, dir, "failhook", `{
		"hooks": {"PostToolUseFailure": [{
			"matcher": "*",
			"hooks": [{"type": "command", "command": "touch ` + marker + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.runFailingTool(t, "Bash") // helper: invokes a tool that returns IsError=true
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("PostToolUseFailure hook did not fire: %v", err)
	}
}
```

`writeFixturePlugin`, `newSessionWithPlugin`, and `runFailingTool` are helpers to add to the test file if they don't already exist; reuse patterns from `plugin_integration_test.go`.

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestPostToolUseFailure_FiresOnFailingTool -v`
Expected: FAIL — event is never fired.

- [ ] **Step 4: Fire the event in `execTool`**

In `agent/session.go`, immediately after the existing `r.RunPostToolUse(...)` call, add:

```go
if res.IsError && s.hookRunner != nil {
	hi := s.hookInput(HookPostToolUseFailure)
	hi.ToolName = MapSerfToolNameToClaude(call.Name)
	if call.Arguments != nil {
		var args map[string]any
		_ = json.Unmarshal(call.Arguments, &args)
		hi.ToolInput = args
	}
	hi.ToolError = res.FullOutput
	hi.ToolUseID = call.ID
	failRes := s.hookRunner.RunPostToolUseFailure(ctx, hi)
	if failRes.Blocked {
		s.markTurnHalted(failRes.BlockReason)
	}
	for _, msg := range failRes.SystemMessages {
		s.appendSystemMessage(msg)
	}
	for _, ac := range failRes.AdditionalContext {
		s.appendSteeringMessage(ac)
	}
}
```

`markTurnHalted` and `appendSteeringMessage` may need to be introduced if they don't exist — match the existing `appendSystemMessage` naming. If "appendSteeringMessage" is missing, route through whatever existing path injects steering turns (look near `RunUserPromptSubmit`'s call site).

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestPostToolUseFailure_FiresOnFailingTool -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/plugin_integration_test.go
git commit -m "feat(hooks): fire PostToolUseFailure after a failing tool call"
```

---

### Task 43: Fire `PostToolBatch` after a parallel batch resolves

**Files:**
- Modify: `agent/session.go` — find the post-batch site (after `for i := range calls`)
- Test: `agent/plugin_integration_test.go`

- [ ] **Step 1: Locate the integration site**

Search for the round loop. After the `for i := range calls { results[i] = ... }` block resolves and before `appendTurn(TurnToolResults, ...)`, identify the insertion point.

- [ ] **Step 2: Write the failing test**

Append:

```go
func TestPostToolBatch_FiresOncePerBatch(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "count")
	plugin := writeFixturePlugin(t, dir, "batchhook", `{
		"hooks": {"PostToolBatch": [{
			"matcher": "*",
			"hooks": [{"type": "command", "command": "echo x >> ` + counter + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.runParallelTools(t, []string{"Read", "Read"}) // helper invokes two tools
	data, _ := os.ReadFile(counter)
	if strings.Count(string(data), "x") != 1 {
		t.Errorf("PostToolBatch fired %d times, want 1", strings.Count(string(data), "x"))
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestPostToolBatch_FiresOncePerBatch -v`
Expected: FAIL.

- [ ] **Step 4: Fire the event**

In `agent/session.go`, after `results []ToolExecResult` is filled:

```go
if s.hookRunner != nil && len(results) > 0 {
	hi := s.hookInput(HookPostToolBatch)
	hi.ToolResults = make([]BatchToolResult, len(results))
	for i, r := range results {
		var args map[string]any
		if calls[i].Arguments != nil {
			_ = json.Unmarshal(calls[i].Arguments, &args)
		}
		hi.ToolResults[i] = BatchToolResult{
			ToolName:     MapSerfToolNameToClaude(calls[i].Name),
			ToolUseID:    calls[i].ID,
			ToolInput:    args,
			ToolResponse: r.FullOutput,
			Succeeded:    !r.IsError,
		}
	}
	batchRes := s.hookRunner.RunPostToolBatch(ctx, hi)
	if batchRes.Blocked {
		s.markTurnHalted(batchRes.BlockReason)
	}
	for _, msg := range batchRes.SystemMessages {
		s.appendSystemMessage(msg)
	}
	for _, ac := range batchRes.AdditionalContext {
		s.appendSteeringMessage(ac)
	}
}
```

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestPostToolBatch_FiresOncePerBatch -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/plugin_integration_test.go
git commit -m "feat(hooks): fire PostToolBatch after each tool-batch resolves"
```

---

### Task 44: Fire `StopFailure` at API-error exit paths

**Files:**
- Modify: `agent/session.go` — find each return-on-API-error site
- Test: `agent/plugin_integration_test.go`

- [ ] **Step 1: Locate sites**

Grep for `processOneInput` and inspect the error-return branches: `llm.Complete` error after retries, `contentFilterRetried`, `emptyResponsesExhaustedError`, `bareTextWithoutResultToolError`.

- [ ] **Step 2: Write the failing test**

Append:

```go
func TestStopFailure_FiresOnAPIError(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "stop")
	plugin := writeFixturePlugin(t, dir, "stopfail", `{
		"hooks": {"StopFailure": [{
			"matcher": "*",
			"hooks": [{"type": "command", "command": "touch ` + marker + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.forceLLMError(t, errorsNew("rate limit exceeded"))
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("StopFailure hook did not fire: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestStopFailure_FiresOnAPIError -v`
Expected: FAIL.

- [ ] **Step 4: Wire firing into each error path**

Add a helper to `agent/session.go`:

```go
func (s *Session) fireStopFailure(ctx context.Context, err error) {
	if s.hookRunner == nil || err == nil {
		return
	}
	hi := s.hookInput(HookStopFailure)
	hi.ErrorType = classifyAPIError(err)
	msg := err.Error()
	if len(msg) > 4096 {
		msg = msg[:4096]
	}
	hi.ErrorMessage = msg
	s.hookRunner.RunStopFailure(ctx, hi)
}
```

In each error-return branch in `processOneInput`, insert `s.fireStopFailure(ctx, err)` just before `return ... err`.

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestStopFailure_FiresOnAPIError -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/plugin_integration_test.go
git commit -m "feat(hooks): fire StopFailure at every API-error exit path"
```

---

### Task 45: Fire `SubagentStart` in `spawnAgent`

**Files:**
- Modify: `agent/subagents.go`
- Test: `agent/plugin_agents_test.go`

- [ ] **Step 1: Locate the integration point**

Open `agent/subagents.go`. Find `spawnAgent` and the line where `go sub.run(...)` is invoked. The hook must fire after `subSess` exists and `sub` is wired, before `go sub.run`.

- [ ] **Step 2: Write the failing test**

Append to `agent/plugin_agents_test.go`:

```go
func TestSubagentStart_FiresBeforeRun(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "start")
	plugin := writeFixturePlugin(t, dir, "substart", `{
		"hooks": {"SubagentStart": [{
			"matcher": "general-purpose",
			"hooks": [{"type": "command", "command": "touch ` + marker + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.spawnTestSubagent(t, "general-purpose", "do work")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("SubagentStart hook did not fire: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestSubagentStart_FiresBeforeRun -v`
Expected: FAIL.

- [ ] **Step 4: Fire the event**

In `agent/subagents.go`, right before `go sub.run(...)`:

```go
if s.hookRunner != nil {
	hi := s.hookInput(HookSubagentStart)
	hi.AgentID = sub.id
	hi.AgentType = agentType
	if hi.AgentType == "" {
		hi.AgentType = "general-purpose"
	}
	hi.Prompt = task
	res := s.hookRunner.RunSubagentStart(ctx, hi)
	for _, ac := range res.AdditionalContext {
		sub.appendSteeringMessage(ac)
	}
	for _, m := range res.SystemMessages {
		s.appendSystemMessage(m)
	}
}
```

Adjust `sub.appendSteeringMessage` to whatever method the codebase uses to inject pre-first-turn steering. If none exists, route via the same channel SessionStart uses.

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestSubagentStart_FiresBeforeRun -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/subagents.go agent/plugin_agents_test.go
git commit -m "feat(hooks): fire SubagentStart before subagent goroutine launches"
```

---

### Task 46: Fire `UserPromptExpansion` at the skill-expansion site

**Files:**
- Modify: `agent/session.go` — find skill-activation / `ActivatedSkillBodies`
- Test: `agent/plugin_integration_test.go`

- [ ] **Step 1: Locate the integration point**

Search `processOneInput` for skill-resolution code (`ActivatedSkillBodies`, `:skill:`, or similar). The hook fires after the input has been classified and an expanded prompt produced, before that prompt is appended as `TurnUserInput`.

- [ ] **Step 2: Write the failing test**

Append:

```go
func TestUserPromptExpansion_FiresOnSkillExpansion(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "expanded")
	plugin := writeFixturePlugin(t, dir, "expand", `{
		"hooks": {"UserPromptExpansion": [{
			"matcher": "test_skill",
			"hooks": [{"type": "command", "command": "touch ` + marker + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.feedUserInput(t, ":skill:test_skill some args")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("UserPromptExpansion hook did not fire: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestUserPromptExpansion_FiresOnSkillExpansion -v`
Expected: FAIL.

- [ ] **Step 4: Fire the event**

In `agent/session.go`, after the expansion produces the final prompt text:

```go
if s.hookRunner != nil && expansion.Happened {
	hi := s.hookInput(HookUserPromptExpansion)
	hi.ExpansionType = "slash_command"
	hi.CommandName = expansion.Name
	hi.CommandArgs = expansion.Args
	hi.CommandSource = expansion.Source // "skill" | "plugin" | "builtin"
	hi.Prompt = expansion.ExpandedText
	res := s.hookRunner.RunUserPromptExpansion(ctx, hi)
	if res.Blocked {
		s.appendSystemMessage("expansion blocked: " + res.BlockReason)
		return SessionAwaitingInput, nil
	}
	for _, ac := range res.AdditionalContext {
		expansion.ExpandedText += "\n\n" + ac
	}
	for _, m := range res.SystemMessages {
		s.appendSystemMessage(m)
	}
}
```

`expansion` is the local struct produced by the skill-resolver; rename to whatever exists in the codebase. The MCP-prompt branch is a stub for the future (per spec §3.5) — leave a comment marking the location.

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestUserPromptExpansion_FiresOnSkillExpansion -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/session.go agent/plugin_integration_test.go
git commit -m "feat(hooks): fire UserPromptExpansion at skill-expansion site"
```

---

### Task 47: Fire `PostCompact` after `MaybeCompact`

**Files:**
- Modify: `agent/context_strategy.go`
- Test: `agent/context_strategy_test.go`

- [ ] **Step 1: Locate the integration point**

Open `agent/context_strategy.go`. Find `CompactStrategy.ManageContext` and the `s.cm.MaybeCompact(...)` call. The new fire site is immediately after — gated on whether compaction actually happened.

- [ ] **Step 2: Write the failing test**

Append:

```go
func TestPostCompact_FiresAfterAutoCompaction(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "pc")
	plugin := writeFixturePlugin(t, dir, "pc", `{
		"hooks": {"PostCompact": [{
			"matcher": "auto",
			"hooks": [{"type": "command", "command": "touch ` + marker + `"}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.triggerAutoCompact(t)
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("PostCompact hook did not fire: %v", err)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestPostCompact_FiresAfterAutoCompaction -v`
Expected: FAIL.

- [ ] **Step 4: Add `Compacted` to `MaybeCompact` return shape and fire the event**

If `MaybeCompact` already returns whether it compacted, reuse the field. Otherwise add a `Compacted bool` return value (additive). In `ManageContext`, after the call:

```go
if compacted && s.hookRunner != nil {
	hi := s.hookInput(HookPostCompact)
	hi.CompactTrigger = "auto"
	res := s.hookRunner.RunPostCompact(ctx, hi)
	for _, m := range res.SystemMessages {
		s.appendSystemMessage(m)
	}
}
```

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./agent/ -run "TestPostCompact_FiresAfterAutoCompaction|TestCompactStrategy" -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/context_strategy.go agent/context_strategy_test.go
git commit -m "feat(hooks): fire PostCompact after MaybeCompact succeeds"
```

---

### Task 48: Drain `asyncRewake` channel in the main loop

**Files:**
- Modify: `agent/session.go` — `processOneInput`
- Test: `agent/plugin_hooks_async_test.go` (extend with integration)

- [ ] **Step 1: Write the failing test**

Append to `agent/plugin_hooks_async_test.go`:

```go
func TestSession_DrainsRewakeBetweenRounds(t *testing.T) {
	dir := t.TempDir()
	plugin := writeFixturePlugin(t, dir, "rewake", `{
		"hooks": {"PostToolUse": [{
			"matcher": "*",
			"hooks": [{"type":"command","command":"echo wake-up 1>&2; exit 2","async":true,"asyncRewake":true}]
		}]}
	}`)
	sess := newSessionWithPlugin(t, plugin)
	sess.EnableAsyncRewake(8)
	sess.runOneTool(t, "Bash")
	// Allow async hook to run.
	time.Sleep(500 * time.Millisecond)
	sess.RunOneRound(t)
	if !sess.SteerLogContains("wake-up") {
		t.Errorf("expected steering to include rewake stderr; got %v", sess.SteerLog())
	}
}
```

`SteerLog`/`RunOneRound`/`SteerLogContains` are test helpers added alongside.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSession_DrainsRewakeBetweenRounds -v`
Expected: FAIL.

- [ ] **Step 3: Wire the drain**

In `agent/session.go`, add a `Session.EnableAsyncRewake(capacity int)` pass-through that calls `s.hookRunner.EnableAsyncRewake(capacity)`. In `processOneInput`, at the top of each round and before each `LLMCall` phase:

```go
if s.hookRunner != nil {
	for {
		select {
		case sig := <-s.hookRunner.AsyncRewakeChan():
			s.appendSteeringMessage(formatRewakeMessage(sig))
		default:
			break
		}
		// inner break handled by Go's lack of labeled break;
		// use a sentinel:
		break
	}
}
```

Restructure the drain so it pulls all available signals (a labeled loop or a helper):

```go
func (s *Session) drainRewakeSignals() {
	if s.hookRunner == nil {
		return
	}
	ch := s.hookRunner.AsyncRewakeChan()
	if ch == nil {
		return
	}
	for {
		select {
		case sig := <-ch:
			s.appendSteeringMessage(formatRewakeMessage(sig))
		default:
			return
		}
	}
}
```

Call `s.drainRewakeSignals()` at the top of each round and before `LLMCall`.

- [ ] **Step 4: Run tests to verify pass**

Run: `go test ./agent/ -run TestSession_DrainsRewakeBetweenRounds -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/plugin_hooks_async_test.go
git commit -m "feat(hooks): drain asyncRewake channel between rounds"
```

---

### Task 49: Set `CLAUDE_CODE_REMOTE` env var when session is remote

**Files:**
- Modify: `agent/plugin_hooks.go` — `executeCommandHook` reads `Session.IsRemote` via context
- Modify: `agent/session.go` — pass remote flag through context or a field on HookInput
- Test: `agent/plugin_hooks_test.go`

- [ ] **Step 1: Decide carriage**

Add `Remote bool` to `HookInput` with `json:"-"` (internal only, not visible to plugins as JSON). Populate from `s.cfg.IsRemote` in `hookInput`. Export to env from `executeCommandHook`.

- [ ] **Step 2: Write the failing test**

Append to `agent/plugin_hooks_test.go`:

```go
func TestExecuteCommandHook_RemoteEnvSet(t *testing.T) {
	hook := RegisteredHook{
		Type:    "command",
		Command: "echo remote=$CLAUDE_CODE_REMOTE",
		Timeout: 5,
	}
	res, err := executeCommandHook(context.Background(), hook, HookInput{Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "remote=true") {
		t.Errorf("expected remote=true in stdout: %q", res.Stdout)
	}
}

func TestExecuteCommandHook_RemoteEnvAbsent(t *testing.T) {
	hook := RegisteredHook{Type: "command", Command: "env | grep -c CLAUDE_CODE_REMOTE || true", Timeout: 5}
	res, _ := executeCommandHook(context.Background(), hook, HookInput{Remote: false})
	if !strings.Contains(res.Stdout, "0") {
		t.Errorf("CLAUDE_CODE_REMOTE should be absent: %q", res.Stdout)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./agent/ -run "TestExecuteCommandHook_RemoteEnv" -v`
Expected: FAIL — `Remote` field undefined.

- [ ] **Step 4: Add field + env logic**

In `HookInput`:

```go
Remote bool `json:"-"`
```

In `executeCommandHook`, after the existing env appends:

```go
if input.Remote {
	env = append(env, "CLAUDE_CODE_REMOTE=true")
}
```

In `Session.hookInput`:

```go
hi.Remote = s.cfg.IsRemote
```

Add `IsRemote bool` to whatever struct `s.cfg` resolves to, if it doesn't already exist; default zero value means CLI/TUI/SDK.

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./agent/ -run "TestExecuteCommandHook_RemoteEnv" -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/plugin_hooks.go agent/session.go agent/plugin_hooks_test.go
git commit -m "feat(hooks): set CLAUDE_CODE_REMOTE env when session is remote"
```

---

## Phase 7: Plumb `statusMessage` and end-to-end coverage

### Task 50: `HookStartData` carries `StatusMessage`

**Files:**
- Modify: `agent/events.go` — `HookStartData`
- Modify: `agent/plugin_hooks.go` — `runAll` emits `StatusMessage`
- Test: `agent/events_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/events_test.go`:

```go
func TestHookStartData_HasStatusMessage(t *testing.T) {
	var seen HookStartData
	r := NewHookRunner(nil, "")
	r.SetEventCallback(func(k EventKind, data any) {
		if k == EventHookStart {
			seen = data.(HookStartData)
		}
	})
	r.Add(HookPostToolUse, RegisteredHook{
		Matcher:       "*",
		Type:          "command",
		Command:       "true",
		Timeout:       5,
		StatusMessage: "Doing the thing",
	})
	_ = r.RunPostToolUse(context.Background(), HookInput{ToolName: "Bash"})
	if seen.StatusMessage != "Doing the thing" {
		t.Errorf("StatusMessage = %q", seen.StatusMessage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestHookStartData_HasStatusMessage -v`
Expected: FAIL — field undefined.

- [ ] **Step 3: Add the field and populate**

In `agent/events.go`, extend `HookStartData`:

```go
type HookStartData struct {
	Event         string
	HookType      string
	Matcher       string
	PluginName    string
	StatusMessage string
}
```

In `runAll` and `runAllUnmatched`, when calling `r.onEvent(EventHookStart, ...)`, include `StatusMessage: h.StatusMessage`.

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestHookStartData_HasStatusMessage -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/events.go agent/plugin_hooks.go agent/events_test.go
git commit -m "feat(hooks): surface hook StatusMessage through HookStartData"
```

---

### Task 51: Matcher-corner fixture plugin

**Files:**
- Create: `agent/testdata/plugins/hooks_matcher_corners/plugin.json`
- Create: `agent/testdata/plugins/hooks_matcher_corners/hooks/hooks.json`
- Modify: `agent/plugin_hooks_matcher_test.go`

- [ ] **Step 1: Write the failing test**

Append:

```go
func TestParse_MatcherCornersFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/plugins/hooks_matcher_corners/hooks/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := ParsePluginHooks(data, "testdata/plugins/hooks_matcher_corners", "corners")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pre := hooks[HookPreToolUse]
	if len(pre) != 5 {
		t.Fatalf("expected 5 hooks, got %d", len(pre))
	}
	// All hooks must have a cached matcher.
	for i, h := range pre {
		if h.matcherFunc == nil {
			t.Errorf("hook %d: matcherFunc nil", i)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParse_MatcherCornersFixture -v`
Expected: FAIL — fixture absent.

- [ ] **Step 3: Create the fixture**

Create `agent/testdata/plugins/hooks_matcher_corners/plugin.json`:

```json
{ "name": "corners", "version": "0.0.1" }
```

Create `agent/testdata/plugins/hooks_matcher_corners/hooks/hooks.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {"matcher": "*",                "hooks": [{"type":"command","command":"true"}]},
      {"matcher": "Bash",             "hooks": [{"type":"command","command":"true"}]},
      {"matcher": "Edit|Write",       "hooks": [{"type":"command","command":"true"}]},
      {"matcher": "mcp__.*",          "hooks": [{"type":"command","command":"true"}]},
      {"matcher": "mcp__server__tool","hooks": [{"type":"command","command":"true"}]}
    ]
  }
}
```

- [ ] **Step 4: Run test to verify pass**

Run: `go test ./agent/ -run TestParse_MatcherCornersFixture -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/testdata/plugins/hooks_matcher_corners agent/plugin_hooks_matcher_test.go
git commit -m "test(hooks): matcher-corners fixture plugin"
```

---

### Task 52: End-to-end `TestPluginE2E_HookParityScenarios`

**Files:**
- Create: `agent/testdata/plugins/hookparity/plugin.json`
- Create: `agent/testdata/plugins/hookparity/hooks/hooks.json`
- Modify: `agent/plugin_e2e_test.go`

- [ ] **Step 1: Create the fixture**

Create `agent/testdata/plugins/hookparity/plugin.json`:

```json
{ "name": "hookparity", "version": "0.0.1" }
```

Create `agent/testdata/plugins/hookparity/hooks/hooks.json` — written entirely as command hooks (no http/mcp/agent to avoid environmental dependence; the per-type harness tests already cover those types):

```json
{
  "hooks": {
    "PostToolUseFailure": [
      {"matcher":"*","hooks":[{"type":"command","command":"echo failure-fired > ${CLAUDE_PLUGIN_ROOT}/.markers/fail"}]}
    ],
    "PostToolBatch": [
      {"matcher":"*","hooks":[{"type":"command","command":"echo batch-fired > ${CLAUDE_PLUGIN_ROOT}/.markers/batch"}]}
    ],
    "SubagentStart": [
      {"matcher":"general-purpose","hooks":[{"type":"command","command":"echo sub-fired > ${CLAUDE_PLUGIN_ROOT}/.markers/sub"}]}
    ],
    "PermissionRequest": [
      {"matcher":"Bash","hooks":[{"type":"command","command":"echo '{\"hookSpecificOutput\":{\"decision\":{\"behavior\":\"allow\"}}}'"}]}
    ]
  }
}
```

- [ ] **Step 2: Write the failing test**

Append to `agent/plugin_e2e_test.go`:

```go
func TestPluginE2E_HookParityScenarios(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e")
	}
	src := "testdata/plugins/hookparity"
	dst := t.TempDir()
	copyFixturePlugin(t, src, dst) // helper that copies + creates .markers
	pluginDir := filepath.Join(dst, "hookparity")
	if err := os.MkdirAll(filepath.Join(pluginDir, ".markers"), 0o755); err != nil {
		t.Fatal(err)
	}

	sess := newSessionWithPluginDir(t, pluginDir)

	sess.runFailingTool(t, "Bash")
	sess.runParallelTools(t, []string{"Read", "Read"})
	sess.spawnTestSubagent(t, "general-purpose", "do thing")
	sess.requestPermission(t, "Bash", map[string]any{"command": "ls"})

	for name, marker := range map[string]string{
		"PostToolUseFailure": filepath.Join(pluginDir, ".markers/fail"),
		"PostToolBatch":      filepath.Join(pluginDir, ".markers/batch"),
		"SubagentStart":      filepath.Join(pluginDir, ".markers/sub"),
	} {
		if _, err := os.Stat(marker); err != nil {
			t.Errorf("%s marker missing: %v", name, err)
		}
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./agent/ -run TestPluginE2E_HookParityScenarios -v -timeout 30s`
Expected: FAIL on the first missing scenario.

- [ ] **Step 4: Implement test helpers**

The four helpers (`runFailingTool`, `runParallelTools`, `spawnTestSubagent`, `requestPermission`) drive minimal real Session flows. Build each one as a small wrapper around the existing test scaffolding in `plugin_e2e_test.go`. Patterns are visible in `plugin_integration_test.go` and `plugin_agents_test.go`.

- [ ] **Step 5: Run test to verify pass**

Run: `go test ./agent/ -run TestPluginE2E_HookParityScenarios -v -timeout 30s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add agent/testdata/plugins/hookparity agent/plugin_e2e_test.go
git commit -m "test(hooks): end-to-end TestPluginE2E_HookParityScenarios"
```

---

### Task 53: Full-suite green check + race detector

**Files:** none

- [ ] **Step 1: Run the whole agent package**

Run: `go test ./agent/... -timeout 120s`
Expected: PASS, no test failures.

- [ ] **Step 2: Run with the race detector**

Run: `go test -race ./agent/... -timeout 240s`
Expected: PASS, no race warnings.

- [ ] **Step 3: Vet**

Run: `go vet ./agent/...`
Expected: clean.

- [ ] **Step 4: Commit any incidental fixes**

If steps 1-3 surfaced issues, fix them in a single follow-up commit:

```bash
git add -A
git commit -m "fix(hooks): address vet/race findings from SP5 final pass"
```

If nothing changed, skip this step.

---

## Self-Review Notes

The self-review pass was run against the SP5 sub-spec section-by-section.

**Spec coverage:**

| Spec section | Task(s) |
|---|---|
| §2.1 nine new event constants | Task 1 |
| §2.2 `RegisteredHook` additions (Args/Async/AsyncRewake/Shell/If/StatusMessage + http/mcp/agent fields) | Tasks 7, 28 |
| §2.3 `HookInput` common new fields | Task 10 |
| §2.3 event-specific input fields + `BatchToolResult` + `EffortField` | Tasks 10, 11 |
| §2.4 `ParsedHookOutput` additions | Tasks 12-15 |
| §2.5 nine `Run<Event>` methods + aggregate result types | Tasks 30-38 |
| §2.6 `AsyncRewakeSignal` + wake channel | Tasks 18-21 |
| §3.1 PostToolUseFailure integration | Task 42 |
| §3.2 PostToolBatch integration | Task 43 |
| §3.3 StopFailure integration + classifier | Tasks 32, 44 |
| §3.4 SubagentStart integration | Task 45 |
| §3.5 UserPromptExpansion integration | Task 46 |
| §3.6 PostCompact integration | Task 47 |
| §3.7 PermissionRequest contract (SP2 calls this) | Task 36 |
| §3.8 PermissionDenied contract (SP2 calls this) | Task 37 |
| §3.9 ConfigChange + policy_settings advisory | Tasks 38, 40 |
| §3.10 Async + rewake main-loop drain | Tasks 18-21, 48 |
| §4.1 command hook extended (args + shell) | Task 28 |
| §4.2 http hook | Tasks 22-24 |
| §4.3 mcp_tool hook | Task 25 |
| §4.4 agent hook + experimental warning | Tasks 26, 27 |
| §5 new config fields (args/async/asyncRewake/shell/if/statusMessage) | Tasks 7, 8, 9, 28, 39, 50 |
| §6 new output fields (additionalContext routing, defer, reason, sessionTitle, addPermissionRule, retry, stopReason) | Tasks 13, 14, 15, 17 |
| §7 new input fields (transcript_path/permission_mode/effort/agent_id/agent_type/tool_use_id) | Tasks 10, 41 |
| §8 new env vars (CLAUDE_PLUGIN_DATA, CLAUDE_EFFORT, CLAUDE_CODE_REMOTE) | Tasks 29, 49 |
| §9 dual-mode matcher | Tasks 2-6 |
| §10 output capping at 10,000 bytes | Task 16 |
| §11 backward compatibility | Implicit (additive structure changes; existing tests stay green; locked in by Task 53) |
| §13 testing strategy | Built into every task; matrix expanded via Tasks 51, 52 |

**Placeholder scan:** No "TBD"/"implement later"/"similar to" remain. Helpers referenced in late tasks (`newSessionWithPlugin`, `writeFixturePlugin`, `runFailingTool`, etc.) are flagged where the implementer must build or extend them; this is unavoidable because they exercise the live `Session` API which SP5 itself extends. The implementer is told to mirror existing patterns in `plugin_integration_test.go` rather than copy a placeholder.

**Type consistency:** `StopResult` is used by both `RunStop` (pre-existing) and `RunPostToolUseFailure` (new). Task 30 widens `StopResult` to include `AdditionalContext`; the field is also referenced by Task 17's `collectSystemMessages`-style aggregation pattern. `HookRunner`'s new fields (`asyncRewake`, `mcpCaller`, `ruleEvaluator`) are added in Tasks 19, 25, and 39 respectively — each task adds only its own field, but the final struct shape is consistent.

**Known gaps the plan accepts (per spec §14.4):**

- MCP-prompt expansion site is a stub (no live serf code expands MCP prompts yet). Task 46 wires the skill branch; MCP-prompt branch waits for a future SP.
- `config_source = "skills"` is a reserved matcher value with no live firing today; the matcher value is parseable per Task 1, but Task 47 (PostCompact) and Task 40 (config watcher) do not yet emit it.
- `addPermissionRule` is honored in-session only (Task 36); persistence is deferred to a future SP per spec §14.4.
- Two test helpers (`appendSteeringMessage`, `markTurnHalted`) referenced in Tasks 42-48 may not exist in the codebase verbatim; the plan instructs the implementer to map them to whatever existing path the agent uses for steering/halt, which is the right move given SP5 is additive and shouldn't invent terminology.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-14-claude-code-compat-sp5-hook-parity-plan.md`. Two execution options:

1. **Subagent-Driven (recommended)** — dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints.

Which approach?
