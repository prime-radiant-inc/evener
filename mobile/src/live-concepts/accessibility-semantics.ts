import type { LiveComposerView, QuestionDraft } from "./contract";
import type {
  ConceptId,
  ConversationDisplayItem,
  LiveConceptSurface,
  LiveConnectionView,
  LiveRosterRow,
  LiveRosterView,
  LiveWorkItem,
} from "./model";

export interface ConceptRootSemanticState {
  readonly concept: ConceptId;
  readonly surface: LiveConceptSurface;
}

export const CONCEPT_DISPLAY_NAME: Readonly<Record<ConceptId, string>> = {
  stillwater: "Stillwater",
  constellation: "Constellation",
  "field-notes": "Field Notes",
};

export function conceptRootAxLabel(state: ConceptRootSemanticState): string {
  return `${CONCEPT_DISPLAY_NAME[state.concept]} ${state.surface}`;
}

export function connectionStatusAxLabel(
  connection: LiveConnectionView,
): string {
  if (connection.status === "connected" && connection.evidence !== undefined) {
    const evidence = connection.evidence;
    return `Connected to ${evidence.serverName} ${evidence.serverVersion}; protocol ${evidence.protocolVersion}; app version ${evidence.appVersion}`;
  }
  switch (connection.status) {
    case "connected":
      return "Connected";
    case "connecting":
      return "Connecting";
    case "reconnecting":
      return "Reconnecting";
    case "offline":
      return "Offline";
    case "error":
      return "Connection error";
  }
}

export function rosterAxLabel(
  concept: ConceptId,
  roster: LiveRosterView,
): string {
  const retained = roster.groups.reduce(
    (count, group) => count + group.rows.length,
    0,
  );
  const availability = roster.hasMore ? "more available" : "complete list";
  return `${CONCEPT_DISPLAY_NAME[concept]} sessions; ${retained} sessions; ${availability}`;
}

export function rosterRowAxLabel(row: LiveRosterRow): string {
  return `Open ${row.title}; status ${row.tone}`;
}

export function conversationItemAxLabel(
  item: ConversationDisplayItem,
  questionResolution: QuestionDraft["resolution"],
): string {
  switch (item.sourceKind) {
    case "user":
      return "Your message; completed";
    case "assistant":
      return item.streaming
        ? "Assistant message; streaming"
        : "Assistant message; completed";
    case "question":
      return questionResolution === null
        ? "Question; response required"
        : "Question; resolved";
    case "failure":
      return "Failure; action required";
    case "notice":
    case "system":
    case "reasoning":
    case "tool":
    case "attachment":
    case "diagnostic":
    case "unknown":
      return markerAxLabel(item);
  }
}

export function conversationItemAxDescription(
  item: ConversationDisplayItem,
): string | null {
  return "preview" in item ? (item.preview?.text ?? null) : null;
}

function markerAxLabel(
  item: Extract<ConversationDisplayItem, { semanticKind: string }>,
): string {
  switch (item.semanticKind) {
    case "notice":
      return "Notice; informational";
    case "warning-notice":
      return "Notice; attention required";
    case "system-context":
      return "System context; details hidden";
    case "system-activity":
      return "System activity; informational";
    case "reasoning":
      return `Reasoning activity; ${item.state}`;
    case "tool":
      return `Tool activity, ${item.label.text}; ${item.state}`;
    case "attachment":
      return `Attachment; ${item.state}`;
    case "activity":
      return `Activity; ${item.state}`;
  }
}

export function streamingAnnouncement(
  previous: ConversationDisplayItem | null,
  next: ConversationDisplayItem,
): string | null {
  if (next.sourceKind !== "assistant") return null;
  if (
    previous?.sourceKind === "assistant" &&
    previous.streaming !== next.streaming
  ) {
    return next.streaming
      ? "Assistant message streaming"
      : "Assistant message completed";
  }
  if (!next.streaming || previous?.sourceKind !== "assistant") return null;
  if (previous.body.text === next.body.text) return null;
  return /[.!?\n]$/u.test(next.body.text) ? next.body.text : null;
}

export function mutationAxLabel(composer: LiveComposerView): string | null {
  if (composer.pending !== null) {
    return `${titleCase(composer.pending.kind)} ${composer.pending.status}`;
  }
  const accepted = composer.accepted ?? null;
  if (accepted !== null) {
    return `${titleCase(accepted.kind)} ${accepted.disposition} by Hub`;
  }
  return null;
}

export function workItemAxLabel(item: LiveWorkItem): string {
  return `${titleCase(item.kind)} ${item.title}; status ${item.tone}`;
}

export function usageAxLabel(): string {
  return "Usage summary";
}

function titleCase(value: string): string {
  return value.length === 0
    ? value
    : `${value.charAt(0).toUpperCase()}${value.slice(1)}`;
}
