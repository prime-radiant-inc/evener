// WebSocketLike is the minimal socket surface AppwireClient depends on. Its
// settable onopen/onmessage/onclose/onerror handlers mirror the assignable
// properties of the browser WebSocket API, so a real WebSocket satisfies it
// structurally at runtime while tests substitute a fake (see testing/fakeSocket.ts).
export interface WebSocketLike {
  send(data: string): void;
  close(code?: number): void;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: unknown }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: (() => void) | null;
}

// rpcURLFromLocation builds the appwire RPC endpoint URL from a
// window.location-shaped object, upgrading http(s) to the matching ws(s)
// scheme. Mirrors appwire.js's rpcURL().
export function rpcURLFromLocation(loc: { protocol: string; host: string }): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}/rpc`;
}
