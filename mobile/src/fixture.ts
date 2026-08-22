/**
 * Fixture mode — `?fixture=onboarding|servers|sessions|new|settings`.
 *
 * Loads deterministic seeded multi-profile data and a fake native bridge so the
 * mobile renderer can be exercised in a browser without a Hub, Tauri plugin,
 * or Keychain. Each fixture route targets a specific screen/state combination.
 * No credential, raw URL, or token is ever held in JS state.
 */

import {
  createShellServices,
  type ShellServiceBundle,
} from "./screens/fixture-services";
import type { ProfileRedacted } from "./services/nativeProfiles";

export type FixtureRoute =
  | "onboarding"
  | "servers"
  | "sessions"
  | "new"
  | "settings";

const TWO_PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "laptop", origin: "https://hub.example.com:8443" },
  { id: "p2", name: "server", origin: "http://192.168.1.10:8080" },
];

const OFFLINE_PROFILES: readonly ProfileRedacted[] = [
  { id: "p1", name: "workstation", origin: "https://hub.internal:9000" },
  { id: "p2", name: "staging", origin: "http://10.0.0.5:8080" },
];

export interface FixtureBundle {
  readonly services: ShellServiceBundle;
  readonly route: FixtureRoute;
  readonly initialTab: "sessions" | "new" | "settings";
  readonly seedProfiles: readonly ProfileRedacted[];
  readonly seedActiveProfileId: string | null;
  /** Profiles to mark unreachable for offline-state fixtures. */
  readonly offlineProfileIds: readonly string[];
}

export function isFixtureRoute(value: string | null): value is FixtureRoute {
  return (
    value === "onboarding" ||
    value === "servers" ||
    value === "sessions" ||
    value === "new" ||
    value === "settings"
  );
}

export function createFixture(route: FixtureRoute): FixtureBundle {
  switch (route) {
    case "onboarding":
      return {
        services: createShellServices({ profiles: [], activeProfileId: null }),
        route,
        initialTab: "sessions",
        seedProfiles: [],
        seedActiveProfileId: null,
        offlineProfileIds: [],
      };
    case "servers":
      return {
        services: createShellServices({
          profiles: TWO_PROFILES,
          activeProfileId: "p1",
        }),
        route,
        initialTab: "sessions",
        seedProfiles: TWO_PROFILES,
        seedActiveProfileId: "p1",
        offlineProfileIds: ["p2"],
      };
    case "sessions":
      return {
        services: createShellServices({
          profiles: TWO_PROFILES,
          activeProfileId: "p1",
        }),
        route,
        initialTab: "sessions",
        seedProfiles: TWO_PROFILES,
        seedActiveProfileId: "p1",
        offlineProfileIds: [],
      };
    case "new":
      return {
        services: createShellServices({
          profiles: TWO_PROFILES,
          activeProfileId: "p1",
        }),
        route,
        initialTab: "new",
        seedProfiles: TWO_PROFILES,
        seedActiveProfileId: "p1",
        offlineProfileIds: [],
      };
    case "settings":
      return {
        services: createShellServices({
          profiles: OFFLINE_PROFILES,
          activeProfileId: "p1",
        }),
        route,
        initialTab: "settings",
        seedProfiles: OFFLINE_PROFILES,
        seedActiveProfileId: "p1",
        offlineProfileIds: ["p1"],
      };
  }
}
