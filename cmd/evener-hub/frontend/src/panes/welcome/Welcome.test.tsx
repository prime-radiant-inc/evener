import type { NavigationSessionSummary } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { FakeClient } from "@evener/appwire-client/testing/fakeClient";
import { cleanup, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, test } from "vitest";
import { connectionStore } from "../../stores/connection";
import { resetCredentialsStoreForTests } from "../../stores/credentials";
import { navigationStore, resetNavigationStoreForTests } from "../../stores/navigation/store";
import buttonStyles from "../../widgets/button/button.module.css";
import Welcome from "./Welcome";
import { WelcomeContent } from "./WelcomeContent";

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  resetNavigationStoreForTests();
  connectionStore.setState({ state: "idle", client: null });
  resetCredentialsStoreForTests();
});

test("automatic setup replaces Welcome so Back returns to the previous page", async () => {
  window.history.replaceState({}, "", "/settings");
  window.history.pushState({}, "", "/");
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  connectionStore.getState().connect(client);
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await waitFor(() => expect(window.location.pathname).toBe("/new"));
  window.history.back();
  await waitFor(() => expect(window.location.pathname).toBe("/settings"));
});

test("a background welcome pane does not replace the active session for setup", async () => {
  window.history.pushState({}, "", "/s/local:existing");
  const client = new FakeClient("ready");
  client.on("evener/instance/list", () => ({ instances: [], availableProviders: [] }));
  connectionStore.getState().connect(client);
  render(<Welcome params={{}} paneId="welcome" focused={false} />);
  await screen.findByTestId("empty-state");
  expect(window.location.pathname).toBe("/s/local:existing");
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
  const visibleResourceKeys = new Set<string>();
  for (const [section, rows] of [
    ["needs_you", needsYou],
    ["live", live],
  ] as const) {
    const key = { kind: "section", section, offset: 0, limit: 50 } as const;
    const id = keyID(key);
    visibleResourceKeys.add(id);
    resources.set(id, {
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
  navigationStore.setState({ mode: "v3", clientGenerationID: "generation_test", resources, visibleResourceKeys });
}

test("shows the empty state when no session is open", () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(screen.getByTestId("empty-state")).toBeTruthy();
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

test('clicking "New session" navigates to /new', async () => {
  const user = userEvent.setup();
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  await user.click(screen.getByRole("button", { name: "New session" }));
  expect(window.location.pathname).toBe("/new");
});

function emptyStateHint(): Element | null {
  return screen.getByTestId("empty-state").querySelector('[data-slot="hint"]');
}

test("shows params.note as a hint when provided", () => {
  render(<Welcome params={{ note: "note-from-params" }} paneId="welcome" focused={true} />);
  expect(emptyStateHint()?.textContent).toBe("note-from-params");
});

test("shows no hint when params.note is absent", () => {
  render(<Welcome params={{}} paneId="welcome" focused={true} />);
  expect(emptyStateHint()).toBeNull();
});

test("WelcomeContent does not render example prompts", () => {
  render(<WelcomeContent />);
  expect(screen.queryAllByRole("button")).toHaveLength(0);
});

test("WelcomeContent renders no prose beyond the chord-hint footer", () => {
  const { container } = render(<WelcomeContent showNewSession showHints />);
  expect(container.querySelectorAll("p")).toHaveLength(1);
});

test("WelcomeContent renders New session only when showNewSession is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryByRole("button", { name: "New session" })).toBeNull();
  rerender(<WelcomeContent showNewSession />);
  expect(screen.getByRole("button", { name: "New session" })).toBeTruthy();
});

test("WelcomeContent renders chord hints only when showHints is true", () => {
  const { rerender } = render(<WelcomeContent />);
  expect(screen.queryAllByRole("term")).toHaveLength(0);
  rerender(<WelcomeContent showHints />);
  expect(screen.getAllByRole("term")).toHaveLength(3);
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

test('can hide "Jump back in"', () => {
  setRows([], [node({ ref: "local:live1", title: "Refactor auth", project: "myrepo" })]);
  render(<WelcomeContent showResume={false} />);

  expect(screen.queryByRole("button", { name: /Jump back in/ })).toBeNull();
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
  test("pairs the command palette, focus composer, and next-needs-you chords with a description each", () => {
    render(<Welcome params={{}} paneId="welcome" focused={true} />);
    const chords = screen.getAllByRole("term").map((term) => [...term.querySelectorAll("kbd")].at(-1)?.textContent);
    expect(chords).toEqual(["K", "I", "J"]);
    const descriptions = screen.getAllByRole("definition");
    expect(descriptions).toHaveLength(3);
    for (const description of descriptions) expect(description.textContent).not.toBe("");
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
});
