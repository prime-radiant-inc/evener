/**
 * Shared type aliases for store handles used by screens.
 */
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { ConceptStorage } from "../live-concepts/live-ui-store";
import type { NativeBridge } from "../native/client";
import type {
  ProfileRedacted,
  ProfileService,
} from "../services/nativeProfiles";
import type { ConnectionState } from "../state/connection";
import type { NavigationState } from "../state/navigation";
import type { PreferencesState } from "../state/preferences";
import type { RosterState } from "../state/roster";
import type { ProfileScopedServices } from "./production-services";

export type ConnectionStore = UseBoundStore<StoreApi<ConnectionState>>;
export type NavigationStore = UseBoundStore<StoreApi<NavigationState>>;
export type PreferencesStore = UseBoundStore<StoreApi<PreferencesState>>;
export type RosterStore = UseBoundStore<StoreApi<RosterState>>;

/** Service bundle shared by fixture and production paths. */
export interface ShellServiceBundle {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
  readonly hapticCalls: string[];
  readonly contentSize: () => string;
}

/**
 * Minimal service surface the root shell and onboarding screen consume.
 * Both the fixture {@link ShellServiceBundle} (superset) and the production
 * {@link ProductionServices} satisfy this — neither exposes a hardcoded
 * content-size; the shell reads it via `native.getContentSize()` instead.
 */
export interface ShellServices {
  readonly profile: ProfileService;
  readonly native: NativeBridge;
  /** Local selected-concept persistence; fixtures may use the shell fallback. */
  readonly conceptStorage?: ConceptStorage;
  /** Production-only factory for the active profile's AppWire service graph. */
  readonly createProfileScopedServices?: (
    profile: ProfileRedacted,
  ) => ProfileScopedServices;
}
