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

## Simplify and publication follow-up (2026-09-15)

Jesse's request: “do that. once that’s done, please open a pr and then shepherd it with”. His subsequent choices were “Rebase the feature onto main” and “60 observations, 120 seconds apart”. Publication and bounded shepherding are authorized; merging into main remains unauthorized.

- Four read-only reviews covered reuse, simplification, efficiency and abstraction level. Applied five behavior-preserving changes: schema-based suffix extraction, explicit offset branches, `strings.Cut` carrier extraction, schema-owned clipboard formatting, and reuse of the parsed external replacement with fresh editor state. Each passed its focused check; all 21 editor/model tests passed together. No added API or assertion changed.
- Skipped the draft-helper rewrite because it changes return identity, and serialization caching because mutable values would change callback/echo ownership or require extra defensive state. No simplification was reverted.
- Rebased onto `1988c5b5a68e67400186e9c76e153a5ce13b57e9`. The single import conflict retained both upstream and feature helpers. Independent review found no lost behavior.
- The full gate exposed a provider-dialog focus bug reproduced on both base and feature: a delayed listing refresh unmounted its focused row. A dialog-local fix retains existing rows during refresh. Deterministic tests also await the permitted store-owned refresh before asserting no further work. All 52 dialog tests and 458 related tests passed; independent review found no weakened assertions or changed endpoint guards.
- Browser guards exposed a Linux desktop-keyring dependency before the first HTTP request. A real Chrome comparison confirmed that the built-in key store restores navigation in a disposable profile. The Linux-only test-launcher setting passed its regression, all 64 launcher/CDP tests, all six real-browser guards, and independent review. Ordinary browser profiles and application credential storage are unchanged.
- Final integrated verification exited zero: `GOMAXPROCS=2 make merge-approval-gate && GOMAXPROCS=2 make vet && GOMAXPROCS=2 make test-web-browser`. Lint, production build, all seven Go modules, frontend gate, native bundle, 746 native tests, 778 shared tests, native typecheck, AppWire package qualification, vet and all six actual browser guards passed.
- The successful run emitted Node SQLite experimental warnings, a Metro cold-cache warning and an npm update notice. These are recorded rather than described as warning-free output. PR creation and bounded shepherding follow the simplification commit; merging into main remains unauthorized.

The native dependency install also reported 14 moderate audit entries rooted in `decode-uri-component` and `uuid`. Those lockfiles are unchanged from main; I haven’t applied dependency upgrades.

## Published-branch rebase (2026-09-16)

- Opened [PR #1410](https://github.com/prime-radiant-inc/evener/pull/1410). Main advanced during verification; Jesse authorized “Rebase and force-with-lease”. Rebased onto `597d4e09b0bdffa93dad8e94e52e49d9193425f5` without changing main.
- Resolved two test conflicts by retaining the upstream AppWire imports, inline-editor helpers, named auth/write assertions and deterministic refresh assertions. Both resolved suites passed all 104 tests, typecheck and Biome. Independent review found no lost assertions or behavior.
- The new package-import gate caught a feature-added filesystem-path import. Switched it to the identical public AppWire export. The previously failing gate, all 21 editor/model tests, typecheck, Biome and independent review passed; committed as `2a4c2572a42aaa0198837ba3ec3a43f95c1f461f`.
- Reran the complete canonical gate, vet and all six actual browser guards on that committed source; the command above exited zero. This run included 757 native tests, 778 shared tests and native script-import checks. The same SQLite, cold-cache and npm notices remain disclosed.
- Hosted checks, required approval and bounded shepherding remain separate from local verification. The authorized push uses an explicit lease on the original published tip; merging into main remains unauthorized.

## Review repair and merge authorization (2026-09-16)

Jesse subsequently authorized resolving findings above low, filing low-severity follow-ups and merging subject to repository requirements. He approved “Proceed with repair and rebase”, then chose “Keep and finish” after the whole-PR scope review. These choices supersede the earlier no-merge instruction; protection bypass remains prohibited.

- Rebased onto `726d31c2db81dde3582d91468bd157e771de5338` without conflicts. All ten rebased feature patches were equal in range-diff.
- Normalize skill selections at initial draft restoration, draft subscription updates, automatic recovery and explicit recovery activation. Use the existing document parser and serializer to drop metadata lacking a complete visible canonical reference. Preserve prose, attachments and visible repeated references; add no position reconstruction, storage migration or protocol change.
- Five regression failures established the incoming hidden-selection and empty-sendability defect before the fix. Four focused suites then passed all 257 tests, including persistence and exact recovery payload checks; Biome and typecheck passed.
- Four read-only angles reviewed the entire PR. Jesse retained the provider-dialog and Linux browser-profile supporting fixes. Applied only two additional behavior-preserving cleanups: reuse the restoration helper for attachment text writes and reuse the mapped position for collapsed selections. Existing assertions and added API signatures are unchanged. The same 257-test focused gate passed afterward.
- Independent final correctness review found no findings and confirmed preservation of all ten rebased patches. The complete canonical gate, vet and six real-browser guards exited zero on the final source. This includes lint, build, all seven Go modules, frontend tests/typecheck/Biome, native bundle, 757 native tests, 778 shared tests, native checks and AppWire qualification. The previously disclosed SQLite, Metro and npm warnings remain.
- Publish with an explicit lease on `d90e8704043f3abc2f9092195cf7d6b6114bb0b7`. Current-head hosted CI and RoboRev, the two low-severity follow-up tickets, required approval and an up-to-date branch remain prerequisites to merge.
