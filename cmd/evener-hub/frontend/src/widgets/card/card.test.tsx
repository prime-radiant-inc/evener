import { fireEvent, render, screen } from "@testing-library/react";
import { expect, test, vi } from "vitest";
import { readModuleCss } from "../../styles/cssBlock";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./card.module.css";
import { Card, type CardProps } from "./index";

const styles = {
  card: requireClass(rawStyles.card, "card.module.css", "card"),
  outlined: requireClass(rawStyles.outlined, "card.module.css", "outlined"),
  attention: requireClass(rawStyles.attention, "card.module.css", "attention"),
};

function cardCss(): string {
  return readModuleCss(import.meta.url, "card.module.css");
}

// The last rule for this selector: outlined and attention also share a grouped
// geometry rule that comes before their own colour rules.
function ruleBody(css: string, selector: string): string {
  const rules = [...css.matchAll(new RegExp(`\\.${selector}\\s*\\{([^}]*)\\}`, "g"))];
  return rules.at(-1)?.[1] ?? "";
}

test("renders its children", () => {
  render(
    <Card>
      <p>Hello</p>
    </Card>,
  );
  expect(screen.getByText("Hello")).toBeTruthy();
});

test("wraps children in a single container carrying the card class", () => {
  render(
    <Card>
      <span data-testid="child" />
    </Card>,
  );
  const child = screen.getByTestId("child");
  expect(child.parentElement?.classList.contains(styles.card)).toBe(true);
});

// Beautiful UI card anatomy: the ring lives in the shadow, not a separate
// border - see docs/superpowers/specs/2026-08-13-webui-beautiful-ui-retheme-
// design.md §6.
test("carries its ring via box-shadow rather than a border", () => {
  const rule = /\.card\s*\{([^}]*)\}/.exec(cardCss())?.[1] ?? "";
  expect(rule).toContain("box-shadow: var(--shadow-card)");
  expect(rule).not.toContain("border:");
});

test("the flat card is the default: no outlined or attention surface", () => {
  render(<Card aria-label="plain">body</Card>);
  const card = screen.getByLabelText("plain");
  expect(card.classList.contains(styles.outlined)).toBe(false);
  expect(card.classList.contains(styles.attention)).toBe(false);
});

test('variant="outlined" draws the bordered surface', () => {
  render(
    <Card variant="outlined" aria-label="outlined">
      body
    </Card>,
  );
  expect(screen.getByLabelText("outlined").classList.contains(styles.outlined)).toBe(true);
  const rule = ruleBody(cardCss(), "outlined");
  expect(rule).toContain("border-color: var(--edge)");
  expect(rule).toContain("background: var(--surface-1)");
});

test('tone="attention" draws the "a human is needed" envelope in the attention hue', () => {
  render(
    <Card tone="attention" aria-label="needs you">
      body
    </Card>,
  );
  expect(screen.getByLabelText("needs you").classList.contains(styles.attention)).toBe(true);
  const rule = ruleBody(cardCss(), "attention");
  expect(rule).toMatch(/border-color: [^;]*var\(--attention\)/);
  expect(rule).toMatch(/background: [^;]*var\(--attention\)/);
});

// Dark-theme legibility: the --attention-bg/-edge token mixes (15%/40%) barely
// tint dark's --surface-1, so the envelope mixes past them.
test("the attention envelope mixes the hue past the token default, for dark-theme legibility", () => {
  const rule = ruleBody(cardCss(), "attention");
  expect(rule).toMatch(/background:\s*color-mix\(in oklab,\s*var\(--attention\)\s*24%,\s*var\(--surface-1\)\)/);
  expect(rule).toMatch(/border-color:\s*color-mix\(in oklab,\s*var\(--attention\)\s*55%,\s*var\(--edge\)\)/);
});

test("native attributes reach the card's element, so a caller can make it a labelled group with its own keys", () => {
  const onKeyDown = vi.fn();
  render(
    <Card tone="attention" role="group" aria-label="Approval" data-batch="b1" tabIndex={-1} onKeyDown={onKeyDown}>
      <button type="button">Allow</button>
    </Card>,
  );
  const group = screen.getByRole("group", { name: "Approval" });
  expect(group.dataset.batch).toBe("b1");
  expect(group.tabIndex).toBe(-1);
  fireEvent.keyDown(screen.getByRole("button", { name: "Allow" }), { key: "Enter" });
  expect(onKeyDown).toHaveBeenCalledTimes(1);
});

test("a caller-supplied className arriving via untyped spread does not override the card's own classes", () => {
  const conflicting = { children: "x", className: "caller-injected", "aria-label": "spread" } as unknown as CardProps;
  render(<Card {...conflicting} />);
  const card = screen.getByLabelText("spread");
  expect(card.classList.contains(styles.card)).toBe(true);
  expect(card.className).not.toContain("caller-injected");
});
