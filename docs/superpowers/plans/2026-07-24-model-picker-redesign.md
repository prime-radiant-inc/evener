# Model Picker Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the web model picker as one always-expanded grouped list with a pre-filled/pre-selected search input, in-place dim unavailable-provider lines, no Cancel button, and no scroll-dismiss — identically on desktop and mobile.

**Architecture:** Pure frontend work in `cmd/serf-hub/frontend`. `ModelCatalogPanel` stops composing the generic `Combobox` (whose contract is options-only rows and a popup that stays closed until you type) and owns its own ARIA-1.2 combobox input + grouped listbox. All row-shaping logic lands in a new pure module (`pickerRows.ts`) built on the existing `catalogView.ts` helpers, so the React component stays thin. `Popover` gains a `closeOnScroll` opt-out, and `ModelSwitch` drops its hand-rolled popover for the shared one — one popover implementation, two call sites.

**Tech Stack:** React 19, TypeScript 6 (strict, `noUncheckedIndexedAccess`), Vite, vitest + jsdom, @testing-library/react + user-event, biome. Design tokens in `src/styles/tokens.css`, enforced by `src/styles/token-contract.test.ts`.

**Spec:** `docs/superpowers/specs/2026-07-24-model-picker-redesign-design.md` (approved).

## Global Constraints

- All paths below are relative to the repo root; **all commands run from `cmd/serf-hub/frontend`** unless stated otherwise.
- **Color:** every color resolves to `var(--…)` from `src/styles/tokens.css` — never a hex/rgb/hsl/oklch/oklab/lab/lch literal, **including inside comments** (`src/styles/token-contract.test.ts` greps raw file text). `color-mix()` over token vars is allowed. This plan adds no new token.
- **Attention-hue allowlist:** `modelCatalog` is NOT in `SEMANTIC_USE_ALLOWLIST`, so its stylesheet may use only `--accent*`, `--surface-*`, `--ink-*`, `--edge`. Same for `panes/session/chrome/modelswitch.module.css`. Unavailable-provider lines use `--ink-low` (never `--danger`) — they are passive metadata, not an alarm.
- **Motion budget:** no new animation. `Popover`'s existing 120ms `--motion-duration-overlay` fade-scale is the only motion in this feature.
- **CSS-module class access:** never `styles.foo` in render — build a module-scope `CLASS` table through `requireClass(styles.foo, "<file>.module.css", "foo")`, matching every existing widget.
- **Test harness:** `npm run typecheck` (`tsc --noEmit`), `npm test` (`vitest run`), `npm run lint` (`biome ci src`). Single-file runs use `npx vitest run <path>`. Tests wrap components manually (no shared render helper) and call `cleanup()` per file.
- **Baseline is green:** the full vitest suite passes on this branch (3571 tests at the last gate). Every task below must leave `npm test` green — a newly-red unrelated test is a regression this plan caused, not pre-existing.
- **No Go change:** this plan touches only `cmd/serf-hub/frontend/**` and the two docs. `/api/models?diagnostics=1` already carries models + `recent` + `diagnostics`; if a task finds itself editing a `.go` file or `types.gen.ts`, stop.
- **Commit style:** one commit per task, imperative subject, no `--no-verify` ever (pre-commit hooks must run and pass).

## File Structure

| File | Responsibility | Task |
|---|---|---|
| `src/widgets/popover/index.tsx` | add `closeOnScroll?: boolean` (default true) | 1 |
| `src/widgets/popover/popover.test.tsx` | prove both scroll behaviors | 1 |
| `src/widgets/modelCatalog/pickerRows.ts` | **new**, pure: catalog + query → flat `PickerRow[]` (recent group, provider groups, unavailable lines) + per-row meta text | 2 |
| `src/widgets/modelCatalog/pickerRows.test.ts` | **new**, unit tests for the above | 2 |
| `src/widgets/modelCatalog/index.tsx` | rebuilt `ModelCatalogPanel` (own input + grouped listbox, no Cancel, `value` prop); `ModelCatalog` passes `value` + `closeOnScroll={false}` | 3 |
| `src/widgets/modelCatalog/modelCatalog.module.css` | panel/input/list/row/group/unavailable styles; drop recent-chip + diagnostics-toggle styles | 3 |
| `src/widgets/modelCatalog/modelCatalog.test.tsx` | rewritten behavior suite | 3 |
| `src/panes/spawn/ModelField.test.tsx` | Cancel test → Escape test | 3 |
| `src/panes/session/chrome/ModelSwitch.tsx` | migrate to shared `Popover closeOnScroll={false}`, pass `value`, drop `onCancel` + hand-rolled listeners | 4 |
| `src/panes/session/chrome/modelswitch.module.css` | drop `.anchor`/`.popover` + keyframes, add `.popoverPanel` | 4 |
| `src/panes/session/chrome/ModelSwitch.test.tsx` | updated for the new panel + shared popover | 4 |
| `src/panes/session/chrome/StatusRow.test.tsx` | composition test updated for the pre-filled input | 4 |

Unchanged on purpose: `catalogView.ts` (its `toCatalogOptions`/`filterCatalog`/`withGroupHeads`/`capabilityLabels`/`formatCost`/`contextWindowLabel` are reused verbatim), `catalogClient.ts`, `scopedCatalog.ts`, `widgets/combobox/**` (other pickers build on it), `widgets/index.ts` (exported names don't change), `src/dev/gallery-sections/modelCatalog.tsx`, `panes/spawn/ModelField.tsx`, `panes/settings/sections/launchShared/fields.tsx` (both already pass `value` into `ModelCatalog`).

---

## Task 1: `Popover` gains a `closeOnScroll` opt-out

`Popover` currently closes on any capture-phase window scroll and on resize (`src/widgets/popover/index.tsx:95-103`), which is defect #5: scrolling the picker's own list, or the page behind it, dismisses the picker. Menu-style popovers still want that behavior, so it becomes an opt-out rather than a removal.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/widgets/popover/index.tsx`
- Test: `cmd/serf-hub/frontend/src/widgets/popover/popover.test.tsx`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `PopoverProps.closeOnScroll?: boolean` (default `true`). When `false`, Popover registers **neither** the window `scroll` (capture) **nor** the `resize` close listener. Outside-click and Escape still close.

- [ ] **Step 1: Write the failing tests**

Append to `cmd/serf-hub/frontend/src/widgets/popover/popover.test.tsx`:

```tsx
// closeOnScroll: the model picker's own list scrolls, and a page scroll behind
// the panel must not dismiss a picker mid-interaction — so the scroll/resize
// close pair is an opt-out. Menu-shaped consumers keep the default.
test("by default a window scroll closes the panel", () => {
  const onClose = vi.fn();
  render(
    <Popover open onClose={onClose} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );

  window.dispatchEvent(new Event("scroll"));

  expect(onClose).toHaveBeenCalled();
});

test("closeOnScroll={false} keeps the panel open through a window scroll and a resize", () => {
  const onClose = vi.fn();
  render(
    <Popover open onClose={onClose} closeOnScroll={false} trigger={<button type="button">open</button>} data-testid="panel">
      <div>panel body</div>
    </Popover>,
  );

  window.dispatchEvent(new Event("scroll"));
  window.dispatchEvent(new Event("resize"));

  expect(onClose).not.toHaveBeenCalled();
  expect(screen.getByTestId("panel")).toBeTruthy();
});
```

Update that file's first import line to pull in `vi`:

```tsx
import { afterEach, expect, test, vi } from "vitest";
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npx vitest run src/widgets/popover/popover.test.tsx`
Expected: the default-scroll test PASSES (today's behavior), and `closeOnScroll={false}` FAILS — TypeScript/biome aside, `onClose` is called because the prop is ignored. In watch-free `vitest run` the failure reads `expected "spy" to not be called at all, but actually been called 2 times`.

- [ ] **Step 3: Implement the prop**

In `cmd/serf-hub/frontend/src/widgets/popover/index.tsx`, add the prop to the interface right after `autoFocus`:

```tsx
  /** When false, neither a window scroll (capture-phase) nor a viewport
   * resize closes the panel. For a panel whose own content scrolls and whose
   * interaction must survive a page scroll behind it — the model picker.
   * Trade-off: without the close, a page scroll can visually detach the
   * panel from its trigger, since placement is computed once per open.
   * Default true (Menu-shaped behavior). */
  closeOnScroll?: boolean;
```

Destructure it with its default:

```tsx
export function Popover({ open, onClose, trigger, children, autoFocus = true, closeOnScroll = true, ...rest }: PopoverProps) {
```

Gate the listener effect (replacing the existing `useEffect` at lines 93-103, comment included):

```tsx
  // A scroll anywhere (capture-phase) or a viewport resize closes the popover -
  // simpler than continuously repositioning, matching Menu. Consumers whose
  // panel content scrolls opt out with closeOnScroll={false}.
  useEffect(() => {
    if (!open || !closeOnScroll) return;
    window.addEventListener("scroll", onClose, true);
    window.addEventListener("resize", onClose);
    return () => {
      window.removeEventListener("scroll", onClose, true);
      window.removeEventListener("resize", onClose);
    };
  }, [open, onClose, closeOnScroll]);
```

Also amend the `onClose` doc comment on the interface (line ~23) so it stops promising scroll/resize unconditionally:

```tsx
  /** Fired on outside click, Escape, and — unless closeOnScroll is false —
   * scroll or resize. The trigger's own toggle is the consumer's job;
   * Popover never fires onClose for a click on the trigger itself (it's
   * inside the trigger wrapper, see below). */
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npx vitest run src/widgets/popover/popover.test.tsx`
Expected: PASS, 4 tests.

- [ ] **Step 5: Typecheck and lint**

Run: `npm run typecheck && npx biome ci src/widgets/popover`
Expected: no output from tsc, `Checked N files` with no diagnostics from biome.

- [ ] **Step 6: Commit**

```bash
git add src/widgets/popover/index.tsx src/widgets/popover/popover.test.tsx
git commit -m "popover: add closeOnScroll opt-out for panels whose content scrolls"
```

---

## Task 2: `pickerRows.ts` — the pure row builder

The redesigned panel renders one flat list of three row kinds: provider/Recent group heads, pickable model rows, and non-interactive unavailable-provider lines. All of that shaping is pure and belongs outside React, next to the existing `catalogView.ts` helpers it builds on.

**Files:**
- Create: `cmd/serf-hub/frontend/src/widgets/modelCatalog/pickerRows.ts`
- Test: `cmd/serf-hub/frontend/src/widgets/modelCatalog/pickerRows.test.ts`

**Interfaces:**
- Consumes: `CatalogOption`, `filterCatalog`, `toCatalogOptions`, `withGroupHeads`, `capabilityLabels`, `formatCost`, `contextWindowLabel` from `./catalogView`; `ModelCatalog`, `ModelCatalogDiagnostic`, `ModelCatalogEntry` types from `./index`.
- Produces:
  ```ts
  export type PickerRow =
    | { kind: "group"; key: string; label: string }
    | { kind: "model"; key: string; option: CatalogOption; meta: string }
    | { kind: "unavailable"; key: string; text: string };
  export type PickerModelRow = Extract<PickerRow, { kind: "model" }>;
  export function buildPickerRows(catalog: ModelCatalog | null, query: string): PickerRow[];
  export function pickableRows(rows: PickerRow[]): PickerModelRow[];
  export function unavailableLine(diag: ModelCatalogDiagnostic): string;
  export function rowMeta(entry: ModelCatalogEntry, withProvider: boolean): string;
  ```

- [ ] **Step 1: Write the failing tests**

Create `cmd/serf-hub/frontend/src/widgets/modelCatalog/pickerRows.test.ts`:

```ts
import { describe, expect, test } from "vitest";
import type { ModelCatalog, ModelCatalogEntry } from "./index";
import { buildPickerRows, pickableRows, rowMeta, unavailableLine } from "./pickerRows";

function entry(overrides: Partial<ModelCatalogEntry> = {}): ModelCatalogEntry {
  return {
    provider: "anthropic",
    model: "claude-sonnet-4-5",
    displayName: "Claude Sonnet 4.5",
    supportsTools: true,
    inputCostPerMillion: 3,
    outputCostPerMillion: 15,
    contextWindow: 200000,
    ...overrides,
  };
}

const SONNET = entry();
const GPT5 = entry({
  provider: "openai",
  model: "gpt-5",
  displayName: "GPT-5",
  inputCostPerMillion: 1.25,
  outputCostPerMillion: 10,
  contextWindow: 400000,
});

function catalog(overrides: Partial<ModelCatalog> = {}): ModelCatalog {
  return { models: [SONNET, GPT5], recent: [], ...overrides };
}

describe("buildPickerRows", () => {
  test("a null catalog yields no rows", () => {
    expect(buildPickerRows(null, "")).toEqual([]);
  });

  test("an empty query yields the FULL list, grouped by provider", () => {
    const rows = buildPickerRows(catalog(), "");
    expect(rows.map((r) => `${r.kind}:${r.kind === "model" ? r.option.qualified : r.kind === "group" ? r.label : r.text}`)).toEqual([
      "group:anthropic",
      "model:anthropic/claude-sonnet-4-5",
      "group:openai",
      "model:openai/gpt-5",
    ]);
  });

  test("recent is the FIRST group, and its rows carry the provider in the meta", () => {
    const rows = buildPickerRows(catalog({ recent: [GPT5] }), "");
    const first = rows[0];
    if (first?.kind !== "group") throw new Error("expected a group row first");
    expect(first.label).toBe("Recent");
    const recentRow = rows[1];
    if (recentRow?.kind !== "model") throw new Error("expected a model row after the Recent head");
    expect(recentRow.meta).toContain("openai");
  });

  test("no Recent group when the envelope carries none", () => {
    expect(buildPickerRows(catalog(), "").some((r) => r.kind === "group" && r.label === "Recent")).toBe(false);
  });

  test("a recent entry and its provider-group twin get DISTINCT keys", () => {
    const keys = buildPickerRows(catalog({ recent: [GPT5] }), "")
      .filter((r) => r.kind === "model")
      .map((r) => r.key);
    expect(new Set(keys).size).toBe(keys.length);
  });

  test("a query filters models and drops the group heads that are left empty", () => {
    const rows = buildPickerRows(catalog(), "sonnet");
    expect(rows.filter((r) => r.kind === "model")).toHaveLength(1);
    expect(rows.some((r) => r.kind === "group" && r.label === "openai")).toBe(false);
  });

  test("a query filters the recent group too", () => {
    const rows = buildPickerRows(catalog({ recent: [GPT5] }), "sonnet");
    expect(rows.some((r) => r.kind === "group" && r.label === "Recent")).toBe(false);
  });

  test("unavailable providers render as in-place lines after the available groups", () => {
    const rows = buildPickerRows(
      catalog({ diagnostics: [{ provider: "ollama", message: "connection refused", hint: "Is it running?" }] }),
      "",
    );
    const last = rows[rows.length - 1];
    if (last?.kind !== "unavailable") throw new Error("expected an unavailable row last");
    expect(last.text).toBe("ollama — connection refused — Is it running?");
  });

  test("an unavailable line survives a query that matches its provider, and filters out otherwise", () => {
    const withDiag = catalog({ diagnostics: [{ provider: "ollama", message: "connection refused" }] });
    expect(buildPickerRows(withDiag, "olla").some((r) => r.kind === "unavailable")).toBe(true);
    expect(buildPickerRows(withDiag, "sonnet").some((r) => r.kind === "unavailable")).toBe(false);
  });
});

describe("pickableRows", () => {
  test("keeps only model rows - group heads and unavailable lines are not options", () => {
    const rows = buildPickerRows(
      catalog({ recent: [GPT5], diagnostics: [{ provider: "ollama", message: "connection refused" }] }),
      "",
    );
    expect(pickableRows(rows).map((r) => r.option.qualified)).toEqual([
      "openai/gpt-5",
      "anthropic/claude-sonnet-4-5",
      "openai/gpt-5",
    ]);
  });
});

describe("rowMeta", () => {
  test("joins capabilities, cost, and context window with a middot", () => {
    expect(rowMeta(SONNET, false)).toBe("tools · $3 in · $15 out /Mtok · 200k");
  });

  test("leads with the provider when asked (the mixed-provider Recent group)", () => {
    expect(rowMeta(GPT5, true)).toBe("openai · tools · $1.25 in · $10 out /Mtok · 400k");
  });

  test("an entry with no metadata at all yields an empty string", () => {
    expect(rowMeta({ provider: "p", model: "m", displayName: "" }, false)).toBe("");
  });
});

describe("unavailableLine", () => {
  test("falls back to the title, then the source, when no provider is named", () => {
    expect(unavailableLine({ title: "Launch check", message: "no API key" })).toBe("Launch check — no API key");
    expect(unavailableLine({ source: "providers.toml", message: "no API key" })).toBe("providers.toml — no API key");
    expect(unavailableLine({ message: "no API key" })).toBe("provider — no API key");
  });
});
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npx vitest run src/widgets/modelCatalog/pickerRows.test.ts`
Expected: FAIL — `Failed to resolve import "./pickerRows"`.

- [ ] **Step 3: Implement the module**

Create `cmd/serf-hub/frontend/src/widgets/modelCatalog/pickerRows.ts`:

```ts
// The picker's list shape: one flat row array combining Recent, the
// provider-grouped models, and one dim in-place line per provider the hub
// couldn't reach. Pure (no React, no wire) and built on catalogView's
// filter/group helpers, so the panel component only maps rows to markup.
//
// Why rows and not nested groups: the panel is an ARIA listbox whose options
// must be linearly navigable by ArrowUp/Down. A flat array with a `kind`
// discriminant makes "skip the heads and the unavailable lines" a filter
// (pickableRows) instead of a tree walk.
import {
  type CatalogOption,
  capabilityLabels,
  contextWindowLabel,
  filterCatalog,
  formatCost,
  toCatalogOptions,
  withGroupHeads,
} from "./catalogView";
import type { ModelCatalog, ModelCatalogDiagnostic, ModelCatalogEntry } from "./index";

export type PickerRow =
  | { kind: "group"; key: string; label: string }
  | { kind: "model"; key: string; option: CatalogOption; meta: string }
  | { kind: "unavailable"; key: string; text: string };

/** The pickable row kind - the only one that becomes a listbox option. */
export type PickerModelRow = Extract<PickerRow, { kind: "model" }>;

/** The Recent group's head. Recent is a pseudo-provider: it mixes providers,
 * so its rows lead their meta with the provider name. */
const RECENT_GROUP = "Recent";

/** One row's small-text metadata: capabilities, cost, context window - and,
 * for the mixed-provider Recent group, the provider first. Empty when the
 * entry carries no metadata at all (a model the embedded catalog doesn't
 * know), so the row renders as just its name rather than a stray separator. */
export function rowMeta(entry: ModelCatalogEntry, withProvider: boolean): string {
  const parts: string[] = [];
  if (withProvider) parts.push(entry.provider);
  parts.push(...capabilityLabels(entry));
  const cost = formatCost(entry);
  if (cost !== null) parts.push(cost);
  const context = contextWindowLabel(entry);
  if (context !== null) parts.push(context);
  return parts.join(" · ");
}

/** A diagnostic as one line: who, what, and what to do about it. The label
 * prefers the provider name (that's what the user was looking for in the
 * list), falling back to the diagnostic's own title/source before a generic
 * word - a launch check that names neither still reads as a sentence. */
export function unavailableLine(diag: ModelCatalogDiagnostic): string {
  const label = diag.provider || diag.title || diag.source || "provider";
  const hint = diag.hint ? ` — ${diag.hint}` : "";
  return `${label} — ${diag.message}${hint}`;
}

/** An unavailable line is only about its provider, so it survives a query
 * that matches that provider's name and filters out like any other non-match
 * otherwise. */
function diagnosticMatches(diag: ModelCatalogDiagnostic, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  return (diag.provider ?? "").toLowerCase().includes(q);
}

/**
 * The full list for a given query: Recent first (when non-empty), then the
 * provider groups in the server's own order, then the unavailable providers.
 * An empty query yields everything - the list is always expanded, never
 * gated behind a keystroke.
 *
 * Keys are prefixed by section and suffixed by position, so a model that
 * appears BOTH in Recent and in its provider group gets two distinct DOM
 * ids, and a provider that (unsorted) opens two runs doesn't collide.
 */
export function buildPickerRows(catalog: ModelCatalog | null, query: string): PickerRow[] {
  if (!catalog) return [];
  const rows: PickerRow[] = [];

  const recent = filterCatalog(toCatalogOptions(catalog.recent), query);
  if (recent.length > 0) {
    rows.push({ kind: "group", key: `group:${rows.length}:${RECENT_GROUP}`, label: RECENT_GROUP });
    for (const option of recent) {
      rows.push({ kind: "model", key: `recent:${rows.length}:${option.qualified}`, option, meta: rowMeta(option.entry, true) });
    }
  }

  for (const option of withGroupHeads(filterCatalog(toCatalogOptions(catalog.models), query))) {
    if (option.groupHead !== undefined) {
      rows.push({ kind: "group", key: `group:${rows.length}:${option.groupHead}`, label: option.groupHead });
    }
    rows.push({ kind: "model", key: `model:${rows.length}:${option.qualified}`, option, meta: rowMeta(option.entry, false) });
  }

  for (const diag of catalog.diagnostics ?? []) {
    if (!diagnosticMatches(diag, query)) continue;
    rows.push({ kind: "unavailable", key: `unavailable:${rows.length}:${diag.provider ?? ""}`, text: unavailableLine(diag) });
  }

  return rows;
}

/** The rows the keyboard walks and a click can pick: models only. Group heads
 * and unavailable lines are text, not options. */
export function pickableRows(rows: PickerRow[]): PickerModelRow[] {
  return rows.filter((row): row is PickerModelRow => row.kind === "model");
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `npx vitest run src/widgets/modelCatalog/pickerRows.test.ts`
Expected: PASS, 15 tests.

- [ ] **Step 5: Typecheck and lint**

Run: `npm run typecheck && npx biome ci src/widgets/modelCatalog`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add src/widgets/modelCatalog/pickerRows.ts src/widgets/modelCatalog/pickerRows.test.ts
git commit -m "modelCatalog: pure row builder for the grouped picker list"
```

---

## Task 3: Rebuild `ModelCatalogPanel`

The panel becomes an input over one always-expanded, internally-scrolling grouped list. This task lands every defect fix that lives in the widget: no Cancel, in-place unavailable lines, pre-filled + fully-selected input, list expanded on open, and `closeOnScroll={false}` at the `ModelCatalog` call site.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/widgets/modelCatalog/index.tsx`
- Modify: `cmd/serf-hub/frontend/src/widgets/modelCatalog/modelCatalog.module.css`
- Test: `cmd/serf-hub/frontend/src/widgets/modelCatalog/modelCatalog.test.tsx` (rewrite)
- Test: `cmd/serf-hub/frontend/src/panes/spawn/ModelField.test.tsx` (replace its Cancel test)

**Interfaces:**
- Consumes: `buildPickerRows`, `pickableRows`, `type PickerModelRow` from `./pickerRows` (Task 2); `Popover`'s `closeOnScroll` (Task 1).
- Produces:
  ```ts
  export interface ModelCatalogPanelProps {
    loading: boolean;
    error: string | null;
    catalog: ModelCatalog | null;
    /** The current qualified "provider/model" (or "" for the harness
     * default): pre-fills the input, marks the current row, scrolls it in. */
    value: string;
    onPick: (entry: ModelCatalogEntry) => void;
  }
  ```
  `onCancel` is gone. `ModelCatalogProps` (`value`/`onChange`/`loadCatalog`) is unchanged.

- [ ] **Step 1: Write the failing test suite**

Replace the whole contents of `cmd/serf-hub/frontend/src/widgets/modelCatalog/modelCatalog.test.tsx` with:

```tsx
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test, vi } from "vitest";
// ModelCatalog is both the component (value) and the envelope interface (type);
// a single import brings in both meanings via declaration merging.
import { ModelCatalog, type ModelCatalogEntry } from "./index";

afterEach(() => cleanup());

const SONNET: ModelCatalogEntry = {
  provider: "anthropic",
  model: "claude-sonnet-4-5",
  displayName: "Claude Sonnet 4.5",
  supportsTools: true,
  supportsVision: true,
  supportsReasoning: true,
  inputCostPerMillion: 3,
  outputCostPerMillion: 15,
  contextWindow: 200000,
};
const HAIKU: ModelCatalogEntry = {
  provider: "anthropic",
  model: "claude-haiku-4-5",
  displayName: "Claude Haiku 4.5",
  supportsTools: true,
  inputCostPerMillion: 1,
  outputCostPerMillion: 5,
  contextWindow: 200000,
};
const GPT5: ModelCatalogEntry = {
  provider: "openai",
  model: "gpt-5",
  displayName: "GPT-5",
  supportsTools: true,
  inputCostPerMillion: 1.25,
  outputCostPerMillion: 10,
  contextWindow: 400000,
};
const CATALOG: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [] };

function renderPicker(props: Partial<Parameters<typeof ModelCatalog>[0]> = {}) {
  const onChange = props.onChange ?? vi.fn();
  render(
    <ModelCatalog
      value={props.value ?? ""}
      onChange={onChange}
      loadCatalog={props.loadCatalog ?? vi.fn().mockResolvedValue(CATALOG)}
    />,
  );
  return { onChange };
}

function openTrigger() {
  return screen.getByRole("button", { name: /change model/i });
}

async function openPicker(user: ReturnType<typeof userEvent.setup>): Promise<HTMLInputElement> {
  await user.click(openTrigger());
  return (await screen.findByRole("combobox", { name: "Model" })) as HTMLInputElement;
}

// --- closed state (unchanged: the chip IS the trigger) ---------------------

test("shows the interim default marker when no model is chosen", () => {
  renderPicker();
  expect(screen.getByText("(default)")).toBeTruthy();
});

test("shows the qualified provider/model when a model is set", () => {
  renderPicker({ value: "openai/gpt-5" });
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
});

test("the closed state has no separate Change-model button - the chip itself is the trigger", () => {
  renderPicker({ value: "openai/gpt-5" });
  expect(screen.queryByRole("button", { name: "Change model" })).toBeNull();
  expect(openTrigger()).toBeTruthy();
});

test("clicking the chip trigger opens the panel as a portaled overlay, not an inline sibling that reflows", async () => {
  const user = userEvent.setup();
  renderPicker({ value: "openai/gpt-5" });

  const triggerWrapper = openTrigger().parentElement;
  expect(triggerWrapper).not.toBeNull();
  const combo = await openPicker(user);

  expect(openTrigger().parentElement).toBe(triggerWrapper);
  let ancestor: HTMLElement | null = combo.parentElement;
  let reachedBody = false;
  while (ancestor) {
    expect(ancestor).not.toBe(triggerWrapper);
    if (ancestor === document.body) {
      reachedBody = true;
      break;
    }
    ancestor = ancestor.parentElement;
  }
  expect(reachedBody).toBe(true);
});

// --- the list is expanded on open, no keystroke needed ---------------------

describe("open state", () => {
  test("renders the FULL grouped list immediately, with no typing or arrow key", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.getByText("openai")).toBeTruthy();
  });

  test("an option carries its capabilities, cost, and context window as small text", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    const row = (await screen.findByText("Claude Sonnet 4.5")).closest("li");
    if (!row) throw new Error("expected the Sonnet option to render inside a listbox <li>");

    expect(within(row).getByText("tools · vision · reasoning · $3 in · $15 out /Mtok · 200k")).toBeTruthy();
  });

  test("there is no Cancel button", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    expect(screen.queryByRole("button", { name: "Cancel" })).toBeNull();
  });

  test("surfaces an inline error when the catalog fails to load", async () => {
    const user = userEvent.setup();
    renderPicker({ loadCatalog: vi.fn().mockRejectedValue(new Error("providers unavailable")) });

    await user.click(openTrigger());

    expect(await screen.findByText(/providers unavailable/i)).toBeTruthy();
  });
});

// --- the input replaces the previously-selected value ---------------------

describe("input pre-fill", () => {
  test("opens pre-filled with the current qualified value, focused, and fully selected", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    const combo = await openPicker(user);

    await waitFor(() => expect(document.activeElement).toBe(combo));
    expect(combo.value).toBe("openai/gpt-5");
    expect(combo.selectionStart).toBe(0);
    expect(combo.selectionEnd).toBe("openai/gpt-5".length);
  });

  test("the pre-filled value does NOT pre-filter the list", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  });

  test("the first keystroke REPLACES the pre-filled value", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    const combo = await openPicker(user);
    // Type into the already-focused input (never user.type, which clicks
    // first and collapses the selection to the caret) - this is exactly the
    // keystroke-over-selection the pre-fill exists for.
    await user.keyboard("haiku");

    expect(combo.value).toBe("haiku");
  });
});

// --- typing filters in place -----------------------------------------------

describe("filtering", () => {
  test("narrows the list and drops the group heads left empty", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await user.keyboard("sonnet");

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    expect(screen.getByText("anthropic")).toBeTruthy();
    expect(screen.queryByText("openai")).toBeNull();
  });

  test("clearing the query restores the full list", async () => {
    const user = userEvent.setup();
    renderPicker();

    const combo = await openPicker(user);
    await user.keyboard("sonnet");
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
    await user.clear(combo);

    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  });
});

// --- Recent is the first group --------------------------------------------

describe("Recent group", () => {
  test("renders first, with the provider in the row's small text, and picks without typing", async () => {
    const user = userEvent.setup();
    const withRecent: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [GPT5] };
    const { onChange } = renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withRecent) });

    await openPicker(user);
    const heads = await screen.findAllByText(/^(Recent|anthropic|openai)$/);
    expect(heads[0]?.textContent).toBe("Recent");
    const options = screen.getAllByRole("option");
    expect(options[0]?.textContent).toContain("GPT-5");
    expect(options[0]?.textContent).toContain("openai");

    await user.click(options[0] as HTMLElement);

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  });

  test("no Recent group renders when the envelope carries none", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    expect(screen.queryByText("Recent")).toBeNull();
  });
});

// --- unavailable providers, in place, in small text ------------------------

describe("unavailable providers", () => {
  test("render as non-interactive in-place lines carrying message and hint", async () => {
    const user = userEvent.setup();
    const withDiag: ModelCatalog = {
      models: [SONNET],
      recent: [],
      diagnostics: [{ provider: "ollama", message: "connection refused", hint: "Is it running?" }],
    };
    renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withDiag) });

    await openPicker(user);

    const line = await screen.findByText("ollama — connection refused — Is it running?");
    expect(line.closest("li")?.getAttribute("role")).toBe("presentation");
    // No toggle button gating them anymore.
    expect(screen.queryByRole("button", { name: /unavailable/i })).toBeNull();
  });

  test("no unavailable lines render when the envelope reports none", async () => {
    const user = userEvent.setup();
    renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    // Scoped to the list: the trigger's own "— change model" screen-reader
    // text carries an em dash too, and it is not a diagnostic.
    expect(within(screen.getByRole("listbox")).queryByText(/—/)).toBeNull();
  });
});

// --- the current value is marked and scrolled into view -------------------

describe("current value", () => {
  test("marks the current row with aria-selected and a check glyph", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);

    const current = await waitFor(() => screen.getByRole("option", { selected: true }));
    expect(current.textContent).toContain("GPT-5");
    expect(within(current).getByText("✓")).toBeTruthy();
  });

  // A single-select listbox may have exactly ONE aria-selected option, but the
  // current model legitimately appears twice when it's also in Recent.
  test("marks only the FIRST occurrence when the current model is also in Recent", async () => {
    const user = userEvent.setup();
    const withRecent: ModelCatalog = { models: [SONNET, HAIKU, GPT5], recent: [GPT5] };
    renderPicker({ value: "openai/gpt-5", loadCatalog: vi.fn().mockResolvedValue(withRecent) });

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(4));

    const selected = screen.getAllByRole("option", { selected: true });
    expect(selected).toHaveLength(1);
    expect(selected[0]).toBe(screen.getAllByRole("option")[0]); // the Recent copy
  });

  test("scrolls the current row into view on open", async () => {
    const user = userEvent.setup();
    // jsdom implements no scrollIntoView at all, so the panel calls it
    // optionally; stub it to observe the call.
    const scrollSpy = vi.fn();
    HTMLElement.prototype.scrollIntoView = scrollSpy as unknown as typeof HTMLElement.prototype.scrollIntoView;
    try {
      renderPicker({ value: "openai/gpt-5" });
      await openPicker(user);
      await waitFor(() => expect(scrollSpy).toHaveBeenCalled());
    } finally {
      // @ts-expect-error restore jsdom's honest absence of scrollIntoView
      delete HTMLElement.prototype.scrollIntoView;
    }
  });
});

// --- keyboard --------------------------------------------------------------

describe("keyboard", () => {
  test("ArrowDown/ArrowUp walk the options and never land on a group head or an unavailable line", async () => {
    const user = userEvent.setup();
    const withDiag: ModelCatalog = {
      models: [SONNET, HAIKU, GPT5],
      recent: [],
      diagnostics: [{ provider: "ollama", message: "connection refused" }],
    };
    renderPicker({ loadCatalog: vi.fn().mockResolvedValue(withDiag) });

    const combo = await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));

    const activeText = () => {
      const id = combo.getAttribute("aria-activedescendant");
      return id ? document.getElementById(id)?.textContent : null;
    };
    await user.keyboard("{ArrowDown}");
    expect(activeText()).toContain("Claude Sonnet 4.5");
    // Past the end of the list: clamps on the LAST option, never the
    // unavailable line that follows it.
    await user.keyboard("{ArrowDown}{ArrowDown}{ArrowDown}{ArrowDown}");
    expect(activeText()).toContain("GPT-5");
    await user.keyboard("{ArrowUp}");
    expect(activeText()).toContain("Claude Haiku 4.5");
  });

  test("Home and End jump to the first and last option", async () => {
    const user = userEvent.setup();
    renderPicker();

    const combo = await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    const activeText = () => {
      const id = combo.getAttribute("aria-activedescendant");
      return id ? document.getElementById(id)?.textContent : null;
    };

    await user.keyboard("{End}");
    expect(activeText()).toContain("GPT-5");
    await user.keyboard("{Home}");
    expect(activeText()).toContain("Claude Sonnet 4.5");
  });

  test("Enter picks the highlighted option", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker();

    await openPicker(user);
    await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
    await user.keyboard("gpt{Enter}");

    expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
    expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull();
  });

  test("Escape closes the picker without changing the value", async () => {
    const user = userEvent.setup();
    const { onChange } = renderPicker({ value: "openai/gpt-5" });

    await openPicker(user);
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
    expect(screen.getByText("openai/gpt-5")).toBeTruthy();
    expect(onChange).not.toHaveBeenCalled();
  });

  // Popover's FocusScope is opted out of focus management (autoFocus={false})
  // so the panel's input can own focus AND its text selection - which makes
  // returning focus to the trigger on close ModelCatalog's own job. Without
  // it, focus falls to <body> and a keyboard user is stranded.
  test("closing returns focus to the trigger", async () => {
    const user = userEvent.setup();
    renderPicker({ value: "openai/gpt-5" });
    const trigger = openTrigger();

    await openPicker(user);
    await user.keyboard("{Escape}");

    await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
    expect(document.activeElement).toBe(trigger);
  });
});

// --- picking with the mouse -----------------------------------------------

test("clicking an option reports the qualified id and closes the picker", async () => {
  const user = userEvent.setup();
  const { onChange } = renderPicker();

  await openPicker(user);
  await user.click(await screen.findByText("GPT-5"));

  expect(onChange).toHaveBeenCalledWith("openai/gpt-5");
  expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull();
});

// --- scrolling never dismisses -------------------------------------------

test("a scroll does not dismiss the open picker", async () => {
  const user = userEvent.setup();
  renderPicker();

  await openPicker(user);
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  window.dispatchEvent(new Event("scroll"));

  expect(screen.getByRole("combobox", { name: "Model" })).toBeTruthy();
  expect(screen.getAllByRole("option")).toHaveLength(3);
});
```

- [ ] **Step 2: Run the suite to verify it fails**

Run: `npx vitest run src/widgets/modelCatalog/modelCatalog.test.tsx`
Expected: FAIL. The pre-fill, expanded-list, unavailable-line, aria-selected, Home/End, and no-Cancel tests all fail against today's `Combobox`-based panel (which opens with an empty query and a closed popup).

- [ ] **Step 3: Rewrite the panel**

Replace `cmd/serf-hub/frontend/src/widgets/modelCatalog/index.tsx` from its import block through the end of `ModelCatalogPanel` — keep the `ModelCatalogEntry` / `ModelCatalogDiagnostic` / `ModelCatalog` / `ModelCatalogProps` interfaces and `errorMessage` exactly as they are, and keep `ModelCatalog` below with the two edits in Step 4. The new file head and panel:

```tsx
// The rich model catalog picker. A search input over ONE always-expanded,
// internally-scrolling list: Recent first, then the provider groups, then a
// dim in-place line per provider the hub couldn't reach. value/onChange
// MIRROR the interim ModelField contract (value is a qualified
// "provider/model" or "" for the harness default); loadCatalog is injected
// (harness-scoped at the spawn site, unscoped at the settings site), so the
// widget itself is wire-free and both swap sites drop it in with a
// one-import change.
//
// This panel deliberately does NOT use widgets/combobox: that widget's
// contract is options-only rows and a popup that stays closed until you type,
// and this picker needs group heads, non-interactive diagnostic lines, and a
// list that is expanded the moment it opens. Combobox stays as-is for the
// pickers that do fit it (pathpicker).
import { type JSX, type KeyboardEvent, useEffect, useId, useMemo, useRef, useState } from "react";
// Import siblings directly, never through the widgets barrel: this module is
// itself barrel-exported, so importing the barrel here would be a cycle (the
// same reason collectioneditor/pathpicker import ../button directly).
import { Chip } from "../chip";
import { requireClass } from "../internal/requireClass";
import { Popover } from "../popover";
import { Skeleton } from "../skeleton";
import styles from "./modelCatalog.module.css";
import { buildPickerRows, pickableRows } from "./pickerRows";

const CLASS = {
  trigger: requireClass(styles.trigger, "modelCatalog.module.css", "trigger"),
  chevron: requireClass(styles.chevron, "modelCatalog.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelCatalog.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelCatalog.module.css", "popoverPanel"),
  panel: requireClass(styles.panel, "modelCatalog.module.css", "panel"),
  input: requireClass(styles.input, "modelCatalog.module.css", "input"),
  error: requireClass(styles.error, "modelCatalog.module.css", "error"),
  list: requireClass(styles.list, "modelCatalog.module.css", "list"),
  groupRow: requireClass(styles.groupRow, "modelCatalog.module.css", "groupRow"),
  row: requireClass(styles.row, "modelCatalog.module.css", "row"),
  rowActive: requireClass(styles.rowActive, "modelCatalog.module.css", "rowActive"),
  rowName: requireClass(styles.rowName, "modelCatalog.module.css", "rowName"),
  check: requireClass(styles.check, "modelCatalog.module.css", "check"),
  meta: requireClass(styles.meta, "modelCatalog.module.css", "meta"),
  unavailable: requireClass(styles.unavailable, "modelCatalog.module.css", "unavailable"),
};

const SKELETON_LINES = 4;
```

Keep the four exported interfaces and `errorMessage` where they are, then replace the old `CatalogRow` + `ModelCatalogPanelProps` + `ModelCatalogPanel` with:

```tsx
export interface ModelCatalogPanelProps {
  loading: boolean;
  error: string | null;
  catalog: ModelCatalog | null;
  /** The current qualified "provider/model" (or "" for the harness default):
   * pre-fills the input, marks the current row, and scrolls it into view. */
  value: string;
  onPick: (entry: ModelCatalogEntry) => void;
}

function rowDomId(listboxId: string, key: string): string {
  return `${listboxId}-${key}`;
}

/**
 * The open picker's content only (input + grouped list) with no trigger or
 * closed-state rendering of its own - extracted so a caller that needs its
 * own trigger affordance (ModelSwitch's status-row chip, ModelCatalog's own
 * chip-as-button trigger below, both opening this as a floating popover) can
 * reuse the rich rendering without duplicating it.
 *
 * The ARIA 1.2 combobox-with-listbox pattern: role="combobox" on the input,
 * a role="listbox" sibling, aria-activedescendant tracking the highlighted
 * option - real DOM focus never leaves the input, so typing is never
 * interrupted. Unlike widgets/combobox the listbox is ALWAYS shown while the
 * panel is open (aria-expanded stays true): the panel itself is the popup,
 * and an empty picker over a hidden list was defect #4.
 *
 * Dismissal is the enclosing Popover's job (Escape bubbles to its panel
 * handler, outside-click is its document listener). There is no Cancel
 * button, and no blur handler: focus staying in the input is the whole point
 * of the activedescendant pattern.
 */
export function ModelCatalogPanel({ loading, error, catalog, value, onPick }: ModelCatalogPanelProps): JSX.Element {
  // null means "the user hasn't typed yet": the input SHOWS the current value
  // (selected, so the first keystroke replaces it) while the list stays
  // unfiltered. Once typing starts, the typed text is both the input's value
  // and the query - including when it's cleared back to "".
  const [typed, setTyped] = useState<string | null>(null);
  const [activeIndex, setActiveIndex] = useState(-1);
  const inputRef = useRef<HTMLInputElement>(null);
  const listboxId = useId();

  const text = typed ?? value;
  const query = typed ?? "";
  const rows = useMemo(() => buildPickerRows(catalog, query), [catalog, query]);
  const picks = useMemo(() => pickableRows(rows), [rows]);
  const activeKey = activeIndex >= 0 && activeIndex < picks.length ? picks[activeIndex]?.key : undefined;
  // The current model can appear TWICE (once under Recent, once under its
  // provider group), but a single-select listbox may have exactly one
  // aria-selected option - so the marker goes on the first occurrence only.
  const currentKey = useMemo(() => picks.find((row) => row.option.qualified === value)?.key, [picks, value]);
  const listShown = !loading && error === null;

  // Focus the input and select all of it, so the first keystroke replaces the
  // pre-filled value wholesale. Mount-only: the panel stays mounted across
  // loading -> loaded, and re-selecting then would fight a user already typing.
  useEffect(() => {
    const input = inputRef.current;
    if (!input) return;
    input.focus();
    input.select();
  }, []);

  // Until the user types, the highlight follows the CURRENT value (so the
  // list opens showing where you already are, and ArrowDown continues from
  // there). This only re-runs when the rows or the value change - arrow keys
  // move the highlight without fighting it, since they change neither.
  useEffect(() => {
    if (typed !== null) return;
    setActiveIndex(picks.findIndex((row) => row.option.qualified === value));
  }, [picks, value, typed]);

  // Keep the highlighted row visible inside the list's own scroll container -
  // both for the current value on open and for keyboard walks past the fold.
  // scrollIntoView is called optionally: jsdom implements none at all.
  useEffect(() => {
    if (activeKey === undefined) return;
    document.getElementById(rowDomId(listboxId, activeKey))?.scrollIntoView?.({ block: "nearest" });
  }, [activeKey, listboxId]);

  function pickAt(index: number): boolean {
    const row = picks[index];
    if (!row) return false;
    onPick(row.option.entry);
    return true;
  }

  function handleKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    switch (event.key) {
      case "ArrowDown":
        event.preventDefault();
        if (picks.length === 0) break;
        setActiveIndex((current) => Math.min(current + 1, picks.length - 1));
        break;
      case "ArrowUp":
        event.preventDefault();
        if (picks.length === 0) break;
        setActiveIndex((current) => Math.max(current - 1, 0));
        break;
      case "Home":
        if (picks.length === 0) break;
        event.preventDefault();
        setActiveIndex(0);
        break;
      case "End":
        if (picks.length === 0) break;
        event.preventDefault();
        setActiveIndex(picks.length - 1);
        break;
      case "Enter": {
        if (pickAt(activeIndex)) {
          event.preventDefault();
          break;
        }
        // Nothing highlighted (no current value, nothing typed yet): an
        // exactly-typed id or display name is still an unambiguous answer.
        const wanted = text.trim();
        const exact = picks.findIndex((row) => row.option.qualified === wanted || row.option.label === wanted);
        if (exact >= 0) {
          event.preventDefault();
          pickAt(exact);
        }
        break;
      }
      default:
        break;
    }
  }

  return (
    <div className={CLASS.panel}>
      <input
        ref={inputRef}
        role="combobox"
        className={CLASS.input}
        value={text}
        onChange={(event) => {
          setTyped(event.target.value);
          // The first match is the answer the user is narrowing toward, so
          // Enter right after typing picks it.
          setActiveIndex(0);
        }}
        onKeyDown={handleKeyDown}
        aria-expanded={listShown}
        aria-autocomplete="list"
        aria-controls={listShown ? listboxId : undefined}
        aria-activedescendant={activeKey !== undefined ? rowDomId(listboxId, activeKey) : undefined}
        aria-label="Model"
      />
      {loading && <Skeleton lines={SKELETON_LINES} />}
      {error !== null && (
        <p className={CLASS.error} role="alert">
          {error}
        </p>
      )}
      {listShown && (
        // <ul role="listbox">/<li role="option"> is the WAI-ARIA APG
        // combobox-with-listbox-popup pattern's own example markup
        // (w3.org/WAI/ARIA/apg/patterns/combobox) - not an interactive role
        // bolted onto an arbitrary static element, Biome's role-vs-element
        // heuristic just doesn't special-case ul/li for it.
        <ul
          // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ul/li is the ARIA APG's own listbox markup, see above
          role="listbox"
          id={listboxId}
          aria-label="Model"
          className={CLASS.list}
        >
          {rows.map((row) => {
            if (row.kind === "group") {
              return (
                <li key={row.key} role="presentation" className={CLASS.groupRow}>
                  {row.label}
                </li>
              );
            }
            if (row.kind === "unavailable") {
              return (
                <li key={row.key} role="presentation" className={CLASS.unavailable}>
                  {row.text}
                </li>
              );
            }
            const current = row.key === currentKey;
            return (
              // Real focus never leaves the input (ARIA 1.2 activedescendant
              // pattern): aria-activedescendant above tracks the "virtual"
              // active option, and handleKeyDown's own Enter case already
              // calls this same pick - so this <li> is deliberately not
              // focusable and needs no onKeyDown of its own.
              // biome-ignore lint/a11y/useFocusableInteractive: activedescendant pattern, real focus stays on the input, see above
              // biome-ignore lint/a11y/useKeyWithClickEvents: activedescendant pattern, Enter on the input already does this, see above
              <li
                key={row.key}
                id={rowDomId(listboxId, row.key)}
                // biome-ignore lint/a11y/noNoninteractiveElementToInteractiveRole: ARIA APG listbox markup, see the ul above
                role="option"
                aria-selected={current}
                className={`${CLASS.row} ${row.key === activeKey ? CLASS.rowActive : ""}`}
                // Selecting with the mouse must not blur the input (the ARIA
                // 1.2 pattern keeps real focus there throughout).
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => onPick(row.option.entry)}
              >
                <span className={CLASS.rowName}>{row.option.label}</span>
                {current && (
                  <span className={CLASS.check} aria-hidden="true">
                    ✓
                  </span>
                )}
                {row.meta !== "" && <span className={CLASS.meta}>{row.meta}</span>}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
```

- [ ] **Step 4: Update the `ModelCatalog` wrapper**

Three changes here, all in `ModelCatalog`: `closeOnScroll={false}`, `autoFocus={false}`, and an explicit trigger-refocus on close.

`autoFocus={false}` is required, not cosmetic. `FocusScope` (which `Popover` wraps its panel in) captures `document.activeElement` as its restore target *on mount* and moves focus to the first tabbable descendant. With the panel focusing its own input in a layout-ordered effect, FocusScope's captured "restore target" ends up being the panel's own input — so on close it restores focus to a node that just unmounted, and focus lands on `<body>` instead of the trigger. `autoFocus={false}` makes FocusScope keep its hands off focus entirely (the documented combobox opt-out, `widgets/focusscope/index.tsx:37-53`), and `closePicker` then returns focus to the trigger itself. Verified empirically in jsdom: without this pair, `document.activeElement` after Escape is `document.body`.

Add a ref for the trigger and refocus it in `closePicker`:

```tsx
export function ModelCatalog({ value, onChange, loadCatalog }: ModelCatalogProps): JSX.Element {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [catalog, setCatalog] = useState<ModelCatalog | null>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
```

```tsx
  // Popover's FocusScope is opted out of focus management entirely
  // (autoFocus={false}) so the panel's own input can hold focus and its
  // selection, which means returning focus to the trigger on close is this
  // component's job - otherwise focus falls to <body>.
  function closePicker() {
    setOpen(false);
    triggerRef.current?.focus();
  }

  function pick(entry: ModelCatalogEntry) {
    setOpen(false);
    onChange(`${entry.provider}/${entry.model}`);
  }
```

`pick` deliberately does NOT refocus the trigger: picking is a completed choice, and yanking focus back to the chip on every pick fights a keyboard user tabbing onward through the form.

Then the `return` at the end of the file:

```tsx
  return (
    <Popover
      open={open}
      onClose={closePicker}
      // The picker's own list scrolls, and a page scroll behind it must not
      // dismiss a picker mid-interaction (defect #5).
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePicker for why FocusScope must not manage focus here.
      autoFocus={false}
      trigger={
        <button
          ref={triggerRef}
          type="button"
          className={CLASS.trigger}
          onClick={() => (open ? closePicker() : void openPicker())}
        >
          <Chip>{value === "" ? "(default)" : value}</Chip>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel loading={loading} error={error} catalog={catalog} value={value} onPick={pick} />
      </div>
    </Popover>
  );
```

Add `useRef` to the React import at the top of the file (Step 3's import line already lists it).

- [ ] **Step 5: Restyle the panel**

In `cmd/serf-hub/frontend/src/widgets/modelCatalog/modelCatalog.module.css`, keep `.trigger`, `.trigger:hover`, `.trigger:focus-visible`, `.chevron`, `.srOnly` unchanged. Replace everything from `.popoverPanel` to the end of the file with:

```css
/* The floating panel's own content padding/sizing - widgets/popover's shared
 * chassis supplies position/background/border/radius/animation; this widget
 * only adds the padding and a sane width so the search field and the grouped
 * list don't read as cramped. Width is capped against the viewport so the
 * same panel works on a phone without a separate mobile rendering. */
.popoverPanel {
  padding: var(--space-3);
  width: 380px;
  max-width: calc(100vw - var(--space-4));
}

/* The open picker: the search field over one internally-scrolling list. */
.panel {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

/* Matches Input/Combobox's own field recipe (32px control height, hairline
 * edge, control radius) so the picker's field is the same object the rest of
 * the app's forms use. */
.input {
  display: block;
  width: 100%;
  height: 32px;
  padding: 0 var(--space-3);
  border: 1px solid var(--edge);
  border-radius: var(--radius-control);
  background: var(--surface-1);
  color: var(--ink-hi);
  font-family: var(--font-sans);
  font-size: var(--font-size-ui);
}

.input:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}

.error {
  margin: 0;
  color: var(--ink-mid);
  font-size: var(--font-size-ui);
}

/* The list scrolls INTERNALLY (the popover never grows past the viewport,
 * and scrolling here never dismisses the picker - see closeOnScroll at the
 * ModelCatalog call site). */
.list {
  margin: 0;
  padding: 0;
  max-height: min(320px, 60vh);
  overflow-y: auto;
  list-style: none;
}

/* Provider (and Recent) section eyebrow: small, quiet, not an option. */
.groupRow {
  padding: var(--space-2) var(--space-2) var(--space-1);
  color: var(--ink-mid);
  font-size: var(--font-size-caption);
  font-weight: var(--font-weight-medium);
  text-transform: uppercase;
  letter-spacing: 0.04em;
}

.row {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  padding: var(--space-1) var(--space-2);
  border-radius: var(--radius-control);
  color: var(--ink-hi);
  font-size: var(--font-size-ui);
  cursor: pointer;
}

.row:hover {
  background: var(--surface-2);
}

/* The active option is a virtual (aria-activedescendant), not real DOM focus -
 * real focus stays on the input the whole time - so this is a plain
 * background wash, not a :focus-visible rule. */
.rowActive {
  background: var(--accent-bg);
}

.rowName {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* The current model's marker. Paired with aria-selected on the row, never
 * carrying the meaning alone. */
.check {
  color: var(--accent);
  font-size: var(--font-size-caption);
}

/* Per-row metadata (provider for the mixed Recent group, capabilities, cost,
 * context window): passive small text, pushed to the row's trailing edge. */
.meta {
  margin-left: auto;
  color: var(--ink-mid);
  font-size: var(--font-size-caption);
  white-space: nowrap;
}

/* A provider the hub couldn't reach, in place where its section would be:
 * dim, small, and not an option. --ink-low, never a status hue - an
 * unreachable provider is information, not an alarm. */
.unavailable {
  padding: var(--space-1) var(--space-2);
  color: var(--ink-low);
  font-size: var(--font-size-caption);
}
```

- [ ] **Step 6: Run the panel suite to verify it passes**

Run: `npx vitest run src/widgets/modelCatalog`
Expected: PASS — `modelCatalog.test.tsx` (28 tests), `pickerRows.test.ts`, `catalogView.test.ts`, `catalogClient.test.ts`, `scopedCatalog.test.ts` all green.

- [ ] **Step 7: Replace `ModelField`'s Cancel test**

In `cmd/serf-hub/frontend/src/panes/spawn/ModelField.test.tsx`, replace the whole `test("Cancel returns to the closed display without changing the value", ...)` block (lines 64-75) with:

```tsx
test("Escape returns to the closed display without changing the value", async () => {
  const user = userEvent.setup();
  const onChange = vi.fn();
  render(<ModelField value="openai/gpt-5" onChange={onChange} loadModels={vi.fn().mockResolvedValue(MODELS)} />);

  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox", { name: "Model" });
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox", { name: "Model" })).toBeNull());
  expect(screen.getByText("openai/gpt-5")).toBeTruthy();
  expect(onChange).not.toHaveBeenCalled();
});
```

Add `waitFor` to that file's testing-library import:

```tsx
import { cleanup, render, screen, waitFor } from "@testing-library/react";
```

- [ ] **Step 8: Run the consumer suites**

Run: `npx vitest run src/panes/spawn/ModelField.test.tsx src/panes/settings/sections/launchShared/fields.test.tsx src/dev`
Expected: PASS. (Both call sites already pass `value` into `ModelCatalog`, and their "type then click the row" flows still work because those cases open with `value=""`, so nothing is pre-selected to overwrite.)

- [ ] **Step 9: Typecheck and lint**

Run: `npm run typecheck && npx biome ci src`
Expected: clean. If biome flags an unused import in `modelCatalog/index.tsx`, delete it — `Button`, `Combobox`, and the `catalogView` metadata helpers are all unused there now (`Chip` is still used by the trigger).

- [ ] **Step 10: Commit**

```bash
git add src/widgets/modelCatalog/index.tsx src/widgets/modelCatalog/modelCatalog.module.css \
        src/widgets/modelCatalog/modelCatalog.test.tsx src/panes/spawn/ModelField.test.tsx
git commit -m "modelCatalog: one always-expanded grouped list, pre-filled input, no Cancel"
```

---

## Task 4: `ModelSwitch` on the shared `Popover`

`ModelSwitch` still hand-rolls its popover (`src/panes/session/chrome/ModelSwitch.tsx:89-103` plus `.anchor`/`.popover` CSS) and passes the deleted `onCancel`. This task collapses it onto the shared `Popover` with the same `closeOnScroll={false}` opt-out, so the in-session picker and the spawn picker are one interaction.

**Files:**
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx`
- Modify: `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx`
- Test: `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx` (one composition test)

**Interfaces:**
- Consumes: `ModelCatalogPanel({ loading, error, catalog, value, onPick })` from Task 3; `Popover`'s `closeOnScroll` from Task 1; the existing `modelLabel(modelProvider, model)` from `./statusFormat`.
- Produces: no new exported surface. `ModelSwitchProps` is unchanged (`{ sessionRef, model }`).

- [ ] **Step 1: Update the tests to the new behavior**

In `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.test.tsx`, make these five edits.

(a) The loading assertion (line ~115) — the panel now shows a `Skeleton` instead of a "Loading models…" chip:

```tsx
  expect(screen.getByRole("status", { name: "Loading" })).toBeTruthy();
```

(b) `test("renders every catalog entry as a combobox option once loaded", ...)` — the list is expanded on open, so the ArrowDown is gone:

```tsx
test("renders every catalog entry as an option immediately once loaded, with no keystroke", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");

  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
});
```

(c) `test("typing filters the option list by provider/model substring", ...)` — the input now opens pre-filled with the current model, so clear it before typing (and type into the already-focused input):

```tsx
test("typing filters the option list by provider/model substring", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  const combobox = await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("opus");

  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
  expect(screen.getByRole("option", { name: /claude-opus-5/i })).toBeTruthy();
});
```

(d) The two pick tests (`"picking an option calls setModel..."` and `"a failed setModel surfaces an error toast..."`) both `user.type(combobox, "gpt")`; replace that single line in each with:

```tsx
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  await user.clear(combobox);
  await user.keyboard("gpt");
```

(e) Add a test proving the picker survives a scroll, right after the outside-click test:

```tsx
// The picker's own list scrolls, and the transcript behind it can scroll too:
// neither may dismiss it (defect #5 - it used to close on any scroll).
test("a scroll does not close the open picker", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  await user.click(screen.getByRole("button", { name: /change model/i }));
  await screen.findByRole("combobox");
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(3));
  window.dispatchEvent(new Event("scroll"));

  expect(screen.getByRole("combobox")).toBeTruthy();
});
```

(f) Add a focus-restore test alongside the Escape test:

```tsx
// Popover runs with autoFocus={false} so the panel's input can own focus and
// its selection, which makes restoring focus to the trigger on close
// ModelSwitch's own job. Without it, focus falls to <body>.
test("closing returns focus to the trigger", async () => {
  const user = userEvent.setup();
  const fake = connectFakeClient();
  fake.on("model/list", () => modelListResponse());

  render(<ModelSwitch sessionRef="ref_a" model={testModel()} />);
  const trigger = screen.getByRole("button", { name: /change model/i });
  await user.click(trigger);
  await screen.findByRole("combobox");
  await user.keyboard("{Escape}");

  await waitFor(() => expect(screen.queryByRole("combobox")).toBeNull());
  expect(document.activeElement).toBe(trigger);
});
```

And in `cmd/serf-hub/frontend/src/panes/session/chrome/StatusRow.test.tsx`, the composition test (`"wires the model-switch trigger in..."`, line ~118) types into the same pre-filled input; replace its `await user.type(combobox, "gpt");` with:

```tsx
  await waitFor(() => expect(screen.getAllByRole("option")).toHaveLength(1));
  await user.clear(combobox);
  await user.keyboard("gpt");
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `npx vitest run src/panes/session/chrome/ModelSwitch.test.tsx src/panes/session/chrome/StatusRow.test.tsx`
Expected: FAIL — `ModelSwitch` still renders its own popover and passes `onCancel` (a TypeScript error under `npm run typecheck`, and at runtime the options don't appear until a keystroke).

- [ ] **Step 3: Migrate the component**

In `cmd/serf-hub/frontend/src/panes/session/chrome/ModelSwitch.tsx`:

Replace the import block's React line and add `Popover` to the widgets import:

```tsx
import { useRef, useState } from "react";
```

```tsx
import { Chip, type ModelCatalog, type ModelCatalogEntry, ModelCatalogPanel, Popover, useToasts } from "../../../widgets";
```

Replace the `CLASS` table (dropping `anchor`/`popover`, adding `popoverPanel`):

```tsx
const CLASS = {
  trigger: requireClass(styles.trigger, "modelswitch.module.css", "trigger"),
  chevron: requireClass(styles.chevron, "modelswitch.module.css", "chevron"),
  srOnly: requireClass(styles.srOnly, "modelswitch.module.css", "srOnly"),
  popoverPanel: requireClass(styles.popoverPanel, "modelswitch.module.css", "popoverPanel"),
};
```

Replace `const pickerRef = useRef<HTMLDivElement>(null);` with a trigger ref, and delete the whole `useEffect` that registers the local `keydown`/`mousedown` listeners (lines 87-103, comment included) — the shared `Popover` owns Escape and outside-click:

```tsx
  const triggerRef = useRef<HTMLButtonElement>(null);
```

Extend `closePicker` to return focus to the trigger, for the same reason as `ModelCatalog` (the panel's input owns focus, so `Popover` runs with `autoFocus={false}` and nothing else would restore it):

```tsx
  // Popover's FocusScope is opted out of focus management (autoFocus={false})
  // so the panel's input can own focus and its selection - which makes
  // restoring focus to the trigger on close this component's job.
  function closePicker() {
    setOpen(false);
    triggerRef.current?.focus();
  }
```

`handlePick`'s existing optimistic `setOpen(false)` stays as-is (no refocus — a completed choice shouldn't yank focus back to the chip).

Replace the component's `return (...)` with:

```tsx
  return (
    <Popover
      open={open}
      onClose={closePicker}
      // The picker's own list scrolls, and the transcript behind it scrolls
      // too: neither may dismiss a picker mid-interaction.
      closeOnScroll={false}
      // The panel's input owns focus and its own text selection - see
      // closePicker for why FocusScope must not manage focus here.
      autoFocus={false}
      trigger={
        <button
          ref={triggerRef}
          type="button"
          className={CLASS.trigger}
          onClick={() => (open ? closePicker() : void openPicker())}
          disabled={disabled}
        >
          <Chip>{modelLabel(model.modelProvider, model.model)}</Chip>
          <span className={CLASS.chevron} aria-hidden="true">
            ▾
          </span>
          <span className={CLASS.srOnly}>— change model</span>
        </button>
      }
    >
      <div className={CLASS.popoverPanel}>
        <ModelCatalogPanel
          loading={loading}
          error={error}
          catalog={catalog}
          value={modelLabel(model.modelProvider, model.model)}
          onPick={(entry) => void handlePick(entry)}
        />
      </div>
    </Popover>
  );
```

Finally, update this file's header comment so it stops describing a bespoke popover — replace its first paragraph (lines 1-6) with:

```tsx
// ModelSwitch: the mid-session model-switch trigger. The current model chip
// IS the trigger (quiet hover affordance + a small chevron) - clicking it
// opens the SAME rich catalog picker the spawn flow uses (ModelCatalogPanel:
// search over one always-expanded grouped list, capability/cost/context
// metadata, Recent, provider diagnostics in place), in the SAME shared
// floating Popover (widgets/popover, closeOnScroll={false}) - so opening it
// never shifts the status row's layout and a scroll never dismisses it.
```

- [ ] **Step 4: Restyle**

In `cmd/serf-hub/frontend/src/panes/session/chrome/modelswitch.module.css`, delete the `.anchor` rule, the `.popover` rule, the `@keyframes popoverFadeScale` block, and the `@media (prefers-reduced-motion: reduce)` block (widgets/popover owns all of that now). Keep `.trigger`, its `:hover`/`:focus-visible`/`:disabled` rules, `.chevron`, and `.srOnly`. Append:

```css
/* The floating panel's own content padding/sizing - widgets/popover supplies
 * position/surface/border/radius/animation. Wider than the spawn form's copy
 * because this picker's rows carry qualified provider/model ids, and capped
 * against the viewport so the same panel works on a phone. */
.popoverPanel {
  padding: var(--space-3);
  width: 400px;
  max-width: calc(100vw - var(--space-4));
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `npx vitest run src/panes/session/chrome`
Expected: PASS — every file in the directory green, `ModelSwitch.test.tsx` now 14 tests (its original 12 plus the scroll and focus-restore additions).

Note on `value` here: `ModelSwitch` passes `modelLabel(model.modelProvider, model.model)`, which collapses to a bare provider on a cold-hydrated thread (see `statusFormat.ts`'s own comment). That's the right string to pre-fill — it's what the chip shows — and when it isn't a qualified id it simply matches no row, so no ✓ and no initial highlight. Do not "fix" this by synthesizing a qualified id the wire never sent.

- [ ] **Step 6: Typecheck and lint**

Run: `npm run typecheck && npx biome ci src`
Expected: clean. `useEffect` is no longer used in `ModelSwitch.tsx` — Step 3's import line already drops it while keeping `useRef` for the trigger.

- [ ] **Step 7: Commit**

```bash
git add src/panes/session/chrome/ModelSwitch.tsx src/panes/session/chrome/modelswitch.module.css \
        src/panes/session/chrome/ModelSwitch.test.tsx src/panes/session/chrome/StatusRow.test.tsx
git commit -m "ModelSwitch: use the shared scroll-safe Popover, drop the hand-rolled one"
```

---

## Task 5: Full gate + live verification on both call sites

Unit tests can't see layout, focus, or a real `/api/models` envelope with actual diagnostics. This task runs the whole frontend gate and then drives the built app.

**Files:** none modified (verification only; fix-ups land in the task they belong to).

**Interfaces:**
- Consumes: everything from Tasks 1-4.
- Produces: a verified build, plus the spec's `Status:` line flipped to implemented.

- [ ] **Step 1: Run the whole frontend gate**

From the repo root:

Run: `make test-web`
Expected: `tsc --noEmit` silent, `vitest run` reporting all files passed (baseline 3571 tests plus this plan's additions, zero failures), `biome ci src` reporting no diagnostics. Any failure is this plan's to fix — including one that looks unrelated.

- [ ] **Step 2: Build the hub with the new SPA embedded**

Run: `make build-web && make build-hub`
Expected: both succeed; `git status --short` shows no stray change under `cmd/serf-hub/frontend/dist` (the Makefile restores `dist/PLACEHOLDER`).

- [ ] **Step 3: Start the hub and open the SPA**

Run: `./serf-hub` (from the repo root; it prints `[hub] serf-hub … listening on <addr>` and an `[hub] auth URL (visit once per browser): http://<host>/auth?token=…` line to stderr). Open that auth URL in the browser (the superpowers-chrome `use_browser` tool, `action: "navigate"`), which sets the capability cookie and lands on the SPA.

- [ ] **Step 4: Verify the spawn-pane picker**

In the browser: open the spawn form, click the model chip, and check each item:
1. The list is fully populated the instant it opens — no keystroke, no arrow key.
2. The input holds the current qualified value, fully highlighted; one keystroke replaces the whole thing and filters the list.
3. Any unreachable provider shows as a dim small-text line in place, reading `provider — message — hint`. No "N providers unavailable" button exists.
4. There is no Cancel button. Escape closes; a click outside closes; picking closes and updates the chip.
5. Scroll the list with the wheel — the picker stays open. Scroll the page behind it — the picker stays open.

- [ ] **Step 5: Verify the in-session picker**

Start (or open) a session, then repeat Step 4's five checks on the status-row model chip. Additionally confirm: the panel opens without shifting the status row, it flips upward when the row sits near the viewport bottom, and the trigger is disabled while a turn is active.

- [ ] **Step 6: Verify one narrow viewport**

Set the viewport to a phone width (`use_browser` `action: "set_viewport"`, `payload: {width: 390, height: 844, mobile: true}`), reload, and open both pickers. Expected: the same single grouped list, panel width clamped inside the viewport (no horizontal scroll, no clipped rows) — the design's "same rendering at every width" claim, checked rather than assumed.

- [ ] **Step 7: Mark the spec implemented**

In `docs/superpowers/specs/2026-07-24-model-picker-redesign-design.md`, change the status line to:

```markdown
**Status:** Implemented (plan: `docs/superpowers/plans/2026-07-24-model-picker-redesign.md`)
```

- [ ] **Step 8: Commit**

```bash
git add docs/superpowers/specs/2026-07-24-model-picker-redesign-design.md
git commit -m "docs: mark the model picker redesign spec implemented"
```

---

## Notes for the implementer

- **Never `git commit --no-verify`.** If a hook fails, fix what it reports.
- **`user.type` vs `user.keyboard` matters here.** `user.type(input, "x")` clicks the input first, which collapses the pre-filled selection to a caret and appends instead of replacing. Every test that exercises typing over the pre-fill uses `user.keyboard` against the already-focused input (or `user.clear` first when the intent is just "filter by x"). If a `user.keyboard` assertion behaves unexpectedly in this version of user-event, do NOT relax the assertion — read the input's `value`/`selectionStart` in the failure output and report what the harness actually did.
- **`Combobox` stays.** `widgets/combobox` is untouched, still exported from `widgets/index.ts`, still covered by its own `combobox.test.tsx`, and still rendered by `src/dev/gallery-sections/combobox.tsx`. It's the widget later pickers build on (`pathpicker` documents borrowing its stale-response idiom). Deleting it is out of scope — do not "clean it up" on the way past.
- **jsdom has no `scrollIntoView`.** The panel calls it optionally (`?.scrollIntoView?.(…)`), so production scrolls and tests no-op unless they stub it. Don't "fix" that into an unconditional call.
- **Escape needs NO handler in the panel.** `handleKeyDown` deliberately has no `Escape` case: the keydown bubbles from the input to `Popover`'s own panel handler, which closes and calls `preventDefault`/`stopPropagation` so an enclosing Dialog never also sees it. Verified empirically (an outer `onKeyDown` never fires). Adding an Escape case to the panel would duplicate the close, not fix anything.
- **Out of scope** (from the spec): the TUI model picker, trigger styling, and any server-side catalog/recents change.
