import { describe, expect, it } from "vitest";
import {
  getPlatformPrimitives,
  platformPrimitives,
  resolveEffectiveAppearance,
  resolveEffectiveReducedMotion,
  resolvePlatform,
} from "./platform";

describe("resolvePlatform", () => {
  it.each([
    ["Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", "ios"],
    ["Mozilla/5.0 (Linux; Android 15; Pixel 9)", "android"],
  ] as const)("detects %s", (userAgent, expected) => {
    expect(
      resolvePlatform({
        userAgent,
        allowOverride: false,
        override: null,
        fallback: "ios",
      }),
    ).toBe(expected);
  });

  it("ignores an override in packaged mode", () => {
    expect(
      resolvePlatform({
        userAgent: "Android",
        allowOverride: false,
        override: "ios",
        fallback: "ios",
      }),
    ).toBe("android");
  });

  it.each(["ios", "android"] as const)(
    "accepts the %s development override",
    (override) => {
      expect(
        resolvePlatform({
          userAgent: "unknown",
          allowOverride: true,
          override,
          fallback: override === "ios" ? "android" : "ios",
        }),
      ).toBe(override);
    },
  );

  it("rejects unknown development overrides", () => {
    expect(
      resolvePlatform({
        userAgent: "unknown",
        allowOverride: true,
        override: "windows",
        fallback: "android",
      }),
    ).toBe("android");
  });

  it("recognizes an iPad using desktop user-agent hints", () => {
    expect(
      resolvePlatform({
        userAgent: "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)",
        navigatorPlatform: "MacIntel",
        maxTouchPoints: 5,
        allowOverride: false,
        override: null,
        fallback: "android",
      }),
    ).toBe("ios");
  });

  it.each(["ios", "android"] as const)(
    "fails an unknown packaged agent closed to the %s build target",
    (fallback) => {
      expect(
        resolvePlatform({
          userAgent: "unknown packaged webview",
          allowOverride: false,
          override: null,
          fallback,
        }),
      ).toBe(fallback);
    },
  );
});

describe("platform primitives", () => {
  it("defines every iOS primitive", () => {
    expect(platformPrimitives.ios).toEqual({
      platform: "ios",
      navigation: "ios-tab-bar",
      title: "large-or-inline",
      sheet: "ios-sheet",
      dialog: "ios-alert",
      elevation: "hairline",
      feedback: "ios-highlight",
      back: "stack-control",
      minimumTarget: 44,
      safeArea: "ios-environment",
    });
    expect(getPlatformPrimitives("ios")).toBe(platformPrimitives.ios);
  });

  it("defines every Android primitive", () => {
    expect(platformPrimitives.android).toEqual({
      platform: "android",
      navigation: "material-navigation-bar",
      title: "material-top-app-bar",
      sheet: "material-modal-bottom-sheet",
      dialog: "material-dialog",
      elevation: "tonal-elevation",
      feedback: "material-state-layer",
      back: "system-predictive",
      minimumTarget: 48,
      safeArea: "android-insets",
    });
    expect(getPlatformPrimitives("android")).toBe(platformPrimitives.android);
  });

  it("keeps load-bearing interaction semantics platform-specific", () => {
    for (const field of [
      "minimumTarget",
      "back",
      "sheet",
      "dialog",
      "feedback",
      "safeArea",
    ] as const) {
      expect(platformPrimitives.ios[field]).not.toBe(
        platformPrimitives.android[field],
      );
    }
  });
});

describe("effective runtime preferences", () => {
  it.each([
    ["light", false, "light"],
    ["light", true, "light"],
    ["dark", false, "dark"],
    ["dark", true, "dark"],
    ["system", false, "light"],
    ["system", true, "dark"],
  ] as const)("resolves %s with dark=%s", (preference, dark, expected) => {
    expect(resolveEffectiveAppearance(preference, dark)).toBe(expected);
  });

  it.each([
    [false, false, false],
    [true, false, true],
    [false, true, true],
    [true, true, true],
  ] as const)(
    "combines simulated=%s and system=%s",
    (simulated, system, expected) => {
      expect(resolveEffectiveReducedMotion(simulated, system)).toBe(expected);
    },
  );
});
