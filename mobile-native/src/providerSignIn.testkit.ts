import { createCredentialInstancesStore } from "@evener/appwire-client/state/credentials";
import type { ConversationClientLike } from "../../mobile/src/services/conversation";
import { ProviderSignIn } from "./providerSignIn";

export interface RecordedCall {
  method: string;
  params: unknown;
}
/** The answers a scripted client gives; tests reassign `request` mid-flow. */
export interface ScriptedIO {
  request: (method: string, params: unknown) => Promise<unknown>;
}

/** A client that answers from `io`, records every call in `calls`, and
 * carries the onNotification the credential store subscribes with. */
export function signInClient(io: ScriptedIO, calls: RecordedCall[]): ConversationClientLike {
  return {
    request: (method, params) => {
      calls.push({ method, params });
      return io.request(method, params);
    },
    onNotification: () => () => {},
  } as ConversationClientLike;
}

/** One sign-in flow over its own credential store, connected to a scripted
 * client whose default reply per method is `answer`. `connect` does what
 * ProvidersScreen's connection effect does: the store's transport and the
 * flow's connected gate move to one client together. */
export function boundary(answer: (method: string) => unknown) {
  const calls: RecordedCall[] = [];
  const io: ScriptedIO = { request: async (method) => answer(method) };
  const client = signInClient(io, calls);
  const store = createCredentialInstancesStore({ ownClientId: () => "native-test" });
  const flow = new ProviderSignIn(store, "work");
  const connect = (next: ConversationClientLike | null) => {
    store.connectionChanged(next, next ? "ready" : "closed");
    flow.setConnection(next);
  };
  connect(client);
  return { flow, calls, io, client, store, connect };
}

/** The flow's own calls: the store's listing reads are not its. */
export function authCalls(calls: RecordedCall[]): RecordedCall[] {
  return calls.filter((call) => call.method !== "evener/instance/list");
}
