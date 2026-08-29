import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test } from "vitest";
import type { NavigationSessionSummary } from "../../protocol/types.gen";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import { keyID } from "../../stores/navigation/types";
import buttonStyles from "../../widgets/button/button.module.css";
import Welcome from "./Welcome";
import { WelcomeContent } from "./WelcomeContent";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  resetNavigationStoreForTests();
});

function node(overrides: Partial<NavigationSessionSummary> = {}): NavigationSessionSummary {
  return {
    ref: "local:row1",
    host_id: "local",
    session_id: "row1",
    title: "Session",
    project: "Proj",
    state: "idle",
    kind: "session",
    live: true,
    children: [],
    ...overrides,
  };
}
function setRows(needsYou: NavigationSessionSummary[] = [], live: NavigationSessionSummary[] = []): void {
  const resources = new Map();
  for (const [section, rows] of [
    ["needs_you", needsYou],
    ["live", live],
  ] as const) {
    const key = { kind: "section", section, offset: 0, limit: 50 } as const;
    resources.set(keyID(key), {
      key,
      data: { generation_id: "generation_test", revision: 1, sessions: rows, remaining: 0, truncated: false },
      loadedRevision: 1,
      targetRevision: null,
      forceToken: 0,
      etag: "etag",
      loading: false,
      stale: false,
      error: null,
      generationID: "generation_test",
    });
  }
  navigationStore.setState({ mode: "v1", clientGenerationID: "generation_test", resources });
}

test('shows "No session open"', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByText("No session open")).toBeTruthy();
});

test('offers a "New session" action', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

// The pane's standing CTA must read as a button, not bare text: it carries
// Button's bordered "secondary" variant (quiet renders chromeless). "Jump
// back in" owns primary when a resume candidate exists, so New session takes
// the design language's common-control look in both states.
test('"New session" is styled as a button (secondary variant, not quiet)', () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  const button = screen.getByRole("button", { name: "New session" });
  expect(buttonStyles.secondary, "button.module.css must define a .secondary class").toBeTruthy();
  expect(button.className).toContain(buttonStyles.secondary);
  expect(button.className).not.toContain(buttonStyles.quiet);
});

test("orients a new person to what a session can do", () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);

  expect(screen.getByText(/read and edit the repository/i)).toBeTruthy();
  expect(screen.getByText(/run commands/i)).toBeTruthy();
  expect(screen.getByText(/delegate work to helpers/i)).toBeTruthy();
});

test('clicking "New session" navigates to /new', async () => {
  const user = userEvent.setup();
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: "New session" }));
  expect(window.location.pathname).toBe("/new");
});

test("shows params.note as a hint when provided", () => {
  render(<Welcome params={{ note: "Starting a new session isn't available yet." }} paneId="welcome" focused={true} />);
  expect(screen.getByText("Starting a new session isn't available yet.")).toBeTruthy();
});

test("shows no hint when params.note is absent", () => {
  const { container } = render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(container.textContent).not.toMatch(/available yet/i);
});

test("WelcomeContent does not render example prompts", () => {
  render(<WelcomeContent />);
  expect(screen.queryByRole("button", { name: /Find and fix the root cause/i })).toBeNull();
  expect(screen.queryByText("Try a task to get started")).toBeNull();
});

test("WelcomeContent renders New session only when showNewSession is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryByRole("button", { name: "New session" })).toBeNull();
  rerender(<WelcomeContent showNewSession />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("WelcomeContent renders chord hints only when showHints is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryByText("command palette")).toBeNull();
  rerender(<WelcomeContent showHints />);
  expect(screen.getByText("command palette")).toBeTruthy();
});

// tbk8: a cold "/" with no restored pane layout (a fresh browser, or
// localStorage cleared) shows the bare Welcome pane with nothing pointing
// back at a live or recent session - on a narrow viewport there's no rail
// beside it to fall back on. "Jump back in" is Welcome's own affordance for
// that case, independent of whether a docked rail happens to be visible.
test('offers "Jump back in" to the first needs-you session when one exists', () => {
  setRows([node({ ref: "local:ny1", title: "Fix the thing", project: "myrepo" })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Fix the thing/s })).toBeTruthy();
});

test("falls back to the first live session when nothing needs you", () => {
  setRows([], [node({ ref: "local:live1", title: "Refactor auth", project: "myrepo" })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Refactor auth/s })).toBeTruthy();
});

test("needs-you outranks live when both exist", () => {
  setRows([node({ ref: "local:ny1", title: "Needs you" })], [node({ ref: "local:live1", title: "Just running" })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByRole("button", { name: /Jump back in.*Needs you/s })).toBeTruthy();
  expect(screen.queryByText(/Just running/)).toBeNull();
});

test('omits "Jump back in" when there is nothing live or needing you', () => {
  setRows();
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.queryByRole("button", { name: /Jump back in/ })).toBeNull();
});

test('omits "Jump back in" before the navigation resources have ever loaded', () => {
  // tree stays null (the store's own initial state, never fetched yet) -
  // must not crash and must not show a phantom resume link.
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.queryByRole("button", { name: /Jump back in/ })).toBeNull();
});

test('clicking "Jump back in" opens that session\'s pane', async () => {
  const user = userEvent.setup();
  setRows([], [node({ ref: "local:live1", title: "Refactor auth" })]);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: /Jump back in/ }));
  expect(window.location.pathname).toBe(`/s/${encodeURIComponent("local:live1")}`);
});

// T6: a quiet hint row teaching the three chords a new person has no other
// way to discover from this cold pane - KeyHint renders each chord's keys as
// real <kbd> elements (platform-split "Mod"), so these assertions target the
// description text next to each chord rather than the glyph itself.
describe("chord hints", () => {
  test("shows the command palette, focus composer, and next-needs-you chords", () => {
    render(<Welcome params={{}} paneId="welcome" focused={true} />);
    expect(screen.getByText(/^command palette$/i)).toBeTruthy();
    expect(screen.getByText(/focus (the )?composer/i)).toBeTruthy();
    expect(screen.getByText(/next session needing you/i)).toBeTruthy();
  });

  test("renders each chord through KeyHint's own <kbd> elements, not hand-rolled text", () => {
    render(<Welcome params={{}} paneId="welcome" focused={true} />);
    // 3 chords x 2 keys (Mod+K, Mod+I, Mod+J) + the bare "?" hint = 7 <kbd>s,
    // and the modifier glyph is KeyHint's platform-split rendering (⌘ or
    // Ctrl), which hand-rolled text wouldn't produce.
    const kbds = [...document.querySelectorAll("kbd")];
    expect(kbds.length).toBe(7);
    const modifierKbds = kbds.filter((kbd) => kbd.textContent === "⌘" || kbd.textContent === "Ctrl");
    expect(modifierKbds.length).toBe(3);
    expect(kbds.map((kbd) => kbd.textContent)).toContain("?");
  });

  test('mentions that "?" inside the command palette shows all shortcuts', () => {
    render(<Welcome params={{}} paneId="welcome" focused={true} />);
    expect(screen.getByText(/shows all shortcuts/i)).toBeTruthy();
  });
});
