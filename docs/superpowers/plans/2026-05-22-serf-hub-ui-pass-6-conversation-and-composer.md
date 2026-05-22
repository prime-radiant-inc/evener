# Serf Hub UI Pass 6 — Conversation & Composer Tightening Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tighten the serf-hub conversation transcript (less padding, snugger line-height, smaller gaps, scrollable diff bodies) and declutter the composer (one attachment rail, three-zone controls row, mono single-line status row).

**Architecture:** All work lives in `cmd/serf-hub/assets/style.css`, `cmd/serf-hub/templates/partials/workspace.html`, `cmd/serf-hub/templates/partials/input_strip.html`, and minor verification touches in `cmd/serf-hub/assets/renderer.js` + `cmd/serf-hub/jstest/test-input-area.js`. The conversation becomes a container-query host (`#workspace`) so phone-density rules narrow tool-cluster indent without re-asserting media queries. The composer's two attachment rails collapse to one container that the paste handler renders into. The controls row gets explicit `.controls-left / .controls-center / .controls-right` zones so layout intent matches markup.

**Tech Stack:** Plain HTML templates (Go `html/template`), vanilla CSS with custom properties + container queries, vanilla JS (no framework). The design tokens (`--space-N`, `--leading-snug`, `--text-md`, `surface-inset`) introduced by Passes 1–3 are prerequisites — this plan assumes they exist as documented in `/home/jesse/git/prime-radiant/serf/docs/superpowers/specs/2026-05-22-serf-hub-design-language.md` §1.3, §1.2, §2.2.

**Class-name reality check:** The design spec uses shorthand `.user-msg / .assistant-msg / .tool-cluster`. The current codebase uses `.user-message / .assistant-message / .tool-call-cluster` and `.tool-call .tool-status`. This plan targets the **real** class names. The Pass 2 / Pass 4 renames (if and when scheduled) are out of scope here.

---

## File Structure

- Modify `cmd/serf-hub/assets/style.css`:
  - Conversation block (~line 357) — padding, line-height, gaps.
  - User pill (`.user-message .pill`, ~line 446) — `max-width: min(62%, 540px)`, `line-height: var(--leading-snug)`.
  - Assistant body (`.assistant-message`, ~line 463) — line-height + bottom margin.
  - Tool-call cluster (`.tool-call-cluster`, ~line 469) — bottom margin + new `--tool-indent` custom property; `.tool-call .tool-status` negative margin keyed to `--tool-indent`.
  - Diff body (~line 488) and other tool-body variants (~line 494, 507, 514, 515, 517) — migrate to `surface-inset` token where appropriate and switch to `white-space: pre` + `overflow-x: auto` on the diff body specifically.
  - Diagnostic (~line 520) — promote `max-width: min(720px, 100%)` to the base rule.
  - Phone overrides (~line 982) — drop the now-redundant diagnostic override; add `#workspace` container query for sub-600px tool-indent + conversation padding.
  - Composer attachments (~line 360, 386) — merge `.input-attachments` + `.composer-attachments` into one `.composer-attachments` rule; delete the orphan `.attachment-chip` legacy rules.
  - Composer controls (~line 405) — add `.controls-left / .controls-center / .controls-right`; remove `.controls-spacer` (replaced by `.controls-center { flex: 1; }`).
  - Composer status row (~line 422) — restyle as mono single line with `.status-key` / `.status-value` semantics.
  - Queue preview (~line 367) — drop the inline kbd hint chrome; introduce `.queue-preview-help` glyph with native `title` tooltip.

- Modify `cmd/serf-hub/templates/partials/workspace.html`:
  - Composer markup: single attachment container, queue-preview hint as `?` glyph, three-zone controls layout.

- Modify `cmd/serf-hub/templates/partials/input_strip.html`:
  - Wrap each metric in `.status-key` / `.status-value` spans; drop the running-indicator (now in `.controls-center` zone of the controls row).

- Modify `cmd/serf-hub/assets/renderer.js`:
  - Verify the paste container query selector still resolves (it does — same `[data-composer-attachments]` attribute). No behavior change, but check the comment block above line 1675 references the merged container, not "alongside the legacy file-picker pipeline".

- Modify `cmd/serf-hub/jstest/test-input-area.js`:
  - Adjust the JSDOM shell at line 28–42 to match the new workspace.html composer markup (one attachment container, three-zone controls).

- Modify `cmd/serf-hub/jstest/test-queue-and-drain.js`:
  - Same shell adjustment (line ~25).

---

## Task 1: Tighten Conversation Padding, Line-Height, and Inter-Turn Gaps

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:357` (`.conversation`)
- Modify: `cmd/serf-hub/assets/style.css:445-446` (`.user-message`, `.user-message .pill`)
- Modify: `cmd/serf-hub/assets/style.css:463` (`.assistant-message`)
- Modify: `cmd/serf-hub/assets/style.css:469` (`.tool-call-cluster`)

- [ ] **Step 1: Tighten `.conversation` block padding and line-height**

Replace the existing `.conversation` rule with the spec-aligned values. Padding drops from `32px 64px` to `var(--space-5) var(--space-6)` (16/24). Body line-height drops from `1.7` to `var(--leading-snug)`.

```css
.conversation {
  flex: 1;
  min-height: 0;
  padding: var(--space-5) var(--space-6);
  overflow-y: auto;
  font-size: var(--text-md);
  line-height: var(--leading-snug);
  color: var(--text);
  container-type: inline-size;
  container-name: conversation;
}
```

Why `container-type: inline-size`? Task 2's container query narrows `--tool-indent` when the conversation pane is sub-600px, regardless of viewport breakpoint. The spec (§1.7) prefers container queries over media queries for surface-level decisions; the design-language doc explicitly calls out `.conversation` as a container query host.

- [ ] **Step 2: Tighten `.user-message` wrapper and pill**

The wrapper's `margin-bottom: 28px` and `padding-top: 22px` come from the hover-affordance carve-out (kata noted in the existing comment). Reduce the bottom gap to `var(--space-4)` (12); keep the 22px padding-top reserve since the hover hit-zone math hasn't changed. The pill's `max-width: 62%` becomes `min(62%, 540px)` so long reading widths never exceed the comfortable measure even on a 1600px monitor. Line-height goes to `var(--leading-snug)`.

```css
.user-message {
  display: flex;
  justify-content: flex-end;
  margin-bottom: var(--space-4);
  padding-top: 22px;
  position: relative;
}
.user-message .pill {
  max-width: min(62%, 540px);
  padding: var(--space-3) var(--space-4);
  background: var(--bg-raised);
  border-radius: var(--radius-pill);
  font-size: var(--text-md);
  color: var(--text);
  line-height: var(--leading-snug);
}
```

`padding: var(--space-3) var(--space-4)` is `8px 12px` — a hair tighter than the current `8px 14px`. Snaps to the token scale; visually indistinguishable.

- [ ] **Step 3: Tighten `.assistant-message`**

Bottom margin from `24px` to `var(--space-4)` (12). Line-height from `1.6` to `var(--leading-snug)` (1.5). Body font-size moves to `var(--text-md)` per the spec voice-by-surface table (§1.2).

```css
.assistant-message {
  margin-bottom: var(--space-4);
  max-width: 680px;
  font-size: var(--text-md);
  line-height: var(--leading-snug);
  color: var(--text);
}
```

- [ ] **Step 4: Tighten `.tool-call-cluster` margin**

Bottom margin from `12px` to `var(--space-4)` (also 12 — same value, but token-aligned). Note: today's `.tool-call-cluster { margin-bottom: 12px }` is already token-aligned by value; this step is mostly a syntactic upgrade. Keep the `last-child { margin-bottom: 0 }` reset.

```css
.tool-call-cluster {
  margin-bottom: var(--space-4);
}
.tool-call-cluster .tool-call:last-child {
  margin-bottom: 0;
}
```

- [ ] **Step 5: Build + visually verify in a live session**

Run:

```bash
go build ./cmd/serf-hub && ./serf-hub --help >/dev/null
```

Expected: no build errors. Then launch a hub locally and open a session with at least 3 user/assistant turns. Eyeball it: the transcript should breathe less between turns, line-height is noticeably tighter, but text doesn't read as cramped. The user pill caps at 540px even when the workspace is wide.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "ui: tighten conversation padding, leading, and turn gaps (pass 6)"
```

---

## Task 2: Add `--tool-indent` Custom Property and Container Query for Narrow Conversation

**Files:**
- Modify: `cmd/serf-hub/assets/style.css:357` (`.conversation` — already a container from Task 1)
- Modify: `cmd/serf-hub/assets/style.css:471` (`.tool-call .tool-status`)
- Modify: `cmd/serf-hub/assets/style.css:488` (`.diff-body`)
- Modify: `cmd/serf-hub/assets/style.css:494` (`.tool-body`)
- Modify: `cmd/serf-hub/assets/style.css:507` (`.task-list-body`)
- Modify: `cmd/serf-hub/assets/style.css:514` (`.fetch-body`)
- Modify: `cmd/serf-hub/assets/style.css:515` (`.search-body`)
- Modify: `cmd/serf-hub/assets/style.css:517` (`.subagent-reference` — uses 22px, see step 3)
- Modify: `cmd/serf-hub/assets/style.css:520` (`.diagnostic`)
- Modify: `cmd/serf-hub/assets/style.css:554` (`.banner`)
- Modify: `cmd/serf-hub/assets/style.css:982-986` (phone media query block)

- [ ] **Step 1: Declare `--tool-indent` on `.conversation`**

The conversation host owns the indent token. Default 36px; container query narrows it on sub-600px.

```css
.conversation {
  flex: 1;
  min-height: 0;
  padding: var(--space-5) var(--space-6);
  overflow-y: auto;
  font-size: var(--text-md);
  line-height: var(--leading-snug);
  color: var(--text);
  container-type: inline-size;
  container-name: conversation;
  --tool-indent: 36px;
}
```

- [ ] **Step 2: Route tool-cluster left-margins through `--tool-indent`**

Every `.tool-body / .diff-body / .task-list-body / .fetch-body / .search-body / .banner / .diagnostic / .subagent-reference` currently hard-codes a left margin of `28px` or `36px` or `22px`. Re-key them to `--tool-indent` so the indent narrows in lockstep. The `.subagent-reference` (22px today) sits one notch shallower — preserve that via `calc(var(--tool-indent) - var(--space-4))` so on phone it stays proportional.

```css
.diff-body {
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding: var(--space-3) var(--space-4);
  background: var(--surface-secondary);
  border-radius: var(--radius-md);
  font-family: var(--font-mono);
  font-size: var(--text-sm);
  line-height: var(--leading-snug);
  color: var(--text-muted);
  white-space: pre;
  overflow-x: auto;
}
.diff-body .add { color: var(--state-idle); }
.diff-body .del { color: var(--state-awaiting); }
.diff-body .hunk { color: var(--text-dim); }

.tool-body {
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding-left: var(--space-3);
  border-left: 1px solid var(--rule);
  font-size: var(--text-sm);
  color: var(--text-muted);
}
.tool-body summary { cursor: pointer; color: var(--text-muted); padding: 0; user-select: none; }
.tool-body summary:hover { color: var(--text); }

.task-list-body {
  list-style: none;
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding: var(--space-3) 0 var(--space-3) var(--space-3);
  border-left: 1px solid var(--rule);
  display: flex;
  flex-direction: column;
  gap: 3px;
}

.fetch-body {
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding-left: var(--space-3);
  border-left: 1px solid var(--rule);
  font-size: var(--text-sm);
  color: var(--text-muted);
  font-style: italic;
}

.search-body {
  list-style: none;
  margin: 0 0 var(--space-4) var(--tool-indent);
  padding-left: var(--space-3);
  border-left: 1px solid var(--rule);
  font-size: var(--text-sm);
  color: var(--text);
}
.search-body li { padding: 2px 0; }

.subagent-reference {
  margin: 0 0 var(--space-4) calc(var(--tool-indent) - var(--space-4));
  font-size: var(--text-sm);
  color: var(--text-muted);
  cursor: pointer;
}
.subagent-reference .verb { color: var(--state-subagent); font-family: var(--font-mono); }
.subagent-reference:hover { color: var(--text); }

.banner {
  margin: 0 0 var(--space-4) calc(var(--tool-indent) - var(--space-3));
  padding: var(--space-3) var(--space-4);
  font-size: var(--text-sm);
  border-radius: var(--radius-md);
}
.banner.error   { color: var(--state-awaiting); border-left: 2px solid var(--state-awaiting); padding-left: var(--space-3); }
.banner.warning { color: var(--state-warning);  border-left: 2px solid var(--state-warning);  padding-left: var(--space-3); }
.banner.note    { color: var(--text-muted);     border-left: 2px solid var(--rule);           padding-left: var(--space-3); }
```

The diff-body change in this step deliberately includes the `surface-inset` migration (background + radius + padding swap) ahead of Task 3's explicit pass — keeping the rule in one place avoids two edits to the same selector. Task 3 verifies the result rather than redoing it.

- [ ] **Step 3: Re-key `.tool-call .tool-status` negative margin to `--tool-indent`**

Today: `margin-left: -20px` — a fixed offset that puts the status glyph in the "gutter" to the left of the cluster body. Replace with a calc tied to `--tool-indent` so the glyph follows the indent. The spec says "match the indent shift"; a 36→20 indent shift means the negative offset proportionally shrinks too. Use `calc(0 - var(--tool-indent) * 0.56)` which yields `-20px` at 36px indent and `-11.2px` at 20px indent — close enough to the original at desktop and proportionally tighter on phone.

```css
.tool-call .tool-status {
  display: inline-block;
  width: 12px;
  flex: 0 0 12px;
  margin-left: calc(0 - var(--tool-indent) * 0.56);
  text-align: center;
  font-size: var(--text-xs);
  line-height: 1;
  font-family: var(--font-mono);
}
```

- [ ] **Step 4: Container query narrows indent and pads on `.conversation < 600px`**

Add a container query block scoped to the `conversation` container name (set in Task 1 Step 1). At sub-600px the conversation drops `--tool-indent` to 20px and reduces horizontal padding to `var(--space-3)` (8px). This replaces the existing media-query rule at line 984.

```css
@container conversation (max-width: 599px) {
  .conversation { --tool-indent: 20px; padding: var(--space-3); }
}
```

- [ ] **Step 5: Promote diagnostic `max-width` to base and strip the phone override**

The diagnostic block today sets `max-width: 720px` in the base rule and `max-width: none` in the phone override. The spec wants `max-width: min(720px, 100%)` in the base — single rule, no override needed.

Replace the base `.diagnostic` rule:

```css
.diagnostic {
  --diagnostic-accent: var(--state-warning);
  margin: 0 0 var(--space-5) var(--tool-indent);
  max-width: min(720px, 100%);
  padding: var(--space-3) var(--space-4) var(--space-3) var(--space-4);
  background: var(--bg-raised);
  border: 1px solid var(--rule);
  border-left: 3px solid var(--diagnostic-accent);
  border-radius: var(--radius-lg);
  color: var(--text);
  font-size: var(--text-sm);
  line-height: var(--leading-snug);
}
```

Then delete the phone-block override at line 985:

```css
/* DELETE this line from the @media (max-width: 767px) block: */
.diagnostic { margin-left: 0; max-width: none; }
```

The container query in Step 4 already drops `--tool-indent` to 20px below 600px, which gives the diagnostic enough room without zeroing its margin. If a phone viewport renders the conversation narrower than 600px the diagnostic still fits because `max-width: min(720px, 100%)` clamps to the conversation's content box.

- [ ] **Step 6: Strip the now-redundant `.conversation { padding: 12px 14px }` from the phone block**

The container query owns conversation padding now. Remove this line from the `@media (max-width: 767px)` block at line 984:

```css
/* DELETE: */
.conversation { padding: 12px 14px; }
```

Keep `.user-message .pill { max-width: 90%; }` in the phone block — that's a wrapping behavior, not a token concern. (Acceptable to leave; revisit in a later pass.)

- [ ] **Step 7: Build + verify glyph alignment at both widths**

```bash
go build ./cmd/serf-hub
```

Resize the workspace pane in the browser. The tool-call status glyph should sit in the gutter and shift inward as the pane narrows below 600px. Diff bodies should sit on the inset surface (slightly darker than the page bg), with horizontal scrollbars appearing when a single line exceeds the pane width.

- [ ] **Step 8: Commit**

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "ui: token-driven --tool-indent + container query for narrow conversation (pass 6)"
```

---

## Task 3: Verify Diff Body Surface-Inset Migration and Horizontal Scroll

**Files:**
- Verify: `cmd/serf-hub/assets/style.css` `.diff-body` rule (already updated in Task 2 Step 2)

This task is verification-only. The substantive change happened in Task 2 Step 2 because the diff-body indent and the diff-body surface migration share a single CSS rule and editing the same selector twice invites drift.

- [ ] **Step 1: Visually verify diff-body renders on surface-inset**

Open a session with a tool-call that produced a `diff-body` (any Edit/Write call to a real file will do). The diff text should sit on a subtly inset surface — slightly darker than the conversation background in dark mode, slightly lighter in light mode. No left border (that was the old `surface-flat` style); rounded corners (`--radius-md`).

- [ ] **Step 2: Verify horizontal scroll on long monospace lines**

Find or generate a diff with a single line ≥ 200 characters (a long URL, a wide function signature, a base64 blob). The line should NOT wrap — it should overflow horizontally with a scrollbar on the diff body. Today's `white-space: pre-wrap` wraps mid-token, which kills diff readability.

If wrapping is still happening, recheck the `.diff-body` rule in style.css — `white-space: pre` (not `pre-wrap`) and `overflow-x: auto` must both be present.

- [ ] **Step 3: Verify both themes**

Toggle the theme (`data-theme="light"` / `data-theme="dark"`) via the existing theme switcher. The diff-body inset surface should be visible in both. The `--surface-secondary` token is mirrored per Pass 1's token table (§1.1).

- [ ] **Step 4: No commit needed (verification-only task)**

If a problem is found, fix the offending rule in `cmd/serf-hub/assets/style.css` and commit:

```bash
git add cmd/serf-hub/assets/style.css
git commit -m "ui: fix diff-body surface-inset rendering (pass 6 follow-up)"
```

---

## Task 4: Merge `.input-attachments` and `.composer-attachments` Into One Rail

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html:37-39` (composer header)
- Modify: `cmd/serf-hub/assets/style.css:360-391` (attachment CSS rules)
- Modify: `cmd/serf-hub/jstest/test-input-area.js:29-30` (JSDOM shell)
- Modify: `cmd/serf-hub/jstest/test-queue-and-drain.js:25` (JSDOM shell)
- Verify: `cmd/serf-hub/assets/composer-attachments.js` (no change — selector still resolves)
- Verify: `cmd/serf-hub/assets/renderer.js:1684-1698` (no change — selectors still resolve)

The legacy `.input-attachments[data-attachments]` div sits in workspace.html but is unused at runtime — comments in `composer-attachments.js` and `jstest/test-input-area.js` already note "now unused". The renderer only ever queries `[data-composer-attachments]`. So the merge is: delete the legacy div from the template; collapse the two CSS rules into one; verify selectors still resolve.

- [ ] **Step 1: Update workspace.html — replace two attachment divs with one**

Edit `cmd/serf-hub/templates/partials/workspace.html`. The change spans lines 37–39.

Old (lines 37–39):
```html
  <div class="input-attachments" data-attachments></div>
  <div class="composer-attachments" data-composer-attachments></div>
  <div class="composer-attachment-error" data-attachment-error hidden></div>
```

New:
```html
  <div class="composer-attachments" data-composer-attachments data-attachments></div>
  <div class="composer-attachment-error" data-attachment-error hidden></div>
```

Keeping `data-attachments` on the same element preserves any historical selector hits without adding a second DOM node. `data-composer-attachments` is what the paste handler actually queries.

- [ ] **Step 2: Collapse the two CSS rules into one in style.css**

Edit `cmd/serf-hub/assets/style.css`. The current rules span lines 360–391 (two separate blocks: `.input-attachments` at 360–361, `.composer-attachments` + chip styles at 386–391). The `.attachment-chip / .att-thumb / .att-remove` block at lines 377–380 is leftover legacy chrome from the now-retired file-picker pipeline (verified: no JS or template references at runtime; only stale CSS).

Delete these blocks entirely (lines 360–361 and 377–391):

```css
/* DELETE — lines 360–361: */
.input-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
.input-attachments:empty { display: none; }

/* DELETE — lines 377–380 (legacy file-picker chip chrome, no runtime users): */
.attachment-chip { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.attachment-chip .att-thumb { width: 18px; height: 18px; object-fit: cover; border-radius: 2px; }
.attachment-chip .att-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; }
.attachment-chip .att-remove:hover { color: var(--text); }

/* DELETE — old .composer-attachments block (lines 386–391): */
.composer-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
.composer-attachments:empty { display: none; }
.composer-attachment { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; color: var(--text); }
.composer-attachment-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; max-width: 240px; }
.composer-attachment-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; font-size: 13px; line-height: 1; }
.composer-attachment-remove:hover { color: var(--text); }
```

Insert the merged block in their place (single source of truth for attachment chips):

```css
/* Composer attachment rail — paste, drag-drop, and file-picker chips all
   render here through SerfComposerAttachments. Hides itself when empty. */
.composer-attachments {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
  padding-bottom: var(--space-3);
}
.composer-attachments:empty { display: none; }
.composer-attachment {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--bg);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  font-size: var(--text-xs);
  color: var(--text);
}
.composer-attachment-label {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 240px;
}
.composer-attachment-remove {
  cursor: pointer;
  color: var(--text-muted);
  padding: 0 2px;
  border: none;
  background: transparent;
  font-size: var(--text-base);
  line-height: 1;
}
.composer-attachment-remove:hover { color: var(--text); }
```

- [ ] **Step 3: Update jstest JSDOM shells**

Edit `cmd/serf-hub/jstest/test-input-area.js`. At line 29–30 (the JSDOM shell template literal), replace:

```js
    <div class="input-attachments" data-attachments></div>
    <div class="composer-attachments" data-composer-attachments></div>
```

with:

```js
    <div class="composer-attachments" data-composer-attachments data-attachments></div>
```

Edit `cmd/serf-hub/jstest/test-queue-and-drain.js`. At line ~25 (same JSDOM shell pattern), make the equivalent replacement.

- [ ] **Step 4: Update the explanatory comment in renderer.js**

The comment block at `cmd/serf-hub/assets/renderer.js:1675-1683` references "the (now unused) legacy data-attachments div" and "the legacy addFiles / FileReader / data-URL pipeline". The legacy div is gone now. Update the comment to reflect the merged container:

Find the block starting at line 1675:

```js
      // Attachments: paste / drag-drop / file-picker all funnel through
      // SerfComposerAttachments (kata r6a1 + 65mm). The submit handler below
      // reads composerPasteState.items at send/queue/drain time and lets
      // appwire.js base64-encode the ArrayBuffer payloads at the wire
      // boundary (kata v80q). The legacy addFiles / FileReader / data-URL
      // pipeline was retired here — chips render via SerfComposerAttachments
      // into [data-composer-attachments], rejection banners into
      // [data-attachment-error].
```

Replace with:

```js
      // Attachments: paste / drag-drop / file-picker all funnel through
      // SerfComposerAttachments (kata r6a1 + 65mm). The submit handler below
      // reads composerPasteState.items at send/queue/drain time and lets
      // appwire.js base64-encode the ArrayBuffer payloads at the wire
      // boundary (kata v80q). One container — [data-composer-attachments] —
      // holds chips from every entry point; rejection banners go to the
      // sibling [data-attachment-error] element.
```

- [ ] **Step 5: Run jstest suite to confirm no selector breakage**

```bash
cd cmd/serf-hub/jstest && npm test
```

Expected: all input-area and queue-and-drain tests pass. If a test fails because it relied on the duplicate `data-attachments` div, update the test selector to use `[data-composer-attachments]` (the canonical hook).

- [ ] **Step 6: Manual smoke — paste an image into the composer**

Launch the hub locally, open a session, focus the composer textarea, and paste an image from the clipboard. A `.composer-attachment` chip should appear in the now-merged container. Click the `×` to remove it; the chip should disappear. Repeat with drag-drop and the `＋` attach button — all three gestures render into the same container.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-input-area.js cmd/serf-hub/jstest/test-queue-and-drain.js cmd/serf-hub/assets/renderer.js
git commit -m "ui: merge composer attachment rails into single container (pass 6)"
```

---

## Task 5: Refactor Composer Controls Into Three Zones (Left / Center / Right)

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html:50-68` (`.input-controls` block)
- Modify: `cmd/serf-hub/assets/style.css:405-417` (`.input-controls`, `.controls-spacer`, button variants)
- Modify: `cmd/serf-hub/jstest/test-input-area.js:35-39` (JSDOM shell controls block)
- Modify: `cmd/serf-hub/jstest/test-queue-and-drain.js` (JSDOM shell controls block, if present)

The current row uses a single flex container with a `.controls-spacer` div between left controls and right buttons. The new layout uses three named zones, with the center zone taking remaining space (via `flex: 1`) and holding the running indicator. This change is purely structural — no button is added, removed, or rebound.

- [ ] **Step 1: Rewrite the composer controls block in workspace.html**

Edit `cmd/serf-hub/templates/partials/workspace.html`. Replace the entire `.input-controls` block (lines 50–68) with three explicit zones.

Old (lines 50–68):
```html
  <div class="input-controls">
    <button type="button" class="input-btn" data-attach-trigger title="attach image">＋</button>
    {{if .Capabilities.ChangeModel}}
    <button type="button" class="input-chip model-chip" data-model-trigger>{{if .Model}}{{.Model}}{{else}}—{{end}} <span class="chip-caret">▾</span></button>
    {{else}}
    <span class="input-chip model-chip" title="model changes unavailable">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
    {{end}}
    {{if or (eq .State "active") (eq .State "awaiting")}}
    <span class="running-indicator" data-running-indicator><span class="running-dot"></span>running{{if .RunningFor}} · {{.RunningFor}}{{end}}</span>
    {{end}}
    <span class="controls-spacer"></span>
    {{if .Capabilities.Interrupt}}
    <button type="button" class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if and (ne .State "awaiting") (ne .State "active")}} disabled{{end}}>Stop</button>
    {{end}}
    {{if or .Capabilities.Steer .Capabilities.Send .Capabilities.Queue}}
    <button type="button" class="input-btn input-btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
    {{end}}
    <button type="submit" class="input-btn input-btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
  </div>
```

New:
```html
  <div class="input-controls">
    <div class="controls-left">
      <button type="button" class="input-btn" data-attach-trigger title="attach image">＋</button>
      {{if .Capabilities.ChangeModel}}
      <button type="button" class="input-chip model-chip" data-model-trigger>{{if .Model}}{{.Model}}{{else}}—{{end}} <span class="chip-caret">▾</span></button>
      {{else}}
      <span class="input-chip model-chip" title="model changes unavailable">{{if .Model}}{{.Model}}{{else}}—{{end}}</span>
      {{end}}
    </div>
    <div class="controls-center">
      {{if or (eq .State "active") (eq .State "awaiting")}}
      <span class="running-indicator" data-running-indicator><span class="running-dot"></span>running{{if .RunningFor}} · {{.RunningFor}}{{end}}</span>
      {{end}}
    </div>
    <div class="controls-right">
      {{if .Capabilities.Interrupt}}
      <button type="button" class="input-btn input-btn-stop stop-btn" data-action-trigger="interrupt" data-capability-interrupt="{{.Capabilities.Interrupt}}" title="stop the in-flight turn"{{if and (ne .State "awaiting") (ne .State "active")}} disabled{{end}}>Stop</button>
      {{end}}
      {{if or .Capabilities.Steer .Capabilities.Send .Capabilities.Queue}}
      <button type="button" class="input-btn input-btn-ghost" data-steer-trigger data-capability-steer="{{.Capabilities.Steer}}" title="drain the queue as a steering message — or steer with the textarea text when the queue is empty"{{if or (not .Capabilities.Steer) (eq .ActiveTurnID "") (and (ne .State "awaiting") (ne .State "active"))}} disabled{{end}}>send as steer <kbd>⇧↵</kbd></button>
      {{end}}
      <button type="submit" class="input-btn input-btn-primary send-btn" data-capability-send="{{.Capabilities.Send}}" data-capability-queue="{{.Capabilities.Queue}}"{{if and (not .Capabilities.Send) (not .Capabilities.Queue)}} disabled title="send unavailable"{{end}}>send <kbd>⌘↵</kbd></button>
    </div>
  </div>
```

Note the running-indicator stays in `.controls-center` only when state is `active` or `awaiting`; otherwise the center zone is an empty `<div>` that still acts as the flex spacer. The input_status partial currently also renders a running indicator at the bottom — that duplicate is removed in Task 8, where the status row is rewritten.

- [ ] **Step 2: Rewrite `.input-controls` CSS to use three zones**

Edit `cmd/serf-hub/assets/style.css`. Replace the `.input-controls` and `.controls-spacer` rules (lines 405–406) with the three-zone layout. Keep the button rules (`.input-btn`, `.input-btn-stop`, etc.) but rename `.input-btn-stop` semantics in step 3.

```css
.input-controls {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: var(--space-3) 0 0;
  flex-wrap: wrap;
}
.controls-left,
.controls-right {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  flex-wrap: wrap;
}
.controls-center {
  flex: 1;
  display: flex;
  align-items: center;
  justify-content: flex-start;
  min-height: 1px; /* keep the row's baseline alignment even when empty */
}
```

Delete the old `.controls-spacer` rule entirely:

```css
/* DELETE: */
.controls-spacer { flex: 1; }
```

- [ ] **Step 3: Tighten button styling to spec roles**

The spec wires the three right-side buttons to canonical button variants: send → `.btn-primary`, stop → `.btn-danger`, steer → `.btn-ghost`. The current code uses `.input-btn .input-btn-primary / .input-btn-stop / .input-btn-ghost` — the `.input-btn-stop` class is effectively the danger variant under an old name. Until the Pass 4 button-rename lands, keep the existing class names; just make sure the visual fidelity matches the spec roles (red border + red text for stop; primary accent fill for send; muted color for steer ghost). The current rules already do this; verify and tweak if needed:

```css
.input-btn {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-4);
  background: var(--bg);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  color: var(--text);
  font: inherit;
  font-size: var(--text-sm);
  cursor: pointer;
}
.input-btn:hover { background: var(--bg-raised); }
.input-btn-ghost { color: var(--text-muted); }
.input-btn-stop {
  color: var(--state-awaiting);
  border-color: rgba(247,118,142,0.45);
  background: rgba(247,118,142,0.08);
}
.input-btn-stop:hover:not([disabled]) {
  background: rgba(247,118,142,0.14);
  border-color: rgba(247,118,142,0.65);
}
.input-btn-stop[disabled] { opacity: 0.45; cursor: not-allowed; }
.input-btn-primary {
  background: var(--state-processing);
  color: var(--btn-primary-text);
  border-color: transparent;
  font-weight: 500;
}
.input-btn-primary:hover { background: var(--state-processing); filter: brightness(1.1); }
.input-btn-primary kbd {
  background: rgba(0,0,0,0.2);
  border: 1px solid rgba(0,0,0,0.3);
  color: inherit;
  font-family: var(--font-mono);
  padding: 0 var(--space-2);
  border-radius: var(--radius-sm);
}
```

Two diffs from today's code:
1. Removed `margin-right: 10px` from `.input-btn-stop` — the three-zone layout owns inter-button spacing via the `.controls-right { gap: var(--space-3) }` rule.
2. Send button text color now resolves through `--btn-primary-text` (the spec's per-theme token introduced in Pass 1 §1.1).

- [ ] **Step 4: Update jstest JSDOM shells to match three-zone markup**

Edit `cmd/serf-hub/jstest/test-input-area.js`. Replace the controls block (lines 35–39):

Old:
```js
    <div class="input-controls">
      <button type="button" class="input-btn" data-attach-trigger>＋</button>
      <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>steer</button>
      <button type="submit" class="send-btn input-btn input-btn-primary">send</button>
    </div>
```

New:
```js
    <div class="input-controls">
      <div class="controls-left">
        <button type="button" class="input-btn" data-attach-trigger>＋</button>
      </div>
      <div class="controls-center"></div>
      <div class="controls-right">
        <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>steer</button>
        <button type="submit" class="send-btn input-btn input-btn-primary">send</button>
      </div>
    </div>
```

Make the equivalent change in `cmd/serf-hub/jstest/test-queue-and-drain.js` if its shell includes the `.input-controls` block. If it doesn't, no change needed.

- [ ] **Step 5: Build + run jstest**

```bash
go build ./cmd/serf-hub
cd cmd/serf-hub/jstest && npm test
```

Expected: all tests pass. The shape of the controls row changed but no JS selector references `.controls-spacer` (verified by grep — only the CSS rule used it).

- [ ] **Step 6: Manual visual check at desktop and phone width**

Open a session at desktop width: the controls row reads attach + model on the left, running indicator centered (when a turn is in flight), Stop + steer + send on the right.

Resize to phone width (< 768px). The row should wrap such that the send button stays reachable. The center zone collapses to its content (the running indicator) when present, or to nothing when not.

- [ ] **Step 7: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-input-area.js cmd/serf-hub/jstest/test-queue-and-drain.js
git commit -m "ui: split composer controls into left/center/right zones (pass 6)"
```

---

## Task 6: Reduce Queue Preview Visual Weight

**Files:**
- Modify: `cmd/serf-hub/templates/partials/workspace.html:40-46` (queue-preview block)
- Modify: `cmd/serf-hub/assets/style.css:367-376` (queue-preview rules)

Today the queue-preview header carries a long inline hint that includes a `kbd` chip explaining `⇧↵`. The hint dominates the strip visually. Replace it with a small `?` glyph that opens a native browser tooltip (`title` attribute) — same information, much less ink.

- [ ] **Step 1: Update workspace.html queue-preview markup**

Edit `cmd/serf-hub/templates/partials/workspace.html`. Replace the block at lines 40–46:

Old:
```html
  <div class="queue-preview" data-queue-preview hidden>
    <div class="queue-preview-header">
      <span class="queue-preview-label">queued <span data-queue-depth>0</span></span>
      <span class="queue-preview-hint">processes after current turn — <kbd>⇧↵</kbd> or "send as steer" drains as steering</span>
    </div>
    <ul class="queue-preview-list" data-queue-list></ul>
  </div>
```

New:
```html
  <div class="queue-preview" data-queue-preview hidden>
    <div class="queue-preview-header">
      <span class="queue-preview-label">queued <span data-queue-depth>0</span></span>
      <button type="button"
              class="queue-preview-help"
              aria-label="queue help"
              title="Queued messages process after the current turn. Press ⇧↵ or click &quot;send as steer&quot; to drain them as a steering message.">?</button>
    </div>
    <ul class="queue-preview-list" data-queue-list></ul>
  </div>
```

The button is keyboard-focusable so the hint is accessible via keyboard hover (focus shows the native tooltip on most browsers). `aria-label` makes it readable to screen readers; the `title` attribute carries the same info to sighted users on hover. No JS popover infrastructure needed.

- [ ] **Step 2: Update queue-preview CSS**

Edit `cmd/serf-hub/assets/style.css`. Replace the queue-preview rules (lines 367–376):

Old:
```css
.queue-preview { padding: 8px 10px; margin-bottom: 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.queue-preview-header { display: flex; align-items: baseline; gap: 12px; color: var(--text-muted); margin-bottom: 4px; }
.queue-preview-label { font-weight: 500; color: var(--text); }
.queue-preview-label [data-queue-depth] { font-family: ui-monospace, "SFMono-Regular", monospace; }
.queue-preview-hint { flex: 1; }
.queue-preview-hint kbd { font-family: ui-monospace, "SFMono-Regular", monospace; padding: 0 3px; background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 3px; }
.queue-preview-list { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 3px; }
.queue-preview-item { display: flex; gap: 6px; align-items: baseline; padding: 3px 6px; background: var(--bg-raised); border-radius: 3px; }
.queue-preview-item .qp-idx { color: var(--text-dim); font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 10px; }
.queue-preview-item .qp-text { color: var(--text); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
```

New:
```css
.queue-preview {
  padding: var(--space-3) var(--space-3);
  margin-bottom: var(--space-3);
  background: var(--bg);
  border: 1px solid var(--rule);
  border-radius: var(--radius-md);
  font-size: var(--text-xs);
}
.queue-preview-header {
  display: flex;
  align-items: baseline;
  gap: var(--space-3);
  color: var(--text-muted);
  margin-bottom: var(--space-2);
}
.queue-preview-label { font-weight: 500; color: var(--text); }
.queue-preview-label [data-queue-depth] { font-family: var(--font-mono); }
.queue-preview-help {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  padding: 0;
  background: transparent;
  border: 1px solid var(--rule);
  border-radius: var(--radius-full);
  color: var(--text-dim);
  font-family: var(--font-mono);
  font-size: 10px;
  line-height: 1;
  cursor: help;
}
.queue-preview-help:hover,
.queue-preview-help:focus-visible {
  color: var(--text);
  border-color: var(--text-muted);
  outline: none;
}
.queue-preview-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.queue-preview-item {
  display: flex;
  gap: var(--space-2);
  align-items: baseline;
  padding: 3px var(--space-3);
  background: var(--bg-raised);
  border-radius: var(--radius-sm);
}
.queue-preview-item .qp-idx {
  color: var(--text-dim);
  font-family: var(--font-mono);
  font-size: var(--text-2xs);
}
.queue-preview-item .qp-text {
  color: var(--text);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  flex: 1;
}
```

The old `.queue-preview-hint` rule (with its `kbd` styling) is gone — no more inline hint text.

- [ ] **Step 3: Build + verify the queue preview by queuing during an active turn**

```bash
go build ./cmd/serf-hub
```

Open a session, start a turn, then type a message and press ⌘↵. The message queues. The queue preview strip should show `queued 1` on the left, the `?` glyph on the right (or just after the label), and the queued items below. Hover the `?` — the browser tooltip should show the explanation. Tab to the `?` with the keyboard — it should focus visibly via `:focus-visible`.

- [ ] **Step 4: Commit**

```bash
git add cmd/serf-hub/templates/partials/workspace.html cmd/serf-hub/assets/style.css
git commit -m "ui: reduce queue preview to label + ? glyph (pass 6)"
```

---

## Task 7: Restyle Composer Status Row as Mono Single Line With Semantic Key/Value Spans

**Files:**
- Modify: `cmd/serf-hub/templates/partials/input_strip.html` (full file)
- Modify: `cmd/serf-hub/assets/style.css:422-430` (`.input-status` rules)

The status row today reads `[cwd] · [branch] · [running] · [context bar] · [cost]`, with the running indicator duplicated from the controls row above. The spec wants a single mono line of `cwd · branch · ctx · cost` with explicit `.status-key` (dim) + `.status-value` (text) spans, and running stays only in the controls row (Task 5's center zone).

- [ ] **Step 1: Rewrite the input_status template**

Edit `cmd/serf-hub/templates/partials/input_strip.html`. Replace the entire `{{define "input_status"}}` block:

Old:
```html
{{define "input_status"}}
{{if .WorkingDir}}<span class="cwd" title="{{.WorkingDir}}">{{.WorkingDir}}</span>{{end}}
{{if and .WorkingDir .Branch}}<span class="rule-dot">·</span>{{end}}
{{if .Branch}}<span class="branch">{{.Branch}}</span>{{end}}
{{if or (eq .State "active") (eq .State "awaiting")}}{{if or .WorkingDir .Branch}}<span class="rule-dot">·</span>{{end}}<span class="running-indicator" data-running-indicator><span class="running-dot"></span>running{{if .RunningFor}} · {{.RunningFor}}{{end}}</span>{{end}}
<span class="status-spacer"></span>
{{if .ContextWindow}}<span class="context"><span class="context-label">context</span><span class="context-bar"><span class="context-fill" style="width:{{.ContextPercent}}%"></span></span><span class="context-numbers">{{.ContextNumbers}}</span></span>{{end}}
{{if .Cost}}<span class="rule-dot">·</span><span class="cost">{{.Cost}}</span>{{end}}
{{end}}
```

New:
```html
{{define "input_status"}}
{{if .WorkingDir}}<span class="status-item cwd" title="{{.WorkingDir}}"><span class="status-key">cwd</span> <span class="status-value">{{.WorkingDir}}</span></span>{{end}}
{{if .Branch}}<span class="status-item branch"><span class="status-key">branch</span> <span class="status-value">{{.Branch}}</span></span>{{end}}
{{if .ContextWindow}}<span class="status-item context"><span class="status-key">ctx</span> <span class="status-value context-numbers">{{.ContextNumbers}}</span><span class="context-bar"><span class="context-fill" style="width:{{.ContextPercent}}%"></span></span></span>{{end}}
{{if .Cost}}<span class="status-item cost"><span class="status-key">cost</span> <span class="status-value">{{.Cost}}</span></span>{{end}}
{{end}}
```

Three changes:
1. Each metric is a `.status-item` containing a `.status-key` (label) + `.status-value` (data). The key is dim, the value is text — the contrast carries the meaning so no `·` separators are needed (gap + color does the work, per the design spec §4.1 mono-cluster pattern).
2. The running indicator is removed — it lives in the controls row's `.controls-center` zone now (Task 5).
3. The `<span class="status-spacer">` is gone; spacing is via gap on the parent.

- [ ] **Step 2: Rewrite `.input-status` CSS**

Edit `cmd/serf-hub/assets/style.css`. Replace the rules at lines 422–430:

Old:
```css
.input-status { display: flex; align-items: center; gap: 14px; padding: 10px 0 0; margin-top: 6px; border-top: 1px solid var(--rule); font-size: 11px; color: var(--text-muted); flex-wrap: wrap; }
.input-status .cwd, .input-status .branch { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cwd { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-status .status-spacer { flex: 1; }
.input-status .context { display: inline-flex; align-items: center; gap: 6px; }
.input-status .context-bar { width: 80px; height: 3px; background: var(--bg); border-radius: 2px; overflow: hidden; }
.input-status .context-fill { display: block; height: 100%; background: var(--state-processing); }
.input-status .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cost { font-family: ui-monospace, "SFMono-Regular", monospace; }
```

New:
```css
.input-status {
  display: flex;
  align-items: center;
  gap: var(--space-5);
  padding: var(--space-3) 0 0;
  margin-top: var(--space-2);
  border-top: 1px solid var(--rule);
  font-family: var(--font-mono);
  font-size: var(--text-xs);
  color: var(--text);
  flex-wrap: wrap;
}
.input-status .status-item {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  white-space: nowrap;
}
.input-status .status-key {
  color: var(--text-dim);
}
.input-status .status-value {
  color: var(--text);
}
.input-status .cwd .status-value {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.input-status .context-bar {
  display: inline-block;
  width: 80px;
  height: 3px;
  background: var(--bg);
  border-radius: var(--radius-sm);
  overflow: hidden;
}
.input-status .context-fill {
  display: block;
  height: 100%;
  background: var(--state-processing);
}
```

Notes:
- The whole row is mono (`var(--font-mono)`) — every metric reads as a machine value.
- `gap: var(--space-5)` (16px) between items is wider than the current 14px gap so the no-separator pattern still parses; the eye groups within an item by the smaller `var(--space-2)` (4px) intra-item gap.
- `.status-key` uses `--text-dim` (the dimmest text token); `.status-value` uses `--text` (full strength). The contrast carries semantics.
- `flex-wrap: wrap` keeps the row single-line until it has to break — exactly the spec's "stays single-line until it has to wrap" line.

- [ ] **Step 3: Confirm no other partial expects the old running-indicator in input_status**

```bash
grep -rn "input-status\|status-spacer\|class=\"running-indicator\"" cmd/serf-hub
```

Expected hits:
- `cmd/serf-hub/templates/partials/workspace.html` — running indicator in `.controls-center` (Task 5).
- `cmd/serf-hub/templates/partials/workspace.html` — `id="input-status"` hx-get hook (unchanged).
- `cmd/serf-hub/assets/style.css` — the new `.input-status` rule.
- No hit on `status-spacer` (now gone).

If `.running-indicator` shows up in `input_strip.html`, the template edit in Step 1 missed it — revisit.

- [ ] **Step 4: Build + load a session and watch the status row**

```bash
go build ./cmd/serf-hub
```

Open a workspace. The status row should read `cwd <path>   branch <name>   ctx [bar] <numbers>   cost <amount>`, all in mono, with dim keys and full-strength values. No `·` separators. When a turn is active, the running indicator appears in the controls row above, NOT in the status row.

Resize the workspace narrow enough to force a wrap. The row should wrap at the gap, not within an item — each `cwd / branch / ctx / cost` group stays together (because `.status-item` is `white-space: nowrap`).

- [ ] **Step 5: Verify htmx swap still works**

The status row is updated via `hx-get="/_partials/s/{{.ID}}/state"` every 2s. Watch the row for 5 seconds — the values should refresh without flicker; the structure (key + value spans) should be preserved across swaps.

- [ ] **Step 6: Commit**

```bash
git add cmd/serf-hub/templates/partials/input_strip.html cmd/serf-hub/assets/style.css
git commit -m "ui: mono single-line composer status row with key/value spans (pass 6)"
```

---

## Task 8: Final Build, Cross-Theme Verification, and Functional Smoke

**Files:** none (verification-only)

- [ ] **Step 1: Full build**

```bash
go build ./...
```

Expected: clean build. No new lints or vet warnings.

- [ ] **Step 2: Run all jstest suites**

```bash
cd cmd/serf-hub/jstest && npm test
```

Expected: all suites pass. If anything in test-input-area or test-queue-and-drain fails, recheck the JSDOM shell adjustments from Tasks 4 and 5.

- [ ] **Step 3: Go tests for any web/template tests that touch the composer**

```bash
cd /home/jesse/git/prime-radiant/serf && go test ./cmd/serf-hub/... -run "Workspace|Composer|Input|Status|Attachment"
```

Expected: pass. If a Go-side template smoke test fails because it greps for `.input-attachments` or `.controls-spacer` in the rendered HTML, update the assertion to match the new markup (the class is now `.composer-attachments` / `.controls-left/center/right`).

- [ ] **Step 4: Manual smoke checklist — desktop dark theme**

Open the hub with `data-theme="dark"` (default). Open or start a session and step through:

- Conversation: padding looks like 16/24 (tighter horizontally than before). User pill caps at ~540px even on a wide monitor.
- Tool-call cluster: status glyph sits in the gutter to the left, indent reads as 36px.
- Diff body: inset darker surface, no border-left, rounded corners. Long lines scroll horizontally.
- Composer attachment rail: paste an image — chip appears in one row above the textarea.
- Composer controls: attach + model on left, running indicator centered (during active turn), Stop + steer + send on right. All three buttons are separate.
- Queue preview: type-and-send during an active turn. Strip shows `queued N` + `?` glyph; hover the glyph to see the tooltip.
- Status row: single mono line, dim keys + full-strength values. No `·` separators. No second running indicator.

- [ ] **Step 5: Manual smoke checklist — desktop light theme**

Toggle `data-theme="light"`. Repeat Step 4. Pay attention to:
- Send button text resolves through `--btn-primary-text` (cream-on-blue, per spec §1.1).
- Diff body inset surface is visible (slightly different from page bg).
- Queue preview `?` glyph contrast holds against light page bg.

- [ ] **Step 6: Manual smoke checklist — phone width**

Resize the browser to 380px wide (DevTools device mode is fine). Verify:
- Conversation padding drops via container query (sub-600px); tool-cluster indent narrows.
- Send button is visible and reachable in the controls row.
- Diagnostic block fits within the conversation pane without horizontal scroll.
- Status row wraps cleanly at the gap (each item stays whole).

- [ ] **Step 7: Manual smoke checklist — drag-drop + file picker**

Drag an image from the desktop onto the input card. Chip appears in the merged attachment rail. Then click the `＋` attach button, pick an image from the file dialog. Second chip appears. Both render in the same `.composer-attachments` container.

- [ ] **Step 8: Final commit (only if any final fixups were needed)**

```bash
git status
# If anything modified during verification, commit:
git add -p
git commit -m "ui: pass 6 verification fixups"
```

If nothing changed, no commit. Note in the rollout message that Pass 6 is verified.

---

## Self-Review Notes

- **Spec coverage:** Every bullet in Pass 6 of the responsive design spec has a task. Conversation padding/line-height/gap (Task 1). Tool-indent custom property + container query (Task 2). Diff body surface-inset + horizontal scroll (Tasks 2+3 — combined to avoid double-editing the same rule). Diagnostic max-width promotion (Task 2 Step 5). Attachment rail merge (Task 4). Composer three-zone controls (Task 5). Queue preview `?` glyph (Task 6). Mono single-line status row (Task 7). Final verification (Task 8).
- **Class-name reality:** Plan uses the actual class names (`.user-message`, `.assistant-message`, `.tool-call-cluster`, `.tool-call .tool-status`) rather than the spec shorthand. Confirmed against the current `style.css`.
- **Token prerequisites:** Plan assumes `--space-N`, `--leading-snug`, `--text-md`, `--text-sm`, `--text-xs`, `--text-2xs`, `--radius-md`, `--radius-lg`, `--radius-pill`, `--radius-sm`, `--radius-full`, `--font-mono`, `--font-sans`, `--surface-secondary`, `--btn-primary-text` exist. These are defined in Passes 1–3 of the broader migration. If those tokens are not yet shipped, this plan must be paused until they are, or each token reference resolved to its raw value.
- **No placeholders:** Every CSS block is complete and copy-paste-ready. Every template snippet is the full element. Every command is exact.
