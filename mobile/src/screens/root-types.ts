/**
 * Shared type aliases for store handles used by screens.
 */
import type { StoreApi } from "zustand";
import type { UseBoundStore } from "zustand/react";
import type { ConnectionState } from "../state/connection";
import type { NavigationState } from "../state/navigation";
import type { PreferencesState } from "../state/preferences";

export type ConnectionStore = UseBoundStore<StoreApi<ConnectionState>>;
export type NavigationStore = UseBoundStore<StoreApi<NavigationState>>;
export type PreferencesStore = UseBoundStore<StoreApi<PreferencesState>>;
