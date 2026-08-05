import { cleanup, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, expect, test, vi } from "vitest";
import Settings from "./Settings";

// jsdom does not implement window.matchMedia at all - useIsMobile.test.ts's
// own header comment documents this; every test file that drives mobile
// layout stubs it locally (no shared test-utils module in this project -
// see stores/threads.test.ts's own precedent for duplicating rather than
// sharing this kind of helper).
function stubMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockReturnValue({
    matches,
    media: "",
    addEventListener: () => {},
    removeEventListener: () => {},
  }) as unknown as typeof window.matchMedia;
}

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
  // @ts-expect-error restores jsdom's own honest default between tests.
  delete window.matchMedia;
});

test("bare params show the default (General) section", () => {
  stubMatchMedia(false);
  render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("heading", { name: "General" })).toBeTruthy();
  // General's own nav link is the active one.
  expect(screen.getByRole("button", { name: "General" }).getAttribute("aria-current")).toBe("page");
});

test("params.section selects that section", () => {
  stubMatchMedia(false);
  render(<Settings params={{ section: "theme" }} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("button", { name: "Theme" }).getAttribute("aria-current")).toBe("page");
});

test("desktop: nav and content render simultaneously, with no back button", () => {
  stubMatchMedia(false);
  render(<Settings params={{}} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "General" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back to settings" })).toBeNull();
});

test("desktop: clicking a nav link requests navigation to that section's URL", async () => {
  // Settings.tsx's own `params` are owned by its caller (in the real app,
  // workspaceStore via DockHost) - re-rendering with a NEW activeId after
  // navigate() is that integration's job, exercised in AppShell.test.tsx
  // (mirroring how Welcome.test.tsx's own "clicking New session" test only
  // checks the URL, not a re-render of Welcome itself). This isolated
  // render only proves Settings.tsx requests the right URL.
  stubMatchMedia(false);
  const user = userEvent.setup();
  render(<Settings params={{}} paneId="settings-1" focused={true} />);

  await user.click(screen.getByRole("button", { name: "Storage" }));

  expect(window.location.pathname).toBe("/settings/storage");
});

test("mobile: initial render shows the content view with a visible back button, not the nav list", () => {
  stubMatchMedia(true);
  render(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("button", { name: "Back to settings" })).toBeTruthy();
  expect(screen.getByRole("heading", { name: "Hub" })).toBeTruthy();
  expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
});

test("mobile: clicking the back button shows the nav list and hides the content/back button", async () => {
  stubMatchMedia(true);
  const user = userEvent.setup();
  render(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);

  await user.click(screen.getByRole("button", { name: "Back to settings" }));

  expect(screen.getByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  expect(screen.queryByRole("button", { name: "Back to settings" })).toBeNull();
  expect(screen.queryByText("This section hasn't been built yet.")).toBeNull();
});

test("mobile: choosing a link from the nav list returns to the content view", async () => {
  stubMatchMedia(true);
  const user = userEvent.setup();
  render(<Settings params={{ section: "hub" }} paneId="settings-1" focused={true} />);
  await user.click(screen.getByRole("button", { name: "Back to settings" }));
  await screen.findByRole("navigation", { name: "Settings sections" });

  await user.click(screen.getByRole("button", { name: "Storage" }));

  expect(screen.getByRole("button", { name: "Back to settings" })).toBeTruthy();
  expect(screen.queryByRole("navigation", { name: "Settings sections" })).toBeNull();
  expect(window.location.pathname).toBe("/settings/storage");
});

test("the pane title reflects the focused section", () => {
  stubMatchMedia(false);
  render(<Settings params={{ section: "credentials" }} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("heading", { name: "Providers & credentials" })).toBeTruthy();
});
