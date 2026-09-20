import type { AppwireClientLike } from "@evener/appwire-client";
import {
  createTranscriptDisplayStore,
  decodeLocalConfig,
  encodeLocalConfig,
  type HubTranscriptDisplayDefault,
  normalizeConfig,
  type TranscriptDisplayStoreState as PackageStoreState,
  resolveEffectiveConfig,
  type TranscriptDisplayConfigV1,
  type TranscriptDisplayStore,
  transcriptDisplaySupport,
  type ViewportClass,
  WireError,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
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
import { publishEffectiveTransition } from "./transcriptDisplay/transitions";

export const TRANSCRIPT_DISPLAY_CHANNEL = "evener.transcript-display.v1";
// This store's own guard: keybindings.ts wires the same connectionStore
// client through its own instance, so the two never contend over one shared
// registration slot.
const readyGenerationCallback = createReadyGenerationCallback();
export const TRANSCRIPT_DISPLAY_CHANNEL_NAME = TRANSCRIPT_DISPLAY_CHANNEL;
type ConfigByLayout = Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
type HubByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;
type PackageClient = Pick<AppwireClientLike, "request" | "onNotification">;

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
let unwireReady: (() => void) | null = null;
// The package's transcript display store riding the wired client. One per
// client identity: a client swap disposes it and builds a fresh one, so the
// replaced client's notifications and late reads/writes can never land in the
// new client's state. It owns the whole hub read/write lifecycle - the ready
// generation, the GET/PATCH wire protocol, revision fencing, conflict and
// post-apply decoding, and payload retirement - which this adapter mirrors
// into the web's one public Zustand store below.
let packageStore: TranscriptDisplayStore | null = null;
let unsubscribeMirror: (() => void) | null = null;
// Previews the adapter restored after a malformed PATCH reply: the package
// clears its own preview when the write fails, while the web contract keeps
// the draft. They are adapter-owned, so they are merged over every package
// drafts publication (a write on another layout must not erase them) and
// dropped when a newer package write on their own layout supersedes them or
// the store is detached or replaced.
let restoredPreviews: ConfigByLayout = {};

// The web's PATCH reply contract is stricter than the package's decoder: the
// web-owned decoder it replaces treated a reply with any unexpected key as
// malformed, and its unchanged suite pins that (a hub adding fields to the
// reply must not commit). The package's decoder deliberately tolerates extra
// keys so future hub fields keep decoding, so the adapter enforces the web's
// exact-shape rule at the client seam it hands the package - rejecting the
// reply before the package can accept it. The thrown message is the same one
// the old decoder used, so the package's own generic-error handling and the
// adapter's preview restore below treat it exactly like the package's own
// malformed-decode rejections.
const MALFORMED_PATCH_MESSAGE = "Hub returned malformed transcript display PATCH response";

function isExactPatchReply(value: unknown): boolean {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return false;
  const record = value as Record<string, unknown>;
  return (
    Object.keys(record).length === 3 &&
    Object.hasOwn(record, "layout") &&
    Object.hasOwn(record, "revision") &&
    Object.hasOwn(record, "config")
  );
}

function strictPatchReplyClient(client: AppwireClientLike): PackageClient {
  const request: AppwireClientLike["request"] = async (method, params, opts) => {
    const result = await client.request(method, params, opts);
    if (method === "evener/settings/transcriptDisplay/patch" && !isExactPatchReply(result)) {
      throw new Error(MALFORMED_PATCH_MESSAGE);
    }
    return result;
  };
  return {
    request,
    onNotification: (callback) => client.onNotification(callback),
  };
}

function isLayout(value: unknown): value is ViewportClass {
  return value === "desktop" || value === "mobile";
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

function currentSupport(): "unknown" | "supported" | "unsupported" {
  return transcriptDisplaySupport(connectionStore.getState().features);
}

// Mirrors one package state publication into the web store. The framework-
// free store publishes its full state on every write, but object fields keep
// their identity across publications that do not touch them, so a reference
// comparison identifies exactly the fields this publication changed.
function mirrorPackageState(next: PackageStoreState, previous: PackageStoreState): void {
  if (next.hub !== previous.hub) {
    for (const layout of ["desktop", "mobile"] as const) {
      if (next.hub[layout] !== previous.hub[layout]) applyMirroredHubDefault(layout, next.hub[layout]);
    }
  }
  if (next.hubSupport !== previous.hubSupport) transcriptDisplayStore.setState({ hubSupport: next.hubSupport });
  if (next.hubLoading !== previous.hubLoading) transcriptDisplayStore.setState({ hubLoading: next.hubLoading });
  if (next.hubError !== previous.hubError) transcriptDisplayStore.setState({ hubError: next.hubError });
  if (next.hubErrors !== previous.hubErrors) transcriptDisplayStore.setState({ hubErrors: next.hubErrors });
  if (next.drafts !== previous.drafts) {
    // A newer package write on a layout owns that layout's preview again,
    // superseding any malformed-reply preview the adapter restored for it.
    for (const layout of ["desktop", "mobile"] as const) {
      if (next.drafts[layout] !== undefined) delete restoredPreviews[layout];
    }
    transcriptDisplayStore.setState({ drafts: { ...next.drafts, ...restoredPreviews } });
  }
}

// A hub default the package just accepted, landing in the web store through
// the same transition routing the web's own writes use (capture/restore/
// announce, masked per layout). The package already decided acceptance - its
// generation fencing, within-generation monotonicity and first-payload
// restarts - so the mirror applies what it accepted, one layout at a time to
// preserve the web's per-layout transition publications.
function applyMirroredHubDefault(layout: ViewportClass, value: HubTranscriptDisplayDefault | undefined): void {
  const state = transcriptDisplayStore.getState();
  if (state.hub[layout] === value) return;
  const hub: HubByLayout = { ...state.hub };
  if (value === undefined) delete hub[layout];
  else hub[layout] = value;
  publishEffectiveTransition(state, { ...state, hub }, () => transcriptDisplayStore.setState({ hub }), layout);
}

// The web-side fallback for a hub change the package could not take: with no
// wired client (or no live ready generation) there is no package hub to fence
// with, and the web's own contract - pinned by its unchanged suite - still
// applies any well-formed change whose revision beats the one it holds.
function applyWebHubDefault(layout: ViewportClass, value: HubTranscriptDisplayDefault): void {
  const state = transcriptDisplayStore.getState();
  const previous = state.hub[layout];
  if (previous !== undefined && value.revision <= previous.revision) return;
  const hub: HubByLayout = { ...state.hub, [layout]: value };
  publishEffectiveTransition(state, { ...state, hub }, () => transcriptDisplayStore.setState({ hub }), layout);
}

function applyWebHubChange(change: TranscriptDisplayChange): void {
  if (!isLayout(change.layout) || !isRevision(change.revision)) return;
  try {
    const config = normalizeConfig(change.config);
    applyWebHubDefault(change.layout, { revision: change.revision, config });
  } catch {
    // A malformed notification cannot be a confirmed hub record.
  }
}

function setSupportFromConnection(): void {
  const support = currentSupport();
  const store = packageStore;
  if (store !== null) {
    store.setSupport(support);
    return;
  }
  // No wired client means no package store: the web store keeps the support
  // field itself, exactly as it did before the package lifecycle existed.
  const state = transcriptDisplayStore.getState();
  if (state.hubSupport !== support || state.hubLoading || state.hubError !== null)
    transcriptDisplayStore.setState({ hubSupport: support, hubLoading: false, hubError: null });
}

// Ends the current package store without touching the web's mirrored fields;
// used when a replacement client is about to build a fresh one.
function disposePackageStore(): void {
  const store = packageStore;
  packageStore = null;
  unsubscribeMirror?.();
  unsubscribeMirror = null;
  store?.dispose();
}

// Anchors the change-only mirror to a freshly wired store's current state.
// The new store starts from its initial values and the mirror only publishes
// transitions, so without this sync whatever the replaced client last
// published would survive in the web store indefinitely - most visibly a
// stale hubError from its failed read, which would keep the settings UI
// disabled while the replacement client loads successfully. Support is
// deliberately not synced: setSupportFromConnection publishes the
// connection's real support immediately after the rewire.
function syncMirrorToStore(store: TranscriptDisplayStore): void {
  restoredPreviews = {};
  const next = store.getState();
  for (const layout of ["desktop", "mobile"] as const) applyMirroredHubDefault(layout, next.hub[layout]);
  const web = transcriptDisplayStore.getState();
  const changed: Partial<TranscriptDisplayStoreState> = {};
  if (web.hubLoading !== next.hubLoading) changed.hubLoading = next.hubLoading;
  if (web.hubError !== next.hubError) changed.hubError = next.hubError;
  if (web.hubErrors !== next.hubErrors) changed.hubErrors = next.hubErrors;
  if (web.drafts !== next.drafts) changed.drafts = { ...next.drafts };
  if (Object.keys(changed).length > 0) transcriptDisplayStore.setState(changed);
}

// The ready callback (and the already-ready path at wire time): the package
// generation owns notification registration and payload retirement. The
// initial refresh is issued BEFORE setSupport because the two never double a
// GET this way round - refreshFor is a no-op while the package's own support
// field is not yet "supported", while setSupport auto-refreshes only when a
// live generation already exists, so whichever of the two sees support first
// performs the generation's one read.
function beginPackageGeneration(client: AppwireClientLike): void {
  const store = packageStore;
  if (store === null || client !== wiredClient) return;
  store.beginReadyGeneration();
  void store.getState().refreshHubDefaults();
  setSupportFromConnection();
}

// A null client detaches the package hub entirely: detachHub publishes the
// retirement while the mirror is still subscribed (so the cleared fields land
// through the normal path), then the store is disposed so its notifications
// and in-flight work can never land, and the web mirrors initial hub fields.
function detachPackageStore(): void {
  const store = packageStore;
  packageStore = null;
  restoredPreviews = {};
  unwireReady?.();
  unwireReady = null;
  wiredClient = null;
  store?.detachHub();
  unsubscribeMirror?.();
  unsubscribeMirror = null;
  store?.dispose();
  transcriptDisplayStore.setState({ hub: {}, drafts: {}, hubLoading: false, hubError: null, hubErrors: {} });
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  // A new client identity gets a fresh package store: the old one's fence is
  // disposed, fencing its notifications and every read and write in flight.
  disposePackageStore();
  unwireReady?.();
  unwireReady = null;
  wiredClient = client;
  packageStore = createTranscriptDisplayStore({ client: strictPatchReplyClient(client) });
  unsubscribeMirror = packageStore.subscribe(mirrorPackageState);
  syncMirrorToStore(packageStore);
  unwireReady = client.onReady(
    readyGenerationCallback(
      client,
      () => wiredClient,
      () => beginPackageGeneration(client),
    ),
  );
  if (client.state === "ready") beginPackageGeneration(client);
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
    // Ready loss ends the generation. Confirmed defaults stay presented and
    // late reads/writes fence, exactly as the package's retirement specifies.
    packageStore?.endReadyGeneration();
  }
  if (state.client === null && wiredClient !== null) {
    detachPackageStore();
  }
  setSupportFromConnection();
}

function restoreMalformedPreview(
  store: TranscriptDisplayStore,
  layout: ViewportClass,
  preview: TranscriptDisplayConfigV1 | undefined,
  error: unknown,
): void {
  if (preview === undefined) return;
  if (error instanceof WireError || !(error instanceof Error)) return;
  if (error.message !== MALFORMED_PATCH_MESSAGE) return;
  // A newer write owns the layout's preview now; restoring over it would
  // resurrect a write the package already superseded.
  if (store.getState().drafts[layout] !== undefined) return;
  // The restored preview is adapter-owned: the package cleared it with this
  // write's failure, so it survives later package drafts publications on other
  // layouts (merged in the mirror) until a newer write on its own layout
  // supersedes it or the store is detached or replaced.
  restoredPreviews = { ...restoredPreviews, [layout]: preview };
  transcriptDisplayStore.setState({ drafts: { ...store.getState().drafts, ...restoredPreviews } });
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
      if (!isLayout(change.layout) || !isRevision(change.revision)) return;
      const store = packageStore;
      if (store !== null) {
        const before = store.getState().hub[change.layout];
        store.getState().applyHubChange(change);
        // The package took the change exactly when its value for the layout
        // changed; the mirror already landed it, transition included.
        if (store.getState().hub[change.layout] !== before) return;
      }
      // No live package hub (no client, or the package fenced the change):
      // the web contract still applies well-formed monotonic changes.
      applyWebHubChange(change);
    },
    refreshHubDefaults: async () => {
      // Harmless when no client is wired, matching the no-client behavior.
      const store = packageStore;
      if (store === null) return;
      await store.getState().refreshHubDefaults();
    },
    patchHubDefault: async (layout, input): Promise<HubTranscriptDisplayDefault> => {
      const store = packageStore;
      if (store === null) {
        const error = "Hub transcript display settings are unavailable.";
        transcriptDisplayStore.setState({
          hubErrors: { ...transcriptDisplayStore.getState().hubErrors, [layout]: error },
        });
        throw new Error(error);
      }
      const write = store.getState().patchHubDefault(layout, input);
      // The package published this write's optimistic preview synchronously
      // above; capture it before awaiting so a malformed reply can restore it.
      const preview = store.getState().drafts[layout];
      try {
        return await write;
      } catch (error) {
        restoreMalformedPreview(store, layout, preview, error);
        throw error;
      }
    },
  }),
);

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
  detachPackageStore();
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
