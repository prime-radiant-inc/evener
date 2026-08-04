# MCP Tool Arguments Rendering Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show unregistered MCP tool-call arguments as readable, copyable JSON in the existing expanded transcript body.

**Architecture:** Add a focused `MCPToolArguments` body component that consumes `ToolRenderProps`, formats valid `argumentsJSON` with `JSON.stringify(parsed, null, 2)`, and falls back to the original text when parsing fails. Attach it to `DEFAULT_DESCRIPTOR` so all unregistered MCP tools receive it, while preserving dedicated tool descriptors and existing raw output/error handling.

**Tech Stack:** React 19, TypeScript, Vitest, Testing Library, CSS modules, Biome.

## Global Constraints

- Render arguments only for the default/unregistered tool renderer; do not change dedicated tool renderers.
- Keep tool rows collapsed by default and preserve existing failure auto-expansion behavior.
- Do not throw on malformed JSON; display the original argument text.
- Render no argument block for absent or whitespace-only arguments.
- Use the existing `CodeBlock` with formatted display text and the original `argumentsJSON` as `copyText`, so copying preserves the wire value exactly.
- Preserve existing output, error, and image rendering.
- Run Biome on touched frontend files before the frontend gates.

---

### Task 1: Add the argument body component and registry wiring

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/transcript/MCPToolArguments.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/toolRenderers.ts` (`DEFAULT_DESCRIPTOR`)

**Interfaces:**
- Consumes: `ToolRenderProps` from `toolRenderers.ts`, `ItemModel.argumentsJSON`.
- Produces: `MCPToolArguments: ComponentType<ToolRenderProps>` and a default descriptor body that renders arguments before `RawToolOutput`.

- [ ] **Step 1: Implement the focused component**

Create `MCPToolArguments.tsx` with a named component:

```tsx
import { CodeBlock } from "../../../widgets";
import type { ToolRenderProps } from "./toolRenderers";

export function MCPToolArguments({ item }: ToolRenderProps) {
  const original = item.argumentsJSON;
  const raw = original?.trim();
  if (!raw) return null;

  let formatted = original ?? raw;
  try {
    formatted = JSON.stringify(JSON.parse(original ?? raw), null, 2);
  } catch {
    // Preserve malformed wire input as received.
  }

  return (
    <section aria-label="Tool call arguments">
      <CodeBlock text={formatted} copyText={original ?? raw} copyLabel="Copy arguments" fold={false} />
    </section>
  );
}
```

Use the repository’s existing body/code-block styling conventions rather than introducing a new visual system. Keep the text selectable and do not truncate it.

- [ ] **Step 2: Wire the component into the default renderer**

Keep `RawToolOutput` as the existing output child and compose `MCPToolArguments` before it in the default body component, passing through `item`, `live`, and `sessionRef`. Do not alter the registry matching behavior or the default body contract.

- [ ] **Step 3: Run formatting and type checks for touched files**

Run:

```bash
cd cmd/serf-hub/frontend
npx biome check --write src/panes/session/transcript/MCPToolArguments.tsx src/panes/session/transcript/toolRenderers.ts
npx tsc --noEmit
```

Expected: formatting succeeds and TypeScript reports no errors.

- [ ] **Step 4: Commit the implementation wiring**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/MCPToolArguments.tsx cmd/serf-hub/frontend/src/panes/session/transcript/toolRenderers.ts
git commit -m "feat(webui): render MCP tool arguments"
```

### Task 2: Add focused renderer regression tests

**Files:**
- Create: `cmd/serf-hub/frontend/src/panes/session/transcript/MCPToolArguments.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx` only if integration coverage needs an existing fixture helper.

**Interfaces:**
- Consumes: `MCPToolArguments`, `toolRendererFor`, `ToolCallItem`.
- Produces: deterministic regression coverage for valid, malformed, absent, and combined arguments.

- [ ] **Step 1: Add valid JSON formatting test**

Render `MCPToolArguments` with an item whose `argumentsJSON` is `{"width":375,"options":{"mobile":true}}`. Assert the argument section has accessible name `Tool call arguments` and its `<pre>` text equals:

```text
{
  "width": 375,
  "options": {
    "mobile": true
  }
}
```

- [ ] **Step 2: Add malformed and empty input tests**

Render malformed `argumentsJSON` such as `{width:375}` and assert the original string appears. Render items with `undefined` and whitespace-only arguments and assert no argument section exists.

- [ ] **Step 3: Add default-descriptor integration test**

Render `ToolCallItem` with an unregistered tool name, valid arguments, and output text. Open the details element, then assert the argument block and existing output are both present. Add an error-text case and assert the error remains present with the argument block.

Click `Copy arguments` with formatting-sensitive input and assert the clipboard receives the original string byte-for-byte.

- [ ] **Step 4: Run focused tests and commit tests**

Run:

```bash
cd cmd/serf-hub/frontend
npx vitest run src/panes/session/transcript/MCPToolArguments.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx src/panes/session/transcript/toolRenderers.test.ts
npx biome check --write src/panes/session/transcript/MCPToolArguments.test.tsx src/panes/session/transcript/ToolCallItem.test.tsx
```

Expected: all focused tests pass. Commit:

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/MCPToolArguments.test.tsx cmd/serf-hub/frontend/src/panes/session/transcript/ToolCallItem.test.tsx
git commit -m "test(webui): cover MCP tool argument rendering"
```

### Task 3: Run complete verification and review the diff

**Files:**
- No planned source changes.

**Interfaces:**
- Consumes: committed implementation and regression tests from Tasks 1–2.
- Produces: verified frontend gates and a clean, focused diff.

- [ ] **Step 1: Run the canonical frontend gate**

```bash
make test-web
```

Expected: frontend unit tests, typecheck, and Biome gates pass.

- [ ] **Step 2: Run the browser gate when available**

```bash
make test-web-browser
```

Expected: browser geometry and browser guards pass. If the environment lacks the required browser, record the explicit failure and do not mask it.

- [ ] **Step 3: Inspect final repository state**

```bash
git status --short
git diff HEAD~2..HEAD --stat
git log --oneline -3
```

Confirm only the spec, component, renderer wiring, and focused tests changed in this branch, with no generated or scratch files.

- [ ] **Step 4: Commit any required formatting-only correction**

If a gate identifies a real formatting issue, fix it, rerun the affected gate, and commit only the correction:

```bash
git add <touched-file>
git commit -m "style(webui): format MCP argument renderer"
```
