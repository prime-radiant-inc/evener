// The read-side transcript display defaults store shared by the web and native
// settings surfaces. It keeps the hub's per-layout confirmed defaults behind a
// framework-free store and fences every request by the active ready generation.

import type { AppwireClient } from "./client";
import { errorText, WireError, wireRejectionPayload } from "./errors";
import { createFrameworkFreeStore, type FrameworkFreeStore } from "./frameworkFreeStore";
import { createReadyGenerationFence, type ReadyGenerationFence } from "./readyGenerationFence";
import { createSettingsHubGeneration, retireSettingsHubPayload } from "./settingsHubGeneration";
import {
  configFingerprint,
  fromWireConfig,
  fromWireDefault,
  fromWireDefaults,
  type HubTranscriptDisplayDefault,
  normalizeConfig,
  shippedDefault,
  type TranscriptDisplayConfigV1,
  toWireConfig,
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
  hubErrors: Partial<Record<ViewportClass, string>>;
  /** Confirmed defaults keep presenting across a transient disconnect. */
  hub: HubDefaultsByLayout;
  /** Optimistic direct-write previews, retained only across transient disconnects. */
  drafts: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  /** True only after the current ready generation's read has completed. */
  loaded: boolean;
  /** Reserved for the write-side settings-hub lifecycle. */
  saving: boolean;
  /** Reserved for an unsettled write outcome from the write-side lifecycle. */
  writeUncertain: boolean;
}

export interface TranscriptDisplayStoreActions {
  refreshHubDefaults(): Promise<void>;
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
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
    hubErrors: {},
    hub: {},
    drafts: {},
    loaded: false,
    saving: false,
    writeUncertain: false,
  };
}

const MALFORMED_DEFAULTS_MESSAGE = "Hub returned malformed transcript display defaults";
const UNAVAILABLE_MESSAGE = "Hub transcript display settings are unavailable.";
const MALFORMED_PATCH_MESSAGE = "Hub returned malformed transcript display PATCH response";

class InvalidPatchResponseError extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

interface HubDefaultCalculation {
  accepted: boolean;
  contradictsPreview: boolean;
}

interface HubDefaultInputs {
  readonly hub: TranscriptDisplayStoreState["hub"];
  readonly drafts: TranscriptDisplayStoreState["drafts"];
  readonly previewBases: ReadonlyMap<ViewportClass, { generation: number; revision: number; fingerprint: string }>;
  readonly fence: { readonly awaitingFirstPayload: boolean; readonly generation: number };
}

function calculateHubDefault(
  layout: ViewportClass,
  incoming: HubTranscriptDisplayDefault,
  inputs: HubDefaultInputs,
): HubDefaultCalculation {
  const previous = inputs.hub[layout];
  const previewBase = inputs.previewBases.get(layout);
  const accepted = inputs.fence.awaitingFirstPayload || previous === undefined || incoming.revision > previous.revision;
  const contradictsPreview =
    accepted &&
    previewBase !== undefined &&
    inputs.drafts[layout] !== undefined &&
    (previewBase.generation !== inputs.fence.generation || incoming.revision > previewBase.revision) &&
    configFingerprint(incoming.config) !== previewBase.fingerprint;
  return { accepted, contradictsPreview };
}

function fromWirePatchResponse(value: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!isRecord(value) || value.layout !== layout) return undefined;
  return fromWireDefault(value);
}

// The conflict and post-apply payloads (appwire/transcript_display.go's
// TranscriptDisplayConflictData and TranscriptDisplayPostApplyData) name the
// layout their canonical value belongs to; a payload naming another layout
// is not this write's answer. The discriminator is the payload's info
// string, never the code - siblings share the code (errors.ts).
function canonicalErrorPayload(
  error: unknown,
  info: string,
  field: string,
  layout: ViewportClass,
): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || !isRecord(error.data) || error.data.layout !== layout) return undefined;
  return wireRejectionPayload(error, info, field, fromWireDefault);
}

function conflictCurrent(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  return canonicalErrorPayload(error, "conflict", "current", layout);
}

function postApplyDefault(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  return canonicalErrorPayload(error, "transcriptDisplayPostApply", "applied", layout);
}

function decodePatchReply(
  result: unknown,
  layout: ViewportClass,
  confirmed: HubTranscriptDisplayDefault,
  requestedFingerprint: string,
): HubTranscriptDisplayDefault {
  const canonical = fromWirePatchResponse(result, layout);
  const isThisWrite =
    canonical !== undefined &&
    configFingerprint(canonical.config) === requestedFingerprint &&
    (canonical.revision === confirmed.revision + 1 ||
      (canonical.revision === confirmed.revision && requestedFingerprint === configFingerprint(confirmed.config)));
  if (canonical === undefined || !isThisWrite) throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
  return canonical;
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
  let successfulHubReads = 0;
  const patchTokens = new Map<ViewportClass, number>();
  const previewBases = new Map<ViewportClass, { generation: number; revision: number; fingerprint: string }>();
  const strandedPreviews = new Set<ViewportClass>();
  let reconciliationRefresh: { generation: number; promise: Promise<void> } | undefined;

  const store = createFrameworkFreeStore<TranscriptDisplayStoreState>(() => ({
    ...initialState(),
    refreshHubDefaults,
    patchHubDefault,
    applyHubChange,
  }));
  const { getState, setState } = store;

  function isSupported(): boolean {
    return getState().hubSupport === "supported";
  }

  function retirePayload(extra: Partial<TranscriptDisplayStoreFields> = {}): void {
    reconciliationRefresh = undefined;
    for (const layout of LAYOUTS) patchTokens.set(layout, fence.claimWrite());
    retireSettingsHubPayload(fence, getState, setState, extra);
  }

  function layoutError(layout: ViewportClass, message: string | undefined): Partial<TranscriptDisplayStoreFields> {
    return { hubErrors: { ...getState().hubErrors, [layout]: message } };
  }

  // A preview's base and its draft clear together, whether the caller
  // clears against live state or against the snapshot an atomic publication
  // is about to build on.
  function dropPreview(layout: ViewportClass, drafts: TranscriptDisplayStoreState["drafts"]): void {
    previewBases.delete(layout);
    delete drafts[layout];
  }

  function clearPreview(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    const drafts = { ...getState().drafts };
    dropPreview(layout, drafts);
    return { drafts };
  }

  // A settled write clears its preview and its error slots together.
  function writeSettled(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    return { ...clearPreview(layout), hubError: null, ...layoutError(layout, undefined) };
  }

  function clearPreviews(): Partial<TranscriptDisplayStoreFields> {
    previewBases.clear();
    strandedPreviews.clear();
    return { drafts: {} };
  }

  /** A confirmed default is monotonic within a generation. The first
   * authoritative payload of a new generation may restart revision numbering. */
  function applyHubDefault(
    layout: ViewportClass,
    value: HubTranscriptDisplayDefault,
    extra: Partial<TranscriptDisplayStoreFields> = {},
  ): boolean {
    const state = getState();
    const calculation = calculateHubDefault(layout, value, {
      hub: state.hub,
      drafts: state.drafts,
      previewBases,
      fence,
    });
    if (!calculation.accepted) {
      if (Object.keys(extra).length > 0) setState(extra);
      return false;
    }
    setState({
      hub: { ...state.hub, [layout]: value },
      ...(calculation.contradictsPreview ? clearPreview(layout) : {}),
      ...extra,
    });
    return true;
  }

  function applyHubDefaults(defaults: Readonly<Record<ViewportClass, HubTranscriptDisplayDefault>>): void {
    const state = getState();
    const hub = { ...state.hub };
    const drafts = { ...state.drafts };
    for (const layout of LAYOUTS) {
      const calculation = calculateHubDefault(layout, defaults[layout], {
        hub,
        drafts,
        previewBases,
        fence,
      });
      if (!calculation.accepted) continue;
      hub[layout] = defaults[layout];
      if (calculation.contradictsPreview) dropPreview(layout, drafts);
    }
    if (fence.awaitingFirstPayload) fence.firstPayloadApplied();
    // A preview the support flap stranded settled before this read: the
    // write's continuation is dead, and an unchanged revision proves the
    // write never landed, so the read ends that preview rather than
    // leaving it for the host.
    for (const layout of strandedPreviews) dropPreview(layout, drafts);
    strandedPreviews.clear();
    setState({ hub, drafts, loaded: true, hubLoading: false });
  }

  const { beginReadyGeneration: beginReadyGenerationCore, endReadyGeneration } = createSettingsHubGeneration({
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

  function beginReadyGeneration(): void {
    const replacingGeneration = fence.generation >= 0;
    beginReadyGenerationCore();
    if (replacingGeneration) retirePayload();
  }

  function detachHub(): void {
    retirePayload({ hub: {}, hubError: null, hubErrors: {}, ...clearPreviews() });
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
      retirePayload({ hubSupport: support, hubError: null, hubErrors: {}, hub: {}, ...clearPreviews() });
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
      applyHubDefaults(defaults);
      if (!stillMine()) return;
      successfulHubReads += 1;
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

  async function reconcileHubDefaults(): Promise<void> {
    const generation = fence.generation;
    if (reconciliationRefresh?.generation === generation) return reconciliationRefresh.promise;
    const refresh = refreshHubDefaults();
    const reconciliation = { generation, promise: refresh };
    reconciliationRefresh = reconciliation;
    try {
      await refresh;
    } finally {
      if (reconciliationRefresh === reconciliation) reconciliationRefresh = undefined;
    }
  }

  async function patchHubDefault(
    layout: ViewportClass,
    input: TranscriptDisplayConfigV1,
  ): Promise<HubTranscriptDisplayDefault> {
    const state = getState();
    const generation = fence.generation;
    if (!fence.liveHub(generation) || !state.loaded || state.hubLoading || state.saving || state.writeUncertain) {
      setState(layoutError(layout, UNAVAILABLE_MESSAGE));
      throw new Error(UNAVAILABLE_MESSAGE);
    }
    const config = normalizeConfig(input);
    const requestedFingerprint = configFingerprint(config);
    const confirmed = state.hub[layout] ?? shippedDefault(layout);
    const token = fence.claimWrite();
    patchTokens.set(layout, token);
    const stillMine = () => fence.liveHub(generation) && patchTokens.get(layout) === token;
    const retained = () => getState().hub[layout] ?? confirmed;
    // A support flap (generation unchanged, hub not live) can kill a
    // continuation that still owns the layout's write token without
    // retiring the payload, so the preview's write settled unseen: mark it
    // for the next authoritative read, which clears it even at an unchanged
    // revision - that read proves the write never landed. A token loss
    // strands nothing (a newer write owns the preview now), and a
    // generation change's retirement leaves the preview to the
    // stranded-preview rules.
    const strandedByFlap = () =>
      fence.isCurrent(generation) && !fence.liveHub(generation) && patchTokens.get(layout) === token;
    // A newer write on this layout owns the preview from here on, so a
    // support flap stranding an older write must not reach this one.
    strandedPreviews.delete(layout);
    previewBases.set(layout, { generation, revision: confirmed.revision, fingerprint: requestedFingerprint });
    setState({ drafts: { ...state.drafts, [layout]: config }, ...layoutError(layout, undefined) });
    if (!stillMine()) return retained();
    try {
      const result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: confirmed.revision,
        config: toWireConfig(config),
      });
      if (!stillMine()) return retained();
      const canonical = decodePatchReply(result, layout, confirmed, requestedFingerprint);
      const current = getState().hub[layout] ?? confirmed;
      if (canonical.revision < current.revision) {
        setState(writeSettled(layout));
        return current;
      }
      applyHubDefault(layout, canonical, writeSettled(layout));
      return canonical;
    } catch (error) {
      if (!stillMine()) {
        if (strandedByFlap()) strandedPreviews.add(layout);
        return retained();
      }
      const applied = postApplyDefault(error, layout);
      if (applied !== undefined) {
        applyHubDefault(layout, applied, writeSettled(layout));
        return getState().hub[layout] ?? applied;
      }
      if (error instanceof WireError && error.evenerErrorInfo === "internal" && stillMine()) {
        for (let attempt = 0; attempt < 2; attempt++) {
          const readsBefore = successfulHubReads;
          await reconcileHubDefaults();
          if (!stillMine()) {
            if (strandedByFlap()) strandedPreviews.add(layout);
            return retained();
          }
          const reconciled = getState().hub[layout];
          if (
            successfulHubReads > readsBefore &&
            reconciled !== undefined &&
            reconciled.revision > confirmed.revision &&
            configFingerprint(reconciled.config) === requestedFingerprint
          ) {
            setState(writeSettled(layout));
            return reconciled;
          }
        }
      }
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) {
        applyHubDefault(layout, canonical);
        if (!stillMine()) return getState().hub[layout] ?? canonical;
      }
      const message = errorText(error);
      setState({
        ...clearPreview(layout),
        hubError: message,
        ...layoutError(layout, message),
      });
      throw error;
    }
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
      previewBases.clear();
      setState(initialState());
    },
    dispose() {
      if (fence.disposed) return;
      endReadyGeneration();
      fence.dispose();
    },
  };
}
