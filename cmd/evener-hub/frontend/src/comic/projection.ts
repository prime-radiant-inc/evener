import {
  delegateSendBase,
  delegateSendEarlierResponses,
  delegateSendResponse,
  delegateSendTarget,
  type EntityView,
  entryDisplayKey,
  findEntityView,
  hasItemFailure,
  type ItemModel,
  isDelegateSendResult,
  isParentSteer,
  isTurnError,
  isUserAuthoredSteer,
  itemTextPresence,
  joinedReasoningParagraphs,
  makeTranscriptDisplayConfig,
  parseSteeringNotifications,
  pendingTextJoined,
  projectThread,
  steeringLabel,
  type ThreadModel,
  type TranscriptDisplayConfigV1,
} from "@evener/appwire-client";
import type { ImageGalleryProps } from "../panes/session/transcript/flow/ImageGallery";
import { type ComicPersona, comicDelegatePersona } from "./cast";

export type ComicEntry = {
  id: string;
  sourceItemId: string;
  sourceIndex: number;
  kind: "dialogue" | "thought" | "caption" | "images";
  speaker: "human" | "assistant" | "source";
  sourceLabel: string;
  text: string;
  streaming: boolean;
  persona?: ComicPersona;
  images?: ImageGalleryProps["images"];
};

export type ComicProjectionContext = {
  assistant?: ComicPersona;
  parent?: ComicPersona;
  entities?: ReadonlyMap<string, EntityView>;
};

const ANNA: ComicPersona = { key: "human", character: "anna", name: "Anna", badge: "human" };
const XENO: ComicPersona = { key: "assistant", character: "xeno", name: "Xeno", badge: "assistant" };

// Tool calls must remain available to the source pass for recorded outputImages;
// the comic filters out routine tool text and intents itself, rather than
// changing either workspace preferences or canonical transcript contents.
export const COMIC_TRANSCRIPT_CONFIG: TranscriptDisplayConfigV1 = makeTranscriptDisplayConfig({
  kind: "custom",
  toolIntent: false,
  toolCalls: true,
  reasoning: true,
  expandByDefault: false,
});

function textOf(item: ItemModel): string {
  if (itemTextPresence(item) === "omitted") return "";
  return item.pendingText ? pendingTextJoined(item.pendingText) : item.text;
}

function delegatePersona(
  ownerRef: string,
  delegateId: string | undefined,
  entities: ReadonlyMap<string, EntityView> | undefined,
  transcriptRef?: string,
): ComicPersona | undefined {
  if (!delegateId || !entities) return undefined;
  const view = findEntityView(entities, "delegate", delegateId, ownerRef);
  if (view?.kind !== "delegate" || view.ownerRef !== ownerRef) return undefined;
  if (transcriptRef && view.open.ref !== transcriptRef) return undefined;
  return comicDelegatePersona(view.ownerRef, view.logicalId, view.name);
}

/** A pure, source-ordered view over the shared projection. No history or preference mutation. */
export function projectComicConversation(
  model: ThreadModel,
  context: ComicProjectionContext = {},
): readonly ComicEntry[] {
  const entries: ComicEntry[] = [];
  for (const turn of projectThread(model, COMIC_TRANSCRIPT_CONFIG).turns) {
    for (const projected of turn.entries) {
      if (projected.kind !== "item" && projected.kind !== "critical") continue;
      const item = projected.item;
      const base = {
        id: entryDisplayKey(projected),
        sourceItemId: item.id,
        sourceIndex: projected.sourceIndex,
        streaming: item.status === "inProgress" || item.status === "running",
      };
      if (item.type === "userMessage") {
        entries.push({
          ...base,
          kind: "dialogue",
          speaker: "human",
          sourceLabel: "Human",
          text: textOf(item),
          persona: ANNA,
          images: item.images,
        });
      } else if (item.type === "agentMessage") {
        entries.push({
          ...base,
          kind: "dialogue",
          speaker: "assistant",
          sourceLabel: "Assistant",
          text: textOf(item),
          persona: context.assistant ?? XENO,
        });
      } else if (item.type === "reasoning") {
        // A redacted critical row must never expose summaries, text or a preview.
        const text =
          projected.kind === "critical" && projected.redacted
            ? ""
            : itemTextPresence(item) === "omitted"
              ? ""
              : joinedReasoningParagraphs(item.reasoningSummaries).join("\n\n");
        entries.push({
          ...base,
          kind: "thought",
          speaker: "assistant",
          sourceLabel: "Thought",
          text,
          persona: context.assistant ?? XENO,
        });
      } else if (item.type === "commandExecution") {
        if (item.toolName === "delegate_send" || item.toolName === "job_send_message") {
          const target = delegateSendTarget(item);
          const raw = isDelegateSendResult(item.raw) ? item.raw : undefined;
          const persona = delegatePersona(model.ref, target, context.entities, raw?.transcript_ref);
          const historical = delegateSendEarlierResponses(item).map((result) => result.text);
          const response = delegateSendResponse(item);
          entries.push({
            ...base,
            kind: "caption",
            speaker: "source",
            sourceLabel: persona ? `${persona.name} · ${persona.badge}` : target ? `Delegate ${target}` : "Delegate",
            text: [...historical, response ?? delegateSendBase(item)].join("\n\n"),
            persona,
          });
        } else if (item.outputImages?.length) {
          entries.push({
            ...base,
            kind: "images",
            speaker: "source",
            sourceLabel: item.toolName ?? "Tool",
            text: item.error ?? "",
            images: item.outputImages,
          });
        } else if (hasItemFailure(item)) {
          entries.push({
            ...base,
            kind: "caption",
            speaker: "source",
            sourceLabel: item.toolName ?? "Tool",
            text: item.error ?? item.description ?? "",
          });
        }
      } else if (item.type === "steering") {
        if (isUserAuthoredSteer(item)) {
          entries.push({
            ...base,
            kind: "dialogue",
            speaker: "human",
            sourceLabel: "Human",
            text: textOf(item),
            persona: ANNA,
          });
        } else if (item.steeringKind === "notification") {
          // Parsing runs only for the typed notification kind, not arbitrary text
          // containing XML-looking strings supplied by a human or parent.
          const fragments = parseSteeringNotifications(textOf(item));
          for (const [index, fragment] of fragments.entries()) {
            if (fragment.kind === "notification") {
              const notification = fragment.notification;
              const persona =
                notification.type === "delegate"
                  ? delegatePersona(model.ref, notification.delegateId, context.entities, notification.transcriptRef)
                  : undefined;
              entries.push({
                ...base,
                id: `${base.id}:fragment:${index}`,
                kind: "caption",
                speaker: "source",
                sourceLabel: persona
                  ? `${persona.name} · ${persona.badge}`
                  : notification.type === "delegate"
                    ? `Delegate ${notification.delegateId ?? "(unknown)"}`
                    : notification.title,
                text: notification.message ?? notification.excerpt ?? "",
                persona,
              });
            } else if (fragment.text) {
              entries.push({
                ...base,
                id: `${base.id}:fragment:${index}`,
                kind: "caption",
                speaker: "source",
                sourceLabel: steeringLabel(item.steeringKind, item.source),
                text: fragment.text,
              });
            }
          }
        } else {
          entries.push({
            ...base,
            kind: "caption",
            speaker: "source",
            sourceLabel: steeringLabel(item.steeringKind, item.source),
            text: textOf(item),
            persona: isParentSteer(item.source) ? context.parent : undefined,
          });
        }
      } else if (item.type === "systemMessage") {
        entries.push({
          ...base,
          kind: "caption",
          speaker: "source",
          sourceLabel: item.eventKind ?? "Source",
          text: textOf(item),
        });
      } else if (item.type === "warning") {
        entries.push({
          ...base,
          kind: "caption",
          speaker: "source",
          sourceLabel: item.warning?.source ?? "Warning",
          text: textOf(item),
        });
      }
    }
    // A terminal error can be attached to a routine tool row the comic hid.
    // Keep its typed failure at the source anchor, even after visible dialogue.
    const last = turn.entries.at(-1);
    if (last && isTurnError(turn.source.error)) {
      entries.push({
        id: `${entryDisplayKey(last)}:turn-error`,
        sourceItemId: last.item.id,
        sourceIndex: last.sourceIndex,
        kind: "caption",
        speaker: "source",
        sourceLabel: "Turn error",
        text: turn.source.error.message,
        streaming: false,
      });
    }
  }
  return entries.sort((a, b) => a.sourceIndex - b.sourceIndex);
}
