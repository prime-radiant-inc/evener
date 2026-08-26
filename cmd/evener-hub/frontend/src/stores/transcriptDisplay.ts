import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { transitionTranscriptViews } from "../panes/session/transcript/flow/transcriptViewRegistry";
import { WireError } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { AnyNotification } from "../protocol/types.gen";
import { isMobileViewport, subscribeMobileViewport } from "../shell/useIsMobile";
import {
  configFingerprint,
  configSummary,
  decodeLocalConfig,
  encodeLocalConfig,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
  type HubTranscriptDisplayDefault,
  legacyWritesFromConfig,
  normalizeConfig,
  resolveEffectiveConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
  toWireConfig,
  type ViewportClass,
} from "../transcriptDisplay/config";
import { connectionStore } from "./connection";
import {
  dualWriteTranscriptDisplayLegacy,
  migrateLegacyTranscriptDisplay,
  readLegacyPreference,
  readTranscriptDisplayLocal,
} from "./prefs";

export const TRANSCRIPT_DISPLAY_CHANNEL = "evener.transcript-display.v1";
export const TRANSCRIPT_DISPLAY_CHANNEL_NAME = TRANSCRIPT_DISPLAY_CHANNEL;
const LOCAL_KEYS: Record<ViewportClass, string> = {
  desktop: "evener.prefs.transcriptDisplay.desktop",
  mobile: "evener.prefs.transcriptDisplay.mobile",
};
const LEGACY_KEYS = [
  "transcriptRoundTimings",
  "transcriptTokenCounts",
  "transcriptHookExitsAll",
  "transcriptHookExitsNormal",
  "transcriptPromptLoaded",
  "showCost",
] as const;

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

interface LocalMessage {
  version: 1;
  sourceId: string;
  layout: ViewportClass;
  config: string | null;
  fingerprint: string | null;
}

const initialState = (): Omit<
  TranscriptDisplayStoreState,
  "setViewport" | "setLocal" | "clearLocal" | "effective" | "applyHubChange" | "refreshHubDefaults" | "patchHubDefault"
> => ({
  viewport: "desktop",
  local: {},
  hub: {},
  drafts: {},
  hubLoading: false,
  hubError: null,
  hubErrors: {},
  storageWarning: null,
  hubSupport: "unknown",
});

let initialized = false;
let channel: BroadcastChannel | null = null;
let sourceId = "";
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
const patchRevisionHints = new Map<ViewportClass, number>();

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
  const changed = configFingerprint(beforeConfig) !== configFingerprint(afterConfig);
  if (!changed && !force) {
    publish();
    return;
  }
  transitionTranscriptViews(publish, configSummary(afterConfig), {
    fingerprint: configFingerprint(afterConfig),
    targetLayout,
    force,
    prepareRemount: force,
    announce: changed,
  });
}

function makeSourceId(): string {
  try {
    if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
  } catch {
    // Some privacy modes expose crypto but deny randomUUID.
  }
  return `${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
}

function setStorageWarning(message: string | null): void {
  transcriptDisplayStore.setState({ storageWarning: message });
}

function writeLocal(layout: ViewportClass, encoded: string): boolean {
  try {
    if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
    localStorage.setItem(LOCAL_KEYS[layout], encoded);
    if (localStorage.getItem(LOCAL_KEYS[layout]) !== encoded) throw new Error("localStorage did not retain the value");
    return true;
  } catch {
    return false;
  }
}

function removeLocal(layout: ViewportClass): boolean {
  try {
    if (typeof localStorage === "undefined") throw new Error("localStorage is unavailable");
    localStorage.removeItem(LOCAL_KEYS[layout]);
    if (localStorage.getItem(LOCAL_KEYS[layout]) !== null) throw new Error("localStorage retained the value");
    return true;
  } catch {
    return false;
  }
}

function verifyLegacyWrite(config: TranscriptDisplayConfigV1): boolean {
  try {
    const expected = legacyWritesFromConfig(config);
    for (const key of LEGACY_KEYS) {
      const raw = readLegacyPreference(key);
      if (raw !== (expected[key] ? "1" : "0")) return false;
    }
    return true;
  } catch {
    return false;
  }
}

function reportStorageResult(localOK: boolean, legacyOK: boolean): void {
  if (localOK && legacyOK) {
    setStorageWarning(null);
    return;
  }
  setStorageWarning(
    "Transcript display changed for this tab, but browser storage is unavailable; it may not survive restart.",
  );
}

function broadcastLocal(layout: ViewportClass, encoded: string | null): void {
  if (channel === null) return;
  const decoded = encoded === null ? undefined : decodeLocalConfig(encoded);
  if (encoded !== null && decoded === undefined) return;
  const message: LocalMessage = {
    version: 1,
    sourceId,
    layout,
    config: encoded,
    fingerprint: decoded === undefined ? null : configFingerprint(decoded),
  };
  try {
    channel.postMessage(message);
  } catch {
    // BroadcastChannel is an enhancement; storage and the origin tab remain
    // authoritative when a browser closes it or refuses a message.
  }
}

function isLayout(value: unknown): value is ViewportClass {
  return value === "desktop" || value === "mobile";
}

function isLocalMessage(value: unknown): value is LocalMessage {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const candidate = value as Record<string, unknown>;
  if (
    Object.keys(candidate).length !== 5 ||
    candidate.version !== 1 ||
    typeof candidate.sourceId !== "string" ||
    candidate.sourceId === "" ||
    !isLayout(candidate.layout) ||
    !(candidate.config === null || typeof candidate.config === "string") ||
    !(candidate.fingerprint === null || typeof candidate.fingerprint === "string")
  )
    return false;
  if (candidate.config === null) return candidate.fingerprint === null;
  const config = decodeLocalConfig(candidate.config);
  return config !== undefined && candidate.fingerprint === configFingerprint(config);
}

function applyIncomingLocal(message: LocalMessage): void {
  if (message.sourceId === sourceId) return;
  const state = transcriptDisplayStore.getState();
  const current = state.local[message.layout];
  if (message.config === null) {
    if (current === undefined) return;
    const local = { ...state.local };
    delete local[message.layout];
    publishEffectiveTransition(
      state,
      { ...state, local },
      () => transcriptDisplayStore.setState({ local }),
      message.layout,
    );
    return;
  }
  const config = decodeLocalConfig(message.config);
  if (config === undefined || (current !== undefined && configFingerprint(current) === message.fingerprint)) return;
  const local = { ...state.local, [message.layout]: config };
  publishEffectiveTransition(
    state,
    { ...state, local },
    () => transcriptDisplayStore.setState({ local }),
    message.layout,
  );
}

function onChannelMessage(event: MessageEvent<unknown>): void {
  if (!isLocalMessage(event.data)) return;
  applyIncomingLocal(event.data);
}

function onStorage(event: StorageEvent): void {
  if (!isLayoutKey(event.key)) return;
  const layout = event.key.endsWith(".mobile") ? "mobile" : "desktop";
  if (event.newValue === null) {
    const state = transcriptDisplayStore.getState();
    if (state.local[layout] === undefined) return;
    const local = { ...state.local };
    delete local[layout];
    publishEffectiveTransition(state, { ...state, local }, () => transcriptDisplayStore.setState({ local }), layout);
    return;
  }
  const config = decodeLocalConfig(event.newValue);
  if (config === undefined) return;
  const current = transcriptDisplayStore.getState().local[layout];
  if (current !== undefined && configFingerprint(current) === configFingerprint(config)) return;
  const state = transcriptDisplayStore.getState();
  const local = { ...state.local, [layout]: config };
  publishEffectiveTransition(state, { ...state, local }, () => transcriptDisplayStore.setState({ local }), layout);
}

function isLayoutKey(key: string | null): key is string {
  return key === LOCAL_KEYS.desktop || key === LOCAL_KEYS.mobile;
}

function attachBrowserSync(): void {
  sourceId = makeSourceId();
  if (typeof BroadcastChannel !== "undefined") {
    try {
      channel = new BroadcastChannel(TRANSCRIPT_DISPLAY_CHANNEL);
      channel.addEventListener("message", onChannelMessage);
    } catch {
      channel = null;
    }
  }
  if (typeof window !== "undefined") window.addEventListener("storage", onStorage);
}

function detachBrowserSync(): void {
  if (channel !== null) {
    channel.removeEventListener("message", onChannelMessage);
    channel.close();
    channel = null;
  }
  if (typeof window !== "undefined") window.removeEventListener("storage", onStorage);
  stopViewportSubscription?.();
  stopViewportSubscription = null;
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
  unwireNotification?.();
  unwireReady?.();
  unwireNotification = null;
  unwireReady = null;
  wiredClient = client;
  unwireReady = client.onReady(() => {
    const epoch = beginReadyGeneration(client);
    void refreshFor(client, epoch);
  });
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
    unwireNotification?.();
    unwireReady?.();
    unwireNotification = null;
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
      reportStorageResult(localOK, legacyOK);
      broadcastLocal(layout, encoded);
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
      reportStorageResult(localOK, legacyOK);
      broadcastLocal(layout, null);
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
      patchRevisionHints.delete(layout);
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
          rememberFencedRevision(layout, result);
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
          typeof result === "object" &&
          result !== null &&
          !Array.isArray(result) &&
          Object.keys(resultRecord).length === 3 &&
          Object.hasOwn(resultRecord, "layout") &&
          Object.hasOwn(resultRecord, "revision") &&
          Object.hasOwn(resultRecord, "config");
        const requestedFingerprint = configFingerprint(config);
        const canonicalFingerprint = canonicalConfig === undefined ? undefined : configFingerprint(canonicalConfig);
        const hintedRevision = patchRevisionHints.get(layout);
        const revisionIsValid =
          typeof revision === "number" &&
          Number.isSafeInteger(revision) &&
          revision >= current.revision &&
          (revision === confirmed.revision ||
            revision === confirmed.revision + 1 ||
            (hintedRevision !== undefined && revision === hintedRevision + 1));
        const advancingRevisionValid =
          typeof revision === "number" &&
          revision > confirmed.revision &&
          (revision === confirmed.revision + 1 || (hintedRevision !== undefined && revision === hintedRevision + 1));
        const canonicalSemanticsValid =
          canonicalConfig !== undefined &&
          ((revision === confirmed.revision && canonicalFingerprint === requestedFingerprint) ||
            advancingRevisionValid);
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
        patchRevisionHints.delete(layout);
        return canonical;
      } catch (error) {
        if (patchTokens.get(layout) !== token || !isCurrentReady(client, generation)) {
          rememberFencedRevision(layout, undefined);
          return transcriptDisplayStore.getState().hub[layout] ?? confirmed;
        }
        const canonical = conflictCurrent(error, layout);
        if (canonical !== undefined) applyHubDefault(layout, canonical);
        if (error instanceof InvalidPatchResponseError) {
          const message = error.message;
          transcriptDisplayStore.setState({
            hubError: message,
            hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: message },
          });
          throw error;
        }
        const drafts = { ...transcriptDisplayStore.getState().drafts };
        delete drafts[layout];
        transcriptDisplayStore.setState({
          drafts,
          hubError: error instanceof Error ? error.message : String(error),
          hubErrors: {
            ...transcriptDisplayStore.getState().hubErrors,
            [layout]: error instanceof Error ? error.message : String(error),
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

function rememberFencedRevision(layout: ViewportClass, result: unknown): void {
  if (typeof result !== "object" || result === null || Array.isArray(result)) return;
  const candidate = result as Record<string, unknown>;
  if (
    Object.keys(candidate).length !== 3 ||
    candidate.layout !== layout ||
    typeof candidate.revision !== "number" ||
    !Number.isSafeInteger(candidate.revision) ||
    candidate.revision < 0 ||
    fromWireConfig(candidate.config) === undefined
  )
    return;
  const current = patchRevisionHints.get(layout) ?? -1;
  if (candidate.revision > current) patchRevisionHints.set(layout, candidate.revision);
}

connectionStore.subscribe(onConnectionChange);
const initialClient = connectionStore.getState().client;
if (initialClient !== null) rewireClient(initialClient);

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
    setStorageWarning("Transcript display migration could not be saved; it may not survive restart.");
  attachBrowserSync();
  stopViewportSubscription = subscribeMobileViewport(() => {
    transcriptDisplayStore.getState().setViewport(isMobileViewport() ? "mobile" : "desktop");
  });
}

export function resetTranscriptDisplayStoreForTests(): void {
  detachBrowserSync();
  initialized = false;
  invalidateReadyGeneration();
  unwireNotification?.();
  unwireReady?.();
  unwireNotification = null;
  unwireReady = null;
  activeReadyClient = null;
  activeReadyEpoch = -1;
  wiredClient = null;
  refreshSerial += 1;
  patchTokens.clear();
  patchRevisionHints.clear();
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
