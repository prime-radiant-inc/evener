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
import { prefsStore } from "./prefs";

export interface KeybindingsStoreState {
  hubSupport: "unknown" | "supported" | "unsupported";
  hubLoading: boolean;
  hubError: string | null;
  /** Revision of the last confirmed hub payload (0 = shipped defaults). */
  revision: number;
  /** The validated rules currently applied to the registry. */
  overrides: readonly OverrideRule[];
  /** The hub payload's rules VERBATIM, before validation filtering. The
   * editor composes whole-payload PATCHes from THIS set, not from
   * `overrides`: a rule validation skips (an unknown action from a newer
   * client, an unparseable chord) is still the hub's state, and a PATCH
   * composed from the validated set would silently delete it. */
  rawOverrides: readonly OverrideRule[];
  /** Semantic-validation warnings from the last applied payload. */
  warnings: readonly ValidationWarning[];
  /** True only while the applied state (revision/overrides/rawOverrides and
   * the registry's effective bindings) was confirmed by the hub for the
   * CURRENT ready generation. Set on every successful payload apply; cleared
   * when the ready generation ends (disconnect, client replacement, test
   * reset). Editing and patchOverrides both gate on it: a PATCH composed
   * from the previous hub's raw set with the previous hub's revision would
   * overwrite the new hub's config on a revision collision. */
  loaded: boolean;
  /** Set when a patch lost the revision race; the store has already refreshed
   * to the server's current state when this is non-null. */
  conflict: string | null;
  refreshOverrides(): Promise<void>;
  /** Writes SERIALIZE at the store: at most one PATCH is in flight, and each
   * write runs only after the previous one has fully landed. Pass a THUNK
   * to compose the whole-payload rule set at execution time (against the
   * then-current rawOverrides) - a rules array composed at call time would
   * race the in-flight write it queues behind: same expectedRevision, and a
   * payload missing the first edit's confirmed change. */
  patchOverrides(rules: readonly OverrideRule[] | (() => readonly OverrideRule[])): Promise<KeybindingsOverrides>;
}

function initialState(): Omit<KeybindingsStoreState, "refreshOverrides" | "patchOverrides"> {
  return {
    hubSupport: "unknown",
    hubLoading: false,
    hubError: null,
    revision: 0,
    overrides: [],
    rawOverrides: [],
    warnings: [],
    loaded: false,
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
/** The write-serialization chain: every patchOverrides queues behind the
 * previous write's full settlement (success OR failure). Reset with the
 * rest of the store's wiring state in resetKeybindingsStoreForTests so a
 * never-resolving write cannot leak into the next test. */
let writeQueue: Promise<void> = Promise.resolve();

/** The action -> effective override (serialized chord, or null for an unbind)
 * currently applied to the registry. Module-level like the template's wiring
 * state: it describes the registry singleton, not store state. */
const appliedOverrides = new Map<string, string | null>();

/** Restores defaults for every applied override and clears the applied map:
 * the registry must stop presenting a hub's overrides the moment that hub's
 * state stops being current (client replacement, test reset) - that
 * staleness one layer down is the same defect as a stale store payload.
 * Atomic, mirroring applyOverrideRules: two-phase (strip every applied
 * action's bindings first, THEN restore each default) so a chord that moved
 * between two overridden actions never trips a transient conflict mid-unwind,
 * and any throw rolls the registry back to its pre-unwind snapshot. On
 * rollback the applied map stays INTACT - clearing it while the registry
 * still holds overrides would detach the unwind bookkeeping from the
 * registry, and the next reconcile's delta math would misread the wedged
 * bindings. The reset itself never propagates (a wedged registry - a foreign
 * binding squatting a default chord - must not make reset throw); the next
 * reconcile or the next test's registry rebuild retries from the intact map.
 *
 * ORDERING (the cheatsheetController contract): this mutates the registry
 * ONLY. Callers must fire any store setState - which is what the
 * character-key reconcile subscribes to - AFTER this returns, so the
 * reconcile sees the final shape (restore re-registers the conditional "?"
 * entry with no knowledge of the pref; the reconcile then removes it if the
 * pref is off). */
function unapplyAllOverrides(): void {
  if (appliedOverrides.size === 0) return;
  const snapshot = keybindingsRegistry.getState().bindings;
  try {
    for (const action of appliedOverrides.keys()) removeActionBindings(keybindingsRegistry, action);
    for (const action of appliedOverrides.keys()) restoreDefaultBinding(keybindingsRegistry, action);
  } catch {
    // See the doc comment: roll back, keep the applied map, never propagate.
    rollbackBindings(snapshot);
    return;
  }
  appliedOverrides.clear();
}

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
  // The pref gates the restore simulation: with character-key triggers off
  // the live registry has no "?" cheatsheet binding, so a simulated restore
  // must not claim one either.
  const validated = validateOverrideRules(
    rules,
    keybindingsRegistry,
    undefined,
    new Set(appliedOverrides.keys()),
    prefsStore.getState().characterKeyTriggers,
  );
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
  // rawOverrides is retained VERBATIM (validation filtering already happened
  // inside applyOverrideRules): an edit's whole-payload PATCH is composed
  // from this set so a rule validation skips survives an unrelated edit.
  // Only a successful reconcile advances it - a failed apply leaves the last
  // good raw set beside the last good revision.
  keybindingsStore.setState({
    rawOverrides: payload.rules.map((rule) => ({ action: rule.action, chord: rule.chord })),
    revision: payload.revision,
    // Every call site is generation-guarded (refreshFor, the notification
    // wrapper, patchOverrides), so a successful apply confirms the state for
    // the CURRENT ready generation - editing and patching gate on this.
    loaded: true,
    hubError: null,
    conflict: null,
  });
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
  // The ready generation that confirmed the loaded state just ended; nothing
  // hub-sourced is current until the next generation's refresh lands.
  if (keybindingsStore.getState().loaded) keybindingsStore.setState({ loaded: false });
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
  if (support === "unsupported") {
    // Support resolving to UNSUPPORTED is not the transient-disconnect case
    // (a supported hub that is temporarily unreachable keeps its overrides
    // firing - the ruled behavior): the feature set is KNOWN and does not
    // advertise keybindings, so the settings section claims "the built-in
    // defaults are in effect". The registry must match that claim. Un-apply
    // BEFORE the setState - the character-key reconcile subscribes to the
    // store and must see the final registry shape (see unapplyAllOverrides).
    unapplyAllOverrides();
  }
  // The unsupported drop also discards the hub PAYLOAD state: retaining
  // loaded/revision/rawOverrides across a flap would let a later supported
  // reconnect's refresh be eaten by the stale guard (the retained revision
  // can be HIGHER than the returning hub's - a restored backup, a reset
  // state file) and would leave edits composing from the old hub's raw set
  // with the old expectedRevision. The setState stays AFTER the registry
  // mutation, per the reconcile ordering contract.
  const dropHubState =
    support === "unsupported" &&
    (state.loaded || state.revision !== 0 || state.overrides.length > 0 || state.rawOverrides.length > 0);
  // The conflict notice clears with hubError here too (the 2b clear
  // asymmetry): a support drop disconnects the store from the hub state the
  // conflict described, so keeping it would be as stale as keeping hubError.
  if (
    state.hubSupport !== support ||
    state.hubLoading ||
    state.hubError !== null ||
    state.conflict !== null ||
    dropHubState
  )
    keybindingsStore.setState({
      hubSupport: support,
      hubLoading: false,
      hubError: null,
      conflict: null,
      ...(dropHubState ? { loaded: false, revision: 0, overrides: [], rawOverrides: [], warnings: [] } : {}),
    });
}

function onNotification(notification: AnyNotification): void {
  if (notification.method !== "evener/settings/keybindings/changed") return;
  // A late notification landing during an unsupported window must not
  // re-install overrides into a registry the support drop just un-applied.
  if (currentSupport() !== "supported") return;
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
  // The loaded state belongs to the PREVIOUS hub. Until this client's
  // refresh lands it must not present as current: a patch composed from the
  // old raw set with the old expectedRevision can overwrite the new hub's
  // config on a revision collision, and the registry firing the old hub's
  // overrides is the same staleness one layer down. Un-apply first, THEN
  // setState - the character-key reconcile subscribes to the store and must
  // see the final registry shape (see unapplyAllOverrides). hubSupport is
  // connection-sourced, not hub state, so it is left to
  // setSupportFromConnection.
  unapplyAllOverrides();
  keybindingsStore.setState({
    revision: 0,
    overrides: [],
    rawOverrides: [],
    warnings: [],
    hubLoading: false,
    hubError: null,
    conflict: null,
  });
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
  patchOverrides: (rulesOrCompose): Promise<KeybindingsOverrides> => {
    // Fence the write to the ready generation it was CREATED under. A queued
    // write executes only after the previous write settles, which can be
    // after a rewire to a NEW hub: the thunk's compose-at-execution is right
    // WITHIN one generation (finding 16), but a write whose generation has
    // ended carries edit intent made against the hub whose state the user
    // was looking at - landing it on the new hub's config is the wrong
    // default even though the payload would compose cleanly there. It
    // rejects with the same unavailable-class error instead, before any
    // wire request.
    const callClient = currentClient();
    const callGeneration = callClient !== null && callClient === activeReadyClient ? activeReadyEpoch : -1;
    const run = async (): Promise<KeybindingsOverrides> => {
      // Compose at EXECUTION time: a thunk reads the raw set as the
      // previous write left it, folding its confirmed payload into this
      // write's rules instead of racing it.
      const rules = typeof rulesOrCompose === "function" ? rulesOrCompose() : rulesOrCompose;
      const state = keybindingsStore.getState();
      const client = currentClient();
      const generation = client === activeReadyClient ? activeReadyEpoch : -1;
      if (
        state.hubSupport !== "supported" ||
        state.loaded !== true ||
        state.hubLoading ||
        currentSupport() !== "supported" ||
        client === null ||
        client !== wiredClient ||
        generation < 0 ||
        client.state !== "ready" ||
        callClient === null ||
        callGeneration < 0 ||
        !isCurrentReady(callClient, callGeneration)
      ) {
        // `loaded` is the defense-in-depth half of the editor's gate: the UI
        // is not the store's contract, and a patch composed from a STALE
        // generation's raw set (client replaced, refresh not yet landed) would
        // send the old hub's expectedRevision and rules to the new hub.
        // `hubLoading` is the same race WITHIN one generation: an in-flight
        // refresh is about to land a payload whose revision may differ from
        // the one a concurrent PATCH would send as expectedRevision.
        // The call-time fence is the same staleness across the QUEUE: this
        // write was created under a generation that has since ended.
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
      // payload is composed from the hub's RAW rules (rawOverrides), so it can
      // carry a PRESERVED rule validation skips - an unknown action from a
      // newer client, say. That is a pre-existing condition, not a defect this
      // call introduced: reject only on warnings the CURRENT raw set does not
      // already produce, keyed by action+message (the baseline simulation runs
      // against the same live registry and applied set, so a preserved rule
      // reproduces its apply-time warning verbatim).
      const applied = new Set(appliedOverrides.keys());
      const pref = prefsStore.getState().characterKeyTriggers;
      const baseline = validateOverrideRules(state.rawOverrides, keybindingsRegistry, undefined, applied, pref);
      const baselineKeys = new Set(baseline.warnings.map((warning) => `${warning.rule.action} ${warning.message}`));
      const preflight = validateOverrideRules(rules, keybindingsRegistry, undefined, applied, pref);
      const introduced = preflight.warnings.filter(
        (warning) => !baselineKeys.has(`${warning.rule.action} ${warning.message}`),
      );
      if (introduced.length > 0) {
        throw new Error(introduced.map((warning) => warning.message).join("\n"));
      }
      const token = ++patchSerial;
      try {
        const result = await client.request("evener/settings/keybindings/patch", {
          expectedRevision: state.revision,
          config: { version: 1, rules: rules.map((rule) => ({ action: rule.action, chord: rule.chord })) },
        });
        if (token !== patchSerial || !isCurrentReady(client, generation) || currentSupport() !== "supported") {
          // A response landing after support loss must not re-apply: the
          // unsupported branch already un-applied and reset the hub state.
          const current = keybindingsStore.getState();
          return { version: 1, revision: current.revision, rules: [...current.rawOverrides] };
        }
        const payload = fromWireOverrides(result);
        if (payload === undefined) throw new Error("Hub returned malformed keybindings PATCH response");
        applyHubOverrides(payload);
        return payload;
      } catch (error) {
        if (token === patchSerial && isCurrentReady(client, generation) && currentSupport() === "supported") {
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
    };
    // Chain behind the previous write's SETTLEMENT: a failed write must not
    // block the queue, and the next write composes against whatever state
    // the failure left (the conflict path already refreshed it).
    const result = writeQueue.then(run, run);
    writeQueue = result.then(
      () => undefined,
      () => undefined,
    );
    return result;
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
  writeQueue = Promise.resolve();
  // Restore defaults for every applied override so the registry singleton
  // cannot leak overrides into the next test (the next test rebuilds the
  // registry from scratch, which removes any binding a wedged restore left).
  unapplyAllOverrides();
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
