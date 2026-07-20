// extractSerfErrorInfo pulls the optional data.serfErrorInfo string out of a
// wire error's data payload, mirroring appwire.js's errorFromWire().
function extractSerfErrorInfo(data: unknown): string | undefined {
  if (data && typeof data === "object" && "serfErrorInfo" in data) {
    const value = (data as { serfErrorInfo?: unknown }).serfErrorInfo;
    if (typeof value === "string") return value;
  }
  return undefined;
}

// WireError represents a JSON-RPC-style {code, message, data} error returned
// by the hub over the appwire socket.
export class WireError extends Error {
  readonly code: number;
  readonly data?: unknown;
  readonly serfErrorInfo?: string;

  constructor(message: string, code: number, data?: unknown) {
    super(message);
    this.name = "WireError";
    this.code = code;
    this.data = data;
    const serfErrorInfo = extractSerfErrorInfo(data);
    if (serfErrorInfo !== undefined) this.serfErrorInfo = serfErrorInfo;
  }
}

// RequestTimeoutError is thrown when a request's response doesn't arrive
// within its timeout window.
export class RequestTimeoutError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "RequestTimeoutError";
  }
}

// ConnectionClosedError signals that a request, or the in-flight
// initialize/initialized handshake, was aborted because close() was called —
// as opposed to a server-reported failure (WireError), a request that
// outlived its timeout (RequestTimeoutError), or a connection lost for some
// other reason (a plain Error). Callers that want to distinguish "I closed
// this on purpose" from an unexpected disconnect can check for this type.
export class ConnectionClosedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ConnectionClosedError";
  }
}
