import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test } from "vitest";
import { PaneScaffold } from "./index";

afterEach(cleanup);

test("renders the title as a heading", () => {
  render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
});

test("renders children inside the scrollable body", () => {
  render(<PaneScaffold title="Sessions">the body content</PaneScaffold>);
  expect(screen.getByText("the body content")).toBeTruthy();
});

test("renders no cadence slot when the cadence prop is omitted", () => {
  const { container } = render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(container.querySelector('[data-testid="pane-cadence-slot"]')).toBeNull();
});

test("renders the cadence slot when provided", () => {
  render(
    <PaneScaffold title="Sessions" cadence={<span data-testid="my-cadence" />}>
      content
    </PaneScaffold>,
  );
  expect(screen.getByTestId("my-cadence")).toBeTruthy();
});

test("renders no actions cluster when the actions prop is omitted", () => {
  const { container } = render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(container.querySelector('[data-testid="pane-actions"]')).toBeNull();
});

test("renders the actions cluster when provided", () => {
  render(
    <PaneScaffold
      title="Sessions"
      actions={
        <button type="button" data-testid="my-action">
          Go
        </button>
      }
    >
      content
    </PaneScaffold>,
  );
  expect(screen.getByTestId("my-action")).toBeTruthy();
});

test("renders no footer when the footer prop is omitted", () => {
  const { container } = render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(container.querySelector('[data-testid="pane-footer"]')).toBeNull();
});

test("renders the footer when provided", () => {
  render(
    <PaneScaffold title="Sessions" footer={<span data-testid="my-footer" />}>
      content
    </PaneScaffold>,
  );
  expect(screen.getByTestId("my-footer")).toBeTruthy();
});

test("renders title, cadence, actions and children in that document order", () => {
  const { container } = render(
    <PaneScaffold
      title="Sessions"
      cadence={<span data-testid="my-cadence" />}
      actions={
        <button type="button" data-testid="my-action">
          Go
        </button>
      }
    >
      content
    </PaneScaffold>,
  );
  const positions = ["Sessions", "my-cadence", "my-action"].map((needle) => container.innerHTML.indexOf(needle));
  expect(positions[0]).toBeLessThan(positions[1]!);
  expect(positions[1]).toBeLessThan(positions[2]!);
});

// jsdom performs no real layout, so title truncation (text-overflow: ellipsis)
// and body scrolling (overflow-y: auto) can't be observed by measuring boxes
// or scroll positions in a test - see also virtuallist.test.tsx, which hits
// the same jsdom limitation for a different reason. Instead this reads the
// CSS module's own source, the same way button.test.tsx verifies its
// :focus-visible rule this way.
test("the title rule truncates overflow with an ellipsis", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  expect(css).toContain("text-overflow: ellipsis");
});

test("the body rule scrolls independently of the header and footer", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  expect(css).toContain("overflow-y: auto");
});
