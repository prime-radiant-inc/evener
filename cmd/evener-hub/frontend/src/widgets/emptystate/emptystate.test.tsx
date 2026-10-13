import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { render, screen } from "@testing-library/react";
import { expect, test } from "vitest";
import rawStyles from "./emptystate.module.css";
import { EmptyState } from "./index";

const compactClass = rawStyles.compact ?? "compact-class-missing";

test("renders the title", () => {
  render(<EmptyState title="No sessions yet" />);
  expect(screen.getByText("No sessions yet")).toBeTruthy();
});

test("renders the hint when provided", () => {
  render(<EmptyState title="No sessions yet" hint="Start one from the command palette." />);
  expect(screen.getByText("Start one from the command palette.")).toBeTruthy();
});

test("omits the hint when not provided", () => {
  const { container } = render(<EmptyState title="No sessions yet" />);
  // the title is the only text node this widget renders on its own -
  // nothing else should appear when hint/action are both absent
  expect(container.textContent).toBe("No sessions yet");
});

test("renders the action slot when provided", () => {
  render(<EmptyState title="No sessions yet" action={<button type="button">New session</button>} />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("omits the action slot when not provided", () => {
  render(<EmptyState title="No sessions yet" />);
  expect(screen.queryByRole("button")).toBeNull();
});

test("renders title, hint, and action together", () => {
  render(
    <EmptyState
      title="No sessions yet"
      hint="Start one from the command palette."
      action={<button type="button">New session</button>}
    />,
  );
  expect(screen.getByText("No sessions yet")).toBeTruthy();
  expect(screen.getByText("Start one from the command palette.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("size=display adds the display-size title class; the default does not", () => {
  const { container, rerender } = render(<EmptyState title="No session open" size="display" />);
  const title = () => container.querySelector('[data-testid="empty-state"] > p');
  expect(title()?.className).toMatch(/titleDisplay/);
  rerender(<EmptyState title="No session open" />);
  expect(title()?.className).not.toMatch(/titleDisplay/);
});

// size="compact" is the empty message inside a list or section (a settings
// collection with nothing in it, the activity sidebar's "No jobs at this
// level."): the same title/hint/action contract, set as one quiet line.
test("size=compact renders title, hint, and action", () => {
  render(
    <EmptyState
      size="compact"
      title="No plugins installed yet."
      hint="Install one from Browse."
      action={<button type="button">Browse</button>}
    />,
  );
  expect(screen.getByText("No plugins installed yet.")).toBeTruthy();
  expect(screen.getByText("Install one from Browse.")).toBeTruthy();
  expect(screen.getByRole("button", { name: "Browse" })).toBeTruthy();
});

test("size=compact applies the compact layout to the whole block, and only that size does", () => {
  const { container, rerender } = render(<EmptyState title="No jobs at this level." size="compact" />);
  const root = () => container.querySelector('[data-testid="empty-state"]');
  expect(root()?.classList.contains(compactClass)).toBe(true);
  rerender(<EmptyState title="No jobs at this level." />);
  expect(root()?.classList.contains(compactClass)).toBe(false);
  rerender(<EmptyState title="No jobs at this level." size="display" />);
  expect(root()?.classList.contains(compactClass)).toBe(false);
});

// jsdom applies no stylesheet, so the declarations are the checkable truth -
// comments stripped first so a doc comment cannot satisfy the match.
test("size=compact declares a left-aligned block on the UI type step", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "emptystate.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
  const block = /\.compact\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(block).toContain("align-items: flex-start");
  expect(block).toContain("text-align: start");
  const title = /\.compact \.emptyTitle\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(title).toContain("font-size: var(--font-size-ui)");
  expect(title).toContain("font-family: var(--font-sans)");
  const hint = /\.compact \.hint\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(hint).toContain("font-size: var(--font-size-caption)");
});

test("exposes why it is empty as data-reason, and omits it when not given", () => {
  const { rerender } = render(<EmptyState title="No session open" reason="deleted" />);
  expect(screen.getByTestId("empty-state").getAttribute("data-reason")).toBe("deleted");
  rerender(<EmptyState title="No session open" />);
  expect(screen.getByTestId("empty-state").hasAttribute("data-reason")).toBe(false);
});

test("several actions passed as a fragment all land in the action slot", () => {
  render(
    <EmptyState
      title="t"
      action={
        <>
          <button type="button">First</button>
          <button type="button">Second</button>
        </>
      }
    />,
  );
  const first = screen.getByRole("button", { name: "First" });
  const second = screen.getByRole("button", { name: "Second" });
  expect(first.parentElement).toBe(second.parentElement);
});

// Several actions sit in one wrapping row with the system gap, so callers pass
// a fragment instead of spacing buttons with text nodes.
test("the action slot lays its children out in a wrapping row with a token gap", () => {
  const css = readFileSync(join(dirname(fileURLToPath(import.meta.url)), "emptystate.module.css"), "utf8").replace(
    /\/\*[\s\S]*?\*\//g,
    "",
  );
  const action = /(?:^|\n)\.action\s*\{([^}]*)\}/.exec(css)?.[1] ?? "";
  expect(action).toContain("display: flex");
  expect(action).toContain("flex-wrap: wrap");
  expect(action).toMatch(/gap: var\(--space-\d\)/);
});
