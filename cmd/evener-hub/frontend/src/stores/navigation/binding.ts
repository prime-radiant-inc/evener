import type { AppwireClientLike } from "@evener/appwire-client";
import { connectionStore, readyConnectionTransition } from "../connection";
import { initNavigation } from "./store";

// Navigation serves both browser surfaces; notification election remains workspace-only.
export function bindNavigationConnection(): () => void {
  let activeClient: AppwireClientLike | null = null;
  let releaseNavigation: () => void = () => {};
  const initialize = (client: AppwireClientLike) => {
    if (activeClient === client) return;
    releaseNavigation();
    releaseNavigation = initNavigation(client);
    activeClient = client;
  };
  const unsubscribe = connectionStore.subscribe((state, previous) => {
    if (state.client && readyConnectionTransition(state, previous)) initialize(state.client);
  });
  const { client, state } = connectionStore.getState();
  if (client && state === "ready") initialize(client);
  return () => {
    unsubscribe();
    releaseNavigation();
  };
}
