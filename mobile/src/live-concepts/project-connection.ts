// Pure projection from ConnectionStatus + Reachability to a display-safe
// LiveConnectionView. No DOM, no network — given the same inputs it produces
// the same output.
//
// Exact mapping (from the brief):
//   initial or loading                       => connecting
//   ready + reachable/reconnecting/unknown   => connected
//   ready + unreachable                       => offline
//   error                                     => error

import type { ConnectionStatus, Reachability } from "../state/connection";
import type { LiveConnectionView } from "./model";

export function projectLiveConnection(
  status: ConnectionStatus,
  _reachability: Reachability,
): LiveConnectionView {
  switch (status) {
    case "initial":
    case "loading":
      return { status: "connecting" };
    case "ready":
      return _reachability === "unreachable"
        ? { status: "offline" }
        : { status: "connected" };
    case "error":
      return { status: "error" };
  }
}
