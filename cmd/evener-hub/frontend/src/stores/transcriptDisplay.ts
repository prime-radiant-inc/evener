// transcriptDisplay.ts is the web's transcript display store: the package's
// transcript display hub-defaults store (createTranscriptDisplayStore) for the
// hub layer, bound to whichever client connectionStore holds, plus the web's
// own local layer - the per-browser localStorage override per viewport class,
// the viewport class itself, the BroadcastChannel/storage-event sync between
// tabs, the legacy dual-write - and the effective-configuration transition that
// announces a change to the mounted transcript views. The hub half's posture
// (feature gating, ready-generation fencing, the direct write with its preview,
// the canonical conflict adoption) is the package's; this file owns the
// CONNECTION lifecycle and the local layer, and mirrors the core's hub fields
// into the one zustand store the panes read.

import type { AnyNotification, AppwireClientLike, TranscriptDisplayClient } from "@evener/appwire-client";
import {
  accessibleConfigSummary,
  configFingerprint,
  createTranscriptDisplayStore,
  decodeLocalConfig,
  encodeLocalConfig,
  type TranscriptDisplayStoreState as HubStoreState,
  type HubTranscriptDisplayDefault,
  legacyWritesFromConfig,
  normalizeConfig,
  resolveEffectiveConfig,
  type TranscriptDisplayConfigV1,
  transcriptDisplaySupport,
  type ViewportClass,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { transitionTranscriptViews } from "../panes/session/transcript/flow/transcriptViewRegistry";
import { isMobileViewport, subscribeMobileViewport } from "../shell/useIsMobile";
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

/** The hub fields the panes read, mirrored from the package store on every
 * transition it publishes. */
type MirroredHubFields = Pick<HubStoreState, "hub" | "drafts" | "hubLoading" | "hubError" | "hubErrors" | "hubSupport">;

export interface TranscriptDisplayStoreState extends MirroredHubFields {
  viewport: ViewportClass;
  local: ConfigByLayout;
  storageWarning: string | null;
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

function mirroredHubFields(state: HubStoreState): MirroredHubFields {
  return {
    hub: state.hub,
    drafts: state.drafts,
    hubLoading: state.hubLoading,
    hubError: state.hubError,
    hubErrors: state.hubErrors,
    hubSupport: state.hubSupport,
  };
}

function initialState(): Omit<
  TranscriptDisplayStoreState,
  "setViewport" | "setLocal" | "clearLocal" | "effective" | "applyHubChange" | "refreshHubDefaults" | "patchHubDefault"
> {
  return {
    viewport: "desktop",
    local: {},
    storageWarning: null,
    ...mirroredHubFields(hubStore.getState()),
  };
}

let initialized = false;
let channel: BroadcastChannel | null = null;
let sourceId = "";
let stopViewportSubscription: (() => void) | null = null;
let wiredClient: AppwireClientLike | null = null;
let unwireReady: (() => void) | null = null;

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("transcript display store: no client connected");
  return client;
}

// The client port resolves connectionStore's CURRENT client on every call.
// The store subscribes to notifications once per ready generation and this
// file begins a generation only once a client is wired, so each subscription
// lands on the client that generation belongs to; onConnectionChange publishes
// support BEFORE rewiring, so the refresh a rewire kicks reads the connection's
// current feature set.
const connectedClient: TranscriptDisplayClient = {
  request: async (method, params, opts) => requireClient().request(method, params, opts),
  onNotification: (cb: (n: AnyNotification) => void) => requireClient().onNotification(cb),
};

const hubStore = createTranscriptDisplayStore({ client: connectedClient });

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
    applyHubChange: (change) => hubStore.getState().applyHubChange(change),
    refreshHubDefaults: () => hubStore.getState().refreshHubDefaults(),
    patchHubDefault: (layout, config) => hubStore.getState().patchHubDefault(layout, config),
  }),
);

// Every hub transition the core publishes lands here. A hub value that moved
// runs through the effective-configuration transition (the panes announce and
// remount for the layer that changed); everything else mirrors in place. The
// core applies one layout per transition, so `changed` names at most one.
hubStore.subscribe((next, previous) => {
  const state = transcriptDisplayStore.getState();
  const mirrored = mirroredHubFields(next);
  const changed = (["desktop", "mobile"] as const).find((layout) => next.hub[layout] !== previous.hub[layout]);
  if (changed === undefined) {
    transcriptDisplayStore.setState(mirrored);
    return;
  }
  publishEffectiveTransition(
    state,
    { ...state, hub: next.hub },
    () => transcriptDisplayStore.setState(mirrored),
    changed,
  );
});

function beginReadyGeneration(): void {
  hubStore.beginReadyGeneration();
  void hubStore.getState().refreshHubDefaults();
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  hubStore.endReadyGeneration();
  unwireReady?.();
  wiredClient = client;
  // The confirmed defaults belong to the PREVIOUS hub: they stop presenting
  // before this client's first refresh can land.
  hubStore.detachHub();
  unwireReady = client.onReady(beginReadyGeneration);
  if (client.state === "ready") beginReadyGeneration();
}

function onConnectionChange(
  state: ReturnType<typeof connectionStore.getState>,
  previous: ReturnType<typeof connectionStore.getState>,
): void {
  // Support FIRST (see connectedClient): the store owns what a support
  // transition means, and any transition into supported with a generation
  // active refreshes under it.
  hubStore.setSupport(transcriptDisplaySupport(state.features));
  if (state.client !== wiredClient && state.client !== null) rewireClient(state.client);
  if (
    state.client === wiredClient &&
    previous.client === state.client &&
    previous.state === "ready" &&
    state.state !== "ready"
  ) {
    hubStore.endReadyGeneration();
  }
  if (state.client === null && wiredClient !== null) {
    hubStore.endReadyGeneration();
    unwireReady?.();
    unwireReady = null;
    wiredClient = null;
  }
}

connectionStore.subscribe(onConnectionChange);
// A module evaluating AFTER the handshake finds a connected client and its
// feature set already in the store: seed both, in onConnectionChange's order.
const initial = connectionStore.getState();
hubStore.setSupport(transcriptDisplaySupport(initial.features));
if (initial.client !== null) rewireClient(initial.client);

export function initTranscriptDisplay(): void {
  if (initialized) return;
  initialized = true;
  transcriptDisplayStore.setState({
    viewport: isMobileViewport() ? "mobile" : "desktop",
    local: {},
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
  hubStore.reset();
  unwireReady?.();
  unwireReady = null;
  wiredClient = null;
  transcriptDisplayStore.setState({ ...initialState() });
  hubStore.setSupport(transcriptDisplaySupport(connectionStore.getState().features));
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

export type { HubByLayout };
