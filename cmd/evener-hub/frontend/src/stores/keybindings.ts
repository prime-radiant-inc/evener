// The keybindings overrides store: mirrors stores/transcriptDisplay.ts's hub
// posture (feature gating, hubSupport, ready-generation wiring, conflict
// refresh) for user keybinding overrides. The hub layer is the ONLY layer -
// unlike transcript display there is no localStorage layer: an unsupported
// or unreachable hub means defaults only, never a local fallback copy.
//
// Applied overrides live in the keybindings registry (src/keybindings/), not
// in this store's state: startup `get` and every `changed` notification are
// validated semantically (keybindings/validation.ts) and reconciled into the
// registry as a DELTA - only actions whose effective chord changed are
// rebound, and actions whose overrides vanished get their defaults restored -
// so in-flight dispatcher state for untouched actions is never torn down.
// Validation failures degrade to warnings + skipped rules; nothing here can
// crash startup on malformed persisted data.

import { useStore } from "zustand";
import { createStore, type StoreApi } from "zustand/vanilla";
import { serializeChord } from "../keybindings/chord";
import { rebindAction, removeActionBindings, restoreDefaultBinding } from "../keybindings/overrides";
import { type Binding, keybindingsRegistry } from "../keybindings/registry";
import { type OverrideRule, type ValidationWarning, validateOverrideRules } from "../keybindings/validation";
import { WireError } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { AnyNotification, KeybindingsOverrides } from "../protocol/types.gen";
import { connectionStore } from "./connection";

export interface KeybindingsStoreState {
  hubSupport: "unknown" | "supported" | "unsupported";
  hubLoading: boolean;
  hubError: string | null;
  /** Revision of the last confirmed hub payload (0 = shipped defaults). */
  revision: number;
  /** The validated rules currently applied to the registry. */
  overrides: readonly OverrideRule[];
  /** Semantic-validation warnings from the last applied payload. */
  warnings: readonly ValidationWarning[];
  /** Set when a patch lost the revision race; the store has already refreshed
   * to the server's current state when this is non-null. */
  conflict: string | null;
  refreshOverrides(): Promise<void>;
  patchOverrides(rules: readonly OverrideRule[]): Promise<KeybindingsOverrides>;
}

function initialState(): Omit<KeybindingsStoreState, "refreshOverrides" | "patchOverrides"> {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    revision: 0,
    overrides: [],
    warnings: [],
    conflict: null,
  };
}

let wiredClient: AppwireClientLike | null = null;
let unwireNotification: (() => void) | null = null;
let unwireReady: (() => void) | null = null;
let clientEpoch = 0;
let activeReadyClient: AppwireClientLike | null = null;
let activeReadyEpoch = -1;
let refreshSerial = 0;
let patchSerial = 0;

/** The action -> effective override (serialized chord, or null for an unbind)
 * currently applied to the registry. Module-level like the template's wiring
 * state: it describes the registry singleton, not store state. */
const appliedOverrides = new Map<string, string | null>();

/** Structural check for a wire payload (get result, changed params, patch
 * response, conflict `current`). The server's own validation already ran; this
 * is the trust-boundary re-check so a malformed payload degrades to an error
 * state instead of a crash. */
function fromWireOverrides(value: unknown): KeybindingsOverrides | undefined {
  if (typeof value !== "object" || value === null || Array.isArray(value)) return undefined;
  const candidate = value as Record<string, unknown>;
  if (candidate.version !== 1) return undefined;
  if (typeof candidate.revision !== "number" || !Number.isSafeInteger(candidate.revision) || candidate.revision < 0)
    return undefined;
  if (!Array.isArray(candidate.rules)) return undefined;
  for (const rule of candidate.rules) {
    if (typeof rule !== "object" || rule === null || Array.isArray(rule)) return undefined;
    const entry = rule as Record<string, unknown>;
    if (typeof entry.action !== "string" || !(entry.chord === null || typeof entry.chord === "string"))
      return undefined;
  }
  return value as KeybindingsOverrides;
}

/** Restores a bindings snapshot taken before a failed reconcile: unwinds
 * every current binding and re-registers the snapshot in order, returning
 * the registry to its last good state. Re-registering a previously-valid
 * set cannot conflict. */
function rollbackBindings(snapshot: readonly Binding[]): void {
  const state = keybindingsRegistry.getState();
  for (const binding of state.bindings) state.unregisterBinding(binding.id);
  for (const binding of snapshot) {
    state.registerBinding({
      id: binding.id,
      actionId: binding.actionId,
      chord: binding.chord,
      scope: binding.scope,
      ...(binding.when === undefined ? {} : { when: binding.when }),
      allowInEditable: binding.allowInEditable,
      allowInModal: binding.allowInModal,
      ignoreIfDefaultPrevented: binding.ignoreIfDefaultPrevented,
    });
  }
}

/** Reconciles the registry to the payload's effective overrides: validates,
 * strips the bindings of every action whose effective chord changed, then
 * re-establishes each (override or restored default). Two-phase so a payload
 * that moves a chord between actions never trips a transient conflict. The
 * mutation is atomic: a throw rolls the registry back to its pre-reconcile
 * state before propagating, so callers surface the failure with the last
 * good bindings intact. */
function applyOverrideRules(rules: readonly OverrideRule[]): void {
  const validated = validateOverrideRules(rules, keybindingsRegistry, undefined, new Set(appliedOverrides.keys()));
  const next = new Map<string, string | null>();
  for (const rule of validated.rules) {
    next.set(rule.action, rule.chord === null ? null : serializeChord(rule.chord));
  }
  const changedActions = new Set<string>();
  for (const [action, chord] of next) {
    if (!appliedOverrides.has(action) || appliedOverrides.get(action) !== chord) changedActions.add(action);
  }
  for (const action of appliedOverrides.keys()) {
    if (!next.has(action)) changedActions.add(action);
  }
  if (changedActions.size > 0) {
    const snapshot = keybindingsRegistry.getState().bindings;
    try {
      for (const action of changedActions) removeActionBindings(keybindingsRegistry, action);
      for (const action of changedActions) {
        if (next.has(action)) {
          rebindAction(keybindingsRegistry, action, next.get(action) ?? null);
        } else {
          restoreDefaultBinding(keybindingsRegistry, action);
        }
      }
    } catch (error) {
      rollbackBindings(snapshot);
      throw error;
    }
  }
  appliedOverrides.clear();
  for (const [action, chord] of next) appliedOverrides.set(action, chord);
  keybindingsStore.setState({
    overrides: validated.rules.map((rule) => ({
      action: rule.action,
      chord: rule.chord === null ? null : serializeChord(rule.chord),
    })),
    warnings: validated.warnings,
  });
}

/** Applies a confirmed hub payload (get result, changed params, patch
 * response): stale revisions are ignored, the rest reconcile the registry.
 * The revision advances ONLY after a successful reconcile: a failed apply
 * leaves the previous revision in place so the payload stays retryable and
 * a later `changed` with the same revision is not eaten by the stale guard. */
function applyHubOverrides(payload: KeybindingsOverrides): void {
  const state = keybindingsStore.getState();
  if (payload.revision < state.revision) return;
  applyOverrideRules(payload.rules);
  // A successful apply supersedes any earlier apply failure's hubError AND
  // any earlier patch's revision-race conflict - the store is now confirmed
  // at this payload either way. Clearing one without the other was the
  // parked 2b asymmetry: a stale conflict notice outlived the state it
  // described (only a successful PATCH cleared it).
  keybindingsStore.setState({ revision: payload.revision, hubError: null, conflict: null });
}

function currentSupport(): "unknown" | "supported" | "unsupported" {
  const features = connectionStore.getState().features;
  if (features === undefined) return "unknown";
  return features.keybindingsSettings === true ? "supported" : "unsupported";
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
  const state = keybindingsStore.getState();
  if (support === "supported") {
    if (state.hubSupport !== support) keybindingsStore.setState({ hubSupport: support });
    return;
  }
  // The conflict notice clears with hubError here too (the 2b clear
  // asymmetry): a support drop disconnects the store from the hub state the
  // conflict described, so keeping it would be as stale as keeping hubError.
  if (state.hubSupport !== support || state.hubLoading || state.hubError !== null || state.conflict !== null)
    keybindingsStore.setState({ hubSupport: support, hubLoading: false, hubError: null, conflict: null });
}

function onNotification(notification: AnyNotification): void {
  if (notification.method !== "evener/settings/keybindings/changed") return;
  const payload = fromWireOverrides(notification.params);
  if (payload === undefined) return;
  try {
    applyHubOverrides(payload);
  } catch (error) {
    // Same posture as refreshFor: a reconcile failure surfaces as hubError
    // (the registry has already rolled back to its last good state), never
    // as an exception escaping the client's notification dispatch.
    keybindingsStore.setState({ hubError: error instanceof Error ? error.message : String(error) });
  }
}

async function refreshFor(client: AppwireClientLike, epoch: number): Promise<void> {
  if (!isCurrentReady(client, epoch) || currentSupport() !== "supported") return;
  const serial = ++refreshSerial;
  keybindingsStore.setState({ hubLoading: true, hubError: null });
  try {
    const result = await client.request("evener/settings/keybindings/get", {});
    if (!isCurrentReady(client, epoch) || serial !== refreshSerial || currentSupport() !== "supported") return;
    const payload = fromWireOverrides(result);
    if (payload === undefined) throw new Error("Hub returned malformed keybindings overrides");
    applyHubOverrides(payload);
  } catch (error) {
    if (isCurrentReady(client, epoch) && serial === refreshSerial) {
      keybindingsStore.setState({ hubError: error instanceof Error ? error.message : String(error) });
    }
  } finally {
    if (isCurrentReady(client, epoch) && serial === refreshSerial) keybindingsStore.setState({ hubLoading: false });
  }
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  invalidateReadyGeneration();
  unwireReady?.();
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
    unwireReady?.();
    unwireReady = null;
    wiredClient = null;
  }
  setSupportFromConnection();
  if (
    state.client === wiredClient &&
    state.features?.keybindingsSettings === true &&
    previous.features?.keybindingsSettings !== true &&
    state.client?.state === "ready"
  ) {
    if (activeReadyClient === state.client) void refreshFor(state.client, activeReadyEpoch);
  }
}

/** Extracts the server's current payload from a revision-conflict rejection
 * (appwire CodeConflict -32013 with data.evenerErrorInfo "conflict"). */
function conflictCurrent(error: unknown): KeybindingsOverrides | undefined {
  if (!(error instanceof WireError) || error.code !== -32013 || typeof error.data !== "object" || error.data === null)
    return undefined;
  const data = error.data as Record<string, unknown>;
  if (data.evenerErrorInfo !== "conflict") return undefined;
  return fromWireOverrides(data.current);
}

export const keybindingsStore: StoreApi<KeybindingsStoreState> = createStore<KeybindingsStoreState>(() => ({
  ...initialState(),
  refreshOverrides: async () => {
    const client = currentClient();
    if (client === null || client !== wiredClient || activeReadyClient !== client) return;
    await refreshFor(client, activeReadyEpoch);
  },
  patchOverrides: async (rules): Promise<KeybindingsOverrides> => {
    const state = keybindingsStore.getState();
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
      const error = "Hub keybindings settings are unavailable.";
      keybindingsStore.setState({ hubError: error });
      throw new Error(error);
    }
    // Pre-flight semantic validation (the parked 2b minor the settings
    // editor makes live): the SAME simulation the reconcile path runs on a
    // confirmed payload, run BEFORE the hub write. A rule the reconcile
    // would skip - unknown action, unparseable or platform-reserved chord,
    // or a conflict on the simulated final map - would otherwise be accepted
    // by the hub (the server validates structure only) and then silently not
    // apply. Reject instead, with the validation layer's own message, and
    // leave hubError/conflict untouched: nothing hub-sourced happened. The
    // currently-applied set re-validates clean (it did at apply time and the
    // reserved lists are platform-static), so a warning always names a rule
    // this call introduced.
    const preflight = validateOverrideRules(rules, keybindingsRegistry, undefined, new Set(appliedOverrides.keys()));
    if (preflight.warnings.length > 0) {
      throw new Error(preflight.warnings.map((warning) => warning.message).join("\n"));
    }
    const token = ++patchSerial;
    try {
      const result = await client.request("evener/settings/keybindings/patch", {
        expectedRevision: state.revision,
        config: { version: 1, rules: rules.map((rule) => ({ action: rule.action, chord: rule.chord })) },
      });
      if (token !== patchSerial || !isCurrentReady(client, generation)) {
        const current = keybindingsStore.getState();
        return { version: 1, revision: current.revision, rules: [...current.overrides] };
      }
      const payload = fromWireOverrides(result);
      if (payload === undefined) throw new Error("Hub returned malformed keybindings PATCH response");
      applyHubOverrides(payload);
      return payload;
    } catch (error) {
      if (token === patchSerial && isCurrentReady(client, generation)) {
        const current = conflictCurrent(error);
        if (current !== undefined) applyHubOverrides(current);
        const message = error instanceof Error ? error.message : String(error);
        keybindingsStore.setState({
          hubError: message,
          ...(current === undefined ? {} : { conflict: message }),
        });
      }
      throw error;
    }
  },
}));

connectionStore.subscribe(onConnectionChange);
const initialClient = connectionStore.getState().client;
if (initialClient !== null) rewireClient(initialClient);

export function resetKeybindingsStoreForTests(): void {
  invalidateReadyGeneration();
  unwireReady?.();
  unwireReady = null;
  wiredClient = null;
  refreshSerial += 1;
  patchSerial += 1;
  // Restore defaults for every applied override so the registry singleton
  // cannot leak overrides into the next test. A wedged registry (a foreign
  // binding squatting a default chord) must not make reset itself throw.
  for (const action of appliedOverrides.keys()) {
    try {
      restoreDefaultBinding(keybindingsRegistry, action);
    } catch {
      // The next test rebuilds the registry from scratch; a failed restore
      // here leaves the override binding in place, which that rebuild removes.
    }
  }
  appliedOverrides.clear();
  keybindingsStore.setState({ ...initialState() });
  setSupportFromConnection();
}

export function useKeybindingsStore(): KeybindingsStoreState;
export function useKeybindingsStore<T>(selector: (state: KeybindingsStoreState) => T): T;
export function useKeybindingsStore<T>(selector?: (state: KeybindingsStoreState) => T): T | KeybindingsStoreState {
  // Same Zustand hook in both arms; the overload only avoids exposing an
  // optional selector to useStore's stricter TypeScript signature.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call the same hook
  return selector ? useStore(keybindingsStore, selector) : useStore(keybindingsStore);
}
