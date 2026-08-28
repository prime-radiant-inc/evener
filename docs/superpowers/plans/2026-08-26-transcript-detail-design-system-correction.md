# Transcript Detail Design-System Correction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the transcript feature's private control chrome with first-class shared widgets while preserving explicit Custom identity, existing transcript behavior, and exact responsive accessibility contracts.

**Architecture:** A new generic `SegmentedControl` and an extended controlled/disabled `Disclosure` establish the shared design-system layer before any feature migration. The transcript domain then preserves Custom kind across flat frontend and nested wire codecs; the editor, live Popover/Sheet, and Settings cards compose shared widgets with layout-only feature CSS. Production-backed browser guards prove the exact 320px/390px geometry, Popover scroll behavior, preview sizing, and visual acceptance matrix.

**Tech Stack:** TypeScript 6, React 19, Zustand 5, Vitest 4, Testing Library, CSS Modules, Evener shared widgets/tokens, Vite, and headless Chrome/CDP browser guards.

**Spec:** `docs/superpowers/specs/2026-08-26-transcript-detail-design-system-correction-design.md`

## Global Constraints

- Read `docs/developing-evener/testing.md` before changing tests. Keep default tests deterministic and independent of provider credentials, network access, quota, current model behavior, ambient machine state, fixed sleeps, and polling races.
- Use strict TDD in every task: write the smallest failing behavior or contract test, run it and confirm the named failure, implement the minimum behavior, then rerun the focused suite.
- Use a fresh `gpt-5.6-luna` implementation subagent for each task. Tasks 1–3 must be implemented, reviewed, committed, and accepted before Task 4 starts feature migration.
- Do not add dependencies, AppWire fields/methods, durable-schema changes, backend changes, new breakpoints, theme-specific props, styling escape hatches, or unrelated widget migrations.
- The frontend `ContentSelection` stays flat. The wire/local durable Custom payload stays nested as `{kind:"custom",custom:{toolIntent,toolCalls,reasoning,expandByDefault}}`.
- Only `{kind:"preset",level:"full"}` starts the Full reset baseline. A Custom vector equal to Full renders the same content but does not clear explicit disclosure choices.
- SegmentedControl accepts 2–6 concise options, remains one row, and never wraps or scrolls horizontally. At 320px in Settings, the track is 256px, each option is 40px wide and at least 44px high, and visible labels may ellipsize while accessible names remain complete.
- Mobile remains `(max-width: 899px)`. The editor's only narrow-layout threshold is its named `transcript-detail-editor` container at `max-width: 34rem`.
- Shared widgets own chrome. Feature CSS may arrange them but may not select widget roles/descendants, change Switch geometry, duplicate Card/Popover chrome, add wildcard motion overrides, or apply decorative accent bars.
- Shared Switch remains a 32×18 visual track. Shared Button, Card, Select, Disclosure, Popover, and Sheet retain their public styling contracts.
- Before frontend gates, run `npx biome check --write` on every touched file under `cmd/evener-hub/frontend/src/`. Do not run Biome over out-of-scope harness HTML under `scripts/`.
- Stage named paths only. Never use `git add .` or `git add -A`. Implementers do not edit this plan's checkboxes; the controller records accepted-task progress.
- Store corrected screenshots under `.superpowers/sdd/2026-08-26-transcript-detail-design-system-correction/proof/`, separate from the original feature proof and defect captures. This is ignored evidence, not production source; every listed feature state must be reviewed in both light and dark themes.

### Per-task controller protocol

For Tasks 1–7, the controller follows this sequence; the final commit step in each task is not delegated to the implementer:

1. Record `repo_root=$(git rev-parse --show-toplevel)`, current HEAD, and `git -C "$repo_root" status --short`.
2. Give one fresh Luna implementer only that task's allowed paths and acceptance steps. Luna writes the failing tests, captures the named RED, makes the minimum implementation, runs GREEN, formats touched `src/` files, and reports the unstaged diff without committing.
3. Send the unstaged diff to a separate specification-compliance reviewer and a separate code-quality reviewer. Neither reviewer edits.
4. Return every finding to the same task's Luna implementer for a focused RED/GREEN fix, then rerun both reviews. Two incomplete review/fix cycles trigger a controller checkpoint instead of a third blind loop.
5. The controller reruns the task's focused GREEN commands, verifies the named path list, then executes that task's exact `git -C "$repo_root" add --` path list and commit command.

After Task 3 passes both reviews, the controller launches `/dev/widgets` and obtains explicit human approval of the SegmentedControl and disabled Disclosure galleries in light and dark before dispatching Task 4. This is the gallery-first migration gate; Task 8 repeats the gallery review as final acceptance evidence.

Every shell block is self-contained. It resolves `repo_root=$(git rev-parse --show-toplevel)` afresh, changes to `"$repo_root/cmd/evener-hub/frontend"` for frontend commands, and uses `git -C "$repo_root"` with root-relative named pathspecs for staging, commits, diffs, and status.

---

## File Structure

### New shared-widget files

- `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/index.tsx` — validated generic API, ARIA radio semantics, roving focus, and controlled selection.
- `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css` — inset-neutral track, complete option geometry, density, focus, disabled, ellipsis, and Mobile height-only behavior.
- `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.test.tsx` — API validation, semantics, keyboard/focus, disabled, CSS, and responsive contracts.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.tsx` — every documented SegmentedControl state under `ThemeFlip`.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.module.css` — gallery frame layout only, including 320px and 390px frames.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.test.tsx` — gallery-state and both-theme coverage.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.test.tsx` — controlled/store-backed disabled gallery coverage.
- `cmd/evener-hub/frontend/src/transcriptDisplay/designSystemContract.test.ts` — path-scoped static contract rejecting private widget skins and duplicate feature chrome.

### Modified shared-widget and documentation files

- `cmd/evener-hub/frontend/src/widgets/index.ts` — export SegmentedControl and the now-documented Disclosure API.
- `cmd/evener-hub/frontend/src/widgets/disclosure/index.tsx` — discriminated store-backed/controlled API and shared disabled behavior.
- `cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.module.css` — summary-only disabled styling and hover suppression.
- `cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.test.tsx` — controlled rerenders, one-event behavior, disabled modes, and legacy regression coverage.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.tsx` — disabled collapsed store-backed and disabled open controlled examples.
- `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.module.css` — gallery arrangement only if the new examples need it.
- `docs/web-ui/design-system.md` — locked inventory rows for SegmentedControl and Disclosure.

### Modified transcript domain and feature files

- `cmd/evener-hub/frontend/src/transcriptDisplay/config.ts` — preserve explicit Custom kind while retaining strict flat/nested codecs and kind-aware fingerprints.
- `cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts` — preset-equivalent Custom, codec, summary, local encoding, and fingerprint tests.
- `cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts` — preset-equivalent Custom renders from its vector.
- `cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.test.tsx` — named-Full-only baseline transition/remount/manual-collapse tests.
- `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts` — local/hub acknowledgement and persistence retain explicit Custom kind.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.tsx` — six segments, mounted Custom cache, controlled Disclosure, and shared Select/FormRow/Switch composition.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.test.tsx` — restore-or-clone, remount, disclosure, shared composition, disabled, and copy tests.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx` — shared Button trigger/actions, exact dialog semantics, Popover scroll opt-out, and status/alert mapping.
- `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx` — live summary, ARIA, Popover/Sheet, scroll, announcements, and composition tests.
- `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css` — editor/live layout only; all private widget chrome is removed.
- `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx` — semantic article wrapping shared Card, one selected-state owner, and named Mobile preview canvas.
- `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.test.tsx` — shared composition, neutral preview/inventory, exact canvas ownership, and no-inner-scroll seams.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css` — Card-content, preview, inventory, status, and responsive layout only.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx` — one intro and exact status/alert/Toast behavior.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.test.tsx` — success/failure announcement and no-duplicate-Toast coverage.
- `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.module.css` — section stack/status layout without decorative chrome.

### Modified browser-guard and tracking files

- `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx` — production-backed segmented/editor/preview measurement seams.
- `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs` — exact 320/390/899/900/narrow-dock/1024/1400 assertions and Popover-scroll interaction.
- `docs/superpowers/plans/2026-08-26-transcript-detail-design-system-correction.md` — controller-owned completion checkboxes after all gates pass.

### Deliberately unchanged

- `appwire/`, generated protocol files, hub persistence/RPC, projector production rules, transcript-store precedence/synchronization, unrelated RadioGroup consumers, and unrelated Settings sections.
- `cmd/evener-hub/frontend/src/widgets/disclosure/disclosureStore.ts` unless a focused failing regression proves a store defect. Controlled Disclosure bypasses the store; it does not redesign it.

---

### Task 1: Preserve explicit Custom identity and named-Full semantics

**Files:**
- Modify: `cmd/evener-hub/frontend/src/transcriptDisplay/config.ts:144-176,297-330,418-490`
- Modify: `cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts:59-131`
- Modify: `cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts`
- Modify: `cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts`

**Interfaces:**
- Preserves existing `ContentSelection`, `normalizeContent`, `normalizeConfig`, `toWireConfig`, `fromWireConfig`, `encodeLocalConfig`, `decodeLocalConfig`, and `configFingerprint` signatures.
- Produces kind-authoritative Custom values for Tasks 4–7.
- Preserves the nested wire/local shape and the existing named-Full check in `TranscriptRenderProvider`.

- [x] **Step 1: Replace the old canonicalization expectation with failing Custom-identity tests**

In `config.test.ts`, replace the test that expects exact Custom vectors to become presets. Cover every preset-equivalent vector, the nested wire/local round trip, summaries, and kind-aware fingerprints:

```ts
const LEVELS = ["chat", "intent", "tools", "activity", "full"] as const;

test.each(LEVELS)("preserves explicit Custom identity when its vector equals %s", (level) => {
  const vector = presetContent(level);
  const custom = makeTranscriptDisplayConfig({ kind: "custom", ...vector });
  const preset = makeTranscriptDisplayConfig({ kind: "preset", level });

  expect(custom.content).toEqual({ kind: "custom", ...vector });
  expect(toWireConfig(custom).content).toEqual({ kind: "custom", custom: vector });
  expect(fromWireConfig(toWireConfig(custom))).toEqual(custom);
  expect(decodeLocalConfig(encodeLocalConfig(custom))).toEqual(custom);
  expect(configFingerprint(custom)).not.toBe(configFingerprint(preset));
  expect(contentSummary(custom.content)).toBe("Custom");
});
```

Also retain strict rejection tests for missing/extra nested Custom fields and invalid booleans.

- [x] **Step 2: Add failing persistence, projection, and Full-baseline transition tests**

In `src/stores/transcriptDisplay.test.ts`, persist a Tools-equivalent Custom through `setLocal`, local storage, and a canonical hub response; assert every resulting `content.kind` is `"custom"`. In `projector.test.ts`, compare the projected entries for a named preset and equivalent Custom vector while asserting the input kind remains Custom.

In `renderContext.test.tsx`, make the existing probe mirror production fallback resolution exactly:

```tsx
const fallback =
  expandDetailsByDefault(context.config) || disclosureDefault(context.disclosureScope, id, false);
```

Then reuse `DisclosureProbe`, `scopedDisclosureId`, and rerender helpers to pin these transitions:

```tsx
const equivalentFull = makeTranscriptDisplayConfig({ kind: "custom", ...presetContent("full") });
const namedFull = makeTranscriptDisplayConfig({ kind: "preset", level: "full" });

function baselineProvider(config: TranscriptDisplayConfigV1, eligibleDisclosureIds: readonly string[]) {
  return (
    <TranscriptRenderProvider config={config} disclosureScope="scope" eligibleDisclosureIds={eligibleDisclosureIds}>
      <DisclosureProbe id="tool-a" />
    </TranscriptRenderProvider>
  );
}

const { rerender } = render(
  baselineProvider(makeTranscriptDisplayConfig({ kind: "preset", level: "activity" }), ["tool-a"]),
);

// A pre-existing explicit close survives ordinary Custom default-open behavior.
act(() => setDisclosureOpen(scopedDisclosureId("scope", "tool-a"), false));
rerender(baselineProvider(equivalentFull, ["tool-a"]));
expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("false");

// Entering the named Full preset clears that stale close and starts a baseline.
rerender(baselineProvider(namedFull, ["tool-a"]));
expect(screen.getByTestId("disclosure-tool-a").dataset.open).toBe("true");
```

Add the inverse named Full→equivalent Custom case, manual collapse after Full entry, and remount under each kind. With no explicit choice, equivalent Custom opens through its vector fallback; with an explicit close, that choice wins. Explicit choices made after the named baseline must win.

- [x] **Step 3: Run the domain tests and verify the old normalizer fails**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/transcriptDisplay/config.test.ts src/transcriptDisplay/projector.test.ts src/transcriptDisplay/renderContext.test.tsx src/stores/transcriptDisplay.test.ts
```

Expected: FAIL because `normalizeContent` returns a named preset for equivalent Custom vectors; downstream kind, fingerprint, summary, persistence, and Custom-Full baseline assertions fail for that reason.

- [x] **Step 4: Make Custom normalization kind-authoritative**

Remove `sameVector` and `presetForVector` if they have no remaining callers. Keep strict Custom validation, then clone the flat vector without canonicalizing it:

```ts
export function normalizeContent(content: ContentSelection): ContentSelection {
  if (content.kind === "preset") {
    if (!isContentLevel(content.level)) throw new Error("unsupported transcript content level");
    return { kind: "preset", level: content.level };
  }
  if (!isCustomSelection(content)) throw new Error("invalid custom transcript content");
  return { kind: "custom", ...cloneVector(content) };
}
```

Do not change `wireContent`/`readWireContent`: they continue mapping flat frontend Custom to and from the nested wire shape. Do not broaden compatibility aliases.

- [x] **Step 5: Run focused and adjacent domain suites**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/transcriptDisplay/config.test.ts src/transcriptDisplay/projector.test.ts src/transcriptDisplay/renderContext.test.tsx src/stores/transcriptDisplay.test.ts src/panes/session/transcript/TranscriptBody.test.tsx
```

Expected: PASS. Named presets remain presets; equivalent Custom remains Custom; projection follows vectors; only named Full resets the baseline.

- [x] **Step 6: Format, typecheck, and commit the domain correction**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/transcriptDisplay/config.ts src/transcriptDisplay/config.test.ts src/transcriptDisplay/projector.test.ts src/transcriptDisplay/renderContext.test.tsx src/stores/transcriptDisplay.test.ts
npm run typecheck
git -C "$repo_root" diff --check
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/transcriptDisplay/config.ts cmd/evener-hub/frontend/src/transcriptDisplay/config.test.ts cmd/evener-hub/frontend/src/transcriptDisplay/projector.test.ts cmd/evener-hub/frontend/src/transcriptDisplay/renderContext.test.tsx cmd/evener-hub/frontend/src/stores/transcriptDisplay.test.ts
git -C "$repo_root" commit -m "fix(web): preserve explicit custom detail"
```

Expected: all commands exit zero; the commit contains only the five named paths.

---

### Task 2: Add the first-class SegmentedControl widget and gallery

**Files:**
- Create: `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/index.tsx`
- Create: `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css`
- Create: `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.test.tsx`
- Create: `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.tsx`
- Create: `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.module.css`
- Create: `cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/index.ts`
- Modify: `docs/web-ui/design-system.md`

**Interfaces:**
- Produces the exact generic `SegmentedControlOption<T>` and `SegmentedControlProps<T>` API from the approved spec.
- Produces `SegmentedControl` and its types from the shared barrel for Task 4.
- Does not alter RadioGroup or add styling/variant escape hatches.

- [x] **Step 1: Write failing validation and accessibility tests**

Create `segmentedcontrol.test.tsx` with a reusable controlled harness and options:

```tsx
const OPTIONS = [
  { value: "chat", label: "Chat" },
  { value: "intent", label: "Intent" },
  { value: "tools", label: "Tools" },
  { value: "activity", label: "Activity" },
  { value: "full", label: "Full", accessibleLabel: "Full detail" },
  { value: "custom", label: "Custom" },
] as const;

function Harness({
  initial = "tools",
  disabled = false,
  onChange,
}: {
  initial?: string;
  disabled?: boolean;
  onChange?(value: string): void;
}) {
  const [value, setValue] = useState(initial);
  return (
    <SegmentedControl
      id="detail-level"
      aria-describedby="detail-help"
      label="Transcript detail"
      value={value}
      options={OPTIONS}
      onChange={(next) => {
        setValue(next);
        onChange?.(next);
      }}
      disabled={disabled}
      fullWidth
    />
  );
}
```

Assert 2 and 6 options render; 1/7 options, duplicate values, unmatched `value`, empty group label, empty option label, and empty supplied accessible label throw developer errors. Assert omitted props select `size="md"` and intrinsic width (`fullWidth=false`). Assert the group has the supplied ID, generates one when omitted, sets `aria-labelledby` to its generated visible-label ID, forwards caller-supplied `aria-describedby`, sets `aria-orientation="horizontal"`, and has exactly one checked radio. Assert Full's accessible name is **Full detail** while its complete DOM text stays **Full**; no option relies on `title` as its accessible name.

- [x] **Step 2: Add failing interaction, disabled, and CSS-contract tests**

Cover click, native Enter/Space activation, no duplicate emission for the current value, Right/Down/Left/Up wrapping, Home/End, disabled skipping, and DOM focus after every navigation key:

```tsx
const onChange = vi.fn();
render(<Harness onChange={onChange} />);
await user.click(screen.getByRole("radio", { name: "Tools" }));
expect(onChange).not.toHaveBeenCalled();

screen.getByRole("radio", { name: "Tools" }).focus();
await user.keyboard("{ArrowRight}");
expect(document.activeElement).toBe(screen.getByRole("radio", { name: "Activity" }));
expect(screen.getByRole("radio", { name: "Activity" }).getAttribute("aria-checked")).toBe("true");
```

For group disablement, assert `aria-disabled="true"`, every button has native `disabled`, every `tabIndex` is `-1`, and click/Enter/Space/synthetic selection attempts emit nothing. Cover selected-disabled fallback and all-disabled groups. Render the widget inside a form with an `onSubmit` spy, activate an option, and assert the form does not submit.

In the same test file, strip CSS comments before matching. Assert root/label typography, track `--field`/`--edge`/`--shadow-inset-field`, literal 2px padding/gap, option centering/padding/radius/type, neutral hover/selection, one option-owned disabled opacity, `outline-offset: 2px`, visible track overflow, label ellipsis, 28px/32px option sizes, computed 34px/38px outer tracks, `--tap-min` Mobile block size and 50px outer track, property-scoped transitions, and reduced motion. Explicitly reject `min-inline-size: var(--tap-min)`, theme branches, accent selection, wrapping, negative-margin/full-bleed escapes, and horizontal scrolling.

- [x] **Step 3: Run all widget tests and confirm the module is absent**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/widgets/segmentedcontrol/segmentedcontrol.test.tsx
```

Expected: FAIL with module-not-found for `./index`/the stylesheet or missing exported component. This single RED run covers behavior and visual contracts before either production file exists.

- [x] **Step 4: Implement validated generic radio-group behavior**

Use RadioGroup's enabled-index/ref pattern but enforce the stricter contract:

```tsx
export interface SegmentedControlOption<T extends string = string> {
  value: T;
  label: string;
  accessibleLabel?: string;
  disabled?: boolean;
}

export interface SegmentedControlProps<T extends string = string> {
  label: string;
  value: T;
  options: readonly SegmentedControlOption<T>[];
  onChange(value: T): void;
  disabled?: boolean;
  size?: "sm" | "md";
  fullWidth?: boolean;
  id?: string;
  "aria-describedby"?: string;
}

function validateSegmentedControl<T extends string>(
  label: string,
  value: T,
  options: readonly SegmentedControlOption<T>[],
): void {
  if (options.length < 2 || options.length > 6) throw new Error("SegmentedControl requires two through six options");
  if (label.trim() === "") throw new Error("SegmentedControl requires a non-empty group label");
  if (new Set(options.map((option) => option.value)).size !== options.length)
    throw new Error("SegmentedControl option values must be unique");
  if (options.filter((option) => option.value === value).length !== 1)
    throw new Error("SegmentedControl value must match exactly one option");
  for (const option of options) {
    if (option.label.trim() === "") throw new Error("SegmentedControl options require visible labels");
    if (option.accessibleLabel !== undefined && option.accessibleLabel.trim() === "")
      throw new Error("SegmentedControl accessible labels must be non-empty");
  }
}
```

Call `useId` unconditionally for generated group and label IDs, validate after hooks, and apply a per-option ref array. `choose` returns when group/option disabled or the option is already selected. Navigation chooses and focuses once. Enter and Space rely on native button click; do not handle them in `onKeyDown` and double-emit.

Render the supplied `id` unchanged on `role="radiogroup"`; set `aria-labelledby` to the generated visible-label ID; forward `aria-describedby` there; set `aria-disabled` only for group disablement. Every option is a real `<button type="button" role="radio">` and receives `disabled={disabled || option.disabled === true}`. The selected disabled option remains checked, but the first enabled option becomes the tab stop. No enabled option means no tab stop. Set `gridTemplateColumns` to `repeat(options.length, minmax(0, 1fr))`; `fullWidth` alone adds the 100%-width class.

- [x] **Step 5: Implement the complete visual contract**

Wire every class through `requireClass` and implement this stylesheet before rerunning tests:

```css
.root {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-inline-size: 0;
  gap: var(--space-2);
}
.label {
  color: var(--ink-hi);
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  line-height: var(--line-height-body);
  font-weight: var(--font-weight-medium);
}
.track {
  display: inline-grid;
  align-items: stretch;
  box-sizing: border-box;
  overflow: visible;
  padding: 2px;
  gap: 2px;
  border: 1px solid var(--edge);
  border-radius: var(--radius-control);
  background: var(--field);
  box-shadow: var(--shadow-inset-field);
}
.fullWidth {
  inline-size: 100%;
}
.option {
  display: flex;
  align-items: center;
  justify-content: center;
  min-inline-size: 0;
  inline-size: 100%;
  border: 0;
  border-radius: var(--radius-control);
  padding-block: 0;
  padding-inline: var(--space-1);
  background: transparent;
  color: var(--ink-mid);
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
  line-height: var(--line-height-body);
  font-weight: var(--font-weight-medium);
  text-align: center;
  cursor: pointer;
  transition:
    background-color var(--motion-duration-hover) var(--motion-easing-standard),
    border-color var(--motion-duration-hover) var(--motion-easing-standard),
    color var(--motion-duration-hover) var(--motion-easing-standard),
    box-shadow var(--motion-duration-hover) var(--motion-easing-standard);
}
.option:not([aria-checked="true"]):hover:not(:disabled) {
  background: var(--hover-1);
}
.option[aria-checked="true"] {
  background: var(--hover-2);
  color: var(--ink-hi);
  font-weight: var(--font-weight-semibold);
}
.option:focus-visible {
  outline: var(--focus-ring);
  outline-offset: 2px;
}
.option:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
.optionLabel {
  min-inline-size: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sm {
  block-size: 28px;
}
.md {
  block-size: 32px;
}
@media (max-width: 899px) {
  .option {
    min-block-size: var(--tap-min);
  }
}
@media (prefers-reduced-motion: reduce) {
  .option {
    transition: none;
  }
}
```

The track/root never apply disabled opacity, so a disabled option is attenuated exactly once.

- [x] **Step 6: Run the complete widget suite GREEN**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/widgets/segmentedcontrol/segmentedcontrol.test.tsx
```

Expected: PASS for validation, ARIA, click/keyboard, focus movement, disabled behavior, default/intrinsic/full-width classes, complete names, geometry, tokens, motion, and ellipsis.

- [x] **Step 7: Add a failing gallery test, then implement exports/docs/gallery**

Create `src/dev/gallery-sections/segmentedcontrol.test.tsx` first, import the absent section, and assert both theme panes expose the required labelled groups, selected values, disabled states, and narrow frames:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/dev/gallery-sections/segmentedcontrol.test.tsx
```

Expected: FAIL with the gallery module/states absent.

Add to `src/widgets/index.ts`:

```ts
export type { SegmentedControlOption, SegmentedControlProps } from "./segmentedcontrol";
export { SegmentedControl } from "./segmentedcontrol";
```

Add the locked API row to `docs/web-ui/design-system.md`. Build a `<section data-testid="segmentedcontrol-gallery">` under `ThemeFlip` for intrinsic `md`, full-width six-option `md`, `sm`, first/middle/last/Custom selected, one disabled option, selected disabled, group disabled, keyboard focus, and 320px/390px frames. Use real widget instances and gallery layout CSS only; do not copy widget chrome. Make the new gallery test pass. `WidgetGallery.tsx` discovers the file automatically; do not add a registry.

- [x] **Step 8: Run widget, gallery, token, type, and lint gates**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/widgets/segmentedcontrol src/widgets/index.ts src/dev/gallery-sections/segmentedcontrol.tsx src/dev/gallery-sections/segmentedcontrol.module.css src/dev/gallery-sections/segmentedcontrol.test.tsx
npx vitest run src/widgets/segmentedcontrol/segmentedcontrol.test.tsx src/dev/gallery-sections/segmentedcontrol.test.tsx src/dev/WidgetGallery.test.tsx src/styles/token-contract.test.ts src/styles/requireclass-contract.test.ts
npm run typecheck
npm run lint
git -C "$repo_root" diff --check
```

Expected: PASS. `WidgetGallery.test.tsx` reports no missing section, and token contracts accept both themes without a new allowlist exception.

- [x] **Step 9: Commit the complete shared widget**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/widgets/segmentedcontrol/index.tsx cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.test.tsx cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.tsx cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.module.css cmd/evener-hub/frontend/src/dev/gallery-sections/segmentedcontrol.test.tsx cmd/evener-hub/frontend/src/widgets/index.ts docs/web-ui/design-system.md
git -C "$repo_root" commit -m "feat(web): add segmented control"
```

Expected: one reviewed commit containing only the eight named paths.

---

### Task 3: Extend Disclosure with controlled and disabled modes

**Files:**
- Modify: `cmd/evener-hub/frontend/src/widgets/disclosure/index.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.module.css`
- Modify: `cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.tsx`
- Modify if needed: `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.module.css`
- Create: `cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/widgets/index.ts`
- Modify: `docs/web-ui/design-system.md`

**Interfaces:**
- Produces `DisclosureProps = DisclosureCommonProps & DisclosureStateProps` exactly as specified.
- Store-backed callers keep `id/defaultOpen` persistence unchanged.
- Controlled callers use `open/onOpenChange`; Task 4 owns editor-local `useState(false)`.

- [x] **Step 1: Add failing controlled-mode tests**

Add a controlled harness to `disclosure.test.tsx`:

```tsx
function ControlledHarness({ disabled = false }: { disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Disclosure open={open} onOpenChange={setOpen} summary="Customize & advanced" disabled={disabled}>
      <p>Advanced body</p>
    </Disclosure>
  );
}
```

Assert supplied `open` controls body mounting, click and native keyboard activation each request exactly one state change, an `open` prop rerender updates the DOM, and a synthetic native `toggle` event does not call `onOpenChange`.

- [x] **Step 2: Add failing disabled tests for both state branches**

For controlled and store-backed modes, assert `aria-disabled="true"`, `tabIndex=-1`, retained current open state, no callback/store mutation, no keyboard/pointer activation, and reenable-by-rerender. Add CSS-source assertions that only `.summary[aria-disabled="true"]` owns `opacity: 0.5`/not-allowed cursor, enabled-only hover excludes it, and `.details`/`.body` do not receive disabled opacity.

- [x] **Step 3: Run Disclosure tests and confirm the old API is red**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/widgets/disclosure/disclosure.test.tsx src/widgets/disclosure/disclosureStore.test.ts
```

Expected: type/runtime failures because the existing API requires `id`, has no `open/onOpenChange/disabled`, and always mutates the store.

- [x] **Step 4: Split controlled and store-backed internals around one renderer**

Implement the exact discriminated union from the spec. Keep hook use unconditional by component boundary:

```tsx
interface DisclosureCommonProps {
  summary: ReactNode;
  children: ReactNode;
  disabled?: boolean;
  "data-testid"?: string;
}

type DisclosureStateProps =
  | { id: string; defaultOpen?: boolean; open?: never; onOpenChange?: never }
  | { open: boolean; onOpenChange(open: boolean): void; id?: never; defaultOpen?: never };

export type DisclosureProps = DisclosureCommonProps & DisclosureStateProps;

type StoreBackedDisclosureProps = DisclosureCommonProps & {
  id: string;
  defaultOpen?: boolean;
};

type ControlledDisclosureProps = DisclosureCommonProps & {
  open: boolean;
  onOpenChange(open: boolean): void;
};

interface DisclosureViewProps extends DisclosureCommonProps {
  open: boolean;
  requestToggle(): void;
}

function DisclosureView({
  summary,
  children,
  disabled = false,
  open,
  requestToggle,
  "data-testid": testId,
}: DisclosureViewProps) {
  return (
    <details className={CLASS.details} open={open} data-testid={testId}>
      <summary
        className={CLASS.summary}
        aria-disabled={disabled || undefined}
        tabIndex={disabled ? -1 : undefined}
        onClick={(event) => {
          event.preventDefault();
          if (!disabled) requestToggle();
        }}
      >
        <span className={CLASS.chevron} aria-hidden="true" data-open={open ? "true" : "false"}>
          <Chevron />
        </span>
        {summary}
      </summary>
      {open && <div className={CLASS.body}>{children}</div>}
    </details>
  );
}

function StoreBackedDisclosure(props: StoreBackedDisclosureProps) {
  const open = isDisclosureOpen(props.id, props.defaultOpen ?? false);
  return <DisclosureView {...props} open={open} requestToggle={() => toggleDisclosure(props.id, props.defaultOpen ?? false)} />;
}

function ControlledDisclosure(props: ControlledDisclosureProps) {
  return <DisclosureView {...props} open={props.open} requestToggle={() => props.onOpenChange(!props.open)} />;
}

export function Disclosure(props: DisclosureProps) {
  return "open" in props ? <ControlledDisclosure {...props} /> : <StoreBackedDisclosure {...props} />;
}
```

`DisclosureView` always prevents native summary toggle. When disabled, it sets `aria-disabled`, `tabIndex={-1}`, and does not invoke `requestToggle`; an open body remains mounted. Do not change `disclosureStore.ts` absent a focused failing store regression.

- [x] **Step 5: Add disabled summary styling without dimming the body**

Change hover to `.summary:hover:not([aria-disabled="true"])`. Add a summary-only disabled selector with not-allowed cursor and 0.5 opacity. Preserve the open `--hover-2` wash, Chevron/body motion, and reduced-motion gate.

- [x] **Step 6: Add shared export, documentation, and both disabled gallery modes**

Create `src/dev/gallery-sections/disclosure.test.tsx` first and assert each `ThemeFlip` pane contains a disabled collapsed store-backed summary plus a disabled open controlled summary whose body is present. Run it before changing the gallery:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/dev/gallery-sections/disclosure.test.tsx
```

Expected: FAIL because the current gallery contains only enabled store-backed open/collapsed examples.

Export `Disclosure` and `DisclosureProps` from `src/widgets/index.ts`. Add a locked inventory row describing both discriminated branches and common `disabled` behavior. Under `ThemeFlip`, make the section owner `data-testid="disclosure-gallery"` and add a disabled collapsed store-backed example plus a disabled open controlled example; the latter's body must remain visibly full-opacity. Make the new gallery test pass in both theme panes.

- [x] **Step 7: Run focused regressions, format, typecheck, and lint**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/widgets/disclosure src/widgets/index.ts src/dev/gallery-sections/disclosure.tsx src/dev/gallery-sections/disclosure.module.css src/dev/gallery-sections/disclosure.test.tsx
npx vitest run src/widgets/disclosure/disclosure.test.tsx src/widgets/disclosure/disclosureStore.test.ts src/dev/gallery-sections/disclosure.test.tsx src/dev/WidgetGallery.test.tsx src/styles/token-contract.test.ts src/styles/requireclass-contract.test.ts src/panes/session/chrome/TasksPanel.test.tsx src/panes/session/chrome/ActivityRowDetail.test.tsx src/panes/settings/sections/about.test.tsx
npm run typecheck
npm run lint
git -C "$repo_root" diff --check
```

Expected: PASS, including all existing store-backed consumers and motion tests.

- [x] **Step 8: Pass the pre-migration two-theme gallery checkpoint**

Resolve `repo_root=$(git rev-parse --show-toplevel)`. From `"$repo_root/cmd/evener-hub/frontend"`, launch `npm run dev -- --host 127.0.0.1 --port 4173` with `exec_command(mode="background")`. After its readiness output, navigate the persistent browser to `http://127.0.0.1:4173/dev/widgets`, set a 1440×1000 viewport, and inspect the real `SegmentedControl` and `Disclosure` gallery sections. Keyboard-focus the first, middle, and last segmented options; inspect intrinsic/full-width, selected, disabled, 320px, and 390px cases; inspect disabled store-backed and controlled Disclosure with the open body undimmed. Review both `data-theme="light"` and `data-theme="dark"` panes. Stop the exact server job with `job_stop`.

Expected: explicit human approval of both shared-widget sections. Any visual correction returns to the Task 2 or Task 3 RED/GREEN and two-review cycle; Task 4 remains blocked.

- [x] **Step 9: Commit the Disclosure extension**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/widgets/disclosure/index.tsx cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.module.css cmd/evener-hub/frontend/src/widgets/disclosure/disclosure.test.tsx cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.tsx cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.module.css cmd/evener-hub/frontend/src/dev/gallery-sections/disclosure.test.tsx cmd/evener-hub/frontend/src/widgets/index.ts docs/web-ui/design-system.md
git -C "$repo_root" commit -m "feat(web): control disclosure state"
```

If the gallery stylesheet did not change, omit that one path from `git add`. Expected: no store implementation change and no unrelated consumer edits.

---

### Task 4: Refit TranscriptDetailEditor with shared controls

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css`

**Interfaces:**
- Consumes Task 1 kind-authoritative Custom, Task 2 `SegmentedControl`, and Task 3 controlled `Disclosure`.
- Preserves `TranscriptDetailEditorProps` unchanged.
- Produces a six-choice, remount-reset editor for live and Settings surfaces.

- [x] **Step 1: Replace old five-stop/readout tests with failing six-choice and Custom-memory tests**

Assert six radios—Chat, Intent, Tools, Activity, Full detail, Custom—with Custom checked for every explicit Custom vector. Remove expectations for a separate Current detail readout and critical-row explanation.

Use a controlled harness to cover restore-or-clone:

```tsx
const changes: TranscriptDisplayConfigV1[] = [];
render(
  <ControlledEditor
    initial={makeTranscriptDisplayConfig({ kind: "preset", level: "tools" })}
    onChange={(next) => changes.push(next)}
  />,
);
function latestContent(): TranscriptDisplayConfigV1["content"] {
  const latest = changes.at(-1);
  if (latest === undefined) throw new Error("editor did not emit a configuration");
  return latest.content;
}

await user.click(screen.getByRole("radio", { name: "Custom" }));
expect(latestContent()).toEqual({ kind: "custom", ...presetContent("tools") });
await user.click(screen.getByText(/^Customize & advanced/));
await user.click(screen.getByRole("switch", { name: "Reasoning" }));
const remembered = latestContent();
await user.click(screen.getByRole("radio", { name: "Chat" }));
await user.click(screen.getByRole("radio", { name: "Custom" }));
expect(latestContent()).toEqual(remembered);
```

Unmount/remount, select a preset, then select Custom; assert the dormant cache was cleared and the current preset is cloned. Verify content changes never alter Metrics or Diagnostics.

- [x] **Step 2: Add failing shared-composition and disclosure tests**

Assert `Customize & advanced · N extras` for presets and `Customize & advanced · Custom content · N extras` for Custom. Open it, rerender values, and verify it stays controlled; unmount/remount and verify it starts closed. When editor `disabled` becomes true, the Disclosure summary is inert while open controls remain disabled.

Assert Hook exit messages is labelled through FormRow/Select, Switch visuals are not feature-overridden, and all three semantic fieldsets remain. `N` counts Metrics/Diagnostics only.

- [x] **Step 3: Run editor tests and confirm old UI behavior fails**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/panes/session/transcript/TranscriptDetailEditor.test.tsx
```

Expected: FAIL because the current editor renders five RadioGroup options, canonicalizes preset-equivalent Custom, shows duplicate Current/critical copy, uses a raw Advanced button and raw select, and has no mounted Custom cache.

- [x] **Step 4: Implement six choices and mounted Custom restore-or-clone**

Import shared controls from `../../../widgets`. Define `ContentChoice = ContentLevel | "custom"` and six `SegmentedControlOption<ContentChoice>` values. Use `useRef<ContentVector | undefined>` for the mounted cache:

```tsx
const lastCustom = useRef<ContentVector | undefined>(undefined);
if (config.content.kind === "custom") lastCustom.current = contentVector(config.content);

function selectContent(choice: ContentChoice): void {
  if (choice === "custom") {
    const vector = lastCustom.current ?? contentVector(config.content);
    const next = { kind: "custom" as const, ...vector };
    lastCustom.current = vector;
    emit({ ...config, content: next });
    return;
  }
  emit({ ...config, content: { kind: "preset", level: choice } });
}
```

When a Content Switch changes, write the emitted Custom vector into `lastCustom.current` before calling `onChange`. Render `SegmentedControl` with `value={config.content.kind === "preset" ? config.content.level : "custom"}`, `fullWidth`, and default `md` size.

- [x] **Step 5: Compose controlled Disclosure, Switch, FormRow, and Select**

Keep `const [advancedOpen, setAdvancedOpen] = useState(false)`. Render:

```tsx
const disclosureSummary =
  config.content.kind === "custom"
    ? `Customize & advanced · Custom content · ${advancedCount} extras`
    : `Customize & advanced · ${advancedCount} extras`;

<FormRow label="Hook exit messages" htmlFor={hookExitId}>
  <Select
    id={hookExitId}
    value={config.advanced.hookExits}
    options={HOOK_EXIT_OPTIONS}
    disabled={disabled}
    onChange={(event) => updateHookExits(event.target.value)}
  />
</FormRow>
```

Type `HOOK_EXIT_OPTIONS` as `SelectOption[]` because the shared `SelectProps.options` contract is mutable; do not broaden Select's public API. Wrap the existing Content, Metrics, and Diagnostics fieldset elements in `<Disclosure open={advancedOpen} onOpenChange={setAdvancedOpen} disabled={disabled} summary={disclosureSummary}>`; keep their existing Switch labels and domain update functions, replacing only the Hook exit row with the exact FormRow/Select block above. Delete the raw Advanced button, text triangles, Current detail paragraph, and critical-row note.

- [x] **Step 6: Replace private chrome with exact layout-only CSS**

The editor root owns `container-type: inline-size`, `container-name: transcript-detail-editor`, `gap: var(--space-3)`, and compact `gap: var(--space-2)`. Fieldsets use reset/semantic layout only; use three columns above 34rem and one column inside:

```css
@container transcript-detail-editor (max-width: 34rem) {
  .fieldsets { grid-template-columns: minmax(0, 1fr); }
}
```

Delete every RadioGroup role selector, Switch geometry selector, raw Select/Advanced skin, Current/Custom accent classes, critical callout, and local reduced-motion rules owned by widgets. Do not add a Mobile width rule.

- [x] **Step 7: Run focused editor/domain/widget regressions and static scans**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/panes/session/transcript/TranscriptDetailEditor.tsx src/panes/session/transcript/TranscriptDetailEditor.test.tsx src/panes/session/transcript/transcriptDisplay.module.css
npx vitest run src/panes/session/transcript/TranscriptDetailEditor.test.tsx src/transcriptDisplay/config.test.ts src/widgets/segmentedcontrol/segmentedcontrol.test.tsx src/widgets/disclosure/disclosure.test.tsx
npm run typecheck
if rg -n ':global\(|role="(radio|switch)"|advancedToggle|selectLabel|critical|Current detail' src/panes/session/transcript/TranscriptDetailEditor.tsx src/panes/session/transcript/transcriptDisplay.module.css; then
  echo "forbidden private transcript-detail styling or copy remains" >&2
  exit 1
fi
```

Expected: PASS for tests and typecheck; the guarded `rg` finds no forbidden private-skin or removed-copy match and therefore exits zero.

- [x] **Step 8: Commit the editor refit**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" diff --check
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailEditor.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css
git -C "$repo_root" commit -m "fix(web): compose transcript detail editor"
```

Expected: one reviewed commit with only editor code/tests/shared feature layout CSS.

---

### Task 5: Refit the live Detail Popover and Sheet

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css`

**Interfaces:**
- Consumes Task 4 `TranscriptDetailEditor` and existing transcript store behavior.
- Preserves `TranscriptDetailControlProps` and trigger-ref focus fallback.
- Produces shared secondary Button trigger, shared footer actions, a nonmodal desktop dialog in Popover, and the existing modal bottom Sheet on Mobile.

- [x] **Step 1: Add failing shared-composition and trigger-ARIA tests**

Assert the trigger is a shared secondary `sm` Button with `aria-haspopup="dialog"` and `aria-expanded` false→true→false. Summary copy uses `extras`, not `advanced`. Desktop opens one `role="dialog"` named by **Transcript display details** with `aria-modal="false"`; Mobile retains shared Sheet modal semantics and focus return.

Assert **Use hub default** is secondary and conditional; **Edit hub defaults** is quiet. Preserve browser-local layout isolation and trigger ref behavior.

- [x] **Step 2: Add failing Popover-scroll and announcement-role tests**

Open Desktop, dispatch an internal/window scroll, and assert the panel remains mounted and trigger stays expanded. Assert passive loading/older-hub information is consolidated in one `role="status"`; hub/storage failures are consolidated in one inline `role="alert"`; no identical message appears in both.

Add CSS-source assertions for exact child geometry and absence of duplicate chrome:

```ts
expect(css).toMatch(/inline-size:\s*min\(42rem, calc\(100vw - var\(--space-8\)\)\)/);
expect(css).toMatch(/max-block-size:\s*calc\(100dvh - var\(--space-8\)\)/);
const panelMatch = /\.detailPanel\s*\{([^}]*)\}/.exec(css);
expect(panelMatch).not.toBeNull();
expect(panelMatch?.[1] ?? "").not.toMatch(/background|box-shadow|border-radius/);
```

- [x] **Step 3: Run control tests and confirm the private implementation is red**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/panes/session/transcript/TranscriptDetailControl.test.tsx
```

Expected: FAIL because the trigger/actions are raw buttons, desktop `aria-haspopup` is not `dialog`, the child lacks dialog semantics, Popover defaults to close on scroll, and CSS duplicates Popover chrome.

- [x] **Step 4: Compose shared Button/Popover/Sheet with exact semantics**

Use `useId` for the desktop heading. The trigger shape is:

```tsx
<Button
  ref={triggerRef}
  size="sm"
  variant="secondary"
  aria-haspopup="dialog"
  aria-expanded={open}
  onClick={() => setOpen((current) => !current)}
>
  {triggerLabel}
</Button>
```

Pass `closeOnScroll={false}` to Popover. Make its feature child `role="dialog"`, `aria-modal="false"`, and `aria-labelledby={headingId}`. Do not add `aria-controls` unless it targets the mounted dialog's stable ID. Keep Mobile Sheet title/focus behavior unchanged. Render footer actions with shared Button variants.

Separate passive `statusMessages` from `failureMessages`; render at most one region for each category and never duplicate text. Local editing remains enabled on older hubs.

- [x] **Step 5: Reduce live-control CSS to exact geometry and layout**

Give `.detailPanel` only `box-sizing`, exact inline/max-block size, `padding: var(--space-4)`, `overflow-y: auto`, container name/type, and internal flex/grid layout. Remove background/radius/shadow/overlay motion. Arrange actions with parent grid/flex; do not target descendant buttons. Remove trigger/action button skins and feature reduced-motion blocks.

- [x] **Step 6: Run live, Popover, Sheet, store, and focus regressions**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/panes/session/transcript/TranscriptDetailControl.tsx src/panes/session/transcript/TranscriptDetailControl.test.tsx src/panes/session/transcript/transcriptDisplay.module.css
npx vitest run src/panes/session/transcript/TranscriptDetailControl.test.tsx src/panes/session/transcript/TranscriptDetailEditor.test.tsx src/widgets/popover/popover.test.tsx src/widgets/sheet/sheet.test.tsx src/stores/transcriptDisplay.test.ts src/panes/session/Session.test.tsx src/panes/transcript/Transcript.test.tsx
npm run typecheck
npm run lint
git -C "$repo_root" diff --check
```

Expected: PASS. Internal scroll does not dismiss Desktop; Sheet still closes on Escape and restores the forwarded trigger focus.

- [x] **Step 7: Commit the live refit**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.tsx cmd/evener-hub/frontend/src/panes/session/transcript/TranscriptDetailControl.test.tsx cmd/evener-hub/frontend/src/panes/session/transcript/transcriptDisplay.module.css
git -C "$repo_root" commit -m "fix(web): align live transcript detail"
```

Expected: one reviewed commit with only the three named paths.

---

### Task 6: Refit Settings cards, preview canvas, and announcements

**Files:**
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.test.tsx`
- Modify: `cmd/evener-hub/frontend/src/panes/settings/sections/transcript.module.css`
- Create: `cmd/evener-hub/frontend/src/transcriptDisplay/designSystemContract.test.ts`

**Interfaces:**
- Consumes Task 4 shared editor and existing save/store/preview fixture behavior.
- Preserves `TranscriptDisplayCardProps`, Desktop/Mobile draft isolation, acknowledgement, retry, and preview data isolation.
- Produces the exact `transcript-display-preview-canvas-${layout}` outer-well marker; Mobile is the measured phone-width owner.

- [x] **Step 1: Add failing Card hierarchy and selected-state tests**

Assert each semantic `<article>` wraps a shared Card and contains one heading/revision, one SegmentedControl selected-state owner, controls before preview, and no **Current detail** line or critical-row note. Assert exactly one section intro:

```text
Transcript display defaults sync to devices paired with this hub. Live transcript choices remain browser-local.
```

Retain Desktop-then-Mobile order and independent drafts/errors/retries.

- [x] **Step 2: Add failing preview-canvas and neutral-surface tests**

Assert the outer well containing the **Example only—not your data** heading and production TranscriptBody has `data-testid="transcript-display-preview-canvas-mobile"`; the inner TranscriptBody host does not own phone width. Assert production fixture flow, no RPC/thread-store dependency, isolated disclosure scopes, and inventory as a separate sibling.

CSS-source tests require `box-sizing: border-box`, `width: min(390px, 100%)`, and `margin-inline: auto` on the Mobile canvas; `--surface-canvas` on the preview well; `--surface-inset` on inventory; and no accent stripe, inner scroller, fake device bezel, duplicate Card border/background/padding/radius/shadow, or wildcard motion rule.

- [x] **Step 3: Add failing status/alert/Toast tests**

Pin these paths in separate passive-state, failure, and acknowledged-success tests:

```ts
expect(screen.getAllByRole("status")).toHaveLength(1); // passive loading/support state
expect(screen.getAllByRole("alert")).toHaveLength(1); // one retryable failure path
const notifications = screen.getByRole("region", { name: "Notifications" });
expect(notifications.textContent).not.toContain("Could not save"); // rejected save stays inline
expect(notifications.textContent).toContain("Settings saved");    // acknowledged save only
```

Cover load failure, browser-storage failure, and per-card save failure separately. Assert one state transition produces one live-region message and failure text never appears simultaneously in Toast and an inline region. Keep shared Button for retry.

- [x] **Step 4: Add the cross-surface static contract and run all new tests red**

Create `designSystemContract.test.ts` before changing production sources. It reads and strips comments from all three feature stylesheets—`panes/session/transcript/transcriptDisplay.module.css`, `panes/settings/sections/transcriptDisplayCard.module.css`, and `panes/settings/sections/transcript.module.css`—and reads editor/control/card TSX. Use path-specific rule extraction and assert:

- no RadioGroup role-descendant selectors;
- no Switch geometry or role selectors;
- no raw Select, Advanced trigger, Detail trigger, action, or retry replacements;
- no Card/Popover background/padding/radius/shadow duplication;
- no widget-internal selectors or wildcard descendant motion;
- no decorative accent bars or raw `1.45`/`1.5` line-height;
- no duplicate `Current detail` or critical-row explanation;
- source imports/composes SegmentedControl, Disclosure, Switch, FormRow, Select, Button, Card, Popover, and Sheet.

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/transcriptDisplay/designSystemContract.test.ts
```

Expected: FAIL because the still-unmigrated Settings cards own private border/background/padding, render duplicate Current detail, use three intros, lack the outer Mobile canvas marker, and retain preview/inventory accent bars plus wildcard motion. Editor/live assertions added to the same contract should already pass from Tasks 4–5.

- [x] **Step 5: Wrap semantic articles in shared Card and simplify hierarchy**

Keep `article` as the labelled/test-ID owner, then render shared Card and one layout wrapper inside it:

```tsx
<article data-testid={cardId} aria-labelledby={`${cardId}-heading`}>
  <Card>
    <div className={CLASS.content}>
      <header className={CLASS.heading}>
        <h2 id={`${cardId}-heading`}>{name} default</h2>
        <span>Hub revision {confirmed.revision}</span>
      </header>
      <div className={CLASS.controls}>
        <TranscriptDetailEditor value={config} onChange={onChange} compact={false} disabled={disabled} />
      </div>
    </div>
  </Card>
</article>
```

Keep the existing scope, status/error, preview, and inventory elements inside `CLASS.content` after the controls, in that order. Delete the duplicate Current detail line. Replace the three section paragraphs with the one approved sentence. Preserve acknowledged-save-only Toast logic; do not add failure Toasts.

- [x] **Step 6: Make the outer preview well the exact width owner**

Give the outer example well `data-testid={`transcript-display-preview-canvas-${layout}`}`. For Mobile only, its width class declares `box-sizing: border-box; width: min(390px, 100%); margin-inline: auto;`. Keep the heading, padding, and production host inside it; leave inventory as a sibling. The Desktop well uses available width.

Use neutral surfaces and layout-only feature CSS. Card owns surface/padding/radius/shadow. Remove Mobile feature padding that would change the approved 256px/326px available widths.

- [x] **Step 7: Make the prewritten static design-system contract green**

Finish the exact private-skin/chrome removals named in Step 4 without weakening its matchers or rejecting legitimate preview/status layout. Run:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx vitest run src/transcriptDisplay/designSystemContract.test.ts
```

Expected: PASS only after all three migrated surfaces compose the shared widgets and all three feature stylesheets contain only their permitted layout/status/preview rules.

- [x] **Step 8: Run Settings, preview, static, and store regressions**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/panes/settings/sections/TranscriptDisplayCard.tsx src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcriptDisplayCard.module.css src/panes/settings/sections/transcript.tsx src/panes/settings/sections/transcript.test.tsx src/panes/settings/sections/transcript.module.css src/transcriptDisplay/designSystemContract.test.ts
npx vitest run src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/transcriptDisplay/designSystemContract.test.ts src/transcriptDisplay/previewFixture.test.ts src/panes/session/transcript/TranscriptBody.test.tsx src/stores/transcriptDisplay.test.ts src/dev/surface-sections/transcript.test.tsx
npm run typecheck
npm run lint
git -C "$repo_root" diff --check
```

Expected: PASS with exactly two cards/previews, one intro, correct announcement ownership, and no preview RPC/real-store access.

- [x] **Step 9: Commit the Settings refit**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.tsx cmd/evener-hub/frontend/src/panes/settings/sections/TranscriptDisplayCard.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/transcriptDisplayCard.module.css cmd/evener-hub/frontend/src/panes/settings/sections/transcript.tsx cmd/evener-hub/frontend/src/panes/settings/sections/transcript.test.tsx cmd/evener-hub/frontend/src/panes/settings/sections/transcript.module.css cmd/evener-hub/frontend/src/transcriptDisplay/designSystemContract.test.ts
git -C "$repo_root" commit -m "fix(web): align transcript display settings"
```

Expected: one reviewed commit containing only the seven named paths.

---

### Task 7: Expand production browser guards and prove the rejected geometry fails

**Files:**
- Modify: `cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx`
- Modify: `cmd/evener-hub/frontend/scripts/overflowguard/run.mjs`
- Temporarily mutate and restore: `cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css`

**Interfaces:**
- Consumes the completed shared widgets, live control, Settings cards, and production-backed deterministic harness.
- Produces named geometry/interaction measurements for all approved widths.
- The temporary mutation is never staged or committed.

- [x] **Step 1: Add 320px and failing segmented/preview expectations to the runner**

Add `320` to the runner's sweep while retaining `390, 700, 899, 900, 1024, 1400`. Add assertions for this expected measurement object, but do not change `overflowharness-entry.tsx` yet:

```ts
interface HarnessMeasurements {
  editors: Array<{
    surface: "live" | "settings";
    layout: "desktop" | "mobile";
    ownerTestId: string;
    track: { left: number; right: number; width: number; scrollWidth: number; clientWidth: number };
    segments: Array<{
      label: string;
      left: number;
      right: number;
      width: number;
      height: number;
      checked: boolean;
    }>;
  }>;
  canvases: Array<{
    layout: "desktop" | "mobile";
    testId: string;
    width: number;
    availableWidth: number;
    scrollWidth: number;
    clientWidth: number;
    scrollHeight: number;
    clientHeight: number;
  }>;
  fieldsets: Array<{ left: number; right: number; top: number; bottom: number }>;
  trigger: { left: number; right: number; top: number; bottom: number };
}
```

Use `transcript-display-card-desktop`, `transcript-display-card-mobile`, and the live Detail root as stable owners. On the Settings route, require both Desktop and Mobile card tracks to be 256px with six 40px options at 320, and 326px with six 51.667px options at 390, within 0.5px tolerance; every option is at least 44px high. Measure the live editor separately for one-row containment/no scroll rather than conflating it with Settings geometry. Require stable selected geometry and zero horizontal scroll on every ancestor that is an actual scroll container.

- [x] **Step 2: Add failing focus, boundary, container, preview, and Popover-scroll assertions**

For first/middle/last segments, send keyboard focus, verify `document.activeElement`, calculate painted outline bounds from `outlineWidth + outlineOffset`, and reject viewport/overflow clipping. Assert 899 uses Sheet and 900 uses Popover; both remain contained and the trigger is fully reachable. Assert one-column fieldsets only when the named editor container is at most 34rem, including the narrow-dock probe.

Measure `transcript-display-preview-canvas-mobile` as `min(390, available preview-section width)`: 256px at a 320 viewport, 326px at 390, and 390px when available. Require no preview inner scroll in either axis.

On Desktop, set panel `scrollTop`, dispatch the internal scroll, await the existing condition/frame helper, and assert the dialog remains connected, viewport-contained, and `aria-expanded="true"`.

- [x] **Step 3: Run the focused guard and confirm new assertions are initially red**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
node --check scripts/overflowguard/run.mjs
npm run typecheck
npm run overflowguard -- 320 390 899 900 1024
```

Expected: FAIL because the updated runner requests named segmented/canvas/focus/scroll fields that the unchanged production harness does not return. Do not weaken existing generic overflow, Sheet anchoring, Popover anchoring, target-height, card-count, or preview-count checks.

- [x] **Step 4: Implement the harness measurements and rerun the complete sweep**

Update `overflowharness-entry.tsx` to supply the object required by Steps 1–2, using its existing realized-viewport, condition/frame, production editor, and scroll-container helpers. Adjust runner plumbing only where needed to consume those fields; keep the failing assertions unchanged.

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/dev/overflowharness-entry.tsx
node --check scripts/overflowguard/run.mjs
npm run typecheck
npm run lint
npm run overflowguard -- 320 390 899 900 1024
npm run overflowguard
```

Expected: PASS for every command. The default sweep includes all seven required widths plus retained 700 coverage, and the special 1024 narrow-dock probe still runs.

- [x] **Step 5: Commit only legitimate guard changes before mutation**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" diff --check
git -C "$repo_root" add -- cmd/evener-hub/frontend/src/dev/overflowharness-entry.tsx cmd/evener-hub/frontend/scripts/overflowguard/run.mjs
git -C "$repo_root" commit -m "test(web): guard transcript detail geometry"
```

Expected: the SegmentedControl stylesheet is unchanged and unstaged.

- [x] **Step 6: Run the required path-scoped mutation RED proof and restore exactly**

From `cmd/evener-hub/frontend`, preserve the one target file outside the worktree, inject only the rejected Mobile inline minimum, and require the guard to fail for the named 40px/256px measurement:

```bash
set -euo pipefail
: "${EVENER_SCRATCH_DIR:?EVENER_SCRATCH_DIR must name the session scratch directory}"
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
css=src/widgets/segmentedcontrol/segmentedcontrol.module.css
backup="$EVENER_SCRATCH_DIR/segmentedcontrol.module.css.before-mutation"
cp "$css" "$backup"
restore_mutation() {
  if test -f "$backup"; then
    cp "$backup" "$css"
    rm -f "$backup"
  fi
}
trap restore_mutation EXIT
python3 - "$css" <<'PY'
from pathlib import Path
import sys
p = Path(sys.argv[1])
s = p.read_text()
needle = "  .option {\n    min-block-size: var(--tap-min);\n"
replacement = "  .option {\n    min-block-size: var(--tap-min);\n    min-inline-size: var(--tap-min);\n"
if s.count(needle) != 1:
    raise SystemExit(f"expected one Mobile option rule, found {s.count(needle)}")
p.write_text(s.replace(needle, replacement))
PY
set +e
npm run overflowguard -- 320 2>&1 | tee "$EVENER_SCRATCH_DIR/segmented-mutation-red.log"
red_status=${PIPESTATUS[0]}
set -e
test "$red_status" -ne 0
rg '40px|256px|257px|rightmost|segment' "$EVENER_SCRATCH_DIR/segmented-mutation-red.log"
restore_mutation
trap - EXIT
git -C "$repo_root" diff --exit-code -- cmd/evener-hub/frontend/src/widgets/segmentedcontrol/segmentedcontrol.module.css
npm run overflowguard -- 320
```

Expected: mutated run exits nonzero because option width is at least 44px and the rightmost local edge reaches at least 257px beyond the 256px track; restoration diff is empty; the same 320 guard exits zero. Remove the scratch log after recording evidence in the ignored proof report.

- [x] **Step 7: Verify no mutation or guard residue remains**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
rm -f "$EVENER_SCRATCH_DIR/segmented-mutation-red.log"
git -C "$repo_root" status --short
git -C "$repo_root" diff --check
```

Expected: clean worktree; no SegmentedControl CSS diff; committed guard changes only.

---

### Task 8: Capture visual proof, run canonical gates, and close the plan

**Files:**
- Create ignored evidence: `.superpowers/sdd/2026-08-26-transcript-detail-design-system-correction/proof/README.md`
- Create ignored screenshots: `.superpowers/sdd/2026-08-26-transcript-detail-design-system-correction/proof/*.png`
- Modify after acceptance: `docs/superpowers/plans/2026-08-26-transcript-detail-design-system-correction.md` (checkboxes only)
- Modify only if a gate proves a root defect: exact failing source/test path

**Interfaces:**
- Consumes all accepted task commits.
- Produces human-reviewed light/dark proof, complete deterministic gate evidence, a clean branch, and checked plan tracking.
- No new product behavior belongs here except minimal root-cause fixes for a discovered failure.

- [x] **Step 1: Format every touched frontend source path and run the full focused surface**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root/cmd/evener-hub/frontend"
npx biome check --write src/widgets/segmentedcontrol src/widgets/disclosure src/widgets/index.ts src/dev/gallery-sections/segmentedcontrol.tsx src/dev/gallery-sections/segmentedcontrol.module.css src/dev/gallery-sections/segmentedcontrol.test.tsx src/dev/gallery-sections/disclosure.tsx src/dev/gallery-sections/disclosure.module.css src/dev/gallery-sections/disclosure.test.tsx src/transcriptDisplay src/stores/transcriptDisplay.test.ts src/panes/session/transcript/TranscriptDetailEditor.tsx src/panes/session/transcript/TranscriptDetailEditor.test.tsx src/panes/session/transcript/TranscriptDetailControl.tsx src/panes/session/transcript/TranscriptDetailControl.test.tsx src/panes/session/transcript/transcriptDisplay.module.css src/panes/settings/sections/TranscriptDisplayCard.tsx src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcriptDisplayCard.module.css src/panes/settings/sections/transcript.tsx src/panes/settings/sections/transcript.test.tsx src/panes/settings/sections/transcript.module.css src/dev/overflowharness-entry.tsx
npx vitest run src/widgets/segmentedcontrol src/widgets/disclosure src/dev/gallery-sections/segmentedcontrol.test.tsx src/dev/gallery-sections/disclosure.test.tsx src/dev/WidgetGallery.test.tsx src/styles/token-contract.test.ts src/transcriptDisplay src/stores/transcriptDisplay.test.ts src/panes/session/transcript/TranscriptDetailEditor.test.tsx src/panes/session/transcript/TranscriptDetailControl.test.tsx src/panes/session/transcript/TranscriptBody.test.tsx src/panes/settings/sections/TranscriptDisplayCard.test.tsx src/panes/settings/sections/transcript.test.tsx src/dev/surface-sections/transcript.test.tsx src/protocol/tokenFlood.test.tsx
npm run typecheck
npm run lint
```

Expected: every command exits zero. Review formatter changes; commit only genuine root-cause fixes with named paths before continuing.

- [x] **Step 2: Capture separate corrected gallery and feature proof in both themes**

Resolve `repo_root`, create `proof_dir="$repo_root/.superpowers/sdd/2026-08-26-transcript-detail-design-system-correction/proof"`, and launch this exact command with `exec_command(cwd="$repo_root/cmd/evener-hub/frontend", mode="background")`:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
mkdir -p .superpowers/sdd/2026-08-26-transcript-detail-design-system-correction/proof
```

```text
npm run dev -- --host 127.0.0.1 --port 4173
```

Record the returned shell `job_id`; create a narrow readiness watch on `Local:.*127\.0\.0\.1:4173`; do not treat a merely running process as ready. Use base URL `http://127.0.0.1:4173` in the persistent Chrome browser and the table below. Before every capture, navigate to the exact URL, call `set_viewport({width, height, mobile: width < 900})`, then evaluate `document.documentElement.dataset.theme = "light"` or `"dark"`. Use real component interactions and `keyboard_press`, never copied markup or simulated focus classes.

| File | Route/query | Viewport | Theme | Required interaction and visible bounds | Browser capture action |
| --- | --- | --- | --- | --- | --- |
| `01-segmented-gallery-light.png` | `/dev/widgets` | 1440×1000 | light | In `segmentedcontrol-gallery`, keyboard-focus first→middle→last; frame includes intrinsic/full-width, selected/disabled, 320px, and 390px states. | `screenshot` with selector `[data-testid="segmentedcontrol-gallery"] [data-theme="light"]` |
| `02-segmented-gallery-dark.png` | `/dev/widgets` | 1440×1000 | dark | Same real focus sequence and all SegmentedControl states in the dark pane. | `screenshot` with selector `[data-testid="segmentedcontrol-gallery"] [data-theme="dark"]` |
| `03-disclosure-gallery-light.png` | `/dev/widgets` | 1440×1000 | light | Frame includes disabled collapsed store-backed and disabled open controlled summaries; open body is fully opaque. | `screenshot` with selector `[data-testid="disclosure-gallery"] [data-theme="light"]` |
| `04-disclosure-gallery-dark.png` | `/dev/widgets` | 1440×1000 | dark | Same two disabled modes and fully legible open body in dark. | `screenshot` with selector `[data-testid="disclosure-gallery"] [data-theme="dark"]` |
| `05-desktop-closed-light.png` | `/overflowharness.html?w=1024` | 1024×900 | light | Production Session, closed Detail trigger, transcript, and viewport edges visible. | viewport `screenshot` |
| `06-desktop-closed-dark.png` | `/overflowharness.html?w=1024` | 1024×900 | dark | Same closed production state. | viewport `screenshot` |
| `07-desktop-open-light.png` | `/overflowharness.html?w=1024` | 1024×900 | light | Click Detail, then **Customize & advanced**; full Popover, six segments, three fieldsets, and trigger visible. | viewport `screenshot` |
| `08-desktop-open-dark.png` | `/overflowharness.html?w=1024` | 1024×900 | dark | Same expanded Popover state. | viewport `screenshot` |
| `09-mobile-closed-light.png` | `/overflowharness.html?w=390` | 390×900 mobile | light | Production Mobile Session with closed Detail trigger and both horizontal edges visible. | viewport `screenshot` |
| `10-mobile-closed-dark.png` | `/overflowharness.html?w=390` | 390×900 mobile | dark | Same closed Mobile state. | viewport `screenshot` |
| `11-mobile-open-light.png` | `/overflowharness.html?w=390` | 390×900 mobile | light | Click Detail, then **Customize & advanced**; bottom Sheet, six segments, stacked fieldsets, actions, and Close visible. | viewport `screenshot` |
| `12-mobile-open-dark.png` | `/overflowharness.html?w=390` | 390×900 mobile | dark | Same expanded Sheet state. | viewport `screenshot` |
| `13-custom-selected-light.png` | `/overflowharness.html?w=1024` | 1024×900 | light | Open Detail, select Custom, keep selected segment, trigger summary, and editor visible. | viewport `screenshot` |
| `14-custom-selected-dark.png` | `/overflowharness.html?w=1024` | 1024×900 | dark | Same explicit Custom state. | viewport `screenshot` |
| `15-settings-phone-preview-light.png` | `/overflowharness.html?w=1200&settings=1` | 1200×1200 | light | Full page includes both stacked Desktop/Mobile Cards and the complete width-bearing Mobile preview canvas. | `screenshot` with `fullpage: true` |
| `16-settings-phone-preview-dark.png` | `/overflowharness.html?w=1200&settings=1` | 1200×1200 | dark | Same two complete Cards/canvases in dark. | `screenshot` with `fullpage: true` |

Save each capture under `proof_dir` with the exact filename. Record viewport, track/segment/canvas dimensions, Popover/Sheet containment, and no-inner-scroll measurements in `proof/README.md`. In a `finally` cleanup, call `job_stop` with `target` set to the exact `job_id` returned by the server's `exec_command`, `include_children=true`, and `max_wait_ms=5000`; then verify the server stopped. A detached or leftover dev server fails this step.

- [x] **Step 3: Obtain human visual approval before canonical gates**

Present all sixteen captures and measurements. Require explicit approval of both themes, selected/disabled/focus-visible/Custom states, 320px/390px frames, disabled Disclosure modes, Desktop closed/open, Mobile closed/open, and both complete stacked Settings cards. If review finds a defect, return to the owning task's RED/GREEN/review cycle; do not patch visually in this final task without a focused failing test.

- [x] **Step 4: Run canonical frontend gates**

From repository root:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
make test-web
make test-web-browser
```

Expected: both exit zero. A missing Chrome/dependency, timeout, or sandbox denial is incomplete, not a pass.

- [x] **Step 5: Run repository lint and static analysis**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
make lint
make vet
```

Expected: both exit zero. Read every warning and generated-output check; do not dismiss unrelated failures.

- [x] **Step 6: Run the full deterministic test gate**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
make test
```

Expected: exit zero. Read every warning, skip, nonzero subprocess, and retained-log path.

- [x] **Step 7: Run whole-branch specification and quality review**

Give an independent reviewer the approved spec, this plan, and the complete branch diff from `347f6ed9f`. Require explicit dispositions for all 21 acceptance criteria, all stated non-goals, the exact 320px mutation evidence, light/dark proof, and any Critical/Important findings. Fix every Critical/Important finding through a focused RED/GREEN cycle and rerun the affected canonical gate.

- [x] **Step 8: Mark accepted plan steps and verify final repository state**

After visual approval, all five canonical gates, and clean final review, mark every completed checkbox in this plan. Then run:

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" diff --check
git -C "$repo_root" status --short
git -C "$repo_root" log --oneline --decorate -15
```

Expected: only this plan's checkbox changes remain; no production/test mutation, scratch file, unexpected untracked file, or ignored server process remains.

- [x] **Step 9: Commit plan tracking only**

```bash
repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
git -C "$repo_root" add -- docs/superpowers/plans/2026-08-26-transcript-detail-design-system-correction.md
git -C "$repo_root" commit -m "docs: complete transcript detail design correction"
```

Expected: the final tracking commit contains only this plan. Report task commits, exact gate exits, mutation RED/restored GREEN evidence, proof path, final review verdict, HEAD, branch, and clean status.
