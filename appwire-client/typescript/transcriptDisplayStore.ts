// The read-side transcript display defaults store shared by the web and native
// settings surfaces. It keeps the hub's per-layout confirmed defaults behind a
// framework-free store and fences every request by the active ready generation.

import type { AppwireClient } from "./client";
import { errorText } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { createReadyGenerationFence, type ReadyGenerationFence } from "./readyGenerationFence";
import { createSettingsHubGeneration, retireSettingsHubPayload } from "./settingsHubGeneration";
import {
  fromWireConfig,
  fromWireDefaults,
  type HubTranscriptDisplayDefault,
  normalizeConfig,
  type TranscriptDisplayConfigV1,
  type ViewportClass,
} from "./transcriptDisplayConfig";
import type { AnyNotification, FeatureSet } from "./types.gen";

export type TranscriptDisplayClient = Pick<AppwireClient, "request" | "onNotification">;

export type TranscriptDisplaySupport = "unknown" | "supported" | "unsupported";

export function transcriptDisplaySupport(
  features: Pick<FeatureSet, "transcriptDisplaySettings"> | undefined,
): TranscriptDisplaySupport {
  if (features === undefined) return "unknown";
  return features.transcriptDisplaySettings === true ? "supported" : "unsupported";
}

export const LAYOUTS: readonly ViewportClass[] = ["desktop", "mobile"];

export function isViewportClass(value: unknown): value is ViewportClass {
  return value === "desktop" || value === "mobile";
}

export type HubDefaultsByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;

export interface TranscriptDisplayChange {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

export interface TranscriptDisplayStoreFields {
  hubSupport: TranscriptDisplaySupport;
  hubLoading: boolean;
  hubError: string | null;
  /** Confirmed defaults keep presenting across a transient disconnect. */
  hub: HubDefaultsByLayout;
  /** True only after the current ready generation's read has completed. */
  loaded: boolean;
  /** Reserved for the write-side settings-hub lifecycle. */
  saving: boolean;
  /** Reserved for an unsettled write outcome from the write-side lifecycle. */
  writeUncertain: boolean;
}

export interface TranscriptDisplayStoreActions {
  refreshHubDefaults(): Promise<void>;
  applyHubChange(change: TranscriptDisplayChange): void;
}

export type TranscriptDisplayStoreState = TranscriptDisplayStoreFields & TranscriptDisplayStoreActions;

export interface TranscriptDisplayStore extends FrameworkFreeStore<TranscriptDisplayStoreState> {
  setSupport(support: TranscriptDisplaySupport): void;
  beginReadyGeneration(): void;
  endReadyGeneration(): void;
  detachHub(): void;
  reset(): void;
  dispose(): void;
}

export interface TranscriptDisplayStoreDeps {
  client: TranscriptDisplayClient;
}

function initialState(): TranscriptDisplayStoreFields {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    hub: {},
    loaded: false,
    saving: false,
    writeUncertain: false,
  };
}

const MALFORMED_DEFAULTS_MESSAGE = "Hub returned malformed transcript display defaults";

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

export function fromWireChange(value: unknown): TranscriptDisplayChange | undefined {
  if (!isRecord(value) || !isViewportClass(value.layout) || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { layout: value.layout, revision: value.revision, config };
}

export function createTranscriptDisplayStore(deps: TranscriptDisplayStoreDeps): TranscriptDisplayStore {
  const { client } = deps;
  const fence: ReadyGenerationFence = createReadyGenerationFence(isSupported);
  let missedChangeNotification = false;

  const store = createFrameworkFreeStore<TranscriptDisplayStoreState>(() => ({
    ...initialState(),
    refreshHubDefaults,
    applyHubChange,
  }));
  const { getState, setState } = store;

  function isSupported(): boolean {
    return getState().hubSupport === "supported";
  }

  function retirePayload(extra: Partial<TranscriptDisplayStoreFields> = {}): void {
    retireSettingsHubPayload(fence, getState, setState, extra);
  }

  /** A confirmed default is monotonic within a generation. The first
   * authoritative payload of a new generation may restart revision numbering. */
  function applyHubDefault(
    layout: ViewportClass,
    value: HubTranscriptDisplayDefault,
    extra: Partial<TranscriptDisplayStoreFields> = {},
  ): boolean {
    const previous = getState().hub[layout];
    if (!fence.awaitingFirstPayload && previous !== undefined && value.revision <= previous.revision) {
      if (Object.keys(extra).length > 0) setState(extra);
      return false;
    }
    setState({ hub: { ...getState().hub, [layout]: value }, ...extra });
    return true;
  }

  const { beginReadyGeneration, endReadyGeneration } = createSettingsHubGeneration({
    fence,
    wireNotifications: (generation) => {
      missedChangeNotification = false;
      return client.onNotification((notification) => {
        if (!fence.isCurrent(generation)) return;
        onNotification(notification, generation);
      });
    },
    retirePayload,
  });

  function detachHub(): void {
    retirePayload({ hub: {}, hubError: null });
  }

  function setSupport(support: TranscriptDisplaySupport): void {
    const state = getState();
    if (support === "supported") {
      if (state.hubSupport === support) return;
      if (state.hubSupport === "unsupported" && fence.generation >= 0) beginReadyGeneration();
      setState({ hubSupport: support });
      if (fence.generation >= 0) void refreshFor(fence.generation);
      return;
    }
    if (support === "unsupported") {
      if (state.hubSupport === support) return;
      retirePayload({ hubSupport: support, hubError: null, hub: {} });
      return;
    }
    if (state.hubSupport !== support || state.hubLoading || state.hubError !== null) {
      setState({ hubSupport: support, hubLoading: false, hubError: null });
    }
  }

  function changeArrivedBeforeConfirmation(): boolean {
    if (fence.generation < 0 || getState().loaded) return false;
    missedChangeNotification = true;
    return true;
  }

  function externalPayloadLanded(applied: boolean): void {
    if (applied && getState().hubError !== null) setState({ hubError: null });
  }

  function onNotification(notification: AnyNotification, generation: number): void {
    if (notification.method !== "evener/settings/transcriptDisplay/changed") return;
    if (!isSupported()) return;
    if (changeArrivedBeforeConfirmation()) return;
    const change = fromWireChange(notification.params);
    if (change === undefined) {
      void refreshFor(generation);
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config: change.config }));
  }

  function applyHubChange(change: TranscriptDisplayChange): void {
    if (!isViewportClass(change.layout) || !isRevision(change.revision)) return;
    if (!fence.liveHub(fence.generation)) return;
    if (changeArrivedBeforeConfirmation()) return;
    let config: TranscriptDisplayConfigV1;
    try {
      config = normalizeConfig(change.config);
    } catch {
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config }));
  }

  async function refreshFor(generation: number): Promise<void> {
    if (!fence.liveHub(generation)) return;
    const serial = fence.claimRead();
    const stillMine = () => fence.readStillMine(generation, serial);
    setState({ hubLoading: true, hubError: null });
    if (!stillMine()) return;
    try {
      const result = await client.request("evener/settings/transcriptDisplay/get", {});
      if (!stillMine()) return;
      const defaults = fromWireDefaults(result);
      if (defaults === undefined) throw new Error(MALFORMED_DEFAULTS_MESSAGE);
      applyHubDefault("desktop", defaults.desktop);
      if (!stillMine()) return;
      applyHubDefault("mobile", defaults.mobile);
      if (!stillMine()) return;
      fence.firstPayloadApplied();
      setState({ loaded: true, hubLoading: false });
      if (!stillMine()) return;
      if (missedChangeNotification) {
        missedChangeNotification = false;
        void refreshFor(generation);
      }
    } catch (error) {
      if (stillMine()) setState({ hubError: errorText(error), hubLoading: false });
    }
  }

  async function refreshHubDefaults(): Promise<void> {
    if (fence.generation < 0) return;
    await refreshFor(fence.generation);
  }

  return {
    ...store,
    setSupport,
    beginReadyGeneration,
    endReadyGeneration,
    detachHub,
    reset() {
      endReadyGeneration();
      missedChangeNotification = false;
      setState(initialState());
    },
    dispose() {
      if (fence.disposed) return;
      endReadyGeneration();
      fence.dispose();
    },
  };
}
