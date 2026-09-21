// extractEvenerErrorInfo pulls the optional data.evenerErrorInfo string out of a
// wire error's data payload, mirroring appwire.js's errorFromWire().
function extractEvenerErrorInfo(data: unknown): string | undefined {
  if (data && typeof data === "object" && "evenerErrorInfo" in data) {
    const value = (data as { evenerErrorInfo?: unknown }).evenerErrorInfo;
    if (typeof value === "string") return value;
  }
  return undefined;
}

// WireError represents a JSON-RPC-style {code, message, data} error returned
// by the hub over the appwire socket.
export class WireError extends Error {
  readonly code: number;
  readonly data?: unknown;
  readonly evenerErrorInfo?: string;

  constructor(message: string, code: number, data?: unknown) {
    super(message);
    this.name = "WireError";
    this.code = code;
    this.data = data;
    const evenerErrorInfo = extractEvenerErrorInfo(data);
    if (evenerErrorInfo !== undefined) this.evenerErrorInfo = evenerErrorInfo;
  }
}

// ErrorInvalidHostField is the hub's discriminator for a host mutation's
// validation refusal, whose data names the input that failed
// (appwire.ErrorInvalidHostField, appwire/errors.go). It shares its code with
// every other validation refusal, so the field is only ever read off this
// discriminant. The binding test (errors.test.ts) reads the Go constant.
export const ErrorInvalidHostField = "invalidHostField";

// hostFieldError returns the input a host-mutation refusal blames, or undefined
// for any other rejection — including a refusal that blames the entry as a
// whole, which carries no field. The returned name is the wire spelling the
// dialog's own inputs use, so a caller maps it onto an input directly.
export function hostFieldError(error: unknown): string | undefined {
  return wireRejectionPayload(error, ErrorInvalidHostField, "field", (value) =>
    typeof value === "string" && value !== "" ? value : undefined,
  );
}

// errorText flattens a rejected value to the text worth showing. It is the
// one definition of a conversion the whole app needs: every caller that
// reports a failure to the user starts here.
export function errorText(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

// isHubLaunchError reports whether a rejection is the hub failing to start a
// session's daemon: appwire.HubLaunchError, which stamps data.evenerErrorInfo
// "hubLaunch" (appwire/errors.go). The discriminator is that string, never
// the code - siblings share the code.
export function isHubLaunchError(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === "hubLaunch";
}

export function isStaleCursorError(error: unknown): boolean {
  return error instanceof WireError && error.evenerErrorInfo === "transcriptItemCursorStale";
}

/** Extracts a payload the hub attached to a rejection under `key` when the
 * rejection is the `evenerErrorInfo` kind named, decoded by `decode`
 * (undefined for a malformed or absent payload, the same as a rejection of a
 * different kind). Every settings-hub store's conflict/post-apply rejection
 * shares this shape (a durable failure whose write already landed carries the
 * hub's canonical state under "applied"; a lost revision race carries it
 * under "current") - only the payload's own decoder differs between stores.
 * The discriminator is the string, never the code - siblings share a code. */
export function wireRejectionPayload<T>(
  error: unknown,
  info: string,
  key: string,
  decode: (value: unknown) => T | undefined,
): T | undefined {
  if (!(error instanceof WireError) || error.evenerErrorInfo !== info) return undefined;
  if (!error.data || typeof error.data !== "object") return undefined;
  return decode((error.data as Record<string, unknown>)[key]);
}

// ErrorInstanceRenamePersisted is the hub's discriminator for a provider-instance
// rename that APPLIED before its credential move or reload failed
// (appwire.ErrorInstanceRenamePersisted, appwire/errors.go); the hub's message
// names the credential left behind, and clients steer to the new name rather than
// report a failed save. The binding test (errors.test.ts) reads the Go constant,
// so a hub-side rename breaks there instead of silently leaving the standing
// rename read as a plain failure.
export const ErrorInstanceRenamePersisted = "instanceRenamePersisted";

// isInstanceRenamePersisted reports whether a rejection is the hub reporting a
// provider-instance rename that APPLIED before its credential move or reload
// failed (ErrorInstanceRenamePersisted above). The discriminator is that
// string, never the code - siblings share the code - so this is the one
// definition every client matches against.
export function isInstanceRenamePersisted(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === ErrorInstanceRenamePersisted;
}

// ErrorInstanceRemoveApplied is the hub's discriminator for a provider-instance
// removal that APPLIED before a later step failed
// (appwire.ErrorInstanceRemoveApplied, appwire/errors.go): the instance's
// credential deletion (or its config entry) reached the store, so the removal
// stands and the hub's message names what was left behind. Clients close the
// confirmation, re-read the listing, and drop any state retained for the name
// rather than report a failed remove whose retry targets a missing instance.
// The binding test (errors.test.ts) reads the Go constant, so a hub-side rename
// breaks there instead of silently leaving the standing removal read as a plain
// failure.
export const ErrorInstanceRemoveApplied = "instanceRemoveApplied";

// isInstanceRemoveApplied reports whether a rejection is the hub reporting a
// provider-instance removal that APPLIED before a later step failed
// (ErrorInstanceRemoveApplied above). The discriminator is that string, never
// the code - siblings share the code - so this is the one definition every
// client matches against.
export function isInstanceRemoveApplied(err: unknown): boolean {
  return err instanceof WireError && err.evenerErrorInfo === ErrorInstanceRemoveApplied;
}

// ErrorEndpointConflict is the hub's discriminant for a refusal of an asserted
// destination (appwire.ErrorEndpointConflict, appwire/errors.go): the name no
// longer resolves to the endpoint the client showed the user. It shares
// CodeConflict with genuine conflicts (a name collision, an expired flow), so
// matching the code would read a create collision as a moved endpoint; this
// string is the one definition every credential flow matches against. The
// binding test (errors.test.ts) reads the Go constant.
export const ErrorEndpointConflict = "endpointConflict";

// ErrorMarketplaceRemoveApplied is the hub's discriminant for a marketplace
// removal that APPLIED - the unregister and its clone cleanup both completed
// - but whose response could not carry the updated list, because the fresh
// read that builds it failed (appwire.ErrorMarketplaceRemoveApplied,
// appwire/errors.go). The marker alone means the removal stands: a retry
// finds ErrMarketplaceNotFound, so consumers report neutrally and reconcile
// through their normal fetch path, never a clone-litter warning and never a
// retry. The binding test (errors.test.ts) reads the Go constant, so a
// hub-side rename breaks there instead of silently leaving the standing
// removal read as a failed one.
export const ErrorMarketplaceRemoveApplied = "marketplaceRemoveApplied";

// sessionActionHeadline names the step that actually died.
//
// Every session call against a cold session resumes it first (cmd/evener-hub/
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

// ClientNotReadyError signals that a caller-side bounded wait for the
// client to become ready (stores/threads.ts's requireReadyClient, issue
// #195's RCA) exhausted its timeout without the client ever reaching
// "ready". Deliberately distinct from the text AppwireClient.request()
// throws synchronously for a non-ready call ("cannot call ... while state
// is ...") - that text means "rejected immediately, never even waited";
// this one only fires after genuinely waiting out the budget, so a caller
// (or a toast) can tell "still trying" apart from "gave up". Classified as
// "hub-unreachable" by isClientUnreachableError below: from the person
// looking at the screen, a client that never got ready within budget IS
// the hub being unreachable.
export class ClientNotReadyError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ClientNotReadyError";
  }
}

// GENERIC_ERROR_MESSAGE is friendlyErrorMessage's last resort: anything that
// isn't a WireError (a message the hub itself composed for a person) or a
// recognized client-unreachable rejection gets this, never the error's own
// text - an arbitrary JS exception's message can name a class, a method, a
// file path, none of which means anything to the person looking at it.
export const GENERIC_ERROR_MESSAGE = "Something went wrong.";

// HUB_UNREACHABLE_MESSAGE covers the family of rejections AppwireClient (and
// its testing/fakeClient.ts stand-in) throws locally when a request is
// attempted against a socket that isn't open: close() was called, the
// connection dropped, or a call landed before the handshake finished. None
// of that is meaningful to a person - "the hub" is the concept they
// understand, not "the client's internal state".
export const HUB_UNREACHABLE_MESSAGE = "Can't reach the hub right now.";

// CLIENT_UNREACHABLE_PATTERN matches the shape both AppwireClient
// (appwire-client/typescript/client.ts's request()/close()) and FakeClient
// (appwire-client/typescript/testing/fakeClient.ts, used throughout the test
// suite) throw for that family:
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
  if (error instanceof ConnectionClosedError || error instanceof ClientNotReadyError) return true;
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
  "No agent daemon responded for this project. Start one by running evener in the repo, then retry.";

// errorKind classifies a rejection into the families a caller with launch
// context (spawn, a model picker) needs to tell apart. Driven by real
// wire/client shapes, not guesses:
//  - "hub-unreachable": the request never reached the hub at all - the
//    CLIENT_UNREACHABLE_PATTERN family friendlyErrorMessage already
//    recognizes (a closed, or not-yet-open, socket).
//  - "daemon-missing": the hub answered, but the AGENT DAEMON for the
//    target project is what failed - isHubLaunchError's own discriminator
//    (data.evenerErrorInfo === "hubLaunch", stamped by every launch-check,
//    credential, and daemon-spawn/resume failure - appwire.HubLaunchError,
//    called from cmd/evener-hub/spawn.go, app_threadlifecycle.go, and
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

// Within the hubLaunch family (appwire.HubLaunchError, stamped from
// cmd/evener-hub/spawn.go, app_threadlifecycle.go, and app_models.go), almost
// every message carries its own diagnosis —
// config failures ("provider credentials missing for ..."), resume advice
// (resumeFailureError's kill-the-old-daemon instructions), and crucially
// the daemon's own redacted stderr ("evener launch-check failed: <stderr>",
// which cmd/evener-hub/app_rpc_test.go's stderr-propagation test exists to
// keep intact end to end). Masking those with generic guidance sends the
// user to fix the wrong thing, so the DEFAULT is pass-through, and only
// the messages that genuinely contain no diagnosis — the daemon never got
// far enough to produce one — take the guidance copy: launch-check
// canceled/timed out (no output existed) and fork/exec (the evener binary
// itself is absent). A new Go message therefore defaults to being shown,
// not swallowed.
const LAUNCH_NO_DIAGNOSIS_PATTERN = /^evener launch-check (?:canceled|timed out)$|^fork\/exec /;

// friendlyLaunchErrorMessage is friendlyErrorMessage for the surfaces that
// can hit the hubLaunch family against a cold project (spawn, a model
// picker): identical to friendlyErrorMessage except the no-diagnosis
// subset above gets DAEMON_MISSING_MESSAGE's actionable copy; every other
// hubLaunch message passes through with its own instructions.
export function friendlyLaunchErrorMessage(error: unknown): string {
  if (errorKind(error) !== "daemon-missing") return friendlyErrorMessage(error);
  const message = error instanceof WireError ? error.message.trim() : "";
  if (message === "" || LAUNCH_NO_DIAGNOSIS_PATTERN.test(message)) return DAEMON_MISSING_MESSAGE;
  return message;
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
