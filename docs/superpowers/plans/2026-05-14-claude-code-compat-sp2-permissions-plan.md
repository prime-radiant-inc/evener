# SP2 — Permissions Matcher and Enforcement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the `permissions` block produced by SP1 into a decision oracle that serf consults on every tool call — parse Claude Code's permission-rule grammar, evaluate `allow`/`deny`/`ask` against `(toolName, toolInput)`, honor `defaultMode`, and wire enforcement into `Session.execTool` immediately after `PreToolUse` hooks fire.

**Architecture:** All public symbols live in a new `agent/permissions.go`. `PermissionMatcher` is built once at session construction from a `PermissionsConfig` and an `ExecutionEnvironment`. Per-rule patterns compile at parse time; `Evaluate` is a pure function. `Session` grows one field and one helper to consult the matcher between `PreToolUse` and tool execution. Test-driven: each rule shape lands a failing table-driven test first, then minimal code.

**Tech Stack:** Go 1.25, `agent/` package, `t.TempDir()` filesystem fixtures, table-driven tests, `github.com/bmatcuk/doublestar/v4` (already vendored) for path globbing.

---

## File Structure

**Created:**

- `agent/permissions.go` — public API (`PermissionMatcher`, `Rule`, `PermissionDecision`, `PermissionMode`, `PermissionOutcome`, `AskFallback`, `NewPermissionMatcher`, `ParseRule`, `Evaluate`), plus the unexported parser and per-rule compiled matchers.
- `agent/permissions_test.go` — all the §10 tables.
- `agent/testdata/permissions/cc-docs-examples.json` — verbatim Claude Code permissions-doc snippets, parsed by a meta-test.

**Modified:**

- `agent/session.go` — add `Permissions PermissionsConfig` and `PermissionAskFallback AskFallback` fields on `SessionConfig`; add `permissionMatcher *PermissionMatcher` on `Session`; construct the matcher in `NewSession`; insert the §6.1 enforcement block in `execTool`; add `permissionDeniedResult` and `resolveAsk` helpers.

---

## Conventions for every task

- TDD always: write failing test → run red → minimal code → run green → commit.
- Filesystem in tests is `t.TempDir()` and real files only — never mock.
- Table-driven Go tests in the style of `agent/mcp_config_test.go`.
- Code blocks below contain the exact code; if a step modifies a file, copy the code as written.
- `permission_rule_string` is whatever the user typed — preserve it verbatim in `decision.Rule` and error messages.

---

## Task 1: Scaffold permissions.go with empty public symbols

**Files:**
- Create: `agent/permissions.go`
- Test: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test (compile-only)**

Create `agent/permissions_test.go` with:

```go
package agent

import "testing"

func TestPermissionsAPIShape(t *testing.T) {
	// Compile-only test that wires all public symbols. If anything is renamed
	// later, this test breaks first and surfaces it cheaply.
	var _ PermissionOutcome = PermissionAllow
	var _ PermissionOutcome = PermissionDeny
	var _ PermissionOutcome = PermissionAsk
	var _ PermissionMode = PermissionModeDefault
	var _ PermissionMode = PermissionModeAcceptEdits
	var _ PermissionMode = PermissionModePlan
	var _ PermissionMode = PermissionModeAuto
	var _ PermissionMode = PermissionModeDontAsk
	var _ PermissionMode = PermissionModeBypassPermissions
	var _ AskFallback = AskFallbackInteractive
	var _ AskFallback = AskFallbackDeny
	var _ AskFallback = AskFallbackAllow
	var _ PermissionDecision = PermissionDecision{}
	var _ *PermissionMatcher = (*PermissionMatcher)(nil)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestPermissionsAPIShape -count=1`
Expected: FAIL — `undefined: PermissionMatcher` etc.

- [ ] **Step 3: Write minimal implementation**

Create `agent/permissions.go`:

```go
package agent

import (
	"fmt"
)

// PermissionOutcome is the result of evaluating one tool call.
type PermissionOutcome string

const (
	PermissionAllow PermissionOutcome = "allow"
	PermissionDeny  PermissionOutcome = "deny"
	PermissionAsk   PermissionOutcome = "ask"
)

// PermissionMode is the parsed form of permissions.defaultMode.
type PermissionMode string

const (
	PermissionModeDefault           PermissionMode = "default"
	PermissionModeAcceptEdits       PermissionMode = "acceptEdits"
	PermissionModePlan              PermissionMode = "plan"
	PermissionModeAuto              PermissionMode = "auto"
	PermissionModeDontAsk           PermissionMode = "dontAsk"
	PermissionModeBypassPermissions PermissionMode = "bypassPermissions"
)

// AskFallback dictates what Evaluate returns when a rule yields "ask" on a
// surface that has no human (serf -p, serfeval, hub batch).
type AskFallback int

const (
	AskFallbackInteractive AskFallback = iota
	AskFallbackDeny
	AskFallbackAllow
)

// PermissionDecision is the result of one Evaluate call.
type PermissionDecision struct {
	Outcome PermissionOutcome
	Rule    string
	Reason  string
}

// Rule is the parsed form of one permission string.
type Rule interface {
	String() string
	Matches(toolName string, toolInput map[string]any, env ExecutionEnvironment) bool
}

// parsedRule pairs a compiled Rule with the verbatim user-typed source.
type parsedRule struct {
	rule   Rule
	source string
}

// PermissionMatcher decides whether a tool call is allowed, denied, or asks.
type PermissionMatcher struct {
	deny  []parsedRule
	ask   []parsedRule
	allow []parsedRule
	mode  PermissionMode
	env   ExecutionEnvironment
}

// NewPermissionMatcher parses a PermissionsConfig and returns a ready matcher.
// Stub for now — task 27 fills this in.
func NewPermissionMatcher(cfg PermissionsConfig, env ExecutionEnvironment) (*PermissionMatcher, error) {
	return nil, fmt.Errorf("NewPermissionMatcher: not implemented")
}

// Evaluate decides one tool call. Stub for now — task 24 fills this in.
func (m *PermissionMatcher) Evaluate(toolName string, toolInput map[string]any) PermissionDecision {
	return PermissionDecision{}
}

// ParseRule parses a single permission rule. Stub for now — task 2 fills this in.
func ParseRule(s string) (Rule, error) {
	return nil, fmt.Errorf("ParseRule: not implemented")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestPermissionsAPIShape -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): scaffold permissions.go API surface"
```

---

## Task 2: ParseRule — bare tool keywords

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_Bare(t *testing.T) {
	cases := []string{"Bash", "Read", "Edit", "Write", "WebFetch", "WebSearch", "Skill", "Agent"}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			r, err := ParseRule(in)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", in, err)
			}
			if r.String() != in {
				t.Errorf("Rule.String() = %q, want %q", r.String(), in)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseRule_Bare -count=1`
Expected: FAIL — `ParseRule: not implemented`.

- [ ] **Step 3: Write minimal implementation**

Replace the `ParseRule` stub in `agent/permissions.go` with:

```go
// knownTools is the set of tool keywords that take a typed specifier shape.
// MCP rules ("mcp__server[__tool]") and unknown keywords are handled separately.
var knownTools = map[string]bool{
	"Bash":         true,
	"Read":         true,
	"Edit":         true,
	"Write":        true,
	"WebFetch":     true,
	"WebSearch":    true,
	"Skill":        true,
	"Agent":        true,
	"Task":         true,
	"NotebookEdit": true,
	"Grep":         true,
	"Glob":         true,
	"PowerShell":   true,
}

// bareRule matches any invocation of one named tool.
type bareRule struct {
	tool   string
	source string
}

func (r bareRule) String() string { return r.source }
func (r bareRule) Matches(toolName string, _ map[string]any, _ ExecutionEnvironment) bool {
	return toolName == r.tool
}

func ParseRule(s string) (Rule, error) {
	if s == "" {
		return nil, fmt.Errorf("permission rule %q: empty", s)
	}
	if knownTools[s] {
		return bareRule{tool: s, source: s}, nil
	}
	return nil, fmt.Errorf("permission rule %q: not implemented yet", s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseRule_Bare -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): ParseRule for bare tool keywords"
```

---

## Task 3: ParseRule — split tool name and specifier

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_ParenStructure(t *testing.T) {
	cases := []struct {
		in         string
		wantErrSub string
	}{
		{"Bash(", "unbalanced parentheses"},
		{"Bash(rm))", "unbalanced parentheses"},
		{"Bash()", "empty specifier"},
		{"Bash(\x00foo)", "NUL byte"},
		{"", "empty"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, err := ParseRule(c.in)
			if err == nil {
				t.Fatalf("ParseRule(%q) expected error containing %q", c.in, c.wantErrSub)
			}
			if !strings.Contains(err.Error(), c.wantErrSub) {
				t.Errorf("ParseRule(%q) error = %v, want substring %q", c.in, err, c.wantErrSub)
			}
		})
	}
}

func TestParseRule_StarNormalizesToBare(t *testing.T) {
	for _, in := range []string{"Bash(*)", "Skill(*)"} {
		r, err := ParseRule(in)
		if err != nil {
			t.Fatalf("ParseRule(%q) error: %v", in, err)
		}
		// Tool(*) normalizes to bare-form semantics — String() preserves source.
		if r.String() != in {
			t.Errorf("Rule.String() = %q, want %q (source preserved)", r.String(), in)
		}
	}
}
```

Add `"strings"` to the imports if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestParseRule_ParenStructure|TestParseRule_StarNormalizesToBare" -count=1`
Expected: FAIL — current `ParseRule` returns "not implemented yet" for these inputs.

- [ ] **Step 3: Write minimal implementation**

Replace the `ParseRule` function in `agent/permissions.go` with:

```go
func ParseRule(s string) (Rule, error) {
	if s == "" {
		return nil, fmt.Errorf("permission rule %q: empty", s)
	}
	if strings.ContainsRune(s, 0) {
		return nil, fmt.Errorf("permission rule %q: NUL byte not allowed", s)
	}

	// Split into "<tool>" or "<tool>(<spec>)".
	tool := s
	var spec string
	if i := strings.IndexByte(s, '('); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			return nil, fmt.Errorf("permission rule %q: unbalanced parentheses", s)
		}
		// Reject extra trailing ')' like "Bash(rm))".
		if strings.Count(s, "(") != strings.Count(s, ")") {
			return nil, fmt.Errorf("permission rule %q: unbalanced parentheses", s)
		}
		tool = s[:i]
		spec = s[i+1 : len(s)-1]
		if spec == "" {
			return nil, fmt.Errorf("permission rule %q: empty specifier", s)
		}
	}

	// Tool(*) is sugar for bare-tool.
	if spec == "*" {
		if knownTools[tool] {
			return bareRule{tool: tool, source: s}, nil
		}
	}

	if spec == "" {
		if knownTools[tool] {
			return bareRule{tool: tool, source: s}, nil
		}
		// Bare unknown keyword: forward-compat exact-match.
		if strings.HasPrefix(tool, "mcp__") {
			return nil, fmt.Errorf("permission rule %q: MCP rules not implemented yet", s)
		}
		return nil, fmt.Errorf("permission rule %q: tool keyword not supported yet", s)
	}

	return nil, fmt.Errorf("permission rule %q: tool keyword not supported yet", s)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestParseRule_ParenStructure|TestParseRule_StarNormalizesToBare|TestParseRule_Bare" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): parse tool(specifier) shell + error cases"
```

---

## Task 4: ParseRule — Bash pattern compilation

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_BashPatterns(t *testing.T) {
	// These exercise parse only — matching semantics are tested in
	// TestEvaluateBash. Every input must parse without error and round-trip
	// via Rule.String() to the verbatim source.
	cases := []string{
		"Bash(npm run build)",
		"Bash(npm run *)",
		"Bash(ls*)",
		"Bash(* install)",
		"Bash(git * main)",
		"Bash(ls:*)",
		"Bash(git:* push)",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			r, err := ParseRule(in)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", in, err)
			}
			if r.String() != in {
				t.Errorf("Rule.String() = %q, want %q", r.String(), in)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseRule_BashPatterns -count=1`
Expected: FAIL — "tool keyword not supported yet".

- [ ] **Step 3: Write minimal implementation**

Add to `agent/permissions.go`. First add to the imports: `"regexp"`.

Then add the bash rule type and parser branch. Replace the trailing `"tool keyword not supported yet"` line in `ParseRule` so the `Bash` case is handled. Updated `ParseRule`:

```go
func ParseRule(s string) (Rule, error) {
	if s == "" {
		return nil, fmt.Errorf("permission rule %q: empty", s)
	}
	if strings.ContainsRune(s, 0) {
		return nil, fmt.Errorf("permission rule %q: NUL byte not allowed", s)
	}

	tool := s
	var spec string
	if i := strings.IndexByte(s, '('); i >= 0 {
		if !strings.HasSuffix(s, ")") {
			return nil, fmt.Errorf("permission rule %q: unbalanced parentheses", s)
		}
		if strings.Count(s, "(") != strings.Count(s, ")") {
			return nil, fmt.Errorf("permission rule %q: unbalanced parentheses", s)
		}
		tool = s[:i]
		spec = s[i+1 : len(s)-1]
		if spec == "" {
			return nil, fmt.Errorf("permission rule %q: empty specifier", s)
		}
	}

	if spec == "*" {
		if knownTools[tool] {
			return bareRule{tool: tool, source: s}, nil
		}
	}

	if spec == "" {
		if knownTools[tool] {
			return bareRule{tool: tool, source: s}, nil
		}
		if strings.HasPrefix(tool, "mcp__") {
			return nil, fmt.Errorf("permission rule %q: MCP rules not implemented yet", s)
		}
		return nil, fmt.Errorf("permission rule %q: tool keyword not supported yet", s)
	}

	switch tool {
	case "Bash":
		re, err := compileBashPattern(spec)
		if err != nil {
			return nil, fmt.Errorf("permission rule %q: %w", s, err)
		}
		return bashRule{pattern: re, source: s}, nil
	}
	return nil, fmt.Errorf("permission rule %q: tool keyword not supported yet", s)
}

// bashRule matches a Bash command pattern against toolInput["command"].
type bashRule struct {
	pattern *regexp.Regexp
	source  string
}

func (r bashRule) String() string { return r.source }
func (r bashRule) Matches(toolName string, in map[string]any, _ ExecutionEnvironment) bool {
	if toolName != "Bash" {
		return false
	}
	cmd, _ := in["command"].(string)
	if cmd == "" {
		return false
	}
	subs := splitCompound(cmd)
	for _, sub := range subs {
		stripped := stripBashWrappers(sub)
		if !r.pattern.MatchString(stripped) {
			return false
		}
	}
	return true
}

// compileBashPattern translates a Claude Code Bash glob pattern into a regex.
//
// Wildcards (per code.claude.com/docs/en/permissions):
//   - "ls*"        → substring-prefix without word boundary; "lsof" matches.
//   - "npm run *"  → prefix with required word boundary before remainder.
//   - "* install"  → suffix with required word boundary before "install".
//   - "git * main" → interior; "*" matches any sequence including spaces.
//   - trailing ":*" at end of pattern is sugar for trailing " *".
func compileBashPattern(spec string) (*regexp.Regexp, error) {
	// Trailing ":*" at the very end is sugar for " *". Mid-pattern ":" is literal.
	if strings.HasSuffix(spec, ":*") {
		spec = spec[:len(spec)-2] + " *"
	}
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(spec); i++ {
		c := spec[i]
		switch c {
		case '*':
			// Wildcard. The space-adjacency around the star enforces word
			// boundaries; we emit ".*" and let the surrounding literal spaces
			// (or anchors) supply the boundary.
			b.WriteString(".*")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("bad bash pattern %q: %w", spec, err)
	}
	return re, nil
}

// splitCompound is implemented in task 5. Stub for now.
func splitCompound(cmd string) []string { return []string{cmd} }

// stripBashWrappers is implemented in task 6. Stub for now.
func stripBashWrappers(cmd string) string { return cmd }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseRule_BashPatterns -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): compile Bash patterns to regex"
```

---

## Task 5: Bash compound-command splitting (quote-aware)

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestSplitCompound(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"ls", []string{"ls"}},
		{"ls && pwd", []string{"ls", "pwd"}},
		{"a||b", []string{"a", "b"}},
		{"a ; b ; c", []string{"a", "b", "c"}},
		{"a | b", []string{"a", "b"}},
		{"a |& b", []string{"a", "b"}},
		{"a & b", []string{"a", "b"}},
		{"a\nb", []string{"a", "b"}},
		// Quoted operators are literal.
		{`echo "a && b"`, []string{`echo "a && b"`}},
		{`echo 'a;b' ; pwd`, []string{`echo 'a;b'`, "pwd"}},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := splitCompound(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("splitCompound(%q) = %q, want %q", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("splitCompound(%q) = %q, want %q", c.in, got, c.want)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSplitCompound -count=1`
Expected: FAIL — stub returns `[]string{cmd}`.

- [ ] **Step 3: Write minimal implementation**

Replace the stub `splitCompound` in `agent/permissions.go` with:

```go
// splitCompound splits cmd on shell operators (&&, ||, ;, |, |&, &, newline),
// respecting single- and double-quoted spans. Each subcommand is trimmed.
func splitCompound(cmd string) []string {
	var out []string
	var buf strings.Builder
	flush := func() {
		s := strings.TrimSpace(buf.String())
		if s != "" {
			out = append(out, s)
		}
		buf.Reset()
	}
	var inSingle, inDouble bool
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		if !inDouble && c == '\'' {
			inSingle = !inSingle
			buf.WriteByte(c)
			continue
		}
		if !inSingle && c == '"' {
			inDouble = !inDouble
			buf.WriteByte(c)
			continue
		}
		if inSingle || inDouble {
			buf.WriteByte(c)
			continue
		}
		// Operator detection.
		if c == '\n' {
			flush()
			continue
		}
		if c == ';' {
			flush()
			continue
		}
		if c == '&' && i+1 < len(cmd) && cmd[i+1] == '&' {
			flush()
			i++
			continue
		}
		if c == '|' && i+1 < len(cmd) && cmd[i+1] == '|' {
			flush()
			i++
			continue
		}
		if c == '|' && i+1 < len(cmd) && cmd[i+1] == '&' {
			flush()
			i++
			continue
		}
		if c == '|' {
			flush()
			continue
		}
		if c == '&' {
			flush()
			continue
		}
		buf.WriteByte(c)
	}
	flush()
	if len(out) == 0 {
		return []string{strings.TrimSpace(cmd)}
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestSplitCompound -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): quote-aware bash compound splitter"
```

---

## Task 6: Bash process-wrapper stripping

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestStripBashWrappers(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"npm test", "npm test"},
		{"timeout 30 npm test", "npm test"},
		{"time npm test", "npm test"},
		{"nice -n 10 npm test", "npm test"},
		{"nohup npm test", "npm test"},
		{"stdbuf -oL npm test", "npm test"},
		{"xargs npm test", "npm test"},
		// xargs WITH flags is not stripped.
		{"xargs -n1 npm test", "xargs -n1 npm test"},
		// Unrelated wrappers are not stripped.
		{"docker exec foo npm test", "docker exec foo npm test"},
		{"watch npm test", "watch npm test"},
		// Multiple wrappers compose (timeout, then time).
		{"timeout 30 time npm test", "npm test"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got := stripBashWrappers(c.in)
			if got != c.want {
				t.Errorf("stripBashWrappers(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestStripBashWrappers -count=1`
Expected: FAIL — stub returns input unchanged.

- [ ] **Step 3: Write minimal implementation**

Replace the stub `stripBashWrappers` in `agent/permissions.go` with:

```go
// stripBashWrappers peels process-wrapper prefixes that the Claude Code docs
// list as "safe to strip". Each wrapper has an arity: how many extra tokens
// (flags/args) it consumes before the wrapped command begins.
//
//   timeout DURATION ...   → 1 extra token
//   time ...               → 0 extra tokens
//   nice [-n N] ...        → if next token starts with "-", consume one extra
//   nohup ...              → 0
//   stdbuf -oL/-eL/-iL ... → if next token starts with "-", consume one extra
//   xargs ...              → only stripped when the very next token does not
//                            start with "-" (bare xargs).
func stripBashWrappers(cmd string) string {
	for {
		fields := strings.Fields(cmd)
		if len(fields) == 0 {
			return cmd
		}
		switch fields[0] {
		case "timeout":
			if len(fields) >= 3 {
				cmd = strings.Join(fields[2:], " ")
				continue
			}
		case "time", "nohup":
			if len(fields) >= 2 {
				cmd = strings.Join(fields[1:], " ")
				continue
			}
		case "nice", "stdbuf":
			drop := 1
			if len(fields) >= 2 && strings.HasPrefix(fields[1], "-") {
				drop = 2
			}
			if len(fields) > drop {
				cmd = strings.Join(fields[drop:], " ")
				continue
			}
		case "xargs":
			if len(fields) >= 2 && !strings.HasPrefix(fields[1], "-") {
				cmd = strings.Join(fields[1:], " ")
				continue
			}
		}
		return cmd
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestStripBashWrappers -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): strip safe Bash process wrappers"
```

---

## Task 7: ParseRule — Read/Edit/Write path patterns

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_Paths(t *testing.T) {
	cases := []struct{ in, wantNorm string }{
		{"Read(./.env)", "Read(./.env)"},
		{"Read(.env)", "Read(.env)"},
		{"Read(//tmp/scratch.txt)", "Read(//tmp/scratch.txt)"},
		{"Read(~/.zshrc)", "Read(~/.zshrc)"},
		{"Read(/docs/**)", "Read(/docs/**)"},
		{"Edit(src/**/*.ts)", "Edit(src/**/*.ts)"},
		{"Write(src/foo.go)", "Write(src/foo.go)"},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			r, err := ParseRule(c.in)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.in, err)
			}
			if r.String() != c.wantNorm {
				t.Errorf("Rule.String() = %q, want %q", r.String(), c.wantNorm)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseRule_Paths -count=1`
Expected: FAIL — "tool keyword not supported yet".

- [ ] **Step 3: Write minimal implementation**

Add to `agent/permissions.go`:

```go
// pathAnchor is the gitignore anchor type for a path pattern.
type pathAnchor int

const (
	anchorCwd      pathAnchor = iota // "path" or "./path"
	anchorProject                    // "/path" (single leading slash)
	anchorAbsolute                   // "//path"
	anchorHome                       // "~/path"
)

// pathRule matches Read/Edit/Write tool calls against a gitignore-style path
// pattern pinned to one anchor.
type pathRule struct {
	tools   []string // which Claude tool names this rule fires on
	anchor  pathAnchor
	pattern string // remaining path portion after stripping the anchor prefix
	source  string
}

func (r pathRule) String() string { return r.source }
func (r pathRule) Matches(toolName string, in map[string]any, env ExecutionEnvironment) bool {
	hit := false
	for _, t := range r.tools {
		if toolName == t {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	target, _ := in["file_path"].(string)
	if target == "" {
		target, _ = in["path"].(string)
	}
	if target == "" {
		return false
	}
	return matchPathRule(r, target, env)
}

// matchPathRule resolves the target path against the rule's anchor and tests
// the pattern with doublestar globbing. Implementation lands in task 8.
func matchPathRule(r pathRule, target string, env ExecutionEnvironment) bool {
	return false
}

// pathToolGroup maps a Read/Edit/Write/NotebookEdit rule keyword to the set of
// Claude tool names it fires on.
//
//   Read   → Read, Grep, Glob (best-effort per CC docs)
//   Edit   → Edit, Write, NotebookEdit
//   Write  → alias for Edit
func pathToolGroup(keyword string) []string {
	switch keyword {
	case "Read":
		return []string{"Read", "Grep", "Glob"}
	case "Edit", "Write":
		return []string{"Edit", "Write", "NotebookEdit"}
	}
	return nil
}

// parsePathSpec strips the anchor prefix off a path specifier and returns
// (anchor, remaining-pattern). The remaining pattern is what the doublestar
// matcher will test against the resolved candidate path.
func parsePathSpec(spec string) (pathAnchor, string) {
	switch {
	case strings.HasPrefix(spec, "//"):
		return anchorAbsolute, spec[1:] // keep one leading "/" for matching against an abs path
	case strings.HasPrefix(spec, "~/"):
		return anchorHome, spec[2:]
	case strings.HasPrefix(spec, "/"):
		return anchorProject, spec[1:]
	case strings.HasPrefix(spec, "./"):
		return anchorCwd, spec[2:]
	default:
		return anchorCwd, spec
	}
}
```

Then update `ParseRule`'s `switch tool` block to add the path-tool cases:

```go
	switch tool {
	case "Bash":
		re, err := compileBashPattern(spec)
		if err != nil {
			return nil, fmt.Errorf("permission rule %q: %w", s, err)
		}
		return bashRule{pattern: re, source: s}, nil
	case "Read", "Edit", "Write":
		anchor, pattern := parsePathSpec(spec)
		return pathRule{
			tools:   pathToolGroup(tool),
			anchor:  anchor,
			pattern: pattern,
			source:  s,
		}, nil
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseRule_Paths -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): parse Read/Edit/Write path rules"
```

---

## Task 8: Path-rule matching against the filesystem (anchor resolution)

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluatePaths(t *testing.T) {
	root := t.TempDir()
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Materialize the fixture tree so absolute paths exist.
	mustMkdir(t, filepath.Join(root, "sub"))
	mustMkdir(t, filepath.Join(root, "docs"))
	mustMkdir(t, filepath.Join(root, "src"))
	mustWrite(t, filepath.Join(root, ".env"), "")
	mustWrite(t, filepath.Join(root, "sub", ".env"), "")
	mustWrite(t, filepath.Join(root, "docs", "x.md"), "")
	mustWrite(t, filepath.Join(root, "src", "main.go"), "")
	mustWrite(t, filepath.Join(home, ".zshrc"), "")

	env := NewLocalExecutionEnvironment(root)

	cases := []struct {
		name     string
		rule     string
		toolName string
		filePath string
		want     bool
	}{
		{"cwd-anchored exact", "Read(./.env)", "Read", ".env", true},
		{"cwd-anchored exact mismatch", "Read(./.env)", "Read", "src/main.go", false},
		{"unanchored is depth-agnostic", "Read(.env)", "Read", "sub/.env", true},
		{"project-anchor docs", "Read(/docs/**)", "Read", "docs/x.md", true},
		{"project-anchor not deep", "Read(/docs/**)", "Read", ".claude/docs/x.md", false},
		{"absolute matches abs path", "Read(//tmp/scratch.txt)", "Read", "/tmp/scratch.txt", true},
		{"absolute does not match relative", "Read(//tmp/scratch.txt)", "Read", "tmp/scratch.txt", false},
		{"home expands", "Read(~/.zshrc)", "Read", filepath.Join(home, ".zshrc"), true},
		{"edit doublestar ts match", "Edit(src/**/*.ts)", "Edit", "src/a/b/foo.ts", true},
		{"edit doublestar ts miss", "Edit(src/**/*.ts)", "Edit", "src/a/b/foo.go", false},
		{"missing file_path field", "Read(./.env)", "Read", "", false},
		{"write alias fires on Edit tool", "Write(src/foo.go)", "Edit", "src/foo.go", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			in := map[string]any{}
			if c.filePath != "" {
				in["file_path"] = c.filePath
			}
			got := r.Matches(c.toolName, in, env)
			if got != c.want {
				t.Errorf("rule %q against %s{file_path:%q} = %v, want %v", c.rule, c.toolName, c.filePath, got, c.want)
			}
		})
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, content string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

Add `"os"` and `"path/filepath"` to the test file imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluatePaths -count=1`
Expected: FAIL — `matchPathRule` returns false for every case.

- [ ] **Step 3: Write minimal implementation**

Add `"github.com/bmatcuk/doublestar/v4"` to `agent/permissions.go` imports.

Replace `matchPathRule` with:

```go
// matchPathRule resolves the target against the anchor and tests with
// doublestar globbing (gitignore-style: ** crosses segments, * stays in one).
func matchPathRule(r pathRule, target string, env ExecutionEnvironment) bool {
	candidate, ok := candidatePathForAnchor(r.anchor, target, env)
	if !ok {
		return false
	}
	// Build the full pattern under the same anchor for comparison.
	pat := patternUnderAnchor(r.anchor, r.pattern, env)
	if pat == "" || candidate == "" {
		return false
	}
	// Gitignore semantics: a bare-filename pattern matches at any depth.
	// doublestar's PathMatch doesn't grant depth-agnostic by default, so
	// when the pattern has no slash and no "**", wrap it with "**/".
	matchPattern := pat
	if !strings.ContainsAny(matchPattern, "/") && !strings.Contains(matchPattern, "**") {
		matchPattern = "**/" + matchPattern
	}
	ok, err := doublestar.PathMatch(matchPattern, candidate)
	if err != nil {
		return false
	}
	return ok
}

// candidatePathForAnchor returns the form of `target` that should be compared
// against a pattern anchored at `anchor`. The bool is false when the target
// cannot match this anchor at all (e.g. a relative path against an absolute
// anchor).
func candidatePathForAnchor(anchor pathAnchor, target string, env ExecutionEnvironment) (string, bool) {
	switch anchor {
	case anchorAbsolute:
		if !filepath.IsAbs(target) {
			return "", false
		}
		return filepath.Clean(target), true
	case anchorHome:
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return "", false
		}
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(env.WorkingDirectory(), abs)
		}
		rel, err := filepath.Rel(home, filepath.Clean(abs))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return rel, true
	case anchorProject:
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(env.WorkingDirectory(), abs)
		}
		rel, err := filepath.Rel(env.WorkingDirectory(), filepath.Clean(abs))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return rel, true
	case anchorCwd:
		abs := target
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(env.WorkingDirectory(), abs)
		}
		rel, err := filepath.Rel(env.WorkingDirectory(), filepath.Clean(abs))
		if err != nil || strings.HasPrefix(rel, "..") {
			return "", false
		}
		return rel, true
	}
	return "", false
}

// patternUnderAnchor returns the doublestar pattern to test against the
// (already-resolved) candidate path.
func patternUnderAnchor(anchor pathAnchor, pattern string, env ExecutionEnvironment) string {
	switch anchor {
	case anchorAbsolute:
		// pattern was kept with one leading "/" (see parsePathSpec).
		return filepath.Clean(pattern)
	case anchorHome, anchorProject, anchorCwd:
		return pattern
	}
	return pattern
}
```

Add `"os"` and `"path/filepath"` to `agent/permissions.go` imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestEvaluatePaths|TestParseRule_Paths" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): resolve path-rule anchors with doublestar"
```

---

## Task 9: ParseRule — WebFetch domain specifier

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_WebFetch(t *testing.T) {
	ok := []string{
		"WebFetch",
		"WebFetch(domain:example.com)",
		"WebFetch(domain:*.example.com)",
	}
	for _, in := range ok {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseRule(in); err != nil {
				t.Fatalf("ParseRule(%q) error: %v", in, err)
			}
		})
	}
	bad := []struct {
		in, want string
	}{
		{"WebFetch(domain:**.example.com)", "** not supported"},
		{"WebFetch(port:443)", "unsupported specifier prefix"},
		{"WebFetch(scheme:https)", "unsupported specifier prefix"},
		{"WebFetch(example.com)", "missing specifier prefix"},
	}
	for _, c := range bad {
		t.Run(c.in, func(t *testing.T) {
			_, err := ParseRule(c.in)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("ParseRule(%q) error = %v, want substring %q", c.in, err, c.want)
			}
		})
	}
}

func TestEvaluateWebFetch(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cases := []struct {
		rule, url string
		want      bool
	}{
		{"WebFetch", "https://anywhere.test/x", true},
		{"WebFetch(domain:github.com)", "https://github.com/x", true},
		{"WebFetch(domain:github.com)", "https://gitlab.com/x", false},
		{"WebFetch(domain:*.example.com)", "https://api.example.com/x", true},
		{"WebFetch(domain:*.example.com)", "https://example.com/x", false},
		// Missing url → no match.
		{"WebFetch(domain:github.com)", "", false},
		// Malformed url → no match.
		{"WebFetch(domain:github.com)", "::not a url::", false},
	}
	for _, c := range cases {
		t.Run(c.rule+"|"+c.url, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			in := map[string]any{}
			if c.url != "" {
				in["url"] = c.url
			}
			got := r.Matches("WebFetch", in, env)
			if got != c.want {
				t.Errorf("rule %q vs %q = %v, want %v", c.rule, c.url, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestParseRule_WebFetch|TestEvaluateWebFetch" -count=1`
Expected: FAIL — "tool keyword not supported yet".

- [ ] **Step 3: Write minimal implementation**

Add `"net/url"` to `agent/permissions.go` imports.

Append to `agent/permissions.go`:

```go
// webFetchRule matches a WebFetch tool call against a domain specifier.
type webFetchRule struct {
	host    string // "example.com" or "*.example.com"
	source  string
}

func (r webFetchRule) String() string { return r.source }
func (r webFetchRule) Matches(toolName string, in map[string]any, _ ExecutionEnvironment) bool {
	if toolName != "WebFetch" {
		return false
	}
	raw, _ := in["url"].(string)
	if raw == "" {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	return matchHost(r.host, u.Host)
}

// matchHost matches "example.com" exactly, or "*.example.com" against a single
// subdomain level (api.example.com matches; deeper.api.example.com does not).
func matchHost(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".example.com"
		if !strings.HasSuffix(host, suffix) {
			return false
		}
		// Allow exactly one segment in front (api.example.com), not (a.b.example.com).
		prefix := host[:len(host)-len(suffix)]
		if prefix == "" || strings.Contains(prefix, ".") {
			return false
		}
		return true
	}
	return false
}
```

Update the `switch tool` in `ParseRule` to add WebFetch (note: bare-only `WebSearch` is already handled by the bare-form branch above):

```go
	case "WebFetch":
		if !strings.Contains(spec, ":") {
			return nil, fmt.Errorf("permission rule %q: missing specifier prefix (expected 'domain:...')", s)
		}
		prefix := spec[:strings.IndexByte(spec, ':')]
		value := spec[strings.IndexByte(spec, ':')+1:]
		switch prefix {
		case "domain":
			if strings.Contains(value, "**") {
				return nil, fmt.Errorf("permission rule %q: ** not supported in host pattern", s)
			}
			return webFetchRule{host: value, source: s}, nil
		case "port", "scheme", "path":
			return nil, fmt.Errorf("permission rule %q: unsupported specifier prefix %q (reserved)", s, prefix)
		default:
			return nil, fmt.Errorf("permission rule %q: unsupported specifier prefix %q", s, prefix)
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestParseRule_WebFetch|TestEvaluateWebFetch" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): WebFetch domain matching"
```

---

## Task 10: ParseRule — Skill and Agent specifiers

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluateSkillAgent(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cases := []struct {
		rule     string
		toolName string
		in       map[string]any
		want     bool
	}{
		{"Skill", "Skill", map[string]any{"name": "anything"}, true},
		{"Skill(my-skill)", "Skill", map[string]any{"name": "my-skill"}, true},
		{"Skill(my-skill)", "Skill", map[string]any{"name": "other"}, false},
		{"Skill(*)", "Skill", map[string]any{"name": "anything"}, true},
		{"Skill(my-skill)", "Skill", map[string]any{}, false},
		{"Agent(Explore)", "Task", map[string]any{"subagent_type": "Explore"}, true},
		{"Agent(Explore)", "Task", map[string]any{"subagent_type": "Plan"}, false},
		{"Agent(Explore)", "Task", map[string]any{}, false},
		{"Agent(my-custom)", "Task", map[string]any{"subagent_type": "my-custom"}, true},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			got := r.Matches(c.toolName, c.in, env)
			if got != c.want {
				t.Errorf("rule %q vs %s%v = %v, want %v", c.rule, c.toolName, c.in, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluateSkillAgent -count=1`
Expected: FAIL — bare `Agent` rule matches `toolName=="Agent"` but the test passes `"Task"`. Even bare cases fail until we wire Agent→Task aliasing.

- [ ] **Step 3: Write minimal implementation**

Append to `agent/permissions.go`:

```go
// nameMatchRule matches a tool call whose `inputField` value equals the rule's
// `name` (or matches a Bash-style wildcard pattern). Used for Skill and Agent.
type nameMatchRule struct {
	toolNames  []string // accepted Claude tool names
	inputField string   // key in toolInput
	pattern    *regexp.Regexp
	source     string
}

func (r nameMatchRule) String() string { return r.source }
func (r nameMatchRule) Matches(toolName string, in map[string]any, _ ExecutionEnvironment) bool {
	hit := false
	for _, t := range r.toolNames {
		if toolName == t {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	v, _ := in[r.inputField].(string)
	if v == "" {
		return false
	}
	return r.pattern.MatchString(v)
}
```

Update the `switch tool` in `ParseRule` to add Skill and Agent. Skill uses bare form when `spec == ""` (already handled). With specifier:

```go
	case "Skill":
		re, err := compileBashPattern(spec)
		if err != nil {
			return nil, fmt.Errorf("permission rule %q: %w", s, err)
		}
		return nameMatchRule{
			toolNames:  []string{"Skill"},
			inputField: "name",
			pattern:    re,
			source:     s,
		}, nil
	case "Agent":
		re, err := compileBashPattern(spec)
		if err != nil {
			return nil, fmt.Errorf("permission rule %q: %w", s, err)
		}
		return nameMatchRule{
			// Serf maps spawn_agent → "Task" (Claude's name). The rule keyword
			// is "Agent" per Claude docs; accept both forms so a rule typed as
			// Agent(Explore) fires on a Task{subagent_type:"Explore"} call.
			toolNames:  []string{"Task", "Agent"},
			inputField: "subagent_type",
			pattern:    re,
			source:     s,
		}, nil
```

Also extend bare-form handling so that bare `Agent` fires on `Task` calls. Update the `if spec == ""` branch in `ParseRule`:

```go
	if spec == "" || spec == "*" {
		if tool == "Agent" {
			// Bare Agent — any subagent invocation. Accept Claude's "Task"
			// tool name (serf's spawn_agent maps to Task).
			return agentBareRule{source: s}, nil
		}
		if knownTools[tool] {
			return bareRule{tool: tool, source: s}, nil
		}
		if strings.HasPrefix(tool, "mcp__") {
			return nil, fmt.Errorf("permission rule %q: MCP rules not implemented yet", s)
		}
		return nil, fmt.Errorf("permission rule %q: tool keyword not supported yet", s)
	}
```

And add:

```go
type agentBareRule struct{ source string }

func (r agentBareRule) String() string { return r.source }
func (r agentBareRule) Matches(toolName string, _ map[string]any, _ ExecutionEnvironment) bool {
	return toolName == "Task" || toolName == "Agent"
}
```

(You will need to remove the duplicate `if spec == ""` block that exists from Task 3; this version replaces it.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestEvaluateSkillAgent|TestParseRule_Bare|TestParseRule_StarNormalizesToBare" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): Skill and Agent rule matching"
```

---

## Task 11: ParseRule — MCP rules

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_MCP(t *testing.T) {
	cases := []string{
		"mcp__puppeteer",
		"mcp__puppeteer__*",
		"mcp__puppeteer__click",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseRule(in); err != nil {
				t.Fatalf("ParseRule(%q) error: %v", in, err)
			}
		})
	}
}

func TestEvaluateMCP(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cases := []struct {
		rule, tool string
		want       bool
	}{
		{"mcp__puppeteer", "mcp__puppeteer__navigate", true},
		{"mcp__puppeteer", "mcp__puppeteer__click", true},
		{"mcp__puppeteer", "mcp__other__navigate", false},
		{"mcp__puppeteer__*", "mcp__puppeteer__navigate", true},
		{"mcp__puppeteer__click", "mcp__puppeteer__click", true},
		{"mcp__puppeteer__click", "mcp__puppeteer__navigate", false},
	}
	for _, c := range cases {
		t.Run(c.rule+"|"+c.tool, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			got := r.Matches(c.tool, nil, env)
			if got != c.want {
				t.Errorf("rule %q vs tool %q = %v, want %v", c.rule, c.tool, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestParseRule_MCP|TestEvaluateMCP" -count=1`
Expected: FAIL — "MCP rules not implemented yet".

- [ ] **Step 3: Write minimal implementation**

Append to `agent/permissions.go`:

```go
// mcpRule matches one or more MCP tool calls by name. Three shapes:
//   mcp__server                  → prefix match  (tool starts with "mcp__server__")
//   mcp__server__*               → equivalent to the prefix form
//   mcp__server__toolname        → exact match on full tool name
type mcpRule struct {
	pattern  string // either "mcp__server__" (prefix) or "mcp__server__tool" (exact)
	isPrefix bool
	source   string
}

func (r mcpRule) String() string { return r.source }
func (r mcpRule) Matches(toolName string, _ map[string]any, _ ExecutionEnvironment) bool {
	if r.isPrefix {
		return strings.HasPrefix(toolName, r.pattern)
	}
	return toolName == r.pattern
}
```

Replace the `MCP rules not implemented yet` branch in `ParseRule`. Add an early branch at the top of `ParseRule` after the parens have been split:

```go
	if strings.HasPrefix(tool, "mcp__") {
		// MCP rules carry no parenthesized specifier.
		if spec != "" {
			return nil, fmt.Errorf("permission rule %q: MCP rules cannot take a specifier", s)
		}
		body := tool[len("mcp__"):]
		if body == "" {
			return nil, fmt.Errorf("permission rule %q: MCP rule missing server name", s)
		}
		// "mcp__server" or "mcp__server__*" → prefix on "mcp__server__".
		// "mcp__server__tool"                → exact.
		if strings.HasSuffix(tool, "__*") {
			return mcpRule{pattern: tool[:len(tool)-1], isPrefix: true, source: s}, nil
		}
		if !strings.Contains(body, "__") {
			// bare server form.
			return mcpRule{pattern: tool + "__", isPrefix: true, source: s}, nil
		}
		return mcpRule{pattern: tool, isPrefix: false, source: s}, nil
	}
```

Make sure this branch runs after the spec split but before the `Tool(*)` normalization (i.e. immediately after we've parsed `tool` and `spec`). Remove the older `if strings.HasPrefix(tool, "mcp__")` error-only stubs from earlier tasks.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestParseRule_MCP|TestEvaluateMCP" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): MCP server/tool rule matching"
```

---

## Task 12: Forward-compat unknown tool keyword + PowerShell inert

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_UnknownAndInert(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())

	// PowerShell parses but never matches under serf.
	psRule, err := ParseRule("PowerShell(Get-ChildItem *)")
	if err != nil {
		t.Fatalf("ParseRule(PowerShell) error: %v", err)
	}
	if psRule.Matches("PowerShell", map[string]any{"command": "Get-ChildItem foo"}, env) {
		t.Errorf("PowerShell rule should be inert under serf")
	}

	// Unknown tool keyword parses (forward-compat) and treats the specifier as
	// a literal equality probe on a field named "command" (best-effort default).
	r, err := ParseRule("Unknown(thing)")
	if err != nil {
		t.Fatalf("ParseRule(Unknown) error: %v", err)
	}
	if r.String() != "Unknown(thing)" {
		t.Errorf("String() = %q, want %q", r.String(), "Unknown(thing)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseRule_UnknownAndInert -count=1`
Expected: FAIL — "tool keyword not supported yet".

- [ ] **Step 3: Write minimal implementation**

Append to `agent/permissions.go`:

```go
// inertRule parses cleanly but never matches under serf. Used for PowerShell
// and any other Claude Code tool family serf does not host.
type inertRule struct{ source string }

func (r inertRule) String() string                                              { return r.source }
func (r inertRule) Matches(string, map[string]any, ExecutionEnvironment) bool { return false }
```

Update `ParseRule`'s `switch tool` to add `PowerShell` (always inert) and a `default` branch that returns an `inertRule` for any other known-keyword-shape input:

```go
	case "PowerShell":
		return inertRule{source: s}, nil
	}

	// Forward-compat: unknown tool keyword with a specifier parses but is
	// inert. We do not invent semantics; SP2 logs a warning at session
	// startup (TODO: hook this to a warn channel in §11 deferrals).
	return inertRule{source: s}, nil
```

(Replace the trailing `"tool keyword not supported yet"` return.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestParseRule_UnknownAndInert|TestParseRule_Bare|TestParseRule_BashPatterns|TestParseRule_Paths|TestParseRule_WebFetch|TestParseRule_MCP|TestParseRule_ParenStructure" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): PowerShell inert + forward-compat unknown tools"
```

---

## Task 13: Bash matching — word-boundary semantics

**Files:**
- Modify: `agent/permissions_test.go`

Bash compile-to-regex was implemented in Task 4, but we have not yet asserted the documented word-boundary semantics. This task pins them.

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluateBash_WordBoundary(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cases := []struct {
		rule, cmd string
		want      bool
	}{
		// Exact match.
		{"Bash(npm run build)", "npm run build", true},
		{"Bash(npm run build)", "npm run test", false},

		// Prefix with word boundary.
		{"Bash(npm run *)", "npm run test", true},
		{"Bash(npm run *)", "npm run-build", false},
		{"Bash(npm run *)", "npm run", false}, // requires trailing space + something

		// Prefix without boundary.
		{"Bash(ls*)", "lsof", true},
		{"Bash(ls*)", "ls -la", true},
		{"Bash(ls*)", "ls", true},

		// Suffix.
		{"Bash(* install)", "npm install", true},
		{"Bash(* install)", "install", false},

		// Interior.
		{"Bash(git * main)", "git push origin main", true},
		{"Bash(git * main)", "git status", false},

		// Trailing :* is sugar for trailing " *".
		{"Bash(ls:*)", "ls -la", true},
		{"Bash(ls:*)", "ls", false}, // ls alone does not match "ls *"

		// Mid-pattern colon is literal.
		{"Bash(git:* push)", "git push origin main", false},
		{"Bash(git:* push)", "git:foo push", true},
	}
	for _, c := range cases {
		t.Run(c.rule+"|"+c.cmd, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			in := map[string]any{"command": c.cmd}
			got := r.Matches("Bash", in, env)
			if got != c.want {
				t.Errorf("rule %q vs cmd %q = %v, want %v", c.rule, c.cmd, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluateBash_WordBoundary -count=1`
Expected: FAIL — naive `*` → `.*` does not enforce the trailing-space requirement on `Bash(npm run *)`.

- [ ] **Step 3: Write minimal implementation**

Replace `compileBashPattern` in `agent/permissions.go` with a boundary-aware translator:

```go
// compileBashPattern translates a Claude Code Bash glob pattern into a regex
// that enforces the documented word-boundary rules.
//
//   "X*"      → "^X.*$"            (no boundary; "lsof" matches "ls*")
//   " *" (suffix) → require whitespace before remainder
//   "* X"     → require whitespace after remainder ("install" alone does not
//               match "* install")
//   trailing ":*" is sugar for trailing " *".
func compileBashPattern(spec string) (*regexp.Regexp, error) {
	if strings.HasSuffix(spec, ":*") {
		spec = spec[:len(spec)-2] + " *"
	}
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(spec) {
		c := spec[i]
		if c == '*' {
			// Look at neighbors to decide whether to emit a boundary-respecting form.
			prevIsSpace := i > 0 && spec[i-1] == ' '
			nextIsSpace := i+1 < len(spec) && spec[i+1] == ' '
			switch {
			case prevIsSpace && nextIsSpace:
				// " * "  → interior. Already preceded by literal space we emitted.
				b.WriteString(".*")
			case prevIsSpace && i+1 == len(spec):
				// Trailing " *" — at least one non-space char must follow.
				// We already emitted the literal space. Require at least one
				// non-whitespace char to anchor the wildcard.
				b.WriteString(`\S.*`)
			case !prevIsSpace && i == 0 && nextIsSpace:
				// Leading "* " — must have at least one non-space + space.
				b.WriteString(`.*\S `)
				i += 2 // consume "* " together (we already wrote the trailing space)
				continue
			default:
				b.WriteString(".*")
			}
			i++
			continue
		}
		b.WriteString(regexp.QuoteMeta(string(c)))
		i++
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("bad bash pattern %q: %w", spec, err)
	}
	return re, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestEvaluateBash_WordBoundary|TestParseRule_BashPatterns" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): enforce Bash word-boundary semantics"
```

---

## Task 14: Bash matching — compound commands and wrappers (integration)

**Files:**
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluateBash_CompoundAndWrappers(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cases := []struct {
		rule, cmd string
		want      bool
	}{
		// Compound: every subcommand must match the rule.
		{"Bash(safe-cmd *)", "safe-cmd foo && safe-cmd bar", true},
		{"Bash(safe-cmd *)", "safe-cmd foo && other-cmd", false},
		{"Bash(safe-cmd *)", "safe-cmd foo ; safe-cmd bar", true},
		// Quoted operator does not split.
		{`Bash(echo "a && b")`, `echo "a && b"`, true},
		// Wrappers strip.
		{"Bash(npm test *)", "timeout 30 npm test bar", true},
		{"Bash(npm test *)", "time npm test bar", true},
		// xargs with flags is NOT stripped.
		{"Bash(npm test *)", "xargs -n1 npm test", false},
		// Bare xargs is stripped.
		{"Bash(grep *)", "xargs grep foo", true},
		// Unknown wrapper not stripped.
		{"Bash(npm test *)", "docker exec foo npm test", false},
	}
	for _, c := range cases {
		t.Run(c.rule+"|"+c.cmd, func(t *testing.T) {
			r, err := ParseRule(c.rule)
			if err != nil {
				t.Fatalf("ParseRule(%q) error: %v", c.rule, err)
			}
			in := map[string]any{"command": c.cmd}
			got := r.Matches("Bash", in, env)
			if got != c.want {
				t.Errorf("rule %q vs cmd %q = %v, want %v", c.rule, c.cmd, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluateBash_CompoundAndWrappers -count=1`
Expected: PASS — splitCompound + stripBashWrappers are already implemented (tasks 5 and 6) and `bashRule.Matches` already integrates them. If this test fails, it's a regression in either of those helpers — fix the helper, not the test.

- [ ] **Step 3: Commit (verification-only task)**

```bash
git add agent/permissions_test.go
git commit -m "test(permissions): pin Bash compound + wrapper integration"
```

---

## Task 15: NewPermissionMatcher — happy path

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestNewPermissionMatcher_Happy(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cfg := PermissionsConfig{
		Allow:       []string{"Bash(npm run *)", "Read(./.env)"},
		Deny:        []string{"Bash(git push *)"},
		DefaultMode: "default",
	}
	m, err := NewPermissionMatcher(cfg, env)
	if err != nil {
		t.Fatalf("NewPermissionMatcher error: %v", err)
	}
	if m == nil {
		t.Fatal("matcher is nil")
	}
	if m.mode != PermissionModeDefault {
		t.Errorf("mode = %q, want %q", m.mode, PermissionModeDefault)
	}
	if len(m.allow) != 2 || len(m.deny) != 1 || len(m.ask) != 0 {
		t.Errorf("allow=%d deny=%d ask=%d, want 2/1/0", len(m.allow), len(m.deny), len(m.ask))
	}
}

func TestNewPermissionMatcher_EmptyModeBecomesDefault(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	m, err := NewPermissionMatcher(PermissionsConfig{DefaultMode: ""}, env)
	if err != nil {
		t.Fatalf("NewPermissionMatcher error: %v", err)
	}
	if m.mode != PermissionModeDefault {
		t.Errorf("mode = %q, want %q (empty → default)", m.mode, PermissionModeDefault)
	}
}

func TestNewPermissionMatcher_ParseError(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())
	cfg := PermissionsConfig{Allow: []string{"Bash(rm"}}
	_, err := NewPermissionMatcher(cfg, env)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), `permission rule "Bash(rm"`) {
		t.Errorf("error = %v, want substring with rule source", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestNewPermissionMatcher -count=1`
Expected: FAIL — stub returns "not implemented".

- [ ] **Step 3: Write minimal implementation**

Replace `NewPermissionMatcher` in `agent/permissions.go` with:

```go
func NewPermissionMatcher(cfg PermissionsConfig, env ExecutionEnvironment) (*PermissionMatcher, error) {
	m := &PermissionMatcher{env: env}

	switch PermissionMode(cfg.DefaultMode) {
	case "", PermissionModeDefault:
		m.mode = PermissionModeDefault
	case PermissionModeAcceptEdits,
		PermissionModePlan,
		PermissionModeAuto,
		PermissionModeDontAsk,
		PermissionModeBypassPermissions:
		m.mode = PermissionMode(cfg.DefaultMode)
	default:
		return nil, fmt.Errorf("unknown permissions.defaultMode %q", cfg.DefaultMode)
	}

	for _, raw := range cfg.Deny {
		rule, err := ParseRule(raw)
		if err != nil {
			return nil, err
		}
		m.deny = append(m.deny, parsedRule{rule: rule, source: raw})
	}
	for _, raw := range cfg.Allow {
		rule, err := ParseRule(raw)
		if err != nil {
			return nil, err
		}
		m.allow = append(m.allow, parsedRule{rule: rule, source: raw})
	}
	// Future: SP1 may grow a permissions.ask list. For v1, ask rules are
	// authored by users only via the CC /permissions slash command which SP2
	// does not implement yet. ask stays empty here; see §11 deferral 10.

	return m, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestNewPermissionMatcher -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): NewPermissionMatcher with mode validation"
```

---

## Task 16: Evaluate — list precedence (deny → ask → allow)

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluate_Precedence(t *testing.T) {
	env := NewLocalExecutionEnvironment(t.TempDir())

	t.Run("deny beats allow", func(t *testing.T) {
		m, err := NewPermissionMatcher(PermissionsConfig{
			Deny:        []string{"Bash(rm *)"},
			Allow:       []string{"Bash"},
			DefaultMode: "default",
		}, env)
		if err != nil {
			t.Fatal(err)
		}
		d := m.Evaluate("Bash", map[string]any{"command": "rm -rf foo"})
		if d.Outcome != PermissionDeny {
			t.Errorf("Outcome = %q, want %q", d.Outcome, PermissionDeny)
		}
		if d.Rule != "Bash(rm *)" {
			t.Errorf("Rule = %q, want %q", d.Rule, "Bash(rm *)")
		}
		if !strings.Contains(d.Reason, "Bash(rm *)") {
			t.Errorf("Reason = %q, want it to mention the deny rule", d.Reason)
		}
	})

	t.Run("first matching rule wins inside list", func(t *testing.T) {
		m, err := NewPermissionMatcher(PermissionsConfig{
			Allow:       []string{"Bash(npm *)", "Bash"},
			DefaultMode: "default",
		}, env)
		if err != nil {
			t.Fatal(err)
		}
		d := m.Evaluate("Bash", map[string]any{"command": "npm test"})
		if d.Outcome != PermissionAllow {
			t.Errorf("Outcome = %q, want %q", d.Outcome, PermissionAllow)
		}
		if d.Rule != "Bash(npm *)" {
			t.Errorf("Rule = %q, want %q (first match)", d.Rule, "Bash(npm *)")
		}
	})

	t.Run("no rule, default mode falls back to ask", func(t *testing.T) {
		m, err := NewPermissionMatcher(PermissionsConfig{
			Allow:       []string{"Bash(npm *)"},
			DefaultMode: "default",
		}, env)
		if err != nil {
			t.Fatal(err)
		}
		d := m.Evaluate("Bash", map[string]any{"command": "git status"})
		if d.Outcome != PermissionAsk {
			t.Errorf("Outcome = %q, want %q", d.Outcome, PermissionAsk)
		}
		if d.Rule != "" {
			t.Errorf("Rule = %q, want \"\" (no rule)", d.Rule)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluate_Precedence -count=1`
Expected: FAIL — `Evaluate` stub returns zero value.

- [ ] **Step 3: Write minimal implementation**

Replace `Evaluate` in `agent/permissions.go` with:

```go
func (m *PermissionMatcher) Evaluate(toolName string, toolInput map[string]any) PermissionDecision {
	if m == nil {
		// Nil matcher means permissions are disabled at the surface (test
		// shortcut). Treat every call as allow.
		return PermissionDecision{Outcome: PermissionAllow}
	}

	// Step 1: mode shortcuts handled in task 17.

	// Step 3: deny rules.
	for _, pr := range m.deny {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionDeny,
				Rule:    pr.source,
				Reason:  "denied by " + pr.source,
			}
		}
	}
	// Step 4: ask rules.
	for _, pr := range m.ask {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionAsk,
				Rule:    pr.source,
				Reason:  "ask required by " + pr.source,
			}
		}
	}
	// Step 5: allow rules.
	for _, pr := range m.allow {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionAllow,
				Rule:    pr.source,
			}
		}
	}

	// Step 6: defaultMode (task 17 fills the non-"default" cases).
	switch m.mode {
	default:
		return PermissionDecision{Outcome: PermissionAsk, Reason: "no rule matched"}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestEvaluate_Precedence -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): Evaluate with deny→ask→allow precedence"
```

---

## Task 17: Evaluate — defaultMode dispatch

**Files:**
- Modify: `agent/permissions.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluate_DefaultMode(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "x"), "")
	env := NewLocalExecutionEnvironment(root)

	cases := []struct {
		mode     PermissionMode
		toolName string
		in       map[string]any
		want     PermissionOutcome
	}{
		{PermissionModeDefault, "Bash", map[string]any{"command": "ls"}, PermissionAsk},

		// acceptEdits: Edit-class calls inside cwd allow; outside fall through to ask.
		{PermissionModeAcceptEdits, "Edit", map[string]any{"file_path": filepath.Join(root, "x")}, PermissionAllow},
		{PermissionModeAcceptEdits, "Edit", map[string]any{"file_path": "/tmp/x"}, PermissionAsk},
		{PermissionModeAcceptEdits, "Bash", map[string]any{"command": "ls"}, PermissionAsk},

		// plan: mutations deny outright; reads allow outright; everything else asks.
		{PermissionModePlan, "Bash", map[string]any{"command": "ls"}, PermissionAsk},
		{PermissionModePlan, "Edit", map[string]any{"file_path": "x"}, PermissionDeny},
		{PermissionModePlan, "Write", map[string]any{"file_path": "x"}, PermissionDeny},
		{PermissionModePlan, "NotebookEdit", map[string]any{"file_path": "x"}, PermissionDeny},
		{PermissionModePlan, "Read", map[string]any{"file_path": "x"}, PermissionAllow},

		// auto: v1 collapses to ask.
		{PermissionModeAuto, "Bash", map[string]any{"command": "ls"}, PermissionAsk},

		// dontAsk: every unmatched call denies.
		{PermissionModeDontAsk, "Bash", map[string]any{"command": "ls"}, PermissionDeny},

		// bypassPermissions: every call allows.
		{PermissionModeBypassPermissions, "Bash", map[string]any{"command": "rm -rf /"}, PermissionAllow},
	}
	for _, c := range cases {
		t.Run(string(c.mode)+"/"+c.toolName, func(t *testing.T) {
			m, err := NewPermissionMatcher(PermissionsConfig{DefaultMode: string(c.mode)}, env)
			if err != nil {
				t.Fatal(err)
			}
			d := m.Evaluate(c.toolName, c.in)
			if d.Outcome != c.want {
				t.Errorf("mode=%s tool=%s in=%v → %q, want %q (reason=%q)", c.mode, c.toolName, c.in, d.Outcome, c.want, d.Reason)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluate_DefaultMode -count=1`
Expected: FAIL — only `default` is implemented; others return ask.

- [ ] **Step 3: Write minimal implementation**

Replace `Evaluate` in `agent/permissions.go` with:

```go
func (m *PermissionMatcher) Evaluate(toolName string, toolInput map[string]any) PermissionDecision {
	if m == nil {
		return PermissionDecision{Outcome: PermissionAllow}
	}

	// Step 1: bypassPermissions short-circuits everything to allow.
	// (The CC-documented rm -rf / circuit-breaker is a deferral; see §11.)
	if m.mode == PermissionModeBypassPermissions {
		return PermissionDecision{Outcome: PermissionAllow, Reason: "bypassPermissions mode"}
	}

	// Step 2: plan mode forbids file mutations before any rule pipeline.
	if m.mode == PermissionModePlan && isMutationTool(toolName) {
		return PermissionDecision{
			Outcome: PermissionDeny,
			Reason:  "plan mode forbids file mutation",
		}
	}

	for _, pr := range m.deny {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionDeny,
				Rule:    pr.source,
				Reason:  "denied by " + pr.source,
			}
		}
	}
	for _, pr := range m.ask {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionAsk,
				Rule:    pr.source,
				Reason:  "ask required by " + pr.source,
			}
		}
	}
	for _, pr := range m.allow {
		if pr.rule.Matches(toolName, toolInput, m.env) {
			return PermissionDecision{
				Outcome: PermissionAllow,
				Rule:    pr.source,
			}
		}
	}

	return m.applyDefaultMode(toolName, toolInput)
}

// applyDefaultMode is the no-rule-matched fallback.
func (m *PermissionMatcher) applyDefaultMode(toolName string, in map[string]any) PermissionDecision {
	switch m.mode {
	case PermissionModeAcceptEdits:
		if isMutationTool(toolName) && pathUnderWorkingDir(in, m.env) {
			return PermissionDecision{Outcome: PermissionAllow, Reason: "acceptEdits mode"}
		}
		return PermissionDecision{Outcome: PermissionAsk, Reason: "no rule matched"}
	case PermissionModePlan:
		if isReadOnlyTool(toolName) {
			return PermissionDecision{Outcome: PermissionAllow, Reason: "plan mode allows reads"}
		}
		return PermissionDecision{Outcome: PermissionAsk, Reason: "no rule matched"}
	case PermissionModeAuto:
		// v1 collapses to "default" — the CC classifier is not yet ported.
		return PermissionDecision{Outcome: PermissionAsk, Reason: "auto mode falls back to ask"}
	case PermissionModeDontAsk:
		return PermissionDecision{Outcome: PermissionDeny, Reason: "dontAsk mode denies unmatched calls"}
	default:
		return PermissionDecision{Outcome: PermissionAsk, Reason: "no rule matched"}
	}
}

func isMutationTool(toolName string) bool {
	switch toolName {
	case "Edit", "Write", "NotebookEdit":
		return true
	}
	return false
}

func isReadOnlyTool(toolName string) bool {
	switch toolName {
	case "Read", "Grep", "Glob", "WebFetch", "WebSearch":
		return true
	}
	return false
}

// pathUnderWorkingDir returns true when toolInput names a file inside the
// session's working directory. Used by acceptEdits.
func pathUnderWorkingDir(in map[string]any, env ExecutionEnvironment) bool {
	target, _ := in["file_path"].(string)
	if target == "" {
		return false
	}
	abs := target
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(env.WorkingDirectory(), abs)
	}
	rel, err := filepath.Rel(env.WorkingDirectory(), filepath.Clean(abs))
	if err != nil || strings.HasPrefix(rel, "..") {
		return false
	}
	return true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestEvaluate_DefaultMode|TestEvaluate_Precedence" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions.go agent/permissions_test.go
git commit -m "feat(permissions): defaultMode dispatch for all six modes"
```

---

## Task 18: Worked-examples table from the SP2 spec (§5)

**Files:**
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestEvaluate_SpecWorkedExamples(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "src"))
	mustWrite(t, filepath.Join(root, "src", "foo.go"), "")
	mustWrite(t, filepath.Join(root, ".env"), "")
	mustWrite(t, filepath.Join(root, "src", "main.go"), "")
	env := NewLocalExecutionEnvironment(root)

	type call struct {
		toolName string
		in       map[string]any
	}
	cases := []struct {
		name string
		cfg  PermissionsConfig
		call call
		want PermissionOutcome
		rule string
	}{
		{
			name: "npm run allowed",
			cfg:  PermissionsConfig{Allow: []string{"Bash(npm run *)"}, Deny: []string{"Bash(git push *)"}, DefaultMode: "default"},
			call: call{"Bash", map[string]any{"command": "npm run test"}},
			want: PermissionAllow,
			rule: "Bash(npm run *)",
		},
		{
			name: "git push denied",
			cfg:  PermissionsConfig{Allow: []string{"Bash(npm run *)"}, Deny: []string{"Bash(git push *)"}, DefaultMode: "default"},
			call: call{"Bash", map[string]any{"command": "git push origin main"}},
			want: PermissionDeny,
			rule: "Bash(git push *)",
		},
		{
			name: "git status unmatched → ask",
			cfg:  PermissionsConfig{Allow: []string{"Bash(npm run *)"}, Deny: []string{"Bash(git push *)"}, DefaultMode: "default"},
			call: call{"Bash", map[string]any{"command": "git status"}},
			want: PermissionAsk,
		},
		{
			name: "read deny beats allow",
			cfg:  PermissionsConfig{Deny: []string{"Read(./.env)"}, Allow: []string{"Read(./**)"}, DefaultMode: "default"},
			call: call{"Read", map[string]any{"file_path": ".env"}},
			want: PermissionDeny,
			rule: "Read(./.env)",
		},
		{
			name: "read allow matches",
			cfg:  PermissionsConfig{Deny: []string{"Read(./.env)"}, Allow: []string{"Read(./**)"}, DefaultMode: "default"},
			call: call{"Read", map[string]any{"file_path": "src/main.go"}},
			want: PermissionAllow,
			rule: "Read(./**)",
		},
		{
			name: "webfetch allowed",
			cfg:  PermissionsConfig{Allow: []string{"WebFetch(domain:github.com)"}, DefaultMode: "default"},
			call: call{"WebFetch", map[string]any{"url": "https://github.com/x"}},
			want: PermissionAllow,
			rule: "WebFetch(domain:github.com)",
		},
		{
			name: "webfetch other domain → ask",
			cfg:  PermissionsConfig{Allow: []string{"WebFetch(domain:github.com)"}, DefaultMode: "default"},
			call: call{"WebFetch", map[string]any{"url": "https://gitlab.com/x"}},
			want: PermissionAsk,
		},
		{
			name: "mcp server-prefix allows any tool on that server",
			cfg:  PermissionsConfig{Allow: []string{"mcp__puppeteer"}, DefaultMode: "default"},
			call: call{"mcp__puppeteer__navigate", nil},
			want: PermissionAllow,
			rule: "mcp__puppeteer",
		},
		{
			name: "mcp exact tool does not cover sibling tool",
			cfg:  PermissionsConfig{Allow: []string{"mcp__puppeteer__navigate"}, DefaultMode: "default"},
			call: call{"mcp__puppeteer__click", nil},
			want: PermissionAsk,
		},
		{
			name: "agent deny",
			cfg:  PermissionsConfig{Deny: []string{"Agent(Explore)"}, DefaultMode: "default"},
			call: call{"Task", map[string]any{"subagent_type": "Explore"}},
			want: PermissionDeny,
			rule: "Agent(Explore)",
		},
		{
			name: "plan mode denies Edit even with allow rule",
			cfg:  PermissionsConfig{Allow: []string{"Edit(*)"}, DefaultMode: "plan"},
			call: call{"Edit", map[string]any{"file_path": "src/foo.go"}},
			want: PermissionDeny,
		},
		{
			name: "acceptEdits short-circuit",
			cfg:  PermissionsConfig{DefaultMode: "acceptEdits"},
			call: call{"Edit", map[string]any{"file_path": filepath.Join(root, "src", "foo.go")}},
			want: PermissionAllow,
		},
		{
			name: "bypassPermissions allows everything",
			cfg:  PermissionsConfig{DefaultMode: "bypassPermissions"},
			call: call{"Bash", map[string]any{"command": "rm -rf /tmp/foo"}},
			want: PermissionAllow,
		},
		{
			name: "dontAsk denies unmatched",
			cfg:  PermissionsConfig{DefaultMode: "dontAsk"},
			call: call{"Bash", map[string]any{"command": "ls"}},
			want: PermissionDeny,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := NewPermissionMatcher(c.cfg, env)
			if err != nil {
				t.Fatalf("NewPermissionMatcher: %v", err)
			}
			d := m.Evaluate(c.call.toolName, c.call.in)
			if d.Outcome != c.want {
				t.Errorf("Outcome = %q, want %q (reason=%q rule=%q)", d.Outcome, c.want, d.Reason, d.Rule)
			}
			if c.rule != "" && d.Rule != c.rule {
				t.Errorf("Rule = %q, want %q", d.Rule, c.rule)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestEvaluate_SpecWorkedExamples -count=1`
Expected: PASS — all branches were implemented in earlier tasks. If a row fails, that's a real regression (or an unimplemented edge case); fix at the implementation site, never the test.

- [ ] **Step 3: Commit**

```bash
git add agent/permissions_test.go
git commit -m "test(permissions): pin SP2 spec §5 worked examples"
```

---

## Task 19: CC-docs example fixture (meta-test)

**Files:**
- Create: `agent/testdata/permissions/cc-docs-examples.json`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_CCDocsExamples(t *testing.T) {
	path := filepath.Join("testdata", "permissions", "cc-docs-examples.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fix struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
		Ask   []string `json:"ask"`
	}
	if err := json.Unmarshal(data, &fix); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	env := NewLocalExecutionEnvironment(t.TempDir())
	cfg := PermissionsConfig{Allow: fix.Allow, Deny: fix.Deny, DefaultMode: "default"}
	if _, err := NewPermissionMatcher(cfg, env); err != nil {
		t.Fatalf("NewPermissionMatcher on cc-docs fixture: %v", err)
	}
}
```

Add `"encoding/json"` to test imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestParseRule_CCDocsExamples -count=1`
Expected: FAIL — fixture file does not exist.

- [ ] **Step 3: Create the fixture**

Create `agent/testdata/permissions/cc-docs-examples.json` with every example string lifted verbatim from code.claude.com/docs/en/permissions:

```json
{
  "allow": [
    "Bash",
    "Bash(*)",
    "Bash(npm run build)",
    "Bash(npm run *)",
    "Bash(ls*)",
    "Bash(* install)",
    "Bash(git * main)",
    "Bash(ls:*)",
    "Read",
    "Read(./.env)",
    "Read(~/.zshrc)",
    "Read(//tmp/scratch.txt)",
    "Read(/docs/**)",
    "Edit(src/**/*.ts)",
    "Write(src/foo.go)",
    "WebFetch",
    "WebFetch(domain:example.com)",
    "WebFetch(domain:*.example.com)",
    "Skill",
    "Skill(*)",
    "Skill(my-skill)",
    "Agent(Explore)",
    "Agent(Plan)",
    "Agent(my-custom)",
    "mcp__puppeteer",
    "mcp__puppeteer__*",
    "mcp__puppeteer__puppeteer_navigate"
  ],
  "deny": [
    "Bash(rm -rf /)",
    "Bash(git push *)",
    "Read(./.env)",
    "WebFetch(domain:evil.example.com)"
  ],
  "ask": []
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseRule_CCDocsExamples -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/permissions_test.go agent/testdata/permissions/cc-docs-examples.json
git commit -m "test(permissions): pin CC docs example strings as a fixture"
```

---

## Task 20: Add SessionConfig.Permissions and PermissionAskFallback fields

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestSessionConfig_HasPermissionsFields(t *testing.T) {
	// Compile-only test that pins the SessionConfig wiring SP8 will rely on.
	var cfg SessionConfig
	cfg.Permissions = PermissionsConfig{}
	cfg.PermissionAskFallback = AskFallbackDeny
	_ = cfg
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSessionConfig_HasPermissionsFields -count=1`
Expected: FAIL — `Permissions` and `PermissionAskFallback` fields do not exist.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, add fields to `SessionConfig` (right after `DeniedToolNames` at line 189):

```go
	// Permissions is the merged Claude Code-style permissions block.
	// Source: SP1 → SP8 wires it from DiscoverSerfConfig at session bootstrap.
	Permissions PermissionsConfig `json:"-"`

	// PermissionAskFallback is the policy each entry-point selects for
	// surfaces that have no human (serf -p, serfeval). Defaults to
	// AskFallbackInteractive on TTY surfaces. SP8 owns the surface defaults.
	PermissionAskFallback AskFallback `json:"-"`
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestSessionConfig_HasPermissionsFields -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/permissions_test.go
git commit -m "feat(permissions): SessionConfig.Permissions + PermissionAskFallback"
```

---

## Task 21: Add Session.permissionMatcher and build it in NewSession

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestNewSession_BuildsPermissionMatcher(t *testing.T) {
	c, _ := newTestLLMClient(t) // reuse existing test helper that supplies a stub llm.Client
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{
			Allow:       []string{"Bash(npm *)"},
			Deny:        []string{"Bash(rm *)"},
			DefaultMode: "default",
		},
	})
	if err != nil {
		t.Fatalf("NewSession error: %v", err)
	}
	if sess.permissionMatcher == nil {
		t.Fatal("expected non-nil permissionMatcher")
	}
}

func TestNewSession_PermissionsParseErrorAborts(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	_, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{Allow: []string{"Bash(rm"}},
	})
	if err == nil {
		t.Fatal("expected error for malformed rule")
	}
	if !strings.Contains(err.Error(), "Bash(rm") {
		t.Errorf("error = %v, want rule source", err)
	}
}
```

The helper `newTestLLMClient` already exists (used elsewhere in this package — see `agent/context_strategy_test.go`). If it does not, copy its definition from the file that uses it (do not write a fresh stub).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run "TestNewSession_BuildsPermissionMatcher|TestNewSession_PermissionsParseErrorAborts" -count=1`
Expected: FAIL — `permissionMatcher` field does not exist; `NewSession` does not build it.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, add the field to `Session` (next to `reg *ToolRegistry`):

```go
	permissionMatcher *PermissionMatcher
```

In `NewSession` (find the function — search `func NewSession`), after `cfg.applyDefaults()` and after `env` is locked in, but before any return path that creates the Session struct, add:

```go
	matcher, err := NewPermissionMatcher(cfg.Permissions, env)
	if err != nil {
		return nil, fmt.Errorf("session permissions: %w", err)
	}
```

Then assign `permissionMatcher: matcher,` in the `Session{}` literal initialization.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run "TestNewSession_BuildsPermissionMatcher|TestNewSession_PermissionsParseErrorAborts" -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/permissions_test.go
git commit -m "feat(permissions): Session builds matcher in NewSession"
```

---

## Task 22: permissionDeniedResult helper

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestPermissionDeniedResult_Shape(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"rm -rf /"}`)}
	res := sess.permissionDeniedResult(call, PermissionDecision{
		Outcome: PermissionDeny,
		Rule:    "Bash(rm *)",
		Reason:  "denied by Bash(rm *)",
	})
	if !res.IsError {
		t.Error("IsError should be true")
	}
	if !strings.Contains(res.Output, "Bash(rm *)") {
		t.Errorf("Output should mention rule; got %q", res.Output)
	}
	if res.ToolName != "shell" || res.CallID != "c1" {
		t.Errorf("ToolName/CallID mismatch: %+v", res)
	}
}
```

Add `"primeradiant.com/serf/llm"` to test imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestPermissionDeniedResult_Shape -count=1`
Expected: FAIL — `permissionDeniedResult` does not exist.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, add (place near `execTool`):

```go
// permissionDeniedResult shapes a ToolExecResult that mirrors the PreToolUse
// hook-deny path so the model sees a uniform "tool denied" surface regardless
// of which layer denied.
func (s *Session) permissionDeniedResult(call llm.ToolCallData, d PermissionDecision) ToolExecResult {
	msg := d.Reason
	if msg == "" {
		msg = "Tool call denied by permissions"
	}
	return ToolExecResult{
		ToolName:   call.Name,
		CallID:     call.ID,
		Output:     msg,
		FullOutput: msg,
		IsError:    true,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestPermissionDeniedResult_Shape -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/permissions_test.go
git commit -m "feat(permissions): permissionDeniedResult helper"
```

---

## Task 23: resolveAsk helper — surface-fallback only (no hooks yet)

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/permissions_test.go`

`PermissionRequest`/`PermissionDenied` hooks are owned by SP5 (see SP2 §7). In SP2 we provide the integration seam by collapsing `ask` through `PermissionAskFallback` and leave the SP5 hook callout as a TODO comment that SP5 fills in.

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestResolveAsk_FallbackBranches(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	cases := []struct {
		fallback AskFallback
		want     PermissionOutcome
	}{
		{AskFallbackDeny, PermissionDeny},
		{AskFallbackAllow, PermissionAllow},
	}
	for _, c2 := range cases {
		t.Run(fmt.Sprintf("%v", c2.fallback), func(t *testing.T) {
			sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
				PermissionAskFallback: c2.fallback,
			})
			if err != nil {
				t.Fatal(err)
			}
			d := sess.resolveAsk(context.Background(),
				llm.ToolCallData{Name: "shell"},
				PermissionDecision{Outcome: PermissionAsk, Reason: "test"})
			if d.Outcome != c2.want {
				t.Errorf("Outcome = %q, want %q", d.Outcome, c2.want)
			}
		})
	}
}
```

Add `"context"` and `"fmt"` to the test imports if missing.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestResolveAsk_FallbackBranches -count=1`
Expected: FAIL — `resolveAsk` does not exist.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, add:

```go
// resolveAsk turns an "ask" PermissionDecision into a concrete allow/deny
// outcome. SP5 will hook this method to fire PermissionRequest/PermissionDenied
// hooks before consulting the surface fallback.
func (s *Session) resolveAsk(ctx context.Context, call llm.ToolCallData, d PermissionDecision) PermissionDecision {
	// SP5 TODO: fire PermissionRequest hook here. A hook returning
	// "allow"/"deny" short-circuits; "defer" falls through to the surface.
	switch s.cfg.PermissionAskFallback {
	case AskFallbackAllow:
		return PermissionDecision{Outcome: PermissionAllow, Reason: d.Reason}
	case AskFallbackDeny:
		return PermissionDecision{Outcome: PermissionDeny, Reason: d.Reason, Rule: d.Rule}
	case AskFallbackInteractive:
		// v1: interactive prompting is owned by the surface (CLI/TUI/Hub).
		// SP8 wires the surface; SP2's contract for AskFallbackInteractive
		// from a non-surface caller is "deny". Documented in §11.1.
		return PermissionDecision{Outcome: PermissionDeny, Reason: "interactive prompt unavailable", Rule: d.Rule}
	}
	return d
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestResolveAsk_FallbackBranches -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/permissions_test.go
git commit -m "feat(permissions): resolveAsk via PermissionAskFallback"
```

---

## Task 24: Wire matcher into Session.execTool

**Files:**
- Modify: `agent/session.go`
- Modify: `agent/permissions_test.go`

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`. This is the §10.7 integration test from the spec, reduced to the cases SP2 owns.

```go
func TestSession_ExecTool_PermissionDenyShortCircuits(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{
			Deny:        []string{"Bash(rm *)"},
			DefaultMode: "default",
		},
		PermissionAskFallback: AskFallbackDeny,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Track whether the shell tool actually ran.
	var ran bool
	sess.reg.MustOverride("shell", func(ctx context.Context, env ExecutionEnvironment, args map[string]any) ToolExecResult {
		ran = true
		return ToolExecResult{ToolName: "shell", Output: "ok"}
	})

	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"rm -rf foo"}`)}
	res := sess.execTool(context.Background(), call)

	if ran {
		t.Error("shell tool should not have executed")
	}
	if !res.IsError {
		t.Error("expected IsError on deny")
	}
	if !strings.Contains(res.Output, "Bash(rm *)") {
		t.Errorf("Output should mention denying rule; got %q", res.Output)
	}
}

func TestSession_ExecTool_NoRuleDefaultDeniesUnderFallbackDeny(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		PermissionAskFallback: AskFallbackDeny,
		// DefaultMode is "" → matcher uses "default" → no rule match → ask.
	})
	if err != nil {
		t.Fatal(err)
	}

	var ran bool
	sess.reg.MustOverride("shell", func(ctx context.Context, env ExecutionEnvironment, args map[string]any) ToolExecResult {
		ran = true
		return ToolExecResult{ToolName: "shell"}
	})

	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"ls"}`)}
	res := sess.execTool(context.Background(), call)
	if ran {
		t.Error("shell tool should not have executed")
	}
	if !res.IsError {
		t.Error("expected IsError on ask→deny fallback")
	}
}

func TestSession_ExecTool_BypassPermissionsAllowsEverything(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{DefaultMode: "bypassPermissions"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var ran bool
	sess.reg.MustOverride("shell", func(ctx context.Context, env ExecutionEnvironment, args map[string]any) ToolExecResult {
		ran = true
		return ToolExecResult{ToolName: "shell", Output: "ok"}
	})

	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"rm -rf /tmp/foo"}`)}
	_ = sess.execTool(context.Background(), call)
	if !ran {
		t.Error("shell tool should have executed under bypassPermissions")
	}
}
```

If `ToolRegistry.MustOverride` does not exist, prefer to use the existing override helpers — search `agent/tool_registry.go` for `Register` / `Replace` and adapt to whatever method the tests in `agent/session_communicate_test.go` use to swap a tool implementation for testing. Do not invent a method; copy the pattern in use.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./agent/ -run TestSession_ExecTool_Permission -count=1`
Expected: FAIL — `execTool` does not consult `permissionMatcher`.

- [ ] **Step 3: Write minimal implementation**

In `agent/session.go`, modify `execTool` (currently at line 1275). After the existing PreToolUse-hook block (around line 1301) and **before** `argsJSON, _ := json.Marshal(call.Arguments)`, insert:

```go
	// SP2: permission rules. Runs after PreToolUse so a hook returning
	// updatedInput is seen by the matcher; runs before execution so a deny
	// short-circuits without invoking the tool registry.
	if s.permissionMatcher != nil {
		claudeName := MapSerfToolNameToClaude(call.Name)
		var toolInput map[string]any
		if len(call.Arguments) > 0 {
			_ = json.Unmarshal(call.Arguments, &toolInput)
		}
		decision := s.permissionMatcher.Evaluate(claudeName, toolInput)
		switch decision.Outcome {
		case PermissionDeny:
			// SP5 TODO: fire PermissionDenied hook here.
			return s.permissionDeniedResult(call, decision)
		case PermissionAsk:
			decision = s.resolveAsk(ctx, call, decision)
			if decision.Outcome == PermissionDeny {
				return s.permissionDeniedResult(call, decision)
			}
		case PermissionAllow:
			// fall through to execution.
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./agent/ -run TestSession_ExecTool_Permission -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add agent/session.go agent/permissions_test.go
git commit -m "feat(permissions): wire matcher into Session.execTool"
```

---

## Task 25: Matcher sees PreToolUse hook's updatedInput

**Files:**
- Modify: `agent/permissions_test.go`

The §10.7 spec table case 5 says: a PreToolUse hook that rewrites the command (via `updatedInput`) must be observed by the matcher. This task pins that behavior end-to-end. If the test reveals that the existing hook-rewrite path does not mutate `call.Arguments` in place, the integration code in Task 24 needs to read from the mutated source. SP5 hardens the hook rewrite; SP2 only asserts that whatever shape `call.Arguments` has when the matcher runs is what the matcher consumes.

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestSession_ExecTool_MatcherSeesUpdatedInput(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{
			Deny:        []string{"Bash(forbidden *)"},
			DefaultMode: "default",
		},
		PermissionAskFallback: AskFallbackAllow,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Install a PreToolUse hook that rewrites the command to "forbidden ...".
	// SP5 owns the hook plumbing; this test simulates it by directly mutating
	// the call.Arguments before execTool consumes it. When SP5 lands, replace
	// this with the real hookSpecificOutput.updatedInput path.
	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"forbidden cmd"}`)}

	res := sess.execTool(context.Background(), call)
	if !res.IsError {
		t.Error("expected deny because the matcher saw the rewritten command")
	}
}
```

This test is intentionally lightweight — it pins the contract that the matcher reads `call.Arguments` and ParameterizedInput (`toolInput`) shape, not that PreToolUse rewriting is wired. The SP5 plan owns the rewrite test (§7 of SP2 names PreToolUse as the upstream).

- [ ] **Step 2: Run test to verify it fails or passes (verification-only)**

Run: `go test ./agent/ -run TestSession_ExecTool_MatcherSeesUpdatedInput -count=1`
Expected: PASS — the matcher does already read from `call.Arguments` (per Task 24). If FAIL, fix the integration code; do not loosen the test.

- [ ] **Step 3: Commit**

```bash
git add agent/permissions_test.go
git commit -m "test(permissions): matcher consumes call.Arguments verbatim"
```

---

## Task 26: PreToolUse hook deny short-circuits before matcher

**Files:**
- Modify: `agent/permissions_test.go`

The §10.7 spec table case 6 says: if PreToolUse returns `denied=true`, the matcher must not be consulted. The existing hook code already returns early on hook-deny; this task simply pins that the matcher does not need to run on the deny path. We assert by configuring a matcher whose deny rule would otherwise be hit, and verifying the surface still reports the hook's deny message, not the matcher's.

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestSession_ExecTool_HookDenyPrecedesMatcher(t *testing.T) {
	c, _ := newTestLLMClient(t)
	dir := t.TempDir()
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), NewLocalExecutionEnvironment(dir), SessionConfig{
		Permissions: PermissionsConfig{
			Deny:        []string{"Bash(rm *)"},
			DefaultMode: "default",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Register a PreToolUse hook that denies with a distinctive message.
	// Use whatever the existing hook-test helpers expose. If no helper exists,
	// install a HookRunner with a hard-coded result. (See agent/plugin_hooks_test.go
	// for the lightweight pattern.)
	sess.hookRunner = newDenyingHookRunner(t, "hook said no")

	call := llm.ToolCallData{ID: "c1", Name: "shell", Arguments: []byte(`{"command":"rm -rf foo"}`)}
	res := sess.execTool(context.Background(), call)
	if !res.IsError {
		t.Fatal("expected deny")
	}
	if !strings.Contains(res.Output, "hook said no") {
		t.Errorf("expected hook deny message; got %q", res.Output)
	}
	if strings.Contains(res.Output, "Bash(rm *)") {
		t.Errorf("matcher should not have run; got %q", res.Output)
	}
}
```

You will need to add `newDenyingHookRunner` to the test file. Adapt the pattern from `agent/plugin_hooks_test.go` (look for "RunPreToolUse" usages in tests). The helper returns a `*HookRunner` whose `RunPreToolUse` returns `PreToolUseResult{Denied: true, DenyMessage: "hook said no"}`. If the existing `HookRunner` has unexported state that makes faking it impossible, write a `denyingHookRunner` struct that satisfies the same interface that `Session` uses for hooks and inject it via a test-only setter.

If neither approach is feasible without invasive changes, mark this task as covered by SP5 and replace the test with a structural assertion: read `agent/session.go:1275`–`1310` and verify the hook-deny `return` happens before the SP2 matcher block.

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/ -run TestSession_ExecTool_HookDenyPrecedesMatcher -count=1`
Expected: PASS (existing wiring already places PreToolUse before the SP2 block).

- [ ] **Step 3: Commit**

```bash
git add agent/permissions_test.go
git commit -m "test(permissions): hook-deny precedes matcher"
```

---

## Task 27: ParseRule large table (canonical §10.1)

**Files:**
- Modify: `agent/permissions_test.go`

Earlier tasks covered each rule family separately; this task adds one canonical table that lists every row from SP2 §10.1 in order, so regressions show up under one test name and CI surfaces a missing row immediately. Several rows already pass — this is a coverage-shape pin, not new behavior.

- [ ] **Step 1: Write the failing test**

Append to `agent/permissions_test.go`:

```go
func TestParseRule_Canonical(t *testing.T) {
	cases := []struct {
		in         string
		wantErrSub string // substring of expected error; "" means must succeed
	}{
		{"Bash", ""},
		{"Bash(*)", ""},
		{"Bash(npm run build)", ""},
		{"Bash(npm run *)", ""},
		{"Bash(ls*)", ""},
		{"Bash(* install)", ""},
		{"Bash(git * main)", ""},
		{"Bash(ls:*)", ""},
		{"Bash(git:* push)", ""},
		{"Read(./.env)", ""},
		{"Read(//tmp/scratch.txt)", ""},
		{"Read(~/.zshrc)", ""},
		{"Read(/docs/**)", ""},
		{"Edit(src/**/*.ts)", ""},
		{"Write(src/foo.go)", ""},
		{"WebFetch", ""},
		{"WebFetch(domain:example.com)", ""},
		{"WebFetch(domain:*.example.com)", ""},
		{"WebFetch(domain:**.example.com)", "** not supported"},
		{"WebFetch(port:443)", "unsupported specifier prefix"},
		{"Skill(my-skill)", ""},
		{"Skill(*)", ""},
		{"Agent(Explore)", ""},
		{"Agent(my-custom)", ""},
		{"mcp__puppeteer", ""},
		{"mcp__puppeteer__*", ""},
		{"mcp__puppeteer__click", ""},
		{"Bash(rm", "unbalanced parentheses"},
		{"Bash()", "empty specifier"},
		{"", "empty"},
		{"Unknown(thing)", ""}, // forward-compat
		{"Read(\x00foo)", "NUL byte"},
		{"PowerShell(Get-ChildItem *)", ""}, // parses; inert
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			_, err := ParseRule(c.in)
			if c.wantErrSub == "" {
				if err != nil {
					t.Fatalf("ParseRule(%q) unexpected error: %v", c.in, err)
				}
			} else {
				if err == nil {
					t.Fatalf("ParseRule(%q) expected error containing %q", c.in, c.wantErrSub)
				}
				if !strings.Contains(err.Error(), c.wantErrSub) {
					t.Errorf("ParseRule(%q) error = %v, want substring %q", c.in, err, c.wantErrSub)
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it passes**

Run: `go test ./agent/ -run TestParseRule_Canonical -count=1`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add agent/permissions_test.go
git commit -m "test(permissions): canonical ParseRule coverage table"
```

---

## Task 28: Full-suite verification

**Files:** (none modified)

- [ ] **Step 1: Run the SP2 test surface**

Run: `go test ./agent/ -run Permission -count=1 -v`
Expected: every `Permission*` test passes.

- [ ] **Step 2: Run the whole agent package for collateral damage**

Run: `go test ./agent/... -count=1`
Expected: PASS.

- [ ] **Step 3: Run the whole repo**

Run: `go test ./...`
Expected: PASS. Any unrelated failure should be investigated and fixed (Broken Windows applies).

- [ ] **Step 4: Verify the coverage gate from SP2 §10.10**

Visually confirm:
- Every exported function in §2 (`NewPermissionMatcher`, `Evaluate`, `ParseRule`, `PermissionMatcher`, `PermissionDecision`, `PermissionMode`, `PermissionOutcome`, `Rule`, `AskFallback`) is hit by at least one test.
- Every error path in §3.8 (empty, unbalanced, empty-spec, `**` host, unsupported specifier prefix, NUL byte) appears in `TestParseRule_Canonical`.
- Every `defaultMode` value appears in `TestEvaluate_DefaultMode`.
- Every Bash example from the CC docs appears verbatim in `TestEvaluateBash_WordBoundary` or `TestEvaluateBash_CompoundAndWrappers`.

- [ ] **Step 5: Commit only if anything had to change (verification often produces no diff)**

```bash
git status
# If diff is empty, skip commit. Otherwise:
git add -p
git commit -m "test(permissions): verification pass for SP2 coverage gate"
```

---

## Self-Review Notes (already incorporated)

A self-review against SP2 was run before saving. Gaps and the tasks that close them:

- **§3 rule grammar:** Tasks 2–12 cover bare, Bash, Read/Edit/Write, WebFetch, Skill, Agent, MCP, inert (PowerShell), forward-compat unknown, and every §3.8 parse-error case.
- **§4 matching algorithm:** Tasks 16 (precedence) and 17 (defaultMode dispatch).
- **§5 worked examples:** Task 18 transcribes every row.
- **§6 integration point:** Task 24 inserts the §6.1 block; Task 25 pins the "matcher sees updated input" contract; Task 26 pins the "hook deny precedes matcher" contract.
- **§7 hook interaction:** SP5 owns the hook firing. Tasks 23 (resolveAsk seam) and 24 (TODO comment in execTool) place the seams; SP5 plugs them in.
- **§8 error contracts:** Task 15 covers `NewPermissionMatcher` parse-error path; Task 21 covers `NewSession` aborting on rule parse failure.
- **§9 file layout:** Task 1 creates `agent/permissions.go` + test file. Task 19 creates `agent/testdata/permissions/cc-docs-examples.json`. Task 20 adds `SessionConfig.Permissions`/`PermissionAskFallback`. Task 21 adds `Session.permissionMatcher`.
- **§10 testing strategy:** Every numbered subsection has a corresponding task:
  - §10.1 → Tasks 27 (canonical) plus 2/3/4/7/9/10/11/12 (per-family).
  - §10.2 → Tasks 13 + 14.
  - §10.3 → Task 8.
  - §10.4 → Tasks 9, 10, 11.
  - §10.5 → Task 16.
  - §10.6 → Task 17.
  - §10.7 → Task 24.
  - §10.8 → Task 23 (ask path). The hook-driven ask path is covered structurally; SP5 adds the hook-decision branches.
  - §10.9 → Task 19.
  - §10.10 → Task 28.
- **§11 deferrals:** Documented in the test names and comments (e.g. `TestEvaluate_DefaultMode` `plan/Bash{ls}` row expects `ask` per §10.6's "v1 returns ask, test asserts that"). Each deferral surfaces in a test row or a TODO in the code.

Known gaps the plan does **not** close (per SP2's own deferrals in §11.3):

1. Symlink double-checking on Read/Edit rules.
2. Built-in read-only Bash command allowlist.
3. `acceptEdits` shortcuts for `mkdir`/`touch`/`mv`/`cp`.
4. `bypassPermissions` `rm -rf /` circuit breaker.
5. `auto` mode classifier.
6. `additionalDirectories` for `acceptEdits` scope.

These are intentional and listed in the spec; future SPs reopen them.
