import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createRef } from "react";
import { expect, test, vi } from "vitest";
import { blockBody, readModuleCss, topRuleBlock } from "../../styles/cssBlock";
import { CountButton } from "./index";

// jsdom applies no stylesheet, so the declarations are the checkable truth;
// comments are stripped first so a doc comment quoting one cannot satisfy a
// match (docs/developing-evener/testing.md).
const CSS = readModuleCss(import.meta.url, "countbutton.module.css");

function containerBlock(condition: string): string {
  const marker = `@container statusbar (${condition})`;
  const start = CSS.indexOf(marker);
  expect(start, `countbutton.module.css must declare ${marker}`).toBeGreaterThanOrEqual(0);
  return blockBody(CSS, CSS.indexOf("{", start));
}

const baseProps = {
  glyph: <span data-testid="glyph" />,
  label: "Agents",
  count: "3/4",
  accessibleLabel: "Agents, 3 of 4 active",
};

test("renders a decorative glyph, the label, and the count under the accessible label", () => {
  render(<CountButton {...baseProps} />);
  const button = screen.getByRole("button", { name: "Agents, 3 of 4 active" });
  expect(button.textContent).toContain("Agents");
  expect(button.textContent).toContain("3/4");
  const glyph = screen.getByTestId("glyph");
  expect(glyph.closest('[aria-hidden="true"]')).not.toBeNull();
});

test("pressed reports the open state as aria-expanded and holds the wash", () => {
  const { rerender } = render(<CountButton {...baseProps} pressed={false} />);
  const button = screen.getByRole("button");
  expect(button.getAttribute("aria-expanded")).toBe("false");
  expect(button.hasAttribute("data-pressed")).toBe(false);

  rerender(<CountButton {...baseProps} pressed />);
  expect(button.getAttribute("aria-expanded")).toBe("true");
  expect(button.getAttribute("data-pressed")).toBe("true");
  expect(topRuleBlock(CSS, '.root[data-chrome="wash"][data-pressed="true"]')).toContain("background: var(--hover-1)");
});

test("an unpressed count has no aria-expanded attribute at all", () => {
  render(<CountButton {...baseProps} />);
  expect(screen.getByRole("button").hasAttribute("aria-expanded")).toBe(false);
});

test("compact keeps the label in the DOM but hides it from the layout", () => {
  render(<CountButton {...baseProps} compact />);
  const button = screen.getByRole("button", { name: "Agents, 3 of 4 active" });
  expect(button.getAttribute("data-compact")).toBe("true");
  expect(screen.getByText("Agents")).toBeTruthy();
  expect(topRuleBlock(CSS, '.root[data-compact="true"] .label')).toContain("display: none");
});

test("column orientation stacks the glyph over the count", () => {
  render(<CountButton {...baseProps} orientation="column" compact />);
  const button = screen.getByRole("button");
  expect(button.getAttribute("data-orientation")).toBe("column");
  const rule = topRuleBlock(CSS, '.root[data-orientation="column"]');
  expect(rule).toContain("flex-direction: column");
  expect(rule).toContain("gap: 0");
});

test("pill chrome grounds the counter on the canvas, wash chrome does not", () => {
  const { rerender } = render(<CountButton {...baseProps} chrome="pill" />);
  expect(screen.getByRole("button").getAttribute("data-chrome")).toBe("pill");
  expect(topRuleBlock(CSS, '.root[data-chrome="pill"]')).toContain("background: var(--surface-canvas)");

  rerender(<CountButton {...baseProps} />);
  expect(screen.getByRole("button").getAttribute("data-chrome")).toBe("wash");
  const wash = topRuleBlock(CSS, '.root[data-chrome="wash"]');
  expect(wash).toContain("background: transparent");
  expect(wash).toContain("border-radius: var(--radius-pane)");
});

test("inside a narrow width container the label drops first, then the glyph", () => {
  expect(containerBlock("max-width: 600px")).toMatch(/\.label\s*\{[^}]*display:\s*none/);
  const narrow = containerBlock("max-width: 320px");
  expect(narrow).toMatch(/\.glyph\s*\{[^}]*display:\s*none/);
  expect(narrow).toMatch(/\.root\s*\{[^}]*gap:\s*0/);
});

test("forwards its ref and spreads data-* and aria-* onto the button", () => {
  const ref = createRef<HTMLButtonElement>();
  render(
    <CountButton
      {...baseProps}
      ref={ref}
      data-activity-tab="agents"
      data-pane-id="pane-1"
      data-session-ref="remote:one"
      aria-haspopup="dialog"
      title="Agents"
    />,
  );
  const button = screen.getByRole("button");
  expect(ref.current).toBe(button);
  expect(button.dataset.activityTab).toBe("agents");
  expect(button.dataset.paneId).toBe("pane-1");
  expect(button.dataset.sessionRef).toBe("remote:one");
  expect(button.getAttribute("aria-haspopup")).toBe("dialog");
  expect(button.getAttribute("title")).toBe("Agents");
});

test("defaults to type=button and runs its onClick", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(<CountButton {...baseProps} onClick={onClick} />);
  const button = screen.getByRole("button");
  expect(button.getAttribute("type")).toBe("button");
  await user.click(button);
  expect(onClick).toHaveBeenCalledOnce();
});

test("an aria-disabled refusal swallows the click but keeps focus", async () => {
  const user = userEvent.setup();
  const onClick = vi.fn();
  render(<CountButton {...baseProps} aria-disabled={true} onClick={onClick} />);
  const button = screen.getByRole("button");
  await user.click(button);
  expect(onClick).not.toHaveBeenCalled();
  expect(document.activeElement).toBe(button);
});
