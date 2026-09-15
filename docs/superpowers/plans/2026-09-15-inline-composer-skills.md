# Inline Composer Skills Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Keep indivisible skill chips in their sentence positions and send the complete sentence with explicit, deduplicated skill activation metadata.

**Architecture:** Scope a ProseMirror editor to the session composer. Its document contains only text (including literal newlines) and inline atomic skill nodes. Serialize nodes as `/canonical-name`; retain the existing text-plus-skillNames persistence and wire contracts. Restore complete canonical references to selected skills as atoms; never infer positions for old detached drafts. The editor owns selection, clipboard, IME and undo history. Composer retains submission, attachments, routing and recovery ownership.

**Tech Stack:** React, TypeScript, ProseMirror model/state/view/history/keymap/commands, Vitest, existing browser guard harness.

**Spec:** Jesse's approved conversation: retain `Run /skill-1 and then /skill-2`, chips inline, explicit activation metadata, current draft/queue/recovery lifecycle preserved; work in `wip/inline-composer-skills`; skip recovery for old sessions; indivisible chips explicitly chosen on 2026-09-15.

## Global Constraints

- Clean, simple, straightforward. No old-session migration or protocol change.
- Read `docs/developing-evener/testing.md` before changing tests. TDD for every behavior change.
- Preserve submission ownership safeguards and unrelated command/attachment behavior.
- Keep text and metadata synchronized through atomic deletion, selection replacement, undo/redo and programmatic edits.
- Preserve repeated references; deduplicate only activation names. A restored selected name recognizes all complete canonical references to that name; unselected slash prose remains text.
- No HTML injection or arbitrary rich formatting on paste. Chip clipboard data must contain canonical names and their displayed text.
- No fabricated textarea properties or mocked editor in tests. Exercise the actual DOM/editor.

---

## Task 1: Atomic skill editor

**Files:** Create `cmd/evener-hub/frontend/src/panes/session/composer/SkillEditor.tsx`, `skillDocument.ts`, `skillDocument.test.ts`, `SkillEditor.test.tsx`, `skilleditor.module.css`. Modify frontend `package.json` and `package-lock.json` for the six ProseMirror packages.

**Interfaces:**
```ts
export interface SkillEditorValue { text: string; skillNames: string[] }
export interface SkillEditorHandle {
  focus(): void;
  getCursor(): number; // UTF-16 offset in serialized text
  setSelection(start: number, end?: number): void;
  insertSkill(start: number, end: number, name: string): void;
}
// SkillEditor props: value: SkillEditorValue;
// onChange(value: SkillEditorValue, cursor: number): void;
// onKeyDown(event: React.KeyboardEvent<HTMLDivElement>): void;
// onPaste(event: React.ClipboardEvent<HTMLDivElement>): void;
// onFocus/onBlur; placeholder; minLines; aria-label;
// aria-controls; aria-activedescendant; skillDetails(name: string): string.
```

- [x] Add red tests proving text/atom serialization, repeated mentions with dedup metadata, canonical boundary matching, unknown slash text, UTF-16 offsets around atoms and literal newlines.
```ts
expect(serializeSkillDocument(parseSkillDocument({
  text: "Run /skill-1 and then /skill-2 and /skill-1",
  skillNames: ["skill-1", "skill-2"],
}))).toEqual({
  text: "Run /skill-1 and then /skill-2 and /skill-1",
  skillNames: ["skill-1", "skill-2"],
});
```
- [x] Run `./node_modules/.bin/vitest run --maxWorkers=1 src/panes/session/composer/skillDocument.test.ts` and confirm the expected missing implementation failure.
- [x] Implement a minimal schema (doc inline*, text, skill atom), serialization and text/ProseMirror offset mapping. Use canonicalSkillNames for activation deduplication.
- [x] Add editor tests for completion insertion at a range, single-action Backspace/Delete, selection replacement, undo/redo restoring both content and names, multiline/plain paste, external replacement, focus/selection and composition guards. Run them red before implementing their editor behavior.
- [x] Implement the React wrapper using ProseMirror transactions/history. Preserve internal state when controlled props merely echo its output. External replacements reconcile complete selected tokens and do not accidentally revive submitted content through history. Keep Enter and paste callbacks available to Composer before editor defaults. Atoms expose skill details accessibly and render inline with existing design tokens.
- [x] Run both new test files, frontend typecheck and explicit-path Biome. Record commands/results and commit named files only.

## Task 2: Composer integration and current draft lifecycle

**Files:** Modify `Composer.tsx`, `Composer.test.tsx`, `Composer.integration.test.tsx`; add a shared DOM testing helper under `panes/session/testing/` only if repeated test operations require it. Update touched composer documentation/comments.

**Consumes:** SkillEditor value/handle contract from Task 1. Existing TextEditor attachment adapter remains text plus cursor.

- [x] Replace the regression that expects deletion with the approved two-skill sentence and run it red:
```ts
// Type Run /skill-1, choose it, type and then /skill-2, choose it.
expect(readComposerDraft(ref)).toEqual({
  text: "Run /skill-1 and then /skill-2 ",
  skillNames: ["skill-1", "skill-2"],
});
// Assert both atomic chips are children of the Message textbox.
// Press Backspace once to remove the completion's trailing separator.
// Submission preserves the resulting text exactly; it does not trim it.
// Submit through the real Composer and FakeClient network boundary.
expect(call.params.input).toEqual([
  { type: "text", text: "Run /skill-1 and then /skill-2" },
  { type: "skill", name: "skill-1" },
  { type: "skill", name: "skill-2" },
]);
```
- [x] Swap only the composer field for SkillEditor. Remove the detached chip row and chip-only remove handler. In onChange, update skill metadata before persisting text. Commit skill completions through `insertSkill(start, end, canonicalName)` as one undoable transaction. Commands continue their existing textual splice.
- [x] Adapt focus and attachment cursor operations to getCursor/setSelection. Pass completion accessibility attributes as props. Keep recovery/submission guards and explicit-skill command bypass.
- [x] Port textarea-specific test operations to real contenteditable DOM selection/input. Preserve each behavioral assertion except the explicitly superseded detached-chip contract.
- [x] Add current draft remount, repeated-reference deletion, queue edit, failed recovery, steer/drain and leading builtin-name collision regressions with inline sentence assertions and canonical metadata assertions.
- [x] Run `./node_modules/.bin/vitest run --maxWorkers=1 src/panes/session/composer/Composer.test.tsx src/panes/session/composer/Composer.integration.test.tsx` and repair root causes. Commit the integration after passing.

## Task 3: Backend and browser evidence, review

**Files:** Extend `agent/session_client_mutation_queue_test.go` only for exact two-skill provider/transcript input regression; extend the existing browser harness with real editor interaction coverage, including a mounted actual SkillEditor/Composer (no mocked editor).

- [x] Add/run the backend two-skill regression, using the existing scripted provider boundary. Assert the original input text occurs intact in provider input and transcript user data, and selection metadata contains both canonical names. Confirm existing backend behavior passes without production changes.
- [x] Run a real browser scenario that inserts two chips, navigates around them, deletes either direction, undoes/redoes, selects/replaces, copies/pastes, inserts newlines, wraps at narrow width and scrolls a long message. Confirm IME Enter cannot submit. Use actual DOM geometry to assert indivisible chips stay inside the editor.
- [x] Run explicit-path `npx biome check --write` on touched frontend source, then `make test-web` and `make test-web-browser`. Run focused Go tests with `-count=1` and gofmt on touched Go tests.
- [x] Obtain independent review against Jesse's requested sentence and indivisible-chip semantics. Resolve findings through targeted red/green checks.
- [x] Verify git diff/status and every acceptance criterion. Commit named paths with hooks enabled. Report commit, tests and any incomplete verification; do not merge or push.

## Acceptance record (2026-09-15)

- `make test-web`: passed unit tests, typecheck and Biome.
- `npm run build` and `GOMAXPROCS=2 make test-web-browser`: passed the production build and all six browser guards. The skill guard verifies native atomic editing, IME, layout, draft/queue/recovery behavior, and exact provider/transcript text with activation metadata.
- `GOMAXPROCS=2 WEB=0 make test`: passed all seven Go modules; the frontend gate ran separately above.
- `GOMAXPROCS=2 make vet && GOMAXPROCS=2 make lint`: passed on final source.
- Focused backend and browser assertion tests passed with `-count=1`. Independent review findings were resolved with targeted regression tests, including mixed image/text paste, editor height/scrolling, and stale cursor restoration after same-text edits.
- Scope remains the approved inline-chip behavior, with existing persistence and wire contracts. No old-session migration, push or merge to main.
