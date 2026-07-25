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

// errorText flattens a rejected value to the text worth showing. It is the
// one definition of a conversion the whole app needs: every caller that
// reports a failure to the user starts here.
export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// isHubLaunchError reports whether a rejection is the hub failing to start a
// session's daemon: appwire.HubLaunchError, which stamps data.serfErrorInfo
// "hubLaunch" (appwire/errors.go). The discriminator is that string, never
// the code - siblings share the code.
export function isHubLaunchError(err: unknown): boolean {
  return err instanceof WireError && err.serfErrorInfo === "hubLaunch";
}

// sessionActionError writes the failure sentence for a session mutation,
// naming the step that actually died.
//
// Every mutation against a cold session resumes it first (cmd/serf-hub/
// app_session_resume.go's withSessionResume, and app_model.go's
// setThreadModelWithResume and siblings). When the resume is what failed, the
// hub returns the spawner's own raw text and nothing in it says which of the
// two steps died - so naming the mutation sends someone debugging /goal when
// the daemon simply would not start. `failure` names the mutation and is used
// only when the mutation itself is what failed.
export function sessionActionError(failure: string, err: unknown): string {
  const headline = isHubLaunchError(err) ? "Couldn't start this session" : failure;
  const detail = errorText(err).trim();
  return detail ? `${headline}: ${detail}` : headline;
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
