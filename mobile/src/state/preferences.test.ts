import { describe, expect, it } from "vitest";
import type { ContentSizeCategory } from "../native/contract";
import { createPreferencesStore } from "./preferences";

describe("preferences store — theme", () => {
  it("defaults to system theme", () => {
    const store = createPreferencesStore();
    expect(store.getState().theme).toBe("system");
  });

  it("sets light or dark theme", () => {
    const store = createPreferencesStore();
    store.getState().setTheme("light");
    expect(store.getState().theme).toBe("light");
    store.getState().setTheme("dark");
    expect(store.getState().theme).toBe("dark");
  });
});

describe("preferences store — reduced motion", () => {
  it("defaults to false (system preference respected at render time)", () => {
    const store = createPreferencesStore();
    expect(store.getState().reducedMotion).toBe(false);
  });

  it("can be toggled explicitly", () => {
    const store = createPreferencesStore();
    store.getState().setReducedMotion(true);
    expect(store.getState().reducedMotion).toBe(true);
  });
});

describe("preferences store — content size (Dynamic Type)", () => {
  it("defaults to large", () => {
    const store = createPreferencesStore();
    expect(store.getState().contentSize).toBe("large");
  });

  it("updates to an accessibility category", () => {
    const store = createPreferencesStore();
    const cat: ContentSizeCategory = "accessibilityExtraExtraExtraLarge";
    store.getState().setContentSize(cat);
    expect(store.getState().contentSize).toBe(cat);
  });

  it("updates to extraExtraLarge", () => {
    const store = createPreferencesStore();
    store.getState().setContentSize("extraExtraLarge");
    expect(store.getState().contentSize).toBe("extraExtraLarge");
  });
});
