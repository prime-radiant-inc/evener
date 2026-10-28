import { isValidTranscriptRef, type SessionActivityContext, type ThreadModel } from "@evener/appwire-client";

/** Wire sessionId owns the transcript. threadId is a protocol identity, and
 * instanceId fences one daemon lifetime, neither replaces a logical session. */
export function comicSessionRef(model: ThreadModel, context?: SessionActivityContext | null): string {
  const sessionId = model.imageSessionId ?? model.threadId;
  if (context?.sessionId === sessionId) return context.ref;
  return `${model.ref.slice(0, model.ref.indexOf(":") + 1)}${sessionId}`;
}

/** Literal payloads retain source-local refs. Qualify only their comparison
 * identity, using the host that served the transcript, not activity context. */
export function comicTranscriptRef(model: ThreadModel, ref: string | undefined): string | undefined {
  if (!ref?.startsWith("local:") || !isValidTranscriptRef(ref) || !isValidTranscriptRef(model.ref)) return ref;
  return `${model.ref.slice(0, model.ref.indexOf(":") + 1)}${ref.slice("local:".length)}`;
}
