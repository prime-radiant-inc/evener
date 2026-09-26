// The ready-generation wiring and payload-retirement primitives every
// settings-hub store built on a ReadyGenerationFence shares. Verified
// against both real implementations: keybindingsStore.ts here, and PR
// #1549's transcriptDisplayStore.ts as the design oracle for a second store
// not yet built. beginReadyGeneration/endReadyGeneration's fence-begin/end
// plus notification wire/unwire is byte-identical between the two, and so
// is the in-flight-flags half of retirePayload (loaded/saving/hubLoading/
// writeUncertain) and of a lost-hub write's own settle.
//
// What does NOT generalize stays store-owned: setSupport (the oracle's
// unsupported branch runs a keybinding reconciler's un-apply/rollback that a
// display-defaults store has no equivalent of - see keybindingsStore.ts's
// own setSupport), and a confirmed payload's own shape. The oracle keeps NO
// top-level revision, appliedSerial or conflict field at all - its confirmed
// defaults are a per-layout map, each layout with its own revision - so
// those three do not belong in this core the way an earlier plan assumed;
// only the fields below actually generalize.

import type { ReadyGenerationFence } from "./readyGenerationFence";

export interface SettingsHubIdleFields {
  loaded: boolean;
  saving: boolean;
  hubLoading: boolean;
  writeUncertain: boolean;
}

/** The confirmed payload can no longer be acted on (the generation ended,
 * support dropped, the hub was replaced): it stops presenting as current,
 * every reply still in flight is superseded so it lands nothing, and the
 * in-flight flags end here. A checkpointed write caught mid-flight has an
 * UNKNOWN outcome - exactly what writeUncertain means, and what its
 * checkpoint already says on disk; the next authoritative read settles it.
 * `extra` is the caller's own payload-shaped reset (a revision back to 0, a
 * per-layout map cleared) in the SAME publish. */
export function retireSettingsHubPayload<Fields extends SettingsHubIdleFields>(
  fence: ReadyGenerationFence,
  getState: () => Fields,
  setState: (partial: Partial<Fields>) => void,
  extra: Partial<Fields> = {},
): void {
  fence.supersede();
  const state = getState();
  setState({
    loaded: false,
    saving: false,
    hubLoading: false,
    writeUncertain: state.writeUncertain || state.saving,
    ...extra,
  } as Partial<Fields>);
}

export interface SettingsHubWriteFields {
  saving: boolean;
  writeUncertain: boolean;
}

/** Publishes the end of a write whose reply can never be settled by
 * anything else: a lost hub (this write's own claim is intact and only
 * support went away - the unknown window keeps the state and the in-flight
 * work) means nothing else will ever clear `saving` for it. A superseded
 * reply (a later write, or a payload retirement, took over) does nothing
 * here - its successor owns the flags. `stillClaimed` is the caller's own
 * check (a store-wide write token, or one per row) - the fence knows only
 * whether the generation itself is still current. `extra` is the caller's
 * own payload-shaped addition (keybindingsStore.ts's own draftConflict, a
 * field this generic Fields type does not carry) in the SAME publish, the
 * same pattern retireSettingsHubPayload above takes. */
export function settleUnsettleableWrite<Fields extends SettingsHubWriteFields>(
  fence: ReadyGenerationFence,
  generation: number,
  stillClaimed: boolean,
  getState: () => Fields,
  setState: (partial: Partial<Fields>) => void,
  extra: Partial<Fields> = {},
): void {
  if (fence.lostHub(generation, stillClaimed) && getState().saving) {
    setState({ saving: false, writeUncertain: true, ...extra } as Partial<Fields>);
  }
}

export interface SettingsHubGeneration {
  beginReadyGeneration(): void;
  endReadyGeneration(): void;
}

/** The fence-begin/end pair every settings-hub store drives its notification
 * dispatch and payload lifetime from. `wireNotifications` is called fresh on
 * every begin - never on the fence's own internal epoch bump alone - and
 * returns the unsubscribe; a store resets its own per-generation bookkeeping
 * (a missed-change flag, per-row write tokens) inside it, before
 * subscribing. `retirePayload` is the store's own primitive (composed from
 * retireSettingsHubPayload above with whatever payload-shaped extra it
 * needs) - called once fence.end() and the unwire have both happened. */
export function createSettingsHubGeneration(deps: {
  fence: ReadyGenerationFence;
  wireNotifications: (generation: number) => () => void;
  retirePayload: () => void;
}): SettingsHubGeneration {
  let unwireNotification: (() => void) | null = null;
  return {
    beginReadyGeneration(): void {
      const generation = deps.fence.begin();
      if (generation < 0) return;
      unwireNotification?.();
      unwireNotification = deps.wireNotifications(generation);
    },
    endReadyGeneration(): void {
      deps.fence.end();
      unwireNotification?.();
      unwireNotification = null;
      deps.retirePayload();
    },
  };
}
