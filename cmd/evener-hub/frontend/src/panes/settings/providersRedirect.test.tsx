// Regression net for the "/settings/providers" old-bookmark redirect
// (triage #12: "/settings/providers"->"/credentials" redirect, stale
// bookmarks only). shell/routing.ts's urlToPane is a T1/chokepoint change
// this stream doesn't own or edit - routing.test.ts already locks its own
// pure output in isolation, and AppShell.test.tsx has an equivalent
// full-render regression for the sibling "/credentials" alias - but nothing
// chains urlToPane's REAL "/settings/providers" output into an actual
// rendered Settings pane the way that /credentials test does (it had no
// jsdom net of its own). AppShell.tsx/AppShell.test.tsx are themselves
// chokepoints this stream doesn't touch, so this composes the same two
// real, already-landed pieces (urlToPane + Settings) from a file this
// stream does own instead.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";
import { urlToPane } from "../../shell/routing";
import Settings, { type SettingsPaneParams } from "./Settings";

// jsdom has no window.matchMedia (useIsMobile.test.ts's own header comment);
// every test file that renders Settings stubs it locally - see
// Settings.test.tsx's identical helper (no shared test-utils module in this
// project - duplicating this is the established convention here).
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

test("the real /settings/providers redirect resolves to a rendered credentials section", () => {
  stubMatchMedia(false);

  const pane = urlToPane("/settings/providers");
  expect(pane).toEqual({ type: "settings", params: { section: "credentials" } });

  render(<Settings params={pane?.params as SettingsPaneParams} paneId="settings-1" focused={true} />);
  expect(screen.getByRole("heading", { name: "Providers & credentials" })).toBeTruthy();
  expect(screen.getByRole("button", { name: "Providers & credentials" }).getAttribute("aria-current")).toBe("page");
});
