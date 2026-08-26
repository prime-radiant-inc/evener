import type { ConceptId } from "../core/model";
import { constellationModule } from "./constellation";
import type { ConceptModule } from "./contract";
import { fieldNotesModule } from "./field-notes";
import { stillwaterModule } from "./stillwater";

export const conceptRegistry = {
  stillwater: stillwaterModule,
  constellation: constellationModule,
  "field-notes": fieldNotesModule,
} as const satisfies Record<ConceptId, ConceptModule>;
