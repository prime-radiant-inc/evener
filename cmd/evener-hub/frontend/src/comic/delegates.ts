import {
  type DelegateEntityView,
  type EntityView,
  findEntityView,
  parseSteeringNotifications,
  pendingTextJoined,
  projectThread,
  type SessionActivityContext,
  type ThreadModel,
} from "@evener/appwire-client";
import { delegateStableState } from "../panes/session/transcript/tools/subagentModuleStore";
import { type ComicPersona, comicDelegateKey, comicDelegatePersona } from "./cast";
import { COMIC_TRANSCRIPT_CONFIG, type ComicProjectionContext } from "./projection";

export type ComicDelegateStrip = {
  key: string;
  ownerRef: string;
  delegateId: string;
  childRef: string;
  persona: ComicPersona;
  entity: DelegateEntityView;
  source?: { itemId: string; sourceIndex: number };
};

/** Activity owns membership and current state, transcript evidence owns placement only. */
export function projectComicDelegates(
  model: ThreadModel,
  entities: ReadonlyMap<string, EntityView>,
): readonly ComicDelegateStrip[] {
  const strips = new Map<string, ComicDelegateStrip>();
  for (const entity of entities.values()) {
    if (entity.kind !== "delegate" || entity.ownerRef !== model.ref || !entity.open.ref) continue;
    if (findEntityView(entities, "delegate", entity.logicalId, model.ref) !== entity) continue;
    const key = comicDelegateKey(entity.ownerRef, entity.logicalId);
    strips.set(entity.logicalId, {
      key,
      ownerRef: entity.ownerRef,
      delegateId: entity.logicalId,
      childRef: entity.open.ref,
      persona: comicDelegatePersona(entity.ownerRef, entity.logicalId, entity.name),
      entity,
    });
  }
  const place = (id: string | undefined, childRef: string | undefined, itemId: string, sourceIndex: number) => {
    const strip = id ? strips.get(id) : undefined;
    if (!strip || (childRef && childRef !== strip.childRef)) return;
    if (!strip.source || strip.source.sourceIndex > sourceIndex) strip.source = { itemId, sourceIndex };
  };
  for (const turn of projectThread(model, COMIC_TRANSCRIPT_CONFIG).turns) {
    for (const entry of turn.entries) {
      if (entry.kind !== "item" && entry.kind !== "critical") continue;
      const item = entry.item;
      if (item.type === "commandExecution" && item.toolName === "delegate") {
        const { parsed, stable } = delegateStableState(item, false, model.ref, model, entities);
        if (stable && typeof parsed?.delegate_id === "string")
          place(
            parsed.delegate_id,
            typeof parsed.transcript_ref === "string" ? parsed.transcript_ref : undefined,
            item.id,
            entry.sourceIndex,
          );
      } else if (item.type === "steering" && item.steeringKind === "notification") {
        for (const fragment of parseSteeringNotifications(
          item.pendingText ? pendingTextJoined(item.pendingText) : item.text,
        )) {
          if (fragment.kind === "notification" && fragment.notification.type === "delegate") {
            place(fragment.notification.delegateId, fragment.notification.transcriptRef, item.id, entry.sourceIndex);
          }
        }
      }
    }
  }
  return [...strips.values()];
}

export function comicActivityContext(
  ref: string,
  context: SessionActivityContext | null,
  entities: ReadonlyMap<string, EntityView>,
): ComicProjectionContext {
  const result: ComicProjectionContext = { entities };
  if (!context?.ancestryKnown || context.ref !== ref || !context.parentRef || !context.delegateId) return result;
  const parentIndex = context.ancestors.length - 1;
  const parent = context.ancestors[parentIndex];
  if (parent?.ref !== context.parentRef || context.ancestors[0]?.ref !== context.rootRef) return result;
  result.assistant = comicDelegatePersona(context.parentRef, context.delegateId, context.delegateName);
  const grandparent = context.ancestors[parentIndex - 1];
  if (parent.delegateId && grandparent)
    result.parent = comicDelegatePersona(grandparent.ref, parent.delegateId, parent.delegateName);
  else if (parent.ref === context.rootRef && !parent.delegateId)
    result.parent = { key: "assistant", character: "xeno", name: "Xeno", badge: "assistant" };
  return result;
}
