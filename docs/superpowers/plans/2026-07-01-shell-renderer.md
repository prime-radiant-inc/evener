# Shell Tool Renderer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve Serf Hub's shell/bash renderer and standardize expandable tool row/body components across visible tools.

**Architecture:** Keep the existing no-bundler renderer split, but make `renderer.js` own a shared expandable row contract and make `renderer-tools.js` supply standardized body variants. Shell-like tools become a terminal variant: collapsed rows use `$`, expanded bodies repeat the full command with pre-wrapped output and an exit/runtime footer.

**Tech Stack:** Browser JavaScript loaded from `cmd/serf-hub/assets/*.js`, CSS in `cmd/serf-hub/assets/style.css`, deterministic JSDOM tests in `cmd/serf-hub/jstest`, Go/HTML templates only if renderer asset loading changes.

## Global Constraints

- Default tests must be deterministic and must not require provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- Do not change backend event shapes.
- Do not change tool execution semantics or output capture.
- Do not turn `communicate`, `task_list`, or `delegate` into ordinary visible tool cards.
- Keep Serf's existing transcript tone: quiet annotation tier, low chrome, clear hierarchy, and honest output states.
- Disclosure is subtle but visible, inline at the end of the action/command text, and keyboard accessible.
- Shell output uses `pre-wrap`, and the expanded terminal body always shows the full untruncated command.

---

## File structure

- Modify `cmd/serf-hub/assets/renderer.js`
  - Tool row construction in `beginToolCall`.
  - Shared disclosure placement and `aria-expanded` initialization.
  - Empty body cleanup after `bodyEnd`.
  - Metadata visibility/placement hooks.
- Modify `cmd/serf-hub/assets/renderer-tools.js`
  - Add body helper return fields/classes used by standardized body containers.
  - Change shell renderer identity to `$`.
  - Add terminal body DOM and update shell body delta/end handling.
- Modify `cmd/serf-hub/assets/renderer-panels.js`
  - Keep delegated expand/collapse behavior working with the renamed/standardized disclosure button.
  - Update toggle code to sync `aria-expanded` directly.
- Modify `cmd/serf-hub/assets/style.css`
  - Shared row layout: action left, inline disclosure, metadata right.
  - Standardized body container variants.
  - Terminal body styling and pre-wrap output.
  - Mobile/compact pane rules that avoid horizontal overflow.
- Modify `cmd/serf-hub/jstest/test-tool-renderers.js`
  - Contract tests for shell row, terminal body, shared disclosure, preview/diff body variants, cheap clusters, and special suppressions.
- Modify `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js` and `cmd/serf-hub/jstest/test-mobile-css.js` if they assert the old hover-only metadata or right-side caret CSS.

---

### Task 1: Pin the shared row/disclosure contract in tests

**Files:**
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`
- Modify if needed after running tests: `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
- Modify if needed after running tests: `cmd/serf-hub/jstest/test-mobile-css.js`

**Interfaces:**
- Consumes: current renderer events `TOOL_CALL_START`, `TOOL_CALL_END`, `TOOL_CALL_OUTPUT_DELTA`.
- Produces: failing tests that define the new DOM/CSS contract before implementation.

- [ ] **Step 1: Add failing assertions for shell row identity and inline disclosure**

In `cmd/serf-hub/jstest/test-tool-renderers.js`, replace the existing scenario name and assertions beginning at the comment `// shell — collapsible details body, exit code result.` with this updated scenario body. Keep the event sequence shape; replace only the check function content for that scenario.

```js
// shell — standardized row, terminal body, exit code footer.
await scenario("shell row uses prompt glyph, inline disclosure, terminal body, and right-side timing", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "shell", arguments_json: JSON.stringify({ command: "ls -la" }), startedAt: 1763714096 }],
  ["TOOL_CALL_OUTPUT_DELTA", { call_id: "s1", delta: "total 8\nfile1\nfile2\n" }],
  ["TOOL_CALL_END", { call_id: "s1", output: "total 8\nfile1\nfile2\n", tool_state: JSON.stringify({ exit_code: 0 }), tool_name: "shell", completedAt: 1763714098, durationMs: 1250 }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.shell");
  if (!card) return { ok: false, detail: "no shell card" };
  const verb = card.querySelector(".verb");
  if (!verb || verb.textContent.trim() !== "$") return { ok: false, detail: "shell row should use $ verb, got " + (verb && JSON.stringify(verb.textContent)) };
  if (/\bshell\b/.test(card.querySelector(".tool-command").textContent.replace("ls -la", ""))) return { ok: false, detail: "collapsed shell row should not expose internal tool name: " + card.querySelector(".tool-command").textContent };
  const command = card.querySelector(".tool-command");
  if (!command || !command.textContent.includes("ls -la")) return { ok: false, detail: "missing command" };
  const disclosure = command.querySelector(".tool-disclosure[data-expand-toggle]");
  if (!disclosure) return { ok: false, detail: "disclosure should be inline inside .tool-command" };
  if (disclosure.getAttribute("aria-expanded") !== "false") return { ok: false, detail: "collapsed disclosure should start aria-expanded=false" };
  const meta = card.querySelector(".tool-meta");
  if (!meta) return { ok: false, detail: "missing tool metadata" };
  if (!meta.textContent.includes("1.3s")) return { ok: false, detail: "missing duration metadata: " + meta.textContent };
  if (!/\d{1,2}:\d{2}:\d{2}/.test(meta.textContent)) return { ok: false, detail: "missing timestamp metadata: " + meta.textContent };
  const body = card.querySelector(".shell-body.tool-body--terminal");
  if (!body) return { ok: false, detail: "no terminal shell body" };
  const prompt = body.querySelector(".terminal-command");
  if (!prompt || prompt.textContent !== "$ ls -la") return { ok: false, detail: "terminal command should repeat full command, got " + (prompt && JSON.stringify(prompt.textContent)) };
  const pre = body.querySelector(".shell-output");
  if (!pre || !pre.textContent.includes("file1")) return { ok: false, detail: "stdout missing" };
  const footer = body.querySelector(".terminal-footer");
  if (!footer || !footer.textContent.includes("exit 0") || !footer.textContent.includes("1.3s")) return { ok: false, detail: "terminal footer missing exit/runtime: " + (footer && footer.textContent) };
  return { ok: true };
});
```

- [ ] **Step 2: Add failing assertions for purpose rows and non-shell disclosure placement**

In the existing scenario `tool purpose leads as the prominent line; command is demoted beneath it`, add these assertions after the `.tool-command` assertions:

```js
const disclosure = command.querySelector(".tool-disclosure[data-expand-toggle]");
if (!disclosure) return { ok: false, detail: "purpose rows should place disclosure inline in demoted command" };
if (intent.querySelector(".tool-disclosure")) return { ok: false, detail: "purpose line should not own the disclosure when a demoted command exists" };
const meta = card.querySelector(".tool-meta");
if (!meta) return { ok: false, detail: "purpose row should keep timing metadata" };
```

In the existing `read_file in cheap cluster with inline range, purpose, and five-line preview` scenario, add these assertions after the `const intent = ...` block:

```js
const readCommand = call.querySelector(".tool-command");
if (!readCommand) return { ok: false, detail: "read_file should have a standardized command row" };
const readDisclosure = readCommand.querySelector(".tool-disclosure[data-expand-toggle]");
if (!readDisclosure) return { ok: false, detail: "read_file disclosure should be inline with the command row" };
```

- [ ] **Step 3: Replace old right-aligned caret CSS assertion with new inline disclosure CSS assertions**

Replace the scenario named `expand caret is right-aligned; card disclosures use a right chevron` with:

```js
await scenario("tool disclosure is inline, visible, and not a separate right-side column", [], () => {
  if (!/\.tool-disclosure\s*\{[^}]*display:\s*inline-flex/.test(styleSrc)) {
    return { ok: false, detail: "tool disclosure should be inline-flex" };
  }
  if (/\.tool-expand-btn\s*\{[^}]*order:\s*3/.test(styleSrc)) {
    return { ok: false, detail: "old right-column tool-expand-btn order:3 rule should be removed" };
  }
  if (!/\.tool-call \.tool-meta\s*\{[^}]*margin-left:\s*auto/.test(styleSrc)) {
    return { ok: false, detail: "tool metadata should remain right-side timing context" };
  }
  if (!/\.notification-card-raw > summary::after/.test(styleSrc) || !/\.notification-card-raw > summary[\s\S]{0,400}justify-content:\s*space-between/.test(styleSrc)) {
    return { ok: false, detail: "non-tool card disclosures must keep their right chevrons" };
  }
  return { ok: true };
});
```

- [ ] **Step 4: Run the targeted test and verify it fails for the new contract**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: FAIL for assertions mentioning `$ verb`, `.tool-disclosure`, `.tool-body--terminal`, or old `tool-expand-btn order:3`. Existing unrelated scenarios should still run.

- [ ] **Step 5: Commit the failing tests**

```bash
git add cmd/serf-hub/jstest/test-tool-renderers.js cmd/serf-hub/jstest/test-pane-and-sidebar-css.js cmd/serf-hub/jstest/test-mobile-css.js
git commit -m "test(hub): pin standardized tool disclosure contract"
```

If `test-pane-and-sidebar-css.js` or `test-mobile-css.js` were not modified, omit them from `git add`.

---

### Task 2: Implement shared inline disclosure row mechanics

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/renderer-panels.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-tool-renderers.js`

**Interfaces:**
- Consumes: failing tests from Task 1.
- Produces: `.tool-disclosure[data-expand-toggle]` button inline in `.tool-command` when a demoted command exists, otherwise inline in the primary action; `aria-expanded` synced on click/keyboard; `.tool-meta` remains right-side metadata.

- [ ] **Step 1: Change disclosure creation in `renderer.js`**

In `beginToolCall`, replace the caret creation block:

```js
// Caret button — keyboard accessible, toggles data-expanded.
const caret = document.createElement("button");
caret.type = "button";
caret.className = "tool-expand-btn";
caret.setAttribute("aria-label", defaultExpanded ? "collapse body" : "expand body");
caret.dataset.expandToggle = "";
caret.textContent = defaultExpanded ? "▾" : "▸";
el.insertBefore(caret, el.firstChild);
state.caretEl = caret;
```

with:

```js
// Inline disclosure button — keyboard accessible, visually belongs to the
// action/command it expands. Purpose rows put it on the demoted command line;
// rows without a purpose put it on the main command/action line.
const disclosure = document.createElement("button");
disclosure.type = "button";
disclosure.className = "tool-disclosure";
disclosure.setAttribute("aria-label", defaultExpanded ? "collapse tool details" : "expand tool details");
disclosure.setAttribute("aria-expanded", defaultExpanded ? "true" : "false");
disclosure.dataset.expandToggle = "";
disclosure.textContent = defaultExpanded ? "▾" : "▸";
command.appendChild(disclosure);
state.caretEl = disclosure;
```

Keep `state.caretEl` for compatibility with the existing empty-body cleanup path.

- [ ] **Step 2: Update expand/collapse event handling in `renderer-panels.js`**

Replace the click handler body lines that set text/label:

```js
toolCall.dataset.expanded = next ? "true" : "false";
btn.textContent = next ? "▾" : "▸";
btn.setAttribute("aria-label", next ? "collapse body" : "expand body");
```

with:

```js
toolCall.dataset.expanded = next ? "true" : "false";
btn.textContent = next ? "▾" : "▸";
btn.setAttribute("aria-label", next ? "collapse tool details" : "expand tool details");
btn.setAttribute("aria-expanded", next ? "true" : "false");
```

- [ ] **Step 3: Replace right-column caret CSS with inline disclosure CSS**

In `cmd/serf-hub/assets/style.css`, replace the `.tool-expand-btn` block with:

```css
/* Inline expandable-tool disclosure. The chevron belongs to the action text it
   expands, not to a separate right-side column. It stays quiet but visible, with
   a hit target larger than the glyph. */
.tool-disclosure {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 22px;
  height: 22px;
  margin-left: var(--space-1);
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-dim);
  cursor: pointer;
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  line-height: 1;
  flex: none;
}
.tool-disclosure:hover { color: var(--text-muted); background: var(--surface-secondary); }
.tool-disclosure:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.tool-call[data-expanded="true"] .tool-disclosure { color: var(--text-muted); }
```

Also update any selectors that refer to `.tool-expand-btn` to include or use `.tool-disclosure` instead. Do not change notification-card disclosure CSS; those are intentionally separate card disclosures.

- [ ] **Step 4: Make tool metadata readable by default**

In `.tool-call .tool-meta`, change:

```css
opacity: 0;
transition: opacity var(--motion-fast);
```

to:

```css
opacity: 1;
```

Remove the now-redundant `.tool-call:hover .tool-meta, .tool-call:focus-within .tool-meta { opacity: 1; }` rule, or leave it harmless if no test forbids it. If `test-pane-and-sidebar-css.js` asserts hover-only metadata, update it to assert default readability instead.

- [ ] **Step 5: Run the targeted renderer test**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: row/disclosure assertions from Task 1 pass, shell terminal body assertions still fail until Task 3.

- [ ] **Step 6: Commit shared disclosure mechanics**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/renderer-panels.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-pane-and-sidebar-css.js cmd/serf-hub/jstest/test-mobile-css.js
git commit -m "feat(hub): standardize inline tool disclosure"
```

If the CSS test files were not modified, omit them from `git add`.

---

### Task 3: Implement shell terminal body variant

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-tools.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Test: `cmd/serf-hub/jstest/test-tool-renderers.js`

**Interfaces:**
- Consumes: shared disclosure mechanics from Task 2.
- Produces: shell renderer with `friendly: "$"`; body object `{ wrap, commandEl, pre, footerEl, moreWrap }`; terminal body class `.tool-body--terminal`; full command line `.terminal-command`; footer `.terminal-footer`.

- [ ] **Step 1: Add shell terminal body helper**

In `cmd/serf-hub/assets/renderer-tools.js`, just before `const shellRenderer = {`, add:

```js
function shellCommandText(args) {
  args = args || {};
  return String(args.command || args.cmd || "");
}

function shellTerminalBody(args, el) {
  const wrap = document.createElement("div");
  wrap.className = "tool-body shell-body tool-body--terminal";

  const commandEl = document.createElement("div");
  commandEl.className = "terminal-command";
  commandEl.textContent = "$ " + shellCommandText(args);
  wrap.appendChild(commandEl);

  const pre = document.createElement("pre");
  pre.className = "shell-output terminal-output";
  wrap.appendChild(pre);

  const footerEl = document.createElement("div");
  footerEl.className = "terminal-footer";
  footerEl.textContent = "running";
  wrap.appendChild(footerEl);

  el.appendChild(wrap);
  return { wrap, commandEl, pre, footerEl };
}

function shellFooterText(data, state) {
  const st = parseToolState(data && data.tool_state);
  const parts = [];
  if (st && st.exit_code != null) parts.push("exit " + st.exit_code);
  else if (data && data.error) parts.push("error");
  if (state && state.durationMs != null) parts.push(formatDurationForTerminal(state.durationMs));
  return parts.join(" · ");
}

function formatDurationForTerminal(ms) {
  const n = Number(ms);
  if (!Number.isFinite(n) || n < 0) return "";
  if (n < 1000) return Math.round(n) + "ms";
  if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "s";
  return Math.round(n / 1000) + "s";
}
```

- [ ] **Step 2: Update `shellRenderer` to use terminal helper and `$` identity**

Replace the shell renderer header/body functions with:

```js
const shellRenderer = {
  mode: "card", friendly: "$",
  target: (a) => clip(shellCommandText(a), 200),
  result: (data) => {
    const st = parseToolState(data.tool_state);
    if (st && st.exit_code != null) return st.exit_code === 0 ? "" : "exit " + st.exit_code;
    return data.error ? "error" : "";
  },
  body: (args, el) => shellTerminalBody(args, el),
  bodyDelta: (state, out) => {
    if (state.body && state.body.pre) {
      setExpandableOutput(state.body, clip(out, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output terminal-output" });
    }
  },
  bodyEnd: (state, data, out) => {
    if (!state.body) return;
    const text = data.error || out || "";
    setExpandableOutput(state.body, clip(text, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output terminal-output" });
    const st = parseToolState(data.tool_state);
    const failed = data.error || (st && st.exit_code && st.exit_code !== 0);
    if (state.body.footerEl) {
      state.body.footerEl.textContent = shellFooterText(data, state) || "done";
      state.body.footerEl.classList.toggle("terminal-footer-bad", !!failed);
    }
    if (failed && state.el) state.el.dataset.expanded = "true";
    if (failed && state.caretEl) {
      state.caretEl.textContent = "▾";
      state.caretEl.setAttribute("aria-label", "collapse tool details");
      state.caretEl.setAttribute("aria-expanded", "true");
    }
  },
};
```

Do not hide the shell body merely because output is empty; the terminal command/footer are meaningful body content.

- [ ] **Step 3: Add terminal CSS**

In `style.css`, replace the existing `.shell-output` rule with these rules:

```css
.tool-body--terminal {
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding: var(--space-2) 0 var(--space-2) var(--space-3);
  border-left: 1px solid var(--rule);
  color: var(--text-muted);
}
.terminal-command {
  margin: 0 0 var(--space-2) 0;
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: var(--leading-tight);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.shell-output {
  margin: var(--space-2) 0 0;
  padding: var(--space-3) var(--space-3);
  background: var(--bg);
  border-radius: var(--radius-md);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: var(--leading-tight);
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: anywhere;
}
.terminal-footer {
  margin-top: var(--space-2);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text-dim);
}
.terminal-footer-bad { color: var(--error); }
```

Keep `.tool-body` generic rules for other tools.

- [ ] **Step 4: Run shell-focused tests**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: shell row, terminal command, output, and footer assertions pass. If the old `nonzero exit shows 'exit N'; clean exit shows nothing` scenario still passes for row result semantics, keep it. If it fails because footer now includes `exit 0`, adjust only assertions that inspect terminal footer vs row `.result-detail`.

- [ ] **Step 5: Commit terminal body implementation**

```bash
git add cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "feat(hub): render shell tools as terminal transcripts"
```

---

### Task 4: Standardize body variant classes for preview and diff tools

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-tools.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`

**Interfaces:**
- Consumes: shared `.tool-body` contract from Tasks 2-3.
- Produces: preview renderers with `.tool-body--preview`; diff renderers with `.tool-body--diff`; list renderers with `.tool-body--list` where applicable.

- [ ] **Step 1: Add failing body variant assertions for preview/diff tools**

In `test-tool-renderers.js`, add these assertions to existing scenarios:

In `read_file in cheap cluster with inline range...`, after `const body = call.querySelector(".read-tool-body");`:

```js
if (!body.classList.contains("tool-body--preview")) return { ok: false, detail: "read body should use preview body variant" };
```

In `job_read_output renders status, truncation, and output preview`, after `const output = call.querySelector(".job-output");`:

```js
const jobBody = call.querySelector(".job-output-body");
if (!jobBody || !jobBody.classList.contains("tool-body--preview")) return { ok: false, detail: "job output body should use preview body variant" };
```

In `edit_file diff body with direct old/new preview` or the existing edit diff scenario, after obtaining `const body = card.querySelector(".edit-body");`:

```js
if (!body.classList.contains("tool-body--diff")) return { ok: false, detail: "edit body should use diff body variant" };
```

In `apply_patch diff body with five-line preview`, after obtaining `const body = card.querySelector(".patch-body");`:

```js
if (!body.classList.contains("tool-body--diff")) return { ok: false, detail: "patch body should use diff body variant" };
```

- [ ] **Step 2: Verify the new assertions fail**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: FAIL messages mention missing `tool-body--preview` or `tool-body--diff`.

- [ ] **Step 3: Update shared body helper classes in `renderer-tools.js`**

Change `cheapToolBody` wrapper class from:

```js
wrap.className = "tool-body cheap-tool-body";
```

to:

```js
wrap.className = "tool-body cheap-tool-body tool-body--preview";
```

Change `readToolBody` wrapper class from:

```js
wrap.className = "tool-body cheap-tool-body read-tool-body";
```

to:

```js
wrap.className = "tool-body cheap-tool-body read-tool-body tool-body--preview";
```

Change `outputPreviewBody` wrapper class from:

```js
wrap.className = "tool-body output-preview-body " + className;
```

to:

```js
wrap.className = "tool-body output-preview-body tool-body--preview " + className;
```

Change edit body class from:

```js
wrap.className = "tool-body edit-body";
```

to:

```js
wrap.className = "tool-body edit-body tool-body--diff";
```

Change patch body class from:

```js
wrap.className = "tool-body output-preview-body patch-body";
```

to:

```js
wrap.className = "tool-body output-preview-body patch-body tool-body--diff";
```

For `webSearchRenderer`, change:

```js
ul.className = "tool-body search-body";
```

to:

```js
ul.className = "tool-body search-body tool-body--list";
```

For `useSkillRenderer`, change:

```js
div.className = "tool-body use-skill-body";
```

to:

```js
div.className = "tool-body use-skill-body tool-body--preview";
```

- [ ] **Step 4: Add CSS aliases for body variants without changing visual weight**

In `style.css`, near the shared `.tool-body` rules, add:

```css
.tool-body--preview,
.tool-body--diff,
.tool-body--list {
  flex: 0 0 100%;
}
```

Keep existing concrete classes (`.cheap-tool-output`, `.diff-body`, `.search-body`) responsible for content-specific styling.

- [ ] **Step 5: Run renderer tests**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS for added variant assertions and existing renderer behavior.

- [ ] **Step 6: Commit body variant standardization**

```bash
git add cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "refactor(hub): standardize tool body variants"
```

---

### Task 5: Preserve empty-body, cheap-cluster, and special-tool behavior

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`

**Interfaces:**
- Consumes: standardized row/body behavior from Tasks 2-4.
- Produces: no fake disclosures for empty rows; cheap cluster disclosure still works; `communicate`, `task_list`, and `delegate` remain special.

- [ ] **Step 1: Add regression assertion for empty non-shell body disclosure removal**

Add a new scenario near the job output tests in `test-tool-renderers.js`:

```js
await scenario("empty preview body removes disclosure after finalization", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "jr-empty", tool_name: "job_read_output", arguments_json: JSON.stringify({ job_id: "job_empty" }) }],
  ["TOOL_CALL_END", { call_id: "jr-empty", tool_name: "job_read_output", output: "", tool_state: JSON.stringify({ job_id: "job_empty", type: "shell", status: "completed", output: "", total_bytes: 0 }) }],
], ({ conv }) => {
  const call = conv.querySelector(".tool-call.job_read_output");
  if (!call) return { ok: false, detail: "no job_read_output card" };
  if (call.querySelector(".tool-disclosure")) return { ok: false, detail: "empty preview body should not keep disclosure" };
  if (call.hasAttribute("data-expanded")) return { ok: false, detail: "empty preview body should remove data-expanded" };
  return { ok: true };
});
```

- [ ] **Step 2: Add cheap-cluster disclosure assertion**

In the existing `cheap cluster collapses to a mutating-step-first summary once done` scenario, add after `const summary = ...`:

```js
if (summary.getAttribute("aria-expanded") !== "false") return { ok: false, detail: "collapsed cluster summary should expose aria-expanded=false" };
summary.click();
if (summary.getAttribute("aria-expanded") !== "true") return { ok: false, detail: "expanded cluster summary should expose aria-expanded=true" };
```

If the scenario already clicks `summary` later, remove the duplicate later click or adapt it so the cluster is clicked only once before checking `.open`.

- [ ] **Step 3: Verify regression tests before code changes**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: If Task 2 already preserved empty-body cleanup and cluster ARIA, this may pass. If it fails, continue to Step 4.

- [ ] **Step 4: Fix empty-body cleanup if needed**

In `renderer.js` inside `finalizeToolCall`, keep the cleanup logic but ensure it works with `.tool-disclosure`:

```js
if (m.caretEl && this.toolBodyIsEmpty(m)) {
  m.caretEl.remove();
  m.caretEl = null;
  if (m.el) m.el.removeAttribute("data-expanded");
}
```

No class-name-specific code should assume `.tool-expand-btn`.

- [ ] **Step 5: Fix cheap-cluster ARIA if needed**

If cluster summary ARIA fails, update `endCheapCluster` after `cluster.classList.add("done");`:

```js
summary.setAttribute("aria-expanded", cluster.classList.contains("open") ? "true" : "false");
```

Do not replace the existing `bindDisclosureToggle(summary, cluster)`; it already handles clicks.

- [ ] **Step 6: Run renderer tests**

Run:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS.

- [ ] **Step 7: Commit regressions/fixes**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "test(hub): preserve tool disclosure edge cases"
```

If `style.css` was not modified, omit it from `git add`.

---

### Task 6: Final CSS/mobile compatibility and full verification

**Files:**
- Modify if needed: `cmd/serf-hub/assets/style.css`
- Modify if needed: `cmd/serf-hub/jstest/test-mobile-css.js`
- Modify if needed: `cmd/serf-hub/jstest/test-pane-and-sidebar-css.js`
- No changes expected outside `cmd/serf-hub` unless a test reveals a template/server issue.

**Interfaces:**
- Consumes: all implementation tasks.
- Produces: full deterministic renderer test pass and a clean commit for any compatibility fixes.

- [ ] **Step 1: Run all Serf Hub JS renderer tests**

Run:

```bash
cd cmd/serf-hub && ./jstest/run-all.sh
```

Expected: exit 0. If failures mention old `.tool-meta` hover-only expectations or `.tool-expand-btn`, update those tests to assert the new contract from the spec: metadata readable, disclosure inline, notification-card disclosures unchanged.

- [ ] **Step 2: Run targeted CSS/mobile tests directly if the full suite failed there**

Run:

```bash
node cmd/serf-hub/jstest/test-mobile-css.js
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected: exit 0 for both. Fix only assertions/rules related to the new disclosure/metadata contract. Preserve existing mobile overflow guard: `.tool-call.has-purpose .tool-command` must avoid margin-induced horizontal overflow.

- [ ] **Step 3: Run Go tests for the hub package if asset/template behavior changed**

If only JS/CSS changed, this is optional but recommended before handoff. Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected: PASS.

- [ ] **Step 4: Inspect git diff for accidental unrelated changes**

Run:

```bash
git status --short
git diff -- cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/assets/renderer-panels.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-tool-renderers.js cmd/serf-hub/jstest/test-mobile-css.js cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected: only intentional renderer/test/CSS changes. Do not stage pre-existing untracked files such as `kimi-jobs-ux-cleanup.md`.

- [ ] **Step 5: Commit final compatibility fixes if any files changed after Task 5**

If Step 1-3 required changes, commit them:

```bash
git add cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-mobile-css.js cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
git commit -m "fix(hub): verify tool renderer responsive contract"
```

If no files changed, skip this commit.

- [ ] **Step 6: Record final verification evidence**

Run:

```bash
git status --short
```

Expected: no tracked changes. The pre-existing untracked `kimi-jobs-ux-cleanup.md` may still appear and must remain untouched.

Final report should include:

- Commits created.
- Exact test commands and pass/fail results.
- Any remaining untracked pre-existing files.
