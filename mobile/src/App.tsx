/**
 * App entry — wires the RootShell with real or fixture services.
 *
 * In production, real Tauri-backed services are injected via
 * {@link createProductionServices}. In fixture mode
 * (`?fixture=onboarding|servers|sessions|new|settings`), deterministic seeded
 * multi-profile data and a fake bridge load so the renderer can be exercised
 * in a browser without a Hub.
 */
import type { JSX } from "react";
import "./ui/global.css";
import { createFixture, type FixtureRoute, isFixtureRoute } from "./fixture";
import { createProductionServices } from "./screens/production-services";
import { RootShell } from "./screens/RootShell";
import type { ShellServiceBundle } from "./screens/root-types";
import { createConnectionStore, type Reachability } from "./state/connection";
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

  // Fixture mode — only when an explicit ?fixture= param is present.
  if (isFixtureRoute(routeParam)) {
    return <FixtureApp route={routeParam} />;
  }

  // Production — real Tauri-backed services.
  if (props.fixture) {
    // Test fallback: fixture=true without a route shows the empty shell.
    // This path is only hit by tests that pass fixture={true} explicitly.
    // with an empty profile store (shows onboarding).
    const services = createProductionServices();
    return <ShellHost services={services} initialTab="sessions" />;
  }

  const services = createProductionServices();
  return <ShellHost services={services} initialTab="sessions" />;
}

function FixtureApp({ route }: { readonly route: FixtureRoute }): JSX.Element {
  const fixture = createFixture(route);
  return (
    <ShellHost
      services={fixture.services}
      initialTab={fixture.initialTab}
      reachabilitySeed={fixture.reachabilitySeed}
    />
  );
}

function ShellHost(props: {
  readonly services:
    | ShellServiceBundle
    | ReturnType<typeof createProductionServices>;
  readonly initialTab: RootTab;
  readonly reachabilitySeed?: Record<string, Reachability>;
}): JSX.Element {
  const { services, initialTab, reachabilitySeed } = props;
  const stores = {
    connection: createConnectionStore(services.profile),
    navigation: createNavigationStore(),
    preferences: createPreferencesStore(),
  };
  if (reachabilitySeed) {
    for (const [id, state] of Object.entries(reachabilitySeed)) {
      stores.connection.getState().setReachability(id, state);
    }
  }
  if (stores.navigation.getState().tab !== initialTab) {
    stores.navigation.getState().setTab(initialTab);
  }
  return <RootShell services={services} stores={stores} />;
}
