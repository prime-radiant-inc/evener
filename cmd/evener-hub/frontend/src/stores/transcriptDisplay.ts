import type { AnyNotification, AppwireClientLike } from "@evener/appwire-client";
import {
  accessibleConfigSummary,
  configFingerprint,
  decodeLocalConfig,
  encodeLocalConfig,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
  type HubTranscriptDisplayDefault,
  normalizeConfig,
  resolveEffectiveConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
  toWireConfig,
  type ViewportClass,
  WireError,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { transitionTranscriptViews } from "../panes/session/transcript/flow/transcriptViewRegistry";
import { isMobileViewport, subscribeMobileViewport } from "../shell/useIsMobile";
import { connectionStore } from "./connection";
import { dualWriteTranscriptDisplayLegacy, migrateLegacyTranscriptDisplay, readTranscriptDisplayLocal } from "./prefs";
import { createReadyGenerationCallback } from "./readyGenerationCallback";
import { createBrowserSync } from "./transcriptDisplay/crossTabSync";
import {
  LOCAL_KEYS,
  removeLocal,
  reportStorageResult,
  verifyLegacyWrite,
  writeLocal,
} from "./transcriptDisplay/localStore";

export const TRANSCRIPT_DISPLAY_CHANNEL = "evener.transcript-display.v1";
// This store's own guard: keybindings.ts wires the same connectionStore
// client through its own instance, so the two never contend over one shared
// registration slot.
const readyGenerationCallback = createReadyGenerationCallback();
export const TRANSCRIPT_DISPLAY_CHANNEL_NAME = TRANSCRIPT_DISPLAY_CHANNEL;
type ConfigByLayout = Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
type HubByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;

export interface TranscriptDisplayChange {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

export interface TranscriptDisplayStoreState {
  viewport: ViewportClass;
  local: ConfigByLayout;
  hub: HubByLayout;
  drafts: ConfigByLayout;
  hubLoading: boolean;
  hubError: string | null;
  hubErrors: Partial<Record<ViewportClass, string>>;
  storageWarning: string | null;
  hubSupport: "unknown" | "supported" | "unsupported";
  setViewport(layout: ViewportClass): void;
  setLocal(layout: ViewportClass, config: TranscriptDisplayConfigV1): void;
  clearLocal(layout: ViewportClass): void;
  effective(layout?: ViewportClass): TranscriptDisplayConfigV1;
  applyHubChange(change: TranscriptDisplayChange): void;
  refreshHubDefaults(): Promise<void>;
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
}

function initialState(): Omit<
  TranscriptDisplayStoreState,
  "setViewport" | "setLocal" | "clearLocal" | "effective" | "applyHubChange" | "refreshHubDefaults" | "patchHubDefault"
> {
  return {
    viewport: "desktop",
    local: {},
    hub: {},
    drafts: {},
    hubLoading: false,
    hubError: null,
    hubErrors: {},
    storageWarning: null,
    hubSupport: "unknown",
  };
}

let initialized = false;
let stopViewportSubscription: (() => void) | null = null;
let wiredClient: AppwireClientLike | null = null;
let unwireNotification: (() => void) | null = null;
let unwireReady: (() => void) | null = null;
let clientEpoch = 0;
let activeReadyClient: AppwireClientLike | null = null;
let activeReadyEpoch = -1;
let refreshSerial = 0;
let patchSerial = 0;
const patchTokens = new Map<ViewportClass, number>();

class InvalidPatchResponseError extends Error {}

type EffectiveLayers = Pick<TranscriptDisplayStoreState, "viewport" | "local" | "hub">;

function effectiveForLayers(layers: EffectiveLayers, layout: ViewportClass): TranscriptDisplayConfigV1 {
  return resolveEffectiveConfig({
    local: layers.local[layout],
    hub: layers.hub[layout],
    layout,
  });
}

function publishEffectiveTransition(
  before: EffectiveLayers,
  after: EffectiveLayers,
  publish: () => void,
  targetLayout: ViewportClass,
  force = false,
): void {
  const beforeConfig = effectiveForLayers(before, before.viewport);
  const afterConfig = effectiveForLayers(after, after.viewport);
  const afterFingerprint = configFingerprint(afterConfig);
  const changed = configFingerprint(beforeConfig) !== afterFingerprint;
  if (!changed && !force) {
    publish();
    return;
  }
  transitionTranscriptViews(publish, accessibleConfigSummary(afterConfig), {
    fingerprint: afterFingerprint,
    targetLayout,
    force,
    prepareRemount: force,
    announce: changed,
  });
}

function isLayout(value: unknown): value is ViewportClass {
  return value === "desktop" || value === "mobile";
}

function currentSupport(): "unknown" | "supported" | "unsupported" {
  const features = connectionStore.getState().features;
  if (features === undefined) return "unknown";
  return features.transcriptDisplaySettings === true ? "supported" : "unsupported";
}

function currentClient(): AppwireClientLike | null {
  return connectionStore.getState().client;
}

function isCurrentReady(client: AppwireClientLike, epoch: number): boolean {
  return (
    wiredClient === client &&
    activeReadyClient === client &&
    activeReadyEpoch === epoch &&
    clientEpoch === epoch &&
    connectionStore.getState().client === client &&
    client.state === "ready"
  );
}

function invalidateReadyGeneration(): void {
  activeReadyClient = null;
  activeReadyEpoch = -1;
  clientEpoch += 1;
  unwireNotification?.();
  unwireNotification = null;
}

function beginReadyGeneration(client: AppwireClientLike): number {
  activeReadyClient = client;
  activeReadyEpoch = ++clientEpoch;
  const epoch = activeReadyEpoch;
  unwireNotification?.();
  unwireNotification = client.onNotification((notification) => {
    if (!isCurrentReady(client, epoch)) return;
    onNotification(notification);
  });
  return epoch;
}

function setSupportFromConnection(): void {
  const support = currentSupport();
  const state = transcriptDisplayStore.getState();
  if (support === "supported") {
    if (state.hubSupport !== support) transcriptDisplayStore.setState({ hubSupport: support });
    return;
  }
  if (state.hubSupport !== support || state.hubLoading || state.hubError !== null)
    transcriptDisplayStore.setState({ hubSupport: support, hubLoading: false, hubError: null });
}

function applyHubDefault(layout: ViewportClass, value: HubTranscriptDisplayDefault): void {
  const state = transcriptDisplayStore.getState();
  const previous = state.hub[layout];
  if (previous !== undefined && value.revision <= previous.revision) return;
  const hub = { ...state.hub, [layout]: value };
  publishEffectiveTransition(state, { ...state, hub }, () => transcriptDisplayStore.setState({ hub }), layout);
}

function onNotification(notification: AnyNotification): void {
  if (notification.method !== "evener/settings/transcriptDisplay/changed") return;
  const params = notification.params;
  if (!isLayout(params.layout) || !Number.isSafeInteger(params.revision) || params.revision < 0) return;
  const config = fromWireConfig(params.config);
  if (config === undefined) return;
  applyHubDefault(params.layout, { revision: params.revision, config });
}

async function refreshFor(client: AppwireClientLike, epoch: number): Promise<void> {
  if (!isCurrentReady(client, epoch) || currentSupport() !== "supported") return;
  const serial = ++refreshSerial;
  transcriptDisplayStore.setState({ hubLoading: true, hubError: null });
  try {
    const result = await client.request("evener/settings/transcriptDisplay/get", {});
    if (!isCurrentReady(client, epoch) || serial !== refreshSerial || currentSupport() !== "supported") return;
    const defaults = fromWireDefaults(result);
    if (defaults === undefined) throw new Error("Hub returned malformed transcript display defaults");
    applyHubDefault("desktop", defaults.desktop);
    applyHubDefault("mobile", defaults.mobile);
  } catch (error) {
    if (isCurrentReady(client, epoch) && serial === refreshSerial) {
      transcriptDisplayStore.setState({ hubError: error instanceof Error ? error.message : String(error) });
    }
  } finally {
    if (isCurrentReady(client, epoch) && serial === refreshSerial)
      transcriptDisplayStore.setState({ hubLoading: false });
  }
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  invalidateReadyGeneration();
  unwireReady?.();
  unwireReady = null;
  wiredClient = client;
  unwireReady = client.onReady(
    readyGenerationCallback(
      client,
      () => wiredClient,
      () => {
        const epoch = beginReadyGeneration(client);
        void refreshFor(client, epoch);
      },
    ),
  );
  if (client.state === "ready") {
    const epoch = beginReadyGeneration(client);
    void refreshFor(client, epoch);
  }
}

function onConnectionChange(
  state: ReturnType<typeof connectionStore.getState>,
  previous: ReturnType<typeof connectionStore.getState>,
): void {
  if (state.client !== wiredClient && state.client !== null) rewireClient(state.client);
  if (
    state.client === wiredClient &&
    previous.client === state.client &&
    previous.state === "ready" &&
    state.state !== "ready"
  ) {
    invalidateReadyGeneration();
  }
  if (state.client === null && wiredClient !== null) {
    invalidateReadyGeneration();
    unwireReady?.();
    unwireReady = null;
    wiredClient = null;
  }
  setSupportFromConnection();
  if (
    state.client === wiredClient &&
    state.features?.transcriptDisplaySettings === true &&
    previous.features?.transcriptDisplaySettings !== true &&
    state.client?.state === "ready"
  ) {
    if (activeReadyClient === state.client) void refreshFor(state.client, activeReadyEpoch);
  }
}

export const transcriptDisplayStore: StoreApi<TranscriptDisplayStoreState> = createStore<TranscriptDisplayStoreState>(
  () => ({
    ...initialState(),
    setViewport: (layout) => {
      const state = transcriptDisplayStore.getState();
      if (state.viewport === layout) return;
      publishEffectiveTransition(
        state,
        { ...state, viewport: layout },
        () => transcriptDisplayStore.setState({ viewport: layout }),
        layout,
        true,
      );
    },
    setLocal: (layout, input) => {
      const config = normalizeConfig(input);
      const state = transcriptDisplayStore.getState();
      const local = { ...state.local, [layout]: config };
      publishEffectiveTransition(state, { ...state, local }, () => transcriptDisplayStore.setState({ local }), layout);
      const encoded = encodeLocalConfig(config);
      const localOK = writeLocal(layout, encoded);
      dualWriteTranscriptDisplayLegacy(config);
      const legacyOK = verifyLegacyWrite(config);
      reportStorageResult((message) => transcriptDisplayStore.setState({ storageWarning: message }), localOK, legacyOK);
      browserSync.broadcastLocal(layout, encoded);
    },
    clearLocal: (layout) => {
      const state = transcriptDisplayStore.getState();
      const local = { ...state.local };
      delete local[layout];
      publishEffectiveTransition(state, { ...state, local }, () => transcriptDisplayStore.setState({ local }), layout);
      const localOK = removeLocal(layout);
      const fallback = resolveEffectiveConfig({ local: undefined, hub: state.hub[layout], layout });
      dualWriteTranscriptDisplayLegacy(fallback);
      const legacyOK = verifyLegacyWrite(fallback);
      reportStorageResult((message) => transcriptDisplayStore.setState({ storageWarning: message }), localOK, legacyOK);
      browserSync.broadcastLocal(layout, null);
    },
    effective: (layout): TranscriptDisplayConfigV1 => {
      const state = transcriptDisplayStore.getState();
      const selected = layout ?? state.viewport;
      return resolveEffectiveConfig({
        local: state.local[selected],
        hub: state.hub[selected],
        layout: selected,
      });
    },
    applyHubChange: (change) => {
      if (!isLayout(change.layout) || !Number.isSafeInteger(change.revision) || change.revision < 0) return;
      try {
        const config = normalizeConfig(change.config);
        applyHubDefault(change.layout, { revision: change.revision, config });
      } catch {
        // A malformed notification cannot be a confirmed hub record.
      }
    },
    refreshHubDefaults: async () => {
      const client = currentClient();
      if (client === null || client !== wiredClient || activeReadyClient !== client) return;
      await refreshFor(client, activeReadyEpoch);
    },
    patchHubDefault: async (layout, input): Promise<HubTranscriptDisplayDefault> => {
      const state = transcriptDisplayStore.getState();
      const client = currentClient();
      const generation = client === activeReadyClient ? activeReadyEpoch : -1;
      if (
        state.hubSupport !== "supported" ||
        currentSupport() !== "supported" ||
        client === null ||
        client !== wiredClient ||
        generation < 0 ||
        client.state !== "ready"
      ) {
        const error = "Hub transcript display settings are unavailable.";
        transcriptDisplayStore.setState({
          hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: error },
        });
        throw new Error(error);
      }
      const config = normalizeConfig(input);
      const confirmed = state.hub[layout] ?? shippedDefault(layout);
      const token = ++patchSerial;
      patchTokens.set(layout, token);
      transcriptDisplayStore.setState({
        drafts: { ...state.drafts, [layout]: config },
        hubErrors: { ...state.hubErrors, [layout]: undefined },
      });
      try {
        const result = await client.request("evener/settings/transcriptDisplay/patch", {
          layout,
          expectedRevision: confirmed.revision,
          config: toWireConfig(config),
        });
        if (patchTokens.get(layout) !== token || !isCurrentReady(client, generation)) {
          return transcriptDisplayStore.getState().hub[layout] ?? confirmed;
        }
        const resultRecord =
          typeof result === "object" && result !== null && !Array.isArray(result)
            ? (result as unknown as Record<string, unknown>)
            : {};
        const current = transcriptDisplayStore.getState().hub[layout] ?? confirmed;
        const canonicalConfig = fromWireConfig(resultRecord.config);
        const revision = resultRecord.revision;
        const responseLayout = resultRecord.layout;
        const exactResponse =
          Object.keys(resultRecord).length === 3 &&
          Object.hasOwn(resultRecord, "layout") &&
          Object.hasOwn(resultRecord, "revision") &&
          Object.hasOwn(resultRecord, "config");
        const requestedFingerprint = configFingerprint(config);
        const canonicalFingerprint = canonicalConfig === undefined ? undefined : configFingerprint(canonicalConfig);
        const confirmedFingerprint = configFingerprint(confirmed.config);
        const revisionIsValid =
          typeof revision === "number" &&
          Number.isSafeInteger(revision) &&
          revision >= current.revision &&
          (revision === confirmed.revision || revision === confirmed.revision + 1);
        const canonicalSemanticsValid =
          canonicalConfig !== undefined &&
          canonicalFingerprint === requestedFingerprint &&
          (revision === confirmed.revision
            ? requestedFingerprint === confirmedFingerprint
            : revision === confirmed.revision + 1);
        if (
          !exactResponse ||
          responseLayout !== layout ||
          canonicalConfig === undefined ||
          !revisionIsValid ||
          !canonicalSemanticsValid
        )
          throw new InvalidPatchResponseError("Hub returned malformed transcript display PATCH response");
        const canonical = { revision: revision as number, config: canonicalConfig };
        applyHubDefault(layout, canonical);
        const drafts = { ...transcriptDisplayStore.getState().drafts };
        delete drafts[layout];
        transcriptDisplayStore.setState({
          drafts,
          hubError: null,
          hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: undefined },
        });
        return canonical;
      } catch (error) {
        const applied = postApplyDefault(error, layout);
        if (applied !== undefined) {
          if (patchTokens.get(layout) !== token || !isCurrentReady(client, generation)) {
            return transcriptDisplayStore.getState().hub[layout] ?? confirmed;
          }
          // The patch APPLIED before a follow-up durable step failed: the hub
          // already published applied and will broadcast it to every other
          // client. Reconcile from it the same way the success path above
          // does, rather than treating this write as rejected.
          applyHubDefault(layout, applied);
          const drafts = { ...transcriptDisplayStore.getState().drafts };
          delete drafts[layout];
          transcriptDisplayStore.setState({
            drafts,
            hubError: null,
            hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: undefined },
          });
          return applied;
        }
        const canonical = conflictCurrent(error, layout);
        if (patchTokens.get(layout) !== token || !isCurrentReady(client, generation)) {
          if (canonical !== undefined) applyHubDefault(layout, canonical);
          return transcriptDisplayStore.getState().hub[layout] ?? canonical ?? confirmed;
        }
        if (canonical !== undefined) applyHubDefault(layout, canonical);
        if (error instanceof InvalidPatchResponseError) {
          const message = error.message;
          transcriptDisplayStore.setState({
            hubError: message,
            hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: message },
          });
          throw error;
        }
        const message = error instanceof Error ? error.message : String(error);
        const drafts = { ...transcriptDisplayStore.getState().drafts };
        delete drafts[layout];
        transcriptDisplayStore.setState({
          drafts,
          hubError: message,
          hubErrors: {
            ...transcriptDisplayStore.getState().hubErrors,
            [layout]: message,
          },
        });
        throw error;
      }
    },
  }),
);

function conflictCurrent(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || error.code !== -32013 || typeof error.data !== "object" || error.data === null)
    return undefined;
  const data = error.data as Record<string, unknown>;
  if (data.evenerErrorInfo !== "conflict" || data.layout !== layout) return undefined;
  return fromWireDefault(data.current);
}

// postApplyDefault extracts the applied canonical value from a
// transcriptDisplayPostApply error (evener/errors.go's
// ErrorTranscriptDisplayPostApply, hubcore.TranscriptDisplayPostApplyError):
// the patch already landed on the hub before a follow-up durable step
// failed, so the caller must reconcile from it instead of treating the
// write as rejected - the same shape as conflictCurrent above, keyed off a
// different error code and evenerErrorInfo.
function postApplyDefault(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || error.code !== -32603 || typeof error.data !== "object" || error.data === null)
    return undefined;
  const data = error.data as Record<string, unknown>;
  if (data.evenerErrorInfo !== "transcriptDisplayPostApply" || data.layout !== layout) return undefined;
  return fromWireDefault(data.applied);
}

connectionStore.subscribe(onConnectionChange);
const initialClient = connectionStore.getState().client;
if (initialClient !== null) rewireClient(initialClient);

function applyLocalFromBrowserSync(layout: ViewportClass, config: TranscriptDisplayConfigV1 | undefined): void {
  const state = transcriptDisplayStore.getState();
  const local = { ...state.local };
  if (config === undefined) delete local[layout];
  else local[layout] = config;
  publishEffectiveTransition(state, { ...state, local }, () => transcriptDisplayStore.setState({ local }), layout);
}

const browserSync = createBrowserSync({
  channelName: TRANSCRIPT_DISPLAY_CHANNEL,
  localKeys: LOCAL_KEYS,
  getState: () => transcriptDisplayStore.getState(),
  applyLocal: applyLocalFromBrowserSync,
  onDetach: () => {
    stopViewportSubscription?.();
    stopViewportSubscription = null;
  },
});

export function initTranscriptDisplay(): void {
  if (initialized) return;
  initialized = true;
  transcriptDisplayStore.setState({
    viewport: isMobileViewport() ? "mobile" : "desktop",
    local: {},
    hub: {},
    drafts: {},
    storageWarning: null,
  });
  const migrated = migrateLegacyTranscriptDisplay();
  const local: ConfigByLayout = {};
  let migrationWriteOK = migrated === undefined;
  for (const layout of ["desktop", "mobile"] as const) {
    const raw = readTranscriptDisplayLocal(layout);
    const config = decodeLocalConfig(raw);
    if (config !== undefined) local[layout] = config;
    if (migrated !== undefined && config === undefined) migrationWriteOK = false;
  }
  transcriptDisplayStore.setState({ local });
  if (!migrationWriteOK)
    transcriptDisplayStore.setState({
      storageWarning: "Transcript display migration could not be saved; it may not survive restart.",
    });
  browserSync.attach();
  stopViewportSubscription = subscribeMobileViewport(() => {
    transcriptDisplayStore.getState().setViewport(isMobileViewport() ? "mobile" : "desktop");
  });
}

export function resetTranscriptDisplayStoreForTests(): void {
  browserSync.detach();
  initialized = false;
  invalidateReadyGeneration();
  unwireReady?.();
  unwireReady = null;
  activeReadyClient = null;
  activeReadyEpoch = -1;
  wiredClient = null;
  refreshSerial += 1;
  patchTokens.clear();
  transcriptDisplayStore.setState({ ...initialState() });
  setSupportFromConnection();
}

export function useEffectiveTranscriptDisplay(layout?: ViewportClass): TranscriptDisplayConfigV1 {
  return useStore(transcriptDisplayStore, (state) => state.effective(layout));
}

export function useTranscriptDisplayStore(): TranscriptDisplayStoreState;
export function useTranscriptDisplayStore<T>(selector: (state: TranscriptDisplayStoreState) => T): T;
export function useTranscriptDisplayStore<T>(
  selector?: (state: TranscriptDisplayStoreState) => T,
): T | TranscriptDisplayStoreState {
  // Same Zustand hook in both arms; the overload only avoids exposing an
  // optional selector to useStore's stricter TypeScript signature.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call the same hook
  return selector ? useStore(transcriptDisplayStore, selector) : useStore(transcriptDisplayStore);
}
