// Pure projection from ConnectionStatus + Reachability to a display-safe
// LiveConnectionView. No DOM, no network — given the same inputs it produces
// the same output.
//
// Exact mapping (from the brief):
//   initial or loading                       => connecting
//   ready + reachable                        => connected
//   ready + reconnecting                     => reconnecting
//   ready + unknown                          => connecting
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
      if (_reachability === "unreachable") return { status: "offline" };
      if (_reachability === "reconnecting") return { status: "reconnecting" };
      return _reachability === "reachable"
        ? { status: "connected" }
        : { status: "connecting" };
    case "error":
      return { status: "error" };
  }
}
