# ANSI Shell Output Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render captured ANSI SGR styling in bash-family web transcript results without changing stored or copied output.

**Architecture:** `CodeBlock` gains an opt-in ANSI mode backed by a pure structured parser. The parser retains only presentational SGR sequences, converts `anser` JSON bundles into logical styled lines, and lets `CodeBlock` fold those parsed lines without losing state inherited from hidden lines. The shell descriptor enables the mode for its three registered names; all other consumers stay plain.

**Tech Stack:** React 19, TypeScript 6, Vitest, CSS modules/design tokens, `anser` 2.3.5, `ansi-regex` 6.2.2.

## Global Constraints

- Keep the original `text` prop byte-for-byte authoritative for clipboard writes.
- Render structured React elements only; never use generated HTML or `dangerouslySetInnerHTML`.
- Support standard/bright foreground and background colors, 256-color, truecolor, bold, dim, italic, underline, inverse, strike-through, and resets.
- Ignore blink and consume unsupported or malformed terminal controls.
- Parse all bounded output before selecting CodeBlock's folded tail so inherited style reaches visible lines.
- Scope ANSI mode to `shell`, `exec_command`, and `run_shell_command`.
- Keep default tests deterministic and offline.

---

### Task 1: Structured ANSI rendering in CodeBlock

**Files:**
- Modify: `cmd/serf-hub/frontend/package.json`
- Modify: `cmd/serf-hub/frontend/package-lock.json`
- Create: `cmd/serf-hub/frontend/src/widgets/codeblock/ansi.ts`
- Create: `cmd/serf-hub/frontend/src/widgets/codeblock/ansiLine.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.module.css`
- Modify: `cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx`
- Modify: `cmd/serf-hub/frontend/src/styles/tokens.css`

**Interfaces:**
- Produces: `CodeBlockProps.ansi?: boolean`.
- Produces: `parseAnsiLines(text: string): AnsiLine[]`, where `AnsiLine` is `AnsiRun[]` and each `AnsiRun` has `text`, optional semantic foreground/background colors, and decoration flags.
- Produces: `AnsiLineContent({ line }: { line: AnsiLine })`, which renders escaped React text/spans.

- [ ] **Step 1: Write the screenshot-shaped failing CodeBlock test**

Add a test using literal bytes:

```tsx
const vitestOutput =
  "\u001b[2m Test Files \u001b[22m \u001b[1m\u001b[32m283 passed\u001b[39m\u001b[22m\n" +
  "\u001b[2m      Tests \u001b[22m \u001b[1m\u001b[32m4904 passed\u001b[39m\u001b[22m";
const { container } = render(<CodeBlock text={vitestOutput} ansi />);
expect(container.querySelector("code")?.textContent).toBe(" Test Files  283 passed\n      Tests  4904 passed");
expect(screen.getByText("283 passed").closest("[data-ansi-fg]")?.getAttribute("data-ansi-fg")).toBe("green");
expect(screen.getByText(" Test Files ").closest("[data-ansi-dim]")).toBeTruthy();
```

This catches the current bug: removing ANSI parsing makes raw escape bytes reappear in `textContent` and removes the styled runs.

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism src/widgets/codeblock/codeblock.test.tsx
```

Expected: FAIL because `CodeBlock` ignores `ansi` and the code element still contains `\u001b[` sequences.

- [ ] **Step 3: Add the parser dependencies**

Run:

```bash
cd cmd/serf-hub/frontend
npm install anser@2.3.5 ansi-regex@6.2.2
```

Use `anser.ansiToJson(..., { use_classes: true, remove_empty: true })`; do not use its HTML API.

- [ ] **Step 4: Implement the minimal structured parser**

In `ansi.ts`, define named colors:

```ts
export type AnsiNamedColor =
  | "black" | "red" | "green" | "yellow" | "blue" | "magenta" | "cyan" | "white"
  | "bright-black" | "bright-red" | "bright-green" | "bright-yellow"
  | "bright-blue" | "bright-magenta" | "bright-cyan" | "bright-white";

export type AnsiColor =
  | { kind: "named"; name: AnsiNamedColor }
  | { kind: "rgb"; value: string };

export interface AnsiRun {
  text: string;
  foreground?: AnsiColor;
  background?: AnsiColor;
  bold: boolean;
  dim: boolean;
  italic: boolean;
  underline: boolean;
  hidden: boolean;
  strikethrough: boolean;
}

export type AnsiLine = AnsiRun[];
export function parseAnsiLines(text: string): AnsiLine[];
```

Normalize C1 CSI (`\u009b`) to `\u001b[`. Use `ansi-regex` to remove every matched control except `ESC [ digits/semicolons m`; remove remaining ESC and non-whitespace C0 controls. Parse the sanitized value with `new Anser().ansiToJson` so state persists over newlines. Convert `ansi-red`/`ansi-bright-red` class names to named colors, `ansi-palette-N` to an xterm RGB value, and `ansi-truecolor` plus the parser's validated `*_truecolor` field to RGB. Split bundle content on `\n` while preserving empty and trailing logical lines.

In `ansiLine.tsx`, map named colors to `var(--ansi-<name>)` and RGB colors to `rgb(<validated channels>)`. Emit spans only for styled runs. Apply decoration classes for font weight/style, opacity, underline, hidden text, and strike-through. Do not render blink.

- [ ] **Step 5: Add the ANSI palette and CodeBlock mode**

Add the sixteen `--ansi-*` tokens to both theme blocks in `tokens.css`. Use the existing semantic hues where their meaning matches and distinct theme-legible terminal values for magenta/cyan/white/black variants.

Extend `CodeBlockProps`:

```ts
ansi?: boolean;
```

When false, retain the current string-line path exactly. When true, memoize `parseAnsiLines(text)`, derive the same line count/fold indexes from those parsed lines, and render each selected `AnsiLine` through `AnsiLineContent`. Insert literal `"\n"` separators in the no-gutter path. Keep `handleCopy()` unchanged so it writes `text`.

- [ ] **Step 6: Run the focused test and verify GREEN**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism src/widgets/codeblock/codeblock.test.tsx
```

Expected: PASS with the screenshot text free of raw escape bytes and the expected styled runs present.

- [ ] **Step 7: Commit the core renderer**

```bash
git add cmd/serf-hub/frontend/package.json \
  cmd/serf-hub/frontend/package-lock.json \
  cmd/serf-hub/frontend/src/widgets/codeblock/ansi.ts \
  cmd/serf-hub/frontend/src/widgets/codeblock/ansiLine.tsx \
  cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx \
  cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.module.css \
  cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx \
  cmd/serf-hub/frontend/src/styles/tokens.css
git commit -m "feat(webui): render ANSI-styled code output"
```

### Task 2: ANSI edge contracts and folding

**Files:**
- Create: `cmd/serf-hub/frontend/src/widgets/codeblock/ansi.test.ts`
- Modify: `cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx`
- Modify if a RED case requires it: `cmd/serf-hub/frontend/src/widgets/codeblock/ansi.ts`
- Modify if a RED case requires it: `cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx`

**Interfaces:**
- Consumes: `parseAnsiLines(text): AnsiLine[]`.
- Consumes: `CodeBlockProps.ansi`.

- [ ] **Step 1: Add parser contract tests before expanding behavior**

Use literal, hand-derived expectations for:

```ts
parseAnsiLines("\u001b[31;1mred\u001b[22;39m plain");
parseAnsiLines("\u001b[38;5;196mindexed\u001b[39m");
parseAnsiLines("\u001b[38;2;12;34;56mtrue\u001b[39m");
parseAnsiLines("\u001b[3;4;9mdecorated\u001b[23;24;29m");
parseAnsiLines("\u001b[7mreverse\u001b[27m");
parseAnsiLines("safe\u001b]8;;https://example.com\u0007link\u001b]8;;\u0007");
parseAnsiLines("a\u001b[2Jb\u001b[Hc\u001b[not-valid");
```

Assert semantic run fields and plain concatenated text, not serialized React or parser implementation details. These catch dropped selective resets, wrong palette conversion, leaked OSC/cursor sequences, and malformed escape fragments.

- [ ] **Step 2: Add a folded-tail inheritance test**

Construct fifteen lines where line 1 starts green and no reset occurs until line 15. Render `CodeBlock ansi`; the default fold hides line 1. Assert visible line 15 still has `data-ansi-fg="green"`. This catches parsing only the visible tail instead of the complete bounded text.

- [ ] **Step 3: Run edge tests and verify any uncovered cases RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism src/widgets/codeblock/ansi.test.ts src/widgets/codeblock/codeblock.test.tsx
```

Expected: any parser behavior not already covered by Task 1 fails by a semantic field/text mismatch. If all parser cases are already green, mutation-check them before committing by temporarily bypassing sanitization and full-text parsing, observing the OSC and folded-tail tests fail, then restore the implementation.

- [ ] **Step 4: Implement only the behavior exposed by RED**

Correct state conversion, control filtering, line splitting, or full-text-before-fold ordering as named by the failing case. Do not add cursor emulation, linkification, HTML handling, or animation.

- [ ] **Step 5: Verify edge tests GREEN and clipboard bytes unchanged**

Add/retain this characterization:

```tsx
const source = "\u001b[32mgreen\u001b[0m";
render(<CodeBlock text={source} ansi />);
await user.click(screen.getByRole("button", { name: "Copy" }));
expect(writeText).toHaveBeenCalledExactlyOnceWith(source);
```

Run the two focused test files again and require exit 0.

- [ ] **Step 6: Commit edge coverage**

```bash
git add cmd/serf-hub/frontend/src/widgets/codeblock/ansi.test.ts \
  cmd/serf-hub/frontend/src/widgets/codeblock/codeblock.test.tsx \
  cmd/serf-hub/frontend/src/widgets/codeblock/ansi.ts \
  cmd/serf-hub/frontend/src/widgets/codeblock/index.tsx
git commit -m "test(webui): cover ANSI output edge contracts"
```

### Task 3: Enable ANSI for bash-family tool results

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/transcript/rawToolOutput.test.tsx`

**Interfaces:**
- Consumes: `<CodeBlock ansi text={body} copyLabel="Copy output" />`.

- [ ] **Step 1: Write the failing shell integration test**

Render the registered descriptor body with screenshot-shaped ANSI output for each name:

```tsx
for (const toolName of ["shell", "exec_command", "run_shell_command"]) {
  const Body = toolRendererFor(toolName).body!;
  const { container, unmount } = render(
    <Body item={item({ toolName, output: "\u001b[32mPASS\u001b[0m" })} live={false} />,
  );
  expect(container.querySelector("code")?.textContent).toBe("PASS");
  expect(container.querySelector('[data-ansi-fg="green"]')?.textContent).toBe("PASS");
  unmount();
}
```

The production change that makes this pass is adding `ansi` to the shell body's `CodeBlock`; removing that prop must fail it.

- [ ] **Step 2: Run the shell test and verify RED**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism src/panes/session/transcript/tools/shellTool.test.tsx
```

Expected: FAIL because the shell body still renders the escape-bearing string as plain text.

- [ ] **Step 3: Enable ANSI mode in ShellBody**

Change only the return:

```tsx
return <CodeBlock text={body} copyLabel="Copy output" ansi />;
```

Do not enable ANSI in `RawToolOutput`; its generic fallback remains byte-honest plain text.

- [ ] **Step 4: Verify shell GREEN and generic raw output unchanged**

Run:

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism \
  src/panes/session/transcript/tools/shellTool.test.tsx \
  src/panes/session/transcript/rawToolOutput.test.tsx
```

Expected: both files pass.

- [ ] **Step 5: Commit shell integration**

```bash
git add cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.tsx \
  cmd/serf-hub/frontend/src/panes/session/transcript/tools/shellTool.test.tsx
git commit -m "fix(webui): interpret ANSI in shell results"
```

### Task 4: Full verification and kata evidence

**Files:**
- Modify only if required by verification: files already in scope.

- [ ] **Step 1: Run focused transcript/widget tests**

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism \
  src/widgets/codeblock/ansi.test.ts \
  src/widgets/codeblock/codeblock.test.tsx \
  src/panes/session/transcript/tools/shellTool.test.tsx \
  src/panes/session/transcript/rawToolOutput.test.tsx
```

- [ ] **Step 2: Run full frontend tests**

```bash
cd cmd/serf-hub/frontend
npm test -- --no-file-parallelism
```

- [ ] **Step 3: Run static and production checks**

```bash
cd cmd/serf-hub/frontend
npm run lint
npm run typecheck
npm run build
cd ../../..
git diff --check
git status --short
```

- [ ] **Step 4: Record evidence without closing before integration**

Comment on kata `mwa9` with RED failure evidence, final commit SHAs, exact passing commands/counts, and any limitations. Leave the kata open until the branch is reviewed and merged into `webui-workspace-shell`.
