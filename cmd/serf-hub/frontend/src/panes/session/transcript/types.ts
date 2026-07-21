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
