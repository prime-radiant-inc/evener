import { cleanup, render, screen } from "@testing-library/react";
import { Suspense } from "react";
import { afterEach, beforeAll, expect, test, vi } from "vitest";
import { paneFor } from "../../shell/paneRegistry";

// Await the modules ONCE up front so React.lazy resolves from a warm module
// cache (mirrors panes/session/index.test.tsx's own beforeAll pattern - see
// that file for the full reasoning). Warming ./index alone is not enough:
// the descriptor lazy-loads ./Settings as its own chunk, and that transform
// is the slow part the render test's findByRole would otherwise race.
beforeAll(async () => {
  await import("./index");
  await import("./Settings");
});

afterEach(() => {
  cleanup();
  window.history.pushState({}, "", "/");
});

function stubMatchMedia(matches: boolean) {
  window.matchMedia = vi.fn().mockReturnValue({
    matches,
    media: "",
    addEventListener: () => {},
    removeEventListener: () => {},
  }) as unknown as typeof window.matchMedia;
}

test('registers "settings" as a singleton pane', () => {
  const descriptor = paneFor("settings");
  expect(descriptor.id).toBe("settings");
  expect(descriptor.singleton).toBe(true);
});

test("the title reflects the focused section, defaulting to General when none is given", () => {
  const descriptor = paneFor("settings");
  expect(descriptor.title({}, {})).toBe("General");
  expect(descriptor.title({ section: "hub" }, {})).toBe("Hub");
  expect(descriptor.title({ section: "credentials" }, {})).toBe("Providers & credentials");
});

test("the registered component renders the settings pane", async () => {
  stubMatchMedia(false);
  const descriptor = paneFor("settings");
  const SettingsComponent = descriptor.component;
  render(
    <Suspense fallback={null}>
      <SettingsComponent params={{}} paneId="settings-1" focused={true} />
    </Suspense>,
  );
  expect(await screen.findByRole("navigation", { name: "Settings sections" })).toBeTruthy();
  // @ts-expect-error test cleanup, matches Settings.test.tsx's own pattern
  delete window.matchMedia;
});
