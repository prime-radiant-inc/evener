// memoryContextWireFixtures reads the daemon's automatic memory refreshes as
// the hub sends them, from agent/testdata/memorycontextwire/events.json. The
// agent's TestMemoryContextWireFixtures produces the fixture from the same
// appendMemoryContext the live projector and reload share, and re-verifies
// it on every Go test run; regenerate it with
// `go test ./agent -run 'TestMemoryContextWireFixtures$' -count=1 -update-wire`.
// It is loaded through a `?raw` import, as systemEventWireFixtures loads its own.
//
// Each item is the frozen wire contract: type systemMessage, eventKind
// "memory-context", id item_memory_context_<index> (suffixed _<n> for a
// message's later sections), Text the exact recorded section, and
// raw.memoryContext present only on an index section (change and page
// sections and the malformed case carry none, so the renderer falls back to
// the Text).

import memoryContextEvents from "../../../agent/testdata/memorycontextwire/events.json?raw";
import type { ThreadItem } from "../types.gen";

const FIXTURE_PATH = "agent/testdata/memorycontextwire/events.json";

/** The recorded case names, in the order the fixture lists them. */
export type MemoryContextWireCase =
  | "current-personal"
  | "current-project"
  | "empty-project"
  | "missing-project"
  | "revoked-project"
  | "unavailable-project"
  | "truncated-project"
  | "quoted-project"
  | "malformed-project"
  | "index-change-personal"
  | "index-change-project"
  | "page-notice-project";

interface MemoryContextWireFixture {
  case: MemoryContextWireCase;
  note: string;
  item: ThreadItem;
}

const fixture = (): MemoryContextWireFixture[] => JSON.parse(memoryContextEvents) as MemoryContextWireFixture[];

/** memoryContextWireItem returns the item the daemon sends for one recorded case. */
export function memoryContextWireItem(name: MemoryContextWireCase): ThreadItem {
  const record = fixture().find((rec) => rec.case === name);
  if (!record) throw new Error(`no ${name} case in ${FIXTURE_PATH}`);
  return record.item;
}

/** memoryContextWireItems returns every recorded item, in fixture order. */
export function memoryContextWireItems(): ThreadItem[] {
  return fixture().map((rec) => rec.item);
}

/** memoryContextWireCases returns every recorded case name, in fixture order. */
export function memoryContextWireCases(): MemoryContextWireCase[] {
  return fixture().map((rec) => rec.case);
}
