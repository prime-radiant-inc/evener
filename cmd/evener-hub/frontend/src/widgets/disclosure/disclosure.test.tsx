import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { MOBILE_QUERY } from "../../shell/useIsMobile";
import { escapeRegExp } from "../../styles/cssBlock";
import { Chevron } from "../chevron";
import { beginDisclosureBaseline, resetDisclosureStoreForTests, setDisclosureOpen } from "./disclosureStore";
import { Disclosure } from "./index";

// jsdom runs no animations, so A6's motion can only be asserted at the
// declaration level. Comments are stripped FIRST: a stylesheet grep that
// matches its own comment prose asserts nothing (this repo has that precedent).
function motionCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "disclosure.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

// The fold-header row recipe (flex row, hover wash, focus ring, tap floor) is
// shared with DisclosureHeading; the assembly is what keeps it applying here.
function foldHeaderCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "../internal/foldHeader.module.css");
  return readFileSync(path, "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
}

afterEach(() => {
  cleanup();
  resetDisclosureStoreForTests();
});

function ControlledHarness({ disabled = false }: { disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <Disclosure open={open} onOpenChange={setOpen} summary="Customize & advanced" disabled={disabled}>
      <p>Advanced body</p>
    </Disclosure>
  );
}

test("starts collapsed by default; clicking the summary expands", () => {
  render(
    <Disclosure id="d1" summary="Head" data-testid="d">
      Body
    </Disclosure>,
  );
  expect(screen.queryByText("Body")).toBeNull();
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
});

test("open state survives remount because it lives in the store", () => {
  const { unmount } = render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  fireEvent.click(screen.getByText("Head"));
  expect(screen.getByText("Body")).toBeTruthy();
  unmount();
  render(
    <Disclosure id="keep" summary="Head">
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy(); // still open after remount
});

test("defaultOpen renders open when the store has no entry", () => {
  render(
    <Disclosure id="d2" summary="Head" defaultOpen>
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy();
});

test("an expand-all baseline opens a store-backed disclosure, and one click closes it", () => {
  render(
    <Disclosure id={"scope\0row"} summary="Head" data-testid="d">
      Body
    </Disclosure>,
  );
  act(() => beginDisclosureBaseline("scope", ["row"], true));
  expect((screen.getByTestId("d") as HTMLDetailsElement).open).toBe(true);
  fireEvent.click(screen.getByText("Head"));
  expect((screen.getByTestId("d") as HTMLDetailsElement).open).toBe(false);
  expect(screen.queryByText("Body")).toBeNull();
});

test("ignoreBaseline keeps a disclosure on its default through expand-all while a reader's click still wins", () => {
  render(
    <Disclosure id={"scope\0row"} summary="Head" data-testid="d" ignoreBaseline>
      Body
    </Disclosure>,
  );
  act(() => beginDisclosureBaseline("scope", ["row"], true));
  expect((screen.getByTestId("d") as HTMLDetailsElement).open).toBe(false);
  fireEvent.click(screen.getByText("Head"));
  expect((screen.getByTestId("d") as HTMLDetailsElement).open).toBe(true);
  expect(screen.getByText("Body")).toBeTruthy();
});

test("variant defaults to the filled row and quiet is reported on the disclosure", () => {
  render(
    <>
      <Disclosure id="v-default" summary="Default" data-testid="default">
        Body
      </Disclosure>
      <Disclosure id="v-quiet" summary="Quiet" data-testid="quiet" variant="quiet">
        Body
      </Disclosure>
    </>,
  );
  expect(screen.getByTestId("default").dataset.variant).toBe("default");
  expect(screen.getByTestId("quiet").dataset.variant).toBe("quiet");
  fireEvent.click(screen.getByText("Quiet"));
  expect((screen.getByTestId("quiet") as HTMLDetailsElement).open).toBe(true);
});

test("leading receives the widget's open state and summaryClassName lands on the summary", () => {
  render(
    <Disclosure
      id="leading-row"
      summary={<span data-testid="summary-slot" />}
      data-testid="leading-details"
      summaryClassName="caller-summary"
      leading={(open) => (
        <span data-testid="leading-slot" data-open={open ? "true" : "false"}>
          <Chevron open={open} />
        </span>
      )}
    >
      <span data-testid="leading-body" />
    </Disclosure>,
  );
  const details = screen.getByTestId("leading-details");
  const summary = details.querySelector("summary");
  if (summary === null) throw new Error("the disclosure rendered no summary");
  const leading = screen.getByTestId("leading-slot");
  expect(summary.firstElementChild).toBe(leading);
  expect(summary.querySelectorAll("svg")).toHaveLength(1);
  expect(leading.getAttribute("data-open")).toBe("false");
  expect(summary.className.split(" ")).toContain("caller-summary");

  fireEvent.click(summary);
  expect(screen.getByTestId("leading-slot").getAttribute("data-open")).toBe("true");
  expect(screen.getByTestId("leading-body")).toBeTruthy();
});

test("summaryAriaLabel names the summary per open state and leaves the visible summary alone", () => {
  const label = (open: boolean) => (open ? "Collapse section" : "Expand section");
  const view = (open: boolean, name: (open: boolean) => string | undefined) => (
    <Disclosure
      open={open}
      onOpenChange={() => {}}
      summary="Visible summary"
      summaryAriaLabel={name}
      data-testid="named"
    >
      <span>Body</span>
    </Disclosure>
  );

  const { rerender } = render(view(false, label));
  const details = screen.getByTestId("named");
  const summary = details.querySelector("summary");
  if (summary === null) throw new Error("the disclosure rendered no summary");
  expect(summary.getAttribute("aria-label")).toBe(label(false));
  expect(summary.textContent).toBe("Visible summary");

  rerender(view(true, label));
  expect(summary.getAttribute("aria-label")).toBe(label(true));

  // A caller that only needs a name in one state returns undefined there, so
  // the visible content stays the accessible name instead of an empty label.
  rerender(view(true, () => undefined));
  expect(summary.hasAttribute("aria-label")).toBe(false);
});

// Declaration-level (jsdom runs no cascade): the quiet variant drops the hover
// fill and the open-state wash, and keeps the summary flush with the content
// column, while the default variant keeps both washes.
test("the quiet variant declares no hover fill, no open wash, and no inline inset", () => {
  const css = motionCss();
  const rule = (selector: string) =>
    css.match(new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
  expect(rule('.details[data-variant="quiet"] > .summary')).toMatch(/padding-inline:\s*0/);
  expect(rule('.details[data-variant="quiet"] > .summary:hover:not([aria-disabled="true"])')).toMatch(
    /background:\s*none/,
  );
  expect(rule('.details[data-variant="quiet"][open] > .summary')).toMatch(/background:\s*none/);
  expect(rule(".details[open] > .summary")).toMatch(/background:\s*var\(--hover-2\)/);
});

test("the rule variant is reported on the disclosure, drops the chevron, and toggles like the others", async () => {
  const user = userEvent.setup();
  render(
    <>
      <Disclosure id="v-rule" summary="Rule" data-testid="rule" variant="rule">
        Rule body
      </Disclosure>
      <Disclosure id="v-default-chevron" summary="Default" data-testid="default">
        Body
      </Disclosure>
    </>,
  );
  const details = screen.getByTestId("rule") as HTMLDetailsElement;
  const summary = details.querySelector("summary")!;
  expect(details.dataset.variant).toBe("rule");
  expect(summary.querySelector("svg")).toBeNull();
  expect(screen.getByTestId("default").querySelector("summary svg")).not.toBeNull();

  await user.click(summary);
  expect(details.open).toBe(true);
  expect(within(details).getByText("Rule body")).toBeTruthy();

  summary.focus();
  expect(document.activeElement).toBe(summary);
  await user.keyboard("{Enter}");
  fireEvent.click(summary);
  expect(details.open).toBe(false);
});

test("the rule variant is controllable like the others", () => {
  const onOpenChange = vi.fn();
  render(
    <Disclosure open={false} onOpenChange={onOpenChange} summary="Head" variant="rule" data-testid="controlled-rule">
      Body
    </Disclosure>,
  );
  expect(screen.getByTestId("controlled-rule").dataset.variant).toBe("rule");
  fireEvent.click(screen.getByText("Head"));
  expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(true);
});

// Declaration-level (jsdom runs no cascade): the rule variant centres its
// caption between two flexible hairlines drawn by the summary's own
// pseudo-elements and keeps the summary background unchanged on hover and open.
test("the rule variant declares two flex hairlines on --edge and no hover or open background", () => {
  const css = motionCss();
  const rule = (selector: string) =>
    css.match(new RegExp(`${selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}\\s*\\{([^}]*)\\}`))?.[1] ?? "";
  const hairlines = rule(
    '.details[data-variant="rule"] > .summary::before,\n.details[data-variant="rule"] > .summary::after',
  );
  expect(hairlines).toMatch(/content:\s*""/);
  expect(hairlines).toMatch(/flex:\s*1/);
  expect(hairlines).toMatch(/border-top:\s*1px solid var\(--edge\)/);
  expect(rule('.details[data-variant="rule"] > .summary')).toMatch(/justify-content:\s*center/);
  expect(rule('.details[data-variant="rule"] > .summary:hover:not([aria-disabled="true"])')).toMatch(
    /background:\s*none/,
  );
  expect(rule('.details[data-variant="rule"][open] > .summary')).toMatch(/background:\s*none/);
});

// Declaration-level: one shared :focus-visible rule gives every variant's
// summary the token ring, inset so a flush transcript row inside a clipping
// scroll port still shows all four sides; no variant overrides it.
test("every variant's summary draws the token focus ring, inset", () => {
  const css = motionCss();
  expect(css).toMatch(/composes:\s*foldRow from "\.\.\/internal\/foldHeader\.module\.css"/);
  const shared = foldHeaderCss();
  const rule = shared.match(/(?:^|\})\s*\.foldRow:focus-visible\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(rule).toMatch(/outline:\s*var\(--focus-ring\)/);
  expect(rule).toMatch(/outline-offset:\s*-2px/);
  const focusRules = [...shared.matchAll(/([^{}]*:focus-visible[^{}]*)\{([^}]*)\}/g)];
  expect(focusRules).toHaveLength(1);
  expect(shared).not.toMatch(/outline:\s*none/);
});

test("controlled harness owns the open state", () => {
  render(<ControlledHarness />);
  expect(screen.queryByText("Advanced body")).toBeNull();
  fireEvent.click(screen.getByText("Customize & advanced"));
  expect(screen.getByText("Advanced body")).toBeTruthy();
});

test("controlled mode starts from supplied open state and rerenders when it changes", () => {
  const onOpenChange = vi.fn();
  const { rerender } = render(
    <Disclosure open={false} onOpenChange={onOpenChange} summary="Head" data-testid="controlled">
      Body
    </Disclosure>,
  );
  expect(screen.queryByText("Body")).toBeNull();
  fireEvent.click(screen.getByText("Head"));
  expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(true);

  rerender(
    <Disclosure open={true} onOpenChange={onOpenChange} summary="Head" data-testid="controlled">
      Body
    </Disclosure>,
  );
  expect(screen.getByText("Body")).toBeTruthy();
});

test("controlled native Enter and Space activation each request exactly one state change", async () => {
  const user = userEvent.setup();
  const onOpenChange = vi.fn();
  const { rerender } = render(
    <Disclosure open={false} onOpenChange={onOpenChange} summary="Head">
      Body
    </Disclosure>,
  );
  const summary = screen.getByText("Head");
  summary.focus();
  // jsdom/user-event dispatches the key events but does not implement the
  // browser's native <summary> default action, which is a follow-up click.
  await user.keyboard("{Enter}");
  fireEvent.click(summary);
  expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(true);

  rerender(
    <Disclosure open={true} onOpenChange={onOpenChange} summary="Head">
      Body
    </Disclosure>,
  );
  await user.keyboard(" ");
  fireEvent.click(summary);
  expect(onOpenChange).toHaveBeenCalledTimes(2);
  expect(onOpenChange).toHaveBeenLastCalledWith(false);
});

test("controlled native toggle events do not call onOpenChange", () => {
  const onOpenChange = vi.fn();
  render(
    <Disclosure open={true} onOpenChange={onOpenChange} summary="Head" data-testid="controlled">
      Body
    </Disclosure>,
  );
  const details = screen.getByTestId("controlled");
  details.dispatchEvent(new Event("toggle", { bubbles: true }));
  expect(onOpenChange).not.toHaveBeenCalled();
});

test("disabled controlled disclosure preserves open state and is inert until reenabled", () => {
  const onOpenChange = vi.fn();
  const { rerender } = render(
    <Disclosure open={true} onOpenChange={onOpenChange} summary="Head" disabled data-testid="controlled-disabled">
      Body
    </Disclosure>,
  );
  const details = screen.getByTestId("controlled-disabled");
  const summary = screen.getByText("Head");
  expect(screen.getByText("Body")).toBeTruthy();
  expect(summary.getAttribute("aria-disabled")).toBe("true");
  expect(summary.getAttribute("tabindex")).toBe("-1");
  fireEvent.pointerDown(summary);
  fireEvent.keyDown(summary, { key: "Enter" });
  fireEvent.click(summary);
  expect(onOpenChange).not.toHaveBeenCalled();
  expect(details.hasAttribute("open")).toBe(true);

  rerender(
    <Disclosure open={true} onOpenChange={onOpenChange} summary="Head" data-testid="controlled-disabled">
      Body
    </Disclosure>,
  );
  expect(summary.getAttribute("tabindex")).toBe(null);
  fireEvent.click(screen.getByText("Head"));
  expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
  rerender(
    <Disclosure open={false} onOpenChange={onOpenChange} summary="Head" data-testid="controlled-disabled">
      Body
    </Disclosure>,
  );
  expect(screen.queryByText("Body")).toBeNull();
});

test("disabled store-backed disclosure does not mutate a collapsed or open state", () => {
  setDisclosureOpen("disabled-store-collapsed", false);
  const collapsed = render(
    <Disclosure id="disabled-store-collapsed" summary="Collapsed" disabled>
      Collapsed body
    </Disclosure>,
  );
  fireEvent.click(screen.getByText("Collapsed"));
  expect(screen.queryByText("Collapsed body")).toBeNull();
  collapsed.rerender(
    <Disclosure id="disabled-store-collapsed" summary="Collapsed">
      Collapsed body
    </Disclosure>,
  );
  expect(screen.queryByText("Collapsed body")).toBeNull();
  fireEvent.click(screen.getByText("Collapsed"));
  expect(screen.getByText("Collapsed body")).toBeTruthy();
  fireEvent.click(screen.getByText("Collapsed"));
  expect(screen.queryByText("Collapsed body")).toBeNull();
  collapsed.unmount();

  setDisclosureOpen("disabled-store-open", true);
  const open = render(
    <Disclosure id="disabled-store-open" summary="Open" disabled>
      Open body
    </Disclosure>,
  );
  const summary = screen.getByText("Open");
  expect(summary.getAttribute("aria-disabled")).toBe("true");
  expect(summary.getAttribute("tabindex")).toBe("-1");
  fireEvent.keyDown(summary, { key: "Enter" });
  fireEvent.click(summary);
  expect(screen.getByText("Open body")).toBeTruthy();
  open.rerender(
    <Disclosure id="disabled-store-open" summary="Open">
      Open body
    </Disclosure>,
  );
  expect(screen.getByText("Open body")).toBeTruthy();
  fireEvent.click(screen.getByText("Open"));
  expect(screen.queryByText("Open body")).toBeNull();
  fireEvent.click(screen.getByText("Open"));
  expect(screen.getByText("Open body")).toBeTruthy();
});

test("disabled styling attenuates only the summary and suppresses its hover wash", () => {
  const css = motionCss();
  expect(foldHeaderCss()).toMatch(/\.foldRow:hover\s*\{[^}]*background:\s*var\(--hover-1\)/);
  expect(css).toMatch(/\.summary\[aria-disabled="true"\]:hover\s*\{[^}]*background:\s*none/);
  expect(css).toMatch(/\.summary\[aria-disabled="true"\]\s*\{[^}]*cursor:\s*not-allowed[^}]*opacity:\s*0\.5/);
  expect(css.match(/opacity:\s*0\.5\s*;/g) ?? []).toHaveLength(1);
  expect(css).not.toMatch(/\.summary:hover\s*\{/);
  expect(css).not.toMatch(/\.details[^}]*opacity\s*:/);
  expect(css).not.toMatch(/\.body[^}]*opacity\s*:/);
});

test("keeps the summary at the narrow-viewport tap floor", () => {
  expect(foldHeaderCss()).toMatch(
    new RegExp(
      `@media ${escapeRegExp(MOBILE_QUERY)}, \\(any-pointer: coarse\\)\\s*\\{[\\s\\S]*?\\.foldRow\\s*\\{[^}]*min-height:\\s*var\\(--tap-min\\)`,
    ),
  );
});

// --- A6: opening animates, subtly, and honors prefers-reduced-motion -----

test("the body fade is declared with real motion; the chevron's rotation lives with Chevron", () => {
  const css = motionCss();
  expect(css).not.toMatch(/\.chevron[^}]*transition:\s*transform/);
  expect(css).not.toMatch(/rotate\(90deg\)/);
  // The body fades in by the shared reveal recipe, which owns the keyframes,
  // its tokens and its reduced-motion gate (styles/motion.contract.test.ts).
  expect(css).toMatch(/\.body\s*\{[^}]*composes:\s*bodyReveal from "\.\.\/\.\.\/styles\/motion\.module\.css"/);
  expect(css).not.toMatch(/@keyframes|\banimation:/);
});

test("every motion declaration uses an existing motion token - no invented duration", () => {
  for (const css of [motionCss(), foldHeaderCss()]) {
    expect(css.match(/(?:transition|animation):[^;]*?(\d+m?s)/g)).toBe(null); // no literal duration anywhere
  }
  // The row's hover wash is the only transition, and it uses the shared hover
  // duration.
  expect(foldHeaderCss()).toMatch(/\.foldRow\s*\{[^}]*transition:\s*background-color var\(--motion-duration-hover\)/);
});

test("no transform motion is left ungated in the disclosure's own stylesheet", () => {
  const css = motionCss();
  // The chevron rotation moved to widgets/chevron, which gates its transition,
  // and the hover wash's transition to the shared fold-header recipe.
  // Disclosure declares no motion of its own; the recipe's only transition is
  // the ungated hover feedback (chevron.test.tsx covers the gated transform).
  expect(css).not.toMatch(/@media\s*\(prefers-reduced-motion:\s*no-preference\)/);
  expect(css).not.toMatch(/transition:\s*transform/);
  expect(css).not.toMatch(/transition:/);
  const transitions = foldHeaderCss().match(/transition:\s*[^;]+;/g) ?? [];
  expect(transitions.length).toBeGreaterThan(0);
  for (const declaration of transitions) {
    expect(declaration).toMatch(/^transition:\s*background-color\b/);
  }
});

test("the body's reveal is a fade, not a slide or a bounce", () => {
  const recipes = readFileSync(
    join(dirname(fileURLToPath(import.meta.url)), "../../styles/motion.module.css"),
    "utf8",
  ).replace(/\/\*[\s\S]*?\*\//g, "");
  const reveal = /\.bodyReveal\s*\{\s*animation:\s*(\w+)/.exec(recipes);
  expect(reveal).not.toBeNull();
  const keyframes = new RegExp(`@keyframes\\s+${reveal![1]}\\s*\\{([\\s\\S]*?)\\n\\}`).exec(recipes);
  expect(keyframes).not.toBeNull();
  expect(keyframes![1]).toContain("opacity");
  expect(keyframes![1]).not.toContain("translate");
  expect(keyframes![1]).not.toContain("scale");
});
