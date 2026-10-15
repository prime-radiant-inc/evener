import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { act, useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { beginDisclosureBaseline, resetDisclosureStoreForTests, setDisclosureOpen } from "./disclosureStore";
import { Disclosure } from "./index";

// jsdom runs no animations, so A6's motion can only be asserted at the
// declaration level. Comments are stripped FIRST: a stylesheet grep that
// matches its own comment prose asserts nothing (this repo has that precedent).
function motionCss(): string {
  const path = join(dirname(fileURLToPath(import.meta.url)), "disclosure.module.css");
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
  const shared = css.match(/(?:^|\})\s*\.summary:focus-visible\s*\{([^}]*)\}/)?.[1] ?? "";
  expect(shared).toMatch(/outline:\s*var\(--focus-ring\)/);
  expect(shared).toMatch(/outline-offset:\s*-2px/);
  const focusRules = [...css.matchAll(/([^{}]*:focus-visible[^{}]*)\{([^}]*)\}/g)];
  expect(focusRules).toHaveLength(1);
  expect(css).not.toMatch(/outline:\s*none/);
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
  expect(css).toMatch(/\.summary:hover:not\(\[aria-disabled="true"\]\)/);
  expect(css).toMatch(/\.summary\[aria-disabled="true"\]\s*\{[^}]*cursor:\s*not-allowed[^}]*opacity:\s*0\.5/);
  expect(css.match(/opacity:\s*0\.5\s*;/g) ?? []).toHaveLength(1);
  expect(css).not.toMatch(/\.summary:hover\s*\{/);
  expect(css).not.toMatch(/\.details[^}]*opacity\s*:/);
  expect(css).not.toMatch(/\.body[^}]*opacity\s*:/);
});

test("keeps the summary at the narrow-viewport tap floor", () => {
  expect(motionCss()).toMatch(
    /@media \(max-width: 899px\), \(any-pointer: coarse\)\s*\{[\s\S]*?\.summary\s*\{[^}]*min-height:\s*var\(--tap-min\)/,
  );
});

// --- A6: opening animates, subtly, and honors prefers-reduced-motion -----

test("the chevron rotation and the body fade are declared with real motion", () => {
  const css = motionCss();
  expect(css).toMatch(/\.chevron\s*\{[^}]*transition:\s*transform/);
  expect(css).toMatch(/\.body\s*\{[^}]*animation:\s*disclosure-body-in/);
});

test("every motion declaration uses an existing motion token - no invented duration", () => {
  const css = motionCss();
  const durations = css.match(/(?:transition|animation):[^;]*?(\d+m?s)/g);
  expect(durations).toBe(null); // no literal duration anywhere
  expect(css).toContain("var(--motion-duration-overlay)");
  expect(css).toContain("var(--motion-easing-standard)");
});

test("the open/close motion (chevron rotation, body fade) sits inside a prefers-reduced-motion: no-preference gate", () => {
  const css = motionCss();
  // The chevron/body open motion must live in the gated block, so a reader
  // who asked for less motion gets an instant open. Interactive-response
  // hover feedback (the app-wide --motion-duration-hover budget every widget
  // chrome pass applies) is deliberately NOT gated here - it's not the
  // idle-adjacent spatial motion prefers-reduced-motion targets. That's
  // asserted generally below rather than pinned to a fixed set of
  // properties: every `transition:` outside the gate must be a
  // background-color response, whatever else the rule declares alongside it.
  const gated = /@media\s*\(prefers-reduced-motion:\s*no-preference\)\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(gated).not.toBeNull();
  expect(gated![1]).toMatch(/\.chevron\s*\{[^}]*transition:\s*transform/);
  expect(gated![1]).toMatch(/\.body\s*\{[^}]*animation:\s*disclosure-body-in/);
  const outsideGate = css.replace(gated![0], "");
  expect(outsideGate).not.toMatch(/\.chevron\s*\{[^}]*transition:\s*transform/);
  expect(outsideGate).not.toMatch(/\banimation:\s*disclosure-body-in/);

  const transitions = outsideGate.match(/transition:\s*[^;]+;/g) ?? [];
  expect(transitions.length).toBeGreaterThan(0);
  for (const declaration of transitions) {
    expect(declaration).toMatch(/^transition:\s*background-color\b/);
  }
});

test("the motion is a fade, not a slide or a bounce", () => {
  const css = motionCss();
  const keyframes = /@keyframes\s+disclosure-body-in\s*\{([\s\S]*?)\n\}/.exec(css);
  expect(keyframes).not.toBeNull();
  expect(keyframes![1]).toContain("opacity");
  expect(keyframes![1]).not.toContain("translate");
  expect(keyframes![1]).not.toContain("scale");
});
