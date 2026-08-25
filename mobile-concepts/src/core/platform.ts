import type { Appearance, Platform } from "./model";

export interface PlatformDetectionInput {
  userAgent: string;
  navigatorPlatform?: string;
  maxTouchPoints?: number;
  allowOverride: boolean;
  override: string | null;
  fallback: Platform;
}

export interface PlatformPrimitives {
  platform: Platform;
  navigation: "ios-tab-bar" | "material-navigation-bar";
  title: "large-or-inline" | "material-top-app-bar";
  sheet: "ios-sheet" | "material-modal-bottom-sheet";
  dialog: "ios-alert" | "material-dialog";
  elevation: "hairline" | "tonal-elevation";
  feedback: "ios-highlight" | "material-state-layer";
  back: "stack-control" | "system-predictive";
  minimumTarget: 44 | 48;
  safeArea: "ios-environment" | "android-insets";
}

export const platformPrimitives = {
  ios: {
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
  },
  android: {
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
  },
} as const satisfies Record<Platform, PlatformPrimitives>;

export function getPlatformPrimitives(platform: Platform): PlatformPrimitives {
  return platformPrimitives[platform];
}

function isPlatform(value: string | null): value is Platform {
  return value === "ios" || value === "android";
}

export function resolvePlatform(input: PlatformDetectionInput): Platform {
  if (input.allowOverride && isPlatform(input.override)) {
    return input.override;
  }

  if (/android/i.test(input.userAgent)) return "android";
  if (/iPad|iPhone|iPod/i.test(input.userAgent)) return "ios";

  const desktopIPad =
    /Macintosh/i.test(input.userAgent) &&
    input.navigatorPlatform === "MacIntel" &&
    (input.maxTouchPoints ?? 0) > 1;
  return desktopIPad ? "ios" : input.fallback;
}

export function resolveEffectiveAppearance(
  preference: Appearance,
  prefersDark: boolean,
): "light" | "dark" {
  return preference === "system"
    ? prefersDark
      ? "dark"
      : "light"
    : preference;
}

export function resolveEffectiveReducedMotion(
  simulated: boolean,
  systemPrefersReducedMotion: boolean,
): boolean {
  return simulated || systemPrefersReducedMotion;
}
