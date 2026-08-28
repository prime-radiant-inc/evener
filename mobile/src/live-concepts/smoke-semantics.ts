import type { LiveComposerView, LiveConceptState } from "./contract";
import type {
  ConceptId,
  LiveActivityView,
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
  const parts = [
    "Evener concept",
    `concept ${CONCEPT_DISPLAY_NAME[state.concept]}`,
    `surface ${state.surface}`,
    `connection ${state.connection.status}`,
  ];
  const evidence = state.connection.evidence;
  if (evidence !== undefined) {
    parts.push(
      `server ${evidence.serverVersion}`,
      `protocol ${evidence.protocolVersion}`,
      `profile generation ${evidence.profileGeneration}`,
      `lifecycle ${evidence.lifecyclePhase} ${evidence.lifecycleGeneration}`,
      `handshake ${evidence.handshakeGeneration}`,
      `app ${evidence.bundleId} ${evidence.appVersion}`,
      `origin ${evidence.originDigest}`,
    );
  }
  if (state.conversation !== null) {
    parts.push(`session ${state.conversation.threadKey}`);
  }
  return parts.join("; ");
}

export function rosterAxLabel(
  concept: ConceptId,
  roster: LiveRosterView,
): string {
  const retained = roster.groups.reduce(
    (count, group) => count + group.rows.length,
    0,
  );
  return `Evener roster; concept ${CONCEPT_DISPLAY_NAME[concept]}; retained ${retained}; has-more ${String(roster.hasMore)}`;
}

export function rosterRowAxLabel(row: LiveRosterRow): string {
  return `Session ${row.key}; ${row.title}; project ${row.project}; status ${row.tone}`;
}

export function transcriptItemAxLabel(item: LiveTranscriptItem): string {
  const kind =
    item.kind === "tool" && item.label.trim().toLowerCase() === "reasoning"
      ? "reasoning"
      : item.kind;
  const status = item.streaming ? "streaming" : "completed";
  return `Evener transcript item; id ${item.key}; kind ${kind}; status ${status}; label ${item.label}; content ${item.body}`;
}

export function mutationAxLabel(composer: LiveComposerView): string | null {
  if (composer.pending !== null) {
    return `Evener mutation; kind ${composer.pending.kind}; status ${composer.pending.status}; receipt none`;
  }
  const accepted = composer.accepted ?? null;
  if (accepted !== null) {
    return `Evener mutation; kind ${accepted.kind}; status accepted; receipt ${accepted.receipt}`;
  }
  return null;
}

export function workItemAxLabel(item: LiveWorkItem): string {
  return `Evener work item; id ${item.key}; kind ${item.kind}; status ${item.tone}; title ${item.title}`;
}

export function usageAxLabel(usage: LiveActivityView["usage"]): string {
  const tokens =
    usage.totalTokens === undefined
      ? "none"
      : usage.totalTokens.toLocaleString("en-US");
  const duration =
    usage.durationMs === undefined
      ? "none"
      : usage.durationMs >= 60_000
        ? `${Math.round(usage.durationMs / 60_000)}m`
        : `${Math.round(usage.durationMs / 1_000)}s`;
  const context =
    usage.contextPressure === undefined
      ? "none"
      : `${Math.round(usage.contextPressure * 100)}%`;
  return `Evener usage; tokens ${tokens}; cost ${usage.cost ?? "none"}; duration ${duration}; context ${context}`;
}
