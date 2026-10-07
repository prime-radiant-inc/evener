// The automatic memory refresh's structured payload, shared by the web and
// native clients. The daemon records each refresh as a systemMessage with the
// typed eventKind "memory-context" whose Text is the model-facing envelope
// (core framing plus a Go-quoted index). A current daemon also attaches the
// decoded observation on raw.memoryContext; this module validates that shape
// for the renderers and the grouping rule. No React, no platform access, so
// both clients consume the same validation rather than maintaining two
// decoders.
//
// The shape is a frozen incoming contract:
//   raw = { memoryContext: { scope, state, truncated, content } }
// scope is personal|project|session, state is
// current|missing|revoked|unavailable, truncated a boolean, and content the
// decoded index (empty for a current observation with nothing recorded). A raw
// that does not match stays undecoded: the renderer falls back to the complete
// original Text rather than manufacturing index data.

/** The systemMessage event kind of an automatic memory refresh. */
export const MEMORY_CONTEXT_EVENT_KIND = "memory-context";

/** The collapsed heading both clients show for a refresh. */
export const MEMORY_CONTEXT_LABEL = "Refreshed my memory";

// "session" stays so transcripts from earlier builds keep their scope label.
export const MEMORY_CONTEXT_SCOPES = ["personal", "project", "session"] as const;
export type MemoryContextScope = (typeof MEMORY_CONTEXT_SCOPES)[number];

export const MEMORY_CONTEXT_STATES = ["current", "missing", "revoked", "unavailable"] as const;
export type MemoryContextState = (typeof MEMORY_CONTEXT_STATES)[number];

export interface MemoryContextObservation {
  readonly scope: MemoryContextScope;
  readonly state: MemoryContextState;
  readonly truncated: boolean;
  readonly content: string;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isScope(value: unknown): value is MemoryContextScope {
  return typeof value === "string" && (MEMORY_CONTEXT_SCOPES as readonly string[]).includes(value);
}

function isState(value: unknown): value is MemoryContextState {
  return typeof value === "string" && (MEMORY_CONTEXT_STATES as readonly string[]).includes(value);
}

/** Validates raw.memoryContext, returning the observation or undefined. Every
 * field must be present and correctly typed; unknown extra fields are ignored
 * so a future additive field does not force the fallback. */
export function parseMemoryContext(raw: unknown): MemoryContextObservation | undefined {
  if (!isRecord(raw)) return undefined;
  const observation = raw.memoryContext;
  if (!isRecord(observation)) return undefined;
  const { scope, state, truncated, content } = observation;
  if (!isScope(scope) || !isState(state) || typeof truncated !== "boolean" || typeof content !== "string") {
    return undefined;
  }
  return { scope, state, truncated, content };
}

const SCOPE_LABELS: Record<MemoryContextScope, string> = {
  personal: "Personal memory",
  project: "Project memory",
  session: "Session memory",
};

/** The scope's display name, the same words on the collapsed meta line. */
export function memoryContextScopeLabel(scope: MemoryContextScope): string {
  return SCOPE_LABELS[scope];
}

/** The state's display word. "current" reads as the successful read it is. */
export function memoryContextStateLabel(state: MemoryContextState): string {
  return state;
}

/** The empty-content note distinguishes the states without inventing data: an
 * empty current index is a real, successful read of nothing. */
export function memoryContextEmptyText(state: MemoryContextState): string {
  switch (state) {
    case "current":
      return "Empty index";
    case "missing":
      return "No memory index";
    case "revoked":
      return "Memory access revoked";
    case "unavailable":
      return "Memory index unavailable";
  }
}
