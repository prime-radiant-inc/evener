import type { LiveComposerView, LiveConceptState } from "./contract";
import type {
  ConceptId,
  LiveConnectionView,
  LiveRosterRow,
  LiveRosterView,
  LiveTranscriptItem,
  LiveWorkItem,
} from "./model";

export const CONCEPT_DISPLAY_NAME: Readonly<Record<ConceptId, string>> = {
  stillwater: "Stillwater",
  constellation: "Constellation",
  "field-notes": "Field Notes",
};

export function conceptRootAxLabel(state: LiveConceptState): string {
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

export function transcriptItemAxLabel(item: LiveTranscriptItem): string {
  const status = item.streaming ? "streaming" : "completed";
  let description: string;
  switch (item.kind) {
    case "user":
      description = "Your message";
      break;
    case "assistant":
      description = "Assistant response";
      break;
    case "tool":
      description =
        item.label.trim().toLowerCase() === "reasoning"
          ? "Reasoning"
          : `Tool ${item.label}`;
      break;
    case "question":
      description = "Question";
      break;
    case "failure":
      description = "Error";
      break;
    case "attachment":
      description = "Attachment";
      break;
  }
  return `${description}; ${status}`;
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
