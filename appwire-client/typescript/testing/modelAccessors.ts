// Test-only accessors for reaching a specific turn/item in a ThreadModel
// without non-null assertions at every call site. Shared by the reducer and
// chunkview suites, which both fold deltas and read the resulting item.
import type { ItemModel, ThreadModel, TurnModel } from "../model";

export function turnAt(model: ThreadModel, index: number): TurnModel {
  const turn = model.turns[index];
  if (!turn) throw new Error(`expected a turn at index ${index}`);
  return turn;
}

export function itemAt(turn: TurnModel, index: number): ItemModel {
  const item = turn.items[index];
  if (!item) throw new Error(`expected an item at index ${index}`);
  return item;
}
