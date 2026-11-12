import { FakeSocket } from "./fakeSocket";

// These fixtures script the legacy history methods. An older hub refuses the
// versioned methods explicitly, so the real client's degradation path can run.
export function replyLegacyHistoryProbe(
  socket: { receive(frame: Record<string, unknown>): void },
  request: { id?: unknown; method?: string },
): boolean {
  if (
    request.id == null ||
    (request.method !== "evener/thread/history/read" && request.method !== "evener/thread/history/turns/list")
  ) {
    return false;
  }
  socket.receive({ id: request.id, error: { code: -32601, message: "fixture-method-unavailable" } });
  return true;
}

export class LegacyHistorySocket extends FakeSocket {
  override send(data: string): void {
    super.send(data);
    replyLegacyHistoryProbe(this, JSON.parse(data));
  }
}
