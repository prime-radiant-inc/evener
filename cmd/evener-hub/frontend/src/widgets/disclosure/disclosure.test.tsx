import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { afterEach, expect, test, vi } from "vitest";
import { resetDisclosureStoreForTests, setDisclosureOpen } from "./disclosureStore";
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
