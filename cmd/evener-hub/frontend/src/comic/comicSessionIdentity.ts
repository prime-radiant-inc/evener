import type { SessionActivityContext, ThreadModel } from "@evener/appwire-client";

/** Wire sessionId owns the transcript. threadId is a protocol identity, and
 * instanceId fences one daemon lifetime, neither replaces a logical session. */
export function comicSessionRef(model: ThreadModel, context?: SessionActivityContext | null): string {
  const sessionId = model.imageSessionId ?? model.threadId;
  if (context?.sessionId === sessionId) return context.ref;
  return `${model.ref.slice(0, model.ref.indexOf(":") + 1)}${sessionId}`;
}
