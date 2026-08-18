import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { requireClass } from "../internal/requireClass";
import rawStyles from "./card.module.css";
import { Card } from "./index";

afterEach(cleanup);

const styles = {
  card: requireClass(rawStyles.card, "card.module.css", "card"),
};

// A stylesheet-grep assertion must not be satisfiable by a comment - this repo
// has a precedent of exactly that passing while asserting nothing.
function stripCssComments(css: string): string {
  return css.replace(/\/\*[\s\S]*?\*\//g, "");
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
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "card.module.css"), "utf8");
  const rule = /\.card\s*\{([^}]*)\}/.exec(stripCssComments(css))?.[1] ?? "";
  expect(rule).toContain("box-shadow: var(--shadow-card)");
  expect(rule).not.toContain("border:");
});
