# Shell cd-Prefix Display Strip Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop displaying the redundant `cd <session cwd> && ` prefix that models habitually prepend to shell commands, on both the web transcript and the TUI.

**Architecture:** A pure strip helper per surface (TS and Go), applied only at display time. Web: helper beside `shellTool.tsx`, cwd read from the threads store by session ref (the `fileOpenBeside.tsx` pattern), plus a one-field optional context added to the tool-descriptor `summary()` signature. TUI: `SummarizeToolInDir` variant in `toolsummary`; existing `SummarizeTool` delegates with empty cwd (no strip), callers that can reach `appwire.Thread.Cwd` upgrade.

**Tech Stack:** TypeScript (React, Vitest) in `cmd/serf-hub/frontend`; Go in `cmd/serf-tui`.

**Spec:** `docs/superpowers/specs/2026-08-06-shell-cd-strip-design.md` — read it first.

## Global Constraints

- Literal match only: the stripped prefix is exactly `"cd " + cwd + " && "`. No quote handling, no path normalization, no trailing-slash tolerance, no `;`/`&` variants. Empty cwd → never strip.
- Display-only: recorded arguments (`argumentsJSON`, `RawArgs`) are never modified; copy/raw affordances show the original.
- TDD; gofmt clean; `golangci-lint run` clean on touched Go packages; pristine test output.
- Frontend tests run from `cmd/serf-hub/frontend` with `npx vitest run <file>`; full stream `make test-web` from repo root.
- TUI tests: `cd cmd/serf-tui && go test ./... -count=1`.

---

### Task 1: Web — strip in shellTool summary and expanded block

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx` (helper + `ShellBody` + `summary`)
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/toolRenderers.ts` (descriptor `summary` signature, ~line 29)
- Modify: the `summary(...)` call site (find it: `grep -n "\.summary(" cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.tsx` — it is in `ToolCallItem.tsx`; pass the context there)
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx` (extend; the file exists)

**Interfaces:**
- Produces: `export function stripRedundantCd(command: string, cwd: string | undefined): string` in `shellTool.tsx`; `ToolSummaryContext = { cwd?: string }` as an optional second parameter of `ToolRendererDescriptor.summary`.
- Consumes: `useThreadsStore` cwd-by-ref lookup — copy the exact selector pattern from `fileOpenBeside.tsx` (it reads the ThreadModel by session ref; mirror it, do not invent a new store path).

- [ ] **Step 1: Write the failing tests**

Add to `shellTool.test.tsx` (match the file's existing import style):

```tsx
describe("stripRedundantCd", () => {
  const cwd = "/Users/jesse/work";
  test("strips the exact cd-cwd prefix", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && make test", cwd)).toBe("make test");
  });
  test("different directory is untouched", () => {
    expect(stripRedundantCd("cd /elsewhere && make test", cwd)).toBe("cd /elsewhere && make test");
  });
  test("quoted cwd is untouched (literal match only)", () => {
    expect(stripRedundantCd('cd "/Users/jesse/work" && make', cwd)).toBe('cd "/Users/jesse/work" && make');
  });
  test("trailing slash variant is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work/ && make", cwd)).toBe("cd /Users/jesse/work/ && make");
  });
  test("semicolon join is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work ; make", cwd)).toBe("cd /Users/jesse/work ; make");
  });
  test("cd mid-command is untouched", () => {
    expect(stripRedundantCd("make && cd /Users/jesse/work && ls", cwd)).toBe("make && cd /Users/jesse/work && ls");
  });
  test("undefined or empty cwd never strips", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && make", undefined)).toBe("cd /Users/jesse/work && make");
    expect(stripRedundantCd("cd /Users/jesse/work && make", "")).toBe("cd /Users/jesse/work && make");
  });
  test("prefix-only command (nothing after &&) is untouched", () => {
    expect(stripRedundantCd("cd /Users/jesse/work && ", cwd)).toBe("cd /Users/jesse/work && ");
  });
});
```

The last case pins a deliberate choice: stripping to an empty command would render a blank row, so a command that is *only* the prefix stays as-is. (Result-empty check, not a spec deviation — the spec's rule produces `""` here, which is undisplayable.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/shellTool.test.tsx`
Expected: FAIL — `stripRedundantCd` is not exported.

- [ ] **Step 3: Implement**

In `shellTool.tsx` (near `shellCommand`, matching the file's comment style):

```tsx
// stripRedundantCd removes the literal "cd <cwd> && " prefix models
// habitually prepend even though the daemon already runs every command in
// the session cwd. Literal match only — a cd anywhere else is information
// and stays. Display-only: argumentsJSON is never modified.
export function stripRedundantCd(command: string, cwd: string | undefined): string {
  if (cwd === undefined || cwd === "") return command;
  const prefix = `cd ${cwd} && `;
  if (!command.startsWith(prefix)) return command;
  const rest = command.slice(prefix.length);
  return rest === "" ? command : rest;
}
```

In `toolRenderers.ts`, widen the descriptor signature (one optional param; no other descriptor changes):

```ts
export interface ToolSummaryContext {
  cwd?: string; // session working directory, when the render path knows it
}
```
and `summary(item: ItemModel, ctx?: ToolSummaryContext): string;`

In `ToolCallItem.tsx` (the one `.summary(` call site): read the session cwd from the threads store using the same by-ref selector `fileOpenBeside.tsx` uses (it has `sessionRef` in scope — verify; if the component lacks it, thread it from its props the same way `ToolRenderProps.sessionRef` arrives), and pass `{ cwd }`.

In `shellTool.tsx`:
- `summary(item, ctx)`: wrap the existing command read: `const command = stripRedundantCd(shellCommand(parseArgs(item.argumentsJSON)), ctx?.cwd);`
- `ShellBody`: it already has `sessionRef`; look up cwd via the same store selector and render `<ShellCommandBlock command={stripRedundantCd(command, cwd)} />`. The `CodeBlock` copy affordance for output is untouched; if `ShellCommandBlock` has a copy affordance, pass the ORIGINAL command as its copy text (check its props; add the pass-through only if the prop already exists — no new affordances).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd cmd/serf-hub/frontend && npx vitest run src/panes/session/transcript/tools/shellTool.test.tsx && npx tsc --noEmit`
Expected: PASS, typecheck clean (the widened `summary` signature is optional-param, so other descriptors compile unchanged).

- [ ] **Step 5: Commit**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/
git commit -m "feat(web): hide redundant cd-to-cwd prefix on displayed shell commands"
```

---

### Task 2: TUI — SummarizeToolInDir

**Files:**
- Modify: `cmd/serf-tui/internal/toolsummary/tool_summary.go` (~line 23)
- Modify: `cmd/serf-tui/internal/transcript/item.go:36` and `:68` (callers)
- Modify: `cmd/serf-tui/internal/msgrender/message.go:488` (caller)
- Test: `cmd/serf-tui/internal/toolsummary/tool_summary_test.go` (extend)

**Interfaces:**
- Produces: `func SummarizeToolInDir(toolName, argsJSON, cwd string) (desc, detail string)`. Existing `SummarizeTool(toolName, argsJSON)` becomes a delegator: `return SummarizeToolInDir(toolName, argsJSON, "")` (empty cwd = never strip), so untraceable call paths keep today's behavior.

- [ ] **Step 1: Write the failing tests**

In `tool_summary_test.go`, following its existing table style:

```go
func TestSummarizeToolInDirStripsRedundantCd(t *testing.T) {
	cwd := "/Users/jesse/work"
	args := `{"command":"cd /Users/jesse/work && make test"}`
	desc, _ := SummarizeToolInDir("shell", args, cwd)
	if desc != "make test" {
		t.Fatalf("desc = %q, want %q", desc, "make test")
	}
}

func TestSummarizeToolInDirLeavesNonMatchingCd(t *testing.T) {
	cwd := "/Users/jesse/work"
	for _, command := range []string{
		"cd /elsewhere && make",
		`cd "/Users/jesse/work" && make`,
		"cd /Users/jesse/work/ && make",
		"cd /Users/jesse/work ; make",
		"make && cd /Users/jesse/work && ls",
		"cd /Users/jesse/work && ", // strip would leave nothing displayable
	} {
		argsJSON, err := json.Marshal(map[string]string{"command": command})
		if err != nil {
			t.Fatal(err)
		}
		desc, _ := SummarizeToolInDir("shell", string(argsJSON), cwd)
		if !strings.Contains(desc, "cd ") {
			t.Fatalf("command %q: cd prefix wrongly stripped, desc = %q", command, desc)
		}
	}
}

func TestSummarizeToolEmptyCwdNeverStrips(t *testing.T) {
	args := `{"command":"cd /Users/jesse/work && make"}`
	desc, _ := SummarizeTool("shell", args)
	if desc != "cd /Users/jesse/work && make" {
		t.Fatalf("desc = %q, want unstripped command", desc)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd cmd/serf-tui && go test ./internal/toolsummary/ -run TestSummarizeToolInDir -v`
Expected: FAIL to build — `undefined: SummarizeToolInDir`.

- [ ] **Step 3: Implement**

In `tool_summary.go`:

```go
// stripRedundantCd removes the literal "cd <cwd> && " prefix models
// habitually prepend even though the daemon already runs every command in
// the session cwd. Literal match only — a cd anywhere else is information
// and stays. Never strips to an empty command, and an empty cwd never
// strips (callers whose render path cannot know the cwd pass "").
func stripRedundantCd(command, cwd string) string {
	if cwd == "" {
		return command
	}
	rest, ok := strings.CutPrefix(command, "cd "+cwd+" && ")
	if !ok || rest == "" {
		return command
	}
	return rest
}
```

Rename the existing `SummarizeTool` body to `SummarizeToolInDir(toolName, argsJSON, cwd string)`; inside the `case "shell":` arm, apply `cmd = stripRedundantCd(cmd, cwd)` immediately after `cmd := str("command")` (so desc, first-line, and detail all see the stripped form). Add the delegator:

```go
// SummarizeTool summarizes without a known session cwd: the redundant-cd
// strip is disabled (empty cwd), everything else is identical.
func SummarizeTool(toolName, argsJSON string) (desc, detail string) {
	return SummarizeToolInDir(toolName, argsJSON, "")
}
```

- [ ] **Step 4: Upgrade the callers that can know the cwd**

For each of the three call sites (`internal/transcript/item.go:36`, `:68`, `internal/msgrender/message.go:488`): trace whether the enclosing call chain holds the session's working directory — the TUI receives it as `appwire.Thread.Cwd` (`appwire/types.go`, `Thread` struct) and may also have the transcript header's `working_dir`. Where a caller's chain has it, switch to `SummarizeToolInDir(..., cwd)` and thread the value down through the minimal number of signatures. Where the chain genuinely does not have it, leave `SummarizeTool` and record that decision (which site, why) in the task report — the fallback displays the full command, which is today's behavior, per spec. Do not invent a new global or package-level variable to smuggle the cwd.

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd cmd/serf-tui && go test ./... -count=1 && gofmt -l . && golangci-lint run ./internal/toolsummary/ ./internal/transcript/ ./internal/msgrender/`
Expected: all PASS, no gofmt output, 0 lint issues.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-tui/
git commit -m "feat(tui): hide redundant cd-to-cwd prefix on displayed shell commands"
```

---

## Self-review notes (already applied)

- Spec coverage: rule + both surfaces + display-only + tests all mapped; the "never strip to empty" behavior is an addition the spec's rule implies but doesn't state — both tasks pin it identically and the Task 1 step explains why.
- Type consistency: `stripRedundantCd(command, cwd)` in both languages; `ToolSummaryContext.cwd` optional; Go delegator preserves the old signature so unupgraded callers compile untouched.
- Line anchors dated 2026-08-06; re-locate by symbol if drifted.
