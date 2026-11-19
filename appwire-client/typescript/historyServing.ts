import { isConfirmedAbsence } from "./bootGeneration";
import { isPlainObject } from "./plainObject";
import type { ThreadHistoryReadResponse } from "./types.gen";
import { WIRE_FIELD_KEYS } from "./types.gen";

export type HistoryServingFacts = Pick<
  ThreadHistoryReadResponse,
  "servedFrom" | "fallbackReason" | "asOf" | "instanceId" | "servingFactsUnavailableReason" | "latestExecutionTurnId"
>;

/**
 * Anything that may carry serving facts: a read response, a page response, or
 * a model's stored facts. Read structurally, like every other helper here, so
 * a legacy wire shape that carries none of the six fields is accepted too -
 * that absence is exactly what `authoritativeSourceOf` has to see.
 */
export type HistoryServingFactsInput = object;

export const HISTORY_SERVING_FACT_KEYS = [
  "servedFrom",
  "fallbackReason",
  "asOf",
  "instanceId",
  "servingFactsUnavailableReason",
  "latestExecutionTurnId",
] as const;

export function historyServingFacts(value: object): HistoryServingFacts | undefined {
  const response = value as HistoryServingFacts;
  if (response.servedFrom === undefined && response.servingFactsUnavailableReason === undefined) return undefined;
  return Object.fromEntries(
    HISTORY_SERVING_FACT_KEYS.filter((key) => response[key] !== undefined).map((key) => [key, response[key]]),
  );
}

export function historyFallback(value: object | undefined): boolean {
  const facts = value as HistoryServingFacts | undefined;
  return facts?.servedFrom === "saved-index" && facts.fallbackReason !== undefined;
}

// historyAnsweredLive reports whether a saved-index fallback was ENRICHED by a
// daemon that answered: the live read succeeded and supplied the transcript
// identity, and the saved index supplied only the rows the live window could
// not carry (`live-read-empty`).
//
// That distinction is an ownership question, not a serving one. Every fallback
// suppresses the boot token for HISTORY VALIDITY - the rows came from the
// saved index, so they cannot stand as evidence about the source. But on an
// enriched response the token is still the answering daemon's own account of
// itself (`fallbackHistoryIdentity` keeps `live.BootGeneration`,
// cmd/evener-hub/app_rpc.go:372-380), so it is the only evidence a client has
// about who owns the hold. Dropping it there lets that daemon's next history
// update clear the live hold its own read just supplied.
//
// `live-read-failed` does not qualify: the live read that made the fallback
// necessary failed, so its token is no claim about who is serving the thread.
export function historyAnsweredLive(value: object | undefined): boolean {
  const facts = value as HistoryServingFacts | undefined;
  return facts?.servedFrom === "saved-index" && facts.fallbackReason === "live-read-empty";
}

/**
 * The source a response is EVIDENCE about — the only thing a hold hand-off or
 * a replay-suppression decision may act on. Empty means the response
 * establishes no source.
 *
 * A saved-index fallback speaks only to history validity (its rows came from
 * the saved index), so it names a source only when a daemon answered it
 * (`live-read-empty`, see `historyAnsweredLive`).
 *
 * An unqualified `daemonless` is the hub's own stamp for "I answered from the
 * transcript". On `thread/read` and `thread/turns/list` it covers a relay
 * outage, a protocol refusal and a deadline after a live read failed as well
 * as a real absence, so without serving facts there is nothing that makes it
 * a claim about who serves the thread: it may neither retire a hold nor
 * become one's recorded owner. With serving facts the hub has said how it was
 * served, and the token stands as any other authoritative answer.
 */
export function authoritativeSourceOf(
  facts: HistoryServingFactsInput | undefined,
  bootGeneration: string | undefined,
): string {
  if (historyFallback(facts)) return historyAnsweredLive(facts) ? (bootGeneration ?? "") : "";
  const servedFrom = (facts as HistoryServingFacts | undefined)?.servedFrom;
  if (isConfirmedAbsence(bootGeneration) && servedFrom === undefined) return "";
  return bootGeneration ?? "";
}

// Legacy responses retain their old authoritative semantics. Only the new
// methods carry a provenance marker that changes replacement behavior.
export function historyReplacesWindow(value: object): boolean {
  const facts = value as HistoryServingFacts & { authoritative?: boolean };
  if (facts.servedFrom === undefined) return facts.authoritative === true;
  return (
    facts.servedFrom === "live-daemon" || (facts.servedFrom === "saved-index" && facts.fallbackReason === undefined)
  );
}

export function validHistoryServingFacts(value: unknown): boolean {
  if (!isPlainObject(value)) return false;
  if (
    value.servedFrom === undefined &&
    (typeof value.servingFactsUnavailableReason !== "string" || value.servingFactsUnavailableReason.length === 0)
  )
    return false;
  if (value.servedFrom !== undefined && value.servedFrom !== "live-daemon" && value.servedFrom !== "saved-index")
    return false;
  if (
    value.fallbackReason !== undefined &&
    (value.servedFrom !== "saved-index" ||
      (value.fallbackReason !== "live-read-failed" && value.fallbackReason !== "live-read-empty"))
  )
    return false;
  if (
    value.servedFrom !== undefined &&
    (typeof value.asOf !== "string" ||
      !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value.asOf) ||
      !Number.isFinite(Date.parse(value.asOf)))
  )
    return false;
  if (value.instanceId !== undefined && (value.servedFrom !== "live-daemon" || typeof value.instanceId !== "string"))
    return false;
  for (const key of ["asOf", "latestExecutionTurnId", "servingFactsUnavailableReason"] as const) {
    if (value[key] !== undefined && typeof value[key] !== "string") return false;
  }
  return true;
}

export function decodeHistoryServingResponse(value: unknown, page: boolean): unknown {
  const keys: readonly string[] = page
    ? WIRE_FIELD_KEYS.ThreadHistoryTurnsListResponse
    : WIRE_FIELD_KEYS.ThreadHistoryReadResponse;
  if (
    !isPlainObject(value) ||
    Object.keys(value).some((key) => !keys.includes(key)) ||
    !validHistoryServingFacts(value) ||
    (page ? !Array.isArray(value.data) : !isPlainObject(value.thread))
  ) {
    throw new TypeError("Invalid history serving response");
  }
  return value;
}
