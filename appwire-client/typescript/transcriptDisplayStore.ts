// The transcript display hub-defaults store both apps' display settings run
// on: one hub's per-layout transcript display defaults
// (evener/settings/transcriptDisplay/{get,patch} and the `changed`
// broadcast), with a direct write for a host that edits live.
// createTranscriptDisplayStore is a factory - each app builds the one
// instance it wires to its connection and its view layer, tests build their
// own - returning the framework-free triple plus the connection-lifecycle
// methods the host drives. Pure logic - no DOM, no React: the client is a
// port.
//
// This piece is the hub-defaults + direct-write half only (D6 piece 10): the
// checkpointed offline draft editor (D6 piece 12) is a later piece that adds
// its own fields (draft, draftConflict, draftError, draftUnreadable,
// storageUnavailable), its own actions (editDraft, saveDraft, discardDraft,
// rebaseDraft) and a drafts port dependency to this same file. `saving` and
// `writeUncertain` are declared now because the shared generation/retirement
// primitives below require them and patchHubDefault's gate will need them
// too - piece 12 is what ever sets either true.
//
// setSupport and applyHubDefault are store-owned: fence/notification wiring,
// payload retirement and a lost-hub write's settle are adopted directly from
// settingsHubGeneration.ts (D6 piece 8) with no re-derivation.

import type { AppwireClient } from "./client";
import { errorText, WireError } from "./errors";
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

/** The two members of the client this store calls; AppwireClientLike satisfies it. */
export type TranscriptDisplayClient = Pick<AppwireClient, "request" | "onNotification">;

export type TranscriptDisplaySupport = "unknown" | "supported" | "unsupported";

/** Whether the connected hub advertises transcript display settings: unknown
 * until the handshake's feature set is in hand. */
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

/** The hub's confirmed default per layout; a layout the hub has not answered
 * for yet is absent. */
export type HubDefaultsByLayout = Partial<Record<ViewportClass, HubTranscriptDisplayDefault>>;

/** One `changed` broadcast, as the host may also feed it in by hand. */
export interface TranscriptDisplayChange {
  layout: ViewportClass;
  revision: number;
  config: TranscriptDisplayConfigV1;
}

export interface TranscriptDisplayStoreFields {
  hubSupport: TranscriptDisplaySupport;
  hubLoading: boolean;
  /** The last hub-sourced failure (a failed read, a malformed reply); the
   * per-layout copy of a write's failure rides `hubErrors`. */
  hubError: string | null;
  /** A write's failure on the layout it wrote; cleared by the layout's next
   * write leaving. */
  hubErrors: Partial<Record<ViewportClass, string>>;
  /** The hub's confirmed defaults. They keep presenting across a transient
   * disconnect (the effective configuration must not flap); `loaded` says
   * whether they are confirmed for the CURRENT ready generation. */
  hub: HubDefaultsByLayout;
  /** True only while `hub` was confirmed by the hub for the current ready
   * generation. Set on every applied payload; cleared when the generation
   * ends, support drops, the hub is replaced or the store resets. Writes gate
   * on it: a PATCH composed against a previous hub's revision would overwrite
   * the new hub's default on a revision collision. */
  loaded: boolean;
  /** The direct write's preview per layout while its request is out. */
  drafts: Partial<Record<ViewportClass, TranscriptDisplayConfigV1>>;
  /** A checkpointed write is in flight - always false until piece 12's
   * checkpointed editor exists. */
  saving: boolean;
  /** A checkpointed write left without a confirmed outcome - always false
   * until piece 12's checkpointed editor exists. */
  writeUncertain: boolean;
}

export interface TranscriptDisplayStoreActions {
  /** Refreshes both layouts from the hub under the current ready generation;
   * a no-op without one or while unsupported. */
  refreshHubDefaults(): Promise<void>;
  /** The live editor's write: previews `config` on `layout` while the PATCH
   * is out, applies the canonical response, clears the preview. A lost
   * revision race adopts the canonical current the hub reports and rejects;
   * a later write on the same layout supersedes an earlier one's reply. */
  patchHubDefault(layout: ViewportClass, config: TranscriptDisplayConfigV1): Promise<HubTranscriptDisplayDefault>;
  /** Applies one hub change by hand (a host relaying a broadcast it received
   * on its own channel): a malformed or stale change is ignored. */
  applyHubChange(change: TranscriptDisplayChange): void;
}

export type TranscriptDisplayStoreState = TranscriptDisplayStoreFields & TranscriptDisplayStoreActions;

export interface TranscriptDisplayStore extends FrameworkFreeStore<TranscriptDisplayStoreState> {
  /** Publishes the connection's support for transcript display settings.
   * Resolving to unsupported retires the payload; flapping back to supported
   * while a ready generation is active begins a new one so no pre-flap
   * in-flight work lands, and loads. */
  setSupport(support: TranscriptDisplaySupport): void;
  /** The client is ready: subscribe to its notifications and fence every
   * later refresh and write to this generation. The host refreshes next. */
  beginReadyGeneration(): void;
  /** The ready generation ended (disconnect, client replacement): drop the
   * subscription, fence in-flight work out, and mark the confirmed defaults
   * as no longer current. They keep presenting - a transient disconnect must
   * not flap the effective configuration. */
  endReadyGeneration(): void;
  /** The client was replaced by one for a possibly different hub: the
   * confirmed defaults are the previous hub's, so they stop presenting and
   * the payload state resets. */
  detachHub(): void;
  /** Ends the generation and returns the state to its initial values; the
   * host re-publishes support afterwards. */
  reset(): void;
  /** Ends the generation for good: every later refresh and write is refused
   * and nothing in flight lands. */
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
    loaded: false,
    drafts: {},
    saving: false,
    writeUncertain: false,
  };
}

const UNAVAILABLE_MESSAGE = "Hub transcript display settings are unavailable.";
const MALFORMED_DEFAULTS_MESSAGE = "Hub returned malformed transcript display defaults";
const MALFORMED_PATCH_MESSAGE = "Hub returned malformed transcript display PATCH response";
/** The direct write's reply landed after the generation ended, support
 * dropped or the hub was replaced: whatever the reply says - a success, a
 * conflict's canonical, a transport failure - is not this write's outcome
 * with the CURRENT hub, so resolving with a stale current value would report
 * an acknowledgement the current hub never gave. */
const WRITE_NOT_ACKNOWLEDGED_MESSAGE = "The hub connection changed before this write's outcome was confirmed.";

/** A distinguishable class for the response-shape rejection: the direct write
 * reports it on both error fields, where a transport failure reports on the
 * layout alone. */
export class InvalidPatchResponseError extends Error {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isRevision(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= 0;
}

/** Structural check for one `changed` broadcast: the trust-boundary re-check
 * so a malformed payload degrades to a re-read instead of a crash. Every
 * field must be present and well-formed; an empty or partial record is not a
 * change. */
export function fromWireChange(value: unknown): TranscriptDisplayChange | undefined {
  if (!isRecord(value) || !isViewportClass(value.layout) || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { layout: value.layout, revision: value.revision, config };
}

/** The PATCH response shape: {layout, revision, config} for the layout
 * written, or undefined. Extra fields a future hub adds are tolerated, not
 * rejected - the same forward-compatible posture fromWireChange and
 * fromWireDefaults already take. Semantic checks against the request belong
 * to the caller. */
function fromWirePatchResponse(value: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!isRecord(value) || value.layout !== layout || !isRevision(value.revision)) return undefined;
  const config = fromWireConfig(value.config);
  return config === undefined ? undefined : { revision: value.revision, config };
}

/** The canonical current the hub reports with a lost revision race, or
 * undefined for any other rejection. */
function conflictCurrent(error: unknown, layout: ViewportClass): HubTranscriptDisplayDefault | undefined {
  if (!(error instanceof WireError) || error.code !== -32013 || !isRecord(error.data)) return undefined;
  if (error.data.evenerErrorInfo !== "conflict" || error.data.layout !== layout) return undefined;
  return fromWireDefault(error.data.current);
}

/** A PATCH reply decoded as THIS write's own outcome: the requested
 * configuration at the confirmed revision (a no-op write) or one past it,
 * never a jump the request cannot account for. */
function decodePatchReply(
  result: unknown,
  layout: ViewportClass,
  confirmed: HubTranscriptDisplayDefault,
  requested: TranscriptDisplayConfigV1,
): HubTranscriptDisplayDefault {
  const canonical = fromWirePatchResponse(result, layout);
  const requestedFingerprint = configFingerprint(requested);
  const isThisWrite =
    canonical !== undefined &&
    configFingerprint(canonical.config) === requestedFingerprint &&
    (canonical.revision === confirmed.revision + 1 ||
      // A no-op write: the hub kept the revision because the requested
      // configuration is the confirmed one.
      (canonical.revision === confirmed.revision && requestedFingerprint === configFingerprint(confirmed.config)));
  if (canonical === undefined || !isThisWrite) throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
  return canonical;
}

/** Builds a store over `client`. */
export function createTranscriptDisplayStore(deps: TranscriptDisplayStoreDeps): TranscriptDisplayStore {
  const { client } = deps;

  // Ready-generation wiring: every refresh and write captures the generation
  // it started under and fences its reply on the shared fence.
  const fence: ReadyGenerationFence = createReadyGenerationFence(isSupported);
  let missedChangeNotification = false;
  /** The direct write's token per layout: a later write on the same layout
   * supersedes an earlier one's reply. Reset for every layout by retirement. */
  const patchTokens = new Map<ViewportClass, number>();
  /** What each direct write's preview was composed against: the hub generation
   * and the confirmed revision - see applyHubDefault's contradictsPreview. */
  const previewBases = new Map<ViewportClass, { generation: number; revision: number }>();

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

  /** Takes the layout for this write. A layer carries ONE hub value, so the
   * later write on it owns the outcome and every earlier reply for that
   * layer is superseded. */
  function claimLayoutWrite(layout: ViewportClass): number {
    const token = fence.claimWrite();
    patchTokens.set(layout, token);
    return token;
  }

  /** A write's reply is its own to land: live hub, and no later write on its
   * layout or payload retirement has superseded it. */
  function writeStillMine(generation: number, layout: ViewportClass, token: number): boolean {
    return fence.liveHub(generation) && patchTokens.get(layout) === token;
  }

  /** The confirmed payload can no longer be acted on - see
   * retireSettingsHubPayload for the in-flight flags this resets. Every
   * layout's own write token is superseded too, by claiming a fresh one for
   * each: `patchTokens` is this store's own bookkeeping, unknown to the
   * shared fence, so nothing else invalidates it. */
  function retirePayload(extra: Partial<TranscriptDisplayStoreFields> = {}): void {
    for (const layout of LAYOUTS) patchTokens.set(layout, fence.claimWrite());
    retireSettingsHubPayload(fence, getState, setState, extra);
  }

  function layoutError(layout: ViewportClass, message: string | undefined): Partial<TranscriptDisplayStoreFields> {
    return { hubErrors: { ...getState().hubErrors, [layout]: message } };
  }

  function clearPreview(layout: ViewportClass): Partial<TranscriptDisplayStoreFields> {
    previewBases.delete(layout);
    const drafts = { ...getState().drafts };
    delete drafts[layout];
    return { drafts };
  }

  function clearPreviews(): Partial<TranscriptDisplayStoreFields> {
    previewBases.clear();
    return { drafts: {} };
  }

  /** Applies one layout's confirmed default (a GET's layer, a `changed`
   * broadcast, a PATCH response, a conflict's canonical current). While the
   * current generation has confirmed state, a revision at or below the held
   * one is stale and ignored; a new generation's first payload is
   * authoritative at ANY revision (fence.awaitingFirstPayload), because
   * revision numbering is the hub's and a reconnect can be a hub restart.
   * `extra` lands in the same publish. Returns false for an ignored payload. */
  function applyHubDefault(
    layout: ViewportClass,
    value: HubTranscriptDisplayDefault,
    extra: Partial<TranscriptDisplayStoreFields> = {},
  ): boolean {
    const state = getState();
    const previous = state.hub[layout];
    if (!fence.awaitingFirstPayload && previous !== undefined && value.revision <= previous.revision) {
      if (Object.keys(extra).length > 0) setState(extra);
      return false;
    }
    const hub = { ...state.hub, [layout]: value };
    const previewBase = previewBases.get(layout);
    const preview = state.drafts[layout];
    const contradictsPreview =
      previewBase !== undefined &&
      preview !== undefined &&
      configFingerprint(value.config) !== configFingerprint(preview) &&
      (previewBase.generation !== fence.generation || value.revision > previewBase.revision);
    setState({
      hub,
      ...(contradictsPreview ? clearPreview(layout) : {}),
      ...extra,
    });
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
    if (state.hubSupport !== support || state.hubLoading || state.hubError !== null)
      setState({ hubSupport: support, hubLoading: false, hubError: null });
  }

  /** A change arriving before the CURRENT generation's refresh has confirmed
   * state carries pre-refresh cargo: dropped, and the generation is marked
   * dirty so the refresh that confirms it fires ONE follow-up fetch. */
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

  /** A change the HOST relayed rather than the store's own subscription. Like
   * a notification it is not this generation's confirmation: it merges into
   * `hub` under the ordinary stale guard and leaves the authoritative read to
   * confirm. */
  function applyHubChange(change: TranscriptDisplayChange): void {
    if (!isViewportClass(change.layout) || !isRevision(change.revision)) return;
    if (getState().hubSupport === "unsupported") return;
    if (changeArrivedBeforeConfirmation()) return;
    let config: TranscriptDisplayConfigV1;
    try {
      config = normalizeConfig(change.config);
    } catch {
      return;
    }
    externalPayloadLanded(applyHubDefault(change.layout, { revision: change.revision, config }));
  }

  function confirmedFor(hub: HubDefaultsByLayout, layout: ViewportClass): HubTranscriptDisplayDefault {
    return hub[layout] ?? shippedDefault(layout);
  }

  async function refreshFor(generation: number): Promise<void> {
    if (!fence.liveHub(generation)) return;
    const serial = fence.claimRead();
    const stillMine = () => fence.readStillMine(generation, serial);
    setState({ hubLoading: true, hubError: null });
    try {
      const result = await client.request("evener/settings/transcriptDisplay/get", {});
      if (!stillMine()) return;
      const defaults = fromWireDefaults(result);
      if (defaults === undefined) throw new Error(MALFORMED_DEFAULTS_MESSAGE);
      // One transition per layout, so a host announcing effective changes
      // sees each layer move on its own. Both layers are one payload from
      // one hub, so `loaded` flips on the desktop apply and mobile's own
      // apply is not fenced by the previous hub's revision.
      applyHubDefault("desktop", defaults.desktop);
      applyHubDefault("mobile", defaults.mobile, { loaded: true, hubLoading: false });
      fence.firstPayloadApplied();
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
    const confirmed = confirmedFor(state.hub, layout);
    const token = claimLayoutWrite(layout);
    const stillMine = () => writeStillMine(generation, layout, token);
    previewBases.set(layout, { generation, revision: confirmed.revision });
    setState({ drafts: { ...state.drafts, [layout]: config }, ...layoutError(layout, undefined) });
    try {
      const result = await client.request("evener/settings/transcriptDisplay/patch", {
        layout,
        expectedRevision: confirmed.revision,
        config: toWireConfig(config),
      });
      if (!stillMine()) throw new Error(WRITE_NOT_ACKNOWLEDGED_MESSAGE);
      const canonical = decodePatchReply(result, layout, confirmed, config);
      if (canonical.revision < (getState().hub[layout] ?? confirmed).revision) {
        setState(clearPreview(layout));
        throw new InvalidPatchResponseError(MALFORMED_PATCH_MESSAGE);
      }
      applyHubDefault(layout, canonical, {
        ...clearPreview(layout),
        hubError: null,
        ...layoutError(layout, undefined),
      });
      return canonical;
    } catch (error) {
      if (!stillMine()) throw new Error(WRITE_NOT_ACKNOWLEDGED_MESSAGE);
      const canonical = conflictCurrent(error, layout);
      if (canonical !== undefined) applyHubDefault(layout, canonical);
      const message = errorText(error);
      // A malformed reply is exactly as unknown an outcome as a transport
      // failure (the request may or may not have applied on the hub): the
      // optimistic preview clears the same way either way. clearPreview is
      // idempotent, so this is safe even for the one InvalidPatchResponseError
      // (the revision-regression check above) that already cleared it.
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
