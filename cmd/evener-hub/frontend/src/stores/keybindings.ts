// The web's one keybindings overrides store: the package's
// createKeybindingsStore bound to the app registry (keybindings/appRegistry.ts),
// the character-key pref (stores/prefs.ts) and whichever client connectionStore
// holds, with zustand's useStore for the reactive read. The store's hub
// posture - feature gating, ready-generation fencing, delta reconciliation
// into the registry, serialized writes - is the package's; this file owns the
// CONNECTION lifecycle: which client is wired, when a ready generation begins
// and ends, and what the connection's feature set says about support.

import {
  type AppwireClientLike,
  createKeybindingsStore,
  type KeybindingsStoreState,
  keybindingsSupport,
} from "@evener/appwire-client";
import { useStore } from "zustand";
import { keybindingsRegistry } from "../keybindings/appRegistry";
import { connectedClientPort, connectionStore } from "./connection";
import { prefsStore } from "./prefs";
import { createReadyGenerationCallback } from "./readyGenerationCallback";

export type { KeybindingsStoreState };

// This store's own guard: transcriptDisplay.ts wires the same connectionStore
// client through its own instance, so the two never contend over one shared
// registration slot.
const readyGenerationCallback = createReadyGenerationCallback();

// The client port resolves connectionStore's CURRENT client on every call.
// Two ordering contracts make the singleton behave like one store per
// connection: the store subscribes to notifications once per ready
// generation and this file begins a generation only once a client is wired
// (including the generation setSupport begins on a flap back to supported),
// so each subscription lands on the client that generation belongs to; and
// onConnectionChange publishes support BEFORE rewiring, so the refresh a
// rewire kicks reads the connection's current feature set.
const connectedClient = connectedClientPort("keybindings");

export const keybindingsStore = createKeybindingsStore({
  client: connectedClient,
  registry: keybindingsRegistry,
  characterKeyTriggers: () => prefsStore.getState().characterKeyTriggers,
});

let wiredClient: AppwireClientLike | null = null;
let unwireReady: (() => void) | null = null;

function beginReadyGeneration(): void {
  keybindingsStore.beginReadyGeneration();
  void keybindingsStore.getState().refreshOverrides();
}

function rewireClient(client: AppwireClientLike): void {
  if (client === wiredClient) return;
  keybindingsStore.endReadyGeneration();
  unwireReady?.();
  wiredClient = client;
  // The loaded state belongs to the PREVIOUS hub: un-apply its overrides and
  // reset its payload before this client's first refresh can land.
  keybindingsStore.detachHub();
  unwireReady = client.onReady(readyGenerationCallback(client, () => wiredClient, beginReadyGeneration));
  if (client.state === "ready") beginReadyGeneration();
}

function onConnectionChange(
  state: ReturnType<typeof connectionStore.getState>,
  previous: ReturnType<typeof connectionStore.getState>,
): void {
  // Support FIRST (see connectedClient). The store owns what a support
  // transition means: a flap back to supported begins a new ready generation
  // (finding 24), and any transition into supported with a generation active
  // refreshes under it.
  keybindingsStore.setSupport(keybindingsSupport(state.features));
  if (state.client !== wiredClient && state.client !== null) rewireClient(state.client);
  if (
    state.client === wiredClient &&
    previous.client === state.client &&
    previous.state === "ready" &&
    state.state !== "ready"
  ) {
    keybindingsStore.endReadyGeneration();
  }
  if (state.client === null && wiredClient !== null) {
    keybindingsStore.endReadyGeneration();
    unwireReady?.();
    unwireReady = null;
    wiredClient = null;
  }
}

connectionStore.subscribe(onConnectionChange);
// A module evaluating AFTER the handshake (a lazily loaded settings chunk, HMR)
// finds a connected client and its feature set already in the store: seed
// both, in onConnectionChange's order, so the transition into supported
// loads exactly as a live connection change would.
const initial = connectionStore.getState();
keybindingsStore.setSupport(keybindingsSupport(initial.features));
if (initial.client !== null) rewireClient(initial.client);

// A characterKeyTriggers flip changes what the persisted rules MEAN: a rule
// skipped while the pref was on (a Shift+? claim conflicting with the
// built-in "?" trigger) validates clean once the pref is off, and an applied
// rule that overlaps "?" stops validating when the pref flips back on. The
// store re-applies the hub's raw set through the same pref-aware simulation;
// its setState re-fires this store's subscribers - the cheatsheetController's
// reconcile among them - which is total and idempotent, so the two compose in
// either subscription order.
prefsStore.subscribe((state, previous) => {
  if (state.characterKeyTriggers === previous.characterKeyTriggers) return;
  keybindingsStore.reapplyOverrides();
});

export function resetKeybindingsStoreForTests(): void {
  keybindingsStore.reset();
  unwireReady?.();
  unwireReady = null;
  wiredClient = null;
  keybindingsStore.setSupport(keybindingsSupport(connectionStore.getState().features));
}

export function useKeybindingsStore(): KeybindingsStoreState;
export function useKeybindingsStore<T>(selector: (state: KeybindingsStoreState) => T): T;
export function useKeybindingsStore<T>(selector?: (state: KeybindingsStoreState) => T): T | KeybindingsStoreState {
  // Same Zustand hook in both arms; the overload only avoids exposing an
  // optional selector to useStore's stricter TypeScript signature.
  // biome-ignore lint/correctness/useHookAtTopLevel: both arms call the same hook
  return selector ? useStore(keybindingsStore, selector) : useStore(keybindingsStore);
}
