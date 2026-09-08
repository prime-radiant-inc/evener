// The personal AGENTS.md store: the hub-authoritative file behind
// Settings -> AGENTS.md (spec 2026-09-07 §1). One document, read whole and
// written whole; no revision or conflict handling by design - the file is
// also hand-edited, and the hub's own rule is last write wins. What the
// store DOES keep current is `doc`: every evener/settings/agentsDoc/changed
// broadcast (this client's own save included) replaces it, so a second tab
// or a save from the TUI lands here without a refetch.
//
// An editor must key off `doc.content`, never `doc` itself: one save yields
// two referentially-distinct `doc` objects carrying the same content - the
// save's own result, then the hub's broadcast echo behind it - so an
// identity-keyed effect would wipe anything typed during the round-trip.
//
// requireClient() throws outside any try/catch, matching stores/credentials.ts:
// "no client connected" is a programmer error, not a state to degrade into.
import { useStore } from "zustand";
import { createStore } from "zustand/vanilla";
import { errorText } from "../protocol/errors";
import type { AppwireClientLike } from "../protocol/testing/fakeClient";
import type { AgentsDocResponse, AnyNotification } from "../protocol/types.gen";
import { connectionStore } from "./connection";

function requireClient(): AppwireClientLike {
  const client = connectionStore.getState().client;
  if (!client) {
    throw new Error("agentsDoc store: no client connected; call useConnectionStore.getState().connect(client) first");
  }
  return client;
}

export interface AgentsDocStoreState {
  /** The last document the hub confirmed - null until the first fetch lands. */
  doc: AgentsDocResponse | null;
  loading: boolean;
  error: string | null;
  fetch(): Promise<void>;
  /** Replaces the file whole. Resolves with the hub's view of the saved file,
   * which is also what `doc` becomes. Rejections propagate: the section
   * owns the inline error and the toast. */
  save(content: string): Promise<AgentsDocResponse>;
}

// Only the most recently started read may land: a response from an
// overlapping older fetch, or from a client the store has since replaced,
// carries a view of the file that is already out of date. requestedDoc is
// what tells the reconnect subscriber below that a view has asked for this
// file at all.
let requestVersion = 0;
let requestedDoc = false;

export const agentsDocStore = createStore<AgentsDocStoreState>((set) => ({
  doc: null,
  loading: false,
  error: null,

  async fetch() {
    const client = requireClient();
    requestedDoc = true;
    const version = ++requestVersion;
    set({ loading: true, error: null });
    try {
      const doc = await client.request("evener/settings/agentsDoc/get", {});
      if (version !== requestVersion || connectionStore.getState().client !== client) return;
      set({ doc, loading: false });
    } catch (err) {
      if (version !== requestVersion || connectionStore.getState().client !== client) return;
      set({ loading: false, error: errorText(err) });
    }
  },

  async save(content) {
    const client = requireClient();
    const doc = await client.request("evener/settings/agentsDoc/set", { content });
    set({ doc });
    return doc;
  },
}));

export function useAgentsDocStore(): AgentsDocStoreState;
export function useAgentsDocStore<T>(selector: (state: AgentsDocStoreState) => T): T;
export function useAgentsDocStore<T>(selector?: (state: AgentsDocStoreState) => T): T | AgentsDocStoreState {
  // Not a real conditional hook call - see stores/connection.ts's own
  // useConnectionStore for the full explanation.
  // biome-ignore lint/correctness/useHookAtTopLevel: same hook both arms, JS default param not a real conditional - see stores/connection.ts
  return selector ? useStore(agentsDocStore, selector) : useStore(agentsDocStore);
}

// --- notification wiring ----------------------------------------------------

let wiredClient: AppwireClientLike | null = null;
let unsubscribeNotifications: (() => void) | undefined;

function handleNotification(n: AnyNotification): void {
  if (n.method === "evener/settings/agentsDoc/changed") agentsDocStore.setState({ doc: n.params });
}

function attachNotifications(client: AppwireClientLike | null): void {
  if (client === wiredClient) return; // already wired to this exact client
  unsubscribeNotifications?.();
  wiredClient = client;
  unsubscribeNotifications = client?.onNotification(handleNotification);
}

// React to the connection store rather than reading it once: this module can
// be evaluated before AppShell's connect() effect runs (stores/extensions.ts
// documents the mount-order race in full).
connectionStore.subscribe((state, previous) => {
  if (state.client !== previous.client || state.state !== previous.state) {
    requestVersion += 1;
  }
  attachNotifications(state.client);
  // Once a view has read the file, every reconnect has to re-read it - an
  // automatic one reuses this same client, so a drop and recovery is a state
  // transition and nothing else. The `changed` broadcasts that landed while
  // the socket was down are gone, and the next Save would push pre-drop
  // content over whatever the hub now has.
  if (
    requestedDoc &&
    state.client &&
    state.state === "ready" &&
    (state.client !== previous.client || previous.state !== "ready")
  ) {
    void agentsDocStore
      .getState()
      .fetch()
      .catch(() => {});
  }
});
const initialClient = connectionStore.getState().client;
if (initialClient) attachNotifications(initialClient);

// resetAgentsDocStoreForTests resets the singleton between tests, including
// the module-private wiring above. No production code should ever call this.
export function resetAgentsDocStoreForTests(): void {
  requestVersion += 1;
  requestedDoc = false;
  unsubscribeNotifications?.();
  unsubscribeNotifications = undefined;
  wiredClient = null;
  agentsDocStore.setState({ doc: null, loading: false, error: null });
}
