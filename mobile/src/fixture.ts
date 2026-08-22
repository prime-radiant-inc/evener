/**
 * Fixture mode — `?fixture=onboarding|servers|sessions|new|settings`.
 *
 * Loads deterministic seeded multi-profile data and a fake native bridge so the
 * mobile renderer can be exercised in a browser without a Hub, Tauri plugin,
 * or Keychain. Each fixture route targets a specific screen/state combination.
 * No credential, raw URL, or token is ever held in JS state.
 */

import { createShellServices } from "./screens/fixture-services";
import type { ShellServiceBundle } from "./screens/root-types";
import type { ProfileRedacted } from "./services/nativeProfiles";
import type { Reachability } from "./state/connection";

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

/** Explicit per-profile reachability seed for each fixture route. */
const REACHABILITY_SEEDS: Record<FixtureRoute, Record<string, Reachability>> = {
  onboarding: {},
  servers: { p1: "reachable", p2: "unreachable" },
  sessions: { p1: "unknown", p2: "reconnecting" },
  new: { p1: "reachable", p2: "unknown" },
  settings: { p1: "unreachable", p2: "reconnecting" },
};

export interface FixtureBundle {
  readonly services: ShellServiceBundle;
  readonly route: FixtureRoute;
  readonly initialTab: "sessions" | "new" | "settings";
  readonly seedProfiles: readonly ProfileRedacted[];
  readonly seedActiveProfileId: string | null;
  /** Explicit per-profile reachability seed (no missing-map accidents). */
  readonly reachabilitySeed: Record<string, Reachability>;
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
        reachabilitySeed: REACHABILITY_SEEDS.onboarding,
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
        reachabilitySeed: REACHABILITY_SEEDS.servers,
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
        reachabilitySeed: REACHABILITY_SEEDS.sessions,
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
        reachabilitySeed: REACHABILITY_SEEDS.new,
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
        reachabilitySeed: REACHABILITY_SEEDS.settings,
      };
  }
}
