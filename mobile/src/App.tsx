/**
 * App entry — wires the RootShell with real or fixture services.
 *
 * In production, the real `nativeProfiles` service and `NativeBridge` are
 * injected. In fixture mode (`?fixture=onboarding|servers|sessions|new|settings`),
 * deterministic seeded multi-profile data and a fake bridge load so the
 * renderer can be exercised in a browser without a Hub.
 */
import type { JSX } from "react";
import "./ui/global.css";
import { createFixture, type FixtureRoute, isFixtureRoute } from "./fixture";
import {
  createShellServices,
  type ShellServiceBundle,
} from "./screens/fixture-services";
import { RootShell } from "./screens/RootShell";
import { createConnectionStore } from "./state/connection";
import { createNavigationStore, type RootTab } from "./state/navigation";
import { createPreferencesStore } from "./state/preferences";

export interface AppProps {
  readonly fixture?: boolean;
  readonly fixtureRoute?: string;
}

function readFixtureRoute(): string | null {
  if (typeof window === "undefined") return null;
  const params = new URLSearchParams(window.location.search);
  return params.get("fixture");
}

export function App(props: AppProps): JSX.Element {
  const routeParam = props.fixtureRoute ?? readFixtureRoute();
  const isFixture = isFixtureRoute(routeParam);

  if (isFixture) {
    return <FixtureApp route={routeParam} />;
  }

  // Production wiring — real services (or fixture=false test fallback).
  if (props.fixture) {
    const services = createShellServices({
      profiles: [],
      activeProfileId: null,
    });
    return <ShellHost services={services} initialTab="sessions" />;
  }

  // Production: real services will be wired in the Tauri build. For now, use
  // the shell with empty profiles (shows onboarding).
  const services = createShellServices({ profiles: [], activeProfileId: null });
  return <ShellHost services={services} initialTab="sessions" />;
}

function FixtureApp({ route }: { readonly route: FixtureRoute }): JSX.Element {
  const fixture = createFixture(route);
  return (
    <ShellHost
      services={fixture.services}
      initialTab={fixture.initialTab}
      offlineProfileIds={fixture.offlineProfileIds}
    />
  );
}

function ShellHost(props: {
  readonly services: ShellServiceBundle;
  readonly initialTab: RootTab;
  readonly offlineProfileIds?: readonly string[];
}): JSX.Element {
  const { services, initialTab, offlineProfileIds } = props;
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  // Seed offline reachability for fixture state testing.
  if (offlineProfileIds) {
    for (const id of offlineProfileIds) {
      stores.connection.getState().setReachability(id, "unreachable");
    }
  }
  if (stores.navigation.getState().tab !== initialTab) {
    stores.navigation.getState().setTab(initialTab);
  }
  return <RootShell services={services} stores={stores} />;
}
