import type { ConceptId } from "../core/model";

export interface ConceptMetadata {
  name: string;
  number: "01" | "02" | "03";
  family: string;
  thesis: string;
  accent: string;
}

export const conceptMetadata = {
  stillwater: {
    name: "Stillwater",
    number: "01",
    family: "Quiet Instrument",
    thesis: "A calm, exact tool that disappears behind the work.",
    accent: "spruce",
  },
  constellation: {
    name: "Constellation",
    number: "02",
    family: "Living System",
    thesis: "See the shape of the work, not just its log.",
    accent: "luminous mint",
  },
  "field-notes": {
    name: "Field Notes",
    number: "03",
    family: "Transcript Studio",
    thesis: "Treat every session as a durable, readable record of work.",
    accent: "rust",
  },
} as const satisfies Record<ConceptId, ConceptMetadata>;
