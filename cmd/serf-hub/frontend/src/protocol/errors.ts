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

// sessionActionHeadline names the step that actually died.
//
// Every session call against a cold session resumes it first (cmd/serf-hub/
// app_session_resume.go's withSessionResume, and app_model.go's
// setThreadModelWithResume and siblings). When the resume is what failed, the
// hub returns the spawner's own raw text and nothing in it says which of the
// two steps died - so naming the action sends someone debugging /goal when
// the daemon simply would not start. `failure` names the action and is used
// only when the action itself is what failed.
//
// Use this where the headline and the detail land in separate slots (an
// EmptyState's title and hint); use sessionActionError for the one-string
// case. Both branch on the same discriminator, so a surface that reports one
// failure twice cannot say two different things.
export function sessionActionHeadline(failure: string, err: unknown): string {
  return isHubLaunchError(err) ? "Couldn't start this session" : failure;
}

// sessionActionError writes the whole failure sentence for a session action:
// the headline sessionActionHeadline picks, then the rejection's own text.
export function sessionActionError(failure: string, err: unknown): string {
  const headline = sessionActionHeadline(failure, err);
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

// GENERIC_ERROR_MESSAGE is friendlyErrorMessage's last resort: anything that
// isn't a WireError (a message the hub itself composed for a person) or a
// recognized client-unreachable rejection gets this, never the error's own
// text - an arbitrary JS exception's message can name a class, a method, a
// file path, none of which means anything to the person looking at it.
const GENERIC_ERROR_MESSAGE = "Something went wrong.";

// HUB_UNREACHABLE_MESSAGE covers the family of rejections AppwireClient (and
// its testing/fakeClient.ts stand-in) throws locally when a request is
// attempted against a socket that isn't open: close() was called, the
// connection dropped, or a call landed before the handshake finished. None
// of that is meaningful to a person - "the hub" is the concept they
// understand, not "the client's internal state".
const HUB_UNREACHABLE_MESSAGE = "Can't reach the hub right now.";

// CLIENT_UNREACHABLE_PATTERN matches the shape both AppwireClient
// (protocol/client.ts's request()/close()) and FakeClient (protocol/testing/
// fakeClient.ts, used throughout the test suite) throw for that family:
// `"<Name>Client: cannot call "<method>" while state is "<state>""`,
// `"<Name>Client: cannot call "<method>"; not connected"`, and
// `"<Name>Client: closed"` (ConnectionClosedError's own message). Matching
// the text rather than a specific class catches plain Error, the FakeClient
// stand-in, AND a bare string, without hard-coding every connectionState
// value - deliberately narrow so it never swallows a genuinely different
// AppwireClient rejection (a socket error, a timeout) that a caller might
// still want to distinguish.
const CLIENT_UNREACHABLE_PATTERN = /^\w*Client: (cannot call ".*"(?: while state is ".*"|; not connected)|closed)$/;

function isClientUnreachableError(error: unknown): boolean {
  if (error instanceof ConnectionClosedError) return true;
  if (error instanceof WireError) return false; // a server-reported error is never this family
  const message = error instanceof Error ? error.message : typeof error === "string" ? error : undefined;
  return message !== undefined && CLIENT_UNREACHABLE_PATTERN.test(message);
}

// friendlyErrorMessage is the one conversion every user-facing error display
// must go through instead of errorText/err.message: a WireError's message
// came from the hub and was written for a person to read, so it survives
// untouched; the client-unreachable family (see CLIENT_UNREACHABLE_PATTERN)
// becomes one plain sentence; everything else - a plain JS exception, a
// timeout, a string, anything this module doesn't otherwise recognize -
// becomes the same generic sentence. Never returns a class name or an
// internal method name.
export function friendlyErrorMessage(error: unknown): string {
  if (error instanceof WireError) {
    const detail = error.message.trim();
    return detail === "" ? GENERIC_ERROR_MESSAGE : detail;
  }
  if (isClientUnreachableError(error)) return HUB_UNREACHABLE_MESSAGE;
  return GENERIC_ERROR_MESSAGE;
}

// DAEMON_MISSING_MESSAGE is what friendlyLaunchErrorMessage shows for the
// daemon-missing family (errorKind's "daemon-missing"): the hub answered,
// but no agent daemon could be reached to serve the request - the first-run
// worst moment (no daemon has ever been started for this project, or the
// one the hub knew about is gone). Distinct from HUB_UNREACHABLE_MESSAGE,
// which is the CLIENT's own connection being down: this is the hub saying
// "I'm here, the daemon isn't", so the fix is different - start one.
const DAEMON_MISSING_MESSAGE =
  "No agent daemon responded for this project. Start one by running `serf` in the repo, then retry.";

// errorKind classifies a rejection into the families a caller with launch
// context (spawn, a model picker) needs to tell apart. Driven by real
// wire/client shapes, not guesses:
//  - "hub-unreachable": the request never reached the hub at all - the
//    CLIENT_UNREACHABLE_PATTERN family friendlyErrorMessage already
//    recognizes (a closed, or not-yet-open, socket).
//  - "daemon-missing": the hub answered, but the AGENT DAEMON for the
//    target project is what failed - isHubLaunchError's own discriminator
//    (data.serfErrorInfo === "hubLaunch", stamped by every launch-check,
//    credential, and daemon-spawn/resume failure - appwire.HubLaunchError,
//    called from cmd/serf-hub/spawn.go, app_threadlifecycle.go, and
//    app_models.go). A WireError only ever exists once the hub is there to
//    write one, so this needs no separate connection-state check: the two
//    families are already mutually exclusive by construction.
//  - "server": any other hub-composed WireError (validation, conflict,
//    etc.) - the hub's own message already says what happened.
//  - "unknown": anything else (a plain JS exception, a timeout, ...).
export type ErrorKind = "hub-unreachable" | "daemon-missing" | "server" | "unknown";

export function errorKind(error: unknown): ErrorKind {
  if (isClientUnreachableError(error)) return "hub-unreachable";
  if (isHubLaunchError(error)) return "daemon-missing";
  if (error instanceof WireError) return "server";
  return "unknown";
}

// The hubLaunch family splits in two (appwire.HubLaunchError call sites in
// cmd/serf-hub): CONFIG failures ("provider credentials missing for ...",
// "model is not configured ...", "... not reported by the launch harness")
// carry their own actionable instructions and must pass through — masking
// them with daemon guidance sends the user to fix the wrong thing (live
// repro: credentialed daemon, uncredentialed default provider). Everything
// else in the family is daemon reachability (launch-check failed/timed
// out/canceled, fork/exec, spawn/resume failures) — raw text a person
// can't act on, which is what the guidance copy is for.
const LAUNCH_CONFIG_MESSAGE_PATTERN =
  /^provider credentials missing for |model is not configured for Serf launch|not reported by the Serf launch harness/;

// friendlyLaunchErrorMessage is friendlyErrorMessage for the two surfaces
// that can hit the daemon-missing family against a cold project (spawn, a
// model picker): identical to friendlyErrorMessage except the
// daemon-reachability half of the hubLaunch family gets
// DAEMON_MISSING_MESSAGE's actionable copy instead of raw launch-check
// text, while the config half keeps its own (already actionable) message.
export function friendlyLaunchErrorMessage(error: unknown): string {
  if (errorKind(error) !== "daemon-missing") return friendlyErrorMessage(error);
  const message = error instanceof WireError ? error.message.trim() : "";
  if (LAUNCH_CONFIG_MESSAGE_PATTERN.test(message)) return message;
  return DAEMON_MISSING_MESSAGE;
}

export type MutationOutcome = "notAccepted" | "unknown" | "targetDeleted";
export type MutationRetryDisposition = "automatic" | "blocked" | "none";

export interface MutationErrorData {
  clientMutationId?: string;
  mutationOutcome?: MutationOutcome;
  retryDisposition?: MutationRetryDisposition;
  cause?: string;
}

// mutationErrorData is the one parser for the retry-safe mutation envelope.
// Only a WireError can carry an authoritative daemon outcome; transport and
// local failures deliberately return undefined so callers retain the outbox
// record rather than guessing whether the mutation applied.
export function mutationErrorData(error: unknown): MutationErrorData | undefined {
  if (!(error instanceof WireError) || !error.data || typeof error.data !== "object") return undefined;
  const data = error.data as Record<string, unknown>;
  const mutationOutcome = data.mutationOutcome;
  const retryDisposition = data.retryDisposition;
  return {
    clientMutationId: typeof data.clientMutationId === "string" ? data.clientMutationId : undefined,
    mutationOutcome:
      mutationOutcome === "notAccepted" || mutationOutcome === "unknown" || mutationOutcome === "targetDeleted"
        ? mutationOutcome
        : undefined,
    retryDisposition:
      retryDisposition === "automatic" || retryDisposition === "blocked" || retryDisposition === "none"
        ? retryDisposition
        : undefined,
    cause: typeof data.cause === "string" ? data.cause : undefined,
  };
}
