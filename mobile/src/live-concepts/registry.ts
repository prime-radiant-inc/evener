import { constellationModule } from "./constellation";
import type { LiveConceptModule } from "./contract";
import { fieldNotesModule } from "./field-notes";
import type { ConceptId } from "./model";
import { stillwaterModule } from "./stillwater";

export const liveConceptRegistry = {
  stillwater: stillwaterModule,
  constellation: constellationModule,
  "field-notes": fieldNotesModule,
} as const satisfies Record<ConceptId, LiveConceptModule>;
