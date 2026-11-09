import type { NavigationSessionSummary, ThreadStartResponse } from "@evener/appwire-client";
import { keyID } from "@evener/appwire-client/state/navigation";
import { screen, within } from "@testing-library/react";

import { vi } from "vitest";
import { navigationStore } from "../stores/navigation/store";

// Session-title queries in this file target navigation, so keep them scoped to
// the rail rather than matching the same session in workspace content.
export const rail = () => within(screen.getByTestId("rail"));

export const THREAD_CAPABILITIES = {
  send: false,
  steer: false,
  interrupt: false,
  compact: false,
  clear: false,
  forkFromTurn: false,
  shutdown: false,
  changeModel: false,
  changeVisionModel: false,
  queue: false,
  goal: false,
  sharedNotes: false,
  rename: false,
};

export function threadStartResponse(ref: string): ThreadStartResponse {
  return {
    thread: {
      id: ref.slice(ref.indexOf(":") + 1),
      sessionId: `sess_${ref}`,
      preview: "test",
      ephemeral: false,
      modelProvider: "anthropic/claude-sonnet-4-5",
      createdAt: 1000,
      updatedAt: 1000,
      status: { type: "idle" },
      cwd: "/tmp/project",
      cliVersion: "1.0.0",
      source: "local",
      evener: { ref, capabilities: THREAD_CAPABILITIES, queue: { revision: 0 } },
    },
    turn: { id: "turn_1", itemsView: "full", status: "idle" },
  };
}

// A first location read that failed outright: no retained data, so the
// route's placement must decide from the workspace it already holds.
export function installFailedLocation(ref: string): void {
  const key = { kind: "location", ref } as const;
  const resources = new Map(navigationStore.getState().resources);
  resources.set(keyID(key), {
    key,
    data: null,
    loadedRevision: null,
    targetRevision: null,
    forceToken: 0,
    etag: null,
    loading: false,
    stale: false,
    error: new Error("location read failed"),
    generationID: "generation_test",
  });
  navigationStore.setState({ mode: "v3", resources });
}

// jsdom implements no matchMedia at all (useIsMobile.test.ts's own header
// comment documents the probe), so a mobile-layout test installs one: the
// mobile query matches, every other query does not. Only useIsMobile reads
// this surface, and only ever at mount here - no test in this file crosses
// the breakpoint mid-run, so the listeners are inert stubs.
export function installMobileViewport(): void {
  vi.stubGlobal(
    "matchMedia",
    vi.fn((media: string) => ({
      media,
      matches: media === "(max-width: 899px)",
      addEventListener: () => {},
      removeEventListener: () => {},
    })),
  );
}

export function installSwitchableViewport(): (mobile: boolean) => void {
  let mobile = false;
  const listeners = new Set<(event: MediaQueryListEvent) => void>();
  const mediaQuery = {
    media: "(max-width: 899px)",
    get matches() {
      return mobile;
    },
    addEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.add(listener as (event: MediaQueryListEvent) => void);
    },
    removeEventListener: (_type: string, listener: EventListenerOrEventListenerObject) => {
      listeners.delete(listener as (event: MediaQueryListEvent) => void);
    },
  } as MediaQueryList;
  vi.stubGlobal(
    "matchMedia",
    vi.fn((query: string) =>
      query === mediaQuery.media
        ? mediaQuery
        : { matches: false, media: query, addEventListener: () => {}, removeEventListener: () => {} },
    ),
  );
  return (nextMobile: boolean) => {
    mobile = nextMobile;
    const event = { matches: mobile, media: mediaQuery.media } as MediaQueryListEvent;
    for (const listener of listeners) listener(event);
  };
}

export const LIVE_CYCLE_B: NavigationSessionSummary = {
  ref: "local:live-b",
  host_id: "local",
  session_id: "live-b",
  title: "Live B",
  project: "prime-radiant",
  state: "idle",
  kind: "session",
  live: true,
  children: [],
};
