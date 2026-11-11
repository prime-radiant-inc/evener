import { isPlainObject } from "./plainObject";
import type { ThreadHistoryReadResponse } from "./types.gen";
import { WIRE_FIELD_KEYS } from "./types.gen";

export type HistoryServingFacts = Pick<
  ThreadHistoryReadResponse,
  "servedFrom" | "fallbackReason" | "asOf" | "instanceId" | "servingFactsUnavailableReason" | "latestExecutionTurnId"
>;

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
