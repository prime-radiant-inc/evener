// The item-renderer registry: one component per ThreadItem.type (the wire
// vocabulary - "userMessage"/"agentMessage"/"reasoning"/"commandExecution"/
// "systemMessage", confirmed against internal/appprojector/
// appwire_projection.go). Wave 4 T1 ships the registry + the raw fallback;
// T2 registers the real message renderers (agentMessage/userMessage/
// steering/system/reasoning), T1 itself registers ONLY "commandExecution"
// (ToolCallItem, which dispatches into toolRenderers.ts).
import type { ComponentType } from "react";
import type { ItemModel, TurnModel } from "../../../protocol/model";
import { RawItemView } from "./RawItemView";

export interface ItemRenderProps {
  item: ItemModel;
  turn: TurnModel;
  live: boolean;
}

const registry = new Map<string, ComponentType<ItemRenderProps>>();

export function registerItemRenderer(type: string, c: ComponentType<ItemRenderProps>): void {
  registry.set(type, c);
}

// itemRendererFor mirrors paneRegistry.ts's paneFor naming, but - unlike
// paneFor, which throws on a miss - always resolves to something
// renderable: an unregistered type is an everyday, expected case here (most
// item types have no dedicated renderer until T2/T3 land), not a bug.
export function itemRendererFor(type: string): ComponentType<ItemRenderProps> {
  return registry.get(type) ?? RawItemView;
}

// A React.memo comparator for an item renderer that reads nothing off
// ItemRenderProps.turn. Streaming deltas rebuild the enclosing TurnModel on
// every delta (reducer.ts's `item/agentMessage/delta` case spreads a new
// turn object), while an unchanged item keeps its exact object reference
// (reducer.ts's immutable-update discipline only replaces the item that
// actually changed) - so a renderer wrapped with `memo(Component,
// ignoringTurn)` skips re-rendering whenever `item` (by reference) and
// `live` (by value) are unchanged, even though `turn` is a fresh object
// every time. This is what stops an already-settled sibling item from
// re-rendering once per delta targeting a DIFFERENT, live item in the same
// turn (T5b's render-count probe: 500 deltas, 500 wasted re-renders).
//
// ONLY correct for a renderer that truly ignores `turn` - see each
// registerItemRenderer call site using this for why it qualifies (grepped:
// every registered renderer destructures `{ item, live }` and never reads
// `turn`, EXCEPT SystemNoticeItem, which reads turn.items for
// systemGrouping.ts's consecutive-run detection and deliberately does NOT
// use this comparator - see that file's own comment). A renderer that
// starts depending on a per-delta turn field would go stale silently if
// wrapped with this; don't add it to a new renderer without first checking
// what it reads off `turn`.
export function ignoringTurn(prev: ItemRenderProps, next: ItemRenderProps): boolean {
  return prev.item === next.item && prev.live === next.live;
}
