import { act, cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeAll, beforeEach, describe, expect, test, vi } from "vitest";
import { MOBILE_QUERY, resetMobileViewportForTests, TABLET_QUERY } from "../../../shell/useIsMobile";
import { installLocalStorage, MemoryStorage } from "../../../storageTestUtils";
import { prefsStore, resetPrefsStoreForTests } from "../../../stores/prefs";
import { Toast } from "../../../widgets";
import { getToasts, resetToastStoreForTests } from "../../../widgets/toast/store";
import { ThemeSection } from "./theme";

beforeAll(() => {
  installLocalStorage(new MemoryStorage());
});

beforeEach(() => {
  localStorage.clear();
  document.documentElement.removeAttribute("data-theme");
  delete document.body.dataset.phoneDensity;
  delete document.body.dataset.fontSize;
  delete document.body.dataset.transcriptMeasure;
  resetPrefsStoreForTests();
  resetToastStoreForTests();
});

/** Waits for a success toast naming the theme value just chosen. */
async function themeToasted(value: string): Promise<void> {
  await waitFor(() => expect(getToasts().at(-1)).toMatchObject({ kind: "success" }));
  expect(getToasts().at(-1)?.text).toContain(value);
}

function renderWithToasts() {
  render(
    <>
      <ThemeSection />
      <Toast />
    </>,
  );
}

describe("Color theme", () => {
  test("defaults to the System option checked", () => {
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "System" }).getAttribute("aria-checked")).toBe("true");
  });

  test("choosing Light persists the pref, applies data-theme, and toasts 'Theme: light'", async () => {
    const user = userEvent.setup();
    renderWithToasts();

    await user.click(screen.getByRole("radio", { name: "Light" }));

    expect(prefsStore.getState().theme).toBe("light");
    expect(document.documentElement.getAttribute("data-theme")).toBe("light");
    await themeToasted("light");
  });

  test("choosing System after a concrete theme removes data-theme and does not toast a stale value", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    await user.click(screen.getByRole("radio", { name: "Dark" }));
    await themeToasted("dark");

    await user.click(screen.getByRole("radio", { name: "System" }));

    expect(prefsStore.getState().theme).toBe("system");
    expect(document.documentElement.hasAttribute("data-theme")).toBe(false);
    await themeToasted("system");
  });
});

describe("Phone density", () => {
  test("defaults to Compact and does not toast on change (unlike Color theme)", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "Compact" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "Comfortable" }));

    expect(prefsStore.getState().phoneDensity).toBe("comfortable");
    expect(document.body.dataset.phoneDensity).toBe("comfortable");
    expect(getToasts()).toEqual([]);
  });
});

describe("Font size", () => {
  test("defaults to M and updates the pref plus document.body.dataset.fontSize", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "M" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "XL" }));

    expect(prefsStore.getState().fontSize).toBe("xl");
    expect(document.body.dataset.fontSize).toBe("xl");
  });
});

describe("Transcript width", () => {
  test("defaults to Reading and updates the pref plus document.body.dataset.transcriptMeasure", async () => {
    const user = userEvent.setup();
    renderWithToasts();
    expect(screen.getByRole("radio", { name: "Reading" }).getAttribute("aria-checked")).toBe("true");

    await user.click(screen.getByRole("radio", { name: "Wide" }));

    expect(prefsStore.getState().transcriptMeasure).toBe("wide");
    expect(document.body.dataset.transcriptMeasure).toBe("wide");
  });
});

test("the session list remembers only the choice made for the active layout", async () => {
  const user = userEvent.setup();
  const narrow = Object.assign(new EventTarget(), { matches: true });
  const tablet = Object.assign(new EventTarget(), { matches: false });
  const otherQuery = Object.assign(new EventTarget(), { matches: false });
  vi.stubGlobal("matchMedia", (query: string) =>
    query === MOBILE_QUERY ? narrow : query === TABLET_QUERY ? tablet : otherQuery,
  );
  const resize = (media: typeof narrow, matches: boolean) =>
    act(() => {
      media.matches = matches;
      media.dispatchEvent(Object.assign(new Event("change"), { matches }));
    });
  const stored = () =>
    ["phone", "tablet", "desktop"].map((layout) => localStorage.getItem(`evener.prefs.sessionListStyle.${layout}`));

  try {
    renderWithToasts();
    const controls = within(screen.getByTestId("session-list-style")).getByRole("radiogroup");
    const [automatic, threeLines, compact] = within(controls).getAllByRole("radio");
    if (!automatic || !threeLines || !compact) throw new Error("expected three style options");
    expect(controls.hasAttribute("aria-describedby")).toBe(true);
    expect(automatic.getAttribute("aria-checked")).toBe("true");
    expect(stored()).toEqual([null, null, null]);

    await user.click(compact);
    resize(tablet, true);
    expect(automatic.getAttribute("aria-checked")).toBe("true");
    expect(stored()).toEqual(["compact", null, null]);
    await user.click(compact);

    resize(narrow, false);
    expect(automatic.getAttribute("aria-checked")).toBe("true");
    expect(stored()).toEqual(["compact", "compact", null]);
    await user.click(threeLines);

    act(() => resetPrefsStoreForTests());
    expect(threeLines.getAttribute("aria-checked")).toBe("true");
    resize(narrow, true);
    expect(compact.getAttribute("aria-checked")).toBe("true");
    await user.click(automatic);
    resize(tablet, false);
    expect(compact.getAttribute("aria-checked")).toBe("true");
    expect(stored()).toEqual(["compact", null, "flat"]);
  } finally {
    cleanup();
    vi.unstubAllGlobals();
    resetMobileViewportForTests();
  }
});
