// Pure projection tests for projectLiveConnection. projectLiveConnection
// maps a ConnectionStatus + Reachability into a display-safe
// LiveConnectionView. Exact mapping (from the brief):
//
//   initial or loading                       => connecting
//   ready + reachable/reconnecting/unknown   => connected
//   ready + unreachable                       => offline
//   error                                     => error

import { describe, expect, it } from "vitest";
import type { LiveConnectionView } from "./model";
import { projectLiveConnection } from "./project-connection";

// --- tests ------------------------------------------------------------------

describe("projectLiveConnection", () => {
  it("maps initial => connecting", () => {
    const view = projectLiveConnection("initial", "unknown");
    expect(view.status).toBe("connecting");
  });

  it("maps loading => connecting", () => {
    const view = projectLiveConnection("loading", "unknown");
    expect(view.status).toBe("connecting");
  });

  it("maps ready + reachable => connected", () => {
    const view = projectLiveConnection("ready", "reachable");
    expect(view.status).toBe("connected");
  });

  it("maps ready + reconnecting => connected", () => {
    const view = projectLiveConnection("ready", "reconnecting");
    expect(view.status).toBe("connected");
  });

  it("maps ready + unknown => connected", () => {
    const view = projectLiveConnection("ready", "unknown");
    expect(view.status).toBe("connected");
  });

  it("maps ready + unreachable => offline", () => {
    const view = projectLiveConnection("ready", "unreachable");
    expect(view.status).toBe("offline");
  });

  it("maps error => error", () => {
    const view = projectLiveConnection("error", "unknown");
    expect(view.status).toBe("error");
  });

  it("returns a LiveConnectionView with only status", () => {
    const view = projectLiveConnection("ready", "reachable");
    expect(view).toEqual({ status: "connected" } satisfies LiveConnectionView);
  });
});
