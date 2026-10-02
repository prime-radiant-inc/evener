// AppwireClientLike is the connection seam applications program against: the
// subset of AppwireClient's surface that stores, hooks and components
// actually call. Naming the seam instead of the concrete class is what lets a
// test substitute FakeClient (testing/fakeClient.ts) for a socket-backed
// client, and what lets an installed consumer type its own connection holder
// without depending on the class.
//
// The eight methods are declared by lookup, so a renamed or re-signed method
// on AppwireClient fails right here. `state` and `terminalReason` are spelled
// out instead, so a drifting accessor type would pass unnoticed; client.test.ts
// assigns a real client to this interface to catch that.
import type { AppwireClient, ConnectionState, TerminalReason } from "./client";
import type { MethodName, MethodTypes } from "./types.gen";

/** A structural request port retaining each method's params, result and deadline.
 * The transport accepts every MethodName; host-bound stores accept only the
 * generated HostRequestMethod subset. A broad client satisfies either port. */
export interface RequestPort<Methods extends MethodName = MethodName> {
  request<M extends Methods>(
    method: M,
    params: MethodTypes[M]["params"],
    opts?: { timeoutMs?: number },
  ): Promise<MethodTypes[M]["result"]>;
}

export interface AppwireClientLike {
  connect: AppwireClient["connect"];
  request: AppwireClient["request"];
  forceStop: AppwireClient["forceStop"];
  resumeThread: AppwireClient["resumeThread"];
  onNotification: AppwireClient["onNotification"];
  onReady: AppwireClient["onReady"];
  onStateChange: AppwireClient["onStateChange"];
  retryNow: AppwireClient["retryNow"];
  get state(): ConnectionState;
  get terminalReason(): TerminalReason;
}
