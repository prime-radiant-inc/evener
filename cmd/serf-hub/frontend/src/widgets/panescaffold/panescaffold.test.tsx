import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, expect, test } from "vitest";
import { requestPaneFocus, resetWorkspaceStoreForTests } from "../../shell/workspace";
import { PaneScaffold } from "./index";

afterEach(cleanup);

beforeEach(resetWorkspaceStoreForTests);

test("renders the title as a heading", () => {
  render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(screen.getByRole("heading", { name: "Sessions" })).toBeTruthy();
});

test("renders children inside the scrollable body", () => {
  render(<PaneScaffold title="Sessions">the body content</PaneScaffold>);
  expect(screen.getByText("the body content")).toBeTruthy();
});

test("makes the content region focusable and consumes a toggle-open focus marker once", () => {
  requestPaneFocus("pane_sessionDetails_1");
  const previousFocus = document.createElement("button");
  document.body.append(previousFocus);
  previousFocus.focus();
  const { rerender, container } = render(
    <PaneScaffold title="Details" paneId="pane_sessionDetails_1" focused scaffoldMarker="session-panel:details:ref_a">
      content
    </PaneScaffold>,
  );

  const body = container.querySelector<HTMLElement>("[data-pane-scaffold]");
  expect(body).not.toBeNull();
  expect(body?.tabIndex).toBe(-1);
  expect(document.activeElement).toBe(body);

  previousFocus.focus();
  rerender(
    <PaneScaffold title="Details" paneId="pane_sessionDetails_1" focused scaffoldMarker="session-panel:details:ref_a">
      content
    </PaneScaffold>,
  );
  expect(document.activeElement).toBe(previousFocus);
});

test("does not focus on an ordinary remount or after a pre-mount activation is cancelled", () => {
  requestPaneFocus("pane_sessionDetails_1");
  const { rerender, unmount, container } = render(
    <PaneScaffold title="Details" paneId="pane_sessionDetails_1" focused={false}>
      content
    </PaneScaffold>,
  );
  const body = container.querySelector<HTMLElement>(".body");
  expect(document.activeElement).not.toBe(body);

  rerender(
    <PaneScaffold title="Details" paneId="pane_sessionDetails_1" focused>
      content
    </PaneScaffold>,
  );
  expect(document.activeElement).not.toBe(container.querySelector<HTMLElement>(".body"));
  unmount();

  const remounted = render(
    <PaneScaffold title="Details" paneId="pane_sessionDetails_1" focused>
      content
    </PaneScaffold>,
  );
  expect(document.activeElement).not.toBe(remounted.container.querySelector<HTMLElement>(".body"));
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

test("the question footer fits short content and caps tall content", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  expect(css).toContain("flex: none");
  expect(css).toContain("flex: 1 1 0");
  expect(css).toContain(".footer:has([data-ask-response-dock])");
  expect(css).toContain("flex: 0 1 auto");
  expect(css).toContain("min-height: 0");
  expect(css).toContain("max-height: 70%");
});

// The chrome-store title channel (2026-07-30-mobile-session-layout-design.md,
// decision 2): PaneScaffold always publishes its title, host-agnostically -
// StackHost renders it in the mobile top bar, DockHost never reads it.
test("publishes its title to the chrome store on mount", async () => {
  const { resetChromeStoreForTests, chromeStore } = await import("../../shell/chromeStore");
  resetChromeStoreForTests();
  render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  expect(chromeStore.getState().paneTitle).toBe("Sessions");
});

test("publishes mobileTitle in preference to title when both are given", async () => {
  const { resetChromeStoreForTests, chromeStore } = await import("../../shell/chromeStore");
  resetChromeStoreForTests();
  render(
    <PaneScaffold title="A very long desktop title" mobileTitle="Short">
      content
    </PaneScaffold>,
  );
  expect(chromeStore.getState().paneTitle).toBe("Short");
});

test("republishes when the title prop changes", async () => {
  const { resetChromeStoreForTests, chromeStore } = await import("../../shell/chromeStore");
  resetChromeStoreForTests();
  const { rerender } = render(<PaneScaffold title="Before">content</PaneScaffold>);
  rerender(<PaneScaffold title="After">content</PaneScaffold>);
  expect(chromeStore.getState().paneTitle).toBe("After");
});

test("clears the chrome store title on unmount", async () => {
  const { resetChromeStoreForTests, chromeStore } = await import("../../shell/chromeStore");
  resetChromeStoreForTests();
  const { unmount } = render(<PaneScaffold title="Sessions">content</PaneScaffold>);
  unmount();
  expect(chromeStore.getState().paneTitle).toBeNull();
});

// Mobile full-bleed + hidden header (2026-07-30-mobile-session-layout-design.md,
// decisions 1 and 3): on the phone the pane loses its card chrome (the
// top bar and surface steps carry the structure) and its header (the title
// moved into StackHost's top bar via the chrome store).
test("mobile: the pane sheds its border and radius to sit flush against the screen edges", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  expect(mobile).not.toBeNull();
  const paneRule = mobile![1]!.match(/\.pane \{([^}]*)\}/);
  expect(paneRule).not.toBeNull();
  expect(paneRule![1]).toContain("border: none");
  expect(paneRule![1]).toContain("border-radius: 0");
});

test("mobile: the in-pane header is hidden - the title lives in the top bar", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  const mobile = css.match(/@media \(max-width: 899px\) \{([\s\S]*?)\n\}/);
  const headerRule = mobile![1]!.match(/\.header \{([^}]*)\}/);
  expect(headerRule).not.toBeNull();
  expect(headerRule![1]).toContain("display: none");
});

test("mobile: the body can never scroll sideways - wide content is contained, not panned", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  const bodyRule = css.match(/\.body \{([^}]*)\}/);
  expect(bodyRule![1]).toContain("overflow-x: clip");
});
