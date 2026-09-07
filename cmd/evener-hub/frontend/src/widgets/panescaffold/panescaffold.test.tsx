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
  // A raw DOM node appended straight to document.body, outside any React
  // tree - cleanup() only unmounts React roots, so under isolate:false this
  // would otherwise outlive the file and false-positive a later file's own
  // plain getByRole("button")/queryByRole("button") query against the
  // shared jsdom document.
  previousFocus.remove();
});

test("does not focus after a scaffold mounts inactive or on an ordinary remount", () => {
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

// The footer used to carry a .footer:has([data-ask-response-dock]) special
// case while AskDock lived in it; the dock is the transcript's trailing row
// now (Session.tsx -> TranscriptBody's trailingRow), so the footer is back
// to the one fixed-slot contract every pane shares.
test("the header and footer are fixed slots around the scrolling body", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  expect(css).toContain("flex: none");
  expect(css).toContain("flex: 1 1 0");
  expect(css).not.toContain("data-ask-response-dock");
});

// The composer's bottom safe-area accommodation lives where it docks - this
// footer - not on StackHost's container (StackHost.module.css's .host
// comment): the footer's own chrome fills the home-indicator band while its
// padding keeps the composer controls above it. env() is 0 on desktop, so
// the padding resolves to plain --space-3 there.
test("the footer fills to the screen's bottom edge while keeping its content clear of the home indicator", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  // Comments stripped: the rule's own comment names the inset (testing.md's
  // "a stylesheet assertion that matches its own comment" trap).
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const footerRule = css.match(/\.footer \{([^}]*)\}/)?.[1] ?? "";
  expect(footerRule).toContain(
    "padding-bottom: calc(var(--space-3) + max(0px, env(safe-area-inset-bottom) - var(--keyboard-inset, 0px)))",
  );
});

// Footer-less panes (welcome, settings, spawn, doc) had their end-of-scroll
// content covered by StackHost's blanket host padding; with that gone (the
// composer owns its own dock), the body's scroll padding carries the inset
// so the last content still clears the home indicator.
test("the body keeps end-of-scroll content clear of the home indicator", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");
  const bodyRule = css.match(/\.body \{([^}]*)\}/)?.[1] ?? "";
  expect(bodyRule).toContain(
    "padding-bottom: calc(var(--space-5) + max(0px, env(safe-area-inset-bottom) - var(--keyboard-inset, 0px)))",
  );
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

// Micro-label pattern (design doc §2/§6): the pane title is chrome, not a
// heading-sized title - small uppercase caption on the inset header band.
test("the header sits on the inset surface", () => {
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  const rule = css.match(/\.header \{([^}]*)\}/)?.[1] ?? "";
  expect(rule).toContain("background: var(--surface-inset)");
  expect(rule).toContain("border-bottom: 1px solid var(--edge)");
});

test("the title renders as a sentence-case pane-title heading, not an uppercase micro-label", () => {
  // typography-spacing-critique-2026-09-06 R4: the pane IS the page, so its
  // title is the page heading. The micro-label recipe stays for containers
  // inside a page (InspectorCard, RecommendationCard, Table headers).
  const here = dirname(fileURLToPath(import.meta.url));
  const css = readFileSync(join(here, "panescaffold.module.css"), "utf8");
  const rule = css.match(/\.title \{([^}]*)\}/)?.[1] ?? "";
  expect(rule).toContain("font-size: var(--font-size-pane-title)");
  expect(rule).toContain("font-weight: var(--font-weight-semibold)");
  expect(rule).not.toContain("text-transform");
  expect(rule).toContain("letter-spacing: var(--tracking-display)");
  expect(rule).toContain("color: var(--ink-hi)");
});
